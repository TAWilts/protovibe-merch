"""One-time importer for the supplied legacy Calc/ODS table.

Usage inside the deployed container (after placing the file in ``imports/``):

    docker compose exec merch python scripts/import_ods.py /import/merch.ods

The importer intentionally reads the original input columns, not Calc's derived
columns.  In particular, it avoids copying a cached formula result if a legacy
formula accidentally references a wrong row.  Run it only into an empty app
database; this protects against accidentally importing the same history twice.
"""

from __future__ import annotations

import json
import os
import sqlite3
import sys
from getpass import getpass
from collections import defaultdict
from datetime import datetime
from pathlib import Path
from typing import Any
from xml.etree import ElementTree as ET
from zipfile import ZipFile

# Python normally puts ``scripts/`` rather than the repository root on
# ``sys.path`` when this file is invoked directly.  Keep both documented forms
# working: ``python scripts/import_ods.py …`` and ``python -m scripts.import_ods
# …``.  The latter was previously required on some local VS Code setups.
REPOSITORY_ROOT = Path(__file__).resolve().parents[1]
if str(REPOSITORY_ROOT) not in sys.path:
    sys.path.insert(0, str(REPOSITORY_ROOT))

# The command is run from /app in the Docker image, where app.py lives.
from app import (
    apply_option_configuration,
    csv_bytes,
    csv_rows,
    database_encryption_enabled,
    db_connect,
    load_database_encryption_metadata,
    money_to_cents,
    _unwrap_database_key,
    sorted_combination_key,
    sync_variants,
    utc_now,
)
from flask import Flask


TABLE_NS = "urn:oasis:names:tc:opendocument:xmlns:table:1.0"
OFFICE_NS = "urn:oasis:names:tc:opendocument:xmlns:office:1.0"
TABLE = f"{{{TABLE_NS}}}"
OFFICE = f"{{{OFFICE_NS}}}"


NORMALISED_ARTICLE_COLUMNS = {
    "Varianten-ID",
    "Artikel",
    "Standard-Einkaufspreis",
    "Standard-Verkaufspreis",
    "Mindestbestand",
    "Angeboten",
}


def read_ods(path: Path) -> dict[str, list[list[dict[str, str | None]]]]:
    """Read the ODS cells we need with only Python's standard library."""

    with ZipFile(path) as archive:
        root = ET.fromstring(archive.read("content.xml"))
    output: dict[str, list[list[dict[str, str | None]]]] = {}
    for table in root.findall(f".//{TABLE}table"):
        name = table.get(f"{TABLE}name")
        rows = []
        for row in table.findall(f"{TABLE}table-row"):
            values = []
            for cell in row:
                if cell.tag not in (f"{TABLE}table-cell", f"{TABLE}covered-table-cell"):
                    continue
                repeat = int(cell.get(f"{TABLE}number-columns-repeated", "1"))
                data = {
                    "text": "".join(cell.itertext()).strip(),
                    "value": cell.get(f"{OFFICE}value"),
                    "date": cell.get(f"{OFFICE}date-value"),
                }
                values.extend([data] * repeat)
            rows.append(values)
        if name:
            output[name] = rows
    return output


def find_records(rows: list[list[dict[str, str | None]]], first_header: str) -> tuple[dict[str, int], list[list[dict[str, str | None]]]]:
    """Find a named header row and return only following non-empty records."""

    for index, row in enumerate(rows):
        headers = [cell["text"] or "" for cell in row]
        if first_header in headers:
            mapping = {header: position for position, header in enumerate(headers) if header}
            return mapping, [record for record in rows[index + 1 :] if record and record[0]["text"]]
    raise ValueError(f"Die Spalte „{first_header}“ wurde nicht gefunden.")


def cell_text(row: list[dict[str, str | None]], index: int | None) -> str:
    return "" if index is None or index >= len(row) else str(row[index]["text"] or "")


def cell_number(row: list[dict[str, str | None]], index: int | None, name: str) -> int:
    if index is None or index >= len(row):
        return 0
    return money_to_cents(row[index]["value"] or row[index]["text"], field_name=name)


def cell_quantity(row: list[dict[str, str | None]], index: int | None) -> int:
    if index is None or index >= len(row):
        return 0
    value = row[index]["value"] or row[index]["text"] or "0"
    return int(float(str(value).replace(",", ".")))


def cell_optional_quantity(row: list[dict[str, str | None]], index: int | None, name: str) -> int | None:
    """Read an optional non-negative whole-number field such as minimum stock."""

    if index is None or index >= len(row):
        return None
    value = row[index]["value"] or row[index]["text"] or ""
    if not str(value).strip():
        return None
    try:
        quantity = int(float(str(value).replace(",", ".")))
    except ValueError as error:
        raise ValueError(f"{name} „{value}“ muss eine ganze Zahl sein.") from error
    if quantity < 0:
        raise ValueError(f"{name} darf nicht negativ sein.")
    return quantity


def cell_bool(
    row: list[dict[str, str | None]], index: int | None, name: str, *, default: bool = True
) -> bool:
    """Read a human-readable yes/no value from an ODS input cell."""

    if index is None or index >= len(row):
        return default
    value = str(row[index]["text"] or row[index]["value"] or "").strip().casefold()
    if not value:
        return default
    if value in {"ja", "j", "yes", "y", "true", "1", "x"}:
        return True
    if value in {"nein", "n", "no", "false", "0"}:
        return False
    raise ValueError(f"{name} „{value}“ muss Ja oder Nein sein.")


def normalise_date(row: list[dict[str, str | None]], index: int | None) -> str:
    """Return an ISO date from Calc's typed date or its displayed German text."""

    if index is None or index >= len(row):
        return datetime.now().date().isoformat()
    raw = row[index]["date"]
    if raw:
        return raw[:10]
    text = cell_text(row, index)
    for format_string in ("%d.%m.%Y", "%d.%m.%y", "%Y-%m-%d"):
        try:
            return datetime.strptime(text, format_string).date().isoformat()
        except ValueError:
            continue
    raise ValueError(f"Datum „{text}“ konnte nicht gelesen werden.")


def ensure_empty_database(connection: sqlite3.Connection) -> None:
    tables = ("articles", "purchases", "sales")
    used = {table: connection.execute(f"SELECT COUNT(*) FROM {table}").fetchone()[0] for table in tables}
    if any(used.values()):
        details = ", ".join(f"{name}: {count}" for name, count in used.items())
        raise RuntimeError(f"Der Import braucht eine leere Datenbank ({details}).")


def create_initial_backup(connection: sqlite3.Connection, database: Path, *, app: Flask | None = None) -> None:
    """Snapshot the freshly imported state before any later manual changes."""

    backup_dir = database.parent / "backups" / f"initial-import-{datetime.now().strftime('%Y-%m-%d_%H-%M-%S')}"
    backup_dir.mkdir(parents=True)
    destination = db_connect(backup_dir / "merch.sqlite3", app=app)
    try:
        connection.backup(destination)
    finally:
        destination.close()
    # CSV files would be unencrypted. Keep them as deliberate browser exports
    # instead of silently putting them next to an encrypted backup.
    if not database_encryption_enabled(app):
        for kind in ("articles", "sales", "purchases", "inventory"):
            filename, headers, rows = csv_rows(connection, kind)
            (backup_dir / f"{filename}.csv").write_bytes(csv_bytes(headers, rows))


def has_header(rows: list[list[dict[str, str | None]]], header: str) -> bool:
    """Return whether an ODS table contains a named header cell."""

    return any(header in [cell["text"] or "" for cell in row] for row in rows)


def import_normalised_sheets(
    sheets: dict[str, list[list[dict[str, str | None]]]], database: Path, *, app: Flask | None = None
) -> None:
    """Import the cleaned ODS with explicit variant IDs and dynamic options.

    The normalised format contains one row per actually existing variant.  The
    app itself creates the Cartesian product required by its option model; any
    combinations absent from the ODS are therefore retained but marked as not
    offered.  This keeps the sales screen clean without deleting future option
    combinations from the article administration.
    """

    article_headers, article_rows = find_records(sheets["Artikel"], "Varianten-ID")
    purchase_headers, purchase_rows = find_records(sheets["Einkäufe"], "Varianten-ID")
    sale_headers, sale_rows = find_records(sheets["Verkäufe"], "Varianten-ID")
    for required in ("Artikel", "Standard-Einkaufspreis", "Standard-Verkaufspreis"):
        if required not in article_headers:
            raise ValueError(f"Die Spalte „{required}“ fehlt in Artikel.")

    option_names = [header for header in article_headers if header not in NORMALISED_ARTICLE_COLUMNS]
    entries_by_article: dict[str, list[dict[str, Any]]] = defaultdict(list)
    seen_variant_keys: set[str] = set()
    for row_number, row in enumerate(article_rows, start=1):
        source_variant_key = cell_text(row, article_headers.get("Varianten-ID"))
        article_name = cell_text(row, article_headers.get("Artikel"))
        if not source_variant_key:
            raise ValueError(f"Artikelzeile {row_number} enthält keine Varianten-ID.")
        if source_variant_key in seen_variant_keys:
            raise ValueError(f"Die Varianten-ID „{source_variant_key}“ kommt mehrfach vor.")
        if not article_name:
            raise ValueError(f"Artikelzeile {row_number} enthält keinen Artikelnamen.")
        seen_variant_keys.add(source_variant_key)
        entries_by_article[article_name].append(
            {
                "source_variant_key": source_variant_key,
                "options": {
                    option_name: cell_text(row, article_headers.get(option_name))
                    for option_name in option_names
                    if cell_text(row, article_headers.get(option_name))
                },
                "cost_cents": cell_number(
                    row, article_headers.get("Standard-Einkaufspreis"), "Standard-Einkaufspreis"
                ),
                "price_cents": cell_number(
                    row, article_headers.get("Standard-Verkaufspreis"), "Standard-Verkaufspreis"
                ),
                "minimum_stock": cell_optional_quantity(
                    row, article_headers.get("Mindestbestand"), "Mindestbestand"
                ),
                "is_offered": cell_bool(row, article_headers.get("Angeboten"), "Angeboten"),
            }
        )

    connection = db_connect(database, app=app)
    try:
        ensure_empty_database(connection)
        connection.execute("BEGIN IMMEDIATE")
        now = utc_now()
        variant_ids: dict[str, int] = {}

        for article_name, entries in entries_by_article.items():
            default_cost = entries[0]["cost_cents"]
            default_price = entries[0]["price_cents"]
            article_is_offered = int(any(entry["is_offered"] for entry in entries))
            cursor = connection.execute(
                """
                INSERT INTO articles (
                    name, default_sale_price_cents, default_purchase_price_cents, is_offered, is_active,
                    created_at, updated_at
                ) VALUES (?, ?, ?, ?, 1, ?, ?)
                """,
                (article_name, default_price, default_cost, article_is_offered, now, now),
            )
            article_id = cursor.lastrowid

            config = []
            for position, option_name in enumerate(option_names):
                values: list[str] = []
                for entry in entries:
                    value = entry["options"].get(option_name)
                    if value and value not in values:
                        values.append(value)
                if values:
                    config.append(
                        {
                            "id": None,
                            "name": option_name,
                            "position": position,
                            "values": [
                                {"id": None, "value": value, "position": value_position}
                                for value_position, value in enumerate(values)
                            ],
                        }
                    )
            apply_option_configuration(connection, article_id, config)
            sync_variants(connection, article_id)

            option_value_ids: dict[str, dict[str, int]] = defaultdict(dict)
            for option_row in connection.execute(
                """
                SELECT og.name, ov.value, ov.id
                FROM option_values ov
                JOIN option_groups og ON og.id = ov.option_group_id
                WHERE og.article_id = ? AND og.is_active = 1 AND ov.is_active = 1
                """,
                (article_id,),
            ).fetchall():
                option_value_ids[option_row["name"]][option_row["value"]] = option_row["id"]
            variants_by_key = {
                variant_row["combination_key"]: variant_row["id"]
                for variant_row in connection.execute(
                    "SELECT id, combination_key FROM variants WHERE article_id = ?", (article_id,)
                ).fetchall()
            }

            # Every combination created by sync_variants starts as sellable.
            # Only the explicitly listed source variants belong to the
            # assortment; all other generated combinations stay in the model
            # but are hidden from the sales window.
            connection.execute(
                "UPDATE variants SET is_offered = 0, updated_at = ? WHERE article_id = ?",
                (now, article_id),
            )
            for entry in entries:
                value_ids = [
                    option_value_ids[option_name][value]
                    for option_name, value in entry["options"].items()
                ]
                key = sorted_combination_key(value_ids)
                variant_id = variants_by_key.get(key)
                if variant_id is None:
                    raise RuntimeError(
                        f"Die Varianten-ID „{entry['source_variant_key']}“ konnte nicht erzeugt werden."
                    )
                connection.execute(
                    """
                    UPDATE variants
                    SET sale_price_cents = ?, default_purchase_price_cents = ?, minimum_stock = ?,
                        is_offered = ?, updated_at = ?
                    WHERE id = ?
                    """,
                    (
                        entry["price_cents"],
                        entry["cost_cents"],
                        entry["minimum_stock"],
                        int(entry["is_offered"]),
                        now,
                        variant_id,
                    ),
                )
                variant_ids[entry["source_variant_key"]] = variant_id

        # The source sheets list purchase positions line by line.  A date is
        # the only reliable common receipt marker in the legacy data, so all
        # purchases from one day become one multi-item cart on import.  This
        # intentionally supersedes any old per-line Einkauf-ID.
        imported_purchases = 0
        purchase_receipts_by_date: dict[str, str] = {}
        for row_number, row in enumerate(purchase_rows, start=1):
            source_variant_key = cell_text(row, purchase_headers.get("Varianten-ID"))
            variant_id = variant_ids.get(source_variant_key)
            if not variant_id:
                raise ValueError(f"Einkauf {row_number} hat eine unbekannte Varianten-ID: {source_variant_key}")
            quantity = cell_quantity(row, purchase_headers.get("Stück"))
            if quantity <= 0:
                continue
            purchased_on = normalise_date(row, purchase_headers.get("Datum"))
            receipt_id = purchase_receipts_by_date.setdefault(
                purchased_on, f"IMPORT-E-{purchased_on.replace('-', '')}"
            )
            invoice_reference = (
                cell_text(row, purchase_headers.get("Rechnungsnummer/Name"))
                or cell_text(row, purchase_headers.get("Rechnungsnummer/Name (Dateipfad auf dem Server)"))
                or None
            )
            connection.execute(
                """
                INSERT INTO purchases (
                    receipt_id, variant_id, quantity, unit_cost_cents, purchased_on, supplier,
                    invoice_reference, comment, created_at, created_by
                ) VALUES (?, ?, ?, ?, ?, NULL, ?, ?, ?, NULL)
                """,
                (
                    receipt_id,
                    variant_id,
                    quantity,
                    cell_number(row, purchase_headers.get("Preis/Stück"), "Preis/Stück"),
                    purchased_on,
                    invoice_reference,
                    cell_text(row, purchase_headers.get("Kommentar")) or None,
                    now,
                ),
            )
            imported_purchases += 1

        imported_sales = 0
        for row_number, row in enumerate(sale_rows, start=1):
            source_variant_key = cell_text(row, sale_headers.get("Varianten-ID"))
            variant_id = variant_ids.get(source_variant_key)
            if not variant_id:
                raise ValueError(f"Verkauf {row_number} hat eine unbekannte Varianten-ID: {source_variant_key}")
            quantity = cell_quantity(row, sale_headers.get("Stück"))
            if quantity <= 0:
                continue
            unit_price = cell_number(row, sale_headers.get("Verkaufspreis/Stück"), "Verkaufspreis/Stück")
            receipt_id = cell_text(row, sale_headers.get("Beleg-ID")) or f"IMPORT-V-{row_number:04d}"
            connection.execute(
                """
                INSERT INTO sales (
                    receipt_id, variant_id, quantity, unit_price_cents, amount_due_cents,
                    amount_given_cents, donation_cents, payment_method, is_paid, is_received,
                    delivery_status, customer_name, customer_address, event_name, comment,
                    sold_on, created_at, created_by
                ) VALUES (?, ?, ?, ?, ?, NULL, 0, 'Import', 1, 1, 'not_applicable', NULL, NULL, ?, NULL, ?, ?, NULL)
                """,
                (
                    receipt_id,
                    variant_id,
                    quantity,
                    unit_price,
                    quantity * unit_price,
                    cell_text(row, sale_headers.get("Kommentar")) or None,
                    normalise_date(row, sale_headers.get("Datum")),
                    now,
                ),
            )
            imported_sales += 1

        connection.commit()
        create_initial_backup(connection, database, app=app)
        print(
            f"Import abgeschlossen: {len(entries_by_article)} Artikel, "
            f"{imported_purchases} Einkaufspositionen in {len(purchase_receipts_by_date)} Warenkörben, "
            f"{imported_sales} Verkäufe."
        )
    except Exception:
        connection.rollback()
        raise
    finally:
        connection.close()


def import_file(path: Path, database: Path, *, app: Flask | None = None) -> None:
    sheets = read_ods(path)
    if has_header(sheets.get("Artikel", []), "Varianten-ID"):
        import_normalised_sheets(sheets, database, app=app)
        return
    article_headers, article_rows = find_records(sheets["Artikel"], "Name")
    purchase_headers, purchase_rows = find_records(sheets["Einkäufe"], "Artikel")
    sale_headers, sale_rows = find_records(sheets["Verkäufe"], "Artikel")
    connection = db_connect(database, app=app)
    try:
        ensure_empty_database(connection)
        connection.execute("BEGIN IMMEDIATE")
        now = utc_now()

        # Collect article variants first.  The legacy table models one option
        # (Größe) explicitly; other article attributes remain part of the name.
        entries_by_article: dict[str, list[dict[str, Any]]] = defaultdict(list)
        for row in article_rows:
            variant_name = cell_text(row, article_headers.get("Name"))
            article_name = cell_text(row, article_headers.get("Art")) or variant_name
            size = cell_text(row, article_headers.get("Größe"))
            entries_by_article[article_name].append(
                {
                    "legacy_name": variant_name,
                    "size": size,
                    "cost_cents": cell_number(row, article_headers.get("Kosten"), "Kosten"),
                    "price_cents": cell_number(row, article_headers.get("Preis"), "Preis"),
                }
            )

        variant_ids: dict[str, int] = {}
        for article_name, entries in entries_by_article.items():
            default_cost = entries[0]["cost_cents"]
            default_price = entries[0]["price_cents"]
            cursor = connection.execute(
                """
                INSERT INTO articles (
                    name, default_sale_price_cents, default_purchase_price_cents, is_active, created_at, updated_at
                ) VALUES (?, ?, ?, 1, ?, ?)
                """,
                (article_name, default_price, default_cost, now, now),
            )
            article_id = cursor.lastrowid
            sizes = []
            for entry in entries:
                if entry["size"] and entry["size"] not in sizes:
                    sizes.append(entry["size"])
            config = []
            if sizes:
                config = [{"id": None, "name": "Größe", "position": 0, "values": [
                    {"id": None, "value": size, "position": position} for position, size in enumerate(sizes)
                ]}]
            apply_option_configuration(connection, article_id, config)
            sync_variants(connection, article_id)

            variants = connection.execute(
                "SELECT id, combination_key FROM variants WHERE article_id = ?", (article_id,)
            ).fetchall()
            variants_by_key = {row["combination_key"]: row["id"] for row in variants}
            size_value_ids: dict[str, int] = {}
            if sizes:
                for row in connection.execute(
                    """
                    SELECT ov.id, ov.value FROM option_values ov
                    JOIN option_groups og ON og.id = ov.option_group_id
                    WHERE og.article_id = ? AND og.name = 'Größe'
                    """,
                    (article_id,),
                ).fetchall():
                    size_value_ids[row["value"]] = row["id"]
            for entry in entries:
                key = str(size_value_ids[entry["size"]]) if entry["size"] else ""
                variant_id = variants_by_key[key]
                connection.execute(
                    """
                    UPDATE variants SET sale_price_cents = ?, default_purchase_price_cents = ?, updated_at = ?
                    WHERE id = ?
                    """,
                    (entry["price_cents"], entry["cost_cents"], now, variant_id),
                )
                variant_ids[entry["legacy_name"]] = variant_id

        def known_variant(row: list[dict[str, str | None]]) -> int | None:
            return variant_ids.get(cell_text(row, purchase_headers.get("Artikel")))

        imported_purchases = 0
        purchase_receipts_by_date: dict[str, str] = {}
        for row_number, row in enumerate(purchase_rows, start=1):
            legacy_name = cell_text(row, purchase_headers.get("Artikel"))
            variant_id = variant_ids.get(legacy_name)
            if not variant_id:
                print(f"WARNUNG: Einkauf {row_number} für unbekannten Artikel übersprungen: {legacy_name}", file=sys.stderr)
                continue
            quantity = cell_quantity(row, purchase_headers.get("Stück"))
            if quantity <= 0:
                continue
            purchased_on = normalise_date(row, purchase_headers.get("Datum"))
            receipt_id = purchase_receipts_by_date.setdefault(
                purchased_on, f"IMPORT-E-{purchased_on.replace('-', '')}"
            )
            connection.execute(
                """
                INSERT INTO purchases (
                    receipt_id, variant_id, quantity, unit_cost_cents, purchased_on, supplier,
                    invoice_reference, comment, created_at, created_by
                ) VALUES (?, ?, ?, ?, ?, NULL, ?, ?, ?, NULL)
                """,
                (
                    receipt_id, variant_id, quantity,
                    cell_number(row, purchase_headers.get("Preis/Stück"), "Preis/Stück"),
                    purchased_on,
                    cell_text(row, purchase_headers.get("Rechnungsnummer/Name (Dateipfad auf dem Server)")) or None,
                    cell_text(row, purchase_headers.get("Kommentar")) or None, now,
                ),
            )
            imported_purchases += 1

        imported_sales = 0
        for row_number, row in enumerate(sale_rows, start=1):
            legacy_name = cell_text(row, sale_headers.get("Artikel"))
            variant_id = variant_ids.get(legacy_name)
            if not variant_id:
                print(f"WARNUNG: Verkauf {row_number} für unbekannten Artikel übersprungen: {legacy_name}", file=sys.stderr)
                continue
            quantity = cell_quantity(row, sale_headers.get("Stück"))
            if quantity <= 0:
                continue
            unit_price = cell_number(row, sale_headers.get("Verkaufspreis/Stück"), "Verkaufspreis/Stück")
            connection.execute(
                """
                INSERT INTO sales (
                    receipt_id, variant_id, quantity, unit_price_cents, amount_due_cents,
                    amount_given_cents, donation_cents, payment_method, is_paid, is_received,
                    delivery_status, customer_name, customer_address, event_name, comment,
                    sold_on, created_at, created_by
                ) VALUES (?, ?, ?, ?, ?, NULL, 0, 'Import', 1, 1, 'not_applicable', NULL, NULL, ?, NULL, ?, ?, NULL)
                """,
                (
                    f"IMPORT-V-{row_number:04d}", variant_id, quantity, unit_price, quantity * unit_price,
                    cell_text(row, sale_headers.get("Kommentar")) or None,
                    normalise_date(row, sale_headers.get("Datum")), now,
                ),
            )
            imported_sales += 1

        connection.commit()
        create_initial_backup(connection, database, app=app)
        print(
            f"Import abgeschlossen: {len(entries_by_article)} Artikel, "
            f"{imported_purchases} Einkaufspositionen in {len(purchase_receipts_by_date)} Warenkörben, "
            f"{imported_sales} Verkäufe."
        )
    except Exception:
        connection.rollback()
        raise
    finally:
        connection.close()


def encrypted_import_context(database: Path) -> Flask:
    """Build the minimal in-memory context needed for a one-off ODS import.

    The unlock passphrase is read interactively, never from the command line
    or .env.  The normal web application is deliberately not started here.
    """

    context = Flask("protovibe-ods-import")
    data_dir = database.parent
    context.config.from_mapping(
        DATABASE=str(database),
        USERS_DATABASE=str(data_dir / "users.sqlite3"),
        DATABASE_ENCRYPTION_ENABLED=True,
        DATABASE_ENCRYPTION_METADATA=str(data_dir / "encryption.json"),
        BACKUP_DIR=str(data_dir / "backups"),
    )
    metadata = load_database_encryption_metadata(context)
    if metadata is None or not metadata.get("databases_ready"):
        raise SystemExit(
            "Die Verschlüsselung ist noch nicht vollständig eingerichtet. Öffne zuerst die Weboberfläche."
        )
    passphrase = getpass("Datenbank-Passphrase: ")
    try:
        context.extensions["database_encryption_key"] = _unwrap_database_key(metadata["passphrase"], passphrase)
    except (ValueError, KeyError):
        raise SystemExit("Die Datenbank-Passphrase ist nicht korrekt.")
    return context


def main() -> None:
    if len(sys.argv) != 2:
        raise SystemExit("Aufruf: python scripts/import_ods.py /import/deine-merch-tabelle.ods")
    source = Path(sys.argv[1])
    if not source.is_file():
        raise SystemExit(f"Nicht gefunden: {source}")
    # Docker provides DATA_DIR=/data. Direct local invocations use app.py's
    # repository-local data/ directory without extra environment variables.
    data_dir = Path(os.environ.get("DATA_DIR", REPOSITORY_ROOT / "data"))
    database = data_dir / "merch.sqlite3"
    if not database.is_file():
        raise SystemExit("Die Anwendung muss einmal gestartet sein, bevor der Import ausgeführt wird.")
    context = encrypted_import_context(database) if (data_dir / "encryption.json").is_file() else None
    import_file(source, database, app=context)


if __name__ == "__main__":
    main()
