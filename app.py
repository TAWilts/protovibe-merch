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

import base64
import csv
import hashlib
import io
import itertools
import json
import os
import re
import secrets
import shutil
import sqlite3
import string
import tempfile
import time
from collections import defaultdict
from datetime import date, datetime, timedelta, timezone
from decimal import Decimal, InvalidOperation, ROUND_HALF_UP
from functools import wraps
from pathlib import Path
from threading import Lock
from typing import Any, Iterable
from urllib.error import HTTPError, URLError
from urllib.request import Request, urlopen
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
from werkzeug.exceptions import RequestEntityTooLarge
from cryptography.fernet import Fernet, InvalidToken
import pyotp
import qrcode


SCHEMA_SQL = """
PRAGMA foreign_keys = ON;

CREATE TABLE IF NOT EXISTS users (
    id INTEGER PRIMARY KEY,
    username TEXT NOT NULL UNIQUE COLLATE NOCASE,
    password_hash TEXT NOT NULL,
    -- ``is_admin`` remains for safe upgrades from the first single-admin
    -- release.  New authorization decisions use the explicit role below.
    is_admin INTEGER NOT NULL DEFAULT 0,
    role TEXT NOT NULL DEFAULT 'seller' CHECK(role IN ('seller', 'manager', 'admin')),
    is_active INTEGER NOT NULL DEFAULT 1,
    must_set_password INTEGER NOT NULL DEFAULT 0,
    setup_code_hash TEXT,
    setup_code_expires_at TEXT,
    -- TOTP secrets are encrypted with a key derived from SECRET_KEY.  Recovery
    -- codes are one-way hashes because they only need to be compared once.
    mfa_secret_encrypted TEXT,
    mfa_pending_secret_encrypted TEXT,
    mfa_recovery_code_hashes_json TEXT NOT NULL DEFAULT '[]',
    mfa_enabled INTEGER NOT NULL DEFAULT 0,
    mfa_enrolled_at TEXT,
    session_version INTEGER NOT NULL DEFAULT 0,
    last_login_at TEXT,
    created_at TEXT NOT NULL
);

CREATE TABLE IF NOT EXISTS articles (
    id INTEGER PRIMARY KEY,
    name TEXT NOT NULL COLLATE NOCASE UNIQUE,
    default_sale_price_cents INTEGER NOT NULL DEFAULT 0 CHECK(default_sale_price_cents >= 0),
    default_purchase_price_cents INTEGER NOT NULL DEFAULT 0 CHECK(default_purchase_price_cents >= 0),
    -- Kept separate from is_active: an article can leave the assortment while
    -- its historic bookings and stock management remain fully available.
    is_offered INTEGER NOT NULL DEFAULT 1,
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
    -- NULL deliberately means that no minimum-stock warning is configured.
    -- This keeps an explicit threshold of zero meaningful: warn only once the
    -- variant is actually sold out.
    minimum_stock INTEGER CHECK(minimum_stock >= 0),
    -- Like articles, variants can be withdrawn from the sales assortment
    -- without being deleted or hidden from purchase/history workflows.
    is_offered INTEGER NOT NULL DEFAULT 1,
    no_reorder INTEGER NOT NULL DEFAULT 0,
    is_active INTEGER NOT NULL DEFAULT 1,
    created_at TEXT NOT NULL,
    updated_at TEXT NOT NULL,
    UNIQUE(article_id, combination_key)
);

CREATE TABLE IF NOT EXISTS purchases (
    id INTEGER PRIMARY KEY,
    -- One purchase receipt can contain several independently editable ledger
    -- lines, just like a sales cart.  The shared ID is therefore deliberately
    -- not unique per line.
    receipt_id TEXT NOT NULL,
    variant_id INTEGER NOT NULL REFERENCES variants(id),
    quantity INTEGER NOT NULL CHECK(quantity > 0),
    unit_cost_cents INTEGER NOT NULL CHECK(unit_cost_cents >= 0),
    purchased_on TEXT NOT NULL,
    supplier TEXT,
    invoice_reference TEXT,
    -- The server-managed filename of an optional PDF/image attachment.  Keep
    -- it distinct from ``invoice_reference`` so a typed invoice number stays
    -- useful even when a document is uploaded as well.
    invoice_file_path TEXT,
    comment TEXT,
    created_at TEXT NOT NULL,
    created_by INTEGER REFERENCES users(id)
);

CREATE TABLE IF NOT EXISTS purchase_receipt_attachments (
    id INTEGER PRIMARY KEY,
    receipt_id TEXT NOT NULL,
    file_path TEXT NOT NULL UNIQUE,
    created_at TEXT NOT NULL,
    created_by INTEGER REFERENCES users(id)
);

CREATE TABLE IF NOT EXISTS sales (
    id INTEGER PRIMARY KEY,
    -- One receipt can contain several line items.  ``receipt_id`` therefore
    -- identifies the shopping basket and must not be unique per ledger row.
    receipt_id TEXT NOT NULL,
    variant_id INTEGER NOT NULL REFERENCES variants(id),
    quantity INTEGER NOT NULL CHECK(quantity > 0),
    unit_price_cents INTEGER NOT NULL CHECK(unit_price_cents >= 0),
    amount_due_cents INTEGER NOT NULL CHECK(amount_due_cents >= 0),
    amount_given_cents INTEGER,
    donation_cents INTEGER NOT NULL DEFAULT 0 CHECK(donation_cents >= 0),
    payment_method TEXT NOT NULL,
    is_paid INTEGER NOT NULL DEFAULT 1,
    -- Keeps the payment workflow distinct from ordinary counter sales that
    -- were already paid when first entered.
    payment_follow_up INTEGER NOT NULL DEFAULT 0,
    is_received INTEGER NOT NULL DEFAULT 1,
    -- ``not_applicable`` identifies an ordinary counter sale.  The other
    -- states form the delivery workflow for a sale that was not handed over
    -- immediately.
    delivery_status TEXT NOT NULL DEFAULT 'not_applicable',
    -- A cancellation preserves the original booking for audit/history while
    -- removing its effect from stock, balances and active work queues.
    is_cancelled INTEGER NOT NULL DEFAULT 0,
    customer_name TEXT,
    customer_address TEXT,
    event_name TEXT,
    sold_by TEXT,
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
CREATE INDEX IF NOT EXISTS idx_purchases_receipt_id ON purchases(receipt_id);
CREATE INDEX IF NOT EXISTS idx_purchase_receipt_attachments_receipt ON purchase_receipt_attachments(receipt_id);
CREATE INDEX IF NOT EXISTS idx_sales_variant ON sales(variant_id, sold_on);
CREATE INDEX IF NOT EXISTS idx_sales_sold_on ON sales(sold_on);
"""

PAYMENT_METHODS = ["Bar", "PayPal", "Überweisung", "Karte", "Sonstiges"]

# Roles are cumulative: every authenticated user is a seller, managers also
# manage stock/purchases/articles, and exactly one configured account holds the
# administrator role.  Keeping the hierarchy as a tiny mapping makes every
# server-side authorization decision explicit and easy to audit.
ROLE_LEVELS = {"seller": 1, "manager": 2, "admin": 3}
ROLE_LABELS = {"seller": "Seller", "manager": "Manager", "admin": "Admin"}
MANAGED_USER_ROLES = ("seller", "manager")
SETUP_CODE_ALPHABET = string.ascii_uppercase + string.digits

# ``is_received`` remains a useful, compact accounting flag.  The additional
# state is only needed when a sale must be sent later: a shipment can be open,
# sent, and eventually received.  Keeping ``not_applicable`` separate prevents
# every ordinary sale at the merch stand from appearing in the shipment archive.
DELIVERY_STATUS_LABELS = {
    "pending": "Noch nicht versendet",
    "shipped": "Versendet",
    "received": "Erhalten",
    "not_applicable": "Nicht relevant",
}
DELIVERY_WORKFLOW_STATUSES = ("pending", "shipped", "received")

# Invoice uploads deliberately stay small enough for the NAS and for regular
# automatic backups.  The browser validates the same extensions for a nicer
# experience, but the server is the authoritative check.
ALLOWED_INVOICE_FILE_EXTENSIONS = {".pdf", ".png", ".jpg", ".jpeg"}
MAX_INVOICE_FILE_BYTES = 10 * 1024 * 1024

# These values are intentionally only used when a *new* article is created.
# Existing articles can have completely different option groups (for example a
# patch without sizes), so a migration must never add them retroactively.
DEFAULT_NEW_ARTICLE_OPTIONS = (
    ("Farbe", ("Schwarz", "Weiß")),
    ("Größe", ("S", "M", "L", "XL", "XXL")),
)

# A release tag is deliberately kept independent from the database schema.  It
# identifies the exact code running in a container and is therefore useful for
# both support requests and the GitHub update check.  Release images receive it
# once at build time through the APP_VERSION environment variable.
GITHUB_REPOSITORY_PATTERN = re.compile(r"^[A-Za-z0-9_.-]+/[A-Za-z0-9_.-]+$")
RELEASE_VERSION_PATTERN = re.compile(r"^v?(\d+)\.(\d+)\.(\d+)$")


def version_tuple(version: str) -> tuple[int, int, int] | None:
    """Return a comparable semantic version tuple or ``None`` for invalid tags.

    Published releases use tags such as ``v0.3.0``.  The same tag is embedded
    in the release image as ``APP_VERSION``.  Accepting a leading ``v`` keeps
    the release convention visible without a fragile string comparison.
    """

    match = RELEASE_VERSION_PATTERN.fullmatch(str(version).strip())
    if match is None:
        return None
    return tuple(int(part) for part in match.groups())


def display_version(version: str) -> str:
    """Return a consistently labelled version for the German user interface."""

    cleaned = str(version).strip()
    return cleaned if cleaned.startswith("v") else f"v{cleaned}"


def fetch_latest_github_release(repository: str, token: str | None, timeout_seconds: float) -> dict[str, Any]:
    """Fetch the newest published GitHub release without exposing credentials.

    The optional token is only useful for a private repository and is kept
    exclusively in the server-side environment.  No token is ever rendered in
    a page or included in a JSON response.
    """

    if not GITHUB_REPOSITORY_PATTERN.fullmatch(repository):
        raise ValueError("UPDATE_CHECK_REPOSITORY muss als owner/repository angegeben werden.")
    headers = {
        "Accept": "application/vnd.github+json",
        "User-Agent": "Protovibe-Merch-Manager",
    }
    if token:
        headers["Authorization"] = f"Bearer {token}"
    request_object = Request(
        f"https://api.github.com/repos/{repository}/releases/latest",
        headers=headers,
    )
    with urlopen(request_object, timeout=timeout_seconds) as response:
        payload = json.load(response)
    if not isinstance(payload, dict):
        raise ValueError("GitHub hat keine gültigen Release-Daten geliefert.")
    return payload


def update_status(app: Flask, *, force: bool = False) -> dict[str, Any]:
    """Return a cached, non-blocking-in-practice status of the newest release.

    The browser asks this endpoint after a successful login.  The actual
    GitHub request is cached for several hours, so normal navigation does not
    repeatedly contact an external service.  A deliberate "Jetzt prüfen"
    action passes ``force=True`` and bypasses that cache.
    """

    current_version = str(app.config["APP_VERSION"])
    repository = str(app.config.get("UPDATE_CHECK_REPOSITORY", "")).strip()
    current_label = display_version(current_version)
    if not repository:
        return {
            "ok": False,
            "state": "not_configured",
            "current_version": current_label,
            "latest_version": None,
            "update_available": False,
            "release_url": None,
            "release_name": None,
            "published_at": None,
            "checked_at": utc_now(),
            "message": "Die GitHub-Update-Prüfung ist nicht eingerichtet.",
        }

    cache = app.extensions.setdefault(
        "update_status_cache",
        {"lock": Lock(), "key": None, "status": None, "checked_at_monotonic": 0.0},
    )
    cache_key = (current_version, repository, str(app.config.get("UPDATE_CHECK_TOKEN") or ""))
    ttl_seconds = max(0, int(app.config.get("UPDATE_CHECK_CACHE_SECONDS", 21600)))
    now = time.monotonic()

    with cache["lock"]:
        cached_status = cache["status"]
        cache_is_fresh = (
            cache["key"] == cache_key
            and cached_status is not None
            and now - float(cache["checked_at_monotonic"]) < ttl_seconds
        )
        if cache_is_fresh and not force:
            return {**cached_status, "cached": True}

        base_status: dict[str, Any] = {
            "current_version": current_label,
            "latest_version": None,
            "update_available": False,
            "release_url": None,
            "release_name": None,
            "published_at": None,
            "checked_at": utc_now(),
        }
        try:
            release = fetch_latest_github_release(
                repository,
                app.config.get("UPDATE_CHECK_TOKEN") or None,
                float(app.config.get("UPDATE_CHECK_TIMEOUT_SECONDS", 3.0)),
            )
            release_tag = str(release.get("tag_name", "")).strip()
            release_version = version_tuple(release_tag)
            if release_version is None:
                raise ValueError("Die neueste GitHub-Version hat kein gültiges Versions-Tag.")
            current_version_tuple = version_tuple(current_version)
            if current_version_tuple is None:  # protected during app creation
                raise ValueError("Die installierte App-Version ist ungültig.")
            release_url = str(release.get("html_url", "")).strip()
            safe_release_url = release_url if release_url.startswith("https://github.com/") else None
            base_status.update(
                {
                    "ok": True,
                    "latest_version": display_version(release_tag),
                    "release_url": safe_release_url,
                    "release_name": str(release.get("name") or release_tag).strip(),
                    "published_at": str(release.get("published_at") or "").strip() or None,
                }
            )
            if release_version > current_version_tuple:
                base_status.update(
                    {
                        "state": "update_available",
                        "update_available": True,
                        "message": f"{display_version(release_tag)} ist als neue stabile Version verfügbar.",
                    }
                )
            elif release_version == current_version_tuple:
                base_status.update(
                    {
                        "state": "current",
                        "message": "Diese Installation ist auf dem aktuellen veröffentlichten Stand.",
                    }
                )
            else:
                base_status.update(
                    {
                        "state": "ahead",
                        "message": "Diese Installation ist neuer als die zuletzt veröffentlichte GitHub-Version.",
                    }
                )
        except HTTPError as exc:
            if exc.code == 404:
                message = "Es ist noch keine veröffentlichte Version erreichbar. Prüfe Release und Zugriffsrechte."
            else:
                message = "GitHub konnte die neueste Version gerade nicht bereitstellen."
            base_status.update({"ok": False, "state": "unavailable", "message": message})
        except (URLError, TimeoutError, OSError, ValueError, json.JSONDecodeError):
            base_status.update(
                {
                    "ok": False,
                    "state": "unavailable",
                    "message": "Die Update-Prüfung ist derzeit nicht erreichbar. Die App läuft unverändert weiter.",
                }
            )

        cache.update(
            {
                "key": cache_key,
                "status": base_status,
                "checked_at_monotonic": time.monotonic(),
            }
        )
        return {**base_status, "cached": False}


def utc_now() -> str:
    """Return an ISO timestamp without pretending that it is local time."""

    return datetime.now(timezone.utc).replace(microsecond=0).isoformat()


def today_iso() -> str:
    return date.today().isoformat()


def normalized_role(user: dict[str, Any] | sqlite3.Row | None) -> str:
    """Return a safe role for current and pre-role database rows."""

    if user is None:
        return "seller"
    role = str(user["role"] or "").strip().lower() if "role" in user.keys() else ""
    if role in ROLE_LEVELS:
        return role
    return "admin" if bool(user["is_admin"]) else "seller"


def has_role(user: dict[str, Any] | sqlite3.Row | None, required_role: str) -> bool:
    """Check a cumulative role without trusting a client-side navigation hint."""

    return ROLE_LEVELS.get(normalized_role(user), 0) >= ROLE_LEVELS[required_role]


def user_capabilities(user: dict[str, Any] | None) -> dict[str, Any] | None:
    """Add only display conveniences; routes still enforce the same rights."""

    if user is None:
        return None
    role = normalized_role(user)
    user["role"] = role
    user["role_label"] = ROLE_LABELS[role]
    user["is_admin"] = role == "admin"
    user["can_manage_purchases"] = has_role(user, "manager")
    user["can_manage_articles"] = has_role(user, "manager")
    return user


def valid_username(value: Any) -> str:
    """Accept readable local account names while avoiding whitespace ambiguity."""

    username = str(value or "").strip()
    if not re.fullmatch(r"[^\s]{3,48}", username):
        raise ValueError("Der Benutzername muss 3 bis 48 Zeichen lang sein und darf keine Leerzeichen enthalten.")
    return username


def validate_new_password(value: Any, confirmation: Any) -> str:
    """Require a memorable, reasonably long password without artificial rules."""

    password = str(value or "")
    if password != str(confirmation or ""):
        raise ValueError("Die beiden Passwörter stimmen nicht überein.")
    if len(password) < 12:
        raise ValueError("Das Passwort muss mindestens 12 Zeichen lang sein.")
    if len(password) > 256:
        raise ValueError("Das Passwort ist zu lang.")
    return password


def generate_setup_code() -> str:
    """Create an easily communicable, one-time account setup code."""

    raw = "".join(secrets.choice(SETUP_CODE_ALPHABET) for _ in range(16))
    return "-".join(raw[index : index + 4] for index in range(0, len(raw), 4))


def setup_code_expiry(days: int) -> str:
    return (datetime.now(timezone.utc) + timedelta(days=days)).replace(microsecond=0).isoformat()


def is_setup_code_current(user: sqlite3.Row | dict[str, Any]) -> bool:
    expires_at = user["setup_code_expires_at"]
    if not expires_at:
        return False
    try:
        expiry = datetime.fromisoformat(str(expires_at))
    except ValueError:
        return False
    if expiry.tzinfo is None:
        expiry = expiry.replace(tzinfo=timezone.utc)
    return expiry > datetime.now(timezone.utc)


def mfa_fernet(app: Flask | None = None) -> Fernet:
    """Derive a stable encryption key from the already mandatory SECRET_KEY.

    This avoids a second secret that people may forget to back up.  The tradeoff
    is intentional and documented: SECRET_KEY must stay stable after 2FA was
    enabled, which is already necessary for stable Flask sessions.
    """

    configured_app = app or current_app._get_current_object()
    material = str(configured_app.config["SECRET_KEY"]).encode("utf-8")
    key = base64.urlsafe_b64encode(hashlib.sha256(b"protovibe-merch:mfa:" + material).digest())
    return Fernet(key)


def encrypt_mfa_secret(secret: str, app: Flask | None = None) -> str:
    return mfa_fernet(app).encrypt(secret.encode("ascii")).decode("ascii")


def decrypt_mfa_secret(value: str | None, app: Flask | None = None) -> str | None:
    if not value:
        return None
    try:
        return mfa_fernet(app).decrypt(str(value).encode("ascii")).decode("ascii")
    except (InvalidToken, UnicodeError, ValueError):
        return None


def recovery_code_hashes(user: sqlite3.Row | dict[str, Any]) -> list[str]:
    try:
        parsed = json.loads(user["mfa_recovery_code_hashes_json"] or "[]")
    except (TypeError, ValueError, json.JSONDecodeError):
        return []
    return [str(item) for item in parsed if isinstance(item, str)]


def generate_recovery_codes() -> list[str]:
    """Create one-use emergency codes; only their hashes are persisted."""

    return [f"{secrets.token_hex(4).upper()}-{secrets.token_hex(4).upper()}" for _ in range(10)]


def verify_mfa_code(
    connection: sqlite3.Connection, user: sqlite3.Row | dict[str, Any], submitted_code: Any
) -> str | None:
    """Verify a TOTP code or consume one recovery code.

    The return value tells the caller whether an emergency code was consumed so
    it can make that unusually important event visible in the audit log.
    """

    code = str(submitted_code or "").strip().upper().replace(" ", "")
    if not code or not bool(user["mfa_enabled"]):
        return None
    secret = decrypt_mfa_secret(user["mfa_secret_encrypted"])
    if secret and pyotp.TOTP(secret).verify(code, valid_window=1):
        return "totp"
    hashes = recovery_code_hashes(user)
    for index, stored_hash in enumerate(hashes):
        if check_password_hash(stored_hash, code):
            del hashes[index]
            connection.execute(
                "UPDATE users SET mfa_recovery_code_hashes_json = ? WHERE id = ?",
                (json.dumps(hashes), user["id"]),
            )
            return "recovery"
    return None


def safe_next_url(value: Any, *, fallback: str = "/verkauf") -> str:
    candidate = str(value or "")
    return candidate if candidate.startswith("/") and not candidate.startswith("//") else fallback


def establish_authenticated_session(user: sqlite3.Row | dict[str, Any]) -> None:
    """Start a fresh session after password and, if configured, MFA checks."""

    session.clear()
    session["user_id"] = int(user["id"])
    session["user_session_version"] = int(user["session_version"] or 0)
    csrf_token()


def begin_auth_challenge(kind: str, user: sqlite3.Row | dict[str, Any], next_url: Any) -> None:
    """Keep pre-authentication state separate from a signed-in user session."""

    session.clear()
    session[f"{kind}_user_id"] = int(user["id"])
    session["post_auth_next"] = safe_next_url(next_url)
    csrf_token()


def take_post_auth_next() -> str:
    return safe_next_url(session.get("post_auth_next"))


def has_profile_reauth(user: dict[str, Any] | None) -> bool:
    return bool(
        user
        and session.get("profile_reauth_user_id") == user["id"]
        and float(session.get("profile_reauth_until", 0)) > time.time()
    )


def default_new_article_option_configuration() -> list[dict[str, Any]]:
    """Return fresh, editable default option columns for a new article.

    IDs deliberately stay ``None`` here.  ``apply_option_configuration`` turns
    them into persistent database rows and can therefore be reused for both
    the normal and the duplicate-name creation paths.
    """

    return [
        {
            "id": None,
            "name": group_name,
            "position": position,
            "values": [
                {"id": None, "value": value, "position": value_position}
                for value_position, value in enumerate(values)
            ],
        }
        for position, (group_name, values) in enumerate(DEFAULT_NEW_ARTICLE_OPTIONS)
    ]


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


def parse_optional_non_negative_int(value: Any, *, field_name: str = "Mindestbestand") -> int | None:
    """Parse an optional whole-number stock threshold.

    A blank field intentionally disables the warning for that variant.  Zero
    is valid and means "warn when sold out", so it must not be treated like an
    empty value.
    """

    raw = "" if value is None else str(value).strip()
    if not raw:
        return None
    if not re.fullmatch(r"\d+", raw):
        raise ValueError(f"{field_name} muss eine ganze Zahl ab 0 sein.")
    return int(raw)


def purchase_request_payload() -> tuple[Any, Any]:
    """Return either JSON or multipart/form purchase data plus uploaded files.

    Existing browser tabs can keep posting the original JSON purchase API.
    The cart UI uses ``FormData`` so item and receipt attachments can be sent
    atomically with their booking.  Supporting both shapes makes upgrades
    harmless instead of losing a just-entered purchase in an older tab.
    """

    if request.is_json:
        return request.get_json(silent=True) or {}, request.files
    return request.form, request.files


def invoice_file_extension(uploaded_file: Any | None) -> str | None:
    """Validate an optional invoice upload and return its lowercase suffix.

    Only PDFs, PNGs and JPEGs are accepted.  In addition to the filename the
    small signature checks reject a renamed HTML/text file.  The user-provided
    filename is never used as a filesystem path.
    """

    if uploaded_file is None or not getattr(uploaded_file, "filename", ""):
        return None

    extension = Path(str(uploaded_file.filename)).suffix.lower()
    if extension not in ALLOWED_INVOICE_FILE_EXTENSIONS:
        raise ValueError("Bitte nur eine Rechnung als PDF, PNG oder JPG hochladen.")

    stream = uploaded_file.stream
    stream.seek(0, os.SEEK_END)
    size = stream.tell()
    stream.seek(0)
    if size <= 0:
        raise ValueError("Die Rechnungsdatei ist leer.")
    if size > int(current_app.config["MAX_INVOICE_FILE_BYTES"]):
        raise ValueError("Die Rechnungsdatei darf höchstens 10 MB groß sein.")

    signature = stream.read(16)
    stream.seek(0)
    is_valid = (
        (extension == ".pdf" and signature.startswith(b"%PDF-"))
        or (extension == ".png" and signature.startswith(b"\x89PNG\r\n\x1a\n"))
        or (extension in {".jpg", ".jpeg"} and signature.startswith(b"\xff\xd8\xff"))
    )
    if not is_valid:
        raise ValueError("Die Datei passt nicht zum gewählten Rechnungsformat.")
    return extension


def invoice_storage_path(filename: str | None) -> Path | None:
    """Resolve a stored invoice name without allowing path traversal."""

    if not filename:
        return None
    safe_name = Path(str(filename)).name
    if safe_name != str(filename) or Path(safe_name).suffix.lower() not in ALLOWED_INVOICE_FILE_EXTENSIONS:
        return None
    return Path(current_app.config["INVOICE_UPLOAD_DIR"]) / safe_name


def save_invoice_file(uploaded_file: Any | None, receipt_id: str) -> str | None:
    """Store a validated invoice under an opaque, receipt-associated name."""

    extension = invoice_file_extension(uploaded_file)
    if extension is None:
        return None
    filename = f"{receipt_id}-{secrets.token_hex(12)}{extension}"
    target = invoice_storage_path(filename)
    if target is None:  # Defensive guard; generated names always pass above.
        raise ValueError("Rechnungsdatei konnte nicht gespeichert werden.")
    target.parent.mkdir(parents=True, exist_ok=True)
    try:
        uploaded_file.save(target)
    except Exception:
        target.unlink(missing_ok=True)
        raise
    return filename


def delete_invoice_file(filename: str | None) -> None:
    """Remove a managed invoice attachment if it still exists."""

    target = invoice_storage_path(filename)
    if target is not None:
        target.unlink(missing_ok=True)


def purchase_values_from_payload(
    connection: sqlite3.Connection,
    payload: Any,
    *,
    current_variant_id: int | None = None,
    current_purchased_on: str | None = None,
) -> dict[str, Any]:
    """Validate the editable fields of a purchase booking.

    A historic, now inactive variant may remain selected when a purchase is
    merely corrected in another field.  Switching to a different variant still
    requires an active target, so no new booking can be attached to retired
    catalogue data.
    """

    variant_id = int(payload.get("variant_id"))
    quantity = parse_positive_int(payload.get("quantity"))
    unit_cost = money_to_cents(payload.get("unit_cost"), field_name="Preis pro Stück")
    purchased_on = str(payload.get("purchased_on") or current_purchased_on or today_iso())
    date.fromisoformat(purchased_on)
    variant = connection.execute("SELECT id, is_active FROM variants WHERE id = ?", (variant_id,)).fetchone()
    if variant is None or (not variant["is_active"] and variant_id != current_variant_id):
        raise ValueError("Diese Artikelvariante ist nicht mehr verfügbar.")
    return {
        "variant_id": variant_id,
        "quantity": quantity,
        "unit_cost_cents": unit_cost,
        "purchased_on": purchased_on,
        "supplier": str(payload.get("supplier", "")).strip() or None,
        "invoice_reference": str(payload.get("invoice_reference", "")).strip() or None,
        "comment": str(payload.get("comment", "")).strip() or None,
    }


def purchase_items_from_payload(
    connection: sqlite3.Connection,
    payload: Any,
    uploaded_files: Any,
) -> tuple[str, list[dict[str, Any]], list[Any]]:
    """Validate a single legacy purchase or a new multi-item purchase cart.

    The cart's date belongs to the receipt and is copied to every ledger row.
    Item-specific prices, suppliers, references, comments and optional invoice
    files remain independent so mixed supplier invoices can still be recorded
    accurately in one day/cart.
    """

    raw_items = payload.get("items")
    if raw_items is None:
        raw_items = [payload]
    elif isinstance(raw_items, str):
        try:
            raw_items = json.loads(raw_items)
        except json.JSONDecodeError as exc:
            raise ValueError("Der Einkaufswarenkorb ist ungültig.") from exc
    if not isinstance(raw_items, list) or not raw_items:
        raise ValueError("Der Einkaufswarenkorb enthält noch keine Artikel.")

    purchased_on = str(payload.get("purchased_on") or today_iso())
    date.fromisoformat(purchased_on)
    items: list[dict[str, Any]] = []
    for index, raw_item in enumerate(raw_items):
        if not isinstance(raw_item, dict):
            raise ValueError("Ungültiger Artikel im Einkaufswarenkorb.")
        item_payload = {**raw_item, "purchased_on": purchased_on}
        values = purchase_values_from_payload(connection, item_payload)
        # ``invoice_file`` keeps the just-released single-item UI compatible.
        uploaded_invoice = uploaded_files.get(f"item_invoice_{index}")
        if uploaded_invoice is None and len(raw_items) == 1:
            uploaded_invoice = uploaded_files.get("invoice_file")
        items.append({**values, "uploaded_invoice": uploaded_invoice})

    cart_invoice_files = [
        uploaded_file
        for uploaded_file in uploaded_files.getlist("cart_invoice_files")
        if getattr(uploaded_file, "filename", "")
    ]
    return purchased_on, items, cart_invoice_files


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


def sales_receipt_id_is_unique(connection: sqlite3.Connection) -> bool:
    """Return whether an older database still restricts receipts to one row.

    SQLite cannot drop the implicit index created by a ``UNIQUE`` column
    constraint.  The small migration below consequently rebuilds only the
    ``sales`` table when upgrading from versions before the shopping basket.
    """

    for index in connection.execute("PRAGMA index_list(sales)").fetchall():
        if not index["unique"]:
            continue
        index_name = str(index["name"]).replace('"', '""')
        columns = connection.execute(f'PRAGMA index_info("{index_name}")').fetchall()
        if [column["name"] for column in columns] == ["receipt_id"]:
            return True
    return False


def rebuild_sales_for_multi_item_receipts(connection: sqlite3.Connection) -> None:
    """Remove the legacy per-row receipt uniqueness without losing history.

    Every existing sale remains byte-for-byte equivalent as a one-item receipt.
    New sales can then insert several rows with the same receipt ID, which
    keeps stock, balances and delivery workflows item-based as before.
    """

    connection.executescript(
        """
        CREATE TABLE sales_multi_item_receipts (
            id INTEGER PRIMARY KEY,
            receipt_id TEXT NOT NULL,
            variant_id INTEGER NOT NULL REFERENCES variants(id),
            quantity INTEGER NOT NULL CHECK(quantity > 0),
            unit_price_cents INTEGER NOT NULL CHECK(unit_price_cents >= 0),
            amount_due_cents INTEGER NOT NULL CHECK(amount_due_cents >= 0),
            amount_given_cents INTEGER,
            donation_cents INTEGER NOT NULL DEFAULT 0 CHECK(donation_cents >= 0),
            payment_method TEXT NOT NULL,
            is_paid INTEGER NOT NULL DEFAULT 1,
            payment_follow_up INTEGER NOT NULL DEFAULT 0,
            is_received INTEGER NOT NULL DEFAULT 1,
            delivery_status TEXT NOT NULL DEFAULT 'not_applicable',
            is_cancelled INTEGER NOT NULL DEFAULT 0,
            customer_name TEXT,
            customer_address TEXT,
            event_name TEXT,
            sold_by TEXT,
            comment TEXT,
            sold_on TEXT NOT NULL,
            created_at TEXT NOT NULL,
            created_by INTEGER REFERENCES users(id)
        );
        INSERT INTO sales_multi_item_receipts (
            id, receipt_id, variant_id, quantity, unit_price_cents,
            amount_due_cents, amount_given_cents, donation_cents,
            payment_method, is_paid, payment_follow_up, is_received,
            delivery_status, is_cancelled, customer_name, customer_address,
            event_name, sold_by, comment, sold_on, created_at, created_by
        )
        SELECT
            id, receipt_id, variant_id, quantity, unit_price_cents,
            amount_due_cents, amount_given_cents, donation_cents,
            payment_method, is_paid, payment_follow_up, is_received,
            delivery_status, is_cancelled, customer_name, customer_address,
            event_name, sold_by, comment, sold_on, created_at, created_by
        FROM sales;
        DROP TABLE sales;
        ALTER TABLE sales_multi_item_receipts RENAME TO sales;
        """
    )


def purchases_receipt_id_is_unique(connection: sqlite3.Connection) -> bool:
    """Return whether an older purchase table permits only one line per ID."""

    for index in connection.execute("PRAGMA index_list(purchases)").fetchall():
        if not index["unique"]:
            continue
        index_name = str(index["name"]).replace('"', '""')
        columns = connection.execute(f'PRAGMA index_info("{index_name}")').fetchall()
        if [column["name"] for column in columns] == ["receipt_id"]:
            return True
    return False


def rebuild_purchases_for_multi_item_receipts(connection: sqlite3.Connection) -> None:
    """Remove the legacy purchase receipt uniqueness without losing rows.

    The previous purchase screen only ever wrote a single ledger row.  The
    migration preserves each row and its optional invoice attachment exactly;
    the new UI can subsequently add several lines under one shared receipt ID.
    """

    connection.executescript(
        """
        CREATE TABLE purchases_multi_item_receipts (
            id INTEGER PRIMARY KEY,
            receipt_id TEXT NOT NULL,
            variant_id INTEGER NOT NULL REFERENCES variants(id),
            quantity INTEGER NOT NULL CHECK(quantity > 0),
            unit_cost_cents INTEGER NOT NULL CHECK(unit_cost_cents >= 0),
            purchased_on TEXT NOT NULL,
            supplier TEXT,
            invoice_reference TEXT,
            invoice_file_path TEXT,
            comment TEXT,
            created_at TEXT NOT NULL,
            created_by INTEGER REFERENCES users(id)
        );
        INSERT INTO purchases_multi_item_receipts (
            id, receipt_id, variant_id, quantity, unit_cost_cents, purchased_on,
            supplier, invoice_reference, invoice_file_path, comment, created_at, created_by
        )
        SELECT
            id, receipt_id, variant_id, quantity, unit_cost_cents, purchased_on,
            supplier, invoice_reference, invoice_file_path, comment, created_at, created_by
        FROM purchases;
        DROP TABLE purchases;
        ALTER TABLE purchases_multi_item_receipts RENAME TO purchases;
        """
    )


def group_legacy_purchases_by_date(connection: sqlite3.Connection) -> None:
    """Turn the pre-cart purchase history into one cart per booking date.

    Earlier imports only had the date as grouping information and consequently
    assigned one receipt ID to every individual line.  Retain the oldest of
    those familiar IDs for each day and attach all rows from that day to it.
    Per-item supplier, comment and invoice fields stay untouched.
    """

    dates = connection.execute(
        "SELECT DISTINCT purchased_on FROM purchases ORDER BY purchased_on"
    ).fetchall()
    for row in dates:
        purchase_rows = connection.execute(
            "SELECT id, receipt_id FROM purchases WHERE purchased_on = ? ORDER BY id", (row["purchased_on"],)
        ).fetchall()
        if len(purchase_rows) < 2:
            continue
        connection.execute(
            "UPDATE purchases SET receipt_id = ? WHERE purchased_on = ?",
            (purchase_rows[0]["receipt_id"], row["purchased_on"]),
        )


def initialise_database(app: Flask) -> None:
    """Create/update the schema and bootstrap the configured administrator."""

    database_path = Path(app.config["DATABASE"])
    database_path.parent.mkdir(parents=True, exist_ok=True)
    connection = db_connect(database_path)
    try:
        connection.executescript(SCHEMA_SQL)
        article_columns = {row["name"] for row in connection.execute("PRAGMA table_info(articles)").fetchall()}
        if "is_offered" not in article_columns:
            connection.execute("ALTER TABLE articles ADD COLUMN is_offered INTEGER NOT NULL DEFAULT 1")
        # The first released schema did not have this legacy-ODS convenience
        # flag.  Keeping this tiny migration here makes a future update safe.
        variant_columns = {row["name"] for row in connection.execute("PRAGMA table_info(variants)").fetchall()}
        if "no_reorder" not in variant_columns:
            connection.execute("ALTER TABLE variants ADD COLUMN no_reorder INTEGER NOT NULL DEFAULT 0")
        if "minimum_stock" not in variant_columns:
            # Existing variants intentionally start without a configured
            # threshold.  A zero default would immediately flag every already
            # sold-out historic variant, even though nobody opted into a
            # minimum-stock warning for it.
            connection.execute("ALTER TABLE variants ADD COLUMN minimum_stock INTEGER CHECK(minimum_stock >= 0)")
        if "is_offered" not in variant_columns:
            connection.execute("ALTER TABLE variants ADD COLUMN is_offered INTEGER NOT NULL DEFAULT 1")

        # Invoice references used to be a single free-text field.  Preserve
        # those values and add a separate server-managed attachment path for
        # drag-and-drop PDF/image uploads.
        purchase_columns = {row["name"] for row in connection.execute("PRAGMA table_info(purchases)").fetchall()}
        if "invoice_file_path" not in purchase_columns:
            connection.execute("ALTER TABLE purchases ADD COLUMN invoice_file_path TEXT")

        # Purchases now mirror sale carts: several rows can share one receipt.
        # During the one-time upgrade, legacy/imported single rows from one
        # date become one cart while retaining every line-level detail.
        if purchases_receipt_id_is_unique(connection):
            rebuild_purchases_for_multi_item_receipts(connection)
            group_legacy_purchases_by_date(connection)
        connection.executescript(
            """
            CREATE INDEX IF NOT EXISTS idx_purchases_variant ON purchases(variant_id, purchased_on);
            CREATE INDEX IF NOT EXISTS idx_purchases_receipt_id ON purchases(receipt_id);
            CREATE INDEX IF NOT EXISTS idx_purchase_receipt_attachments_receipt
                ON purchase_receipt_attachments(receipt_id);
            """
        )

        # Version 0.2 adds a small delivery state machine.  Existing counter
        # sales remain outside it, while older sales marked "not received" are
        # safely migrated into the first workflow state.
        sales_columns = {row["name"] for row in connection.execute("PRAGMA table_info(sales)").fetchall()}
        if "delivery_status" not in sales_columns:
            connection.execute(
                "ALTER TABLE sales ADD COLUMN delivery_status TEXT NOT NULL DEFAULT 'not_applicable'"
            )
            connection.execute(
                "UPDATE sales SET delivery_status = 'pending' WHERE is_received = 0"
            )
        connection.execute(
            "CREATE INDEX IF NOT EXISTS idx_sales_delivery_status ON sales(delivery_status)"
        )

        # A completed payment history should only contain sales that actually
        # began as open payments.  Existing open sales get that marker during
        # migration; already paid counter sales stay out of the new history.
        if "payment_follow_up" not in sales_columns:
            connection.execute(
                "ALTER TABLE sales ADD COLUMN payment_follow_up INTEGER NOT NULL DEFAULT 0"
            )
            connection.execute(
                "UPDATE sales SET payment_follow_up = 1 WHERE is_paid = 0"
            )
        connection.execute(
            "CREATE INDEX IF NOT EXISTS idx_sales_payment_follow_up ON sales(payment_follow_up, is_paid)"
        )

        # ``created_by`` identifies the logged-in app account.  Keep the
        # optional person actually staffing the merch stand separately, so a
        # shared account does not erase that useful sale context.
        if "sold_by" not in sales_columns:
            connection.execute("ALTER TABLE sales ADD COLUMN sold_by TEXT")

        # A cancellation is intentionally a status change instead of a delete.
        # Existing sales remain valid, active ledger rows after this migration.
        if "is_cancelled" not in sales_columns:
            connection.execute(
                "ALTER TABLE sales ADD COLUMN is_cancelled INTEGER NOT NULL DEFAULT 0"
            )

        # Until v0.2.4, a receipt was forced to contain exactly one sale row.
        # Rebuild that one table for a safe in-place upgrade; all existing rows
        # automatically become one-item receipts.
        if sales_receipt_id_is_unique(connection):
            rebuild_sales_for_multi_item_receipts(connection)

        # Recreate the normal indexes after a possible table rebuild.  The
        # schema script creates them for fresh databases, whereas upgrading
        # drops their old instances together with the legacy ``sales`` table.
        connection.executescript(
            """
            CREATE INDEX IF NOT EXISTS idx_sales_variant ON sales(variant_id, sold_on);
            CREATE INDEX IF NOT EXISTS idx_sales_sold_on ON sales(sold_on);
            CREATE INDEX IF NOT EXISTS idx_sales_receipt_id ON sales(receipt_id);
            CREATE INDEX IF NOT EXISTS idx_sales_delivery_status ON sales(delivery_status);
            CREATE INDEX IF NOT EXISTS idx_sales_payment_follow_up ON sales(payment_follow_up, is_paid);
            CREATE INDEX IF NOT EXISTS idx_sales_cancelled ON sales(is_cancelled);
            """
        )

        # Upgrade the former single-admin account table in place.  Existing
        # administrator rows become the one admin account; any unexpected
        # extra legacy admins are conservatively downgraded to managers rather
        # than silently leaving several all-powerful accounts behind.
        user_columns = {row["name"] for row in connection.execute("PRAGMA table_info(users)").fetchall()}
        user_column_migrations = (
            ("role", "TEXT NOT NULL DEFAULT 'seller'"),
            ("is_active", "INTEGER NOT NULL DEFAULT 1"),
            ("must_set_password", "INTEGER NOT NULL DEFAULT 0"),
            ("setup_code_hash", "TEXT"),
            ("setup_code_expires_at", "TEXT"),
            ("mfa_secret_encrypted", "TEXT"),
            ("mfa_pending_secret_encrypted", "TEXT"),
            ("mfa_recovery_code_hashes_json", "TEXT NOT NULL DEFAULT '[]'"),
            ("mfa_enabled", "INTEGER NOT NULL DEFAULT 0"),
            ("mfa_enrolled_at", "TEXT"),
            ("session_version", "INTEGER NOT NULL DEFAULT 0"),
            ("last_login_at", "TEXT"),
        )
        added_role_column = "role" not in user_columns
        for column_name, column_definition in user_column_migrations:
            if column_name not in user_columns:
                connection.execute(f"ALTER TABLE users ADD COLUMN {column_name} {column_definition}")
        if added_role_column:
            connection.execute(
                "UPDATE users SET role = CASE WHEN is_admin = 1 THEN 'admin' ELSE 'seller' END"
            )
        connection.execute(
            "UPDATE users SET role = 'seller' WHERE role NOT IN ('seller', 'manager', 'admin') OR role IS NULL"
        )
        admin_rows = connection.execute(
            "SELECT id FROM users WHERE role = 'admin' ORDER BY id"
        ).fetchall()
        if len(admin_rows) > 1:
            connection.executemany(
                "UPDATE users SET role = 'manager' WHERE id = ?",
                [(row["id"],) for row in admin_rows[1:]],
            )
        elif not admin_rows:
            legacy_admin = connection.execute(
                "SELECT id FROM users WHERE is_admin = 1 ORDER BY id LIMIT 1"
            ).fetchone()
            if legacy_admin is not None:
                connection.execute("UPDATE users SET role = 'admin' WHERE id = ?", (legacy_admin["id"],))
        connection.execute("UPDATE users SET is_admin = CASE WHEN role = 'admin' THEN 1 ELSE 0 END")
        connection.execute("CREATE INDEX IF NOT EXISTS idx_users_role_active ON users(role, is_active)")

        user_count = connection.execute("SELECT COUNT(*) FROM users").fetchone()[0]
        if user_count == 0:
            username = app.config["ADMIN_USERNAME"].strip()
            password = app.config["ADMIN_PASSWORD"]
            if not username or not password or password.startswith("replace-this"):
                raise RuntimeError(
                    "Set ADMIN_USERNAME and a strong ADMIN_PASSWORD in .env before starting the app."
                )
            connection.execute(
                """
                INSERT INTO users (username, password_hash, is_admin, role, is_active, created_at)
                VALUES (?, ?, 1, 'admin', 1, ?)
                """,
                (username, generate_password_hash(password), utc_now()),
            )
        # Explicitly commit both schema migrations and initial user creation.
        # In particular, an existing database may have users already and still
        # need the delivery-status column added above.
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


def role_required(required_role: str):
    """Return a decorator for a cumulative, server-enforced role check."""

    def decorator(view):
        @wraps(view)
        def wrapped(*args, **kwargs):
            if g.get("user") is None or not has_role(g.user, required_role):
                abort(403)
            return view(*args, **kwargs)

        return wrapped

    return decorator


def manager_required(view):
    """Permit stock and article management only to Manager and Admin."""

    return role_required("manager")(view)


def admin_required(view):
    """Restrict system and account administration to the single Admin role."""

    return role_required("admin")(view)


def profile_reauth_required(view):
    """Require a fresh password confirmation before account-sensitive views."""

    @wraps(view)
    def wrapped(*args, **kwargs):
        if g.get("user") is None:
            return redirect(url_for("login", next=request.path))
        if not has_profile_reauth(g.user):
            return redirect(url_for("profile_reauth", next=request.path))
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
    *,
    user_id: int | None = None,
) -> None:
    """Append a compact, human-inspectable record of an important change."""

    if user_id is None:
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
            SELECT variant_id, -quantity AS stock_delta FROM sales WHERE is_cancelled = 0
        )
        GROUP BY variant_id
        """
    ).fetchall()
    return {row["variant_id"]: int(row["stock"] or 0) for row in rows}


def is_at_or_below_minimum_stock(stock: int, minimum_stock: int | None) -> bool:
    """Return whether an explicitly configured stock threshold was reached."""

    return minimum_stock is not None and int(stock) <= int(minimum_stock)


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
               v.default_purchase_price_cents, v.minimum_stock, v.is_offered, v.is_active,
               a.name AS article_name, a.is_offered AS article_is_offered,
               a.is_active AS article_is_active
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


def sales_with_labels(
    connection: sqlite3.Connection, sale_rows: Iterable[sqlite3.Row]
) -> list[dict[str, Any]]:
    """Add current variant labels to sale rows for history and work queues.

    Labels intentionally resolve from the current article configuration.  This
    keeps the delivery/payment tab consistent with the existing history view
    after a variant or option has been renamed.
    """

    rows = list(sale_rows)
    labels = variant_label_map(connection, [row["variant_id"] for row in rows])
    sales = []
    for row in rows:
        item = dict(row)
        variant = labels[row["variant_id"]]
        # Never merge the complete variant payload here.  Both sales and
        # variants have generic column names such as ``id``.  A wholesale
        # ``dict.update`` can therefore silently replace the sale ID used by
        # the operations controls.  Only copy the label fields a sale view
        # actually needs, keeping the ledger row as the source of all status
        # and identity data.
        item["article_name"] = variant["article_name"]
        item["option_text"] = variant["option_text"]
        item["label"] = variant["label"]
        item["sale_id"] = item["id"]
        sales.append(item)
    return sales


def purchases_with_labels(
    connection: sqlite3.Connection, purchase_rows: Iterable[sqlite3.Row]
) -> list[dict[str, Any]]:
    """Add safe current variant labels to purchase ledger rows.

    Keep the purchase primary key separate from the similarly named variant
    fields.  The edit/delete controls must always address the booking row, not
    accidentally the article variant.
    """

    rows = list(purchase_rows)
    labels = variant_label_map(connection, [row["variant_id"] for row in rows])
    purchases = []
    for row in rows:
        item = dict(row)
        variant = labels[row["variant_id"]]
        item["article_name"] = variant["article_name"]
        item["option_text"] = variant["option_text"]
        item["label"] = variant["label"]
        item["purchase_id"] = item["id"]
        purchases.append(item)
    return purchases


def purchase_receipt_payload(
    connection: sqlite3.Connection, purchase_rows: Iterable[sqlite3.Row]
) -> list[dict[str, Any]]:
    """Group purchase ledger rows into expandable, editable shopping carts."""

    grouped: dict[str, dict[str, Any]] = {}
    for purchase in purchases_with_labels(connection, purchase_rows):
        receipt = grouped.setdefault(purchase["receipt_id"], {"receipt_id": purchase["receipt_id"], "items": []})
        receipt["items"].append(purchase)

    attachments_by_receipt: dict[str, list[dict[str, Any]]] = defaultdict(list)
    if grouped:
        receipt_ids = list(grouped)
        placeholders = ",".join("?" for _ in receipt_ids)
        attachment_rows = connection.execute(
            f"""
            SELECT id, receipt_id, file_path
            FROM purchase_receipt_attachments
            WHERE receipt_id IN ({placeholders})
            ORDER BY id
            """,
            receipt_ids,
        ).fetchall()
        for attachment in attachment_rows:
            attachments_by_receipt[attachment["receipt_id"]].append(dict(attachment))

    receipts: list[dict[str, Any]] = []
    for receipt in grouped.values():
        items = receipt["items"]
        items.sort(key=lambda item: item["purchase_id"])
        first_item = items[0]
        receipt.update(
            {
                "primary_purchase_id": first_item["purchase_id"],
                "purchased_on": first_item["purchased_on"],
                "item_count": len(items),
                "total_quantity": sum(int(item["quantity"]) for item in items),
                "total_cost_cents": sum(int(item["quantity"]) * int(item["unit_cost_cents"]) for item in items),
                "attachments": attachments_by_receipt.get(receipt["receipt_id"], []),
            }
        )
        if len(items) == 1:
            receipt["summary_label"] = first_item["article_name"]
            receipt["summary_options"] = first_item["option_text"]
        else:
            receipt["summary_label"] = f"Warenkorb ({len(items)} Artikel)"
            receipt["summary_options"] = f"{receipt['total_quantity']} Stück insgesamt"
        # The history filter works at shopping-cart level. Include position
        # details too, so an individual variant, supplier or invoice number
        # finds the complete cart instead of only a flattened summary.
        receipt["search_text"] = " ".join(
            str(value or "")
            for item in items
            for value in (
                receipt["purchased_on"],
                receipt["receipt_id"],
                item["article_name"],
                item["option_text"],
                item["supplier"],
                item["invoice_reference"],
                item["comment"],
            )
        )
        receipts.append(receipt)
    receipts.sort(key=lambda receipt: (receipt["purchased_on"], receipt["primary_purchase_id"]), reverse=True)
    return receipts


def distribute_cents(total_cents: int, weights: list[int]) -> list[int]:
    """Split a positive amount exactly across receipt items.

    A basket has one optional ``Gegeben`` field but several ledger rows.  The
    invoice amount itself belongs unambiguously to each row; only the donation
    needs distributing.  Integer arithmetic keeps the allocation exact, even
    for odd cent values.  A fully free basket gives its donation to the first
    item, because proportional allocation has no meaningful denominator then.
    """

    if not weights:
        return []
    if total_cents <= 0:
        return [0 for _ in weights]
    weight_sum = sum(max(0, weight) for weight in weights)
    if weight_sum <= 0:
        return [total_cents, *([0] * (len(weights) - 1))]

    shares = [(total_cents * max(0, weight)) // weight_sum for weight in weights]
    remainder = total_cents - sum(shares)
    # Assign the unavoidable rounding cents deterministically from left to
    # right.  The individual items always sum back to the basket total.
    positive_indexes = [index for index, weight in enumerate(weights) if weight > 0]
    for index in range(remainder):
        shares[positive_indexes[index % len(positive_indexes)]] += 1
    return shares


def receipt_history_payload(
    connection: sqlite3.Connection, sale_rows: Iterable[sqlite3.Row]
) -> list[dict[str, Any]]:
    """Group sale ledger rows into expandable receipts for the history page.

    The ledger deliberately stays one row per article variant, as stock and
    delivery status can later differ for individual basket items.  This helper
    gives the history UI a receipt-level view without flattening that detail.
    """

    grouped: dict[str, dict[str, Any]] = {}
    for sale in sales_with_labels(connection, sale_rows):
        receipt = grouped.setdefault(sale["receipt_id"], {"receipt_id": sale["receipt_id"], "items": []})
        receipt["items"].append(sale)

    receipts: list[dict[str, Any]] = []
    for receipt in grouped.values():
        items = receipt["items"]
        items.sort(key=lambda item: item["sale_id"])
        first_item = items[0]
        active_items = [item for item in items if not item["is_cancelled"]]
        cancelled_items = [item for item in items if item["is_cancelled"]]
        receipt.update(
            {
                "primary_sale_id": first_item["sale_id"],
                "sold_on": first_item["sold_on"],
                "customer_name": first_item["customer_name"],
                "customer_address": first_item["customer_address"],
                "event_name": first_item["event_name"],
                "sold_by": first_item["sold_by"],
                "comment": first_item["comment"],
                "payment_method": first_item["payment_method"],
                "item_count": len(items),
                "total_quantity": sum(int(item["quantity"]) for item in items),
                # History keeps the original document values visible.  Active
                # totals are included as an explicit subline for a partial
                # cancellation, while accounting derives from active rows.
                "amount_due_cents": sum(int(item["amount_due_cents"]) for item in items),
                "active_amount_due_cents": sum(int(item["amount_due_cents"]) for item in active_items),
                "amount_given_cents": (
                    None
                    if all(item["amount_given_cents"] is None for item in items)
                    else sum(int(item["amount_given_cents"] or 0) for item in items)
                ),
                "active_amount_given_cents": (
                    None
                    if all(item["amount_given_cents"] is None for item in active_items)
                    else sum(int(item["amount_given_cents"] or 0) for item in active_items)
                ),
                "donation_cents": sum(int(item["donation_cents"]) for item in items),
                "active_donation_cents": sum(int(item["donation_cents"]) for item in active_items),
                "active_item_count": len(active_items),
                "is_cancelled": not active_items,
                "is_partially_cancelled": bool(active_items and cancelled_items),
                "all_paid": bool(active_items) and all(item["is_paid"] for item in active_items),
                "has_unpaid_items": any(not item["is_paid"] for item in active_items),
                "delivery_pending": any(item["delivery_status"] == "pending" for item in active_items),
                "delivery_shipped": any(item["delivery_status"] == "shipped" for item in active_items),
                "delivery_received": any(item["delivery_status"] == "received" for item in active_items),
            }
        )
        if len(items) == 1:
            receipt["summary_label"] = first_item["article_name"]
            receipt["summary_options"] = first_item["option_text"]
        else:
            receipt["summary_label"] = f"Warenkorb ({len(items)} Artikel)"
            receipt["summary_options"] = f"{receipt['total_quantity']} Stück insgesamt"
        receipt["search_text"] = " ".join(
            str(value or "")
            for item in items
            for value in (
                receipt["sold_on"],
                receipt["receipt_id"],
                receipt["customer_name"],
                receipt["customer_address"],
                receipt["event_name"],
                receipt["sold_by"],
                receipt["comment"],
                receipt["payment_method"],
                item["article_name"],
                item["option_text"],
            )
        )
        receipts.append(receipt)
    return receipts


def article_payload(
    connection: sqlite3.Connection, *, offered_only: bool = False
) -> list[dict[str, Any]]:
    """Return active article data, optionally limited to the sales assortment.

    Purchases intentionally use the complete active catalogue: stock can still
    be received for an article or variant that is no longer offered for sale.
    """

    stock = variant_stock_map(connection)
    article_sql = """
        SELECT * FROM articles
        WHERE is_active = 1
    """
    if offered_only:
        article_sql += " AND is_offered = 1"
    article_rows = connection.execute(f"{article_sql} ORDER BY name COLLATE NOCASE").fetchall()
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

        variant_sql = """
            SELECT id, option_value_ids_json, sale_price_cents, default_purchase_price_cents
            FROM variants
            WHERE article_id = ? AND is_active = 1
        """
        if offered_only:
            variant_sql += " AND is_offered = 1"
        variant_rows = connection.execute(f"{variant_sql} ORDER BY id", (article["id"],)).fetchall()
        labels = variant_label_map(connection, [row["id"] for row in variant_rows])
        variants = []
        for raw_variant in variant_rows:
            variant = labels[raw_variant["id"]]
            variant["stock"] = stock.get(raw_variant["id"], 0)
            variant["minimum_stock_warning"] = is_at_or_below_minimum_stock(
                variant["stock"], variant["minimum_stock"]
            )
            variants.append(variant)

        # An article with no option groups is valid (patch, cap, ...).  An
        # article where an option group has no values is intentionally disabled
        # until it is fully configured, so no ambiguous sale can be entered.
        is_config_complete = not group_payload or all(group["values"] for group in group_payload)
        # Hide an article from the sale screen entirely when all of its
        # variants have been individually withdrawn from the assortment.
        if offered_only and not variants:
            continue
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
        variant["minimum_stock_warning"] = is_at_or_below_minimum_stock(
            variant["stock"], variant["minimum_stock"]
        )
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
                        "" if variant["minimum_stock"] is None else variant["minimum_stock"],
                        "ja" if variant["minimum_stock_warning"] else "nein",
                        variant["sale_price_cents"] / 100,
                        variant["default_purchase_price_cents"] / 100,
                        "ja" if article["is_offered"] and variant["is_offered"] else "nein",
                        "aktiv" if variant["is_active"] else "inaktiv",
                    ]
                )
        return (
            "artikel",
            [
                "Artikel-ID", "Artikel", "Varianten-ID", "Optionen", "Bestand", "Mindestbestand",
                "Mindestbestandswarnung", "Verkaufspreis", "Standard-Einkaufspreis", "Angeboten", "Status",
            ],
            rows,
        )
    if kind == "sales":
        records = connection.execute("SELECT * FROM sales ORDER BY sold_on, id").fetchall()
        labels = variant_label_map(connection, [row["variant_id"] for row in records])
        return (
            "verkaeufe",
            [
                "Beleg-ID", "Datum", "Artikel", "Optionen", "Stück", "Preis/Stück", "Betrag", "Gegeben", "Spende",
                "Bezahlart", "Bezahlt", "Artikel erhalten", "Versandstatus", "Storniert", "Kundenname", "Adresse", "Veranstaltung", "Verkauft von", "Kommentar",
            ],
            [
                [
                    row["receipt_id"], row["sold_on"], labels[row["variant_id"]]["article_name"], labels[row["variant_id"]]["option_text"],
                    row["quantity"], row["unit_price_cents"] / 100, row["amount_due_cents"] / 100,
                    "" if row["amount_given_cents"] is None else row["amount_given_cents"] / 100,
                    row["donation_cents"] / 100, row["payment_method"], "ja" if row["is_paid"] else "nein",
                    "ja" if row["is_received"] else "nein",
                    DELIVERY_STATUS_LABELS.get(row["delivery_status"], row["delivery_status"]),
                    "ja" if row["is_cancelled"] else "nein",
                    row["customer_name"] or "", row["customer_address"] or "",
                    row["event_name"] or "", row["sold_by"] or "", row["comment"] or "",
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
                "SELECT COALESCE(SUM(quantity), 0) FROM sales WHERE variant_id = ? AND is_cancelled = 0", (variant_id,)
            ).fetchone()[0]
            current_stock = stock.get(variant_id, 0)
            minimum_stock = label["minimum_stock"]
            rows.append(
                [
                    label["article_name"], label["option_text"], purchased, sold, current_stock,
                    "" if minimum_stock is None else minimum_stock,
                    "ja" if is_at_or_below_minimum_stock(current_stock, minimum_stock) else "nein",
                    "ja" if label["article_is_offered"] and label["is_offered"] else "nein",
                ]
            )
        return (
            "bestand",
            [
                "Artikel", "Optionen", "Gekauft", "Verkauft", "Aktueller Bestand", "Mindestbestand",
                "Mindestbestandswarnung", "Angeboten",
            ],
            rows,
        )
    raise ValueError("Unbekannter Export.")


def csv_bytes(headers: list[str], rows: Iterable[Iterable[Any]]) -> bytes:
    """Create an Excel-friendly UTF-8 CSV with a BOM and semicolon separator."""

    buffer = io.StringIO(newline="")
    writer = csv.writer(buffer, delimiter=";")
    writer.writerow(headers)
    writer.writerows(rows)
    return ("\ufeff" + buffer.getvalue()).encode("utf-8")


def create_backup(app: Flask) -> None:
    """Create a restorable SQLite snapshot, CSV exports and invoice files.

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
        # Attachments belong to the same recovery point as their database
        # rows.  Hard links avoid multiplying disk use on normal local
        # filesystems; fall back to copying if a platform does not support it.
        invoice_source = Path(app.config["INVOICE_UPLOAD_DIR"])
        if invoice_source.is_dir():
            invoice_target = target / "invoices"
            invoice_target.mkdir()
            for invoice in invoice_source.iterdir():
                if not invoice.is_file():
                    continue
                try:
                    os.link(invoice, invoice_target / invoice.name)
                except OSError:
                    shutil.copy2(invoice, invoice_target / invoice.name)
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


def create_reset_archive(app: Flask, source_connection: sqlite3.Connection) -> Path:
    """Zip a consistent database snapshot and every current data-directory file.

    The reset archive deliberately lives below ``data/reset-archives`` but is
    excluded from itself.  That makes it persistent with the regular Docker
    data volume without recursively zipping older reset archives forever.
    """

    data_dir = Path(app.config["DATABASE"]).parent
    archive_dir = Path(app.config["RESET_ARCHIVE_DIR"])
    archive_dir.mkdir(parents=True, exist_ok=True)
    timestamp = datetime.now().strftime("%Y-%m-%d_%H-%M-%S")
    archive_path = archive_dir / f"merch-reset-before-{timestamp}.zip"
    suffix = 1
    while archive_path.exists():
        suffix += 1
        archive_path = archive_dir / f"merch-reset-before-{timestamp}-{suffix}.zip"

    snapshot_file = tempfile.NamedTemporaryFile(prefix="merch-reset-", suffix=".sqlite3", delete=False)
    snapshot_path = Path(snapshot_file.name)
    snapshot_file.close()
    try:
        snapshot_connection = sqlite3.connect(snapshot_path)
        try:
            source_connection.backup(snapshot_connection)
        finally:
            snapshot_connection.close()
        database_path = Path(app.config["DATABASE"])
        database_sidecars = {
            database_path,
            Path(f"{database_path}-wal"),
            Path(f"{database_path}-shm"),
        }
        with ZipFile(archive_path, "w", ZIP_DEFLATED) as archive:
            archive.write(snapshot_path, "data/merch.sqlite3")
            for item in data_dir.rglob("*"):
                if not item.is_file() or item in database_sidecars:
                    continue
                # A ZIP below the data directory must never contain itself or
                # a previous reset archive. Normal automatic backups remain
                # useful historic data and are intentionally included.
                try:
                    item.relative_to(archive_dir)
                    continue
                except ValueError:
                    pass
                archive.write(item, Path("data") / item.relative_to(data_dir))
    except Exception:
        archive_path.unlink(missing_ok=True)
        raise
    finally:
        snapshot_path.unlink(missing_ok=True)
    return archive_path


def reset_data_store(app: Flask, preserved_admin: dict[str, Any]) -> None:
    """Replace all operational data while preserving the sole Admin account.

    The calling route has already written a ZIP snapshot and re-authenticated
    the admin.  Retaining that account is deliberate: a true blank user table
    would otherwise make the system depend on an old environment password after
    every reset and could lock the only administrator out.
    """

    database_path = Path(app.config["DATABASE"])
    invoice_dir = Path(app.config["INVOICE_UPLOAD_DIR"])
    backup_dir = Path(app.config["BACKUP_DIR"])
    for path in (database_path, Path(f"{database_path}-wal"), Path(f"{database_path}-shm")):
        path.unlink(missing_ok=True)
    shutil.rmtree(invoice_dir, ignore_errors=True)
    shutil.rmtree(backup_dir, ignore_errors=True)
    database_path.parent.mkdir(parents=True, exist_ok=True)
    invoice_dir.mkdir(parents=True, exist_ok=True)
    backup_dir.mkdir(parents=True, exist_ok=True)

    # Initialise a new complete schema, then replace its bootstrap account by
    # the current verified administrator and the same password/MFA material.
    initialise_database(app)
    connection = db_connect(database_path)
    try:
        connection.execute("DELETE FROM users")
        connection.execute(
            """
            INSERT INTO users (
                id, username, password_hash, is_admin, role, is_active,
                must_set_password, setup_code_hash, setup_code_expires_at,
                mfa_secret_encrypted, mfa_pending_secret_encrypted,
                mfa_recovery_code_hashes_json, mfa_enabled, mfa_enrolled_at,
                session_version, last_login_at, created_at
            ) VALUES (?, ?, ?, 1, 'admin', 1, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
            """,
            (
                preserved_admin["id"],
                preserved_admin["username"],
                preserved_admin["password_hash"],
                preserved_admin["must_set_password"],
                preserved_admin["setup_code_hash"],
                preserved_admin["setup_code_expires_at"],
                preserved_admin["mfa_secret_encrypted"],
                preserved_admin["mfa_pending_secret_encrypted"],
                preserved_admin["mfa_recovery_code_hashes_json"],
                preserved_admin["mfa_enabled"],
                preserved_admin["mfa_enrolled_at"],
                int(preserved_admin["session_version"] or 0) + 1,
                preserved_admin["last_login_at"],
                preserved_admin["created_at"],
            ),
        )
        connection.commit()
    finally:
        connection.close()


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
            FROM sales WHERE variant_id = ? AND is_cancelled = 0
            """,
            (variant_id,),
        ).fetchone()
        # Do not show completely unused, retired variants as balance rows.
        if not purchase_row["quantity"] and not sale_row["quantity"] and not label["is_active"]:
            continue
        current_stock = stock.get(variant_id, 0)
        minimum_stock = label["minimum_stock"]
        rows.append(
            {
                "variant_id": variant_id,
                "article_name": label["article_name"],
                "option_text": label["option_text"],
                "label": label["label"],
                "purchased_quantity": int(purchase_row["quantity"]),
                "sold_quantity": int(sale_row["quantity"]),
                "stock": current_stock,
                "minimum_stock": minimum_stock,
                "minimum_stock_warning": is_at_or_below_minimum_stock(current_stock, minimum_stock),
                "purchase_cost_cents": int(purchase_row["cost"]),
                "revenue_cents": int(sale_row["revenue"]),
                "collected_cents": int(sale_row["collected"]),
                "donation_cents": int(sale_row["donation"]),
                "is_offered": bool(label["is_offered"]),
                "article_is_offered": bool(label["article_is_offered"]),
                "is_available_for_sale": bool(
                    label["is_active"]
                    and label["article_is_active"]
                    and label["is_offered"]
                    and label["article_is_offered"]
                ),
                "is_active": bool(label["is_active"]),
            }
        )
    rows.sort(key=lambda item: (item["article_name"].casefold(), item["option_text"].casefold()))

    total_purchase_cost = sum(row["purchase_cost_cents"] for row in rows)
    total_revenue = sum(row["revenue_cents"] for row in rows)
    total_collected = sum(row["collected_cents"] for row in rows)
    total_donation = sum(row["donation_cents"] for row in rows)
    outstanding_paid = connection.execute(
        "SELECT COALESCE(SUM(amount_due_cents), 0) FROM sales WHERE is_paid = 0 AND is_cancelled = 0"
    ).fetchone()[0]
    pending_delivery = connection.execute(
        "SELECT COUNT(*) FROM sales WHERE is_received = 0 AND is_cancelled = 0"
    ).fetchone()[0]

    # These lists intentionally only use active sales. A cancellation must
    # disappear from this overview just as it already does from stock and
    # financial totals. Money rankings use cash marked as received; open
    # invoices remain visible in the dedicated headline metric above.
    top_selling_items = [
        dict(row)
        for row in connection.execute(
            """
            SELECT a.name AS label,
                   COALESCE(SUM(s.quantity), 0) AS quantity,
                   COALESCE(SUM(CASE WHEN s.is_paid = 1 THEN s.amount_due_cents ELSE 0 END), 0)
                       AS collected_cents
            FROM sales s
            JOIN variants v ON v.id = s.variant_id
            JOIN articles a ON a.id = v.article_id
            WHERE s.is_cancelled = 0
            GROUP BY a.id, a.name
            ORDER BY quantity DESC, collected_cents DESC, a.name COLLATE NOCASE
            LIMIT 5
            """
        ).fetchall()
    ]
    top_revenue_items = [
        dict(row)
        for row in connection.execute(
            """
            SELECT a.name AS label,
                   COALESCE(SUM(s.quantity), 0) AS quantity,
                   COALESCE(SUM(CASE WHEN s.is_paid = 1
                                     THEN s.amount_due_cents + s.donation_cents
                                     ELSE 0 END), 0) AS income_cents
            FROM sales s
            JOIN variants v ON v.id = s.variant_id
            JOIN articles a ON a.id = v.article_id
            WHERE s.is_cancelled = 0
            GROUP BY a.id, a.name
            ORDER BY income_cents DESC, quantity DESC, a.name COLLATE NOCASE
            LIMIT 5
            """
        ).fetchall()
    ]
    top_events = [
        dict(row)
        for row in connection.execute(
            """
            SELECT COALESCE(NULLIF(TRIM(s.event_name), ''), 'Ohne Veranstaltung') AS label,
                   COALESCE(SUM(s.quantity), 0) AS quantity,
                   COALESCE(SUM(CASE WHEN s.is_paid = 1
                                     THEN s.amount_due_cents + s.donation_cents
                                     ELSE 0 END), 0) AS income_cents
            FROM sales s
            WHERE s.is_cancelled = 0
            GROUP BY COALESCE(NULLIF(TRIM(s.event_name), ''), 'Ohne Veranstaltung')
            ORDER BY income_cents DESC, quantity DESC, label COLLATE NOCASE
            LIMIT 5
            """
        ).fetchall()
    ]
    top_sellers = [
        dict(row)
        for row in connection.execute(
            """
            SELECT COALESCE(NULLIF(TRIM(s.sold_by), ''), 'Nicht angegeben') AS label,
                   COALESCE(SUM(s.quantity), 0) AS quantity,
                   COALESCE(SUM(CASE WHEN s.is_paid = 1
                                     THEN s.amount_due_cents + s.donation_cents
                                     ELSE 0 END), 0) AS income_cents
            FROM sales s
            WHERE s.is_cancelled = 0
            GROUP BY COALESCE(NULLIF(TRIM(s.sold_by), ''), 'Nicht angegeben')
            ORDER BY income_cents DESC, quantity DESC, label COLLATE NOCASE
            LIMIT 5
            """
        ).fetchall()
    ]
    daily_income = [
        dict(row)
        for row in connection.execute(
            """
            SELECT sold_on AS date,
                   COALESCE(SUM(CASE WHEN is_paid = 1
                                     THEN amount_due_cents + donation_cents
                                     ELSE 0 END), 0) AS income_cents
            FROM sales
            WHERE is_cancelled = 0
            GROUP BY sold_on
            ORDER BY sold_on ASC
            """
        ).fetchall()
    ]
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
            "minimum_stock_warning_count": sum(1 for row in rows if row["minimum_stock_warning"]),
        },
        "analytics": {
            "top_selling_items": top_selling_items,
            "top_revenue_items": top_revenue_items,
            "top_events": top_events,
            "top_sellers": top_sellers,
            "daily_income": daily_income,
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
        RESET_ARCHIVE_DIR=str(data_dir / "reset-archives"),
        INVOICE_UPLOAD_DIR=str(data_dir / "invoices"),
        MAX_INVOICE_FILE_BYTES=MAX_INVOICE_FILE_BYTES,
        # Leave modest room for the multipart envelope while independently
        # enforcing the actual 10 MB file limit in ``invoice_file_extension``.
        MAX_CONTENT_LENGTH=MAX_INVOICE_FILE_BYTES + 1024 * 1024,
        BACKUP_RETENTION_DAYS=int(os.environ.get("BACKUP_RETENTION_DAYS", "90")),
        ADMIN_USERNAME=os.environ.get("ADMIN_USERNAME", "admin"),
        ADMIN_PASSWORD=os.environ.get("ADMIN_PASSWORD", "replace-this-password"),
        ACCOUNT_SETUP_CODE_DAYS=int(os.environ.get("ACCOUNT_SETUP_CODE_DAYS", "14")),
        PROFILE_REAUTH_SECONDS=int(os.environ.get("PROFILE_REAUTH_SECONDS", "600")),
        MFA_ISSUER=os.environ.get("MFA_ISSUER", "Protovibe Merch Manager").strip(),
        # A published image receives the GitHub release tag at Docker build
        # time.  The neutral fallback only applies to local development builds.
        APP_VERSION=os.environ.get("APP_VERSION", "0.0.0").strip(),
        # A public repository needs no token.  For a private repository, use a
        # separate, fine-grained read-only token; it remains server-side.
        UPDATE_CHECK_REPOSITORY=os.environ.get("UPDATE_CHECK_REPOSITORY", "TAWilts/protovibe-merch").strip(),
        UPDATE_CHECK_TOKEN=os.environ.get("UPDATE_CHECK_TOKEN", "").strip(),
        UPDATE_CHECK_TIMEOUT_SECONDS=float(os.environ.get("UPDATE_CHECK_TIMEOUT_SECONDS", "3")),
        UPDATE_CHECK_CACHE_SECONDS=int(os.environ.get("UPDATE_CHECK_CACHE_SECONDS", "21600")),
        AUTO_BACKUP=True,
        SESSION_COOKIE_HTTPONLY=True,
        SESSION_COOKIE_SAMESITE="Lax",
    )
    if test_config:
        app.config.update(test_config)
    if version_tuple(str(app.config["APP_VERSION"])) is None:
        raise RuntimeError("APP_VERSION muss dem Format vX.Y.Z entsprechen, zum Beispiel v0.3.0.")
    if app.config["SECRET_KEY"] == "development-only-change-me" and not app.config.get("TESTING"):
        raise RuntimeError("Set SECRET_KEY in .env before starting the app.")

    Path(app.config["DATABASE"]).parent.mkdir(parents=True, exist_ok=True)
    Path(app.config["BACKUP_DIR"]).mkdir(parents=True, exist_ok=True)
    Path(app.config["RESET_ARCHIVE_DIR"]).mkdir(parents=True, exist_ok=True)
    Path(app.config["INVOICE_UPLOAD_DIR"]).mkdir(parents=True, exist_ok=True)
    initialise_database(app)
    app.teardown_appcontext(close_db)

    @app.template_filter("money")
    def money_filter(value: int | None) -> str:
        return cents_to_money(value)

    @app.context_processor
    def inject_template_values() -> dict[str, Any]:
        return {
            "csrf_token": csrf_token,
            "current_user": g.get("user"),
            "role_labels": ROLE_LABELS,
            "payment_methods": PAYMENT_METHODS,
            "app_version": app.config["APP_VERSION"],
            "app_version_label": display_version(app.config["APP_VERSION"]),
        }

    @app.before_request
    def load_request_context() -> None:
        require_csrf()
        user_id = session.get("user_id")
        g.user = None
        if user_id:
            user = row_to_dict(get_db().execute("SELECT * FROM users WHERE id = ?", (user_id,)).fetchone())
            expected_session_version = session.get("user_session_version")
            if (
                user is None
                or not bool(user["is_active"])
                # Sessions from the former one-account release do not carry a
                # version.  Expire them once on this upgrade so an existing
                # Admin browser must pass through the mandatory MFA setup.
                or expected_session_version is None
                or int(expected_session_version) != int(user["session_version"] or 0)
            ):
                # Password resets, role changes and deactivations increment the
                # version and thereby invalidate every existing browser session.
                session.clear()
            else:
                g.user = user_capabilities(user)

    @app.after_request
    def prevent_sensitive_page_caching(response: Response) -> Response:
        """Keep passwords, QR codes and one-time recovery codes out of caches."""

        if request.endpoint in {
            "account_setup",
            "mfa_login",
            "mfa_enroll",
            "mfa_qr",
            "regenerate_recovery_codes",
            "profile_reauth",
            "profile_page",
        }:
            response.headers["Cache-Control"] = "no-store, max-age=0"
            response.headers["Pragma"] = "no-cache"
        return response

    @app.get("/")
    def index():
        return redirect(url_for("sales_page"))

    @app.route("/login", methods=["GET", "POST"])
    def login():
        if request.method == "POST":
            username = request.form.get("username", "").strip()
            password_or_setup_code = request.form.get("password", "")
            user = get_db().execute("SELECT * FROM users WHERE username = ?", (username,)).fetchone()
            if user is None or not bool(user["is_active"]):
                flash("Benutzername oder Passwort ist nicht korrekt.", "error")
            elif bool(user["must_set_password"]):
                if (
                    not user["setup_code_hash"]
                    or not is_setup_code_current(user)
                    or not check_password_hash(user["setup_code_hash"], password_or_setup_code)
                ):
                    flash("Benutzername oder Passwort ist nicht korrekt.", "error")
                else:
                    begin_auth_challenge("password_setup", user, request.args.get("next"))
                    return redirect(url_for("account_setup"))
            elif not check_password_hash(user["password_hash"], password_or_setup_code):
                flash("Benutzername oder Passwort ist nicht korrekt.", "error")
            else:
                # Admin access is deliberately impossible before its TOTP
                # device has been enrolled.  Other roles can opt into it in
                # their profile later.
                if normalized_role(user) == "admin" and not bool(user["mfa_enabled"]):
                    begin_auth_challenge("mfa_enrollment", user, request.args.get("next"))
                    return redirect(url_for("mfa_enroll"))
                if bool(user["mfa_enabled"]):
                    begin_auth_challenge("mfa_login", user, request.args.get("next"))
                    return redirect(url_for("mfa_login"))
                get_db().execute("UPDATE users SET last_login_at = ? WHERE id = ?", (utc_now(), user["id"]))
                get_db().commit()
                next_url = safe_next_url(request.args.get("next"), fallback=url_for("sales_page"))
                establish_authenticated_session(user)
                return redirect(next_url)
        return render_template("login.html", title="Anmelden")

    @app.route("/konto/einrichten", methods=["GET", "POST"])
    def account_setup():
        """Turn an admin-issued, one-time setup code into a private password."""

        user_id = session.get("password_setup_user_id")
        user = (
            get_db().execute("SELECT * FROM users WHERE id = ?", (user_id,)).fetchone()
            if user_id
            else None
        )
        if user is None or not bool(user["is_active"]) or not bool(user["must_set_password"]):
            session.clear()
            flash("Der Einrichtungsvorgang ist nicht mehr gültig. Bitte einen neuen Code anfordern.", "error")
            return redirect(url_for("login"))
        if request.method == "POST":
            try:
                password = validate_new_password(
                    request.form.get("password"), request.form.get("password_confirmation")
                )
                connection = get_db()
                connection.execute(
                    """
                    UPDATE users
                    SET password_hash = ?, must_set_password = 0,
                        setup_code_hash = NULL, setup_code_expires_at = NULL
                    WHERE id = ?
                    """,
                    (generate_password_hash(password), user["id"]),
                )
                audit(connection, "set_password", "user", user["id"], {"via": "setup_code"}, user_id=user["id"])
                connection.commit()
                refreshed_user = connection.execute("SELECT * FROM users WHERE id = ?", (user["id"],)).fetchone()
                next_url = take_post_auth_next()
                if normalized_role(refreshed_user) == "admin" and not bool(refreshed_user["mfa_enabled"]):
                    begin_auth_challenge("mfa_enrollment", refreshed_user, next_url)
                    return redirect(url_for("mfa_enroll"))
                if bool(refreshed_user["mfa_enabled"]):
                    begin_auth_challenge("mfa_login", refreshed_user, next_url)
                    return redirect(url_for("mfa_login"))
                connection.execute("UPDATE users SET last_login_at = ? WHERE id = ?", (utc_now(), user["id"]))
                connection.commit()
                establish_authenticated_session(refreshed_user)
                return redirect(next_url)
            except ValueError as exc:
                flash(str(exc), "error")
        return render_template("account_setup.html", title="Passwort einrichten", username=user["username"])

    @app.route("/mfa/anmelden", methods=["GET", "POST"])
    def mfa_login():
        """Finish a password login with a time-based code or recovery code."""

        user_id = session.get("mfa_login_user_id")
        user = (
            get_db().execute("SELECT * FROM users WHERE id = ?", (user_id,)).fetchone()
            if user_id
            else None
        )
        if user is None or not bool(user["is_active"]) or not bool(user["mfa_enabled"]):
            session.clear()
            flash("Die Zwei-Faktor-Anmeldung ist nicht mehr gültig. Bitte erneut anmelden.", "error")
            return redirect(url_for("login"))
        if request.method == "POST":
            connection = get_db()
            method = verify_mfa_code(connection, user, request.form.get("mfa_code"))
            if method is None:
                flash("Der Sicherheitscode ist nicht gültig.", "error")
            else:
                if method == "recovery":
                    refreshed_user = connection.execute(
                        "SELECT * FROM users WHERE id = ?", (user["id"],)
                    ).fetchone()
                    audit(
                        connection,
                        "use_recovery_code",
                        "user",
                        user["id"],
                        {"remaining_codes": len(recovery_code_hashes(refreshed_user))},
                        user_id=user["id"],
                    )
                connection.execute("UPDATE users SET last_login_at = ? WHERE id = ?", (utc_now(), user["id"]))
                connection.commit()
                next_url = take_post_auth_next()
                refreshed_user = connection.execute("SELECT * FROM users WHERE id = ?", (user["id"],)).fetchone()
                establish_authenticated_session(refreshed_user)
                return redirect(next_url)
        return render_template("mfa_login.html", title="Sicherheitscode", username=user["username"])

    def mfa_enrollment_target() -> tuple[sqlite3.Row | None, bool]:
        """Return the user currently allowed to enrol/re-enrol a TOTP device."""

        if g.get("user") is not None:
            if not has_profile_reauth(g.user):
                return None, False
            return get_db().execute("SELECT * FROM users WHERE id = ?", (g.user["id"],)).fetchone(), False
        user_id = session.get("mfa_enrollment_user_id")
        if not user_id:
            return None, False
        user = get_db().execute("SELECT * FROM users WHERE id = ?", (user_id,)).fetchone()
        if user is None or not bool(user["is_active"]) or normalized_role(user) != "admin":
            return None, False
        return user, True

    @app.route("/profil/2fa/einrichten", methods=["GET", "POST"])
    @app.route("/mfa/einrichten", methods=["GET", "POST"])
    def mfa_enroll():
        """Show a QR code and only enable TOTP after a live-code confirmation."""

        user, is_pre_auth = mfa_enrollment_target()
        if user is None:
            if g.get("user") is not None:
                return redirect(url_for("profile_reauth", next=request.path))
            session.clear()
            flash("Die Zwei-Faktor-Einrichtung ist nicht mehr gültig. Bitte erneut anmelden.", "error")
            return redirect(url_for("login"))
        connection = get_db()
        pending_secret = decrypt_mfa_secret(user["mfa_pending_secret_encrypted"])
        if pending_secret is None:
            pending_secret = pyotp.random_base32()
            connection.execute(
                "UPDATE users SET mfa_pending_secret_encrypted = ? WHERE id = ?",
                (encrypt_mfa_secret(pending_secret), user["id"]),
            )
            connection.commit()
            user = connection.execute("SELECT * FROM users WHERE id = ?", (user["id"],)).fetchone()
        if request.method == "POST":
            code = str(request.form.get("mfa_code", "")).strip().replace(" ", "")
            if not pyotp.TOTP(pending_secret).verify(code, valid_window=1):
                flash("Der Sicherheitscode stimmt nicht. Bitte QR-Code erneut scannen und einen aktuellen Code eingeben.", "error")
            else:
                recovery_codes = generate_recovery_codes()
                connection.execute(
                    """
                    UPDATE users
                    SET mfa_secret_encrypted = ?, mfa_pending_secret_encrypted = NULL,
                        mfa_recovery_code_hashes_json = ?, mfa_enabled = 1, mfa_enrolled_at = ?
                    WHERE id = ?
                    """,
                    (
                        encrypt_mfa_secret(pending_secret),
                        json.dumps([generate_password_hash(item) for item in recovery_codes]),
                        utc_now(),
                        user["id"],
                    ),
                )
                audit(
                    connection,
                    "enable_mfa",
                    "user",
                    user["id"],
                    {"role": normalized_role(user)},
                    user_id=user["id"],
                )
                connection.commit()
                backup_after_commit()
                return_url = url_for("profile_page")
                template_user: dict[str, Any] | None = g.get("user")
                if is_pre_auth:
                    return_url = take_post_auth_next()
                    refreshed_user = connection.execute("SELECT * FROM users WHERE id = ?", (user["id"],)).fetchone()
                    establish_authenticated_session(refreshed_user)
                    template_user = user_capabilities(dict(refreshed_user))
                return render_template(
                    "mfa_recovery_codes.html",
                    title="Wiederherstellungscodes",
                    recovery_codes=recovery_codes,
                    return_url=return_url,
                    current_user=template_user,
                )
        provisioning_uri = pyotp.TOTP(pending_secret).provisioning_uri(
            name=user["username"], issuer_name=current_app.config["MFA_ISSUER"]
        )
        return render_template(
            "mfa_enroll.html",
            title="Zwei-Faktor-Authentifizierung",
            username=user["username"],
            manual_secret=pending_secret,
            provisioning_uri=provisioning_uri,
            is_required=normalized_role(user) == "admin",
            is_pre_auth=is_pre_auth,
        )

    @app.get("/mfa/qr")
    def mfa_qr():
        """Serve the short-lived, session-bound QR image for an enrolment."""

        user, _ = mfa_enrollment_target()
        if user is None:
            abort(403)
        pending_secret = decrypt_mfa_secret(user["mfa_pending_secret_encrypted"])
        if pending_secret is None:
            abort(404)
        provisioning_uri = pyotp.TOTP(pending_secret).provisioning_uri(
            name=user["username"], issuer_name=current_app.config["MFA_ISSUER"]
        )
        image = qrcode.make(provisioning_uri)
        buffer = io.BytesIO()
        image.save(buffer, format="PNG")
        buffer.seek(0)
        return send_file(buffer, mimetype="image/png", max_age=0)

    @app.route("/profil/zugriff", methods=["GET", "POST"])
    @login_required
    def profile_reauth():
        """Freshly verify the current password before exposing account data."""

        target = safe_next_url(request.args.get("next"), fallback=url_for("profile_page"))
        if has_profile_reauth(g.user):
            return redirect(target)
        if request.method == "POST":
            password = request.form.get("password", "")
            connection = get_db()
            user = connection.execute("SELECT * FROM users WHERE id = ?", (g.user["id"],)).fetchone()
            if not check_password_hash(user["password_hash"], password):
                flash("Das Passwort ist nicht korrekt.", "error")
            else:
                method = "password"
                if bool(user["mfa_enabled"]):
                    method = verify_mfa_code(connection, user, request.form.get("mfa_code")) or ""
                    if not method:
                        flash("Der Sicherheitscode ist nicht gültig.", "error")
                        return render_template(
                            "profile_reauth.html",
                            title="Zugriff bestätigen",
                            target=target,
                            needs_mfa=True,
                        )
                if method == "recovery":
                    audit(
                        connection,
                        "use_recovery_code",
                        "user",
                        user["id"],
                        {"context": "profile_reauth"},
                        user_id=user["id"],
                    )
                connection.commit()
                session["profile_reauth_user_id"] = int(user["id"])
                session["profile_reauth_until"] = time.time() + int(current_app.config["PROFILE_REAUTH_SECONDS"])
                return redirect(target)
        return render_template(
            "profile_reauth.html",
            title="Zugriff bestätigen",
            target=target,
            needs_mfa=bool(g.user["mfa_enabled"]),
        )

    @app.get("/profil")
    @login_required
    @profile_reauth_required
    def profile_page():
        user = get_db().execute("SELECT * FROM users WHERE id = ?", (g.user["id"],)).fetchone()
        return render_template(
            "profile.html",
            title="Mein Profil",
            profile_user=user_capabilities(dict(user)),
            recovery_code_count=len(recovery_code_hashes(user)),
        )

    @app.post("/profil/passwort")
    @login_required
    @profile_reauth_required
    def update_own_password():
        try:
            connection = get_db()
            user = connection.execute("SELECT * FROM users WHERE id = ?", (g.user["id"],)).fetchone()
            if not check_password_hash(user["password_hash"], request.form.get("current_password", "")):
                raise ValueError("Das aktuelle Passwort ist nicht korrekt.")
            password = validate_new_password(
                request.form.get("password"), request.form.get("password_confirmation")
            )
            connection.execute(
                """
                UPDATE users
                SET password_hash = ?, session_version = session_version + 1,
                    must_set_password = 0, setup_code_hash = NULL, setup_code_expires_at = NULL
                WHERE id = ?
                """,
                (generate_password_hash(password), g.user["id"]),
            )
            audit(connection, "change_password", "user", g.user["id"], {"via": "profile"})
            connection.commit()
            refreshed = connection.execute("SELECT * FROM users WHERE id = ?", (g.user["id"],)).fetchone()
            session["user_session_version"] = int(refreshed["session_version"])
            flash("Dein Passwort wurde geändert. Andere Sitzungen wurden abgemeldet.", "success")
        except ValueError as exc:
            flash(str(exc), "error")
        return redirect(url_for("profile_page"))

    @app.post("/profil/benutzername")
    @login_required
    @profile_reauth_required
    def update_own_username():
        """Change the locally displayed/login username after fresh re-auth."""

        connection = get_db()
        try:
            user = connection.execute("SELECT * FROM users WHERE id = ?", (g.user["id"],)).fetchone()
            username = valid_username(request.form.get("username"))
            if username.casefold() == str(user["username"]).casefold():
                flash("Der Benutzername wurde nicht verändert.", "success")
                return redirect(url_for("profile_page"))
            duplicate = connection.execute(
                "SELECT id FROM users WHERE username = ? AND id != ?", (username, g.user["id"])
            ).fetchone()
            if duplicate is not None:
                raise ValueError("Dieser Benutzername ist bereits vergeben.")
            previous_username = str(user["username"])
            connection.execute(
                "UPDATE users SET username = ?, session_version = session_version + 1 WHERE id = ?",
                (username, g.user["id"]),
            )
            audit(
                connection,
                "change_username",
                "user",
                g.user["id"],
                {"previous_username": previous_username, "username": username},
            )
            connection.commit()
            refreshed = connection.execute("SELECT * FROM users WHERE id = ?", (g.user["id"],)).fetchone()
            session["user_session_version"] = int(refreshed["session_version"])
            flash("Dein Benutzername wurde geändert. Beim nächsten Anmelden verwendest du den neuen Namen.", "success")
        except ValueError as exc:
            connection.rollback()
            flash(str(exc), "error")
        return redirect(url_for("profile_page"))

    @app.post("/profil/2fa/deaktivieren")
    @login_required
    @profile_reauth_required
    def disable_own_mfa():
        if normalized_role(g.user) == "admin":
            flash("Für den Admin ist die Zwei-Faktor-Authentifizierung verpflichtend.", "error")
            return redirect(url_for("profile_page"))
        connection = get_db()
        connection.execute(
            """
            UPDATE users
            SET mfa_enabled = 0, mfa_secret_encrypted = NULL, mfa_pending_secret_encrypted = NULL,
                mfa_recovery_code_hashes_json = '[]', mfa_enrolled_at = NULL
            WHERE id = ?
            """,
            (g.user["id"],),
        )
        audit(connection, "disable_mfa", "user", g.user["id"], {"via": "profile"})
        connection.commit()
        backup_after_commit()
        flash("Die Zwei-Faktor-Authentifizierung wurde deaktiviert.", "success")
        return redirect(url_for("profile_page"))

    @app.post("/profil/2fa/wiederherstellungscodes")
    @login_required
    @profile_reauth_required
    def regenerate_recovery_codes():
        connection = get_db()
        user = connection.execute("SELECT * FROM users WHERE id = ?", (g.user["id"],)).fetchone()
        if not bool(user["mfa_enabled"]):
            flash("Aktiviere zuerst die Zwei-Faktor-Authentifizierung.", "error")
            return redirect(url_for("profile_page"))
        recovery_codes = generate_recovery_codes()
        connection.execute(
            "UPDATE users SET mfa_recovery_code_hashes_json = ? WHERE id = ?",
            (json.dumps([generate_password_hash(item) for item in recovery_codes]), user["id"]),
        )
        audit(connection, "regenerate_recovery_codes", "user", user["id"], {})
        connection.commit()
        backup_after_commit()
        return render_template(
            "mfa_recovery_codes.html",
            title="Neue Wiederherstellungscodes",
            recovery_codes=recovery_codes,
            return_url=url_for("profile_page"),
        )

    @app.post("/logout")
    @login_required
    def logout():
        session.clear()
        return redirect(url_for("login"))

    def administration_users() -> list[dict[str, Any]]:
        """Return safe, display-ready user records for the admin screen."""

        rows = get_db().execute(
            """
            SELECT * FROM users
            ORDER BY CASE role WHEN 'admin' THEN 0 WHEN 'manager' THEN 1 ELSE 2 END,
                     username COLLATE NOCASE
            """
        ).fetchall()
        users = []
        for row in rows:
            user = user_capabilities(dict(row))
            user["recovery_code_count"] = len(recovery_code_hashes(row))
            users.append(user)
        return users

    def render_administration(
        *, setup_credential: dict[str, str] | None = None, reset_archive_name: str | None = None
    ):
        return render_template(
            "admin.html",
            title="Verwaltung",
            users=administration_users(),
            setup_credential=setup_credential,
            reset_archive_name=reset_archive_name,
            setup_code_days=int(current_app.config["ACCOUNT_SETUP_CODE_DAYS"]),
        )

    @app.get("/verwaltung")
    @login_required
    @admin_required
    def administration_page():
        return render_administration()

    @app.post("/verwaltung/benutzer")
    @login_required
    @admin_required
    def create_user():
        try:
            username = valid_username(request.form.get("username"))
            role = str(request.form.get("role", "seller")).strip().lower()
            if role not in MANAGED_USER_ROLES:
                raise ValueError("Neue Benutzer können nur die Rollen Seller oder Manager erhalten.")
            setup_code = generate_setup_code()
            connection = get_db()
            connection.execute(
                """
                INSERT INTO users (
                    username, password_hash, is_admin, role, is_active, must_set_password,
                    setup_code_hash, setup_code_expires_at, created_at
                ) VALUES (?, ?, 0, ?, 1, 1, ?, ?, ?)
                """,
                (
                    username,
                    generate_password_hash(secrets.token_urlsafe(48)),
                    role,
                    generate_password_hash(setup_code),
                    setup_code_expiry(int(current_app.config["ACCOUNT_SETUP_CODE_DAYS"])),
                    utc_now(),
                ),
            )
            user_id = connection.execute("SELECT id FROM users WHERE username = ?", (username,)).fetchone()[0]
            audit(connection, "create", "user", user_id, {"username": username, "role": role})
            connection.commit()
            backup_after_commit()
            return render_administration(
                setup_credential={"username": username, "code": setup_code, "purpose": "new"}
            )
        except (ValueError, sqlite3.IntegrityError) as exc:
            flash("Dieser Benutzer konnte nicht angelegt werden: " + str(exc), "error")
            return redirect(url_for("administration_page"))

    @app.post("/verwaltung/benutzer/<int:user_id>/passwort-zuruecksetzen")
    @login_required
    @admin_required
    def reset_user_password(user_id: int):
        connection = get_db()
        user = connection.execute("SELECT * FROM users WHERE id = ?", (user_id,)).fetchone()
        if user is None:
            abort(404)
        if normalized_role(user) == "admin":
            flash("Das Admin-Passwort wird ausschließlich im eigenen Profil geändert.", "error")
            return redirect(url_for("administration_page"))
        setup_code = generate_setup_code()
        connection.execute(
            """
            UPDATE users
            SET password_hash = ?, must_set_password = 1, setup_code_hash = ?,
                setup_code_expires_at = ?, session_version = session_version + 1
            WHERE id = ?
            """,
            (
                generate_password_hash(secrets.token_urlsafe(48)),
                generate_password_hash(setup_code),
                setup_code_expiry(int(current_app.config["ACCOUNT_SETUP_CODE_DAYS"])),
                user_id,
            ),
        )
        audit(connection, "reset_password", "user", user_id, {"username": user["username"]})
        connection.commit()
        backup_after_commit()
        return render_administration(
            setup_credential={"username": user["username"], "code": setup_code, "purpose": "reset"}
        )

    @app.post("/verwaltung/benutzer/<int:user_id>/rolle")
    @login_required
    @admin_required
    def update_user_role(user_id: int):
        role = str(request.form.get("role", "")).strip().lower()
        if role not in MANAGED_USER_ROLES:
            flash("Es sind nur die Rollen Seller und Manager auswählbar.", "error")
            return redirect(url_for("administration_page"))
        connection = get_db()
        user = connection.execute("SELECT * FROM users WHERE id = ?", (user_id,)).fetchone()
        if user is None:
            abort(404)
        if normalized_role(user) == "admin":
            flash("Die einzige Admin-Rolle kann nicht geändert werden.", "error")
            return redirect(url_for("administration_page"))
        connection.execute(
            "UPDATE users SET role = ?, is_admin = 0, session_version = session_version + 1 WHERE id = ?",
            (role, user_id),
        )
        audit(connection, "change_role", "user", user_id, {"username": user["username"], "role": role})
        connection.commit()
        backup_after_commit()
        flash("Die Rolle von „{}“ wurde geändert.".format(user["username"]), "success")
        return redirect(url_for("administration_page"))

    @app.post("/verwaltung/benutzer/<int:user_id>/aktiv")
    @login_required
    @admin_required
    def update_user_active_state(user_id: int):
        connection = get_db()
        user = connection.execute("SELECT * FROM users WHERE id = ?", (user_id,)).fetchone()
        if user is None:
            abort(404)
        if normalized_role(user) == "admin":
            flash("Der einzige Admin kann nicht deaktiviert werden.", "error")
            return redirect(url_for("administration_page"))
        active = request.form.get("active") == "1"
        connection.execute(
            "UPDATE users SET is_active = ?, session_version = session_version + 1 WHERE id = ?",
            (int(active), user_id),
        )
        audit(
            connection,
            "activate_user" if active else "deactivate_user",
            "user",
            user_id,
            {"username": user["username"], "is_active": active},
        )
        connection.commit()
        backup_after_commit()
        flash("Der Benutzer wurde {}.".format("aktiviert" if active else "deaktiviert"), "success")
        return redirect(url_for("administration_page"))

    @app.post("/verwaltung/benutzer/<int:user_id>/2fa-zuruecksetzen")
    @login_required
    @admin_required
    def reset_user_mfa(user_id: int):
        connection = get_db()
        user = connection.execute("SELECT * FROM users WHERE id = ?", (user_id,)).fetchone()
        if user is None:
            abort(404)
        if normalized_role(user) == "admin":
            flash("Die verpflichtende Admin-2FA kann nur durch die Wiederherstellungscodes des Admins ersetzt werden.", "error")
            return redirect(url_for("administration_page"))
        connection.execute(
            """
            UPDATE users
            SET mfa_enabled = 0, mfa_secret_encrypted = NULL, mfa_pending_secret_encrypted = NULL,
                mfa_recovery_code_hashes_json = '[]', mfa_enrolled_at = NULL,
                session_version = session_version + 1
            WHERE id = ?
            """,
            (user_id,),
        )
        audit(connection, "reset_mfa", "user", user_id, {"username": user["username"]})
        connection.commit()
        backup_after_commit()
        flash("Die 2FA von „{}“ wurde zurückgesetzt.".format(user["username"]), "success")
        return redirect(url_for("administration_page"))

    @app.post("/verwaltung/daten-zuruecksetzen")
    @login_required
    @admin_required
    def reset_application_data():
        """Archive all data, then start a blank ledger with the verified admin."""

        if request.form.get("confirmation", "").strip() != "DATEN ZURÜCKSETZEN":
            flash("Bitte die Bestätigung exakt als „DATEN ZURÜCKSETZEN“ eingeben.", "error")
            return render_administration()
        connection = get_db()
        admin = connection.execute("SELECT * FROM users WHERE id = ?", (g.user["id"],)).fetchone()
        if not check_password_hash(admin["password_hash"], request.form.get("password", "")):
            flash("Das Passwort ist nicht korrekt. Es wurden keine Daten verändert.", "error")
            return render_administration()
        mfa_method = verify_mfa_code(connection, admin, request.form.get("mfa_code"))
        if mfa_method is None:
            flash("Der Zwei-Faktor-Code ist nicht gültig. Es wurden keine Daten verändert.", "error")
            return render_administration()
        if mfa_method == "recovery":
            audit(
                connection,
                "use_recovery_code",
                "user",
                admin["id"],
                {"context": "data_reset"},
                user_id=admin["id"],
            )
        connection.commit()
        try:
            archive_path = create_reset_archive(app, connection)
            # A recovery code may have been consumed while confirming this
            # reset. Preserve the post-verification state, never a stale copy
            # that would accidentally make that code valid again.
            admin = connection.execute("SELECT * FROM users WHERE id = ?", (admin["id"],)).fetchone()
            preserved_admin = dict(admin)
            close_db(None)
            reset_data_store(app, preserved_admin)
            fresh_connection = get_db()
            audit(
                fresh_connection,
                "reset_application_data",
                "system",
                None,
                {"archive": archive_path.name, "preserved_admin": preserved_admin["username"]},
                user_id=preserved_admin["id"],
            )
            fresh_connection.commit()
        except Exception:
            current_app.logger.exception("Could not reset application data")
            flash("Die Daten konnten nicht zurückgesetzt werden. Das Reset-Archiv wurde nicht gelöscht.", "error")
            return redirect(url_for("administration_page"))
        session.clear()
        flash(
            "Alle Artikel, Buchungen, Anhänge und weiteren Benutzer wurden zurückgesetzt. "
            f"Das Archiv „{archive_path.name}“ wurde angelegt; der Admin-Zugang bleibt erhalten.",
            "success",
        )
        return redirect(url_for("login"))

    @app.get("/updates")
    @login_required
    @admin_required
    def updates_page():
        """Show release state and deliberately non-automatic update guidance."""

        return render_template(
            "updates.html",
            title="Updates",
            update_repository=app.config["UPDATE_CHECK_REPOSITORY"],
        )

    @app.get("/api/update-status")
    @login_required
    @admin_required
    def api_update_status():
        """Provide a cached GitHub release comparison for the admin UI."""

        force = request.args.get("force", "").lower() in {"1", "true", "yes"}
        return jsonify(update_status(app, force=force))

    @app.get("/verkauf")
    @login_required
    def sales_page():
        return render_template(
            "sales.html", title="Verkauf", articles=article_payload(get_db(), offered_only=True), today=today_iso()
        )

    @app.get("/api/receipt-preview")
    @login_required
    def receipt_preview():
        kind = request.args.get("kind", "sale")
        prefix = "V" if kind == "sale" else "E"
        return jsonify({"ok": True, "receipt_id": next_receipt_id(get_db(), prefix)})

    @app.post("/api/sales")
    @login_required
    def create_sale():
        """Create one receipt with one or more sale ledger rows.

        ``items`` is the basket API introduced with v0.2.5.  A basket item can
        optionally carry ``unit_price``; without it the configured variant
        price remains the default.  The legacy ``variant_id``/``quantity``
        shape remains accepted so that a browser tab left open during an
        update cannot lose a sale.
        """

        payload = request.get_json(silent=True) or {}
        connection = get_db()
        try:
            raw_items = payload.get("items")
            if raw_items is None:
                raw_items = [{"variant_id": payload.get("variant_id"), "quantity": payload.get("quantity")}]
            if not isinstance(raw_items, list) or not raw_items:
                raise ValueError("Der Warenkorb enthält noch keine Artikel.")

            basket_items: list[dict[str, Any]] = []
            for raw_item in raw_items:
                if not isinstance(raw_item, dict):
                    raise ValueError("Ungültiger Artikel im Warenkorb.")
                variant_id = int(raw_item.get("variant_id"))
                quantity = parse_positive_int(raw_item.get("quantity"))
                variant = connection.execute(
                    """
                    SELECT v.*
                    FROM variants v
                    JOIN articles a ON a.id = v.article_id
                    WHERE v.id = ?
                      AND v.is_active = 1 AND v.is_offered = 1
                      AND a.is_active = 1 AND a.is_offered = 1
                    """,
                    (variant_id,),
                ).fetchone()
                if variant is None:
                    raise ValueError("Diese Artikelvariante wird nicht mehr angeboten.")
                raw_unit_price = raw_item.get("unit_price")
                unit_price_cents = (
                    int(variant["sale_price_cents"])
                    if raw_unit_price is None or not str(raw_unit_price).strip()
                    else money_to_cents(raw_unit_price, field_name="Preis pro Stück")
                )
                amount_due = quantity * unit_price_cents
                basket_items.append(
                    {
                        "variant_id": variant_id,
                        "quantity": quantity,
                        "unit_price_cents": unit_price_cents,
                        "amount_due_cents": amount_due,
                    }
                )

            is_paid = bool(payload.get("is_paid", True))
            is_received = bool(payload.get("is_received", True))
            payment_method = str(payload.get("payment_method", "")).strip()
            if payment_method not in PAYMENT_METHODS:
                raise ValueError("Bitte eine gültige Bezahlart auswählen.")
            customer_name = str(payload.get("customer_name", "")).strip()
            customer_address = str(payload.get("customer_address", "")).strip()
            sold_by = str(payload.get("sold_by", "")).strip()
            if (not is_received or not is_paid) and (not customer_name or not customer_address):
                raise ValueError("Bei nicht bezahlten oder noch nicht erhaltenen Artikeln sind Name und Adresse Pflicht.")
            # A sale collected at the merch table never enters the shipment
            # workflow.  A non-collected sale starts as "not sent" and can be
            # progressed later in the dedicated operations tab.
            delivery_status = "not_applicable" if is_received else "pending"
            payment_follow_up = int(not is_paid)

            amount_due = sum(item["amount_due_cents"] for item in basket_items)
            given_raw = payload.get("amount_given")
            amount_given = (
                None
                if given_raw is None or not str(given_raw).strip()
                else money_to_cents(given_raw, field_name="Gegeben")
            )
            if is_paid and amount_given is not None and amount_given < amount_due:
                raise ValueError("Wenn „Bezahlt“ markiert ist, darf „Gegeben“ nicht kleiner als der Betrag sein.")
            # An unpaid booking must never accidentally count as a donation just
            # because a stale browser field was sent with it.
            if not is_paid:
                amount_given = None
                donation = 0
            else:
                donation = max(0, (amount_given or 0) - amount_due)
            donation_shares = distribute_cents(donation, [item["amount_due_cents"] for item in basket_items])
            for item, donation_share in zip(basket_items, donation_shares):
                item["donation_cents"] = donation_share
                item["amount_given_cents"] = (
                    None if amount_given is None else item["amount_due_cents"] + donation_share
                )

            sold_on = str(payload.get("sold_on") or today_iso())
            date.fromisoformat(sold_on)
            event_name = str(payload.get("event_name", "")).strip() or None
            comment = str(payload.get("comment", "")).strip() or None

            connection.execute("BEGIN IMMEDIATE")
            # The inventory is a ledger, not a hard sales lock: a missed
            # purchase entry or a later shipment must not prevent the merch
            # stand from recording a real sale.  Holding the write lock gives
            # the response authoritative post-sale stock values for every
            # basket line.
            receipt_id = unique_receipt_id(connection, "V", payload.get("receipt_id"), sold_on)
            for item in basket_items:
                cursor = connection.execute(
                    """
                    INSERT INTO sales (
                        receipt_id, variant_id, quantity, unit_price_cents, amount_due_cents,
                        amount_given_cents, donation_cents, payment_method, is_paid, payment_follow_up, is_received,
                        delivery_status, customer_name, customer_address, event_name, sold_by, comment,
                        sold_on, created_at, created_by
                    ) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
                    """,
                    (
                        receipt_id, item["variant_id"], item["quantity"], item["unit_price_cents"],
                        item["amount_due_cents"], item["amount_given_cents"], item["donation_cents"],
                        payment_method, int(is_paid), payment_follow_up, int(is_received), delivery_status,
                        customer_name or None, customer_address or None, event_name, sold_by or None,
                        comment, sold_on, utc_now(), g.user["id"],
                    ),
                )
                item["sale_id"] = cursor.lastrowid
                audit(
                    connection,
                    "create",
                    "sale",
                    cursor.lastrowid,
                    {
                        "receipt_id": receipt_id,
                        "quantity": item["quantity"],
                        "unit_price_cents": item["unit_price_cents"],
                        "cart_item_count": len(basket_items),
                        "is_paid": is_paid,
                        "payment_follow_up": bool(payment_follow_up),
                        "delivery_status": delivery_status,
                        "sold_by": sold_by or None,
                    },
                )
            for item in basket_items:
                item["stock_after_sale"] = stock_for_variant(connection, item["variant_id"])
            connection.commit()
            backup_after_commit()
            first_item = basket_items[0]
            return jsonify(
                {
                    "ok": True,
                    "receipt_id": receipt_id,
                    # Keep these two top-level fields for a browser that was
                    # still open on the one-item UI during an upgrade.
                    "variant_id": first_item["variant_id"],
                    "stock_after_sale": first_item["stock_after_sale"],
                    "amount_due_cents": amount_due,
                    "donation_cents": donation,
                    "items": basket_items,
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
        connection = get_db()
        purchase_rows = connection.execute("SELECT * FROM purchases ORDER BY purchased_on DESC, id DESC").fetchall()
        return render_template(
            "purchases.html",
            title="Einkäufe",
            articles=article_payload(connection),
            receipts=purchase_receipt_payload(connection, purchase_rows),
            today=today_iso(),
            can_manage_purchases=has_role(g.user, "manager"),
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
    @manager_required
    def create_purchase():
        """Create a single legacy purchase or a complete multi-item cart."""

        payload, uploaded_files = purchase_request_payload()
        connection = get_db()
        stored_files: list[str] = []
        try:
            purchased_on, cart_items, cart_invoice_files = purchase_items_from_payload(
                connection, payload, uploaded_files
            )

            connection.execute("BEGIN IMMEDIATE")
            receipt_id = unique_receipt_id(connection, "E", payload.get("receipt_id"), purchased_on)
            for item_index, item in enumerate(cart_items):
                invoice_file_path = save_invoice_file(item["uploaded_invoice"], receipt_id)
                if invoice_file_path:
                    stored_files.append(invoice_file_path)
                cursor = connection.execute(
                    """
                    INSERT INTO purchases (
                        receipt_id, variant_id, quantity, unit_cost_cents, purchased_on,
                        supplier, invoice_reference, invoice_file_path, comment, created_at, created_by
                    ) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
                    """,
                    (
                        receipt_id, item["variant_id"], item["quantity"], item["unit_cost_cents"], purchased_on,
                        item["supplier"], item["invoice_reference"], invoice_file_path, item["comment"],
                        utc_now(), g.user["id"],
                    ),
                )
                item["purchase_id"] = cursor.lastrowid
                audit(
                    connection,
                    "create",
                    "purchase",
                    cursor.lastrowid,
                    {
                        "receipt_id": receipt_id,
                        "quantity": item["quantity"],
                        "cart_item_count": len(cart_items),
                        "cart_item_index": item_index,
                        "has_invoice_file": bool(invoice_file_path),
                    },
                )

            for uploaded_invoice in cart_invoice_files:
                invoice_file_path = save_invoice_file(uploaded_invoice, receipt_id)
                if invoice_file_path:
                    stored_files.append(invoice_file_path)
                    connection.execute(
                        """
                        INSERT INTO purchase_receipt_attachments (
                            receipt_id, file_path, created_at, created_by
                        ) VALUES (?, ?, ?, ?)
                        """,
                        (receipt_id, invoice_file_path, utc_now(), g.user["id"]),
                    )
            audit(
                connection,
                "create",
                "purchase_receipt",
                cart_items[0]["purchase_id"],
                {
                    "receipt_id": receipt_id,
                    "purchased_on": purchased_on,
                    "cart_item_count": len(cart_items),
                    "cart_attachment_count": len(cart_invoice_files),
                },
            )
            connection.commit()
            backup_after_commit()
            return jsonify(
                {
                    "ok": True,
                    "receipt_id": receipt_id,
                    "item_count": len(cart_items),
                    "cart_attachment_count": len(cart_invoice_files),
                    # Kept for the former single-purchase client/API.  New
                    # clients can use the more precise item/cart counts.
                    "has_invoice_file": any(item["uploaded_invoice"] for item in cart_items),
                    "message": "Einkaufswarenkorb erfolgreich erfasst.",
                }
            )
        except (ValueError, TypeError) as exc:
            connection.rollback()
            for stored_file in stored_files:
                delete_invoice_file(stored_file)
            return jsonify({"ok": False, "error": str(exc)}), 400
        except Exception:
            connection.rollback()
            for stored_file in stored_files:
                delete_invoice_file(stored_file)
            current_app.logger.exception("Could not create purchase")
            return jsonify({"ok": False, "error": "Der Einkauf konnte nicht gespeichert werden."}), 500

    @app.patch("/api/purchases/<int:purchase_id>")
    @login_required
    @manager_required
    def update_purchase(purchase_id: int):
        """Correct a purchase after an explicit client-side safety delay."""

        payload, uploaded_files = purchase_request_payload()
        connection = get_db()
        stored_invoice: str | None = None
        old_invoice: str | None = None
        try:
            purchase = connection.execute("SELECT * FROM purchases WHERE id = ?", (purchase_id,)).fetchone()
            if purchase is None:
                return jsonify({"ok": False, "error": "Einkauf wurde nicht gefunden."}), 404

            values = purchase_values_from_payload(
                connection,
                payload,
                current_variant_id=int(purchase["variant_id"]),
                current_purchased_on=str(purchase["purchased_on"]),
            )
            old_invoice = purchase["invoice_file_path"]
            uploaded_invoice = uploaded_files.get("invoice_file")
            if uploaded_invoice is not None and getattr(uploaded_invoice, "filename", ""):
                stored_invoice = save_invoice_file(uploaded_invoice, purchase["receipt_id"])
            invoice_file_path = stored_invoice if stored_invoice is not None else old_invoice

            connection.execute("BEGIN IMMEDIATE")
            # A receipt represents one shopping trip/day.  Keep that shared
            # date coherent even for older API clients that still send an
            # editable ``purchased_on`` field for a single line.
            if values["purchased_on"] != purchase["purchased_on"]:
                connection.execute(
                    "UPDATE purchases SET purchased_on = ? WHERE receipt_id = ?",
                    (values["purchased_on"], purchase["receipt_id"]),
                )
            connection.execute(
                """
                UPDATE purchases
                SET variant_id = ?, quantity = ?, unit_cost_cents = ?, purchased_on = ?,
                    supplier = ?, invoice_reference = ?, invoice_file_path = ?, comment = ?
                WHERE id = ?
                """,
                (
                    values["variant_id"], values["quantity"], values["unit_cost_cents"], values["purchased_on"],
                    values["supplier"], values["invoice_reference"], invoice_file_path, values["comment"], purchase_id,
                ),
            )
            audit(
                connection,
                "update",
                "purchase",
                purchase_id,
                {
                    "receipt_id": purchase["receipt_id"],
                    "before": {
                        "variant_id": purchase["variant_id"],
                        "quantity": purchase["quantity"],
                        "unit_cost_cents": purchase["unit_cost_cents"],
                        "purchased_on": purchase["purchased_on"],
                    },
                    "after": {
                        "variant_id": values["variant_id"],
                        "quantity": values["quantity"],
                        "unit_cost_cents": values["unit_cost_cents"],
                        "purchased_on": values["purchased_on"],
                    },
                    "invoice_replaced": bool(stored_invoice),
                    "receipt_date_changed": values["purchased_on"] != purchase["purchased_on"],
                },
            )
            connection.commit()
        except (ValueError, TypeError) as exc:
            connection.rollback()
            if stored_invoice:
                delete_invoice_file(stored_invoice)
            return jsonify({"ok": False, "error": str(exc)}), 400
        except Exception:
            connection.rollback()
            if stored_invoice:
                delete_invoice_file(stored_invoice)
            current_app.logger.exception("Could not update purchase")
            return jsonify({"ok": False, "error": "Der Einkauf konnte nicht bearbeitet werden."}), 500

        # The database change is already durable.  A failure here leaves at
        # most an unused old file, never a booking that points at a missing
        # replacement invoice.
        if stored_invoice and old_invoice and old_invoice != stored_invoice:
            try:
                delete_invoice_file(old_invoice)
            except OSError:
                current_app.logger.exception("Could not remove replaced purchase invoice")
        backup_after_commit()
        return jsonify({"ok": True, "message": "Einkauf wurde aktualisiert."})

    @app.delete("/api/purchases/<int:purchase_id>")
    @login_required
    @manager_required
    def delete_purchase(purchase_id: int):
        """Delete one item from a purchase cart after client confirmation."""

        connection = get_db()
        cart_attachment_paths: list[str] = []
        try:
            purchase = connection.execute("SELECT * FROM purchases WHERE id = ?", (purchase_id,)).fetchone()
            if purchase is None:
                return jsonify({"ok": False, "error": "Einkauf wurde nicht gefunden."}), 404
            connection.execute("BEGIN IMMEDIATE")
            remaining_item_count = connection.execute(
                "SELECT COUNT(*) FROM purchases WHERE receipt_id = ? AND id <> ?",
                (purchase["receipt_id"], purchase_id),
            ).fetchone()[0]
            connection.execute("DELETE FROM purchases WHERE id = ?", (purchase_id,))
            if remaining_item_count == 0:
                attachment_rows = connection.execute(
                    "SELECT file_path FROM purchase_receipt_attachments WHERE receipt_id = ?",
                    (purchase["receipt_id"],),
                ).fetchall()
                cart_attachment_paths = [row["file_path"] for row in attachment_rows]
                connection.execute(
                    "DELETE FROM purchase_receipt_attachments WHERE receipt_id = ?", (purchase["receipt_id"],)
                )
            audit(
                connection,
                "delete",
                "purchase",
                purchase_id,
                {
                    "receipt_id": purchase["receipt_id"],
                    "variant_id": purchase["variant_id"],
                    "quantity": purchase["quantity"],
                    "unit_cost_cents": purchase["unit_cost_cents"],
                    "purchased_on": purchase["purchased_on"],
                    "had_invoice_file": bool(purchase["invoice_file_path"]),
                    "scope": "item",
                    "receipt_deleted": not remaining_item_count,
                },
            )
            connection.commit()
        except Exception:
            connection.rollback()
            current_app.logger.exception("Could not delete purchase")
            return jsonify({"ok": False, "error": "Der Einkauf konnte nicht gelöscht werden."}), 500

        try:
            delete_invoice_file(purchase["invoice_file_path"])
            for file_path in cart_attachment_paths:
                delete_invoice_file(file_path)
        except OSError:
            # Do not report the successfully deleted booking as a failure.  A
            # harmless orphan can be removed manually, and is safer than
            # rolling back accounting after the fact.
            current_app.logger.exception("Could not remove deleted purchase invoice")
        backup_after_commit()
        message = "Einkauf wurde gelöscht." if not remaining_item_count else "Artikel wurde aus dem Warenkorb entfernt."
        return jsonify({"ok": True, "receipt_deleted": not remaining_item_count, "message": message})

    @app.delete("/api/purchase-receipts/<receipt_id>")
    @login_required
    @manager_required
    def delete_purchase_receipt(receipt_id: str):
        """Delete an entire purchase cart, including its item/cart invoices."""

        connection = get_db()
        try:
            purchase_rows = connection.execute(
                "SELECT * FROM purchases WHERE receipt_id = ? ORDER BY id", (receipt_id,)
            ).fetchall()
            if not purchase_rows:
                return jsonify({"ok": False, "error": "Einkaufswarenkorb wurde nicht gefunden."}), 404
            attachment_rows = connection.execute(
                "SELECT file_path FROM purchase_receipt_attachments WHERE receipt_id = ?", (receipt_id,)
            ).fetchall()
            item_invoice_paths = [row["invoice_file_path"] for row in purchase_rows if row["invoice_file_path"]]
            cart_attachment_paths = [row["file_path"] for row in attachment_rows]
            connection.execute("BEGIN IMMEDIATE")
            connection.execute("DELETE FROM purchase_receipt_attachments WHERE receipt_id = ?", (receipt_id,))
            connection.execute("DELETE FROM purchases WHERE receipt_id = ?", (receipt_id,))
            audit(
                connection,
                "delete",
                "purchase_receipt",
                purchase_rows[0]["id"],
                {
                    "receipt_id": receipt_id,
                    "deleted_item_count": len(purchase_rows),
                    "cart_attachment_count": len(cart_attachment_paths),
                    "scope": "receipt",
                },
            )
            connection.commit()
        except Exception:
            connection.rollback()
            current_app.logger.exception("Could not delete purchase receipt")
            return jsonify({"ok": False, "error": "Der Einkaufswarenkorb konnte nicht gelöscht werden."}), 500

        for file_path in [*item_invoice_paths, *cart_attachment_paths]:
            try:
                delete_invoice_file(file_path)
            except OSError:
                current_app.logger.exception("Could not remove deleted purchase receipt invoice")
        backup_after_commit()
        return jsonify({"ok": True, "message": "Einkaufswarenkorb wurde gelöscht."})

    @app.get("/api/purchases/<int:purchase_id>/invoice")
    @login_required
    def purchase_invoice(purchase_id: int):
        """Serve an invoice only when it belongs to an existing booking."""

        purchase = get_db().execute(
            "SELECT invoice_file_path FROM purchases WHERE id = ?", (purchase_id,)
        ).fetchone()
        target = invoice_storage_path(purchase["invoice_file_path"] if purchase else None)
        if target is None or not target.is_file():
            abort(404)
        return send_file(target, as_attachment=False, download_name=target.name)

    @app.post("/api/purchase-receipts/<receipt_id>/attachments")
    @login_required
    @manager_required
    def add_purchase_receipt_attachments(receipt_id: str):
        """Attach one or more invoice files to an already saved purchase cart."""

        connection = get_db()
        stored_files: list[str] = []
        try:
            purchase = connection.execute(
                "SELECT id FROM purchases WHERE receipt_id = ? LIMIT 1", (receipt_id,)
            ).fetchone()
            if purchase is None:
                return jsonify({"ok": False, "error": "Einkaufswarenkorb wurde nicht gefunden."}), 404
            uploaded_invoices = [
                uploaded_file
                for uploaded_file in request.files.getlist("cart_invoice_files")
                if getattr(uploaded_file, "filename", "")
            ]
            if not uploaded_invoices:
                raise ValueError("Bitte mindestens eine Rechnung auswählen.")

            connection.execute("BEGIN IMMEDIATE")
            for uploaded_invoice in uploaded_invoices:
                file_path = save_invoice_file(uploaded_invoice, receipt_id)
                if not file_path:
                    continue
                stored_files.append(file_path)
                connection.execute(
                    """
                    INSERT INTO purchase_receipt_attachments (receipt_id, file_path, created_at, created_by)
                    VALUES (?, ?, ?, ?)
                    """,
                    (receipt_id, file_path, utc_now(), g.user["id"]),
                )
            audit(
                connection,
                "add_attachment",
                "purchase_receipt",
                purchase["id"],
                {"receipt_id": receipt_id, "attachment_count": len(stored_files)},
            )
            connection.commit()
        except ValueError as exc:
            connection.rollback()
            for file_path in stored_files:
                delete_invoice_file(file_path)
            return jsonify({"ok": False, "error": str(exc)}), 400
        except Exception:
            connection.rollback()
            for file_path in stored_files:
                delete_invoice_file(file_path)
            current_app.logger.exception("Could not add purchase receipt attachment")
            return jsonify({"ok": False, "error": "Die Rechnung konnte nicht angehängt werden."}), 500

        backup_after_commit()
        return jsonify({"ok": True, "attachment_count": len(stored_files), "message": "Rechnung angehängt."})

    @app.get("/api/purchase-receipts/<receipt_id>/attachments/<int:attachment_id>")
    @login_required
    def purchase_receipt_attachment(receipt_id: str, attachment_id: int):
        """Serve a cart invoice only when it belongs to that purchase receipt."""

        attachment = get_db().execute(
            "SELECT file_path FROM purchase_receipt_attachments WHERE id = ? AND receipt_id = ?",
            (attachment_id, receipt_id),
        ).fetchone()
        target = invoice_storage_path(attachment["file_path"] if attachment else None)
        if target is None or not target.is_file():
            abort(404)
        return send_file(target, as_attachment=False, download_name=target.name)

    @app.get("/historie")
    @login_required
    def history_page():
        connection = get_db()
        sale_rows = connection.execute("SELECT * FROM sales ORDER BY sold_on DESC, id DESC").fetchall()
        return render_template(
            "history.html", title="Historie", receipts=receipt_history_payload(connection, sale_rows)
        )

    @app.patch("/api/sales/<int:sale_id>/cancel")
    @login_required
    def cancel_sale(sale_id: int):
        """Cancel one basket item or all remaining items of its receipt."""

        payload = request.get_json(silent=True) or {}
        scope = str(payload.get("scope", "item")).strip().lower()
        if scope not in {"item", "receipt"}:
            return jsonify({"ok": False, "error": "Ungültiger Stornoumfang."}), 400
        connection = get_db()
        try:
            sale = connection.execute(
                "SELECT id, receipt_id, is_cancelled FROM sales WHERE id = ?", (sale_id,)
            ).fetchone()
            if sale is None:
                return jsonify({"ok": False, "error": "Verkauf wurde nicht gefunden."}), 404
            if scope == "item" and sale["is_cancelled"]:
                return jsonify({"ok": False, "error": "Dieser Verkauf ist bereits storniert."}), 400

            connection.execute("BEGIN IMMEDIATE")
            if scope == "receipt":
                # The header may use the first item of a receipt, even when
                # that individual item was already cancelled earlier.  Scope
                # the update by receipt ID so the remaining basket rows still
                # cancel correctly.
                result = connection.execute(
                    "UPDATE sales SET is_cancelled = 1 WHERE receipt_id = ? AND is_cancelled = 0",
                    (sale["receipt_id"],),
                )
                if result.rowcount == 0:
                    raise ValueError("Dieser Warenkorb ist bereits vollständig storniert.")
                cancelled_count = result.rowcount
                audit_entity_type = "sale_receipt"
            else:
                result = connection.execute(
                    "UPDATE sales SET is_cancelled = 1 WHERE id = ? AND is_cancelled = 0", (sale_id,)
                )
                if result.rowcount != 1:
                    raise ValueError("Dieser Verkauf ist bereits storniert.")
                cancelled_count = 1
                audit_entity_type = "sale"
            audit(
                connection,
                "cancel",
                audit_entity_type,
                sale_id,
                {
                    "receipt_id": sale["receipt_id"],
                    "scope": scope,
                    "cancelled_item_count": cancelled_count,
                    "is_cancelled": True,
                },
            )
            connection.commit()
            backup_after_commit()
            message = "Warenkorb wurde storniert." if scope == "receipt" else "Artikel wurde storniert."
            return jsonify(
                {
                    "ok": True,
                    "is_cancelled": True,
                    "cancelled_item_count": cancelled_count,
                    "message": message,
                }
            )
        except ValueError as exc:
            connection.rollback()
            return jsonify({"ok": False, "error": str(exc)}), 400
        except Exception:
            connection.rollback()
            current_app.logger.exception("Could not cancel sale")
            return jsonify({"ok": False, "error": "Verkauf konnte nicht storniert werden."}), 500

    @app.get("/vorgaenge")
    @login_required
    def operations_page():
        """Show shipment and payment work queues without hiding sale history."""

        connection = get_db()
        current_shipments = connection.execute(
            """
            SELECT * FROM sales
            WHERE is_cancelled = 0 AND delivery_status IN ('pending', 'shipped')
            ORDER BY sold_on DESC, id DESC
            """
        ).fetchall()
        delivered_goods = connection.execute(
            """
            SELECT * FROM sales
            WHERE is_cancelled = 0 AND delivery_status = 'received'
            ORDER BY sold_on DESC, id DESC
            """
        ).fetchall()
        unpaid_sales = connection.execute(
            "SELECT * FROM sales WHERE is_cancelled = 0 AND is_paid = 0 ORDER BY sold_on DESC, id DESC"
        ).fetchall()
        paid_follow_up_sales = connection.execute(
            """
            SELECT * FROM sales
            WHERE is_cancelled = 0 AND is_paid = 1 AND payment_follow_up = 1
            ORDER BY sold_on DESC, id DESC
            """
        ).fetchall()
        return render_template(
            "operations.html",
            title="Offene Vorgänge",
            current_shipments=sales_with_labels(connection, current_shipments),
            unpaid_sales=sales_with_labels(connection, unpaid_sales),
            delivered_goods=sales_with_labels(connection, delivered_goods),
            paid_follow_up_sales=sales_with_labels(connection, paid_follow_up_sales),
        )

    @app.patch("/api/sales/<int:sale_id>/delivery-status")
    @login_required
    def update_delivery_status(sale_id: int):
        """Advance or correct a later-delivery sale's shipping state."""

        payload = request.get_json(silent=True) or {}
        requested_status = str(payload.get("delivery_status", "")).strip()
        if requested_status not in DELIVERY_WORKFLOW_STATUSES:
            return jsonify({"ok": False, "error": "Ungültiger Versandstatus."}), 400

        connection = get_db()
        try:
            sale = connection.execute(
                "SELECT id, receipt_id, delivery_status, is_cancelled FROM sales WHERE id = ?", (sale_id,)
            ).fetchone()
            if sale is None:
                return jsonify({"ok": False, "error": "Verkauf wurde nicht gefunden."}), 404
            if sale["is_cancelled"]:
                raise ValueError("Ein stornierter Verkauf kann nicht weiter bearbeitet werden.")
            if sale["delivery_status"] == "not_applicable":
                raise ValueError("Dieser Verkauf wurde bereits direkt übergeben und hat keinen Versandvorgang.")

            is_received = int(requested_status == "received")
            connection.execute("BEGIN IMMEDIATE")
            connection.execute(
                "UPDATE sales SET delivery_status = ?, is_received = ? WHERE id = ?",
                (requested_status, is_received, sale_id),
            )
            audit(
                connection,
                "update_delivery_status",
                "sale",
                sale_id,
                {"receipt_id": sale["receipt_id"], "delivery_status": requested_status},
            )
            connection.commit()
            backup_after_commit()
            return jsonify(
                {
                    "ok": True,
                    "delivery_status": requested_status,
                    "is_received": bool(is_received),
                    "message": "Versandstatus gespeichert.",
                }
            )
        except ValueError as exc:
            connection.rollback()
            return jsonify({"ok": False, "error": str(exc)}), 400
        except Exception:
            connection.rollback()
            current_app.logger.exception("Could not update delivery status")
            return jsonify({"ok": False, "error": "Versandstatus konnte nicht gespeichert werden."}), 500

    @app.patch("/api/sales/<int:sale_id>/payment-status")
    @login_required
    def update_payment_status(sale_id: int):
        """Mark an outstanding sale as paid (or correct it back to outstanding)."""

        payload = request.get_json(silent=True) or {}
        requested_paid = payload.get("is_paid")
        if not isinstance(requested_paid, bool):
            return jsonify({"ok": False, "error": "Ungültiger Zahlungsstatus."}), 400

        connection = get_db()
        try:
            sale = connection.execute(
                "SELECT id, receipt_id, amount_due_cents, is_cancelled FROM sales WHERE id = ?", (sale_id,)
            ).fetchone()
            if sale is None:
                return jsonify({"ok": False, "error": "Verkauf wurde nicht gefunden."}), 404
            if sale["is_cancelled"]:
                raise ValueError("Ein stornierter Verkauf kann nicht weiter bearbeitet werden.")

            # The dropdown deliberately has no second amount input.  Marking a
            # sale as paid therefore records the exact due amount and no
            # donation.  This matches the simple two-state workflow requested
            # for an outstanding payment.
            amount_given = int(sale["amount_due_cents"]) if requested_paid else None
            donation = 0
            connection.execute("BEGIN IMMEDIATE")
            connection.execute(
                """
                UPDATE sales
                SET is_paid = ?, payment_follow_up = 1, amount_given_cents = ?, donation_cents = ?
                WHERE id = ?
                """,
                (int(requested_paid), amount_given, donation, sale_id),
            )
            audit(
                connection,
                "update_payment_status",
                "sale",
                sale_id,
                {"receipt_id": sale["receipt_id"], "is_paid": requested_paid},
            )
            connection.commit()
            backup_after_commit()
            return jsonify({"ok": True, "is_paid": requested_paid, "message": "Zahlungsstatus gespeichert."})
        except ValueError as exc:
            connection.rollback()
            return jsonify({"ok": False, "error": str(exc)}), 400
        except Exception:
            connection.rollback()
            current_app.logger.exception("Could not update payment status")
            return jsonify({"ok": False, "error": "Zahlungsstatus konnte nicht gespeichert werden."}), 500

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
    @manager_required
    def article_management_page():
        connection = get_db()
        article_rows = connection.execute(
            """
            SELECT id, name, is_offered FROM articles
            WHERE is_active = 1
            ORDER BY is_offered DESC, name COLLATE NOCASE
            """
        ).fetchall()
        requested_id = request.args.get("article", type=int)
        selected_id = requested_id or (article_rows[0]["id"] if article_rows else None)
        article = get_article_management_data(connection, selected_id) if selected_id else None
        return render_template(
            "articles.html", title="Artikelverwaltung", article_list=[dict(row) for row in article_rows], article=article
        )

    @app.post("/artikelverwaltung/neu")
    @login_required
    @manager_required
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
            # A new article starts with the common apparel defaults.  They are
            # ordinary editable values; the administrator can remove a size or
            # even replace both option columns for a non-apparel article.
            apply_option_configuration(
                connection,
                article_id,
                default_new_article_option_configuration(),
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
                default_new_article_option_configuration(),
            )
            sync_variants(connection, article_id)
            audit(connection, "create", "article", article_id, {"name": unique_name})
            connection.commit()
            backup_after_commit()
            return redirect(url_for("article_management_page", article=article_id))

    @app.post("/artikelverwaltung/<int:article_id>/speichern")
    @login_required
    @manager_required
    def save_article(article_id: int):
        connection = get_db()
        try:
            article = connection.execute("SELECT * FROM articles WHERE id = ?", (article_id,)).fetchone()
            if article is None:
                abort(404)
            name = request.form.get("name", "").strip()
            if not name:
                raise ValueError("Der Artikelname darf nicht leer sein.")
            sale_price = money_to_cents(request.form.get("default_sale_price"), field_name="Standard-Verkaufspreis")
            purchase_price = money_to_cents(request.form.get("default_purchase_price"), field_name="Standard-Einkaufspreis")
            is_offered = 0 if request.form.get("not_offered") else 1
            apply_minimum_stock_to_all = parse_optional_non_negative_int(
                request.form.get("apply_minimum_stock_to_all"),
                field_name="Mindestbestand für alle Varianten",
            )
            raw_options = request.form.get("options_json", "[]")
            option_groups = validate_option_configuration(json.loads(raw_options))

            connection.execute("BEGIN IMMEDIATE")
            connection.execute(
                """
                UPDATE articles
                SET name = ?, default_sale_price_cents = ?, default_purchase_price_cents = ?, is_offered = ?, updated_at = ?
                WHERE id = ?
                """,
                (name, sale_price, purchase_price, is_offered, utc_now(), article_id),
            )
            apply_option_configuration(connection, article_id, option_groups)
            sync_variants(connection, article_id)
            # The article-level control is intentionally an explicit one-shot
            # action.  It is useful for a new shirt design with many sizes,
            # but must not overwrite individual thresholds on every later
            # article edit.
            if apply_minimum_stock_to_all is not None:
                connection.execute(
                    "UPDATE variants SET minimum_stock = ?, updated_at = ? WHERE article_id = ? AND is_active = 1",
                    (apply_minimum_stock_to_all, utc_now(), article_id),
                )
            # Checkboxes are absent from regular forms when unchecked, so put
            # all current variants back into the sales assortment before
            # applying the explicitly withdrawn entries below.
            connection.execute(
                "UPDATE variants SET is_offered = 1, updated_at = ? WHERE article_id = ? AND is_active = 1",
                (utc_now(), article_id),
            )
            # Variant price overrides arrive as regular form fields, so prices
            # still survive if JavaScript is unavailable during a save.
            for key, value in request.form.items():
                match = re.fullmatch(r"(sale|purchase)_price_(\d+)", key)
                minimum_stock_match = re.fullmatch(r"minimum_stock_(\d+)", key)
                if minimum_stock_match:
                    minimum_stock = parse_optional_non_negative_int(value, field_name="Mindestbestand")
                    connection.execute(
                        "UPDATE variants SET minimum_stock = ?, updated_at = ? WHERE id = ? AND article_id = ?",
                        (minimum_stock, utc_now(), int(minimum_stock_match.group(1)), article_id),
                    )
                    continue
                if not match:
                    not_offered_match = re.fullmatch(r"not_offered_(\d+)", key)
                    if not_offered_match:
                        connection.execute(
                            "UPDATE variants SET is_offered = 0, updated_at = ? WHERE id = ? AND article_id = ?",
                            (utc_now(), int(not_offered_match.group(1)), article_id),
                        )
                    continue
                field, variant_id = match.groups()
                cents = money_to_cents(value, field_name="Variantenpreis")
                column = "sale_price_cents" if field == "sale" else "default_purchase_price_cents"
                previous_default = int(
                    article["default_sale_price_cents"]
                    if field == "sale"
                    else article["default_purchase_price_cents"]
                )
                current_default = sale_price if field == "sale" else purchase_price
                current_variant = connection.execute(
                    f"SELECT {column} FROM variants WHERE id = ? AND article_id = ?",
                    (int(variant_id), article_id),
                ).fetchone()
                # A freshly created article already has its default option
                # combinations.  When the administrator changes the article's
                # standard price in that first save, unchanged per-variant
                # inputs must follow it instead of preserving their initial
                # zero.  The same rule keeps only truly custom prices fixed on
                # later standard-price changes.
                if current_variant is not None and cents == previous_default and int(current_variant[column]) == previous_default:
                    cents = current_default
                connection.execute(
                    f"UPDATE variants SET {column} = ?, updated_at = ? WHERE id = ? AND article_id = ?",
                    (cents, utc_now(), int(variant_id), article_id),
                )
            audit(
                connection,
                "update",
                "article",
                article_id,
                {"name": name, "options_changed": True, "is_offered": bool(is_offered)},
            )
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

    @app.errorhandler(RequestEntityTooLarge)
    def invoice_request_too_large(_: RequestEntityTooLarge):
        message = "Die Rechnungsdatei darf höchstens 10 MB groß sein."
        if request.path.startswith("/api/"):
            return jsonify({"ok": False, "error": message}), 413
        return render_template("error.html", title="Datei zu groß", message=message), 413

    @app.errorhandler(403)
    def forbidden(_: Exception):
        return render_template("error.html", title="Kein Zugriff", message="Dafür fehlen die benötigten Rechte."), 403

    @app.errorhandler(404)
    def not_found(_: Exception):
        return render_template("error.html", title="Nicht gefunden", message="Diese Seite gibt es nicht."), 404

    return app


if __name__ == "__main__":  # pragma: no cover - Docker starts Gunicorn instead.
    create_app().run(host="0.0.0.0", port=8000, debug=False)
