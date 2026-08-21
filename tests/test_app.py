"""Regression tests for the business rules that must not silently change."""

from __future__ import annotations

import io
import json
import re
import sqlite3
import tempfile
import unittest
from zipfile import ZipFile
from pathlib import Path
from unittest.mock import patch

import pyotp
from PIL import Image
from werkzeug.security import check_password_hash, generate_password_hash

from app import (
    LEGACY_COMBINED_SCHEMA_SQL,
    apply_option_configuration,
    article_payload,
    balance_payload,
    change_database_passphrase,
    create_backup,
    csv_rows,
    create_app,
    database_encryption_state,
    decrypt_mfa_secret,
    encrypt_mfa_secret,
    get_db,
    get_user_db,
    read_invoice_bytes,
    regenerate_database_recovery_key,
    setup_encrypted_databases,
    send_smtp_notification,
    smtp_notification_status,
    store_invoice_bytes,
    sync_variants,
    unlock_encrypted_databases,
    variant_label_map,
)


class MerchAppTestCase(unittest.TestCase):
    def setUp(self) -> None:
        self.tempdir = tempfile.TemporaryDirectory()
        root = Path(self.tempdir.name)
        self.app = create_app(
            {
                "TESTING": True,
                "SECRET_KEY": "test-secret",
                "DATABASE": str(root / "merch.sqlite3"),
                "BACKUP_DIR": str(root / "backups"),
                "RESET_ARCHIVE_DIR": str(root / "reset-archives"),
                "INVOICE_UPLOAD_DIR": str(root / "invoices"),
                "VARIANT_PHOTO_UPLOAD_DIR": str(root / "variant-photos"),
                "ADMIN_USERNAME": "tester",
                "ADMIN_PASSWORD": "test-password",
                "APP_VERSION": "v0.3.0",
                "AUTO_BACKUP": False,
            }
        )
        self.client = self.app.test_client()
        with self.client.session_transaction() as session:
            session["user_id"] = 1
            session["user_session_version"] = 0
            session["csrf_token"] = "test-csrf"

    def tearDown(self) -> None:
        self.tempdir.cleanup()

    def test_local_build_uses_a_neutral_version_without_a_release_tag(self) -> None:
        """Source builds must not depend on a second VERSION file."""

        root = Path(self.tempdir.name) / "local-build"
        with patch.dict("app.os.environ", {}, clear=True):
            local_app = create_app(
                {
                    "TESTING": True,
                    "SECRET_KEY": "test-secret",
                    "DATABASE": str(root / "merch.sqlite3"),
                    "BACKUP_DIR": str(root / "backups"),
                    "INVOICE_UPLOAD_DIR": str(root / "invoices"),
                    "ADMIN_USERNAME": "tester",
                    "ADMIN_PASSWORD": "test-password",
                    "AUTO_BACKUP": False,
                }
            )

        self.assertEqual(local_app.config["APP_VERSION"], "0.0.0")

    def test_encrypted_database_requires_setup_then_unlocks_without_plaintext_files(self) -> None:
        """SQLCipher data stays unreadable to sqlite3 and needs a separate unlock after restart."""

        root = Path(self.tempdir.name) / "encrypted-installation"
        config = {
            "TESTING": True,
            "SECRET_KEY": "test-secret",
            "DATABASE": str(root / "merch.sqlite3"),
            "USERS_DATABASE": str(root / "users.sqlite3"),
            "BACKUP_DIR": str(root / "backups"),
            "RESET_ARCHIVE_DIR": str(root / "reset-archives"),
            "INVOICE_UPLOAD_DIR": str(root / "invoices"),
            "DATABASE_ENCRYPTION_ENABLED": True,
            "ADMIN_USERNAME": "encrypted-admin",
            "ADMIN_PASSWORD": "bootstrap-admin-password",
            "APP_VERSION": "v0.3.0",
            "AUTO_BACKUP": False,
        }
        encrypted_app = create_app(config)
        encrypted_client = encrypted_app.test_client()
        first = encrypted_client.get("/verkauf")
        self.assertTrue(first.location.endswith("/system/verschluesselung/einrichten"))

        setup_page = encrypted_client.get("/system/verschluesselung/einrichten")
        self.assertEqual(setup_page.status_code, 200)
        with encrypted_client.session_transaction() as session:
            setup_csrf = session["csrf_token"]
        setup = encrypted_client.post(
            "/system/verschluesselung/einrichten",
            data={
                "csrf_token": setup_csrf,
                "bootstrap_password": "bootstrap-admin-password",
                "database_passphrase": "a local database passphrase",
                "database_passphrase_confirmation": "a local database passphrase",
            },
        )
        self.assertTrue(setup.location.endswith("/system/verschluesselung/recovery"))
        recovery_page = encrypted_client.get("/system/verschluesselung/recovery")
        recovery_match = re.search(r"PVM-RK1-[A-Z0-9-]+", recovery_page.get_data(as_text=True))
        self.assertIsNotNone(recovery_match)
        recovery_key = recovery_match.group(0)
        with encrypted_client.session_transaction() as session:
            recovery_csrf = session["csrf_token"]
        complete = encrypted_client.post(
            "/system/verschluesselung/recovery",
            data={"csrf_token": recovery_csrf},
        )
        self.assertTrue(complete.location.endswith("/login"))
        self.assertEqual(database_encryption_state(encrypted_app), "unlocked")

        with encrypted_app.app_context():
            encrypted_invoice = store_invoice_bytes("private.pdf", b"%PDF-private invoice")
            self.assertTrue(encrypted_invoice.name.endswith(".pdf.enc"))
            self.assertNotIn(b"private invoice", encrypted_invoice.read_bytes())
            self.assertEqual(read_invoice_bytes("private.pdf"), b"%PDF-private invoice")
            encrypted_backup = create_backup(encrypted_app, force=True)
        self.assertIsNotNone(encrypted_backup)
        self.assertFalse((encrypted_backup / "verkaeufe.csv").exists())
        self.assertTrue((encrypted_backup / "invoices" / "private.pdf.enc").is_file())
        self.assertTrue((encrypted_backup / "encryption.json").is_file())

        plaintext_attempt = sqlite3.connect(root / "merch.sqlite3")
        try:
            with self.assertRaises(sqlite3.DatabaseError):
                plaintext_attempt.execute("SELECT name FROM sqlite_master").fetchall()
        finally:
            plaintext_attempt.close()
        self.assertNotIn(b"encrypted-admin", (root / "users.sqlite3").read_bytes())

        restarted_app = create_app(config)
        restarted_client = restarted_app.test_client()
        self.assertEqual(database_encryption_state(restarted_app), "locked")
        locked = restarted_client.get("/verkauf")
        self.assertTrue(locked.location.endswith("/system/verschluesselung/entsperren"))
        unlock_page = restarted_client.get("/system/verschluesselung/entsperren")
        with restarted_client.session_transaction() as session:
            unlock_csrf = session["csrf_token"]
        unlocked = restarted_client.post(
            "/system/verschluesselung/entsperren",
            data={"csrf_token": unlock_csrf, "recovery_key": recovery_key},
        )
        self.assertTrue(unlocked.location.endswith("/login"))
        self.assertEqual(database_encryption_state(restarted_app), "unlocked")
        with restarted_app.app_context():
            self.assertEqual(get_user_db().execute("SELECT username FROM users").fetchone()[0], "encrypted-admin")

    def test_plaintext_legacy_import_is_not_available(self) -> None:
        """Plaintext stores cannot be uploaded into the active installation."""

        routes = {rule.rule for rule in self.app.url_map.iter_rules()}
        self.assertNotIn("/verwaltung/altdaten/vorschau", routes)
        self.assertNotIn("/verwaltung/altdaten/importieren", routes)
        administration = self.client.get("/verwaltung")
        self.assertEqual(administration.status_code, 200)
        self.assertNotIn("Ungesicherte Altdaten importieren", administration.get_data(as_text=True))
        self.assertEqual(
            self.client.post(
                "/verwaltung/altdaten/vorschau", data={"csrf_token": "test-csrf"}
            ).status_code,
            404,
        )
    def test_database_passphrase_and_recovery_key_can_be_rotated_after_unlock(self) -> None:
        """A recovery unlock can be made durable again by replacing the normal passphrase."""

        root = Path(self.tempdir.name) / "encrypted-key-rotation"
        config = {
            "TESTING": True,
            "SECRET_KEY": "test-secret",
            "DATABASE": str(root / "merch.sqlite3"),
            "USERS_DATABASE": str(root / "users.sqlite3"),
            "BACKUP_DIR": str(root / "backups"),
            "RESET_ARCHIVE_DIR": str(root / "reset-archives"),
            "INVOICE_UPLOAD_DIR": str(root / "invoices"),
            "DATABASE_ENCRYPTION_ENABLED": True,
            "ADMIN_USERNAME": "rotation-admin",
            "ADMIN_PASSWORD": "rotation-bootstrap-password",
            "APP_VERSION": "v0.3.0",
            "AUTO_BACKUP": False,
        }
        configured_app = create_app(config)
        with configured_app.app_context():
            _, old_recovery_key = setup_encrypted_databases(
                configured_app,
                bootstrap_password="rotation-bootstrap-password",
                database_passphrase="first database passphrase",
                confirmation="first database passphrase",
            )
            change_database_passphrase(
                configured_app,
                passphrase="second database passphrase",
                confirmation="second database passphrase",
            )
            _, new_recovery_key = regenerate_database_recovery_key(configured_app)

        restarted = create_app(config)
        with restarted.app_context():
            with self.assertRaises(ValueError):
                unlock_encrypted_databases(restarted, database_passphrase="first database passphrase")
            self.assertEqual(
                unlock_encrypted_databases(restarted, database_passphrase="second database passphrase"),
                "passphrase",
            )

        restarted_again = create_app(config)
        with restarted_again.app_context():
            with self.assertRaises(ValueError):
                unlock_encrypted_databases(restarted_again, recovery_key=old_recovery_key)
            self.assertEqual(
                unlock_encrypted_databases(restarted_again, recovery_key=new_recovery_key),
                "recovery",
            )

    def test_existing_database_gets_minimum_stock_and_offered_columns(self) -> None:
        """An update must not require manually recreating the merch database."""

        legacy_database = Path(self.tempdir.name) / "legacy.sqlite3"
        connection = sqlite3.connect(legacy_database)
        try:
            connection.execute(
                """
                CREATE TABLE articles (
                    id INTEGER PRIMARY KEY,
                    name TEXT NOT NULL COLLATE NOCASE UNIQUE,
                    default_sale_price_cents INTEGER NOT NULL DEFAULT 0,
                    default_purchase_price_cents INTEGER NOT NULL DEFAULT 0,
                    is_active INTEGER NOT NULL DEFAULT 1,
                    created_at TEXT NOT NULL,
                    updated_at TEXT NOT NULL
                )
                """
            )
            connection.execute(
                """
                CREATE TABLE variants (
                    id INTEGER PRIMARY KEY,
                    article_id INTEGER NOT NULL,
                    option_value_ids_json TEXT NOT NULL DEFAULT '[]',
                    combination_key TEXT NOT NULL,
                    sale_price_cents INTEGER NOT NULL DEFAULT 0,
                    default_purchase_price_cents INTEGER NOT NULL DEFAULT 0,
                    no_reorder INTEGER NOT NULL DEFAULT 0,
                    is_active INTEGER NOT NULL DEFAULT 1,
                    created_at TEXT NOT NULL,
                    updated_at TEXT NOT NULL,
                    UNIQUE(article_id, combination_key)
                )
                """
            )
            connection.execute(
                """
                INSERT INTO articles (
                    id, name, default_sale_price_cents, default_purchase_price_cents,
                    is_active, created_at, updated_at
                ) VALUES (1, 'Legacy Shirt', 0, 0, 1, '2026-01-01T00:00:00+00:00', '2026-01-01T00:00:00+00:00')
                """
            )
            connection.execute(
                """
                INSERT INTO variants (
                    id, article_id, option_value_ids_json, combination_key,
                    sale_price_cents, default_purchase_price_cents, no_reorder,
                    is_active, created_at, updated_at
                ) VALUES (1, 1, '[]', '', 0, 0, 1, 1, '2026-01-01T00:00:00+00:00', '2026-01-01T00:00:00+00:00')
                """
            )
            connection.commit()
        finally:
            connection.close()

        legacy_app = create_app(
            {
                "TESTING": True,
                "SECRET_KEY": "test-secret",
                "DATABASE": str(legacy_database),
                "BACKUP_DIR": str(Path(self.tempdir.name) / "legacy-backups"),
                "ADMIN_USERNAME": "tester",
                "ADMIN_PASSWORD": "test-password",
                "AUTO_BACKUP": False,
            }
        )
        with legacy_app.app_context():
            variant_columns = {row["name"] for row in get_db().execute("PRAGMA table_info(variants)").fetchall()}
            article_columns = {row["name"] for row in get_db().execute("PRAGMA table_info(articles)").fetchall()}
            legacy_variant_is_offered = get_db().execute(
                "SELECT is_offered FROM variants WHERE id = 1"
            ).fetchone()[0]
        self.assertIn("minimum_stock", variant_columns)
        self.assertIn("is_offered", variant_columns)
        self.assertIn("is_offered", article_columns)
        self.assertTrue(legacy_variant_is_offered)

    def test_existing_single_admin_table_is_upgraded_to_roles_and_security_columns(self) -> None:
        """The existing deployed admin becomes the unique Admin without a data reset."""

        legacy_database = Path(self.tempdir.name) / "legacy-users.sqlite3"
        connection = sqlite3.connect(legacy_database)
        try:
            connection.execute(
                """
                CREATE TABLE users (
                    id INTEGER PRIMARY KEY,
                    username TEXT NOT NULL UNIQUE COLLATE NOCASE,
                    password_hash TEXT NOT NULL,
                    is_admin INTEGER NOT NULL DEFAULT 1,
                    created_at TEXT NOT NULL
                )
                """
            )
            connection.execute(
                """
                INSERT INTO users (id, username, password_hash, is_admin, created_at)
                VALUES (1, 'old-admin', 'unused', 1, '2026-08-14T00:00:00+00:00')
                """
            )
            connection.commit()
        finally:
            connection.close()

        legacy_app = create_app(
            {
                "TESTING": True,
                "SECRET_KEY": "test-secret",
                "DATABASE": str(legacy_database),
                "USERS_DATABASE": str(Path(self.tempdir.name) / "legacy-user-accounts.sqlite3"),
                "BACKUP_DIR": str(Path(self.tempdir.name) / "legacy-user-backups"),
                "RESET_ARCHIVE_DIR": str(Path(self.tempdir.name) / "legacy-user-reset-archives"),
                "INVOICE_UPLOAD_DIR": str(Path(self.tempdir.name) / "legacy-user-invoices"),
                "ADMIN_USERNAME": "tester",
                "ADMIN_PASSWORD": "test-password",
                "APP_VERSION": "v0.3.0",
                "AUTO_BACKUP": False,
            }
        )
        with legacy_app.app_context():
            columns = {row["name"] for row in get_user_db().execute("PRAGMA table_info(users)").fetchall()}
            user = get_user_db().execute("SELECT * FROM users WHERE id = 1").fetchone()
            tables = {row["name"] for row in get_db().execute("SELECT name FROM sqlite_master WHERE type = 'table'").fetchall()}
        self.assertTrue(
            {
                "role",
                "is_active",
                "must_set_password",
                "setup_code_hash",
                "mfa_secret_encrypted",
                "mfa_enabled",
                "session_version",
                "ui_theme",
                "ui_language",
                "show_variant_photos",
            }.issubset(columns)
        )
        self.assertEqual(user["role"], "admin")
        self.assertTrue(user["is_admin"])
        self.assertTrue(user["is_active"])
        self.assertEqual(user["ui_theme"], "aurora")
        self.assertEqual(user["ui_language"], "de")
        self.assertFalse(user["show_variant_photos"])
        self.assertIn("sync_events", tables)
        self.assertNotIn("users", tables)
        self.assertTrue(list(Path(legacy_app.config["MIGRATION_ARCHIVE_DIR"]).glob("*.zip")))

    def test_combined_database_split_keeps_bookings_and_actor_snapshots(self) -> None:
        """The one-time migration preserves IDs, rows and a readable actor name."""

        legacy_database = Path(self.tempdir.name) / "combined.sqlite3"
        connection = sqlite3.connect(legacy_database)
        try:
            connection.executescript(LEGACY_COMBINED_SCHEMA_SQL)
            connection.execute(
                """
                INSERT INTO users (id, username, password_hash, is_admin, role, is_active, created_at)
                VALUES (7, 'historic-seller', 'unused', 1, 'admin', 1, '2026-08-14T00:00:00+00:00')
                """
            )
            connection.execute(
                """
                INSERT INTO articles (id, name, created_at, updated_at)
                VALUES (3, 'Historisches Shirt', '2026-08-14T00:00:00+00:00', '2026-08-14T00:00:00+00:00')
                """
            )
            connection.execute(
                """
                INSERT INTO variants (
                    id, article_id, option_value_ids_json, combination_key,
                    sale_price_cents, default_purchase_price_cents, created_at, updated_at
                ) VALUES (11, 3, '[]', '', 2500, 1200, '2026-08-14T00:00:00+00:00', '2026-08-14T00:00:00+00:00')
                """
            )
            connection.execute(
                """
                INSERT INTO sales (
                    id, receipt_id, variant_id, quantity, unit_price_cents, amount_due_cents,
                    payment_method, is_paid, is_received, delivery_status, event_name, sold_on, created_at,
                    created_by, created_by_username
                ) VALUES (19, 'V-20260814-001', 11, 1, 2500, 2500, 'Bar', 1, 1,
                          'not_applicable', 'Historisches Festival', '2026-08-14',
                          '2026-08-14T00:00:00+00:00', 7, NULL)
                """
            )
            # Model the released combined schema before global events existed.
            # The split migration must recreate and seed both tables itself.
            connection.execute("DROP TABLE sale_event_state")
            connection.execute("DROP TABLE sale_events")
            connection.commit()
        finally:
            connection.close()

        split_app = create_app(
            {
                "TESTING": True,
                "SECRET_KEY": "test-secret",
                "DATABASE": str(legacy_database),
                "USERS_DATABASE": str(Path(self.tempdir.name) / "split-users.sqlite3"),
                "BACKUP_DIR": str(Path(self.tempdir.name) / "split-backups"),
                "RESET_ARCHIVE_DIR": str(Path(self.tempdir.name) / "split-reset-archives"),
                "INVOICE_UPLOAD_DIR": str(Path(self.tempdir.name) / "split-invoices"),
                "ADMIN_USERNAME": "tester",
                "ADMIN_PASSWORD": "test-password",
                "APP_VERSION": "v0.3.0",
                "AUTO_BACKUP": False,
            }
        )
        with split_app.app_context():
            actor = get_user_db().execute("SELECT id, username FROM users WHERE id = 7").fetchone()
            sale = get_db().execute(
                "SELECT id, variant_id, created_by, created_by_username FROM sales WHERE id = 19"
            ).fetchone()
            event = get_db().execute(
                "SELECT id, name FROM sale_events WHERE name = 'Historisches Festival'"
            ).fetchone()
            selected_event = get_db().execute(
                "SELECT event_id FROM sale_event_state WHERE id = 1"
            ).fetchone()
            operational_tables = {
                row["name"]
                for row in get_db().execute("SELECT name FROM sqlite_master WHERE type = 'table'").fetchall()
            }
        self.assertEqual(dict(actor), {"id": 7, "username": "historic-seller"})
        self.assertEqual(dict(sale), {"id": 19, "variant_id": 11, "created_by": 7, "created_by_username": "historic-seller"})
        self.assertEqual(event["name"], "Historisches Festival")
        self.assertEqual(selected_event["event_id"], event["id"])
        self.assertNotIn("users", operational_tables)
        archives = list(Path(split_app.config["MIGRATION_ARCHIVE_DIR"]).glob("*.zip"))
        self.assertEqual(len(archives), 1)
        with ZipFile(archives[0]) as archive:
            self.assertIn("data/merch.sqlite3", archive.namelist())

    def test_existing_operational_sales_seed_the_shared_event_catalogue(self) -> None:
        """A separated legacy database derives its first global event from sales."""

        variant_id = self.seed_variant("Event-Migration-Shirt")
        first = self.api_post(
            "/api/sales",
            {
                "variant_id": variant_id,
                "quantity": 1,
                "payment_method": "Bar",
                "sold_on": "2026-05-01",
                "event_name": "Frühes Festival",
            },
        )
        second = self.api_post(
            "/api/sales",
            {
                "variant_id": variant_id,
                "quantity": 1,
                "payment_method": "Bar",
                "sold_on": "2026-06-01",
                "event_name": "Spätes Festival",
            },
        )
        self.assertEqual(first.status_code, 200)
        self.assertEqual(second.status_code, 200)
        with self.app.app_context():
            connection = get_db()
            connection.execute(
                "UPDATE sales SET created_at = '2026-05-01T12:00:00+00:00' WHERE event_name = 'Frühes Festival'"
            )
            connection.execute(
                "UPDATE sales SET created_at = '2026-06-01T12:00:00+00:00' WHERE event_name = 'Spätes Festival'"
            )
            # This reproduces a deployment from before the new catalogue
            # tables existed; the restart must add them without a data reset.
            connection.execute("DROP TABLE sale_event_state")
            connection.execute("DROP TABLE sale_events")
            connection.commit()

        restarted = create_app(
            {
                "TESTING": True,
                "SECRET_KEY": "test-secret",
                "DATABASE": self.app.config["DATABASE"],
                "USERS_DATABASE": self.app.config["USERS_DATABASE"],
                "BACKUP_DIR": self.app.config["BACKUP_DIR"],
                "RESET_ARCHIVE_DIR": self.app.config["RESET_ARCHIVE_DIR"],
                "INVOICE_UPLOAD_DIR": self.app.config["INVOICE_UPLOAD_DIR"],
                "VARIANT_PHOTO_UPLOAD_DIR": self.app.config["VARIANT_PHOTO_UPLOAD_DIR"],
                "ADMIN_USERNAME": "tester",
                "ADMIN_PASSWORD": "test-password",
                "AUTO_BACKUP": False,
            }
        )
        with restarted.app_context():
            catalogue = get_db().execute("SELECT name FROM sale_events ORDER BY id").fetchall()
            current = get_db().execute(
                """
                SELECT e.name
                FROM sale_event_state state
                JOIN sale_events e ON e.id = state.event_id
                WHERE state.id = 1
                """
            ).fetchone()
        self.assertCountEqual(
            [row["name"] for row in catalogue], ["Frühes Festival", "Spätes Festival"]
        )
        self.assertEqual(current["name"], "Spätes Festival")

    def test_existing_purchase_table_gets_invoice_attachment_column(self) -> None:
        """A deployed database upgrades without losing its free-text reference."""

        legacy_database = Path(self.tempdir.name) / "legacy-purchases.sqlite3"
        connection = sqlite3.connect(legacy_database)
        try:
            # A real deployed purchase table always references existing
            # catalogue rows.  The small fixture needs only that primary key
            # for the migration's reconstructed foreign key.
            connection.execute(
                "CREATE TABLE variants (id INTEGER PRIMARY KEY, article_id INTEGER NOT NULL DEFAULT 1, is_active INTEGER NOT NULL DEFAULT 1)"
            )
            connection.execute("INSERT INTO variants (id) VALUES (1)")
            connection.execute(
                """
                CREATE TABLE purchases (
                    id INTEGER PRIMARY KEY,
                    receipt_id TEXT NOT NULL UNIQUE,
                    variant_id INTEGER NOT NULL,
                    quantity INTEGER NOT NULL,
                    unit_cost_cents INTEGER NOT NULL,
                    purchased_on TEXT NOT NULL,
                    supplier TEXT,
                    invoice_reference TEXT,
                    comment TEXT,
                    created_at TEXT NOT NULL,
                    created_by INTEGER
                )
                """
            )
            connection.execute(
                """
                INSERT INTO purchases (
                    receipt_id, variant_id, quantity, unit_cost_cents, purchased_on,
                    invoice_reference, created_at
                ) VALUES ('E-20260814-001', 1, 1, 1100, '2026-08-14', 'ALT-42', '2026-08-14T00:00:00+00:00')
                """
            )
            connection.execute(
                """
                INSERT INTO purchases (
                    receipt_id, variant_id, quantity, unit_cost_cents, purchased_on,
                    invoice_reference, created_at
                ) VALUES ('E-20260814-002', 1, 2, 900, '2026-08-14', 'ALT-43', '2026-08-14T00:01:00+00:00')
                """
            )
            connection.commit()
        finally:
            connection.close()

        legacy_app = create_app(
            {
                "TESTING": True,
                "SECRET_KEY": "test-secret",
                "DATABASE": str(legacy_database),
                "BACKUP_DIR": str(Path(self.tempdir.name) / "legacy-purchase-backups"),
                "INVOICE_UPLOAD_DIR": str(Path(self.tempdir.name) / "legacy-purchase-invoices"),
                "ADMIN_USERNAME": "tester",
                "ADMIN_PASSWORD": "test-password",
                "AUTO_BACKUP": False,
            }
        )
        with legacy_app.app_context():
            columns = {row["name"] for row in get_db().execute("PRAGMA table_info(purchases)").fetchall()}
            references = get_db().execute(
                "SELECT receipt_id, invoice_reference, invoice_file_path FROM purchases ORDER BY id"
            ).fetchall()
        self.assertIn("invoice_file_path", columns)
        self.assertEqual([row["receipt_id"] for row in references], ["E-20260814-001", "E-20260814-001"])
        self.assertEqual([row["invoice_reference"] for row in references], ["ALT-42", "ALT-43"])
        self.assertTrue(all(row["invoice_file_path"] is None for row in references))

    def test_existing_variant_photo_schema_defaults_photos_into_the_slideshow(self) -> None:
        """A deployment with the previous photo feature keeps every old photo selected."""

        root = Path(self.tempdir.name) / "photo-schema-migration"
        database = root / "merch.sqlite3"
        root.mkdir()
        connection = sqlite3.connect(database)
        try:
            connection.executescript(
                """
                CREATE TABLE variant_photos (
                    id INTEGER PRIMARY KEY,
                    variant_id INTEGER NOT NULL,
                    file_path TEXT NOT NULL UNIQUE,
                    original_filename TEXT NOT NULL,
                    position INTEGER NOT NULL DEFAULT 0,
                    created_at TEXT NOT NULL,
                    created_by INTEGER,
                    created_by_username TEXT
                );
                INSERT INTO variant_photos (variant_id, file_path, original_filename, position, created_at)
                VALUES (1, 'existing-photo.jpg', 'existing-photo.jpg', 0, '2026-08-14T00:00:00+00:00');
                """
            )
            connection.commit()
        finally:
            connection.close()

        migrated_app = create_app(
            {
                "TESTING": True,
                "SECRET_KEY": "test-secret",
                "DATABASE": str(database),
                "BACKUP_DIR": str(root / "backups"),
                "RESET_ARCHIVE_DIR": str(root / "reset-archives"),
                "INVOICE_UPLOAD_DIR": str(root / "invoices"),
                "ADMIN_USERNAME": "tester",
                "ADMIN_PASSWORD": "test-password",
                "APP_VERSION": "v0.3.0",
                "AUTO_BACKUP": False,
                "EMAIL_NOTIFICATIONS_ENABLED": False,
            }
        )
        with migrated_app.app_context():
            columns = {row["name"] for row in get_db().execute("PRAGMA table_info(variant_photos)").fetchall()}
            value = get_db().execute("SELECT include_in_slideshow FROM variant_photos WHERE id = 1").fetchone()[0]
            indexes = {row["name"] for row in get_db().execute("PRAGMA index_list(variant_photos)").fetchall()}
            tables = {row["name"] for row in get_db().execute("SELECT name FROM sqlite_master WHERE type = 'table'").fetchall()}
        self.assertIn("include_in_slideshow", columns)
        self.assertEqual(value, 1)
        self.assertIn("idx_variant_photos_slideshow", indexes)
        self.assertIn("slideshow_extra_photos", tables)

    def seed_variant(self, article_name: str = "Test Shirt") -> int:
        """Create an article with generic Farbe/Größe options and one variant."""

        with self.app.app_context():
            connection = get_db()
            cursor = connection.execute(
                """
                INSERT INTO articles (
                    name, default_sale_price_cents, default_purchase_price_cents, is_active, created_at, updated_at
                ) VALUES (?, 2000, 1100, 1, '2026-01-01T00:00:00+00:00', '2026-01-01T00:00:00+00:00')
                """,
                (article_name,),
            )
            article_id = cursor.lastrowid
            apply_option_configuration(
                connection,
                article_id,
                [
                    {"id": None, "name": "Farbe", "position": 0, "values": [{"id": None, "value": "schwarz", "position": 0}]},
                    {"id": None, "name": "Größe", "position": 1, "values": [{"id": None, "value": "M", "position": 0}]},
                ],
            )
            sync_variants(connection, article_id)
            variant_id = connection.execute("SELECT id FROM variants WHERE article_id = ?", (article_id,)).fetchone()[0]
            connection.commit()
            return variant_id

    def api_post(self, path: str, payload: dict):
        return self.client.post(
            path,
            json=payload,
            headers={"X-CSRF-Token": "test-csrf"},
        )

    def post_csv_import(self, import_kind: str, content: str, filename: str = "import.csv"):
        return self.client.post(
            f"/artikelverwaltung/import/{import_kind}",
            data={
                "csrf_token": "test-csrf",
                "csv_file": (io.BytesIO(content.encode("utf-8")), filename),
            },
            content_type="multipart/form-data",
        )

    def csrf_token(self) -> str:
        with self.client.session_transaction() as session:
            return session["csrf_token"]

    def become_user(self, user_id: int) -> None:
        """Switch a test browser to a direct, already authenticated session."""

        with self.client.session_transaction() as session:
            session.clear()
            session["user_id"] = user_id
            session["user_session_version"] = 0
            session["csrf_token"] = "test-csrf"

    def create_local_user(self, username: str, role: str) -> int:
        with self.app.app_context():
            connection = get_user_db()
            cursor = connection.execute(
                """
                INSERT INTO users (username, password_hash, is_admin, role, is_active, created_at)
                VALUES (?, ?, 0, ?, 1, '2026-08-14T00:00:00+00:00')
                """,
                (username, generate_password_hash("test-password"), role),
            )
            connection.commit()
            return int(cursor.lastrowid)

    @patch("app.fetch_latest_github_release")
    def test_admin_update_check_detects_and_caches_a_new_release(self, fetch_release) -> None:
        """The first post-login check is cached; the explicit button refreshes it."""

        fetch_release.return_value = {
            "tag_name": "v0.3.1",
            "name": "Merch Manager v0.3.1",
            "html_url": "https://github.com/TAWilts/protovibe-merch/releases/tag/v0.3.1",
            "published_at": "2026-08-14T15:30:00Z",
        }

        first = self.client.get("/api/update-status")
        self.assertEqual(first.status_code, 200)
        self.assertTrue(first.json["ok"])
        self.assertTrue(first.json["update_available"])
        self.assertEqual(first.json["current_version"], "v0.3.0")
        self.assertEqual(first.json["latest_version"], "v0.3.1")
        self.assertFalse(first.json["cached"])
        self.assertEqual(fetch_release.call_count, 1)

        cached = self.client.get("/api/update-status")
        self.assertEqual(cached.status_code, 200)
        self.assertTrue(cached.json["cached"])
        self.assertEqual(fetch_release.call_count, 1)

        forced = self.client.get("/api/update-status?force=1")
        self.assertEqual(forced.status_code, 200)
        self.assertFalse(forced.json["cached"])
        self.assertEqual(fetch_release.call_count, 2)

    @patch("app.fetch_latest_github_release")
    def test_update_check_handles_invalid_release_tags_without_breaking_the_app(self, fetch_release) -> None:
        fetch_release.return_value = {"tag_name": "latest", "html_url": "https://github.com/example/release"}

        response = self.client.get("/api/update-status?force=1")
        self.assertEqual(response.status_code, 200)
        self.assertFalse(response.json["ok"])
        self.assertEqual(response.json["state"], "unavailable")
        self.assertFalse(response.json["update_available"])

    def test_updates_page_is_admin_only_and_header_contains_the_version_link(self) -> None:
        updates_page = self.client.get("/updates")
        self.assertEqual(updates_page.status_code, 200)
        updates_html = updates_page.get_data(as_text=True)
        self.assertIn("Jetzt nach Updates suchen", updates_html)
        self.assertIn('data-update-panel', updates_html)

        sales_html = self.client.get("/verkauf").get_data(as_text=True)
        self.assertIn('data-update-indicator', sales_html)
        self.assertIn('static/updates.js', sales_html)

        with self.app.app_context():
            connection = get_user_db()
            password_hash = connection.execute("SELECT password_hash FROM users WHERE id = 1").fetchone()[0]
            connection.execute(
                "INSERT INTO users (username, password_hash, is_admin, created_at) VALUES (?, ?, 0, ?)",
                ("non-admin", password_hash, "2026-08-14T00:00:00+00:00"),
            )
            connection.commit()
        with self.client.session_transaction() as session:
            session["user_id"] = 2
            session["user_session_version"] = 0
            session["csrf_token"] = "test-csrf"
        self.assertEqual(self.client.get("/updates").status_code, 403)

        with self.client.session_transaction() as session:
            session.clear()
        unauthenticated = self.client.get("/api/update-status")
        self.assertEqual(unauthenticated.status_code, 401)

    def test_admin_creates_user_who_sets_a_private_password_on_first_login(self) -> None:
        """A setup credential is one-time only and never becomes the password."""

        response = self.client.post(
            "/verwaltung/benutzer",
            data={"csrf_token": "test-csrf", "username": "seller-one", "role": "seller"},
        )
        self.assertEqual(response.status_code, 200)
        match = re.search(r'data-setup-code>([^<]+)</code>', response.get_data(as_text=True))
        self.assertIsNotNone(match)
        setup_code = match.group(1)

        with self.app.app_context():
            created = get_user_db().execute("SELECT * FROM users WHERE username = 'seller-one'").fetchone()
        self.assertTrue(created["must_set_password"])
        self.assertTrue(created["setup_code_hash"])
        self.assertFalse(check_password_hash(created["password_hash"], setup_code))

        with self.client.session_transaction() as session:
            session.clear()
            session["csrf_token"] = "login-token"
        login = self.client.post(
            "/login",
            data={"csrf_token": "login-token", "username": "seller-one", "password": setup_code},
        )
        self.assertEqual(login.status_code, 302)
        self.assertTrue(login.location.endswith("/konto/einrichten"))

        setup = self.client.post(
            "/konto/einrichten",
            data={
                "csrf_token": self.csrf_token(),
                "password": "a-private-password",
                "password_confirmation": "a-private-password",
            },
        )
        self.assertEqual(setup.status_code, 302)
        self.assertTrue(setup.location.endswith("/verkauf"))
        with self.app.app_context():
            created = get_user_db().execute("SELECT * FROM users WHERE username = 'seller-one'").fetchone()
        self.assertFalse(created["must_set_password"])
        self.assertIsNone(created["setup_code_hash"])
        self.assertTrue(check_password_hash(created["password_hash"], "a-private-password"))
        self.assertIn('value="seller-one"', self.client.get("/verkauf").get_data(as_text=True))

    def test_admin_login_requires_mfa_and_accepts_totp_after_enrolment(self) -> None:
        """The sole admin cannot complete a password-only login."""

        with self.client.session_transaction() as session:
            session.clear()
            session["csrf_token"] = "login-token"
        password_login = self.client.post(
            "/login",
            data={"csrf_token": "login-token", "username": "tester", "password": "test-password"},
        )
        self.assertEqual(password_login.status_code, 302)
        self.assertTrue(password_login.location.endswith("/mfa/einrichten"))
        self.assertEqual(self.client.get("/mfa/einrichten").status_code, 200)

        with self.app.app_context():
            enrolled = get_user_db().execute("SELECT * FROM users WHERE id = 1").fetchone()
            pending_secret = decrypt_mfa_secret(enrolled["mfa_pending_secret_encrypted"], self.app)
        self.assertIsNotNone(pending_secret)
        activation = self.client.post(
            "/mfa/einrichten",
            data={"csrf_token": self.csrf_token(), "mfa_code": pyotp.TOTP(pending_secret).now()},
        )
        self.assertEqual(activation.status_code, 200)
        self.assertIn("Wiederherstellungscodes", activation.get_data(as_text=True))
        with self.app.app_context():
            enrolled = get_user_db().execute("SELECT * FROM users WHERE id = 1").fetchone()
        self.assertTrue(enrolled["mfa_enabled"])
        self.assertEqual(decrypt_mfa_secret(enrolled["mfa_secret_encrypted"], self.app), pending_secret)

        self.client.post("/logout", data={"csrf_token": self.csrf_token()})
        with self.client.session_transaction() as session:
            session["csrf_token"] = "login-again"
        password_login = self.client.post(
            "/login",
            data={"csrf_token": "login-again", "username": "tester", "password": "test-password"},
        )
        self.assertEqual(password_login.status_code, 302)
        self.assertTrue(password_login.location.endswith("/mfa/anmelden"))
        second_factor = self.client.post(
            "/mfa/anmelden",
            data={"csrf_token": self.csrf_token(), "mfa_code": pyotp.TOTP(pending_secret).now()},
        )
        self.assertEqual(second_factor.status_code, 302)
        self.assertTrue(second_factor.location.endswith("/verkauf"))

    def test_pre_upgrade_session_is_expired_before_admin_can_bypass_mfa_setup(self) -> None:
        """Old session cookies cannot keep an Admin logged in around the new MFA rule."""

        with self.client.session_transaction() as session:
            session.clear()
            session["user_id"] = 1
            session["csrf_token"] = "old-session-token"
        response = self.client.get("/verwaltung")
        self.assertEqual(response.status_code, 302)
        self.assertIn("/login", response.location)

    def test_profile_requires_fresh_password_confirmation_before_view_or_password_change(self) -> None:
        blocked = self.client.get("/profil")
        self.assertEqual(blocked.status_code, 302)
        self.assertIn("/profil/zugriff", blocked.location)

        confirmation = self.client.post(
            "/profil/zugriff?next=/profil",
            data={"csrf_token": "test-csrf", "password": "test-password"},
        )
        self.assertEqual(confirmation.status_code, 302)
        self.assertTrue(confirmation.location.endswith("/profil"))
        self.assertEqual(self.client.get("/profil").status_code, 200)

        changed = self.client.post(
            "/profil/passwort",
            data={
                "csrf_token": "test-csrf",
                "current_password": "test-password",
                "password": "a-new-private-password",
                "password_confirmation": "a-new-private-password",
            },
        )
        self.assertEqual(changed.status_code, 302)
        with self.app.app_context():
            user = get_user_db().execute("SELECT * FROM users WHERE id = 1").fetchone()
        self.assertTrue(check_password_hash(user["password_hash"], "a-new-private-password"))
        with self.client.session_transaction() as session:
            self.assertEqual(session["user_session_version"], user["session_version"])

    def test_profile_username_can_be_changed_after_fresh_confirmation(self) -> None:
        """A profile re-auth is sufficient to rename the current local account."""

        confirmation = self.client.post(
            "/profil/zugriff?next=/profil",
            data={"csrf_token": "test-csrf", "password": "test-password"},
        )
        self.assertEqual(confirmation.status_code, 302)
        changed = self.client.post(
            "/profil/benutzername",
            data={"csrf_token": "test-csrf", "username": "tester-neu"},
        )
        self.assertEqual(changed.status_code, 302)
        with self.app.app_context():
            user = get_user_db().execute("SELECT * FROM users WHERE id = 1").fetchone()
            audit_row = get_user_db().execute(
                "SELECT details_json FROM audit_log WHERE action = 'change_username' ORDER BY id DESC LIMIT 1"
            ).fetchone()
        self.assertEqual(user["username"], "tester-neu")
        self.assertEqual(json.loads(audit_row["details_json"])["previous_username"], "tester")
        with self.client.session_transaction() as session:
            self.assertEqual(session["user_session_version"], user["session_version"])

    def test_profile_personalization_is_saved_per_user_and_applied_to_sales(self) -> None:
        confirmation = self.client.post(
            "/profil/zugriff?next=/profil",
            data={"csrf_token": "test-csrf", "password": "test-password"},
        )
        self.assertEqual(confirmation.status_code, 302)
        changed = self.client.post(
            "/profil/personalisierung",
            data={
                "csrf_token": "test-csrf",
                "ui_theme": "ocean",
                "ui_language": "en",
                "show_variant_photos": "on",
            },
        )
        self.assertEqual(changed.status_code, 302)
        with self.app.app_context():
            user = get_user_db().execute(
                "SELECT ui_theme, ui_language, show_variant_photos FROM users WHERE id = 1"
            ).fetchone()
        self.assertEqual(dict(user), {"ui_theme": "ocean", "ui_language": "en", "show_variant_photos": 1})

        sales_html = self.client.get("/verkauf").get_data(as_text=True)
        self.assertIn('data-theme="ocean"', sales_html)
        self.assertIn('lang="en"', sales_html)
        self.assertIn('id="variant-photo-preview"', sales_html)
        profile_html = self.client.get("/profil").get_data(as_text=True)
        self.assertIn("Appearance &amp; sales", profile_html)
        self.assertIn("Lagoon", profile_html)
        self.assertIn("Two-factor authentication", profile_html)
        self.assertIn("Change password", profile_html)

    def test_balance_analytics_rank_active_paid_sales_and_render_filters(self) -> None:
        """Insights expose income/profit values and the local balance controls."""

        shirt_variant = self.seed_variant("Analytics Shirt")
        hoodie_variant = self.seed_variant("Analytics Hoodie")
        self.assertEqual(
            self.api_post(
                "/api/sales",
                {
                    "variant_id": shirt_variant,
                    "quantity": 3,
                    "is_paid": True,
                    "is_received": True,
                    "payment_method": "Bar",
                    "amount_given": "65,00",
                    "sold_on": "2026-08-14",
                    "event_name": "Langeln",
                    "sold_by": "Tim",
                },
            ).status_code,
            200,
        )
        self.assertEqual(
            self.api_post(
                "/api/sales",
                {
                    "variant_id": hoodie_variant,
                    "quantity": 1,
                    "is_paid": False,
                    "is_received": True,
                    "payment_method": "PayPal",
                    "sold_on": "2026-08-15",
                    "customer_name": "Offen",
                    "customer_address": "Noch offen 1",
                    "event_name": "Langeln",
                    "sold_by": "Lena",
                },
            ).status_code,
            200,
        )
        with self.app.app_context():
            balances = balance_payload(get_db())
        analytics = balances["analytics"]
        self.assertEqual(analytics["top_selling_items"][0]["label"], "Analytics Shirt")
        self.assertEqual(analytics["top_selling_items"][0]["quantity"], 3)
        self.assertEqual(analytics["top_revenue_items"][0]["label"], "Analytics Shirt")
        self.assertEqual(analytics["top_revenue_items"][0]["profit_cents"], 3200)
        self.assertEqual(analytics["top_events"][0]["label"], "Langeln")
        self.assertEqual(analytics["top_events"][0]["profit_cents"], 3200)
        self.assertEqual(analytics["top_sellers"][0]["label"], "Tim")
        self.assertEqual(analytics["top_sellers"][0]["profit_cents"], 3200)
        self.assertEqual(analytics["daily_income"], [{"date": "2026-08-14", "income_cents": 6500}, {"date": "2026-08-15", "income_cents": 0}])

        balances_html = self.client.get("/bilanzen").get_data(as_text=True)
        history_html = self.client.get("/historie").get_data(as_text=True)
        purchases_html = self.client.get("/einkaeufe").get_data(as_text=True)
        operations_html = self.client.get("/vorgaenge").get_data(as_text=True)
        articles_html = self.client.get("/artikelverwaltung").get_data(as_text=True)
        self.assertIn("Einnahmenverlauf", balances_html)
        self.assertIn('data-balance-filter', balances_html)
        self.assertIn('data-balance-sort-key', balances_html)
        self.assertIn('data-balance-export="inventory"', balances_html)
        self.assertIn('data-ranking-mode="profit"', balances_html)
        self.assertIn('data-table-filter', history_html)
        self.assertIn('data-table-filter', purchases_html)
        self.assertIn('data-table-filter', operations_html)
        self.assertIn('data-table-filter', articles_html)
        self.assertIn("income-chart", (Path(__file__).parents[1] / "static" / "balances.js").read_text(encoding="utf-8"))
        self.assertIn("data-filter-linked", (Path(__file__).parents[1] / "static" / "table-filters.js").read_text(encoding="utf-8"))

    def test_roles_are_enforced_on_the_server_not_only_in_navigation(self) -> None:
        """Seller may view purchases but cannot mutate them through direct URLs."""

        variant_id = self.seed_variant()
        seller_id = self.create_local_user("seller-role", "seller")
        manager_id = self.create_local_user("manager-role", "manager")

        self.become_user(seller_id)
        purchase_page = self.client.get("/einkaeufe")
        self.assertEqual(purchase_page.status_code, 200)
        self.assertIn("Nur Lesezugriff", purchase_page.get_data(as_text=True))
        self.assertEqual(self.client.get("/artikelverwaltung").status_code, 403)
        self.assertEqual(self.client.get("/verwaltung").status_code, 403)
        self.assertEqual(
            self.post_csv_import(
                "verkaeufe",
                "Anzahl;Artikel;Optionen;Verkaufspreis;Verkauft an\n1;Direktimport;;1,00;Testkunde",
            ).status_code,
            403,
        )
        self.assertEqual(
            self.api_post(
                "/api/purchases",
                {"variant_id": variant_id, "quantity": 1, "unit_cost": "11", "purchased_on": "2026-08-14"},
            ).status_code,
            403,
        )
        self.assertEqual(
            self.api_post(
                "/api/sales",
                {
                    "variant_id": variant_id,
                    "quantity": 1,
                    "is_paid": True,
                    "is_received": True,
                    "payment_method": "Bar",
                    "sold_on": "2026-08-14",
                },
            ).status_code,
            200,
        )

        self.become_user(manager_id)
        self.assertEqual(self.client.get("/artikelverwaltung").status_code, 200)
        self.assertEqual(self.client.get("/verwaltung").status_code, 403)
        self.assertEqual(
            self.api_post(
                "/api/purchases",
                {"variant_id": variant_id, "quantity": 1, "unit_cost": "11", "purchased_on": "2026-08-14"},
            ).status_code,
            200,
        )

    def test_every_user_can_message_the_admin_but_only_admin_can_view_the_inbox(self) -> None:
        """Messages retain their sender snapshot and are rendered escaped in the private admin tab."""

        seller_id = self.create_local_user("message-seller", "seller")
        self.become_user(seller_id)
        sales_page = self.client.get("/verkauf").get_data(as_text=True)
        self.assertIn('data-admin-message-open', sales_page)
        self.assertIn('id="admin-message-dialog"', sales_page)
        self.assertIn("static/admin-messages.js", sales_page)
        self.assertLess(sales_page.index("data-admin-message-open"), sales_page.index("message-seller · Seller"))

        sent = self.client.post(
            "/admin-nachricht",
            data={
                "csrf_token": "test-csrf",
                "next": "/verkauf",
                "message_type": "issue",
                "subject": "Scanner <script>alert(1)</script>",
                "body": "Der Scanner verliert die Verbindung.\nBitte prüfen.",
            },
        )
        self.assertEqual(sent.status_code, 302)
        self.assertTrue(sent.location.endswith("/verkauf"))
        self.assertEqual(self.client.get("/verwaltung").status_code, 403)

        with self.app.app_context():
            connection = get_user_db()
            message = connection.execute("SELECT * FROM admin_messages").fetchone()
            audit_row = connection.execute(
                "SELECT * FROM audit_log WHERE action = 'send_admin_message'"
            ).fetchone()
        self.assertEqual(message["sender_user_id"], seller_id)
        self.assertEqual(message["sender_username"], "message-seller")
        self.assertEqual(message["message_type"], "issue")
        self.assertEqual(message["body"], "Der Scanner verliert die Verbindung.\nBitte prüfen.")
        self.assertEqual(audit_row["entity_id"], message["id"])

        self.become_user(1)
        admin_page = self.client.get("/verwaltung").get_data(as_text=True)
        self.assertIn("Nachrichten an den Admin", admin_page)
        self.assertIn("message-seller", admin_page)
        self.assertIn("Scanner &lt;script&gt;alert(1)&lt;/script&gt;", admin_page)
        self.assertNotIn("Scanner <script>alert(1)</script>", admin_page)
        self.assertIn("Der Scanner verliert die Verbindung.", admin_page)

    def test_invalid_admin_message_is_not_persisted(self) -> None:
        response = self.client.post(
            "/admin-nachricht",
            data={
                "csrf_token": "test-csrf",
                "next": "//example.invalid",
                "message_type": "other",
                "subject": "Test",
                "body": "Testnachricht",
            },
        )
        self.assertEqual(response.status_code, 302)
        self.assertTrue(response.location.endswith("/verkauf"))
        with self.app.app_context():
            count = get_user_db().execute("SELECT COUNT(*) FROM admin_messages").fetchone()[0]
        self.assertEqual(count, 0)

    def test_smtp_notification_uses_tls_without_exposing_credentials_in_status(self) -> None:
        config = {
            "EMAIL_NOTIFICATIONS_ENABLED": True,
            "SMTP_HOST": "smtp.example.test",
            "SMTP_PORT": "465",
            "SMTP_SECURITY": "ssl",
            "SMTP_USERNAME": "notifier@example.test",
            "SMTP_PASSWORD": "private-app-password",
            "SMTP_FROM": "notifier@example.test",
            "ADMIN_NOTIFICATION_EMAIL": "admin@example.test",
            "SMTP_TIMEOUT_SECONDS": "4",
        }
        status = smtp_notification_status(config)
        self.assertTrue(status["ready"])
        self.assertNotIn("SMTP_PASSWORD", status)
        self.assertNotIn("private-app-password", repr(status))

        with (
            patch("app.ssl.create_default_context") as create_context,
            patch("app.smtplib.SMTP_SSL") as smtp_factory,
        ):
            smtp_client = smtp_factory.return_value.__enter__.return_value
            send_smtp_notification(config, subject="Test\nHeader", body="Testinhalt")

        smtp_factory.assert_called_once_with(
            "smtp.example.test",
            465,
            timeout=4.0,
            context=create_context.return_value,
        )
        smtp_client.login.assert_called_once_with("notifier@example.test", "private-app-password")
        sent_message = smtp_client.send_message.call_args.args[0]
        self.assertEqual(str(sent_message["Subject"]), "Test Header")
        self.assertEqual(str(sent_message["To"]), "admin@example.test")

        config.update(SMTP_PORT="587", SMTP_SECURITY="starttls")
        with (
            patch("app.ssl.create_default_context") as create_context,
            patch("app.smtplib.SMTP") as smtp_factory,
        ):
            smtp_client = smtp_factory.return_value.__enter__.return_value
            send_smtp_notification(config, subject="STARTTLS-Test", body="Testinhalt")

        smtp_factory.assert_called_once_with("smtp.example.test", 587, timeout=4.0)
        self.assertEqual(smtp_client.ehlo.call_count, 2)
        smtp_client.starttls.assert_called_once_with(context=create_context.return_value)
        smtp_client.login.assert_called_once_with("notifier@example.test", "private-app-password")
        smtp_client.send_message.assert_called_once()

    def test_admin_message_email_is_optional_and_smtp_failure_keeps_the_message(self) -> None:
        self.app.config.update(
            EMAIL_NOTIFICATIONS_ENABLED=True,
            SMTP_HOST="smtp.example.test",
            SMTP_PORT=465,
            SMTP_SECURITY="ssl",
            SMTP_USERNAME="notifier@example.test",
            SMTP_PASSWORD="private-app-password",
            SMTP_FROM="notifier@example.test",
            ADMIN_NOTIFICATION_EMAIL="admin@example.test",
            SMTP_TIMEOUT_SECONDS=4,
        )
        seller_id = self.create_local_user("email-seller", "seller")
        self.become_user(seller_id)

        with patch("app.send_smtp_notification") as send_email:
            sent = self.client.post(
                "/admin-nachricht",
                data={
                    "csrf_token": "test-csrf",
                    "next": "/verkauf",
                    "message_type": "question",
                    "subject": "Erste Frage",
                    "body": "Bitte per E-Mail benachrichtigen.",
                },
            )
        self.assertEqual(sent.status_code, 302)
        send_email.assert_called_once()
        self.assertIn("email-seller", send_email.call_args.kwargs["body"])

        with patch("app.send_smtp_notification", side_effect=OSError("SMTP offline")):
            failed_email = self.client.post(
                "/admin-nachricht",
                data={
                    "csrf_token": "test-csrf",
                    "next": "/verkauf",
                    "message_type": "issue",
                    "subject": "Zweite Nachricht",
                    "body": "Diese Nachricht muss trotz SMTP-Ausfall erhalten bleiben.",
                },
            )
        self.assertEqual(failed_email.status_code, 302)
        with self.app.app_context():
            messages = get_user_db().execute(
                "SELECT subject FROM admin_messages ORDER BY id"
            ).fetchall()
        self.assertEqual([row["subject"] for row in messages], ["Erste Frage", "Zweite Nachricht"])

        self.become_user(1)
        admin_page = self.client.get("/verwaltung").get_data(as_text=True)
        self.assertIn("E-Mail bei neuen Nachrichten", admin_page)
        self.assertIn("admin@example.test", admin_page)
        self.assertIn("Test-E-Mail senden", admin_page)
        self.assertNotIn("private-app-password", admin_page)
        with patch("app.send_smtp_notification") as test_email:
            test_response = self.client.post(
                "/verwaltung/email/test",
                data={"csrf_token": "test-csrf"},
            )
        self.assertEqual(test_response.status_code, 302)
        test_email.assert_called_once()

        self.become_user(seller_id)
        self.assertEqual(
            self.client.post(
                "/verwaltung/email/test",
                data={"csrf_token": "test-csrf"},
            ).status_code,
            403,
        )

    def test_database_reset_archives_operations_and_preserves_all_accounts(self) -> None:
        """Reset is protected by password/TOTP but never touches user storage."""

        self.seed_variant()
        self.create_local_user("reset-manager", "manager")
        invoice_dir = Path(self.app.config["INVOICE_UPLOAD_DIR"])
        invoice_dir.mkdir(parents=True, exist_ok=True)
        (invoice_dir / "before-reset.pdf").write_bytes(b"%PDF-test")
        secret = pyotp.random_base32()
        with self.app.app_context():
            connection = get_user_db()
            connection.execute(
                """
                UPDATE users
                SET mfa_enabled = 1, mfa_secret_encrypted = ?,
                    mfa_recovery_code_hashes_json = '[]'
                WHERE id = 1
                """,
                (encrypt_mfa_secret(secret, self.app),),
            )
            connection.commit()

        reset = self.client.post(
            "/verwaltung/daten-zuruecksetzen",
            data={
                "csrf_token": "test-csrf",
                "password": "test-password",
                "mfa_code": pyotp.TOTP(secret).now(),
                "confirmation": "DATEN ZURÜCKSETZEN",
            },
        )
        self.assertEqual(reset.status_code, 302)
        self.assertTrue(reset.location.endswith("/login"))
        archives = list(Path(self.app.config["RESET_ARCHIVE_DIR"]).glob("*.zip"))
        self.assertEqual(len(archives), 1)
        with ZipFile(archives[0]) as archive:
            self.assertIn("data/merch.sqlite3", archive.namelist())
            self.assertIn("data/invoices/before-reset.pdf", archive.namelist())
            self.assertNotIn("data/users.sqlite3", archive.namelist())

        with self.app.app_context():
            users = get_user_db().execute("SELECT * FROM users ORDER BY id").fetchall()
            article_count = get_db().execute("SELECT COUNT(*) FROM articles").fetchone()[0]
        self.assertEqual(len(users), 2)
        self.assertEqual(users[0]["username"], "tester")
        self.assertEqual(users[0]["role"], "admin")
        self.assertTrue(users[0]["mfa_enabled"])
        self.assertEqual(decrypt_mfa_secret(users[0]["mfa_secret_encrypted"], self.app), secret)
        self.assertEqual(article_count, 0)

    def test_admin_can_delete_user_without_erasing_historic_sales(self) -> None:
        """A removed account leaves its immutable booking actor snapshot behind."""

        variant_id = self.seed_variant()
        seller_id = self.create_local_user("former-seller", "seller")
        self.become_user(seller_id)
        self.assertEqual(
            self.api_post(
                "/api/sales",
                {
                    "variant_id": variant_id,
                    "quantity": 1,
                    "is_paid": True,
                    "is_received": True,
                    "payment_method": "Bar",
                    "sold_on": "2026-08-14",
                },
            ).status_code,
            200,
        )
        secret = pyotp.random_base32()
        with self.app.app_context():
            user_connection = get_user_db()
            user_connection.execute(
                "UPDATE users SET mfa_enabled = 1, mfa_secret_encrypted = ? WHERE id = 1",
                (encrypt_mfa_secret(secret, self.app),),
            )
            user_connection.commit()
        self.become_user(1)

        deleted = self.client.post(
            f"/verwaltung/benutzer/{seller_id}/loeschen",
            data={
                "csrf_token": "test-csrf",
                "password": "test-password",
                "mfa_code": pyotp.TOTP(secret).now(),
                "confirmation": "BENUTZER LÖSCHEN",
            },
        )
        self.assertEqual(deleted.status_code, 302)
        with self.app.app_context():
            account = get_user_db().execute("SELECT id FROM users WHERE id = ?", (seller_id,)).fetchone()
            sale = get_db().execute(
                "SELECT created_by, created_by_username FROM sales ORDER BY id DESC LIMIT 1"
            ).fetchone()
        self.assertIsNone(account)
        self.assertEqual(dict(sale), {"created_by": seller_id, "created_by_username": "former-seller"})

    def test_admin_restores_selected_operational_backup_without_changing_accounts(self) -> None:
        """Restore replaces ledger/invoices and makes a safety backup, never users."""

        backup_variant_id = self.seed_variant("Backup Shirt")
        invoice_dir = Path(self.app.config["INVOICE_UPLOAD_DIR"])
        photo_dir = Path(self.app.config["VARIANT_PHOTO_UPLOAD_DIR"])
        invoice_dir.mkdir(parents=True, exist_ok=True)
        photo_dir.mkdir(parents=True, exist_ok=True)
        (invoice_dir / "at-backup.pdf").write_bytes(b"%PDF-backup")
        (photo_dir / "at-backup.jpg").write_bytes(b"\xff\xd8backup-photo")
        with self.app.app_context():
            connection = get_db()
            connection.execute(
                """
                INSERT INTO variant_photos (variant_id, file_path, original_filename, position, created_at)
                VALUES (?, 'at-backup.jpg', 'at-backup.jpg', 0, '2026-08-14T00:00:00+00:00')
                """,
                (backup_variant_id,),
            )
            connection.commit()
        restore_point = create_backup(self.app, force=True)
        self.assertIsNotNone(restore_point)
        admin_html = self.client.get("/verwaltung").get_data(as_text=True)
        self.assertIn("Sicherung wiederherstellen", admin_html)
        self.assertIn(restore_point.name, admin_html)
        self.seed_variant("Later Shirt")
        (invoice_dir / "after-backup.pdf").write_bytes(b"%PDF-later")
        (photo_dir / "after-backup.jpg").write_bytes(b"\xff\xd8later-photo")
        preserved_user_id = self.create_local_user("backup-manager", "manager")
        secret = pyotp.random_base32()
        with self.app.app_context():
            user_connection = get_user_db()
            user_connection.execute(
                "UPDATE users SET mfa_enabled = 1, mfa_secret_encrypted = ? WHERE id = 1",
                (encrypt_mfa_secret(secret, self.app),),
            )
            user_connection.commit()

        restored = self.client.post(
            "/verwaltung/backups/wiederherstellen",
            data={
                "csrf_token": "test-csrf",
                "backup_name": restore_point.name,
                "password": "test-password",
                "mfa_code": pyotp.TOTP(secret).now(),
                "confirmation": "SICHERUNG WIEDERHERSTELLEN",
            },
        )
        self.assertEqual(restored.status_code, 302)
        with self.app.app_context():
            article_names = [
                row[0] for row in get_db().execute("SELECT name FROM articles ORDER BY id").fetchall()
            ]
            preserved_account = get_user_db().execute(
                "SELECT username FROM users WHERE id = ?", (preserved_user_id,)
            ).fetchone()
        self.assertEqual(article_names, ["Backup Shirt"])
        self.assertEqual(preserved_account["username"], "backup-manager")
        self.assertTrue((invoice_dir / "at-backup.pdf").is_file())
        self.assertFalse((invoice_dir / "after-backup.pdf").exists())
        self.assertTrue((photo_dir / "at-backup.jpg").is_file())
        self.assertFalse((photo_dir / "after-backup.jpg").exists())
        self.assertGreaterEqual(len(list(Path(self.app.config["BACKUP_DIR"]).iterdir())), 2)

    def test_purchase_then_sale_updates_stock_and_creates_receipts(self) -> None:
        variant_id = self.seed_variant()
        purchase = self.api_post(
            "/api/purchases",
            {"variant_id": variant_id, "quantity": 4, "unit_cost": "11,00", "purchased_on": "2026-08-14"},
        )
        self.assertEqual(purchase.status_code, 200)
        self.assertTrue(purchase.json["receipt_id"].startswith("E-20260814-"))

        sale = self.api_post(
            "/api/sales",
            {
                "variant_id": variant_id,
                "quantity": 3,
                "is_paid": True,
                "is_received": True,
                "payment_method": "Bar",
                "sold_on": "2026-08-14",
            },
        )
        self.assertEqual(sale.status_code, 200)
        self.assertEqual(sale.json["amount_due_cents"], 6000)
        self.assertEqual(sale.json["stock_after_sale"], 1)
        with self.app.app_context():
            connection = get_db()
            stock = connection.execute(
                """
                SELECT COALESCE((SELECT SUM(quantity) FROM purchases WHERE variant_id = ?), 0)
                     - COALESCE((SELECT SUM(quantity) FROM sales WHERE variant_id = ?), 0)
                """,
                (variant_id, variant_id),
            ).fetchone()[0]
        self.assertEqual(stock, 1)

    def test_sale_can_be_recorded_when_the_ledger_has_no_stock(self) -> None:
        """A missing inventory booking must not block a real counter sale."""

        variant_id = self.seed_variant()
        sale = self.api_post(
            "/api/sales",
            {
                "variant_id": variant_id,
                "quantity": 2,
                "is_paid": True,
                "is_received": True,
                "payment_method": "Bar",
                "sold_on": "2026-08-14",
            },
        )
        self.assertEqual(sale.status_code, 200)
        self.assertEqual(sale.json["stock_after_sale"], -2)

    def test_basket_shares_one_receipt_and_supports_item_or_full_cancellation(self) -> None:
        """A cart is one receipt with independently cancellable ledger rows."""

        first_variant_id = self.seed_variant("Cart Shirt")
        second_variant_id = self.seed_variant("Cart Hoodie")
        sale = self.api_post(
            "/api/sales",
            {
                "items": [
                    {"variant_id": first_variant_id, "quantity": 1},
                    {"variant_id": second_variant_id, "quantity": 2},
                ],
                "is_paid": True,
                "is_received": True,
                "payment_method": "Bar",
                "amount_given": "65,00",
                "sold_on": "2026-08-14",
            },
        )
        self.assertEqual(sale.status_code, 200)
        self.assertEqual(sale.json["amount_due_cents"], 6000)
        self.assertEqual(sale.json["donation_cents"], 500)
        self.assertEqual(len(sale.json["items"]), 2)

        with self.app.app_context():
            connection = get_db()
            rows = connection.execute(
                """
                SELECT id, receipt_id, amount_due_cents, amount_given_cents,
                       donation_cents, is_cancelled
                FROM sales ORDER BY id
                """
            ).fetchall()
        self.assertEqual(len(rows), 2)
        self.assertEqual({row["receipt_id"] for row in rows}, {sale.json["receipt_id"]})
        self.assertEqual(sum(row["amount_due_cents"] for row in rows), 6000)
        self.assertEqual(sum(row["amount_given_cents"] for row in rows), 6500)
        self.assertEqual(sum(row["donation_cents"] for row in rows), 500)

        history = self.client.get("/historie").get_data(as_text=True)
        self.assertIn("Warenkorb (2 Artikel)", history)
        self.assertIn('data-cancel-scope="receipt"', history)
        self.assertIn('data-cancel-scope="item"', history)
        sales_script = (Path(__file__).parents[1] / "static" / "sales.js").read_text(encoding="utf-8")
        self.assertIn("const cartItems", sales_script)
        self.assertIn("items: cartItems.map", sales_script)
        history_script = (Path(__file__).parents[1] / "static" / "history.js").read_text(encoding="utf-8")
        self.assertIn("data-cart-toggle", history_script)

        item_cancellation = self.client.patch(
            f"/api/sales/{rows[0]['id']}/cancel",
            json={"scope": "item"},
            headers={"X-CSRF-Token": "test-csrf"},
        )
        self.assertEqual(item_cancellation.status_code, 200)
        self.assertEqual(item_cancellation.json["cancelled_item_count"], 1)
        with self.app.app_context():
            partial_states = [
                row[0] for row in get_db().execute("SELECT is_cancelled FROM sales ORDER BY id").fetchall()
            ]
        self.assertEqual(partial_states, [1, 0])

        # Deliberately address the already cancelled first item.  Cancelling at
        # receipt scope must still find and cancel the remaining cart line.
        receipt_cancellation = self.client.patch(
            f"/api/sales/{rows[0]['id']}/cancel",
            json={"scope": "receipt"},
            headers={"X-CSRF-Token": "test-csrf"},
        )
        self.assertEqual(receipt_cancellation.status_code, 200)
        self.assertEqual(receipt_cancellation.json["cancelled_item_count"], 1)
        with self.app.app_context():
            final_states = [
                row[0] for row in get_db().execute("SELECT is_cancelled FROM sales ORDER BY id").fetchall()
            ]
        self.assertEqual(final_states, [1, 1])

    def test_sale_accepts_an_explicit_unit_price_for_each_cart_line(self) -> None:
        """Discounted cart lines keep their actual, independently chosen prices."""

        first_variant_id = self.seed_variant("Discount Shirt")
        second_variant_id = self.seed_variant("Discount Hoodie")
        sale = self.api_post(
            "/api/sales",
            {
                "items": [
                    {"variant_id": first_variant_id, "quantity": 2, "unit_price": "14,50"},
                    {"variant_id": second_variant_id, "quantity": 1, "unit_price": "17,00"},
                ],
                "is_paid": True,
                "is_received": True,
                "payment_method": "Bar",
                "amount_given": "50,00",
                "sold_on": "2026-08-14",
            },
        )
        self.assertEqual(sale.status_code, 200)
        self.assertEqual(sale.json["amount_due_cents"], 4600)
        self.assertEqual(sale.json["donation_cents"], 400)
        self.assertEqual(
            {(item["variant_id"], item["unit_price_cents"], item["amount_due_cents"]) for item in sale.json["items"]},
            {(first_variant_id, 1450, 2900), (second_variant_id, 1700, 1700)},
        )

        with self.app.app_context():
            rows = get_db().execute(
                "SELECT variant_id, quantity, unit_price_cents, amount_due_cents FROM sales ORDER BY id"
            ).fetchall()
        self.assertEqual(
            {(row["variant_id"], row["quantity"], row["unit_price_cents"], row["amount_due_cents"]) for row in rows},
            {(first_variant_id, 2, 1450, 2900), (second_variant_id, 1, 1700, 1700)},
        )

    def test_sale_events_are_global_and_legacy_sale_payloads_remain_compatible(self) -> None:
        """Every seller shares the last selection, while old free-text sales still work."""

        first_event = self.api_post("/api/sale-events", {"name": "Sommerfest 2026"})
        self.assertEqual(first_event.status_code, 201)
        first_event_id = first_event.json["event"]["id"]
        self.assertEqual(first_event.json["current_event_id"], first_event_id)

        seller_id = self.create_local_user("event-seller", "seller")
        self.become_user(seller_id)
        seller_catalogue = self.client.get("/api/sale-events")
        self.assertEqual(seller_catalogue.status_code, 200)
        self.assertEqual(seller_catalogue.json["current_event_id"], first_event_id)
        seller_page = self.client.get("/verkauf").get_data(as_text=True)
        self.assertIn(f'<option value="{first_event_id}" selected>Sommerfest 2026</option>', seller_page)

        unknown_event = self.api_post("/api/sale-events/987654/select", {})
        self.assertEqual(unknown_event.status_code, 404)
        too_long_event = self.api_post("/api/sale-events", {"name": "x" * 121})
        self.assertEqual(too_long_event.status_code, 400)

        second_event = self.api_post("/api/sale-events", {"name": "Herbsthalle"})
        self.assertEqual(second_event.status_code, 201)
        second_event_id = second_event.json["event"]["id"]
        self.assertEqual(second_event.json["current_event_id"], second_event_id)

        selected = self.api_post(f"/api/sale-events/{first_event_id}/select", {})
        self.assertEqual(selected.status_code, 200)
        self.assertEqual(selected.json["current_event_id"], first_event_id)
        self.become_user(1)
        admin_catalogue = self.client.get("/api/sale-events")
        self.assertEqual(admin_catalogue.json["current_event_id"], first_event_id)
        sales_page = self.client.get("/verkauf").get_data(as_text=True)
        self.assertIn(
            f'<option value="{first_event_id}" selected>Sommerfest 2026</option>', sales_page
        )

        variant_id = self.seed_variant("Event API Shirt")
        canonical_sale = self.api_post(
            "/api/sales",
            {
                "variant_id": variant_id,
                "quantity": 1,
                "payment_method": "Bar",
                "sold_on": "2026-08-20",
                "event_id": first_event_id,
                "event_name": "Veraltete Schreibweise",
            },
        )
        # An offline request with an event ID from an earlier installation can
        # still fall back to its established event_name field.
        legacy_sale = self.api_post(
            "/api/sales",
            {
                "variant_id": variant_id,
                "quantity": 1,
                "payment_method": "Bar",
                "sold_on": "2026-08-21",
                "event_id": 987654,
                "event_name": "Offline Altbestand",
            },
        )
        self.assertEqual(canonical_sale.status_code, 200)
        self.assertEqual(legacy_sale.status_code, 200)
        with self.app.app_context():
            sales = get_db().execute("SELECT event_name FROM sales ORDER BY id DESC LIMIT 2").fetchall()
            current = get_db().execute("SELECT event_id FROM sale_event_state WHERE id = 1").fetchone()
            remembered = get_db().execute(
                "SELECT name FROM sale_events WHERE name = 'Offline Altbestand'"
            ).fetchone()
        self.assertEqual([row["event_name"] for row in reversed(sales)], ["Sommerfest 2026", "Offline Altbestand"])
        self.assertEqual(current["event_id"], first_event_id)
        self.assertEqual(remembered["name"], "Offline Altbestand")

    def test_offline_sale_event_is_idempotent_and_rejects_id_collisions(self) -> None:
        """A lost browser response may be retried, but must never duplicate a sale."""

        variant_id = self.seed_variant("Offline Shirt")
        payload = {
            "items": [{"variant_id": variant_id, "quantity": 2, "unit_price": "18,00"}],
            "is_paid": True,
            "is_received": True,
            "payment_method": "Bar",
            "sold_on": "2026-08-15",
            "event_name": "Ohne Empfang",
            "sold_by": "Tester",
            "client_event_id": "62f4cfe2-5205-4f0f-8317-4c90960763bb",
            "client_device_id": "fb73af63-1e9e-4a8f-a53f-1e1ed8af3a7d",
            "client_actor_id": 1,
            "client_created_at": "2026-08-15T17:42:00.000Z",
        }
        first = self.api_post("/api/sales", payload)
        with self.app.app_context():
            get_db().execute("UPDATE variants SET is_offered = 0 WHERE id = ?", (variant_id,))
            get_db().commit()
        retry = self.api_post("/api/sales", payload)
        self.assertEqual(first.status_code, 200)
        self.assertEqual(retry.status_code, 200)
        self.assertFalse(first.json.get("duplicate", False))
        self.assertTrue(retry.json["duplicate"])
        self.assertEqual(first.json["receipt_id"], retry.json["receipt_id"])
        with self.app.app_context():
            sale_count = get_db().execute("SELECT COUNT(*) FROM sales").fetchone()[0]
            event = get_db().execute("SELECT * FROM sync_events").fetchone()
        self.assertEqual(sale_count, 1)
        self.assertEqual(event["event_id"], payload["client_event_id"])
        self.assertEqual(event["actor_user_id"], 1)

        collision = {**payload, "items": [{"variant_id": variant_id, "quantity": 3, "unit_price": "18,00"}]}
        conflict = self.api_post("/api/sales", collision)
        self.assertEqual(conflict.status_code, 409)
        self.assertIn("bereits", conflict.json["error"])

    def test_sales_pwa_shell_is_exposed_without_caching_admin_pages(self) -> None:
        """The worker is root-scoped and the sales page loads the local outbox UI."""

        sales_html = self.client.get("/verkauf").get_data(as_text=True)
        worker = self.client.get("/service-worker.js")
        self.assertIn('rel="manifest"', sales_html)
        self.assertIn('id="offline-sync-panel"', sales_html)
        self.assertIn("static/offline-sales.js", sales_html)
        self.assertEqual(worker.status_code, 200)
        self.assertEqual(worker.headers["Service-Worker-Allowed"], "/")
        self.assertIn('url.pathname === "/verkauf"', worker.get_data(as_text=True))
        worker.close()
        offline_script = (Path(__file__).parents[1] / "static" / "offline-sales.js").read_text(encoding="utf-8")
        self.assertIn("client_event_id", offline_script)
        self.assertIn("syncPending", offline_script)

    def test_sales_mobile_details_and_automatic_sync_are_compact(self) -> None:
        """Optional sales fields collapse on phones while syncing stays automatic."""

        sales_html = self.client.get("/verkauf").get_data(as_text=True)
        self.assertIn('class="offline-sync-status"', sales_html)
        self.assertNotIn('id="sync-offline-sales"', sales_html)
        self.assertLess(sales_html.index('class="cart-section"'), sales_html.index('class="sale-additional-details"'))
        self.assertLess(sales_html.index('class="sale-additional-details"'), sales_html.index('id="sale-contact-details"'))
        self.assertLess(sales_html.index('id="sale-contact-details"'), sales_html.index('id="amount-given"'))
        self.assertIn('class="field-grid two-columns sale-payment-date"', sales_html)
        self.assertIn('id="sale-event"', sales_html)
        self.assertIn('id="sale-event-dialog"', sales_html)
        self.assertIn("Neue Veranstaltung", sales_html)
        self.assertEqual(sales_html.count("data-mobile-collapsed"), 2)

        offline_script = (Path(__file__).parents[1] / "static" / "offline-sales.js").read_text(encoding="utf-8")
        sales_script = (Path(__file__).parents[1] / "static" / "sales.js").read_text(encoding="utf-8")
        stylesheet = (Path(__file__).parents[1] / "static" / "app.css").read_text(encoding="utf-8")
        self.assertIn("RETRY_DELAYS_MS", offline_script)
        self.assertNotIn("sync-offline-sales", offline_script)
        self.assertIn("initializeResponsiveDetails", sales_script)
        self.assertIn("ui.contactDetails.open = true", sales_script)
        self.assertIn("selectedSaleEventPayload", sales_script)
        self.assertIn("/api/sale-events", sales_script)
        self.assertIn("event_id: selectedEvent.event_id", sales_script)
        self.assertIn("const confirmedValue = selectedSaleEventValue", sales_script)
        self.assertIn("refreshSaleEvents", sales_script)
        self.assertIn("window.setInterval(refreshVisibleSaleEvents, 15000)", sales_script)
        self.assertIn(".sale-payment-date { grid-template-columns: repeat(2, minmax(0, 1fr));", stylesheet)

    def test_transaction_price_inputs_are_prepopulated_from_variant_defaults(self) -> None:
        self.seed_variant()

        sales_html = self.client.get("/verkauf").get_data(as_text=True)
        purchases_html = self.client.get("/einkaeufe").get_data(as_text=True)
        self.assertIn('id="unit-price"', sales_html)
        self.assertIn("Standard-Verkaufspreis", sales_html)
        self.assertIn('id="unit-cost"', purchases_html)
        self.assertIn("Standard-Einkaufspreis", purchases_html)

        sales_script = (Path(__file__).parents[1] / "static" / "sales.js").read_text(encoding="utf-8")
        purchases_script = (Path(__file__).parents[1] / "static" / "purchases.js").read_text(encoding="utf-8")
        self.assertIn("variant.sale_price_cents", sales_script)
        self.assertIn("unit_price: window.MerchTransaction.centsToInput(item.unitPriceCents)", sales_script)
        self.assertIn("variant.default_purchase_price_cents", purchases_script)
        self.assertIn("ui.quantity.value = summary.quantity", purchases_script)
        self.assertNotIn("loadLastCost", purchases_script)

    def test_delivery_and_payment_queues_update_sale_statuses(self) -> None:
        """A later-delivery sale must move through both requested work queues."""

        variant_id = self.seed_variant()
        self.api_post(
            "/api/purchases",
            {"variant_id": variant_id, "quantity": 2, "unit_cost": "11", "purchased_on": "2026-08-14"},
        )
        # Create one unrelated counter sale first.  The next sale's primary key
        # must differ from the variant ID, otherwise a template that accidentally
        # sends the variant ID to the status API would not be detected here.
        self.api_post(
            "/api/sales",
            {
                "variant_id": variant_id,
                "quantity": 1,
                "is_paid": True,
                "is_received": True,
                "payment_method": "Bar",
                "sold_on": "2026-08-14",
            },
        )
        sale = self.api_post(
            "/api/sales",
            {
                "variant_id": variant_id,
                "quantity": 1,
                "is_paid": False,
                "is_received": False,
                "payment_method": "PayPal",
                "sold_on": "2026-08-14",
                "customer_name": "Ada Käuferin",
                "customer_address": "Bandstraße 1\n24103 Kiel",
            },
        )
        self.assertEqual(sale.status_code, 200)
        with self.app.app_context():
            sale_id = get_db().execute("SELECT id FROM sales ORDER BY id DESC").fetchone()[0]
            state = get_db().execute(
                "SELECT delivery_status, is_received, is_paid, payment_follow_up FROM sales WHERE id = ?", (sale_id,)
            ).fetchone()
        self.assertEqual(state["delivery_status"], "pending")
        self.assertFalse(state["is_received"])
        self.assertFalse(state["is_paid"])
        self.assertTrue(state["payment_follow_up"])

        queue = self.client.get("/vorgaenge")
        self.assertEqual(queue.status_code, 200)
        queue_html = queue.get_data(as_text=True)
        self.assertIn("Aktuelle Sendungen", queue_html)
        self.assertIn(sale.json["receipt_id"], queue_html)
        self.assertIn(f'data-sale-id="{sale_id}"', queue_html)

        delivery = self.client.patch(
            f"/api/sales/{sale_id}/delivery-status",
            json={"delivery_status": "received"},
            headers={"X-CSRF-Token": "test-csrf"},
        )
        self.assertEqual(delivery.status_code, 200)
        self.assertTrue(delivery.json["is_received"])

        payment = self.client.patch(
            f"/api/sales/{sale_id}/payment-status",
            json={"is_paid": True},
            headers={"X-CSRF-Token": "test-csrf"},
        )
        self.assertEqual(payment.status_code, 200)
        with self.app.app_context():
            state = get_db().execute(
                """
                SELECT delivery_status, is_received, is_paid, payment_follow_up,
                       amount_due_cents, amount_given_cents
                FROM sales WHERE id = ?
                """,
                (sale_id,),
            ).fetchone()
        self.assertEqual(state["delivery_status"], "received")
        self.assertTrue(state["is_received"])
        self.assertTrue(state["is_paid"])
        self.assertTrue(state["payment_follow_up"])
        self.assertEqual(state["amount_given_cents"], state["amount_due_cents"])
        history = self.client.get("/vorgaenge").get_data(as_text=True)
        self.assertIn("Bezahlte Verkäufe", history)
        self.assertIn(sale.json["receipt_id"], history)

    def test_operations_template_uses_stable_sale_ids_and_no_page_reload(self) -> None:
        """Queue controls address their own sale and move without form restoration."""

        variant_id = self.seed_variant()
        for payment_method in ("PayPal", "Überweisung", "PayPal"):
            response = self.api_post(
                "/api/sales",
                {
                    "variant_id": variant_id,
                    "quantity": 1,
                    "is_paid": False,
                    "is_received": False,
                    "payment_method": payment_method,
                    "sold_on": "2026-08-14",
                    "customer_name": "Ada Käuferin",
                    "customer_address": "Bandstraße 1\n24103 Kiel",
                },
            )
            self.assertEqual(response.status_code, 200)

        with self.app.app_context():
            sale_rows = get_db().execute("SELECT id, receipt_id FROM sales ORDER BY id").fetchall()
        queue_html = self.client.get("/vorgaenge").get_data(as_text=True)
        for sale_row in sale_rows:
            receipt_marker = f'<td class="mono">{sale_row["receipt_id"]}</td>'
            sale_id_marker = f'data-sale-id="{sale_row["id"]}"'
            start = queue_html.index(receipt_marker)
            self.assertIn(sale_id_marker, queue_html[start : start + 2000])

        operations_script = (Path(__file__).parents[1] / "static" / "operations.js").read_text(encoding="utf-8")
        self.assertNotIn("window.location.reload", operations_script)
        self.assertIn("moveRowToQueue", operations_script)

    def test_new_article_starts_with_common_colour_and_size_defaults(self) -> None:
        response = self.client.post(
            "/artikelverwaltung/neu",
            data={"csrf_token": "test-csrf"},
            follow_redirects=False,
        )
        self.assertEqual(response.status_code, 302)
        with self.app.app_context():
            connection = get_db()
            article_id = connection.execute("SELECT id FROM articles WHERE name = 'Neuer Artikel'").fetchone()[0]
            groups = connection.execute(
                "SELECT id, name FROM option_groups WHERE article_id = ? ORDER BY position", (article_id,)
            ).fetchall()
            values_by_group = {
                group["name"]: [
                    row["value"]
                    for row in connection.execute(
                        "SELECT value FROM option_values WHERE option_group_id = ? ORDER BY position", (group["id"],)
                    ).fetchall()
                ]
                for group in groups
            }
            variant_count = connection.execute(
                "SELECT COUNT(*) FROM variants WHERE article_id = ? AND is_active = 1", (article_id,)
            ).fetchone()[0]
        self.assertEqual(values_by_group["Farbe"], ["Schwarz", "Weiß"])
        self.assertEqual(values_by_group["Größe"], ["S", "M", "L", "XL", "XXL"])
        self.assertEqual(variant_count, 10)

    def test_unreceived_sale_requires_full_contact_data(self) -> None:
        variant_id = self.seed_variant()
        self.api_post(
            "/api/purchases",
            {"variant_id": variant_id, "quantity": 1, "unit_cost": "11", "purchased_on": "2026-08-14"},
        )
        response = self.api_post(
            "/api/sales",
            {
                "variant_id": variant_id,
                "quantity": 1,
                "is_paid": True,
                "is_received": False,
                "payment_method": "PayPal",
                "sold_on": "2026-08-14",
                "customer_name": "Nur Name",
                "customer_address": "",
            },
        )
        self.assertEqual(response.status_code, 400)
        self.assertIn("Name und Adresse", response.json["error"])

    def test_unpaid_sale_requires_full_contact_data(self) -> None:
        variant_id = self.seed_variant()
        response = self.api_post(
            "/api/sales",
            {
                "variant_id": variant_id,
                "quantity": 1,
                "is_paid": False,
                "is_received": True,
                "payment_method": "PayPal",
                "sold_on": "2026-08-14",
            },
        )
        self.assertEqual(response.status_code, 400)
        self.assertIn("Name und Adresse", response.json["error"])

    def test_optional_seller_is_stored_and_exported(self) -> None:
        variant_id = self.seed_variant()
        sale = self.api_post(
            "/api/sales",
            {
                "variant_id": variant_id,
                "quantity": 1,
                "is_paid": True,
                "is_received": True,
                "payment_method": "Bar",
                "sold_on": "2026-08-14",
                "sold_by": "Lena",
            },
        )
        self.assertEqual(sale.status_code, 200)
        with self.app.app_context():
            sold_by = get_db().execute("SELECT sold_by FROM sales").fetchone()[0]
        self.assertEqual(sold_by, "Lena")

        history = self.client.get("/historie").get_data(as_text=True)
        self.assertIn("Verkauft von: Lena", history)
        exported_sales = self.client.get("/export/sales.csv").get_data(as_text=True)
        self.assertIn("Verkauft von", exported_sales)
        self.assertIn("Lena", exported_sales)

    def test_cancelling_a_sale_preserves_history_and_reverses_its_effects(self) -> None:
        """A storno must remain auditable but leave active ledgers and queues."""

        variant_id = self.seed_variant()
        sale = self.api_post(
            "/api/sales",
            {
                "variant_id": variant_id,
                "quantity": 1,
                "is_paid": False,
                "is_received": False,
                "payment_method": "PayPal",
                "sold_on": "2026-08-14",
                "customer_name": "Ada Käuferin",
                "customer_address": "Bandstraße 1\n24103 Kiel",
            },
        )
        self.assertEqual(sale.status_code, 200)
        with self.app.app_context():
            sale_id = get_db().execute("SELECT id FROM sales").fetchone()[0]

        cancellation = self.client.patch(
            f"/api/sales/{sale_id}/cancel",
            json={},
            headers={"X-CSRF-Token": "test-csrf"},
        )
        self.assertEqual(cancellation.status_code, 200)
        self.assertTrue(cancellation.json["is_cancelled"])
        duplicate = self.client.patch(
            f"/api/sales/{sale_id}/cancel",
            json={},
            headers={"X-CSRF-Token": "test-csrf"},
        )
        self.assertEqual(duplicate.status_code, 400)

        with self.app.app_context():
            connection = get_db()
            self.assertTrue(connection.execute("SELECT is_cancelled FROM sales WHERE id = ?", (sale_id,)).fetchone()[0])
            balances = balance_payload(connection)
        balance_row = next(row for row in balances["rows"] if row["variant_id"] == variant_id)
        self.assertEqual(balance_row["sold_quantity"], 0)
        self.assertEqual(balance_row["stock"], 0)
        self.assertEqual(balances["summary"]["outstanding_cents"], 0)
        self.assertEqual(balances["summary"]["pending_delivery_count"], 0)

        history = self.client.get("/historie").get_data(as_text=True)
        self.assertIn(sale.json["receipt_id"], history)
        self.assertIn("storniert", history)
        operations = self.client.get("/vorgaenge").get_data(as_text=True)
        self.assertNotIn(sale.json["receipt_id"], operations)

        history_script = (Path(__file__).parents[1] / "static" / "history.js").read_text(encoding="utf-8")
        self.assertIn("CONFIRMATION_SECONDS = 3", history_script)
        self.assertIn("Stornierung bestätigen", history_script)

    def test_minimum_stock_can_be_applied_to_all_and_overridden_per_variant(self) -> None:
        """A bulk threshold is a one-shot default, not a lock on variant values."""

        self.seed_variant()
        with self.app.app_context():
            connection = get_db()
            article_id = connection.execute("SELECT id FROM articles WHERE name = 'Test Shirt'").fetchone()[0]
            color_group = connection.execute(
                "SELECT id FROM option_groups WHERE article_id = ? AND name = 'Farbe'", (article_id,)
            ).fetchone()[0]
            size_group = connection.execute(
                "SELECT id FROM option_groups WHERE article_id = ? AND name = 'Größe'", (article_id,)
            ).fetchone()[0]
            color_value = connection.execute(
                "SELECT id, value FROM option_values WHERE option_group_id = ?", (color_group,)
            ).fetchone()
            size_value = connection.execute(
                "SELECT id, value FROM option_values WHERE option_group_id = ?", (size_group,)
            ).fetchone()
            option_payload = [
                {
                    "id": color_group,
                    "name": "Farbe",
                    "position": 0,
                    "values": [
                        {"id": color_value["id"], "value": color_value["value"], "position": 0},
                        {"id": None, "value": "weiß", "position": 1},
                    ],
                },
                {
                    "id": size_group,
                    "name": "Größe",
                    "position": 1,
                    "values": [{"id": size_value["id"], "value": size_value["value"], "position": 0}],
                },
            ]
            apply_option_configuration(connection, article_id, option_payload)
            sync_variants(connection, article_id)
            connection.commit()
            # Submit the now persistent IDs just as the browser does after an
            # option edit.  Reusing the initial ``None`` for Weiß would model
            # a second, newly added value instead of the same variant.
            option_payload = []
            for group in connection.execute(
                "SELECT id, name, position FROM option_groups WHERE article_id = ? ORDER BY position", (article_id,)
            ).fetchall():
                values = [
                    {"id": value["id"], "value": value["value"], "position": value["position"]}
                    for value in connection.execute(
                        "SELECT id, value, position FROM option_values WHERE option_group_id = ? ORDER BY position",
                        (group["id"],),
                    ).fetchall()
                ]
                option_payload.append(
                    {"id": group["id"], "name": group["name"], "position": group["position"], "values": values}
                )
            variants = connection.execute(
                "SELECT id FROM variants WHERE article_id = ? AND is_active = 1 ORDER BY id", (article_id,)
            ).fetchall()

        self.assertEqual(len(variants), 2)
        first_variant_id, second_variant_id = (row["id"] for row in variants)
        response = self.client.post(
            f"/artikelverwaltung/{article_id}/speichern",
            data={
                "csrf_token": "test-csrf",
                "name": "Test Shirt",
                "default_sale_price": "20,00",
                "default_purchase_price": "11,00",
                "options_json": json.dumps(option_payload),
                "apply_minimum_stock_to_all": "3",
                f"minimum_stock_{first_variant_id}": "5",
                f"minimum_stock_{second_variant_id}": "3",
            },
            follow_redirects=False,
        )
        self.assertEqual(response.status_code, 302)

        with self.app.app_context():
            connection = get_db()
            thresholds = {
                row["id"]: row["minimum_stock"]
                for row in connection.execute(
                    "SELECT id, minimum_stock FROM variants WHERE article_id = ? AND is_active = 1", (article_id,)
                ).fetchall()
            }
            balances = balance_payload(connection)
            _, inventory_headers, inventory_rows = csv_rows(connection, "inventory")
        self.assertEqual(thresholds, {first_variant_id: 5, second_variant_id: 3})
        self.assertEqual(balances["summary"]["minimum_stock_warning_count"], 2)
        self.assertTrue(all(row["minimum_stock_warning"] for row in balances["rows"]))
        self.assertIn("Mindestbestand", inventory_headers)
        self.assertTrue(
            all(row[inventory_headers.index("Mindestbestandswarnung")] == "ja" for row in inventory_rows)
        )
        self.assertTrue(all(row[inventory_headers.index("Angeboten")] == "ja" for row in inventory_rows))

        article_html = self.client.get(f"/artikelverwaltung?article={article_id}").get_data(as_text=True)
        self.assertIn('id="minimum-stock-for-all"', article_html)
        self.assertIn("Mindestbestandswarnungen", self.client.get("/bilanzen").get_data(as_text=True))
        article_script = (Path(__file__).parents[1] / "static" / "articles.js").read_text(encoding="utf-8")
        self.assertIn("renderVariantTable", article_script)
        self.assertIn("apply-minimum-stock-to-all", article_script)
        self.assertIn("syncFromInputs: false", article_script)
        sales_script = (Path(__file__).parents[1] / "static" / "sales.js").read_text(encoding="utf-8")
        self.assertIn("Mindestbestandswarnung", sales_script)

    def test_balance_default_sort_follows_configured_variant_order(self) -> None:
        """Default balance order keeps S, M, L, XL instead of alphabetising labels."""

        first_variant_id = self.seed_variant("Ordered Shirt")
        with self.app.app_context():
            connection = get_db()
            article_id = connection.execute(
                "SELECT article_id FROM variants WHERE id = ?", (first_variant_id,)
            ).fetchone()[0]
            groups = connection.execute(
                "SELECT id, name, position FROM option_groups WHERE article_id = ? ORDER BY position, id",
                (article_id,),
            ).fetchall()
            color_group, size_group = groups
            color_value = connection.execute(
                "SELECT id, value FROM option_values WHERE option_group_id = ?", (color_group["id"],)
            ).fetchone()
            medium_value = connection.execute(
                "SELECT id, value FROM option_values WHERE option_group_id = ?", (size_group["id"],)
            ).fetchone()
            apply_option_configuration(
                connection,
                article_id,
                [
                    {
                        "id": color_group["id"],
                        "name": color_group["name"],
                        "position": 0,
                        "values": [{"id": color_value["id"], "value": color_value["value"], "position": 0}],
                    },
                    {
                        "id": size_group["id"],
                        "name": size_group["name"],
                        "position": 1,
                        "values": [
                            {"id": None, "value": "S", "position": 0},
                            {"id": medium_value["id"], "value": "M", "position": 1},
                            {"id": None, "value": "L", "position": 2},
                            {"id": None, "value": "XL", "position": 3},
                        ],
                    },
                ],
            )
            sync_variants(connection, article_id)
            connection.commit()
            balances = balance_payload(connection)

        self.assertEqual(
            [row["option_text"] for row in balances["reorder_rows"]],
            [
                "Farbe: schwarz · Größe: S",
                "Farbe: schwarz · Größe: M",
                "Farbe: schwarz · Größe: L",
                "Farbe: schwarz · Größe: XL",
            ],
        )
        balance_script = (Path(__file__).parents[1] / "static" / "balances.js").read_text(encoding="utf-8")
        self.assertIn('{ key: null, direction: "default" }', balance_script)
        self.assertIn('sort.direction === "default"', balance_script)
        self.assertIn('state.sort[view] = { key: null, direction: "default" }', balance_script)

    def test_article_option_values_can_be_reordered(self) -> None:
        """Each option keeps its own persisted value order, for example S through XL."""

        variant_id = self.seed_variant("Ordered Options Shirt")
        with self.app.app_context():
            connection = get_db()
            article_id = connection.execute(
                "SELECT article_id FROM variants WHERE id = ?", (variant_id,)
            ).fetchone()[0]
            group_rows = connection.execute(
                "SELECT id, name FROM option_groups WHERE article_id = ? ORDER BY position, id",
                (article_id,),
            ).fetchall()
            color_group, size_group = group_rows
            color_value = connection.execute(
                "SELECT id, value FROM option_values WHERE option_group_id = ?", (color_group["id"],)
            ).fetchone()
            medium_value = connection.execute(
                "SELECT id, value FROM option_values WHERE option_group_id = ?", (size_group["id"],)
            ).fetchone()
            apply_option_configuration(
                connection,
                article_id,
                [
                    {
                        "id": color_group["id"],
                        "name": color_group["name"],
                        "position": 0,
                        "values": [{"id": color_value["id"], "value": color_value["value"], "position": 0}],
                    },
                    {
                        "id": size_group["id"],
                        "name": size_group["name"],
                        "position": 1,
                        "values": [
                            {"id": medium_value["id"], "value": "M", "position": 0},
                            {"id": None, "value": "S", "position": 1},
                            {"id": None, "value": "L", "position": 2},
                            {"id": None, "value": "XL", "position": 3},
                        ],
                    },
                ],
            )
            sync_variants(connection, article_id)
            connection.commit()

            groups = []
            for group in connection.execute(
                "SELECT id, name FROM option_groups WHERE article_id = ? AND is_active = 1 ORDER BY position, id",
                (article_id,),
            ).fetchall():
                values = [
                    {"id": value["id"], "value": value["value"]}
                    for value in connection.execute(
                        "SELECT id, value FROM option_values WHERE option_group_id = ? AND is_active = 1 ORDER BY position, id",
                        (group["id"],),
                    ).fetchall()
                ]
                groups.append({"id": group["id"], "name": group["name"], "values": values})
            size_values_by_name = {value["value"]: value for value in groups[1]["values"]}
            groups[1]["values"] = [size_values_by_name[name] for name in ("S", "M", "L", "XL")]

        response = self.client.post(
            f"/artikelverwaltung/{article_id}/speichern",
            data={
                "csrf_token": "test-csrf",
                "name": "Ordered Options Shirt",
                "default_sale_price": "20,00",
                "default_purchase_price": "11,00",
                "options_json": json.dumps(groups),
            },
        )
        self.assertEqual(response.status_code, 302)

        with self.app.app_context():
            connection = get_db()
            persisted_groups = connection.execute(
                "SELECT name, position FROM option_groups WHERE article_id = ? AND is_active = 1 ORDER BY position, id",
                (article_id,),
            ).fetchall()
            persisted_sizes = connection.execute(
                """
                SELECT ov.value, ov.position
                FROM option_values ov
                JOIN option_groups og ON og.id = ov.option_group_id
                WHERE og.article_id = ? AND og.name = 'Größe' AND ov.is_active = 1
                ORDER BY ov.position, ov.id
                """,
                (article_id,),
            ).fetchall()
        self.assertEqual([row["name"] for row in persisted_groups], ["Farbe", "Größe"])
        self.assertEqual(
            [(row["value"], row["position"]) for row in persisted_sizes],
            [("S", 0), ("M", 1), ("L", 2), ("XL", 3)],
        )

        article_script = (Path(__file__).parents[1] / "static" / "articles.js").read_text(encoding="utf-8")
        self.assertIn("moveOptionValue", article_script)
        self.assertIn("Wert nach oben", article_script)
        self.assertIn("Wert nach unten", article_script)
        self.assertIn("moveOptionGroup", article_script)
        self.assertIn("Option nach oben verschieben", article_script)
        self.assertIn("Option nach unten verschieben", article_script)

    def test_sales_csv_import_creates_catalogue_and_withdraws_unlisted_combinations(self) -> None:
        """A manager can atomically build a catalogue and sales ledger from CSV."""

        csv_content = "\n".join(
            [
                "Anzahl;Artikel;Optionen;Verkaufspreis;Verkauft an",
                '2;CSV Shirt;"Größe=S;Farbe=Schwarz";20,00;Kundin A',
                '3;CSV Shirt;"Größe=M;Farbe=Schwarz";;Kunde B',
                '1;CSV Shirt;"Größe=S;Farbe=Weiß";22,00;Kundin C',
            ]
        )
        response = self.post_csv_import("verkaeufe", csv_content)
        self.assertEqual(response.status_code, 302)

        with self.app.app_context():
            connection = get_db()
            article = connection.execute("SELECT * FROM articles WHERE name = 'CSV Shirt'").fetchone()
            groups = connection.execute(
                "SELECT name, position FROM option_groups WHERE article_id = ? AND is_active = 1 ORDER BY position",
                (article["id"],),
            ).fetchall()
            variants = connection.execute(
                "SELECT id, sale_price_cents, is_offered FROM variants WHERE article_id = ? AND is_active = 1",
                (article["id"],),
            ).fetchall()
            labels = variant_label_map(connection, [variant["id"] for variant in variants])
            sales = connection.execute(
                "SELECT quantity, unit_price_cents, customer_name, comment FROM sales ORDER BY id"
            ).fetchall()
            audit_row = connection.execute(
                "SELECT details_json FROM audit_log WHERE action = 'import_csv' AND entity_type = 'sales'"
            ).fetchone()

        self.assertEqual([(row["name"], row["position"]) for row in groups], [("Größe", 0), ("Farbe", 1)])
        self.assertEqual(len(variants), 4)
        offered_labels = {
            labels[variant["id"]]["option_text"]
            for variant in variants
            if variant["is_offered"]
        }
        self.assertEqual(
            offered_labels,
            {
                "Größe: S · Farbe: Schwarz",
                "Größe: M · Farbe: Schwarz",
                "Größe: S · Farbe: Weiß",
            },
        )
        self.assertEqual(
            [(row["quantity"], row["unit_price_cents"], row["customer_name"]) for row in sales],
            [(2, 2000, "Kundin A"), (3, 2000, "Kunde B"), (1, 2200, "Kundin C")],
        )
        self.assertTrue(all(row["comment"] == "CSV-Import" for row in sales))
        self.assertEqual(json.loads(audit_row["details_json"])["fallback_price_count"], 1)

        page = self.client.get("/artikelverwaltung").get_data(as_text=True)
        self.assertIn("Einkäufe importieren", page)
        self.assertIn("Verkäufe importieren", page)
        self.assertIn("Anzahl;Artikel;Optionen;Verkaufspreis;Verkauft an", page)
        self.assertIn("static/csv-import.js", page)
        self.assertLess(page.index("Artikel bearbeiten"), page.index("<h2>CSV-Import</h2>"))

    def test_purchase_csv_import_uses_following_exact_price_and_keeps_existing_values(self) -> None:
        """A blank price may inherit from a following row of the same variant."""

        variant_id = self.seed_variant("CSV Purchase Shirt")
        csv_content = "\n".join(
            [
                "Anzahl;Artikel;Optionen;Einkaufspreis;Gekauft von",
                '4;CSV Purchase Shirt;"Farbe=schwarz;Größe=M";;Druckerei A',
                '5;CSV Purchase Shirt;"Farbe=schwarz;Größe=M";9,50;Druckerei B',
                '2;CSV Purchase Shirt;"Farbe=schwarz;Größe=L";12,00;Druckerei C',
            ]
        )
        response = self.post_csv_import("einkaeufe", csv_content)
        self.assertEqual(response.status_code, 302)

        with self.app.app_context():
            connection = get_db()
            article_id = connection.execute(
                "SELECT article_id FROM variants WHERE id = ?", (variant_id,)
            ).fetchone()[0]
            purchases = connection.execute(
                "SELECT quantity, unit_cost_cents, supplier FROM purchases ORDER BY id"
            ).fetchall()
            active_variants = connection.execute(
                "SELECT id, default_purchase_price_cents, is_offered FROM variants WHERE article_id = ? AND is_active = 1",
                (article_id,),
            ).fetchall()
            labels = variant_label_map(connection, [variant["id"] for variant in active_variants])

        self.assertEqual(
            [(row["quantity"], row["unit_cost_cents"], row["supplier"]) for row in purchases],
            [(4, 950, "Druckerei A"), (5, 950, "Druckerei B"), (2, 1200, "Druckerei C")],
        )
        self.assertEqual(
            {labels[row["id"]]["option_text"] for row in active_variants},
            {"Farbe: schwarz · Größe: M", "Farbe: schwarz · Größe: L"},
        )
        self.assertTrue(all(row["is_offered"] for row in active_variants))

    def test_invalid_csv_aborts_without_partial_catalogue_or_ledger_changes(self) -> None:
        """A malformed later row rolls back the whole file, including new articles."""

        csv_content = "\n".join(
            [
                "Anzahl;Artikel;Optionen;Verkaufspreis;Verkauft an",
                '2;Must Not Exist;"Größe=M";20,00;Kundin A',
                'keine-zahl;Must Not Exist;"Größe=L";21,00;Kunde B',
            ]
        )
        response = self.post_csv_import("verkaeufe", csv_content, "invalid.csv")
        self.assertEqual(response.status_code, 302)
        with self.app.app_context():
            connection = get_db()
            self.assertEqual(connection.execute("SELECT COUNT(*) FROM articles").fetchone()[0], 0)
            self.assertEqual(connection.execute("SELECT COUNT(*) FROM sales").fetchone()[0], 0)
            self.assertEqual(connection.execute("SELECT COUNT(*) FROM audit_log").fetchone()[0], 0)

        page = self.client.get("/artikelverwaltung").get_data(as_text=True)
        self.assertIn("CSV-Import abgebrochen", page)
        self.assertIn("Zeile 3", page)

    def test_article_and_variant_can_be_withdrawn_from_sales_assortment(self) -> None:
        """Withdrawing an item hides it from sale, not from inventory history."""

        variant_id = self.seed_variant()
        with self.app.app_context():
            connection = get_db()
            article_id = connection.execute(
                "SELECT article_id FROM variants WHERE id = ?", (variant_id,)
            ).fetchone()[0]
            option_payload = []
            for group in connection.execute(
                "SELECT id, name, position FROM option_groups WHERE article_id = ? ORDER BY position", (article_id,)
            ).fetchall():
                values = [
                    {"id": value["id"], "value": value["value"], "position": value["position"]}
                    for value in connection.execute(
                        "SELECT id, value, position FROM option_values WHERE option_group_id = ? ORDER BY position",
                        (group["id"],),
                    ).fetchall()
                ]
                option_payload.append(
                    {"id": group["id"], "name": group["name"], "position": group["position"], "values": values}
                )

        variant_withdrawal_form = {
            "csrf_token": "test-csrf",
            "name": "Test Shirt",
            "default_sale_price": "20,00",
            "default_purchase_price": "11,00",
            "options_json": json.dumps(option_payload),
            f"no_reorder_{variant_id}": "on",
            f"not_offered_{variant_id}": "on",
        }
        response = self.client.post(
            f"/artikelverwaltung/{article_id}/speichern",
            data=variant_withdrawal_form,
            follow_redirects=False,
        )
        self.assertEqual(response.status_code, 302)

        with self.app.app_context():
            connection = get_db()
            self.assertFalse(connection.execute("SELECT is_offered FROM variants WHERE id = ?", (variant_id,)).fetchone()[0])
            self.assertTrue(connection.execute("SELECT no_reorder FROM variants WHERE id = ?", (variant_id,)).fetchone()[0])
            _, article_headers, article_rows = csv_rows(connection, "articles")
            _, inventory_headers, inventory_rows = csv_rows(connection, "inventory")
            balances = balance_payload(connection)
        balance_row = next(row for row in balances["rows"] if row["variant_id"] == variant_id)
        self.assertFalse(balance_row["is_available_for_sale"])
        self.assertTrue(balance_row["no_reorder"])
        self.assertEqual([variant_id], [row["variant_id"] for row in balances["obsolete_rows"]])
        self.assertEqual([], balances["reorder_rows"])
        self.assertIn("Nachbestellen", article_headers)
        self.assertIn("Nachbestellen", inventory_headers)
        self.assertEqual("nein", next(row for row in inventory_rows if row[0] == "Test Shirt")[inventory_headers.index("Nachbestellen")])
        self.assertEqual(
            next(row for row in article_rows if row[2] == variant_id)[article_headers.index("Angeboten")], "nein"
        )

        sales_html = self.client.get("/verkauf").get_data(as_text=True)
        self.assertNotIn("Test Shirt", sales_html)
        rejected_sale = self.api_post(
            "/api/sales",
            {
                "variant_id": variant_id,
                "quantity": 1,
                "is_paid": True,
                "is_received": True,
                "payment_method": "Bar",
                "sold_on": "2026-08-14",
            },
        )
        self.assertEqual(rejected_sale.status_code, 400)
        self.assertIn("nicht mehr angeboten", rejected_sale.json["error"])

        # Discontinued variants remain available in the purchase workflow so
        # existing stock can still be entered or reconciled.
        purchase = self.api_post(
            "/api/purchases",
            {"variant_id": variant_id, "quantity": 2, "unit_cost": "11,00", "purchased_on": "2026-08-14"},
        )
        self.assertEqual(purchase.status_code, 200)

        article_withdrawal_form = dict(variant_withdrawal_form)
        article_withdrawal_form.pop(f"not_offered_{variant_id}")
        article_withdrawal_form["not_offered"] = "on"
        response = self.client.post(
            f"/artikelverwaltung/{article_id}/speichern",
            data=article_withdrawal_form,
            follow_redirects=False,
        )
        self.assertEqual(response.status_code, 302)
        with self.app.app_context():
            connection = get_db()
            self.assertFalse(connection.execute("SELECT is_offered FROM articles WHERE id = ?", (article_id,)).fetchone()[0])
            self.assertTrue(connection.execute("SELECT is_offered FROM variants WHERE id = ?", (variant_id,)).fetchone()[0])
        self.assertNotIn("Test Shirt", self.client.get("/verkauf").get_data(as_text=True))
        article_html = self.client.get(f"/artikelverwaltung?article={article_id}").get_data(as_text=True)
        self.assertIn("Artikel nicht mehr anbieten", article_html)
        self.assertIn("Nicht angeboten", article_html)

    def test_option_rename_is_visible_in_historic_labels(self) -> None:
        variant_id = self.seed_variant()
        with self.app.app_context():
            connection = get_db()
            article_id = connection.execute("SELECT article_id FROM variants WHERE id = ?", (variant_id,)).fetchone()[0]
            group = connection.execute(
                "SELECT id FROM option_groups WHERE article_id = ? AND name = 'Farbe'", (article_id,)
            ).fetchone()[0]
            value = connection.execute(
                "SELECT id FROM option_values WHERE option_group_id = ?", (group,)).fetchone()[0]
            size_group = connection.execute(
                "SELECT id FROM option_groups WHERE article_id = ? AND name = 'Größe'", (article_id,)
            ).fetchone()[0]
            size_value = connection.execute(
                "SELECT id, value FROM option_values WHERE option_group_id = ?", (size_group,)).fetchone()
            apply_option_configuration(
                connection,
                article_id,
                [
                    {"id": group, "name": "Farbe", "position": 0, "values": [{"id": value, "value": "Schwarz", "position": 0}]},
                    {"id": size_group, "name": "Größe", "position": 1, "values": [{"id": size_value["id"], "value": size_value["value"], "position": 0}]},
                ],
            )
            sync_variants(connection, article_id)
            connection.commit()
            label = variant_label_map(connection, [variant_id])[variant_id]["label"]
        self.assertIn("Farbe: Schwarz", label)

    def test_variant_photos_are_jpeg_files_and_fall_back_to_the_closest_variant(self) -> None:
        first_variant_id = self.seed_variant("Photo Shirt")
        with self.app.app_context():
            connection = get_db()
            article_id = connection.execute(
                "SELECT article_id FROM variants WHERE id = ?", (first_variant_id,)
            ).fetchone()[0]
            size_group_id = connection.execute(
                "SELECT id FROM option_groups WHERE article_id = ? AND name = 'Größe'", (article_id,)
            ).fetchone()[0]
            connection.execute(
                """
                INSERT INTO option_values (option_group_id, value, position, is_active, created_at, updated_at)
                VALUES (?, 'L', 1, 1, ?, ?)
                """,
                (size_group_id, "2026-08-14T00:00:00+00:00", "2026-08-14T00:00:00+00:00"),
            )
            sync_variants(connection, article_id)
            second_variant_id = connection.execute(
                "SELECT id FROM variants WHERE article_id = ? AND id != ? AND is_active = 1",
                (article_id, first_variant_id),
            ).fetchone()[0]
            connection.commit()

        source = io.BytesIO()
        Image.new("RGB", (2200, 1200), (24, 82, 140)).save(source, format="PNG")
        source.seek(0)
        uploaded = self.client.post(
            f"/api/varianten/{first_variant_id}/fotos",
            data={"photos": (source, "shirt.png")},
            headers={"X-CSRF-Token": "test-csrf"},
            content_type="multipart/form-data",
        )
        self.assertEqual(uploaded.status_code, 200)
        photo = uploaded.json["photos"][0]
        photo_id = photo["id"]
        self.assertTrue(photo["include_in_slideshow"])

        photo_dir = Path(self.app.config["VARIANT_PHOTO_UPLOAD_DIR"])
        stored_files = list(photo_dir.iterdir())
        self.assertEqual(len(stored_files), 1)
        self.assertEqual(stored_files[0].suffix.lower(), ".jpg")
        with Image.open(stored_files[0]) as stored_image:
            self.assertEqual(stored_image.format, "JPEG")
            self.assertLessEqual(max(stored_image.size), 1600)

        with self.app.app_context():
            photo_row = get_db().execute(
                "SELECT file_path, original_filename, include_in_slideshow FROM variant_photos WHERE id = ?", (photo_id,)
            ).fetchone()
            catalogue = article_payload(get_db(), offered_only=True, include_variant_photos=True)
            backup = create_backup(self.app, force=True)
        self.assertEqual(photo_row["file_path"], stored_files[0].name)
        self.assertEqual(photo_row["original_filename"], "shirt.png")
        self.assertTrue(photo_row["include_in_slideshow"])
        self.assertTrue((backup / "variant-photos" / stored_files[0].name).is_file())
        second_variant = next(
            variant
            for article in catalogue
            for variant in article["variants"]
            if variant["id"] == second_variant_id
        )
        self.assertTrue(second_variant["photo_is_fallback"])
        self.assertEqual(second_variant["photo_source_variant_id"], first_variant_id)
        self.assertEqual(second_variant["display_photos"][0]["id"], photo_id)

        served = self.client.get(photo["url"])
        self.assertEqual(served.status_code, 200)
        self.assertEqual(served.mimetype, "image/jpeg")
        deleted = self.client.delete(photo["url"], headers={"X-CSRF-Token": "test-csrf"})
        self.assertEqual(deleted.status_code, 200)
        self.assertFalse(stored_files[0].exists())
        with self.app.app_context():
            self.assertIsNone(get_db().execute("SELECT id FROM variant_photos WHERE id = ?", (photo_id,)).fetchone())

    def test_product_slideshow_gallery_persists_global_photo_selection(self) -> None:
        variant_id = self.seed_variant("Campaign Shirt")
        source = io.BytesIO()
        Image.new("RGB", (1200, 800), (184, 42, 129)).save(source, format="PNG")
        source.seek(0)
        uploaded = self.client.post(
            f"/api/varianten/{variant_id}/fotos",
            data={"photos": (source, "campaign.png")},
            headers={"X-CSRF-Token": "test-csrf"},
            content_type="multipart/form-data",
        )
        self.assertEqual(uploaded.status_code, 200)
        photo_id = uploaded.json["photos"][0]["id"]

        gallery = self.client.get("/produktpalette")
        self.assertEqual(gallery.status_code, 200)
        html = gallery.get_data(as_text=True)
        self.assertIn('href="/produktpalette"', html)
        self.assertIn(">Diashow<", html)
        self.assertIn('/static/slideshow.js', html)
        self.assertIn('value="other"', html)
        self.assertIn('id="slideshow-change-rate"', html)
        self.assertIn('id="slideshow-animation-speed"', html)
        self.assertIn('id="slideshow-animation-speed" type="range" min="0.1"', html)
        slideshow_response = self.client.get("/static/slideshow.js")
        slideshow_script = slideshow_response.get_data(as_text=True)
        slideshow_response.close()
        self.assertIn("function beginSlideExit()", slideshow_script)
        self.assertIn('frame.classList.add("is-leaving"', slideshow_script)
        match = re.search(
            r'<script id="product-slideshow-data" type="application/json">(.*?)</script>', html, flags=re.DOTALL
        )
        self.assertIsNotNone(match)
        payload = json.loads(match.group(1))
        gallery_photo = next(photo for photo in payload["photos"] if photo["id"] == photo_id)
        self.assertEqual(gallery_photo["article_name"], "Campaign Shirt")
        self.assertEqual(gallery_photo["sale_price_cents"], 2000)
        self.assertTrue(gallery_photo["include_in_slideshow"])

        changed = self.client.patch(
            f"/api/variantenfotos/{photo_id}/diashow",
            json={"include_in_slideshow": False},
            headers={"X-CSRF-Token": "test-csrf"},
        )
        self.assertEqual(changed.status_code, 200)
        self.assertFalse(changed.json["include_in_slideshow"])
        with self.app.app_context():
            photo = get_db().execute(
                "SELECT include_in_slideshow FROM variant_photos WHERE id = ?", (photo_id,)
            ).fetchone()
            audit_row = get_db().execute(
                "SELECT details_json FROM audit_log WHERE action = 'set_slideshow_photo_inclusion' ORDER BY id DESC LIMIT 1"
            ).fetchone()
        self.assertFalse(photo["include_in_slideshow"])
        self.assertFalse(json.loads(audit_row["details_json"])["include_in_slideshow"])

        refreshed = self.client.get("/produktpalette").get_data(as_text=True)
        refreshed_match = re.search(
            r'<script id="product-slideshow-data" type="application/json">(.*?)</script>', refreshed, flags=re.DOTALL
        )
        refreshed_payload = json.loads(refreshed_match.group(1))
        refreshed_photo = next(photo for photo in refreshed_payload["photos"] if photo["id"] == photo_id)
        self.assertFalse(refreshed_photo["include_in_slideshow"])

        rejected = self.client.patch(
            f"/api/variantenfotos/{photo_id}/diashow",
            json={"include_in_slideshow": "false"},
            headers={"X-CSRF-Token": "test-csrf"},
        )
        self.assertEqual(rejected.status_code, 400)

        other_source = io.BytesIO()
        Image.new("RGB", (900, 1400), (12, 116, 86)).save(other_source, format="PNG")
        other_source.seek(0)
        other_uploaded = self.client.post(
            "/api/diashow/fotos",
            data={"photos": (other_source, "preisliste.png")},
            headers={"X-CSRF-Token": "test-csrf"},
            content_type="multipart/form-data",
        )
        self.assertEqual(other_uploaded.status_code, 200)
        other_photo = other_uploaded.json["photos"][0]
        self.assertEqual(other_photo["kind"], "other")
        self.assertFalse(other_photo["is_product_photo"])
        self.assertTrue(other_photo["include_in_slideshow"])

        other_gallery = self.client.get("/produktpalette").get_data(as_text=True)
        other_match = re.search(
            r'<script id="product-slideshow-data" type="application/json">(.*?)</script>', other_gallery, flags=re.DOTALL
        )
        other_payload = json.loads(other_match.group(1))
        catalogue_other_photo = next(photo for photo in other_payload["photos"] if photo["key"] == other_photo["key"])
        self.assertEqual(catalogue_other_photo["original_filename"], "preisliste.png")
        self.assertNotIn("article_name", catalogue_other_photo)

        served_other = self.client.get(other_photo["url"])
        self.assertEqual(served_other.status_code, 200)
        self.assertEqual(served_other.mimetype, "image/jpeg")
        changed_other = self.client.patch(
            other_photo["url"],
            json={"include_in_slideshow": False},
            headers={"X-CSRF-Token": "test-csrf"},
        )
        self.assertEqual(changed_other.status_code, 200)
        self.assertFalse(changed_other.json["include_in_slideshow"])
        with self.app.app_context():
            other_row = get_db().execute(
                "SELECT file_path, include_in_slideshow FROM slideshow_extra_photos WHERE id = ?", (other_photo["id"],)
            ).fetchone()
        stored_other = Path(self.app.config["VARIANT_PHOTO_UPLOAD_DIR"]) / other_row["file_path"]
        self.assertFalse(other_row["include_in_slideshow"])
        self.assertTrue(stored_other.is_file())

        deleted_other = self.client.delete(other_photo["url"], headers={"X-CSRF-Token": "test-csrf"})
        self.assertEqual(deleted_other.status_code, 200)
        self.assertFalse(stored_other.exists())
        with self.app.app_context():
            self.assertIsNone(
                get_db().execute("SELECT id FROM slideshow_extra_photos WHERE id = ?", (other_photo["id"],)).fetchone()
            )

    def test_purchase_invoice_upload_edit_delete_and_backup(self) -> None:
        """Invoices are atomically attached, replaceable and recoverable."""

        first_variant_id = self.seed_variant("Invoice Shirt")
        second_variant_id = self.seed_variant("Invoice Hoodie")
        purchase = self.client.post(
            "/api/purchases",
            data={
                "variant_id": str(first_variant_id),
                "quantity": "4",
                "unit_cost": "11,00",
                "purchased_on": "2026-08-14",
                "supplier": "Merch Druck",
                "invoice_reference": "RG-42",
                "comment": "erste Lieferung",
                "invoice_file": (io.BytesIO(b"%PDF-1.7\nInvoice test\n"), "rechnung.pdf"),
            },
            headers={"X-CSRF-Token": "test-csrf"},
        )
        self.assertEqual(purchase.status_code, 200)
        self.assertTrue(purchase.json["has_invoice_file"])
        with self.app.app_context():
            connection = get_db()
            purchase_row = connection.execute("SELECT * FROM purchases").fetchone()
            purchase_id = purchase_row["id"]
            first_invoice_name = purchase_row["invoice_file_path"]
            first_invoice_path = Path(self.app.config["INVOICE_UPLOAD_DIR"]) / first_invoice_name
        self.assertTrue(first_invoice_path.is_file())
        invoice_response = self.client.get(f"/api/purchases/{purchase_id}/invoice")
        self.assertEqual(invoice_response.data, b"%PDF-1.7\nInvoice test\n")
        invoice_response.close()

        # A recovery snapshot contains the attachment matching its SQLite row.
        self.app.config["AUTO_BACKUP"] = True
        create_backup(self.app)
        backup_invoices = list(Path(self.app.config["BACKUP_DIR"]).glob("*/invoices/*"))
        self.assertEqual(len(backup_invoices), 1)
        self.assertEqual(backup_invoices[0].read_bytes(), b"%PDF-1.7\nInvoice test\n")
        self.app.config["AUTO_BACKUP"] = False

        page = self.client.get("/einkaeufe").get_data(as_text=True)
        self.assertIn("Alle Einkaufswarenkörbe", page)
        self.assertIn("Position öffnen", page)
        self.assertIn(f'data-edit-purchase data-purchase-id="{purchase_id}"', page)

        update = self.client.patch(
            f"/api/purchases/{purchase_id}",
            data={
                "variant_id": str(second_variant_id),
                "quantity": "2",
                "unit_cost": "13,50",
                "purchased_on": "2026-08-15",
                "supplier": "Merch Druck Nord",
                "invoice_reference": "RG-43",
                "comment": "korrigierte Lieferung",
                "invoice_file": (io.BytesIO(b"\x89PNG\r\n\x1a\nreplacement"), "rechnung-neu.png"),
            },
            headers={"X-CSRF-Token": "test-csrf"},
        )
        self.assertEqual(update.status_code, 200)
        with self.app.app_context():
            connection = get_db()
            updated = connection.execute("SELECT * FROM purchases WHERE id = ?", (purchase_id,)).fetchone()
            balances = balance_payload(connection)
            audit_actions = [row[0] for row in connection.execute("SELECT action FROM audit_log ORDER BY id").fetchall()]
        self.assertEqual(updated["variant_id"], second_variant_id)
        self.assertEqual(updated["quantity"], 2)
        self.assertEqual(updated["unit_cost_cents"], 1350)
        self.assertEqual(updated["invoice_reference"], "RG-43")
        self.assertFalse(first_invoice_path.exists())
        replacement_path = Path(self.app.config["INVOICE_UPLOAD_DIR"]) / updated["invoice_file_path"]
        self.assertTrue(replacement_path.is_file())
        stocks = {row["variant_id"]: row["stock"] for row in balances["rows"]}
        self.assertEqual(stocks[first_variant_id], 0)
        self.assertEqual(stocks[second_variant_id], 2)
        self.assertEqual(audit_actions[-1], "update")

        deletion = self.client.delete(
            f"/api/purchases/{purchase_id}", headers={"X-CSRF-Token": "test-csrf"}
        )
        self.assertEqual(deletion.status_code, 200)
        self.assertFalse(replacement_path.exists())
        with self.app.app_context():
            connection = get_db()
            self.assertIsNone(connection.execute("SELECT id FROM purchases WHERE id = ?", (purchase_id,)).fetchone())
            self.assertEqual(connection.execute("SELECT action FROM audit_log ORDER BY id DESC").fetchone()[0], "delete")

    def test_purchase_cart_groups_lines_and_keeps_item_and_cart_attachments_separate(self) -> None:
        """A multi-item purchase has one receipt but independently managed lines."""

        first_variant_id = self.seed_variant("Cart Invoice Shirt")
        second_variant_id = self.seed_variant("Cart Invoice Hoodie")
        created = self.client.post(
            "/api/purchases",
            data={
                "purchased_on": "2026-08-14",
                "items": json.dumps(
                    [
                        {
                            "variant_id": first_variant_id,
                            "quantity": 3,
                            "unit_cost": "11,00",
                            "supplier": "Druckerei A",
                            "invoice_reference": "POS-1",
                            "comment": "Shirts",
                        },
                        {
                            "variant_id": second_variant_id,
                            "quantity": 2,
                            "unit_cost": "22,50",
                            "supplier": "Druckerei B",
                            "invoice_reference": "POS-2",
                            "comment": "Hoodies",
                        },
                    ]
                ),
                "item_invoice_0": (io.BytesIO(b"%PDF-1.7\nitem invoice\n"), "position.pdf"),
                "cart_invoice_files": (io.BytesIO(b"%PDF-1.7\ncart invoice\n"), "warenkorb.pdf"),
            },
            headers={"X-CSRF-Token": "test-csrf"},
        )
        self.assertEqual(created.status_code, 200)
        self.assertEqual(created.json["item_count"], 2)
        self.assertEqual(created.json["cart_attachment_count"], 1)
        self.assertTrue(created.json["has_invoice_file"])
        receipt_id = created.json["receipt_id"]

        with self.app.app_context():
            connection = get_db()
            rows = connection.execute("SELECT * FROM purchases ORDER BY id").fetchall()
            attachment = connection.execute(
                "SELECT * FROM purchase_receipt_attachments WHERE receipt_id = ?", (receipt_id,)
            ).fetchone()
        self.assertEqual(len(rows), 2)
        self.assertEqual({row["receipt_id"] for row in rows}, {receipt_id})
        self.assertEqual([row["supplier"] for row in rows], ["Druckerei A", "Druckerei B"])
        self.assertTrue(rows[0]["invoice_file_path"])
        self.assertIsNone(rows[1]["invoice_file_path"])
        self.assertIsNotNone(attachment)

        page = self.client.get("/einkaeufe").get_data(as_text=True)
        self.assertIn("Warenkorb (2 Artikel)", page)
        self.assertIn('data-purchase-cart-toggle', page)
        self.assertIn('data-delete-purchase-cart data-receipt-id="' + receipt_id + '"', page)
        self.assertIn("Weitere Beleg-Anhänge", page)
        script = (Path(__file__).parents[1] / "static" / "purchases.js").read_text(encoding="utf-8")
        self.assertIn("cart_invoice_files", script)
        self.assertIn("data-purchase-cart-index", script)

        cart_invoice = self.client.get(
            f"/api/purchase-receipts/{receipt_id}/attachments/{attachment['id']}"
        )
        self.assertEqual(cart_invoice.status_code, 200)
        self.assertEqual(cart_invoice.data, b"%PDF-1.7\ncart invoice\n")
        cart_invoice.close()

        extra_attachment = self.client.post(
            f"/api/purchase-receipts/{receipt_id}/attachments",
            data={"cart_invoice_files": (io.BytesIO(b"\x89PNG\r\n\x1a\nextra"), "extra.png")},
            headers={"X-CSRF-Token": "test-csrf"},
        )
        self.assertEqual(extra_attachment.status_code, 200)
        self.assertEqual(extra_attachment.json["attachment_count"], 1)

        # The cart UI deliberately omits the date while editing one line; the
        # server must keep the receipt date rather than replacing it by today.
        edited = self.client.patch(
            f"/api/purchases/{rows[0]['id']}",
            data={
                "variant_id": str(first_variant_id),
                "quantity": "4",
                "unit_cost": "12,00",
                "supplier": "Druckerei A",
                "invoice_reference": "POS-1b",
                "comment": "Shirts korrigiert",
            },
            headers={"X-CSRF-Token": "test-csrf"},
        )
        self.assertEqual(edited.status_code, 200)
        with self.app.app_context():
            connection = get_db()
            edited_row = connection.execute("SELECT * FROM purchases WHERE id = ?", (rows[0]["id"],)).fetchone()
            attachment_count = connection.execute(
                "SELECT COUNT(*) FROM purchase_receipt_attachments WHERE receipt_id = ?", (receipt_id,)
            ).fetchone()[0]
        self.assertEqual(edited_row["purchased_on"], "2026-08-14")
        self.assertEqual(edited_row["quantity"], 4)
        self.assertEqual(attachment_count, 2)

        # Older clients still send a date while editing a single line.  That
        # correction must move the whole receipt, never split one cart across
        # two days.
        moved = self.client.patch(
            f"/api/purchases/{rows[0]['id']}",
            data={
                "variant_id": str(first_variant_id),
                "quantity": "4",
                "unit_cost": "12,00",
                "purchased_on": "2026-08-15",
                "supplier": "Druckerei A",
                "invoice_reference": "POS-1b",
                "comment": "Shirts korrigiert",
            },
            headers={"X-CSRF-Token": "test-csrf"},
        )
        self.assertEqual(moved.status_code, 200)
        with self.app.app_context():
            dates_in_cart = [
                row[0]
                for row in get_db().execute(
                    "SELECT DISTINCT purchased_on FROM purchases WHERE receipt_id = ?", (receipt_id,)
                ).fetchall()
            ]
        self.assertEqual(dates_in_cart, ["2026-08-15"])

        item_deletion = self.client.delete(
            f"/api/purchases/{rows[1]['id']}", headers={"X-CSRF-Token": "test-csrf"}
        )
        self.assertEqual(item_deletion.status_code, 200)
        self.assertFalse(item_deletion.json["receipt_deleted"])
        with self.app.app_context():
            connection = get_db()
            self.assertEqual(connection.execute("SELECT COUNT(*) FROM purchases").fetchone()[0], 1)
            self.assertEqual(
                connection.execute(
                    "SELECT COUNT(*) FROM purchase_receipt_attachments WHERE receipt_id = ?", (receipt_id,)
                ).fetchone()[0],
                2,
            )

        cart_deletion = self.client.delete(
            f"/api/purchase-receipts/{receipt_id}", headers={"X-CSRF-Token": "test-csrf"}
        )
        self.assertEqual(cart_deletion.status_code, 200)
        with self.app.app_context():
            connection = get_db()
            self.assertEqual(connection.execute("SELECT COUNT(*) FROM purchases").fetchone()[0], 0)
            self.assertEqual(connection.execute("SELECT COUNT(*) FROM purchase_receipt_attachments").fetchone()[0], 0)
        self.assertEqual(list(Path(self.app.config["INVOICE_UPLOAD_DIR"]).iterdir()), [])

    def test_purchase_page_shows_more_than_the_former_fifteen_rows(self) -> None:
        variant_id = self.seed_variant()
        receipt_ids = []
        for _ in range(16):
            response = self.api_post(
                "/api/purchases",
                {"variant_id": variant_id, "quantity": 1, "unit_cost": "11", "purchased_on": "2026-08-14"},
            )
            self.assertEqual(response.status_code, 200)
            receipt_ids.append(response.json["receipt_id"])

        page = self.client.get("/einkaeufe").get_data(as_text=True)
        self.assertIn("Alle Einkaufswarenkörbe", page)
        self.assertIn("16 Warenkörbe", page)
        self.assertIn(receipt_ids[0], page)
        self.assertIn(receipt_ids[-1], page)
        self.assertNotIn("Letzte Einkäufe", page)

    def test_purchase_rejects_non_invoice_uploads(self) -> None:
        variant_id = self.seed_variant()
        response = self.client.post(
            "/api/purchases",
            data={
                "variant_id": str(variant_id),
                "quantity": "1",
                "unit_cost": "11",
                "purchased_on": "2026-08-14",
                "invoice_file": (io.BytesIO(b"not an invoice"), "rechnung.txt"),
            },
            headers={"X-CSRF-Token": "test-csrf"},
        )
        self.assertEqual(response.status_code, 400)
        self.assertIn("PDF, PNG oder JPG", response.json["error"])
        with self.app.app_context():
            self.assertEqual(get_db().execute("SELECT COUNT(*) FROM purchases").fetchone()[0], 0)
        self.assertEqual(list(Path(self.app.config["INVOICE_UPLOAD_DIR"]).iterdir()), [])


if __name__ == "__main__":
    unittest.main()
