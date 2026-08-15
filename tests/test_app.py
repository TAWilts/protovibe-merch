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
from werkzeug.security import check_password_hash, generate_password_hash

from app import (
    LEGACY_COMBINED_SCHEMA_SQL,
    apply_option_configuration,
    balance_payload,
    create_backup,
    csv_rows,
    create_app,
    decrypt_mfa_secret,
    encrypt_mfa_secret,
    get_db,
    get_user_db,
    sync_variants,
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
            }.issubset(columns)
        )
        self.assertEqual(user["role"], "admin")
        self.assertTrue(user["is_admin"])
        self.assertTrue(user["is_active"])
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
                    payment_method, is_paid, is_received, delivery_status, sold_on, created_at,
                    created_by, created_by_username
                ) VALUES (19, 'V-20260814-001', 11, 1, 2500, 2500, 'Bar', 1, 1,
                          'not_applicable', '2026-08-14', '2026-08-14T00:00:00+00:00', 7, NULL)
                """
            )
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
            operational_tables = {
                row["name"]
                for row in get_db().execute("SELECT name FROM sqlite_master WHERE type = 'table'").fetchall()
            }
        self.assertEqual(dict(actor), {"id": 7, "username": "historic-seller"})
        self.assertEqual(dict(sale), {"id": 19, "variant_id": 11, "created_by": 7, "created_by_username": "historic-seller"})
        self.assertNotIn("users", operational_tables)
        archives = list(Path(split_app.config["MIGRATION_ARCHIVE_DIR"]).glob("*.zip"))
        self.assertEqual(len(archives), 1)
        with ZipFile(archives[0]) as archive:
            self.assertIn("data/merch.sqlite3", archive.namelist())

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

    def test_balance_analytics_rank_active_paid_sales_and_render_filters(self) -> None:
        """Insights ignore cancellations/open invoices and expose the local table search UI."""

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
        self.assertEqual(analytics["top_events"][0]["label"], "Langeln")
        self.assertEqual(analytics["top_sellers"][0]["label"], "Tim")
        self.assertEqual(analytics["daily_income"], [{"date": "2026-08-14", "income_cents": 6500}, {"date": "2026-08-15", "income_cents": 0}])

        balances_html = self.client.get("/bilanzen").get_data(as_text=True)
        history_html = self.client.get("/historie").get_data(as_text=True)
        purchases_html = self.client.get("/einkaeufe").get_data(as_text=True)
        operations_html = self.client.get("/vorgaenge").get_data(as_text=True)
        articles_html = self.client.get("/artikelverwaltung").get_data(as_text=True)
        self.assertIn("Einnahmenverlauf", balances_html)
        self.assertIn('data-table-filter', balances_html)
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

        self.seed_variant("Backup Shirt")
        invoice_dir = Path(self.app.config["INVOICE_UPLOAD_DIR"])
        invoice_dir.mkdir(parents=True, exist_ok=True)
        (invoice_dir / "at-backup.pdf").write_bytes(b"%PDF-backup")
        restore_point = create_backup(self.app, force=True)
        self.assertIsNotNone(restore_point)
        admin_html = self.client.get("/verwaltung").get_data(as_text=True)
        self.assertIn("Sicherung wiederherstellen", admin_html)
        self.assertIn(restore_point.name, admin_html)
        self.seed_variant("Later Shirt")
        (invoice_dir / "after-backup.pdf").write_bytes(b"%PDF-later")
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
            _, article_headers, article_rows = csv_rows(connection, "articles")
            balances = balance_payload(connection)
        balance_row = next(row for row in balances["rows"] if row["variant_id"] == variant_id)
        self.assertFalse(balance_row["is_available_for_sale"])
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
