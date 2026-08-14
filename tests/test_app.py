"""Regression tests for the business rules that must not silently change."""

from __future__ import annotations

import tempfile
import unittest
from pathlib import Path

from app import (
    apply_option_configuration,
    create_app,
    get_db,
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
                "ADMIN_USERNAME": "tester",
                "ADMIN_PASSWORD": "test-password",
                "AUTO_BACKUP": False,
            }
        )
        self.client = self.app.test_client()
        with self.client.session_transaction() as session:
            session["user_id"] = 1
            session["csrf_token"] = "test-csrf"

    def tearDown(self) -> None:
        self.tempdir.cleanup()

    def seed_variant(self) -> int:
        """Create an article with generic Farbe/Größe options and one variant."""

        with self.app.app_context():
            connection = get_db()
            cursor = connection.execute(
                """
                INSERT INTO articles (
                    name, default_sale_price_cents, default_purchase_price_cents, is_active, created_at, updated_at
                ) VALUES ('Test Shirt', 2000, 1100, 1, '2026-01-01T00:00:00+00:00', '2026-01-01T00:00:00+00:00')
                """
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


if __name__ == "__main__":
    unittest.main()
