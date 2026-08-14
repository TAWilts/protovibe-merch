"""Protovibe Merch Manager.

This module contains the complete application intentionally in one well-separated
file for the first version.  It makes it easy to inspect and change on a NAS:

* SQLite is the single source of truth.  CSV files are exports/backups, never the
  live database.
* Articles consist of arbitrary option groups (e.g. colour and size).
* A sale or purchase always points to a concrete variant.  Historic records keep
  this relationship, so renamed option values are reflected retroactively.
* Removing an option only deactivates it.  Nothing referenced by historic sales
  is deleted, which protects the accounting history.

The app is deliberately designed for a small band inventory, not for a public
shop.  Keep it inside the home network or behind a VPN; do not expose port 8088
directly to the public internet.
"""

from __future__ import annotations

import csv
import io
import itertools
import json
import os
import re
import shutil
import sqlite3
import tempfile
from collections import defaultdict
from datetime import date, datetime, timezone
from decimal import Decimal, InvalidOperation, ROUND_HALF_UP
from functools import wraps
from pathlib import Path
from typing import Any, Iterable
from zipfile import ZIP_DEFLATED, ZipFile

from flask import (
    Flask,
    Response,
    abort,
    current_app,
    flash,
    g,
    jsonify,
    redirect,
    render_template,
    request,
    send_file,
    session,
    url_for,
)
from werkzeug.security import check_password_hash, generate_password_hash


SCHEMA_SQL = """
PRAGMA foreign_keys = ON;

CREATE TABLE IF NOT EXISTS users (
    id INTEGER PRIMARY KEY,
    username TEXT NOT NULL UNIQUE COLLATE NOCASE,
    password_hash TEXT NOT NULL,
    is_admin INTEGER NOT NULL DEFAULT 1,
    created_at TEXT NOT NULL
);

CREATE TABLE IF NOT EXISTS articles (
    id INTEGER PRIMARY KEY,
    name TEXT NOT NULL COLLATE NOCASE UNIQUE,
    default_sale_price_cents INTEGER NOT NULL DEFAULT 0 CHECK(default_sale_price_cents >= 0),
    default_purchase_price_cents INTEGER NOT NULL DEFAULT 0 CHECK(default_purchase_price_cents >= 0),
    is_active INTEGER NOT NULL DEFAULT 1,
    created_at TEXT NOT NULL,
    updated_at TEXT NOT NULL
);

CREATE TABLE IF NOT EXISTS option_groups (
    id INTEGER PRIMARY KEY,
    article_id INTEGER NOT NULL REFERENCES articles(id),
    name TEXT NOT NULL,
    position INTEGER NOT NULL DEFAULT 0,
    is_active INTEGER NOT NULL DEFAULT 1,
    created_at TEXT NOT NULL,
    updated_at TEXT NOT NULL
);

CREATE TABLE IF NOT EXISTS option_values (
    id INTEGER PRIMARY KEY,
    option_group_id INTEGER NOT NULL REFERENCES option_groups(id),
    value TEXT NOT NULL,
    position INTEGER NOT NULL DEFAULT 0,
    is_active INTEGER NOT NULL DEFAULT 1,
    created_at TEXT NOT NULL,
    updated_at TEXT NOT NULL
);

CREATE TABLE IF NOT EXISTS variants (
    id INTEGER PRIMARY KEY,
    article_id INTEGER NOT NULL REFERENCES articles(id),
    option_value_ids_json TEXT NOT NULL DEFAULT '[]',
    combination_key TEXT NOT NULL,
    sale_price_cents INTEGER NOT NULL DEFAULT 0 CHECK(sale_price_cents >= 0),
    default_purchase_price_cents INTEGER NOT NULL DEFAULT 0 CHECK(default_purchase_price_cents >= 0),
    no_reorder INTEGER NOT NULL DEFAULT 0,
    is_active INTEGER NOT NULL DEFAULT 1,
    created_at TEXT NOT NULL,
    updated_at TEXT NOT NULL,
    UNIQUE(article_id, combination_key)
);

CREATE TABLE IF NOT EXISTS purchases (
    id INTEGER PRIMARY KEY,
    receipt_id TEXT NOT NULL UNIQUE,
    variant_id INTEGER NOT NULL REFERENCES variants(id),
    quantity INTEGER NOT NULL CHECK(quantity > 0),
    unit_cost_cents INTEGER NOT NULL CHECK(unit_cost_cents >= 0),
    purchased_on TEXT NOT NULL,
    supplier TEXT,
    invoice_reference TEXT,
    comment TEXT,
    created_at TEXT NOT NULL,
    created_by INTEGER REFERENCES users(id)
);

CREATE TABLE IF NOT EXISTS sales (
    id INTEGER PRIMARY KEY,
    receipt_id TEXT NOT NULL UNIQUE,
    variant_id INTEGER NOT NULL REFERENCES variants(id),
    quantity INTEGER NOT NULL CHECK(quantity > 0),
    unit_price_cents INTEGER NOT NULL CHECK(unit_price_cents >= 0),
    amount_due_cents INTEGER NOT NULL CHECK(amount_due_cents >= 0),
    amount_given_cents INTEGER,
    donation_cents INTEGER NOT NULL DEFAULT 0 CHECK(donation_cents >= 0),
    payment_method TEXT NOT NULL,
    is_paid INTEGER NOT NULL DEFAULT 1,
    is_received INTEGER NOT NULL DEFAULT 1,
    customer_name TEXT,
    customer_address TEXT,
    event_name TEXT,
    comment TEXT,
    sold_on TEXT NOT NULL,
    created_at TEXT NOT NULL,
    created_by INTEGER REFERENCES users(id)
);

CREATE TABLE IF NOT EXISTS audit_log (
    id INTEGER PRIMARY KEY,
    created_at TEXT NOT NULL,
    user_id INTEGER REFERENCES users(id),
    action TEXT NOT NULL,
    entity_type TEXT NOT NULL,
    entity_id INTEGER,
    details_json TEXT NOT NULL DEFAULT '{}'
);

CREATE INDEX IF NOT EXISTS idx_option_groups_article ON option_groups(article_id, position);
CREATE INDEX IF NOT EXISTS idx_option_values_group ON option_values(option_group_id, position);
CREATE INDEX IF NOT EXISTS idx_variants_article ON variants(article_id, is_active);
CREATE INDEX IF NOT EXISTS idx_purchases_variant ON purchases(variant_id, purchased_on);
CREATE INDEX IF NOT EXISTS idx_sales_variant ON sales(variant_id, sold_on);
CREATE INDEX IF NOT EXISTS idx_sales_sold_on ON sales(sold_on);
"""

PAYMENT_METHODS = ["Bar", "PayPal", "Überweisung", "Karte", "Sonstiges"]


def utc_now() -> str:
    """Return an ISO timestamp without pretending that it is local time."""

    return datetime.now(timezone.utc).replace(microsecond=0).isoformat()


def today_iso() -> str:
    return date.today().isoformat()


def money_to_cents(value: Any, *, field_name: str = "Betrag") -> int:
    """Parse German or international decimal input and return integer cents.

    Money is never kept as a floating-point number.  That avoids the usual
    ``0.1 + 0.2`` issues in balances and CSV exports.
    """

    if value is None or str(value).strip() == "":
        return 0
    cleaned = str(value).strip().replace("€", "").replace(" ", "")
    # German inputs usually use a decimal comma.  If both separators occur,
    # dots are treated as thousands separators (e.g. 1.234,50).
    if "," in cleaned:
        cleaned = cleaned.replace(".", "").replace(",", ".")
    try:
        decimal_value = Decimal(cleaned)
    except InvalidOperation as exc:
        raise ValueError(f"{field_name} ist keine gültige Zahl.") from exc
    if decimal_value < 0:
        raise ValueError(f"{field_name} darf nicht negativ sein.")
    return int((decimal_value * 100).quantize(Decimal("1"), rounding=ROUND_HALF_UP))


def cents_to_money(cents: int | None) -> str:
    """Format cents for the German UI without using the process locale."""

    cents = cents or 0
    sign = "-" if cents < 0 else ""
    cents = abs(cents)
    euros, remainder = divmod(cents, 100)
    grouped = f"{euros:,}".replace(",", ".")
    return f"{sign}{grouped},{remainder:02d} €"


def parse_positive_int(value: Any, *, field_name: str = "Anzahl") -> int:
    try:
        parsed = int(value)
    except (TypeError, ValueError) as exc:
        raise ValueError(f"{field_name} muss eine ganze Zahl sein.") from exc
    if parsed <= 0:
        raise ValueError(f"{field_name} muss größer als null sein.")
    return parsed


def db_connect(path: str | Path) -> sqlite3.Connection:
    """Open a SQLite connection with the consistency settings used by the app."""

    connection = sqlite3.connect(path, detect_types=sqlite3.PARSE_DECLTYPES)
    connection.row_factory = sqlite3.Row
    connection.execute("PRAGMA foreign_keys = ON")
    connection.execute("PRAGMA journal_mode = WAL")
    connection.execute("PRAGMA busy_timeout = 5000")
    return connection


def get_db() -> sqlite3.Connection:
    """Return the current request's connection and close it after the request."""

    if "db" not in g:
        g.db = db_connect(current_app.config["DATABASE"])
    return g.db


def close_db(_: BaseException | None = None) -> None:
    connection = g.pop("db", None)
    if connection is not None:
        connection.close()


def initialise_database(app: Flask) -> None:
    """Create/update the schema and bootstrap the configured administrator."""

    database_path = Path(app.config["DATABASE"])
    database_path.parent.mkdir(parents=True, exist_ok=True)
    connection = db_connect(database_path)
    try:
        connection.executescript(SCHEMA_SQL)
        # The first released schema did not have this legacy-ODS convenience
        # flag.  Keeping this tiny migration here makes a future update safe.
        variant_columns = {row["name"] for row in connection.execute("PRAGMA table_info(variants)").fetchall()}
        if "no_reorder" not in variant_columns:
            connection.execute("ALTER TABLE variants ADD COLUMN no_reorder INTEGER NOT NULL DEFAULT 0")
        user_count = connection.execute("SELECT COUNT(*) FROM users").fetchone()[0]
        if user_count == 0:
            username = app.config["ADMIN_USERNAME"].strip()
            password = app.config["ADMIN_PASSWORD"]
            if not username or not password or password.startswith("replace-this"):
                raise RuntimeError(
                    "Set ADMIN_USERNAME and a strong ADMIN_PASSWORD in .env before starting the app."
                )
            connection.execute(
                "INSERT INTO users (username, password_hash, is_admin, created_at) VALUES (?, ?, 1, ?)",
                (username, generate_password_hash(password), utc_now()),
            )
            connection.commit()
    finally:
        connection.close()


def login_required(view):
    """Redirect unauthenticated browser requests to the login screen."""

    @wraps(view)
    def wrapped(*args, **kwargs):
        if g.get("user") is None:
            if request.path.startswith("/api/"):
                return jsonify({"ok": False, "error": "Nicht angemeldet."}), 401
            return redirect(url_for("login", next=request.path))
        return view(*args, **kwargs)

    return wrapped


def admin_required(view):
    """Keep article configuration restricted to administrators."""

    @wraps(view)
    def wrapped(*args, **kwargs):
        if g.get("user") is None or not g.user["is_admin"]:
            abort(403)
        return view(*args, **kwargs)

    return wrapped


def csrf_token() -> str:
    """Return the per-session anti-CSRF token used by forms and fetch calls."""

    import secrets

    if "csrf_token" not in session:
        session["csrf_token"] = secrets.token_urlsafe(32)
    return session["csrf_token"]


def require_csrf() -> None:
    """Validate mutation requests before anything is written to the database."""

    if request.method not in {"POST", "PUT", "PATCH", "DELETE"}:
        return
    provided = request.headers.get("X-CSRF-Token") or request.form.get("csrf_token")
    if not provided or provided != session.get("csrf_token"):
        abort(400, description="Ungültige Formular-Sicherheitskennung. Seite bitte neu laden.")


def row_to_dict(row: sqlite3.Row | None) -> dict[str, Any] | None:
    return dict(row) if row is not None else None


def audit(
    connection: sqlite3.Connection,
    action: str,
    entity_type: str,
    entity_id: int | None,
    details: dict[str, Any] | None = None,
) -> None:
    """Append a compact, human-inspectable record of an important change."""

    user_id = g.user["id"] if g.get("user") else None
    connection.execute(
        """
        INSERT INTO audit_log (created_at, user_id, action, entity_type, entity_id, details_json)
        VALUES (?, ?, ?, ?, ?, ?)
        """,
        (utc_now(), user_id, action, entity_type, entity_id, json.dumps(details or {}, ensure_ascii=False)),
    )


def sorted_combination_key(option_value_ids: Iterable[int]) -> str:
    """Provide a stable, order-independent identity for an article variant."""

    return "|".join(str(value_id) for value_id in sorted(option_value_ids))


def variant_stock_map(connection: sqlite3.Connection) -> dict[int, int]:
    """Calculate stock from the immutable purchase and sale ledger."""

    rows = connection.execute(
        """
        SELECT variant_id, SUM(stock_delta) AS stock
        FROM (
            SELECT variant_id, quantity AS stock_delta FROM purchases
            UNION ALL
            SELECT variant_id, -quantity AS stock_delta FROM sales
        )
        GROUP BY variant_id
        """
    ).fetchall()
    return {row["variant_id"]: int(row["stock"] or 0) for row in rows}


def variant_label_map(
    connection: sqlite3.Connection, variant_ids: Iterable[int] | None = None
) -> dict[int, dict[str, Any]]:
    """Build current article/option labels for variants.

    Option labels are resolved every time instead of being copied into sales.
    Therefore a confirmed rename in article administration also appears in older
    sales and purchases, exactly as requested.  Inactive/deleted option values
    stay in the database and are deliberately included for historic records.
    """

    params: list[Any] = []
    sql = """
        SELECT v.id, v.article_id, v.option_value_ids_json, v.sale_price_cents,
               v.default_purchase_price_cents, v.no_reorder, v.is_active,
               a.name AS article_name, a.is_active AS article_is_active
        FROM variants v
        JOIN articles a ON a.id = v.article_id
    """
    if variant_ids is not None:
        identifiers = list({int(value) for value in variant_ids})
        if not identifiers:
            return {}
        placeholders = ",".join("?" for _ in identifiers)
        sql += f" WHERE v.id IN ({placeholders})"
        params.extend(identifiers)
    variant_rows = connection.execute(sql, params).fetchall()

    value_ids: set[int] = set()
    parsed_ids: dict[int, list[int]] = {}
    for row in variant_rows:
        try:
            ids = [int(value) for value in json.loads(row["option_value_ids_json"])]
        except (TypeError, ValueError, json.JSONDecodeError):
            ids = []
        parsed_ids[row["id"]] = ids
        value_ids.update(ids)

    values: dict[int, dict[str, Any]] = {}
    if value_ids:
        placeholders = ",".join("?" for _ in value_ids)
        value_rows = connection.execute(
            f"""
            SELECT ov.id, ov.value, ov.is_active, og.name AS group_name,
                   og.position AS group_position, og.is_active AS group_is_active
            FROM option_values ov
            JOIN option_groups og ON og.id = ov.option_group_id
            WHERE ov.id IN ({placeholders})
            """,
            list(value_ids),
        ).fetchall()
        values = {row["id"]: dict(row) for row in value_rows}

    output: dict[int, dict[str, Any]] = {}
    for raw_row in variant_rows:
        row = dict(raw_row)
        options = []
        for value_id in parsed_ids[row["id"]]:
            value = values.get(value_id)
            if value is None:
                # This should not occur with normal soft deletion.  It makes an
                # anomalous old record understandable instead of crashing views.
                options.append({"group_name": "Unbekannte Option", "value": f"#{value_id}", "position": 9999})
            else:
                options.append(
                    {
                        "group_name": value["group_name"],
                        "value": value["value"],
                        "position": value["group_position"],
                    }
                )
        options.sort(key=lambda item: (item["position"], item["group_name"].lower()))
        option_text = " · ".join(f"{item['group_name']}: {item['value']}" for item in options)
        row["option_value_ids"] = parsed_ids[row["id"]]
        row["options"] = options
        row["option_text"] = option_text
        row["label"] = row["article_name"] if not option_text else f"{row['article_name']} — {option_text}"
        output[row["id"]] = row
    return output


def article_payload(connection: sqlite3.Connection) -> list[dict[str, Any]]:
    """Return all active, sellable article data for the sale/purchase screens."""

    stock = variant_stock_map(connection)
    article_rows = connection.execute(
        """
        SELECT * FROM articles
        WHERE is_active = 1
        ORDER BY name COLLATE NOCASE
        """
    ).fetchall()
    result: list[dict[str, Any]] = []

    for raw_article in article_rows:
        article = dict(raw_article)
        groups = connection.execute(
            """
            SELECT * FROM option_groups
            WHERE article_id = ? AND is_active = 1
            ORDER BY position, id
            """,
            (article["id"],),
        ).fetchall()
        group_payload = []
        group_ids = []
        for raw_group in groups:
            group = dict(raw_group)
            value_rows = connection.execute(
                """
                SELECT id, value, position FROM option_values
                WHERE option_group_id = ? AND is_active = 1
                ORDER BY position, id
                """,
                (group["id"],),
            ).fetchall()
            values = [dict(row) for row in value_rows]
            group_payload.append({"id": group["id"], "name": group["name"], "values": values})
            group_ids.append(group["id"])

        variant_rows = connection.execute(
            """
            SELECT id, option_value_ids_json, sale_price_cents, default_purchase_price_cents
            FROM variants
            WHERE article_id = ? AND is_active = 1
            ORDER BY id
            """,
            (article["id"],),
        ).fetchall()
        labels = variant_label_map(connection, [row["id"] for row in variant_rows])
        variants = []
        for raw_variant in variant_rows:
            variant = labels[raw_variant["id"]]
            variant["stock"] = stock.get(raw_variant["id"], 0)
            variants.append(variant)

        # An article with no option groups is valid (patch, cap, ...).  An
        # article where an option group has no values is intentionally disabled
        # until it is fully configured, so no ambiguous sale can be entered.
        is_config_complete = not group_payload or all(group["values"] for group in group_payload)
        article["groups"] = group_payload
        article["variants"] = variants
        article["is_config_complete"] = is_config_complete and bool(variants)
        article["total_stock"] = sum(variant["stock"] for variant in variants)
        result.append(article)
    return result


def get_article_management_data(connection: sqlite3.Connection, article_id: int) -> dict[str, Any] | None:
    """Return the editable article configuration including active option values."""

    article_row = connection.execute("SELECT * FROM articles WHERE id = ?", (article_id,)).fetchone()
    if article_row is None:
        return None
    article = dict(article_row)
    groups = []
    for raw_group in connection.execute(
        """
        SELECT * FROM option_groups
        WHERE article_id = ? AND is_active = 1
        ORDER BY position, id
        """,
        (article_id,),
    ).fetchall():
        group = dict(raw_group)
        group["values"] = [
            dict(row)
            for row in connection.execute(
                """
                SELECT * FROM option_values
                WHERE option_group_id = ? AND is_active = 1
                ORDER BY position, id
                """,
                (group["id"],),
            ).fetchall()
        ]
        groups.append(group)

    stock = variant_stock_map(connection)
    variant_rows = connection.execute(
        """
        SELECT * FROM variants
        WHERE article_id = ? AND is_active = 1
        ORDER BY id
        """,
        (article_id,),
    ).fetchall()
    labels = variant_label_map(connection, [row["id"] for row in variant_rows])
    variants = []
    for raw_variant in variant_rows:
        variant = labels[raw_variant["id"]]
        variant["stock"] = stock.get(raw_variant["id"], 0)
        variants.append(variant)
    article["option_groups"] = groups
    article["variants"] = variants
    return article


def active_option_config(connection: sqlite3.Connection, article_id: int) -> list[dict[str, Any]]:
    """Read active option IDs in a form suitable for Cartesian variant creation."""

    groups = []
    group_rows = connection.execute(
        """
        SELECT id FROM option_groups
        WHERE article_id = ? AND is_active = 1
        ORDER BY position, id
        """,
        (article_id,),
    ).fetchall()
    for group_row in group_rows:
        value_rows = connection.execute(
            """
            SELECT id FROM option_values
            WHERE option_group_id = ? AND is_active = 1
            ORDER BY position, id
            """,
            (group_row["id"],),
        ).fetchall()
        groups.append({"id": group_row["id"], "value_ids": [row["id"] for row in value_rows]})
    return groups


def sync_variants(connection: sqlite3.Connection, article_id: int) -> None:
    """Synchronise active variants with the active article option configuration.

    There is no physical deletion here.  Historic sales may still point at a
    retired variant, and the matching option values remain available to render
    that history.  Existing price overrides are retained whenever a combination
    survives an option edit.
    """

    article = connection.execute("SELECT * FROM articles WHERE id = ?", (article_id,)).fetchone()
    if article is None:
        raise ValueError("Artikel wurde nicht gefunden.")

    groups = active_option_config(connection, article_id)
    if groups and any(not group["value_ids"] for group in groups):
        expected_keys: set[str] = set()
    elif not groups:
        expected_keys = {""}
    else:
        expected_keys = {
            sorted_combination_key(combination)
            for combination in itertools.product(*(group["value_ids"] for group in groups))
        }

    existing_rows = connection.execute(
        "SELECT id, combination_key, is_active FROM variants WHERE article_id = ?", (article_id,)
    ).fetchall()
    existing_by_key = {row["combination_key"]: row for row in existing_rows}
    now = utc_now()

    for key in expected_keys:
        if key in existing_by_key:
            connection.execute(
                "UPDATE variants SET is_active = 1, updated_at = ? WHERE id = ?",
                (now, existing_by_key[key]["id"]),
            )
            continue
        option_ids = [] if key == "" else [int(part) for part in key.split("|")]
        connection.execute(
            """
            INSERT INTO variants (
                article_id, option_value_ids_json, combination_key,
                sale_price_cents, default_purchase_price_cents, is_active, created_at, updated_at
            ) VALUES (?, ?, ?, ?, ?, 1, ?, ?)
            """,
            (
                article_id,
                json.dumps(option_ids),
                key,
                article["default_sale_price_cents"],
                article["default_purchase_price_cents"],
                now,
                now,
            ),
        )

    if expected_keys:
        placeholders = ",".join("?" for _ in expected_keys)
        connection.execute(
            f"""
            UPDATE variants SET is_active = 0, updated_at = ?
            WHERE article_id = ? AND combination_key NOT IN ({placeholders})
            """,
            [now, article_id, *expected_keys],
        )
    else:
        connection.execute(
            "UPDATE variants SET is_active = 0, updated_at = ? WHERE article_id = ?",
            (now, article_id),
        )


def validate_option_configuration(payload: Any) -> list[dict[str, Any]]:
    """Validate and normalise the dynamic option table submitted by the UI."""

    if not isinstance(payload, list):
        raise ValueError("Die Artikeloptionen sind ungültig.")
    normalised = []
    seen_group_names: set[str] = set()
    for position, raw_group in enumerate(payload):
        if not isinstance(raw_group, dict):
            continue
        name = str(raw_group.get("name", "")).strip()
        if not name:
            # Empty columns are editing leftovers, not actual option groups.
            continue
        name_key = name.casefold()
        if name_key in seen_group_names:
            raise ValueError(f"Die Option „{name}“ ist doppelt angelegt.")
        seen_group_names.add(name_key)
        values = []
        seen_values: set[str] = set()
        for value_position, raw_value in enumerate(raw_group.get("values", [])):
            if not isinstance(raw_value, dict):
                continue
            value = str(raw_value.get("value", "")).strip()
            if not value:
                continue
            value_key = value.casefold()
            if value_key in seen_values:
                raise ValueError(f"Der Wert „{value}“ ist bei „{name}“ doppelt angelegt.")
            seen_values.add(value_key)
            existing_id = raw_value.get("id")
            values.append(
                {
                    "id": int(existing_id) if str(existing_id or "").isdigit() else None,
                    "value": value,
                    "position": value_position,
                }
            )
        existing_id = raw_group.get("id")
        normalised.append(
            {
                "id": int(existing_id) if str(existing_id or "").isdigit() else None,
                "name": name,
                "position": position,
                "values": values,
            }
        )
    return normalised


def apply_option_configuration(
    connection: sqlite3.Connection, article_id: int, option_groups: list[dict[str, Any]]
) -> None:
    """Apply an option-grid edit using soft deletion for removed values/groups."""

    now = utc_now()
    known_group_rows = connection.execute(
        "SELECT id FROM option_groups WHERE article_id = ?", (article_id,)
    ).fetchall()
    known_group_ids = {row["id"] for row in known_group_rows}
    submitted_group_ids: set[int] = set()

    for group in option_groups:
        group_id = group["id"]
        if group_id is not None and group_id not in known_group_ids:
            raise ValueError("Eine übermittelte Option gehört nicht zu diesem Artikel.")
        if group_id is None:
            cursor = connection.execute(
                """
                INSERT INTO option_groups (article_id, name, position, is_active, created_at, updated_at)
                VALUES (?, ?, ?, 1, ?, ?)
                """,
                (article_id, group["name"], group["position"], now, now),
            )
            group_id = cursor.lastrowid
        else:
            connection.execute(
                """
                UPDATE option_groups
                SET name = ?, position = ?, is_active = 1, updated_at = ?
                WHERE id = ?
                """,
                (group["name"], group["position"], now, group_id),
            )
        submitted_group_ids.add(group_id)

        known_value_rows = connection.execute(
            "SELECT id FROM option_values WHERE option_group_id = ?", (group_id,)
        ).fetchall()
        known_value_ids = {row["id"] for row in known_value_rows}
        submitted_value_ids: set[int] = set()
        for value in group["values"]:
            value_id = value["id"]
            if value_id is not None and value_id not in known_value_ids:
                raise ValueError("Ein übermittelter Optionswert gehört nicht zu diesem Artikel.")
            if value_id is None:
                cursor = connection.execute(
                    """
                    INSERT INTO option_values (
                        option_group_id, value, position, is_active, created_at, updated_at
                    ) VALUES (?, ?, ?, 1, ?, ?)
                    """,
                    (group_id, value["value"], value["position"], now, now),
                )
                value_id = cursor.lastrowid
            else:
                connection.execute(
                    """
                    UPDATE option_values
                    SET value = ?, position = ?, is_active = 1, updated_at = ?
                    WHERE id = ?
                    """,
                    (value["value"], value["position"], now, value_id),
                )
            submitted_value_ids.add(value_id)

        # The UI's delete button simply omits a value.  Soft deletion preserves
        # the old ID/label for historic sales but prevents fresh selection.
        if known_value_ids - submitted_value_ids:
            placeholders = ",".join("?" for _ in known_value_ids - submitted_value_ids)
            connection.execute(
                f"UPDATE option_values SET is_active = 0, updated_at = ? WHERE id IN ({placeholders})",
                [now, *(known_value_ids - submitted_value_ids)],
            )

    retired_group_ids = known_group_ids - submitted_group_ids
    if retired_group_ids:
        placeholders = ",".join("?" for _ in retired_group_ids)
        connection.execute(
            f"UPDATE option_groups SET is_active = 0, updated_at = ? WHERE id IN ({placeholders})",
            [now, *retired_group_ids],
        )
        connection.execute(
            f"""
            UPDATE option_values SET is_active = 0, updated_at = ?
            WHERE option_group_id IN ({placeholders})
            """,
            [now, *retired_group_ids],
        )


def latest_purchase_price(connection: sqlite3.Connection, variant_id: int) -> int:
    """Use the actual most recent purchase price before falling back to a default."""

    row = connection.execute(
        """
        SELECT unit_cost_cents FROM purchases
        WHERE variant_id = ?
        ORDER BY purchased_on DESC, id DESC
        LIMIT 1
        """,
        (variant_id,),
    ).fetchone()
    if row is not None:
        return int(row["unit_cost_cents"])
    row = connection.execute(
        "SELECT default_purchase_price_cents FROM variants WHERE id = ?", (variant_id,)
    ).fetchone()
    return int(row["default_purchase_price_cents"]) if row else 0


def stock_for_variant(connection: sqlite3.Connection, variant_id: int) -> int:
    return variant_stock_map(connection).get(variant_id, 0)


def next_receipt_id(connection: sqlite3.Connection, prefix: str, on_date: str | None = None) -> str:
    """Return a readable ID such as ``V-20260814-003``.

    The ID is previewed before confirmation.  A concurrent sale can consume that
    preview, so write routes check uniqueness and generate a new ID if needed.
    """

    day = (on_date or today_iso()).replace("-", "")
    pattern = f"{prefix}-{day}-%"
    table = "sales" if prefix == "V" else "purchases"
    rows = connection.execute(
        f"SELECT receipt_id FROM {table} WHERE receipt_id LIKE ?", (pattern,)
    ).fetchall()
    highest = 0
    for row in rows:
        match = re.search(r"-(\d+)$", row["receipt_id"])
        if match:
            highest = max(highest, int(match.group(1)))
    return f"{prefix}-{day}-{highest + 1:03d}"


def unique_receipt_id(
    connection: sqlite3.Connection, prefix: str, supplied: str | None, on_date: str
) -> str:
    """Use a valid preview if still free; otherwise create a new sequential ID."""

    table = "sales" if prefix == "V" else "purchases"
    pattern = rf"^{prefix}-{on_date.replace('-', '')}-\d{{3,}}$"
    if supplied and re.fullmatch(pattern, supplied):
        exists = connection.execute(
            f"SELECT 1 FROM {table} WHERE receipt_id = ?", (supplied,)
        ).fetchone()
        if exists is None:
            return supplied
    return next_receipt_id(connection, prefix, on_date)


def csv_rows(connection: sqlite3.Connection, kind: str) -> tuple[str, list[str], list[list[Any]]]:
    """Produce exports from the database, never from rendered tables."""

    if kind == "articles":
        rows = []
        for article in article_payload(connection):
            for variant in article["variants"]:
                rows.append(
                    [
                        article["id"],
                        article["name"],
                        variant["id"],
                        variant["option_text"],
                        variant["stock"],
                        variant["sale_price_cents"] / 100,
                        variant["default_purchase_price_cents"] / 100,
                        "nein" if variant["no_reorder"] else "ja",
                        "aktiv" if variant["is_active"] else "inaktiv",
                    ]
                )
        return (
            "artikel",
            ["Artikel-ID", "Artikel", "Varianten-ID", "Optionen", "Bestand", "Verkaufspreis", "Standard-Einkaufspreis", "Nachbestellen", "Status"],
            rows,
        )
    if kind == "sales":
        records = connection.execute("SELECT * FROM sales ORDER BY sold_on, id").fetchall()
        labels = variant_label_map(connection, [row["variant_id"] for row in records])
        return (
            "verkaeufe",
            [
                "Beleg-ID", "Datum", "Artikel", "Optionen", "Stück", "Preis/Stück", "Betrag", "Gegeben", "Spende",
                "Bezahlart", "Bezahlt", "Artikel erhalten", "Kundenname", "Adresse", "Veranstaltung", "Kommentar",
            ],
            [
                [
                    row["receipt_id"], row["sold_on"], labels[row["variant_id"]]["article_name"], labels[row["variant_id"]]["option_text"],
                    row["quantity"], row["unit_price_cents"] / 100, row["amount_due_cents"] / 100,
                    "" if row["amount_given_cents"] is None else row["amount_given_cents"] / 100,
                    row["donation_cents"] / 100, row["payment_method"], "ja" if row["is_paid"] else "nein",
                    "ja" if row["is_received"] else "nein", row["customer_name"] or "", row["customer_address"] or "",
                    row["event_name"] or "", row["comment"] or "",
                ]
                for row in records
            ],
        )
    if kind == "purchases":
        records = connection.execute("SELECT * FROM purchases ORDER BY purchased_on, id").fetchall()
        labels = variant_label_map(connection, [row["variant_id"] for row in records])
        return (
            "einkaeufe",
            ["Beleg-ID", "Datum", "Artikel", "Optionen", "Stück", "Preis/Stück", "Gesamt", "Lieferant", "Rechnung", "Kommentar"],
            [
                [
                    row["receipt_id"], row["purchased_on"], labels[row["variant_id"]]["article_name"], labels[row["variant_id"]]["option_text"],
                    row["quantity"], row["unit_cost_cents"] / 100, row["quantity"] * row["unit_cost_cents"] / 100,
                    row["supplier"] or "", row["invoice_reference"] or "", row["comment"] or "",
                ]
                for row in records
            ],
        )
    if kind == "inventory":
        stock = variant_stock_map(connection)
        rows = []
        variant_rows = connection.execute("SELECT id FROM variants ORDER BY id").fetchall()
        labels = variant_label_map(connection, [row["id"] for row in variant_rows])
        for variant_id, label in labels.items():
            purchased = connection.execute(
                "SELECT COALESCE(SUM(quantity), 0) FROM purchases WHERE variant_id = ?", (variant_id,)
            ).fetchone()[0]
            sold = connection.execute(
                "SELECT COALESCE(SUM(quantity), 0) FROM sales WHERE variant_id = ?", (variant_id,)
            ).fetchone()[0]
            rows.append([label["article_name"], label["option_text"], purchased, sold, stock.get(variant_id, 0)])
        return ("bestand", ["Artikel", "Optionen", "Gekauft", "Verkauft", "Aktueller Bestand"], rows)
    raise ValueError("Unbekannter Export.")


def csv_bytes(headers: list[str], rows: Iterable[Iterable[Any]]) -> bytes:
    """Create an Excel-friendly UTF-8 CSV with a BOM and semicolon separator."""

    buffer = io.StringIO(newline="")
    writer = csv.writer(buffer, delimiter=";")
    writer.writerow(headers)
    writer.writerows(rows)
    return ("\ufeff" + buffer.getvalue()).encode("utf-8")


def create_backup(app: Flask) -> None:
    """Create a restorable SQLite snapshot and human-readable CSV exports.

    It runs after every successful write.  The application database itself stays
    authoritative; the SQLite snapshot is the recovery copy, while CSV makes
    ad-hoc inspection and migration straightforward.
    """

    if not app.config.get("AUTO_BACKUP", True):
        return
    backup_root = Path(app.config["BACKUP_DIR"])
    backup_root.mkdir(parents=True, exist_ok=True)
    timestamp = datetime.now().strftime("%Y-%m-%d_%H-%M-%S")
    target = backup_root / timestamp
    suffix = 1
    while target.exists():
        suffix += 1
        target = backup_root / f"{timestamp}_{suffix}"
    target.mkdir()

    source = db_connect(app.config["DATABASE"])
    destination = sqlite3.connect(target / "merch.sqlite3")
    try:
        source.backup(destination)
        destination.close()
        for kind in ("articles", "sales", "purchases", "inventory"):
            filename, headers, rows = csv_rows(source, kind)
            (target / f"{filename}.csv").write_bytes(csv_bytes(headers, rows))
    finally:
        source.close()
        try:
            destination.close()
        except sqlite3.ProgrammingError:
            pass

    retention_days = int(app.config["BACKUP_RETENTION_DAYS"])
    cutoff = datetime.now().timestamp() - retention_days * 24 * 60 * 60
    for child in backup_root.iterdir():
        if child.is_dir() and child.stat().st_mtime < cutoff:
            shutil.rmtree(child)


def backup_after_commit() -> None:
    """Run backup without risking a successfully committed sale on backup failure."""

    try:
        create_backup(current_app._get_current_object())
    except Exception:  # pragma: no cover - failure is logged, not hidden from ops
        current_app.logger.exception("Automatic backup failed after a committed write")


def balance_payload(connection: sqlite3.Connection) -> dict[str, Any]:
    """Calculate the article balance table and headline figures from ledgers."""

    stock = variant_stock_map(connection)
    variant_rows = connection.execute("SELECT id FROM variants ORDER BY id").fetchall()
    labels = variant_label_map(connection, [row["id"] for row in variant_rows])
    rows = []
    for variant_id, label in labels.items():
        purchase_row = connection.execute(
            """
            SELECT COALESCE(SUM(quantity), 0) AS quantity,
                   COALESCE(SUM(quantity * unit_cost_cents), 0) AS cost
            FROM purchases WHERE variant_id = ?
            """,
            (variant_id,),
        ).fetchone()
        sale_row = connection.execute(
            """
            SELECT COALESCE(SUM(quantity), 0) AS quantity,
                   COALESCE(SUM(amount_due_cents), 0) AS revenue,
                   COALESCE(SUM(CASE WHEN is_paid = 1 THEN amount_due_cents ELSE 0 END), 0) AS collected,
                   COALESCE(SUM(CASE WHEN is_paid = 1 THEN donation_cents ELSE 0 END), 0) AS donation
            FROM sales WHERE variant_id = ?
            """,
            (variant_id,),
        ).fetchone()
        # Do not show completely unused, retired variants as balance rows.
        if not purchase_row["quantity"] and not sale_row["quantity"] and not label["is_active"]:
            continue
        rows.append(
            {
                "variant_id": variant_id,
                "article_name": label["article_name"],
                "option_text": label["option_text"],
                "label": label["label"],
                "purchased_quantity": int(purchase_row["quantity"]),
                "sold_quantity": int(sale_row["quantity"]),
                "stock": stock.get(variant_id, 0),
                "purchase_cost_cents": int(purchase_row["cost"]),
                "revenue_cents": int(sale_row["revenue"]),
                "collected_cents": int(sale_row["collected"]),
                "donation_cents": int(sale_row["donation"]),
                "no_reorder": bool(label["no_reorder"]),
                "is_active": bool(label["is_active"]),
            }
        )
    rows.sort(key=lambda item: (item["article_name"].casefold(), item["option_text"].casefold()))

    total_purchase_cost = sum(row["purchase_cost_cents"] for row in rows)
    total_revenue = sum(row["revenue_cents"] for row in rows)
    total_collected = sum(row["collected_cents"] for row in rows)
    total_donation = sum(row["donation_cents"] for row in rows)
    outstanding_paid = connection.execute(
        "SELECT COALESCE(SUM(amount_due_cents), 0) FROM sales WHERE is_paid = 0"
    ).fetchone()[0]
    pending_delivery = connection.execute("SELECT COUNT(*) FROM sales WHERE is_received = 0").fetchone()[0]
    return {
        "rows": rows,
        "summary": {
            "purchase_cost_cents": total_purchase_cost,
            "revenue_cents": total_revenue,
            "collected_cents": total_collected,
            "donation_cents": total_donation,
            "cash_balance_cents": total_collected + total_donation - total_purchase_cost,
            "outstanding_cents": int(outstanding_paid),
            "pending_delivery_count": int(pending_delivery),
            "stock_count": sum(row["stock"] for row in rows),
        },
    }


def create_app(test_config: dict[str, Any] | None = None) -> Flask:
    """Application factory used by Gunicorn and by future automated tests."""

    data_dir = Path(os.environ.get("DATA_DIR", Path(__file__).parent / "data"))
    app = Flask(__name__)
    app.config.from_mapping(
        SECRET_KEY=os.environ.get("SECRET_KEY", "development-only-change-me"),
        DATABASE=str(data_dir / "merch.sqlite3"),
        BACKUP_DIR=str(data_dir / "backups"),
        BACKUP_RETENTION_DAYS=int(os.environ.get("BACKUP_RETENTION_DAYS", "90")),
        ADMIN_USERNAME=os.environ.get("ADMIN_USERNAME", "admin"),
        ADMIN_PASSWORD=os.environ.get("ADMIN_PASSWORD", "replace-this-password"),
        AUTO_BACKUP=True,
        SESSION_COOKIE_HTTPONLY=True,
        SESSION_COOKIE_SAMESITE="Lax",
    )
    if test_config:
        app.config.update(test_config)
    if app.config["SECRET_KEY"] == "development-only-change-me" and not app.config.get("TESTING"):
        raise RuntimeError("Set SECRET_KEY in .env before starting the app.")

    Path(app.config["DATABASE"]).parent.mkdir(parents=True, exist_ok=True)
    Path(app.config["BACKUP_DIR"]).mkdir(parents=True, exist_ok=True)
    initialise_database(app)
    app.teardown_appcontext(close_db)

    @app.template_filter("money")
    def money_filter(value: int | None) -> str:
        return cents_to_money(value)

    @app.context_processor
    def inject_template_values() -> dict[str, Any]:
        return {"csrf_token": csrf_token, "current_user": g.get("user"), "payment_methods": PAYMENT_METHODS}

    @app.before_request
    def load_request_context() -> None:
        require_csrf()
        user_id = session.get("user_id")
        g.user = None
        if user_id:
            g.user = row_to_dict(get_db().execute("SELECT * FROM users WHERE id = ?", (user_id,)).fetchone())

    @app.get("/")
    def index():
        return redirect(url_for("sales_page"))

    @app.route("/login", methods=["GET", "POST"])
    def login():
        if request.method == "POST":
            username = request.form.get("username", "").strip()
            password = request.form.get("password", "")
            user = get_db().execute("SELECT * FROM users WHERE username = ?", (username,)).fetchone()
            if user is None or not check_password_hash(user["password_hash"], password):
                flash("Benutzername oder Passwort ist nicht korrekt.", "error")
            else:
                session.clear()
                session["user_id"] = user["id"]
                csrf_token()
                next_url = request.args.get("next")
                if not next_url or not next_url.startswith("/") or next_url.startswith("//"):
                    next_url = url_for("sales_page")
                return redirect(next_url)
        return render_template("login.html", title="Anmelden")

    @app.post("/logout")
    @login_required
    def logout():
        session.clear()
        return redirect(url_for("login"))

    @app.get("/verkauf")
    @login_required
    def sales_page():
        return render_template("sales.html", title="Verkauf", articles=article_payload(get_db()), today=today_iso())

    @app.get("/api/receipt-preview")
    @login_required
    def receipt_preview():
        kind = request.args.get("kind", "sale")
        prefix = "V" if kind == "sale" else "E"
        return jsonify({"ok": True, "receipt_id": next_receipt_id(get_db(), prefix)})

    @app.post("/api/sales")
    @login_required
    def create_sale():
        payload = request.get_json(silent=True) or {}
        connection = get_db()
        try:
            variant_id = int(payload.get("variant_id"))
            quantity = parse_positive_int(payload.get("quantity"))
            is_paid = bool(payload.get("is_paid", True))
            is_received = bool(payload.get("is_received", True))
            payment_method = str(payload.get("payment_method", "")).strip()
            if payment_method not in PAYMENT_METHODS:
                raise ValueError("Bitte eine gültige Bezahlart auswählen.")
            customer_name = str(payload.get("customer_name", "")).strip()
            customer_address = str(payload.get("customer_address", "")).strip()
            if not is_received and (not customer_name or not customer_address):
                raise ValueError("Bei noch nicht erhaltenen Artikeln sind Name und Adresse Pflicht.")

            variant = connection.execute(
                "SELECT * FROM variants WHERE id = ? AND is_active = 1", (variant_id,)
            ).fetchone()
            if variant is None:
                raise ValueError("Diese Artikelvariante ist nicht mehr verfügbar.")
            stock = stock_for_variant(connection, variant_id)
            if quantity > stock:
                raise ValueError(f"Nur noch {stock} Stück dieser Variante auf Lager.")

            amount_due = quantity * int(variant["sale_price_cents"])
            given_raw = payload.get("amount_given")
            amount_given = None if given_raw in (None, "") else money_to_cents(given_raw, field_name="Gegeben")
            if is_paid and amount_given is not None and amount_given < amount_due:
                raise ValueError("Wenn „Bezahlt“ markiert ist, darf „Gegeben“ nicht kleiner als der Betrag sein.")
            # An unpaid booking must never accidentally count as a donation just
            # because a stale browser field was sent with it.
            if not is_paid:
                amount_given = None
                donation = 0
            else:
                donation = max(0, (amount_given or 0) - amount_due)
            sold_on = str(payload.get("sold_on") or today_iso())
            date.fromisoformat(sold_on)

            connection.execute("BEGIN IMMEDIATE")
            # Repeat the stock check after acquiring the write lock, so two people
            # cannot sell the final shirt at exactly the same moment.
            stock = stock_for_variant(connection, variant_id)
            if quantity > stock:
                raise ValueError(f"Nur noch {stock} Stück dieser Variante auf Lager.")
            receipt_id = unique_receipt_id(connection, "V", payload.get("receipt_id"), sold_on)
            cursor = connection.execute(
                """
                INSERT INTO sales (
                    receipt_id, variant_id, quantity, unit_price_cents, amount_due_cents,
                    amount_given_cents, donation_cents, payment_method, is_paid, is_received,
                    customer_name, customer_address, event_name, comment, sold_on, created_at, created_by
                ) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
                """,
                (
                    receipt_id, variant_id, quantity, variant["sale_price_cents"], amount_due, amount_given,
                    donation, payment_method, int(is_paid), int(is_received), customer_name or None,
                    customer_address or None, str(payload.get("event_name", "")).strip() or None,
                    str(payload.get("comment", "")).strip() or None, sold_on, utc_now(), g.user["id"],
                ),
            )
            audit(connection, "create", "sale", cursor.lastrowid, {"receipt_id": receipt_id, "quantity": quantity})
            connection.commit()
            backup_after_commit()
            return jsonify(
                {
                    "ok": True,
                    "receipt_id": receipt_id,
                    "amount_due_cents": amount_due,
                    "donation_cents": donation,
                    "message": "Kauf erfolgreich erfasst.",
                }
            )
        except (ValueError, TypeError) as exc:
            connection.rollback()
            return jsonify({"ok": False, "error": str(exc)}), 400
        except Exception:
            connection.rollback()
            current_app.logger.exception("Could not create sale")
            return jsonify({"ok": False, "error": "Der Kauf konnte nicht gespeichert werden."}), 500

    @app.get("/einkaeufe")
    @login_required
    def purchases_page():
        latest_rows = get_db().execute("SELECT * FROM purchases ORDER BY purchased_on DESC, id DESC LIMIT 15").fetchall()
        labels = variant_label_map(get_db(), [row["variant_id"] for row in latest_rows])
        purchases = []
        for row in latest_rows:
            item = dict(row)
            item.update(labels[row["variant_id"]])
            purchases.append(item)
        return render_template(
            "purchases.html", title="Einkäufe", articles=article_payload(get_db()), purchases=purchases, today=today_iso()
        )

    @app.get("/api/variants/<int:variant_id>/last-purchase-price")
    @login_required
    def last_purchase_price(variant_id: int):
        variant = get_db().execute("SELECT id FROM variants WHERE id = ? AND is_active = 1", (variant_id,)).fetchone()
        if variant is None:
            return jsonify({"ok": False, "error": "Variante nicht gefunden."}), 404
        cents = latest_purchase_price(get_db(), variant_id)
        return jsonify({"ok": True, "price_cents": cents, "price": f"{cents / 100:.2f}"})

    @app.post("/api/purchases")
    @login_required
    def create_purchase():
        payload = request.get_json(silent=True) or {}
        connection = get_db()
        try:
            variant_id = int(payload.get("variant_id"))
            quantity = parse_positive_int(payload.get("quantity"))
            unit_cost = money_to_cents(payload.get("unit_cost"), field_name="Preis pro Stück")
            purchased_on = str(payload.get("purchased_on") or today_iso())
            date.fromisoformat(purchased_on)
            variant = connection.execute(
                "SELECT id FROM variants WHERE id = ? AND is_active = 1", (variant_id,)
            ).fetchone()
            if variant is None:
                raise ValueError("Diese Artikelvariante ist nicht mehr verfügbar.")

            connection.execute("BEGIN IMMEDIATE")
            receipt_id = unique_receipt_id(connection, "E", payload.get("receipt_id"), purchased_on)
            cursor = connection.execute(
                """
                INSERT INTO purchases (
                    receipt_id, variant_id, quantity, unit_cost_cents, purchased_on,
                    supplier, invoice_reference, comment, created_at, created_by
                ) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
                """,
                (
                    receipt_id, variant_id, quantity, unit_cost, purchased_on,
                    str(payload.get("supplier", "")).strip() or None,
                    str(payload.get("invoice_reference", "")).strip() or None,
                    str(payload.get("comment", "")).strip() or None,
                    utc_now(), g.user["id"],
                ),
            )
            audit(connection, "create", "purchase", cursor.lastrowid, {"receipt_id": receipt_id, "quantity": quantity})
            connection.commit()
            backup_after_commit()
            return jsonify({"ok": True, "receipt_id": receipt_id, "message": "Einkauf erfolgreich erfasst."})
        except (ValueError, TypeError) as exc:
            connection.rollback()
            return jsonify({"ok": False, "error": str(exc)}), 400
        except Exception:
            connection.rollback()
            current_app.logger.exception("Could not create purchase")
            return jsonify({"ok": False, "error": "Der Einkauf konnte nicht gespeichert werden."}), 500

    @app.get("/historie")
    @login_required
    def history_page():
        sale_rows = get_db().execute("SELECT * FROM sales ORDER BY sold_on DESC, id DESC").fetchall()
        labels = variant_label_map(get_db(), [row["variant_id"] for row in sale_rows])
        sales = []
        for row in sale_rows:
            item = dict(row)
            item.update(labels[row["variant_id"]])
            sales.append(item)
        return render_template("history.html", title="Historie", sales=sales)

    @app.get("/bilanzen")
    @login_required
    def balances_page():
        return render_template("balances.html", title="Bilanzen", balances=balance_payload(get_db()))

    @app.get("/export/<kind>.csv")
    @login_required
    def export_csv(kind: str):
        try:
            filename, headers, rows = csv_rows(get_db(), kind)
        except ValueError:
            abort(404)
        return Response(
            csv_bytes(headers, rows),
            mimetype="text/csv; charset=utf-8",
            headers={"Content-Disposition": f'attachment; filename="{filename}.csv"'},
        )

    @app.get("/export/alles.zip")
    @login_required
    def export_all():
        buffer = io.BytesIO()
        with ZipFile(buffer, "w", ZIP_DEFLATED) as archive:
            for kind in ("articles", "sales", "purchases", "inventory"):
                filename, headers, rows = csv_rows(get_db(), kind)
                archive.writestr(f"{filename}.csv", csv_bytes(headers, rows))
        buffer.seek(0)
        return send_file(buffer, mimetype="application/zip", as_attachment=True, download_name="merch-export.zip")

    @app.get("/artikelverwaltung")
    @login_required
    @admin_required
    def article_management_page():
        connection = get_db()
        article_rows = connection.execute(
            "SELECT id, name FROM articles WHERE is_active = 1 ORDER BY name COLLATE NOCASE"
        ).fetchall()
        requested_id = request.args.get("article", type=int)
        selected_id = requested_id or (article_rows[0]["id"] if article_rows else None)
        article = get_article_management_data(connection, selected_id) if selected_id else None
        return render_template(
            "articles.html", title="Artikelverwaltung", article_list=[dict(row) for row in article_rows], article=article
        )

    @app.post("/artikelverwaltung/neu")
    @login_required
    @admin_required
    def create_article():
        connection = get_db()
        now = utc_now()
        try:
            connection.execute("BEGIN IMMEDIATE")
            cursor = connection.execute(
                """
                INSERT INTO articles (
                    name, default_sale_price_cents, default_purchase_price_cents, is_active, created_at, updated_at
                ) VALUES (?, 0, 0, 1, ?, ?)
                """,
                ("Neuer Artikel", now, now),
            )
            article_id = cursor.lastrowid
            # New articles get the requested default columns.  Empty groups make
            # the article non-sellable until the values are filled in.
            apply_option_configuration(
                connection,
                article_id,
                [
                    {"id": None, "name": "Farbe", "position": 0, "values": []},
                    {"id": None, "name": "Größe", "position": 1, "values": []},
                ],
            )
            sync_variants(connection, article_id)
            audit(connection, "create", "article", article_id, {"name": "Neuer Artikel"})
            connection.commit()
            backup_after_commit()
            return redirect(url_for("article_management_page", article=article_id))
        except sqlite3.IntegrityError:
            connection.rollback()
            # Repeated clicks should still work; provide a clearly named unique article.
            unique_name = f"Neuer Artikel {datetime.now().strftime('%H%M%S')}"
            connection.execute("BEGIN IMMEDIATE")
            cursor = connection.execute(
                """
                INSERT INTO articles (
                    name, default_sale_price_cents, default_purchase_price_cents, is_active, created_at, updated_at
                ) VALUES (?, 0, 0, 1, ?, ?)
                """,
                (unique_name, now, now),
            )
            article_id = cursor.lastrowid
            apply_option_configuration(
                connection,
                article_id,
                [
                    {"id": None, "name": "Farbe", "position": 0, "values": []},
                    {"id": None, "name": "Größe", "position": 1, "values": []},
                ],
            )
            sync_variants(connection, article_id)
            audit(connection, "create", "article", article_id, {"name": unique_name})
            connection.commit()
            backup_after_commit()
            return redirect(url_for("article_management_page", article=article_id))

    @app.post("/artikelverwaltung/<int:article_id>/speichern")
    @login_required
    @admin_required
    def save_article(article_id: int):
        connection = get_db()
        try:
            article = connection.execute("SELECT id FROM articles WHERE id = ?", (article_id,)).fetchone()
            if article is None:
                abort(404)
            name = request.form.get("name", "").strip()
            if not name:
                raise ValueError("Der Artikelname darf nicht leer sein.")
            sale_price = money_to_cents(request.form.get("default_sale_price"), field_name="Standard-Verkaufspreis")
            purchase_price = money_to_cents(request.form.get("default_purchase_price"), field_name="Standard-Einkaufspreis")
            raw_options = request.form.get("options_json", "[]")
            option_groups = validate_option_configuration(json.loads(raw_options))

            connection.execute("BEGIN IMMEDIATE")
            connection.execute(
                """
                UPDATE articles
                SET name = ?, default_sale_price_cents = ?, default_purchase_price_cents = ?, updated_at = ?
                WHERE id = ?
                """,
                (name, sale_price, purchase_price, utc_now(), article_id),
            )
            apply_option_configuration(connection, article_id, option_groups)
            sync_variants(connection, article_id)
            # Checkboxes are absent from regular forms when unchecked, so reset
            # the article's active variants before applying checked entries.
            connection.execute("UPDATE variants SET no_reorder = 0 WHERE article_id = ? AND is_active = 1", (article_id,))
            # Variant price overrides arrive as regular form fields, so prices
            # still survive if JavaScript is unavailable during a save.
            for key, value in request.form.items():
                match = re.fullmatch(r"(sale|purchase)_price_(\d+)", key)
                if not match:
                    reorder_match = re.fullmatch(r"no_reorder_(\d+)", key)
                    if reorder_match:
                        connection.execute(
                            "UPDATE variants SET no_reorder = 1, updated_at = ? WHERE id = ? AND article_id = ?",
                            (utc_now(), int(reorder_match.group(1)), article_id),
                        )
                    continue
                field, variant_id = match.groups()
                cents = money_to_cents(value, field_name="Variantenpreis")
                column = "sale_price_cents" if field == "sale" else "default_purchase_price_cents"
                connection.execute(
                    f"UPDATE variants SET {column} = ?, updated_at = ? WHERE id = ? AND article_id = ?",
                    (cents, utc_now(), int(variant_id), article_id),
                )
            audit(connection, "update", "article", article_id, {"name": name, "options_changed": True})
            connection.commit()
            backup_after_commit()
            flash("Artikel und Varianten wurden gespeichert.", "success")
        except (ValueError, json.JSONDecodeError, sqlite3.IntegrityError) as exc:
            connection.rollback()
            flash(str(exc) if not isinstance(exc, sqlite3.IntegrityError) else "Der Artikelname ist bereits vergeben.", "error")
        return redirect(url_for("article_management_page", article=article_id))

    @app.errorhandler(400)
    def bad_request(error):
        if request.path.startswith("/api/"):
            return jsonify({"ok": False, "error": error.description}), 400
        return render_template("error.html", title="Ungültige Anfrage", message=error.description), 400

    @app.errorhandler(403)
    def forbidden(_: Exception):
        return render_template("error.html", title="Kein Zugriff", message="Dafür fehlen die benötigten Rechte."), 403

    @app.errorhandler(404)
    def not_found(_: Exception):
        return render_template("error.html", title="Nicht gefunden", message="Diese Seite gibt es nicht."), 404

    return app


if __name__ == "__main__":  # pragma: no cover - Docker starts Gunicorn instead.
    create_app().run(host="0.0.0.0", port=8000, debug=False)
