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
import binascii
import csv
import hashlib
import io
import itertools
import json
import os
import re
import secrets
import shutil
import smtplib
import sqlite3
import ssl
import string
import tempfile
import time
import uuid
import warnings
from collections import defaultdict
from datetime import date, datetime, timedelta, timezone
from decimal import Decimal, InvalidOperation, ROUND_HALF_UP
from email.message import EmailMessage
from functools import wraps
from pathlib import Path
from threading import Lock
from typing import Any, Iterable
from urllib.error import HTTPError, URLError
from urllib.request import Request, urlopen
from zoneinfo import ZoneInfo, ZoneInfoNotFoundError
from zipfile import ZIP_DEFLATED, ZipFile

from flask import (
    Flask,
    Response,
    abort,
    current_app,
    flash,
    g,
    has_app_context,
    jsonify,
    redirect,
    render_template,
    request,
    send_file,
    send_from_directory,
    session,
    url_for,
)
from werkzeug.security import check_password_hash, generate_password_hash
from werkzeug.exceptions import RequestEntityTooLarge
from cryptography.fernet import Fernet, InvalidToken
from cryptography.hazmat.primitives.kdf.scrypt import Scrypt
import pyotp
import qrcode
from PIL import Image, ImageOps, UnidentifiedImageError

try:
    from sqlcipher3 import dbapi2 as sqlcipher
except ImportError:  # pragma: no cover - deployment configuration, not business rules.
    sqlcipher = None


USERS_SCHEMA_SQL = """
PRAGMA foreign_keys = ON;

CREATE TABLE IF NOT EXISTS users (
    id INTEGER PRIMARY KEY,
    username TEXT NOT NULL UNIQUE COLLATE NOCASE,
    password_hash TEXT NOT NULL,
    -- ``is_admin`` remains for safe upgrades from the first single-admin
    -- release.  New authorization decisions use the explicit role below.
    is_admin INTEGER NOT NULL DEFAULT 0,
    role TEXT NOT NULL DEFAULT 'seller' CHECK(role IN ('seller', 'member', 'manager', 'admin')),
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
    -- These preferences deliberately belong to the account database: every
    -- person can choose their own presentation without changing the shared
    -- catalogue or another user's sales view.
    ui_theme TEXT NOT NULL DEFAULT 'aurora',
    ui_language TEXT NOT NULL DEFAULT 'de',
    show_variant_photos INTEGER NOT NULL DEFAULT 0,
    created_at TEXT NOT NULL
);

-- Account-related audit entries deliberately live with the accounts.  This
-- keeps password/MFA/user-administration history available when an admin
-- resets or restores the operational ledger.
CREATE TABLE IF NOT EXISTS audit_log (
    id INTEGER PRIMARY KEY,
    created_at TEXT NOT NULL,
    user_id INTEGER,
    user_username TEXT,
    action TEXT NOT NULL,
    entity_type TEXT NOT NULL,
    entity_id INTEGER,
    details_json TEXT NOT NULL DEFAULT '{}'
);

-- Messages belong to the account side of the application. They survive an
-- operational data reset and retain the sender name even if that account is
-- removed later, so the administrator's inbox remains understandable.
CREATE TABLE IF NOT EXISTS admin_messages (
    id INTEGER PRIMARY KEY,
    sender_user_id INTEGER,
    sender_username TEXT NOT NULL,
    sender_email TEXT,
    message_type TEXT NOT NULL CHECK(message_type IN ('issue', 'question')),
    subject TEXT NOT NULL,
    body TEXT NOT NULL,
    created_at TEXT NOT NULL,
    is_resolved INTEGER NOT NULL DEFAULT 0,
    resolved_at TEXT,
    resolved_by_user_id INTEGER,
    resolved_by_username TEXT
);

CREATE INDEX IF NOT EXISTS idx_admin_messages_created ON admin_messages(created_at DESC, id DESC);

CREATE TABLE IF NOT EXISTS smtp_notification_settings (
    id INTEGER PRIMARY KEY CHECK(id = 1),
    enabled INTEGER NOT NULL DEFAULT 0,
    host TEXT NOT NULL DEFAULT '',
    port INTEGER NOT NULL DEFAULT 465,
    security TEXT NOT NULL DEFAULT 'ssl',
    username TEXT NOT NULL DEFAULT '',
    password_encrypted TEXT,
    sender_address TEXT NOT NULL DEFAULT '',
    recipient_address TEXT NOT NULL DEFAULT '',
    timeout_seconds REAL NOT NULL DEFAULT 8,
    updated_at TEXT,
    updated_by_user_id INTEGER,
    updated_by_username TEXT
);

"""

OPERATIONS_SCHEMA_SQL = """
PRAGMA foreign_keys = ON;

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

-- Product pictures stay on the filesystem.  SQLite only keeps the opaque
-- managed filename and the relation to the variant, so backups remain small
-- and no image bytes are embedded in database pages.
CREATE TABLE IF NOT EXISTS variant_photos (
    id INTEGER PRIMARY KEY,
    variant_id INTEGER NOT NULL REFERENCES variants(id),
    file_path TEXT NOT NULL UNIQUE,
    original_filename TEXT NOT NULL,
    position INTEGER NOT NULL DEFAULT 0,
    -- Product photos are global catalogue data.  They are included in the
    -- shop-display slideshow unless a manager explicitly opts one out.
    include_in_slideshow INTEGER NOT NULL DEFAULT 1,
    -- Price overlays are independently configurable for each product photo.
    -- Keeping this per image lets a detail shot stay uncluttered while the
    -- main product picture still advertises its price.
    show_price INTEGER NOT NULL DEFAULT 1,
    created_at TEXT NOT NULL,
    created_by INTEGER,
    created_by_username TEXT
);

-- Extra shop-display pictures (for example a price overview or band artwork)
-- deliberately have no variant relation.  They reuse the same protected file
-- store as product photos, while their metadata stays separate and globally
-- visible to every manager.
CREATE TABLE IF NOT EXISTS slideshow_extra_photos (
    id INTEGER PRIMARY KEY,
    file_path TEXT NOT NULL UNIQUE,
    original_filename TEXT NOT NULL,
    position INTEGER NOT NULL DEFAULT 0,
    include_in_slideshow INTEGER NOT NULL DEFAULT 1,
    show_price INTEGER NOT NULL DEFAULT 1,
    created_at TEXT NOT NULL,
    created_by INTEGER,
    created_by_username TEXT
);

-- One optional, global display preference.  No seed row is needed: a missing
-- row deliberately resolves to the safe default (show prices), and avoids a
-- synthetic row complicating the combined-database copy migration.
CREATE TABLE IF NOT EXISTS slideshow_settings (
    id INTEGER PRIMARY KEY CHECK(id = 1),
    collage_show_prices INTEGER NOT NULL DEFAULT 1
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
    -- This ID is intentionally not a foreign key: accounts live in
    -- users.sqlite3 and can be removed without touching booking history.
    created_by INTEGER,
    created_by_username TEXT
);

CREATE TABLE IF NOT EXISTS purchase_receipt_attachments (
    id INTEGER PRIMARY KEY,
    receipt_id TEXT NOT NULL,
    file_path TEXT NOT NULL UNIQUE,
    created_at TEXT NOT NULL,
    created_by INTEGER,
    created_by_username TEXT
);

-- Band finances deliberately have their own ledger.  Merch purchases and
-- sales remain a self-contained stock/accounting history, while gigs,
-- royalties and equipment can be tracked alongside them without changing a
-- historic merch balance.
CREATE TABLE IF NOT EXISTS band_transactions (
    id INTEGER PRIMARY KEY,
    transaction_type TEXT NOT NULL CHECK(transaction_type IN ('income', 'expense')),
    transaction_on TEXT NOT NULL,
    category TEXT NOT NULL,
    description TEXT NOT NULL,
    amount_cents INTEGER NOT NULL CHECK(amount_cents > 0),
    is_cancelled INTEGER NOT NULL DEFAULT 0,
    cancelled_at TEXT,
    cancelled_by_user_id INTEGER,
    cancelled_by_username TEXT,
    created_at TEXT NOT NULL,
    -- Account data lives in users.sqlite3, therefore actor IDs deliberately
    -- have no foreign key.  The immutable name snapshot keeps old entries
    -- readable after a user account is removed.
    created_by INTEGER,
    created_by_username TEXT
);

CREATE TABLE IF NOT EXISTS band_transaction_attachments (
    id INTEGER PRIMARY KEY,
    transaction_id INTEGER NOT NULL REFERENCES band_transactions(id) ON DELETE CASCADE,
    file_path TEXT NOT NULL UNIQUE,
    original_filename TEXT NOT NULL,
    created_at TEXT NOT NULL,
    created_by INTEGER,
    created_by_username TEXT
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
    created_by INTEGER,
    created_by_username TEXT
);

-- Veranstaltungen are shared operational metadata, rather than a per-user
-- preference.  Sales intentionally retain their event_name snapshot so older
-- bookings, CSV exports and offline clients stay readable without a foreign
-- key that could couple historic accounting rows to catalogue edits.
CREATE TABLE IF NOT EXISTS sale_events (
    id INTEGER PRIMARY KEY,
    name TEXT NOT NULL UNIQUE COLLATE NOCASE,
    created_at TEXT NOT NULL,
    last_selected_at TEXT NOT NULL
);

-- A missing row deliberately means that no event has been selected yet.  The
-- one-row table makes the current event exact and global even when several
-- users have the sales page open at once.
CREATE TABLE IF NOT EXISTS sale_event_state (
    id INTEGER PRIMARY KEY CHECK(id = 1),
    event_id INTEGER NOT NULL REFERENCES sale_events(id) ON DELETE RESTRICT,
    updated_at TEXT NOT NULL
);

CREATE TABLE IF NOT EXISTS audit_log (
    id INTEGER PRIMARY KEY,
    created_at TEXT NOT NULL,
    -- IDs remain useful for forensic correlation, while the immutable name
    -- snapshot makes old booking history readable after a user was removed.
    user_id INTEGER,
    user_username TEXT,
    action TEXT NOT NULL,
    entity_type TEXT NOT NULL,
    entity_id INTEGER,
    details_json TEXT NOT NULL DEFAULT '{}'
);

-- Offline clients never create ledger rows directly. They submit a durable,
-- client-generated event ID after a connection is available again. Keeping
-- the accepted event and its exact response makes retries idempotent even if
-- a browser lost the response after the server had already committed.
CREATE TABLE IF NOT EXISTS sync_events (
    event_id TEXT PRIMARY KEY,
    event_type TEXT NOT NULL CHECK(event_type IN ('sale')),
    actor_user_id INTEGER NOT NULL,
    actor_username TEXT,
    device_id TEXT NOT NULL,
    payload_hash TEXT NOT NULL,
    client_created_at TEXT NOT NULL,
    response_json TEXT NOT NULL,
    created_at TEXT NOT NULL
);

CREATE INDEX IF NOT EXISTS idx_option_groups_article ON option_groups(article_id, position);
CREATE INDEX IF NOT EXISTS idx_option_values_group ON option_values(option_group_id, position);
CREATE INDEX IF NOT EXISTS idx_variants_article ON variants(article_id, is_active);
CREATE INDEX IF NOT EXISTS idx_variant_photos_variant_position ON variant_photos(variant_id, position, id);
CREATE INDEX IF NOT EXISTS idx_slideshow_extra_photos_position ON slideshow_extra_photos(position, id);
CREATE INDEX IF NOT EXISTS idx_purchases_variant ON purchases(variant_id, purchased_on);
CREATE INDEX IF NOT EXISTS idx_purchases_receipt_id ON purchases(receipt_id);
CREATE INDEX IF NOT EXISTS idx_purchase_receipt_attachments_receipt ON purchase_receipt_attachments(receipt_id);
CREATE INDEX IF NOT EXISTS idx_band_transactions_on ON band_transactions(transaction_on DESC, id DESC);
CREATE INDEX IF NOT EXISTS idx_band_transaction_attachments_transaction
    ON band_transaction_attachments(transaction_id, id);
CREATE INDEX IF NOT EXISTS idx_sales_variant ON sales(variant_id, sold_on);
CREATE INDEX IF NOT EXISTS idx_sales_sold_on ON sales(sold_on);
CREATE INDEX IF NOT EXISTS idx_sale_events_last_selected ON sale_events(last_selected_at DESC, id DESC);
CREATE INDEX IF NOT EXISTS idx_sync_events_actor_created ON sync_events(actor_user_id, created_at);
"""

# This schema is only used to make a deployed pre-split database current
# before its rows are copied into the two new files.  New installations never
# create a combined database again.
LEGACY_COMBINED_SCHEMA_SQL = USERS_SCHEMA_SQL + OPERATIONS_SCHEMA_SQL

OPERATION_TABLES = (
    "articles",
    "option_groups",
    "option_values",
    "variants",
    "variant_photos",
    "slideshow_extra_photos",
    "slideshow_settings",
    "purchases",
    "purchase_receipt_attachments",
    "band_transactions",
    "band_transaction_attachments",
    "sales",
    "sale_events",
    "sale_event_state",
    "audit_log",
    "sync_events",
)

PAYMENT_METHODS = ["Bar", "PayPal", "Überweisung", "Karte", "Sonstiges"]

TRANSACTION_CSV_HEADERS = {
    "purchases": ["Anzahl", "Artikel", "Optionen", "Einkaufspreis", "Gekauft von"],
    "sales": ["Anzahl", "Artikel", "Optionen", "Verkaufspreis", "Verkauft an"],
}
MAX_TRANSACTION_CSV_BYTES = 2 * 1024 * 1024
MAX_SALE_EVENT_NAME_LENGTH = 120
MAX_TRANSACTION_CSV_ROWS = 5_000
MAX_IMPORTED_VARIANTS_PER_ARTICLE = 10_000
MAX_BAND_TRANSACTION_CATEGORY_LENGTH = 80
MAX_BAND_TRANSACTION_DESCRIPTION_LENGTH = 1_000
BAND_TRANSACTION_TYPES = frozenset({"income", "expense"})
BAND_TRANSACTION_CATEGORY_PRESETS = (
    "Gage",
    "Tantiemen",
    "Fahrgeld",
    "Equipment",
    "Unterkunft",
    "Verpflegung",
    "Sonstiges",
)

# The POS mode intentionally leaves the sales workflow, history, open tasks
# and product display reachable.  Everything after the visual navigation
# divider is still protected on the server, so a copied URL cannot bypass the
# compact point-of-sale screen.
POS_MODE_RESTRICTED_PATH_PREFIXES = (
    "/artikelverwaltung",
    "/einkaeufe",
    "/band-finanzen",
    "/bilanzen",
    "/verwaltung",
    "/updates",
    "/export",
)
POS_MODE_RESTRICTED_API_PATHS = frozenset({"/api/purchases", "/api/update-status"})

# Roles are cumulative.  ``member`` preserves the former Seller workflow,
# while the new, deliberately restricted Seller role is for the sales stand.
# Keeping the hierarchy as a tiny mapping makes every server-side
# authorization decision explicit and easy to audit.
ROLE_LEVELS = {"seller": 1, "member": 2, "manager": 3, "admin": 4}
ROLE_LABELS = {"seller": "Seller", "member": "Member", "manager": "Manager", "admin": "Admin"}
MANAGED_USER_ROLES = ("seller", "member", "manager")
SETUP_CODE_ALPHABET = string.ascii_uppercase + string.digits
SYNC_EVENT_METADATA_FIELDS = frozenset(
    {"client_event_id", "client_device_id", "client_actor_id", "client_created_at"}
)


class SyncEventConflict(Exception):
    """A reused offline event ID describes a different transaction."""


class DatabaseEncryptionError(RuntimeError):
    """The on-disk encryption configuration is invalid or unavailable."""


class DatabaseLockedError(DatabaseEncryptionError):
    """A database connection was requested before the encrypted store was unlocked."""

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
ALLOWED_INVOICE_FILE_EXTENSIONS = frozenset({".pdf", ".png", ".jpg", ".jpeg"})
# Band booking attachments deliberately follow the established invoice policy
# so every stored attachment has the same small, well-understood attack
# surface and can use the shared encrypted store.
ALLOWED_BAND_TRANSACTION_ATTACHMENT_EXTENSIONS = ALLOWED_INVOICE_FILE_EXTENSIONS
MANAGED_ATTACHMENT_FILE_EXTENSIONS = ALLOWED_INVOICE_FILE_EXTENSIONS
MAX_INVOICE_FILE_BYTES = 10 * 1024 * 1024

# Variant pictures are deliberately normalised on upload.  This avoids huge
# phone originals slowing down a sales device and gives every stored picture a
# predictable JPEG format regardless of the source camera/app.
ALLOWED_VARIANT_PHOTO_FILE_EXTENSIONS = {".jpg", ".jpeg", ".png", ".webp"}
MAX_VARIANT_PHOTO_FILE_BYTES = 10 * 1024 * 1024
MAX_VARIANT_PHOTO_PIXELS = 30_000_000
MAX_VARIANT_PHOTO_DIMENSION = 1600
VARIANT_PHOTO_JPEG_QUALITY = 84

DEFAULT_UI_THEME = "aurora"
DEFAULT_UI_LANGUAGE = "de"
USER_THEMES: dict[str, dict[str, str]] = {
    "aurora": {
        "label": "Aurora",
        "description": "Violett, Pink und ein weicher Nachtverlauf.",
        "theme_color": "#16131d",
    },
    "ocean": {
        "label": "Lagune",
        "description": "Tiefes Blau mit leuchtendem Türkis.",
        "theme_color": "#0d1d27",
    },
    "sunset": {
        "label": "Sonnenuntergang",
        "description": "Koralle, Orange und warme Magenta-Akzente.",
        "theme_color": "#251019",
    },
    "forest": {
        "label": "Waldlicht",
        "description": "Dunkles Grün mit frischem Mint.",
        "theme_color": "#102019",
    },
    "midnight": {
        "label": "Mitternacht",
        "description": "Kühles Marineblau mit Indigo-Licht.",
        "theme_color": "#10172a",
    },
}
USER_LANGUAGES: dict[str, dict[str, str]] = {
    "de": {"label": "Deutsch", "locale": "de-DE"},
    "en": {"label": "English", "locale": "en-GB"},
}

# The application has grown from a German-first local tool.  These common
# strings cover the global chrome, profile and the newly added photo flow;
# keeping them in one place makes the remaining specialist screens easy to
# translate incrementally without changing stored business data.
UI_TRANSLATIONS: dict[str, dict[str, str]] = {
    "de": {
        "nav.label": "Hauptnavigation",
        "nav.sales": "Verkauf",
        "nav.history": "Historie",
        "nav.operations": "Offene Vorgänge",
        "nav.purchases": "Einkäufe",
        "nav.band_finances": "Bandfinanzen",
        "nav.balances": "Bilanzen",
        "nav.articles": "Artikelverwaltung",
        "nav.slideshow": "Diashow",
        "nav.administration": "Verwaltung",
        "profile.link_title": "Profil und Sicherheitseinstellungen öffnen",
        "message.button": "Nachricht",
        "message.button_title": "Issue oder Frage an den Admin senden",
        "message.title": "Nachricht an den Admin",
        "message.intro": "Sende ein Issue oder eine Frage direkt an den Administrator.",
        "message.type": "Art der Nachricht",
        "message.issue": "Issue / Problem",
        "message.question": "Frage",
        "message.subject": "Betreff",
        "message.body": "Nachricht",
        "message.send": "Nachricht senden",
        "message.cancel": "Abbrechen",
        "message.sent": "Deine Nachricht wurde an den Admin gesendet.",
        "message.invalid_type": "Bitte Issue oder Frage auswählen.",
        "message.subject_required": "Bitte einen Betreff mit höchstens 120 Zeichen eingeben.",
        "message.body_required": "Bitte eine Nachricht mit höchstens 4.000 Zeichen eingeben.",
        "message.save_failed": "Die Nachricht konnte nicht gespeichert werden.",
        "logout": "Abmelden",
        "profile.eyebrow": "Konto",
        "profile.title": "Mein Profil",
        "profile.intro": "Persönliche Zugangsdaten, Darstellung und zusätzlicher Kontoschutz.",
        "profile.mfa_active": "2FA aktiv",
        "profile.mfa_inactive": "2FA nicht aktiv",
        "profile.account": "Konto",
        "profile.username": "Benutzername",
        "profile.role": "Rolle",
        "profile.last_login": "Letzte Anmeldung",
        "profile.never": "Noch nie",
        "profile.mfa": "2FA",
        "profile.enabled": "Aktiviert",
        "profile.disabled": "Nicht aktiviert",
        "profile.mfa_title": "Zwei-Faktor-Authentifizierung",
        "profile.mfa_enabled_text": "Dein Konto ist mit einer Authenticator-App geschützt. Noch verfügbare Wiederherstellungscodes:",
        "profile.mfa_reconfigure": "Authenticator-App neu einrichten",
        "profile.mfa_new_recovery": "Neue Wiederherstellungscodes",
        "profile.mfa_disable": "2FA deaktivieren",
        "profile.mfa_intro": "Mit 2FA benötigst du zusätzlich zum Passwort einen zeitbasierten Code aus einer kostenlosen Authenticator-App.",
        "profile.mfa_setup": "2FA einrichten",
        "profile.mfa_admin_notice": "Für den Admin ist 2FA verpflichtend und kann hier nicht deaktiviert werden.",
        "profile.username_title": "Benutzername ändern",
        "profile.username_intro": "Der neue Name wird beim nächsten Anmelden verwendet und erscheint künftig automatisch bei „Verkauft von“.",
        "profile.new_username": "Neuer Benutzername",
        "profile.save_username": "Benutzername speichern",
        "profile.password_title": "Passwort ändern",
        "profile.password_intro": "Die aktuelle Sicherheitsbestätigung gilt nur kurz. Nach dem Ändern werden andere Sitzungen dieses Kontos abgemeldet.",
        "profile.current_password": "Aktuelles Passwort",
        "profile.new_password": "Neues Passwort",
        "profile.repeat_password": "Passwort wiederholen",
        "profile.save_password": "Passwort ändern",
        "profile.personalization": "Darstellung & Verkauf",
        "profile.personalization_intro": "Diese Einstellungen gelten nur für dein Benutzerkonto und können jederzeit geändert werden.",
        "profile.language": "Sprache",
        "profile.theme": "Farbthema",
        "profile.show_variant_photos": "Produktfotos im Verkauf unter den Variantenoptionen anzeigen",
        "profile.show_variant_photos_hint": "Fehlt ein Foto der gewählten Variante, wird das ähnlichste Foto desselben Artikels verwendet.",
        "profile.save_personalization": "Darstellung speichern",
        "profile.personalization_saved": "Deine Darstellung und Verkaufsansicht wurden gespeichert.",
        "theme.aurora.label": "Aurora",
        "theme.aurora.description": "Violett, Pink und ein weicher Nachtverlauf.",
        "theme.ocean.label": "Lagune",
        "theme.ocean.description": "Tiefes Blau mit leuchtendem Türkis.",
        "theme.sunset.label": "Sonnenuntergang",
        "theme.sunset.description": "Koralle, Orange und warme Magenta-Akzente.",
        "theme.forest.label": "Waldlicht",
        "theme.forest.description": "Dunkles Grün mit frischem Mint.",
        "theme.midnight.label": "Mitternacht",
        "theme.midnight.description": "Kühles Marineblau mit Indigo-Licht.",
        "photos.heading": "Produktfotos",
        "photos.caption": "Produktfoto dieser Variante",
        "photos.fallback": "Foto einer ähnlichen Variante: {label}",
        "photos.upload": "Fotos hinzufügen",
        "photos.uploading": "Fotos werden optimiert …",
        "photos.delete": "Foto löschen",
        "photos.delete_confirm": "Dieses Produktfoto wirklich löschen?",
        "photos.empty": "Noch keine Fotos",
        "photos.save_first": "Nach dem ersten Speichern verfügbar",
        "photos.upload_failed": "Die Fotos konnten nicht hochgeladen werden.",
        "photos.delete_failed": "Das Foto konnte nicht gelöscht werden.",
        "slideshow.eyebrow": "Werbeanzeige",
        "slideshow.title": "Produktpalette",
        "slideshow.intro": "Stelle die gemeinsamen Produktfotos zusammen und starte eine Vollbild-Diashow für den Verkaufsstand.",
        "slideshow.start": "Produktpalette zeigen",
        "slideshow.start_hint": "Die Diashow endet mit einem beliebigen Tastendruck oder Klick.",
        "slideshow.upload_title": "Weitere Produktfotos",
        "slideshow.upload_intro": "Ordne die Fotos einer Variante zu oder wähle Anderes für eigenständige Bilder. Alle Uploads werden für alle Benutzer gespeichert und als JPEG optimiert.",
        "slideshow.variant": "Variante",
        "slideshow.target": "Zuordnung",
        "slideshow.choose_variant": "Variante auswählen",
        "slideshow.other": "Anderes",
        "slideshow.other_hint": "Eigenständiges Dia ohne Artikel, Variante und Preis",
        "slideshow.upload": "Fotos hochladen",
        "slideshow.uploading": "Fotos werden hochgeladen und optimiert …",
        "slideshow.gallery_title": "Alle Bilder für die Diashow",
        "slideshow.gallery_intro": "Neue Fotos sind automatisch für die Produktpalette ausgewählt. Deaktiviere einzelne Fotos, um sie nur im Artikel zu behalten.",
        "slideshow.include": "In Produktpalette zeigen",
        "slideshow.selected_count": "{count} von {total} Fotos für die Diashow ausgewählt",
        "slideshow.empty": "Noch keine Bilder vorhanden. Wähle oben eine Variante oder Anderes und lade die ersten Bilder hoch.",
        "slideshow.no_selected": "Wähle mindestens ein Bild für die Diashow aus.",
        "slideshow.variant_required": "Wähle eine Variante oder Anderes für die Fotos aus.",
        "slideshow.default_variant": "Standardvariante",
        "slideshow.not_offered": "Nicht im Verkauf angeboten",
        "slideshow.exit_hint": "Beliebige Taste oder Klick beendet die Produktpalette",
        "slideshow.update_failed": "Die Auswahl für die Produktpalette konnte nicht gespeichert werden.",
        "slideshow.upload_failed": "Die Produktfotos konnten nicht hochgeladen werden.",
        "slideshow.delete_other": "Bild entfernen",
        "slideshow.delete_other_confirm": "Dieses eigenständige Dia wirklich entfernen?",
        "slideshow.delete_failed": "Das Dia konnte nicht entfernt werden.",
        "slideshow.change_rate": "Bildwechselrate",
        "slideshow.change_rate_value": "alle {seconds} s",
        "slideshow.animation_speed": "Animationsgeschwindigkeit",
        "slideshow.animation_speed_value": "{speed}×",
        "slideshow.show_price": "Preis im Dia zeigen",
        "slideshow.collage_price_title": "Abschluss-Collage",
        "slideshow.collage_show_prices": "Preise in der Collage zeigen",
        "slideshow.collage_update_failed": "Die Preis-Anzeige der Abschluss-Collage konnte nicht gespeichert werden.",
        "slideshow.collage_label": "Abschluss-Collage",
    },
    "en": {
        "nav.label": "Main navigation",
        "nav.sales": "Sales",
        "nav.history": "History",
        "nav.operations": "Open tasks",
        "nav.purchases": "Purchases",
        "nav.band_finances": "Band finances",
        "nav.balances": "Balances",
        "nav.articles": "Catalogue",
        "nav.slideshow": "Slideshow",
        "nav.administration": "Administration",
        "profile.link_title": "Open profile and security settings",
        "message.button": "Message",
        "message.button_title": "Send an issue or question to the administrator",
        "message.title": "Message the administrator",
        "message.intro": "Send an issue or question directly to the administrator.",
        "message.type": "Message type",
        "message.issue": "Issue / problem",
        "message.question": "Question",
        "message.subject": "Subject",
        "message.body": "Message",
        "message.send": "Send message",
        "message.cancel": "Cancel",
        "message.sent": "Your message was sent to the administrator.",
        "message.invalid_type": "Choose Issue or Question.",
        "message.subject_required": "Enter a subject of no more than 120 characters.",
        "message.body_required": "Enter a message of no more than 4,000 characters.",
        "message.save_failed": "The message could not be saved.",
        "logout": "Sign out",
        "profile.eyebrow": "Account",
        "profile.title": "My profile",
        "profile.intro": "Personal sign-in details, appearance and additional account protection.",
        "profile.mfa_active": "2FA active",
        "profile.mfa_inactive": "2FA inactive",
        "profile.account": "Account",
        "profile.username": "Username",
        "profile.role": "Role",
        "profile.last_login": "Last sign-in",
        "profile.never": "Never",
        "profile.mfa": "2FA",
        "profile.enabled": "Enabled",
        "profile.disabled": "Not enabled",
        "profile.mfa_title": "Two-factor authentication",
        "profile.mfa_enabled_text": "Your account is protected by an authenticator app. Remaining recovery codes:",
        "profile.mfa_reconfigure": "Set up authenticator app again",
        "profile.mfa_new_recovery": "New recovery codes",
        "profile.mfa_disable": "Disable 2FA",
        "profile.mfa_intro": "With 2FA, you need a time-based code from a free authenticator app in addition to your password.",
        "profile.mfa_setup": "Set up 2FA",
        "profile.mfa_admin_notice": "2FA is mandatory for the administrator and cannot be disabled here.",
        "profile.username_title": "Change username",
        "profile.username_intro": "The new name will be used at your next sign-in and will automatically appear under “Sold by”.",
        "profile.new_username": "New username",
        "profile.save_username": "Save username",
        "profile.password_title": "Change password",
        "profile.password_intro": "The current security confirmation is valid only briefly. Changing it signs out other sessions for this account.",
        "profile.current_password": "Current password",
        "profile.new_password": "New password",
        "profile.repeat_password": "Repeat password",
        "profile.save_password": "Save password",
        "profile.personalization": "Appearance & sales",
        "profile.personalization_intro": "These settings apply only to your account and can be changed at any time.",
        "profile.language": "Language",
        "profile.theme": "Colour theme",
        "profile.show_variant_photos": "Show product photos below variant choices in Sales",
        "profile.show_variant_photos_hint": "If the chosen variant has no photo, the closest matching photo from the same product is used.",
        "profile.save_personalization": "Save appearance",
        "profile.personalization_saved": "Your appearance and sales view have been saved.",
        "theme.aurora.label": "Aurora",
        "theme.aurora.description": "Violet, pink and a soft night gradient.",
        "theme.ocean.label": "Lagoon",
        "theme.ocean.description": "Deep blue with luminous turquoise.",
        "theme.sunset.label": "Sunset",
        "theme.sunset.description": "Coral, orange and warm magenta accents.",
        "theme.forest.label": "Forest glow",
        "theme.forest.description": "Deep green with fresh mint.",
        "theme.midnight.label": "Midnight",
        "theme.midnight.description": "Cool navy blue with indigo light.",
        "photos.heading": "Product photos",
        "photos.caption": "Photo for this variant",
        "photos.fallback": "Photo from a similar variant: {label}",
        "photos.upload": "Add photos",
        "photos.uploading": "Optimising photos …",
        "photos.delete": "Delete photo",
        "photos.delete_confirm": "Delete this product photo?",
        "photos.empty": "No photos yet",
        "photos.save_first": "Available after first save",
        "photos.upload_failed": "The photos could not be uploaded.",
        "photos.delete_failed": "The photo could not be deleted.",
        "slideshow.eyebrow": "Shop display",
        "slideshow.title": "Product display",
        "slideshow.intro": "Curate the shared product photos and launch a full-screen slideshow for the sales stand.",
        "slideshow.start": "Show product display",
        "slideshow.start_hint": "Any key press or click ends the slideshow.",
        "slideshow.upload_title": "More product photos",
        "slideshow.upload_intro": "Assign photos to a variant or choose Other for independent pictures. Uploads are saved for every user and optimised as JPEG files.",
        "slideshow.variant": "Variant",
        "slideshow.target": "Assignment",
        "slideshow.choose_variant": "Choose a variant",
        "slideshow.other": "Other",
        "slideshow.other_hint": "Standalone slide without article, variant or price",
        "slideshow.upload": "Upload photos",
        "slideshow.uploading": "Uploading and optimising photos …",
        "slideshow.gallery_title": "All slideshow pictures",
        "slideshow.gallery_intro": "New photos are included in the product display by default. Disable individual photos to keep them only with the product.",
        "slideshow.include": "Show in product display",
        "slideshow.selected_count": "{count} of {total} photos selected for the slideshow",
        "slideshow.empty": "There are no pictures yet. Choose a variant or Other above and upload the first images.",
        "slideshow.no_selected": "Choose at least one picture for the slideshow.",
        "slideshow.variant_required": "Choose a variant or Other for the photos first.",
        "slideshow.default_variant": "Standard variant",
        "slideshow.not_offered": "Not offered for sale",
        "slideshow.exit_hint": "Any key or click ends the product display",
        "slideshow.update_failed": "The product-display selection could not be saved.",
        "slideshow.upload_failed": "The product photos could not be uploaded.",
        "slideshow.delete_other": "Remove picture",
        "slideshow.delete_other_confirm": "Remove this standalone slide?",
        "slideshow.delete_failed": "The slide could not be removed.",
        "slideshow.change_rate": "Image change rate",
        "slideshow.change_rate_value": "every {seconds} s",
        "slideshow.animation_speed": "Animation speed",
        "slideshow.animation_speed_value": "{speed}×",
        "slideshow.show_price": "Show price on the slide",
        "slideshow.collage_price_title": "Closing collage",
        "slideshow.collage_show_prices": "Show prices in the collage",
        "slideshow.collage_update_failed": "The closing-collage price display could not be saved.",
        "slideshow.collage_label": "Closing collage",
    },
}

# SQLCipher encrypts the complete SQLite files (including WAL pages).  The
# generated 256-bit database key is never written to disk in plaintext.  It is
# wrapped once with the administrator's unlock passphrase and once with a
# separately generated recovery key.  SECRET_KEY deliberately remains outside
# this mechanism: it continues to sign browser sessions and encrypt TOTP
# secrets, but is not a database key.
DATABASE_ENCRYPTION_METADATA_VERSION = 1
DATABASE_ENCRYPTION_KEY_BYTES = 32
DATABASE_ENCRYPTION_SCRYPT_N = 2**15
DATABASE_ENCRYPTION_SCRYPT_R = 8
DATABASE_ENCRYPTION_SCRYPT_P = 1
DATABASE_ENCRYPTION_SALT_BYTES = 16
DATABASE_ENCRYPTION_RECOVERY_PREFIX = "PVM-RK1"
DATABASE_ENCRYPTION_RECOVERY_TOKEN_BYTES = 30
DATABASE_ENCRYPTION_PENDING_RECOVERY_TTL_SECONDS = 15 * 60

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


def environment_flag(name: str, default: bool = False) -> bool:
    """Read a deliberately small boolean setting from the process environment."""

    raw = os.environ.get(name)
    if raw is None:
        return default
    return raw.strip().lower() in {"1", "true", "yes", "on"}


def smtp_notification_status(config: Any) -> dict[str, Any]:
    """Return display-safe SMTP readiness without exposing credentials."""

    enabled = bool(config.get("EMAIL_NOTIFICATIONS_ENABLED", False))
    security = str(config.get("SMTP_SECURITY", "ssl") or "ssl").strip().lower()
    required = {
        "SMTP_HOST": str(config.get("SMTP_HOST", "") or "").strip(),
        "SMTP_USERNAME": str(config.get("SMTP_USERNAME", "") or "").strip(),
        "SMTP_PASSWORD": str(config.get("SMTP_PASSWORD", "") or "").strip(),
        "SMTP_FROM": str(config.get("SMTP_FROM", "") or "").strip(),
        "ADMIN_NOTIFICATION_EMAIL": str(config.get("ADMIN_NOTIFICATION_EMAIL", "") or "").strip(),
    }
    missing = [name for name, value in required.items() if not value]
    errors: list[str] = []
    if security not in {"ssl", "starttls"}:
        errors.append("SMTP_SECURITY muss ssl oder starttls sein")
    try:
        port = int(config.get("SMTP_PORT", 465))
        if not 1 <= port <= 65_535:
            raise ValueError
    except (TypeError, ValueError):
        port = 0
        errors.append("SMTP_PORT ist ungültig")
    try:
        timeout_seconds = float(config.get("SMTP_TIMEOUT_SECONDS", 8))
        if timeout_seconds <= 0:
            raise ValueError
    except (TypeError, ValueError):
        timeout_seconds = 0
        errors.append("SMTP_TIMEOUT_SECONDS ist ungültig")
    return {
        "enabled": enabled,
        "ready": enabled and not missing and not errors,
        "missing": missing,
        "errors": errors,
        "host": required["SMTP_HOST"],
        "port": port,
        "security": security,
        "recipient": required["ADMIN_NOTIFICATION_EMAIL"],
        "timeout_seconds": timeout_seconds,
        "source": str(config.get("_smtp_source", "environment")),
        "password_configured": bool(config.get("_smtp_password_configured", required["SMTP_PASSWORD"])),
        "password_decryption_failed": bool(config.get("_smtp_password_decryption_failed", False)),
    }


def smtp_fernet(app: Flask | None = None) -> Fernet:
    """Derive an SMTP-specific encryption key from this installation's secret."""

    configured_app = app or current_app._get_current_object()
    material = str(configured_app.config["SECRET_KEY"]).encode("utf-8")
    key = base64.urlsafe_b64encode(hashlib.sha256(b"protovibe-merch:smtp:" + material).digest())
    return Fernet(key)


def encrypt_smtp_password(password: str, app: Flask | None = None) -> str:
    return smtp_fernet(app).encrypt(str(password).encode("utf-8")).decode("ascii")


def decrypt_smtp_password(value: str | None, app: Flask | None = None) -> str | None:
    if not value:
        return None
    try:
        return smtp_fernet(app).decrypt(str(value).encode("ascii")).decode("utf-8")
    except (InvalidToken, UnicodeError, ValueError):
        return None


def smtp_notification_config(connection: sqlite3.Connection, app: Flask | None = None) -> dict[str, Any]:
    """Return environment defaults or the encrypted, admin-managed override."""

    configured_app = app or current_app._get_current_object()
    config = {
        "EMAIL_NOTIFICATIONS_ENABLED": bool(configured_app.config.get("EMAIL_NOTIFICATIONS_ENABLED", False)),
        "SMTP_HOST": str(configured_app.config.get("SMTP_HOST", "") or "").strip(),
        "SMTP_PORT": configured_app.config.get("SMTP_PORT", "465"),
        "SMTP_SECURITY": str(configured_app.config.get("SMTP_SECURITY", "ssl") or "ssl").strip().lower(),
        "SMTP_USERNAME": str(configured_app.config.get("SMTP_USERNAME", "") or "").strip(),
        "SMTP_PASSWORD": str(configured_app.config.get("SMTP_PASSWORD", "") or ""),
        "SMTP_FROM": str(configured_app.config.get("SMTP_FROM", "") or "").strip(),
        "ADMIN_NOTIFICATION_EMAIL": str(configured_app.config.get("ADMIN_NOTIFICATION_EMAIL", "") or "").strip(),
        "SMTP_TIMEOUT_SECONDS": configured_app.config.get("SMTP_TIMEOUT_SECONDS", "8"),
        "_smtp_source": "environment",
        "_smtp_password_configured": bool(configured_app.config.get("SMTP_PASSWORD")),
    }
    if not table_exists(connection, "smtp_notification_settings"):
        return config
    row = connection.execute("SELECT * FROM smtp_notification_settings WHERE id = 1").fetchone()
    if row is None:
        return config
    password = decrypt_smtp_password(row["password_encrypted"], configured_app)
    config.update(
        {
            "EMAIL_NOTIFICATIONS_ENABLED": bool(row["enabled"]),
            "SMTP_HOST": str(row["host"] or "").strip(),
            "SMTP_PORT": row["port"],
            "SMTP_SECURITY": str(row["security"] or "ssl").strip().lower(),
            "SMTP_USERNAME": str(row["username"] or "").strip(),
            "SMTP_PASSWORD": password or "",
            "SMTP_FROM": str(row["sender_address"] or "").strip(),
            "ADMIN_NOTIFICATION_EMAIL": str(row["recipient_address"] or "").strip(),
            "SMTP_TIMEOUT_SECONDS": row["timeout_seconds"],
            "_smtp_source": "stored",
            "_smtp_password_configured": bool(row["password_encrypted"]),
            "_smtp_password_decryption_failed": bool(row["password_encrypted"]) and password is None,
        }
    )
    return config


def smtp_notification_settings_public(config: dict[str, Any]) -> dict[str, Any]:
    """Return SMTP form defaults without ever putting its password in HTML."""

    status = smtp_notification_status(config)
    return {
        "enabled": bool(config.get("EMAIL_NOTIFICATIONS_ENABLED")),
        "host": str(config.get("SMTP_HOST", "") or "").strip(),
        "port": status["port"] or 465,
        "security": status["security"],
        "username": str(config.get("SMTP_USERNAME", "") or "").strip(),
        "sender_address": str(config.get("SMTP_FROM", "") or "").strip(),
        "recipient_address": str(config.get("ADMIN_NOTIFICATION_EMAIL", "") or "").strip(),
        "timeout_seconds": status["timeout_seconds"] or 8,
        "password_configured": status["password_configured"],
        "source": status["source"],
    }


def send_smtp_notification(config: Any, *, subject: str, body: str) -> None:
    """Send one plain-text admin notification through configured TLS SMTP."""

    status = smtp_notification_status(config)
    if not status["ready"]:
        details = [*status["missing"], *status["errors"]]
        raise ValueError("SMTP-Benachrichtigung ist nicht vollständig konfiguriert: " + ", ".join(details))
    username = str(config["SMTP_USERNAME"]).strip()
    password = str(config["SMTP_PASSWORD"])
    message = EmailMessage()
    message["From"] = str(config["SMTP_FROM"]).strip()
    message["To"] = str(config["ADMIN_NOTIFICATION_EMAIL"]).strip()
    message["Subject"] = " ".join(str(subject).splitlines()).strip()
    message.set_content(str(body))

    tls_context = ssl.create_default_context()
    if status["security"] == "ssl":
        with smtplib.SMTP_SSL(
            status["host"], status["port"], timeout=status["timeout_seconds"], context=tls_context
        ) as client:
            client.login(username, password)
            client.send_message(message)
        return
    with smtplib.SMTP(status["host"], status["port"], timeout=status["timeout_seconds"]) as client:
        client.ehlo()
        client.starttls(context=tls_context)
        client.ehlo()
        client.login(username, password)
        client.send_message(message)


def send_admin_message_email(config: Any, message: dict[str, Any]) -> None:
    """Format a persisted internal message as a concise email notification."""

    type_label = "Issue / Problem" if message["message_type"] == "issue" else "Frage"
    send_smtp_notification(
        config,
        subject=f"[Merch Manager] {type_label}: {message['subject']}",
        body=(
            "Eine neue Nachricht wurde im Admin-Postfach gespeichert.\n\n"
            f"Absender: {message['sender_username']}\n"
            f"E-Mail: {message.get('sender_email') or 'Nicht angegeben'}\n"
            f"Kategorie: {type_label}\n"
            f"Zeitpunkt: {message['created_at']}\n"
            f"Betreff: {message['subject']}\n\n"
            f"{message['body']}\n\n"
            "Die Nachricht bleibt zusätzlich dauerhaft im Admin-Tab des Merch Managers erhalten."
        ),
    )


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


def display_recorded_time(value: Any, timezone_name: str) -> str:
    """Format a stored UTC timestamp for the band's local history view.

    Older records can be malformed or lack an offset.  Treat those values as
    UTC rather than silently applying the server's timezone, and keep the
    page readable even if an optional IANA timezone is unavailable.
    """

    try:
        recorded_at = datetime.fromisoformat(str(value).replace("Z", "+00:00"))
    except (TypeError, ValueError):
        return "—"
    if recorded_at.tzinfo is None:
        recorded_at = recorded_at.replace(tzinfo=timezone.utc)
    try:
        return recorded_at.astimezone(ZoneInfo(timezone_name)).strftime("%H:%M")
    except ZoneInfoNotFoundError:
        return recorded_at.astimezone(timezone.utc).strftime("%H:%M UTC")


def today_iso() -> str:
    return date.today().isoformat()


def normalized_role(user: dict[str, Any] | sqlite3.Row | None) -> str:
    """Return a safe role for current and pre-role database rows."""

    if user is None:
        return "seller"
    role = str(user["role"] or "").strip().lower() if "role" in user.keys() else ""
    if role in ROLE_LEVELS:
        return role
    # A record without a role predates the restricted Seller account.  Preserve
    # its former Seller rights until the startup migration stores ``member``.
    return "admin" if bool(user["is_admin"]) else "member"


def has_role(user: dict[str, Any] | sqlite3.Row | None, required_role: str) -> bool:
    """Check a cumulative role without trusting a client-side navigation hint."""

    return ROLE_LEVELS.get(normalized_role(user), 0) >= ROLE_LEVELS[required_role]


def effective_mfa_enabled(user: dict[str, Any] | sqlite3.Row | None, app: Flask | None = None) -> bool:
    """Return whether MFA is active for this runtime, honoring local-dev mode."""

    if user is None or not bool(user["mfa_enabled"]):
        return False
    configured_app = app
    if configured_app is None and has_app_context():
        configured_app = current_app._get_current_object()
    return not bool(configured_app and configured_app.config.get("LOCAL_DEV_MODE"))


def valid_ui_theme(value: Any) -> str:
    """Validate a persisted theme key instead of trusting a submitted value."""

    theme = str(value or "").strip().lower()
    if theme not in USER_THEMES:
        raise ValueError("Das ausgewählte Farbthema ist nicht verfügbar.")
    return theme


def valid_ui_language(value: Any) -> str:
    """Validate a persisted UI-language key."""

    language = str(value or "").strip().lower()
    if language not in USER_LANGUAGES:
        raise ValueError("Die ausgewählte Sprache ist nicht verfügbar.")
    return language


def user_ui_theme(user: dict[str, Any] | sqlite3.Row | None) -> str:
    """Return a safe theme for current and pre-preference user rows."""

    if user is None or "ui_theme" not in user.keys():
        return DEFAULT_UI_THEME
    value = str(user["ui_theme"] or "").strip().lower()
    return value if value in USER_THEMES else DEFAULT_UI_THEME


def user_ui_language(user: dict[str, Any] | sqlite3.Row | None) -> str:
    """Return a safe language for current and pre-preference user rows."""

    if user is None or "ui_language" not in user.keys():
        return DEFAULT_UI_LANGUAGE
    value = str(user["ui_language"] or "").strip().lower()
    return value if value in USER_LANGUAGES else DEFAULT_UI_LANGUAGE


def user_shows_variant_photos(user: dict[str, Any] | sqlite3.Row | None) -> bool:
    """Return whether this account opted into product photos in Sales."""

    return bool(user and "show_variant_photos" in user.keys() and user["show_variant_photos"])


def user_capabilities(user: dict[str, Any] | None) -> dict[str, Any] | None:
    """Add only display conveniences; routes still enforce the same rights."""

    if user is None:
        return None
    role = normalized_role(user)
    user["role"] = role
    user["role_label"] = ROLE_LABELS[role]
    user["is_admin"] = role == "admin"
    user["can_access_member_workflows"] = has_role(user, "member")
    user["can_manage_purchases"] = has_role(user, "manager")
    user["can_manage_band_finances"] = has_role(user, "manager")
    user["can_manage_articles"] = has_role(user, "manager")
    user["can_manage_slideshow"] = has_role(user, "manager")
    user["mfa_enabled"] = int(effective_mfa_enabled(user))
    user["ui_theme"] = user_ui_theme(user)
    user["ui_language"] = user_ui_language(user)
    user["show_variant_photos"] = int(user_shows_variant_photos(user))
    return user


def valid_username(value: Any) -> str:
    """Accept readable local account names while avoiding whitespace ambiguity."""

    username = str(value or "").strip()
    if not re.fullmatch(r"[^\s]{3,48}", username):
        raise ValueError("Der Benutzername muss 3 bis 48 Zeichen lang sein und darf keine Leerzeichen enthalten.")
    return username


def valid_email_address(value: Any, *, field_name: str = "E-Mail-Adresse") -> str:
    """Perform a deliberately small validation for contact and SMTP addresses."""

    address = str(value or "").strip()
    if len(address) > 254 or not re.fullmatch(r"[^\s@]+@[^\s@]+\.[^\s@]+", address):
        raise ValueError(f"{field_name} ist nicht gültig.")
    return address


def offline_sync_event(payload: dict[str, Any], actor_user_id: int) -> dict[str, str | int] | None:
    """Validate and fingerprint an optional idempotency event from a PWA.

    A normal browser sale remains fully supported without these fields. Once a
    client sends any offline-event metadata, however, all four fields are
    required. The UUID is intentionally random rather than derived from the
    sale contents: a timestamp-based hash can collide or be guessed, whereas
    the payload hash below still detects a malicious/accidental ID reuse.
    """

    present = {field for field in SYNC_EVENT_METADATA_FIELDS if payload.get(field) not in (None, "")}
    if not present:
        return None
    if present != SYNC_EVENT_METADATA_FIELDS:
        raise ValueError("Die Offline-Buchung ist unvollständig. Bitte erneut synchronisieren.")
    try:
        event_id = str(uuid.UUID(str(payload["client_event_id"])))
        device_id = str(uuid.UUID(str(payload["client_device_id"])))
    except (ValueError, AttributeError) as exc:
        raise ValueError("Die Kennung dieser Offline-Buchung ist ungültig.") from exc
    try:
        claimed_actor_id = int(payload["client_actor_id"])
    except (TypeError, ValueError) as exc:
        raise ValueError("Die Offline-Buchung enthält keinen gültigen Benutzerbezug.") from exc
    if claimed_actor_id != int(actor_user_id):
        raise ValueError("Diese Offline-Buchung gehört zu einem anderen Benutzerkonto.")
    client_created_at = str(payload["client_created_at"]).strip()
    try:
        parsed_client_time = datetime.fromisoformat(client_created_at.replace("Z", "+00:00"))
    except ValueError as exc:
        raise ValueError("Der Zeitstempel dieser Offline-Buchung ist ungültig.") from exc
    if parsed_client_time.tzinfo is None:
        raise ValueError("Der Zeitstempel dieser Offline-Buchung benötigt eine Zeitzone.")
    transaction_payload = {
        key: value for key, value in payload.items() if key not in SYNC_EVENT_METADATA_FIELDS
    }
    try:
        canonical_payload = json.dumps(
            transaction_payload,
            ensure_ascii=False,
            sort_keys=True,
            separators=(",", ":"),
            allow_nan=False,
        )
    except (TypeError, ValueError) as exc:
        raise ValueError("Die Offline-Buchung enthält ungültige Daten.") from exc
    return {
        "event_id": event_id,
        "event_type": "sale",
        "actor_user_id": claimed_actor_id,
        "device_id": device_id,
        "payload_hash": hashlib.sha256(canonical_payload.encode("utf-8")).hexdigest(),
        "client_created_at": client_created_at,
    }


def duplicate_sync_event_response(
    connection: sqlite3.Connection, event: dict[str, str | int]
) -> dict[str, Any] | None:
    """Return an old response or reject an event-ID collision under a DB lock."""

    existing = connection.execute(
        "SELECT * FROM sync_events WHERE event_id = ?", (event["event_id"],)
    ).fetchone()
    if existing is None:
        return None
    expected = ("event_type", "actor_user_id", "device_id", "payload_hash")
    if any(str(existing[field]) != str(event[field]) for field in expected):
        raise SyncEventConflict("Diese Offline-Kennung wurde bereits für eine andere Buchung verwendet.")
    try:
        response = json.loads(existing["response_json"])
    except (TypeError, json.JSONDecodeError) as exc:
        raise RuntimeError("Die gespeicherte Synchronisationsantwort ist beschädigt.") from exc
    if not isinstance(response, dict):
        raise RuntimeError("Die gespeicherte Synchronisationsantwort ist ungültig.")
    response["duplicate"] = True
    response["message"] = "Bereits synchronisierte Offline-Buchung bestätigt."
    return response


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


def mfa_fernet_for_secret(secret_key: str) -> Fernet:
    """Derive a Fernet instance for one application secret."""

    material = str(secret_key).encode("utf-8")
    key = base64.urlsafe_b64encode(hashlib.sha256(b"protovibe-merch:mfa:" + material).digest())
    return Fernet(key)


def mfa_fernet(app: Flask | None = None) -> Fernet:
    """Derive the installation's stable TOTP-encryption key from SECRET_KEY."""

    configured_app = app or current_app._get_current_object()
    return mfa_fernet_for_secret(str(configured_app.config["SECRET_KEY"]))


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

    if has_app_context() and current_app.config.get("LOCAL_DEV_MODE"):
        return "local_dev"
    code = str(submitted_code or "").strip().upper().replace(" ", "")
    if not code or not effective_mfa_enabled(user):
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


def verify_admin_sensitive_action(
    connection: sqlite3.Connection, *, password: Any, mfa_code: Any, context: str
) -> sqlite3.Row:
    """Require the current admin password and MFA for destructive actions."""

    admin = connection.execute("SELECT * FROM users WHERE id = ?", (g.user["id"],)).fetchone()
    if admin is None or not check_password_hash(admin["password_hash"], str(password or "")):
        raise ValueError("Das Passwort ist nicht korrekt. Es wurden keine Daten verändert.")
    mfa_method = "local_dev" if current_app.config.get("LOCAL_DEV_MODE") else verify_mfa_code(connection, admin, mfa_code)
    if mfa_method is None:
        raise ValueError("Der Zwei-Faktor-Code ist nicht gültig. Es wurden keine Daten verändert.")
    if mfa_method == "recovery":
        audit(
            connection,
            "use_recovery_code",
            "user",
            admin["id"],
            {"context": context},
            user_id=admin["id"],
        )
    connection.commit()
    return admin


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


def cents_to_money(cents: int | None, *, language: str = DEFAULT_UI_LANGUAGE) -> str:
    """Format cents for the German UI without using the process locale."""

    cents = cents or 0
    sign = "-" if cents < 0 else ""
    cents = abs(cents)
    euros, remainder = divmod(cents, 100)
    if language == "en":
        return f"{sign}€{euros:,}.{remainder:02d}"
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


def band_transaction_values_from_form(form: Any) -> dict[str, Any]:
    """Validate one browser-submitted band income/expense booking."""

    transaction_type = str(form.get("transaction_type", "")).strip().lower()
    if transaction_type not in BAND_TRANSACTION_TYPES:
        raise ValueError("Bitte Einnahme oder Ausgabe auswählen.")

    raw_date = str(form.get("transaction_on", "")).strip()
    try:
        transaction_on = date.fromisoformat(raw_date).isoformat()
    except ValueError as exc:
        raise ValueError("Bitte ein gültiges Buchungsdatum eingeben.") from exc

    category = " ".join(str(form.get("category", "")).split())
    if not category:
        raise ValueError("Bitte eine Kategorie eingeben.")
    if len(category) > MAX_BAND_TRANSACTION_CATEGORY_LENGTH:
        raise ValueError("Die Kategorie darf höchstens 80 Zeichen lang sein.")

    description = str(form.get("description", "")).strip()
    if not description:
        raise ValueError("Bitte eine Beschreibung eingeben.")
    if len(description) > MAX_BAND_TRANSACTION_DESCRIPTION_LENGTH:
        raise ValueError("Die Beschreibung darf höchstens 1.000 Zeichen lang sein.")

    amount_cents = money_to_cents(form.get("amount"), field_name="Betrag")
    if amount_cents <= 0:
        raise ValueError("Der Betrag muss größer als null sein.")

    return {
        "transaction_type": transaction_type,
        "transaction_on": transaction_on,
        "category": category,
        "description": description,
        "amount_cents": amount_cents,
    }


def band_attachment_original_filename(uploaded_file: Any) -> str:
    """Return a display-safe, non-path filename for a saved band attachment."""

    supplied_name = str(getattr(uploaded_file, "filename", "") or "")
    filename = Path(supplied_name.replace("\x00", "").replace("\\", "/")).name.strip()[:255]
    if not filename:
        raise ValueError("Der Anhang braucht einen Dateinamen.")
    return filename


def managed_attachment_extension(
    uploaded_file: Any | None,
    *,
    allowed_extensions: frozenset[str],
    file_label: str,
) -> str | None:
    """Validate one small managed attachment and return its lowercase suffix.

    The filesystem never receives a user-provided filename. A concise
    signature check prevents renamed HTML/text files from becoming apparently
    harmless attachments.
    """

    if uploaded_file is None or not getattr(uploaded_file, "filename", ""):
        return None

    extension = Path(str(uploaded_file.filename)).suffix.lower()
    if extension not in allowed_extensions:
        raise ValueError(f"Bitte {file_label} als PDF, PNG oder JPG hochladen.")

    stream = uploaded_file.stream
    try:
        stream.seek(0, os.SEEK_END)
        size = stream.tell()
        stream.seek(0)
        if size <= 0:
            raise ValueError(f"Die {file_label.lower()} ist leer.")
        if size > int(current_app.config["MAX_INVOICE_FILE_BYTES"]):
            raise ValueError(f"Die {file_label.lower()} darf höchstens 10 MB groß sein.")

        signature = stream.read(16)
        is_valid = (
            (extension == ".pdf" and signature.startswith(b"%PDF-"))
            or (extension == ".png" and signature.startswith(b"\x89PNG\r\n\x1a\n"))
            or (extension in {".jpg", ".jpeg"} and signature.startswith(b"\xff\xd8\xff"))
        )
        if not is_valid:
            raise ValueError(f"Die Datei passt nicht zum gewählten {file_label.lower()}format.")
        return extension
    finally:
        stream.seek(0)


def invoice_file_extension(uploaded_file: Any | None) -> str | None:
    """Validate an optional invoice upload and return its lowercase suffix.

    Only PDFs, PNGs and JPEGs are accepted. In addition to the filename the
    small signature checks reject a renamed HTML/text file. The user-provided
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


def band_transaction_attachment_extension(uploaded_file: Any | None) -> str | None:
    """Validate an optional document kept with a band income/expense entry."""

    return managed_attachment_extension(
        uploaded_file,
        allowed_extensions=ALLOWED_BAND_TRANSACTION_ATTACHMENT_EXTENSIONS,
        file_label="Anhang",
    )


def invoice_storage_path(
    filename: str | None,
    *,
    directory: str | Path | None = None,
    app: Flask | None = None,
) -> Path | None:
    """Resolve a managed invoice path without allowing traversal or plaintext leakage.

    Database rows keep the original PDF/image filename.  In an encrypted
    installation the physical file has an additional ``.enc`` suffix, so rows
    remain independent of the storage format.
    """

    if not filename:
        return None
    safe_name = Path(str(filename)).name
    if safe_name != str(filename) or Path(safe_name).suffix.lower() not in MANAGED_ATTACHMENT_FILE_EXTENSIONS:
        return None
    configured_app = app
    if configured_app is None and has_app_context():
        configured_app = current_app._get_current_object()
    if directory is None:
        if configured_app is None:
            raise RuntimeError("Für den Rechnungs-Speicher fehlt die Anwendungskonfiguration.")
        directory = configured_app.config["INVOICE_UPLOAD_DIR"]
    physical_name = f"{safe_name}.enc" if database_encryption_enabled(configured_app) else safe_name
    return Path(directory) / physical_name


def invoice_file_fernet(app: Flask | None = None) -> Fernet:
    """Derive a separate attachment key from the in-memory database key."""

    database_key = active_database_key(app)
    attachment_key = base64.urlsafe_b64encode(
        hashlib.sha256(b"protovibe-merch:invoice-files:" + database_key).digest()
    )
    return Fernet(attachment_key)


def store_invoice_bytes(
    filename: str,
    content: bytes,
    *,
    directory: str | Path | None = None,
    app: Flask | None = None,
) -> Path:
    """Persist an invoice, encrypting it whenever the live database is encrypted."""

    target = invoice_storage_path(filename, directory=directory, app=app)
    if target is None:
        raise ValueError("Rechnungsdatei konnte nicht gespeichert werden.")
    configured_app = app
    if configured_app is None and has_app_context():
        configured_app = current_app._get_current_object()
    payload = invoice_file_fernet(configured_app).encrypt(content) if database_encryption_enabled(configured_app) else content
    target.parent.mkdir(parents=True, exist_ok=True)
    try:
        with target.open("xb") as output:
            output.write(payload)
            output.flush()
            os.fsync(output.fileno())
    except Exception:
        target.unlink(missing_ok=True)
        raise
    return target


def read_invoice_bytes(filename: str, *, app: Flask | None = None) -> bytes | None:
    """Return a validated managed invoice in memory for an authorised response."""

    target = invoice_storage_path(filename, app=app)
    if target is None or not target.is_file():
        return None
    try:
        content = target.read_bytes()
        configured_app = app
        if configured_app is None and has_app_context():
            configured_app = current_app._get_current_object()
        return invoice_file_fernet(configured_app).decrypt(content) if database_encryption_enabled(configured_app) else content
    except (OSError, InvalidToken) as exc:
        current_app.logger.error("Could not read encrypted invoice attachment: %s", filename)
        raise ValueError("Die gespeicherte Rechnung kann nicht entschlüsselt werden.") from exc


def invoice_mimetype(filename: str) -> str:
    return {
        ".pdf": "application/pdf",
        ".png": "image/png",
        ".jpg": "image/jpeg",
        ".jpeg": "image/jpeg",
    }.get(Path(filename).suffix.lower(), "application/octet-stream")


def save_invoice_file(uploaded_file: Any | None, receipt_id: str) -> str | None:
    """Store a validated invoice under an opaque, receipt-associated name."""

    extension = invoice_file_extension(uploaded_file)
    if extension is None:
        return None
    filename = f"{receipt_id}-{secrets.token_hex(12)}{extension}"
    uploaded_file.stream.seek(0)
    store_invoice_bytes(filename, uploaded_file.stream.read())
    return filename


def save_band_transaction_attachment(uploaded_file: Any | None, transaction_token: str) -> str | None:
    """Store a validated band attachment under an opaque transaction filename."""

    extension = band_transaction_attachment_extension(uploaded_file)
    if extension is None:
        return None
    filename = f"band-{transaction_token}-{secrets.token_hex(12)}{extension}"
    uploaded_file.stream.seek(0)
    store_invoice_bytes(filename, uploaded_file.stream.read())
    return filename


def delete_invoice_file(filename: str | None) -> None:
    """Remove a managed invoice attachment if it still exists."""

    target = invoice_storage_path(filename)
    if target is not None:
        target.unlink(missing_ok=True)


def variant_photo_storage_path(
    filename: str | None,
    *,
    directory: str | Path | None = None,
    app: Flask | None = None,
) -> Path | None:
    """Resolve one server-managed JPEG photo without allowing traversal."""

    if not filename:
        return None
    safe_name = Path(str(filename)).name
    if safe_name != str(filename) or Path(safe_name).suffix.lower() != ".jpg":
        return None
    configured_app = app
    if configured_app is None and has_app_context():
        configured_app = current_app._get_current_object()
    if directory is None:
        if configured_app is None:
            raise RuntimeError("Für den Produktfoto-Speicher fehlt die Anwendungskonfiguration.")
        directory = configured_app.config["VARIANT_PHOTO_UPLOAD_DIR"]
    physical_name = f"{safe_name}.enc" if database_encryption_enabled(configured_app) else safe_name
    return Path(directory) / physical_name


def variant_photo_file_fernet(app: Flask | None = None) -> Fernet:
    """Derive a separate key for filesystem product-photo attachments."""

    database_key = active_database_key(app)
    attachment_key = base64.urlsafe_b64encode(
        hashlib.sha256(b"protovibe-merch:variant-photo-files:" + database_key).digest()
    )
    return Fernet(attachment_key)


def store_variant_photo_bytes(
    filename: str,
    content: bytes,
    *,
    directory: str | Path | None = None,
    app: Flask | None = None,
) -> Path:
    """Persist an optimised photo beside, never inside, the SQLite files."""

    target = variant_photo_storage_path(filename, directory=directory, app=app)
    if target is None:
        raise ValueError("Produktfoto konnte nicht gespeichert werden.")
    configured_app = app
    if configured_app is None and has_app_context():
        configured_app = current_app._get_current_object()
    payload = (
        variant_photo_file_fernet(configured_app).encrypt(content)
        if database_encryption_enabled(configured_app)
        else content
    )
    target.parent.mkdir(parents=True, exist_ok=True)
    try:
        with target.open("xb") as output:
            output.write(payload)
            output.flush()
            os.fsync(output.fileno())
    except Exception:
        target.unlink(missing_ok=True)
        raise
    return target


def read_variant_photo_bytes(filename: str, *, app: Flask | None = None) -> bytes | None:
    """Read a managed JPEG after the serving route has checked authorisation."""

    target = variant_photo_storage_path(filename, app=app)
    if target is None or not target.is_file():
        return None
    try:
        content = target.read_bytes()
        configured_app = app
        if configured_app is None and has_app_context():
            configured_app = current_app._get_current_object()
        return (
            variant_photo_file_fernet(configured_app).decrypt(content)
            if database_encryption_enabled(configured_app)
            else content
        )
    except (OSError, InvalidToken) as exc:
        current_app.logger.error("Could not read product photo attachment: %s", filename)
        raise ValueError("Das gespeicherte Produktfoto kann nicht entschlüsselt werden.") from exc


def delete_variant_photo_file(filename: str | None, *, app: Flask | None = None) -> None:
    """Remove a managed product-photo file if it still exists."""

    target = variant_photo_storage_path(filename, app=app)
    if target is not None:
        target.unlink(missing_ok=True)


def normalized_variant_photo_upload(uploaded_file: Any) -> tuple[str, bytes]:
    """Validate, resize and convert one submitted product photo to JPEG."""

    supplied_name = str(getattr(uploaded_file, "filename", "") or "")
    original_filename = Path(supplied_name).name.replace("\x00", "").strip()[:255]
    if not original_filename:
        raise ValueError("Bitte mindestens ein Produktfoto auswählen.")
    extension = Path(original_filename).suffix.lower()
    if extension not in ALLOWED_VARIANT_PHOTO_FILE_EXTENSIONS:
        raise ValueError("Bitte nur JPG-, PNG- oder WebP-Bilder als Produktfoto hochladen.")

    stream = uploaded_file.stream
    try:
        stream.seek(0)
    except (AttributeError, OSError):
        pass
    raw_bytes = stream.read(int(current_app.config["MAX_VARIANT_PHOTO_FILE_BYTES"]) + 1)
    if not raw_bytes:
        raise ValueError("Das Produktfoto ist leer.")
    if len(raw_bytes) > int(current_app.config["MAX_VARIANT_PHOTO_FILE_BYTES"]):
        raise ValueError("Ein Produktfoto darf höchstens 10 MB groß sein.")

    try:
        with warnings.catch_warnings():
            warnings.simplefilter("error", Image.DecompressionBombWarning)
            with Image.open(io.BytesIO(raw_bytes)) as verifier:
                verifier.verify()
            with Image.open(io.BytesIO(raw_bytes)) as source_image:
                if source_image.width * source_image.height > int(current_app.config["MAX_VARIANT_PHOTO_PIXELS"]):
                    raise ValueError("Das Produktfoto hat zu viele Bildpunkte.")
                prepared_image = ImageOps.exif_transpose(source_image).convert("RGB")
                maximum = int(current_app.config["MAX_VARIANT_PHOTO_DIMENSION"])
                prepared_image.thumbnail((maximum, maximum), Image.Resampling.LANCZOS)
                output = io.BytesIO()
                quality = max(50, min(95, int(current_app.config["VARIANT_PHOTO_JPEG_QUALITY"])))
                prepared_image.save(output, format="JPEG", quality=quality, optimize=True, progressive=True)
    except (
        UnidentifiedImageError,
        OSError,
        ValueError,
        Image.DecompressionBombError,
        Image.DecompressionBombWarning,
    ) as exc:
        if isinstance(exc, ValueError) and str(exc) == "Das Produktfoto hat zu viele Bildpunkte.":
            raise
        raise ValueError("Die Datei ist kein unterstütztes, lesbares Bild.") from exc
    return original_filename, output.getvalue()


def variant_photos_by_variant(
    connection: sqlite3.Connection, variant_ids: Iterable[int]
) -> dict[int, list[dict[str, Any]]]:
    """Return safe public metadata for the requested variants' photos."""

    identifiers = sorted({int(variant_id) for variant_id in variant_ids})
    if not identifiers:
        return {}
    placeholders = ",".join("?" for _ in identifiers)
    rows = connection.execute(
        f"""
        SELECT id, variant_id, original_filename, position, include_in_slideshow, show_price, created_at
        FROM variant_photos
        WHERE variant_id IN ({placeholders})
        ORDER BY variant_id, position, id
        """,
        identifiers,
    ).fetchall()
    photos: dict[int, list[dict[str, Any]]] = defaultdict(list)
    for row in rows:
        photo = dict(row)
        photo["include_in_slideshow"] = bool(photo["include_in_slideshow"])
        photo["show_price"] = bool(photo["show_price"])
        photo["url"] = f"/api/variantenfotos/{photo['id']}"
        photos[int(photo["variant_id"])].append(photo)
    return dict(photos)


def slideshow_extra_photo_metadata(
    connection: sqlite3.Connection, photo_ids: Iterable[int] | None = None
) -> list[dict[str, Any]]:
    """Return public metadata for independently uploaded slideshow pictures."""

    parameters: list[Any] = []
    where_clause = ""
    if photo_ids is not None:
        identifiers = sorted({int(photo_id) for photo_id in photo_ids})
        if not identifiers:
            return []
        where_clause = f"WHERE id IN ({','.join('?' for _ in identifiers)})"
        parameters.extend(identifiers)
    rows = connection.execute(
        f"""
        SELECT id, original_filename, position, include_in_slideshow, show_price, created_at
        FROM slideshow_extra_photos
        {where_clause}
        ORDER BY position, id
        """,
        parameters,
    ).fetchall()
    photos: list[dict[str, Any]] = []
    for row in rows:
        photo = dict(row)
        photo["kind"] = "other"
        photo["key"] = f"other:{photo['id']}"
        photo["is_product_photo"] = False
        photo["include_in_slideshow"] = bool(photo["include_in_slideshow"])
        photo["show_price"] = bool(photo["show_price"])
        photo["url"] = f"/api/diashow/fotos/{photo['id']}"
        photos.append(photo)
    return photos


def slideshow_settings_payload(connection: sqlite3.Connection) -> dict[str, bool]:
    """Return global slideshow preferences, retaining safe defaults for old data."""

    row = connection.execute(
        "SELECT collage_show_prices FROM slideshow_settings WHERE id = 1"
    ).fetchone()
    return {"collage_show_prices": True if row is None else bool(row["collage_show_prices"])}


def slideshow_photo_setting_from_payload(payload: Any) -> tuple[str, bool]:
    """Validate the one mutable, boolean slideshow setting in a PATCH body."""

    if not isinstance(payload, dict):
        raise ValueError("Die Dia-Einstellung muss als Ja oder Nein übergeben werden.")
    requested = [(field, payload[field]) for field in ("include_in_slideshow", "show_price") if field in payload]
    if len(requested) != 1 or not isinstance(requested[0][1], bool):
        raise ValueError("Die Dia-Einstellung muss als Ja oder Nein übergeben werden.")
    return requested[0][0], requested[0][1]


def product_slideshow_catalogue(connection: sqlite3.Connection) -> dict[str, Any]:
    """Return global, active catalogue photos and valid upload targets.

    Product photos belong to variants, not to an individual account.  The
    slideshow therefore intentionally reads one shared set of active variants
    and carries the current price/labels alongside each safe photo URL.
    """

    variant_rows = connection.execute(
        """
        SELECT v.id
        FROM variants v
        JOIN articles a ON a.id = v.article_id
        WHERE v.is_active = 1 AND a.is_active = 1
        ORDER BY a.name COLLATE NOCASE, v.id
        """
    ).fetchall()
    variant_ids = [int(row["id"]) for row in variant_rows]
    labels = variant_label_map(connection, variant_ids)
    photos_by_variant = variant_photos_by_variant(connection, variant_ids)

    variants: list[dict[str, Any]] = []
    for variant_id in variant_ids:
        label = labels.get(variant_id)
        if label is None:
            continue
        variants.append(
            {
                "id": variant_id,
                "article_id": int(label["article_id"]),
                "article_name": str(label["article_name"]),
                "option_text": str(label["option_text"]),
                "label": str(label["label"]),
                "sale_price_cents": int(label["sale_price_cents"]),
                "is_offered": bool(label["is_offered"] and label["article_is_offered"]),
            }
        )
    variants.sort(key=lambda item: (item["article_name"].casefold(), item["option_text"].casefold(), item["id"]))

    photos: list[dict[str, Any]] = []
    for variant in variants:
        for photo in photos_by_variant.get(int(variant["id"]), []):
            photos.append(
                {
                    "id": int(photo["id"]),
                    "kind": "variant",
                    "key": f"variant:{photo['id']}",
                    "is_product_photo": True,
                    "variant_id": int(variant["id"]),
                    "article_id": int(variant["article_id"]),
                    "original_filename": str(photo["original_filename"]),
                    "position": int(photo["position"]),
                    "include_in_slideshow": bool(photo["include_in_slideshow"]),
                    "show_price": bool(photo["show_price"]),
                    "url": str(photo["url"]),
                    "article_name": variant["article_name"],
                    "option_text": variant["option_text"],
                    "label": variant["label"],
                    "sale_price_cents": variant["sale_price_cents"],
                    "is_offered": variant["is_offered"],
                }
            )
    photos.extend(slideshow_extra_photo_metadata(connection))
    return {
        "variants": variants,
        "photos": photos,
        "settings": slideshow_settings_payload(connection),
    }


def add_variant_photo_fallbacks(
    variants: list[dict[str, Any]],
    photos_by_variant: dict[int, list[dict[str, Any]]],
    *,
    fallback_candidates: list[dict[str, Any]] | None = None,
) -> None:
    """Attach own photos or the closest same-product variant's photos.

    Matching option values are a useful, deterministic definition of
    "closest": e.g. a black shirt in another size wins over an unrelated
    colour/size combination.  Offered variants then win ties, followed by the
    stable variant ID.
    """

    candidates = [
        variant
        for variant in (fallback_candidates if fallback_candidates is not None else variants)
        if photos_by_variant.get(int(variant["id"]))
    ]
    for variant in variants:
        variant_id = int(variant["id"])
        own_photos = photos_by_variant.get(variant_id, [])
        source = variant if own_photos else None
        if source is None and candidates:
            target_options = {int(option_id) for option_id in variant.get("option_value_ids", [])}

            def candidate_score(candidate: dict[str, Any]) -> tuple[int, int, int]:
                matching_options = len(target_options.intersection(candidate.get("option_value_ids", [])))
                return (matching_options, int(bool(candidate.get("is_offered"))), -int(candidate["id"]))

            source = max(candidates, key=candidate_score)
        if source is None:
            variant["display_photos"] = []
            continue
        source_id = int(source["id"])
        variant["display_photos"] = photos_by_variant.get(source_id, [])
        variant["photo_source_variant_id"] = source_id
        variant["photo_source_label"] = str(source["label"])
        variant["photo_is_fallback"] = source_id != variant_id


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


def database_encryption_enabled(app: Flask | None = None) -> bool:
    """Return whether this app instance must use SQLCipher for live data."""

    configured_app = app
    if configured_app is None and has_app_context():
        configured_app = current_app._get_current_object()
    return bool(configured_app and configured_app.config.get("DATABASE_ENCRYPTION_ENABLED", False))


def database_encryption_metadata_path(app: Flask) -> Path:
    """Return the small non-secret envelope beside the encrypted databases."""

    return Path(app.config["DATABASE_ENCRYPTION_METADATA"])


def _decode_encryption_base64(value: Any, *, field: str) -> bytes:
    try:
        decoded = base64.urlsafe_b64decode(str(value).encode("ascii"))
    except (ValueError, UnicodeEncodeError, binascii.Error) as exc:
        raise DatabaseEncryptionError(f"Die Verschlüsselungs-Konfiguration enthält ein ungültiges Feld: {field}.") from exc
    if not decoded:
        raise DatabaseEncryptionError(f"Die Verschlüsselungs-Konfiguration enthält ein leeres Feld: {field}.")
    return decoded


def _encryption_kdf_parameters(envelope: dict[str, Any]) -> tuple[bytes, int, int, int]:
    """Validate bounded Scrypt parameters before spending memory on an unlock attempt."""

    try:
        salt = _decode_encryption_base64(envelope["salt"], field="salt")
        n = int(envelope["n"])
        r = int(envelope["r"])
        p = int(envelope["p"])
    except (KeyError, TypeError, ValueError) as exc:
        raise DatabaseEncryptionError("Die Verschlüsselungs-Konfiguration ist unvollständig.") from exc
    if len(salt) != DATABASE_ENCRYPTION_SALT_BYTES:
        raise DatabaseEncryptionError("Die Verschlüsselungs-Konfiguration enthält einen ungültigen Salt.")
    # Protect the unlock route from a tampered metadata file requesting an
    # unreasonable amount of memory or CPU time.
    if n < 2**14 or n > 2**20 or n & (n - 1) or r < 1 or r > 32 or p < 1 or p > 8:
        raise DatabaseEncryptionError("Die Verschlüsselungs-Konfiguration enthält ungültige KDF-Parameter.")
    return salt, n, r, p


def _derive_wrapping_key(secret: str, envelope: dict[str, Any]) -> bytes:
    salt, n, r, p = _encryption_kdf_parameters(envelope)
    try:
        return base64.urlsafe_b64encode(
            Scrypt(salt=salt, length=32, n=n, r=r, p=p).derive(secret.encode("utf-8"))
        )
    except (TypeError, ValueError) as exc:
        raise DatabaseEncryptionError("Der Datenbankschlüssel konnte nicht abgeleitet werden.") from exc


def _wrap_database_key(database_key: bytes, secret: str) -> dict[str, Any]:
    salt = secrets.token_bytes(DATABASE_ENCRYPTION_SALT_BYTES)
    envelope: dict[str, Any] = {
        "kdf": "scrypt",
        "n": DATABASE_ENCRYPTION_SCRYPT_N,
        "r": DATABASE_ENCRYPTION_SCRYPT_R,
        "p": DATABASE_ENCRYPTION_SCRYPT_P,
        "salt": base64.urlsafe_b64encode(salt).decode("ascii"),
    }
    envelope["wrapped_key"] = Fernet(_derive_wrapping_key(secret, envelope)).encrypt(database_key).decode("ascii")
    return envelope


def _unwrap_database_key(envelope: dict[str, Any], secret: str) -> bytes:
    try:
        wrapped_key = str(envelope["wrapped_key"]).encode("ascii")
        database_key = Fernet(_derive_wrapping_key(secret, envelope)).decrypt(wrapped_key)
    except (KeyError, InvalidToken, UnicodeEncodeError, DatabaseEncryptionError) as exc:
        raise ValueError("Der Entsperrcode ist nicht korrekt.") from exc
    if len(database_key) != DATABASE_ENCRYPTION_KEY_BYTES:
        raise DatabaseEncryptionError("Die Verschlüsselungs-Konfiguration enthält keinen gültigen Datenbankschlüssel.")
    return database_key


def validate_database_passphrase(value: Any) -> str:
    """Accept a strong, deliberately separate local database passphrase."""

    passphrase = str(value or "")
    if passphrase != passphrase.strip():
        raise ValueError("Die Datenbank-Passphrase darf nicht mit Leerzeichen beginnen oder enden.")
    if len(passphrase) < 12:
        raise ValueError("Die Datenbank-Passphrase muss mindestens 12 Zeichen lang sein.")
    return passphrase


def _normalised_recovery_key(value: Any) -> str:
    compact = re.sub(r"[\s-]+", "", str(value or "").upper())
    prefix = re.sub(r"-", "", DATABASE_ENCRYPTION_RECOVERY_PREFIX)
    if not compact.startswith(prefix):
        raise ValueError("Der Wiederherstellungsschlüssel hat kein gültiges Format.")
    token = compact[len(prefix):]
    if not token:
        raise ValueError("Der Wiederherstellungsschlüssel hat kein gültiges Format.")
    try:
        decoded = base64.b32decode(token + "=" * (-len(token) % 8), casefold=True)
    except (ValueError, UnicodeEncodeError, binascii.Error) as exc:
        raise ValueError("Der Wiederherstellungsschlüssel hat kein gültiges Format.") from exc
    if len(decoded) != DATABASE_ENCRYPTION_RECOVERY_TOKEN_BYTES:
        raise ValueError("Der Wiederherstellungsschlüssel hat kein gültiges Format.")
    return prefix + token


def generate_database_recovery_key() -> str:
    """Create a printable high-entropy recovery key; never persist it in plaintext."""

    token = base64.b32encode(secrets.token_bytes(DATABASE_ENCRYPTION_RECOVERY_TOKEN_BYTES)).decode("ascii").rstrip("=")
    groups = "-".join(token[index:index + 5] for index in range(0, len(token), 5))
    return f"{DATABASE_ENCRYPTION_RECOVERY_PREFIX}-{groups}"


def new_database_encryption_metadata(passphrase: str, recovery_key: str, database_key: bytes) -> dict[str, Any]:
    """Create the only persistent key metadata; it contains no usable clear-text key."""

    return {
        "version": DATABASE_ENCRYPTION_METADATA_VERSION,
        "cipher": "sqlcipher-4",
        "created_at": utc_now(),
        "databases_ready": False,
        "passphrase": _wrap_database_key(database_key, passphrase),
        "recovery": _wrap_database_key(database_key, _normalised_recovery_key(recovery_key)),
    }


def load_database_encryption_metadata(app: Flask) -> dict[str, Any] | None:
    """Read and sanity-check the non-secret encryption envelope."""

    metadata_path = database_encryption_metadata_path(app)
    if not metadata_path.is_file():
        return None
    try:
        metadata = json.loads(metadata_path.read_text(encoding="utf-8"))
    except (OSError, ValueError, json.JSONDecodeError) as exc:
        raise DatabaseEncryptionError("Die Verschlüsselungs-Konfiguration kann nicht gelesen werden.") from exc
    if not isinstance(metadata, dict) or metadata.get("version") != DATABASE_ENCRYPTION_METADATA_VERSION:
        raise DatabaseEncryptionError("Die Verschlüsselungs-Konfiguration hat eine nicht unterstützte Version.")
    if metadata.get("cipher") != "sqlcipher-4":
        raise DatabaseEncryptionError("Die Verschlüsselungs-Konfiguration verwendet kein unterstütztes Datenbankformat.")
    for name in ("passphrase", "recovery"):
        envelope = metadata.get(name)
        if not isinstance(envelope, dict):
            raise DatabaseEncryptionError("Die Verschlüsselungs-Konfiguration ist unvollständig.")
        _encryption_kdf_parameters(envelope)
        if not isinstance(envelope.get("wrapped_key"), str):
            raise DatabaseEncryptionError("Die Verschlüsselungs-Konfiguration ist unvollständig.")
    if not isinstance(metadata.get("databases_ready"), bool):
        raise DatabaseEncryptionError("Die Verschlüsselungs-Konfiguration ist unvollständig.")
    return metadata


def write_database_encryption_metadata(app: Flask, metadata: dict[str, Any]) -> None:
    """Atomically persist the wrapped key envelope with owner-only permissions."""

    path = database_encryption_metadata_path(app)
    path.parent.mkdir(parents=True, exist_ok=True)
    temporary = path.with_name(f".{path.name}.{uuid.uuid4().hex}.tmp")
    encoded = json.dumps(metadata, ensure_ascii=False, sort_keys=True, separators=(",", ":")).encode("utf-8")
    try:
        descriptor = os.open(temporary, os.O_WRONLY | os.O_CREAT | os.O_EXCL, 0o600)
        with os.fdopen(descriptor, "wb") as output:
            output.write(encoded)
            output.flush()
            os.fsync(output.fileno())
        os.replace(temporary, path)
        try:
            os.chmod(path, 0o600)
        except OSError:
            # Windows and some NAS filesystems do not expose POSIX modes. The
            # atomic write still prevents a partially written configuration.
            pass
    finally:
        temporary.unlink(missing_ok=True)


def database_encryption_state(app: Flask) -> str:
    """Return setup, legacy, locked or unlocked without opening user data."""

    if not database_encryption_enabled(app):
        return "disabled"
    metadata = load_database_encryption_metadata(app)
    if metadata is None:
        data_paths = (Path(app.config["DATABASE"]), Path(app.config["USERS_DATABASE"]))
        return "legacy" if any(path.exists() for path in data_paths) else "setup"
    if not metadata["databases_ready"]:
        return "setup_pending"
    return "unlocked" if isinstance(app.extensions.get("database_encryption_key"), bytes) else "locked"


def active_database_key(app: Flask | None = None) -> bytes:
    """Return the process-memory key, never a key from configuration or disk."""

    configured_app = app
    if configured_app is None and has_app_context():
        configured_app = current_app._get_current_object()
    if configured_app is None or not database_encryption_enabled(configured_app):
        raise DatabaseLockedError("Für diese Verbindung ist keine verschlüsselte Datenbank konfiguriert.")
    database_key = configured_app.extensions.get("database_encryption_key")
    if not isinstance(database_key, bytes) or len(database_key) != DATABASE_ENCRYPTION_KEY_BYTES:
        raise DatabaseLockedError("Die Datenbank ist gesperrt.")
    return database_key


def _sqlcipher_dbapi():
    if sqlcipher is None:
        raise DatabaseEncryptionError(
            "SQLCipher ist nicht installiert. Installiere die Abhängigkeiten erneut, bevor die verschlüsselte "
            "Datenbank gestartet wird."
        )
    return sqlcipher


def plaintext_db_connect(path: str | Path) -> sqlite3.Connection:
    """Open an explicitly unencrypted temporary or legacy SQLite database."""

    connection = sqlite3.connect(path, detect_types=sqlite3.PARSE_DECLTYPES)
    connection.row_factory = sqlite3.Row
    connection.execute("PRAGMA foreign_keys = ON")
    connection.execute("PRAGMA journal_mode = WAL")
    connection.execute("PRAGMA busy_timeout = 5000")
    return connection


def db_connect(
    path: str | Path,
    *,
    app: Flask | None = None,
    encrypted: bool | None = None,
    database_key: bytes | None = None,
) -> sqlite3.Connection:
    """Open a live SQLCipher or explicit plain SQLite connection consistently."""

    configured_app = app
    if configured_app is None and has_app_context():
        configured_app = current_app._get_current_object()
    if encrypted is None:
        encrypted = database_key is not None or database_encryption_enabled(configured_app)
    if not encrypted:
        return plaintext_db_connect(path)

    key = database_key or active_database_key(configured_app)
    api = _sqlcipher_dbapi()
    connection = api.connect(path, detect_types=sqlite3.PARSE_DECLTYPES)
    # Pin the on-disk format deliberately. A future SQLCipher major release
    # must not silently choose different defaults for existing installations.
    connection.execute("PRAGMA cipher_compatibility = 4")
    # The key consists solely of lowercase hexadecimal characters generated by
    # this process, so interpolation cannot turn the PRAGMA into SQL input.
    connection.execute(f"PRAGMA key = \"x'{key.hex()}'\"")
    connection.execute("PRAGMA cipher_memory_security = ON")
    connection.row_factory = getattr(api, "Row", sqlite3.Row)
    # Force SQLCipher to validate the key before callers make schema changes.
    connection.execute("SELECT COUNT(*) FROM sqlite_master").fetchone()
    connection.execute("PRAGMA foreign_keys = ON")
    connection.execute("PRAGMA temp_store = MEMORY")
    connection.execute("PRAGMA journal_mode = WAL")
    connection.execute("PRAGMA busy_timeout = 5000")
    return connection


def copy_live_database_snapshot(app: Flask, source_path: Path, target_path: Path) -> None:
    """Copy an encrypted live database into another encrypted SQLCipher file."""

    target_path.parent.mkdir(parents=True, exist_ok=True)
    for file_path in (target_path, Path(f"{target_path}-wal"), Path(f"{target_path}-shm")):
        file_path.unlink(missing_ok=True)
    source = db_connect(source_path, app=app)
    target = db_connect(target_path, app=app)
    try:
        source.backup(target)
    finally:
        target.close()
        source.close()


def get_db() -> sqlite3.Connection:
    """Return the current request's operational-data connection.

    ``merch.sqlite3`` contains catalogue, ledger, attachments and the
    operational audit trail.  Authentication deliberately uses
    :func:`get_user_db` below so a data reset never affects accounts.
    """

    if "db" not in g:
        g.db = db_connect(current_app.config["DATABASE"])
    return g.db


def get_user_db() -> sqlite3.Connection:
    """Return the request-local account database connection."""

    if "users_db" not in g:
        g.users_db = db_connect(current_app.config["USERS_DATABASE"])
    return g.users_db


def close_db(_: BaseException | None = None) -> None:
    """Close both independent request-local SQLite connections."""

    for key in ("db", "users_db"):
        connection = g.pop(key, None)
        if connection is not None:
            connection.close()


def close_operational_db() -> None:
    """Close only ``merch.sqlite3`` before atomically replacing it."""

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
            created_by INTEGER,
            created_by_username TEXT
        );
        INSERT INTO sales_multi_item_receipts (
            id, receipt_id, variant_id, quantity, unit_price_cents,
            amount_due_cents, amount_given_cents, donation_cents,
            payment_method, is_paid, payment_follow_up, is_received,
            delivery_status, is_cancelled, customer_name, customer_address,
            event_name, sold_by, comment, sold_on, created_at, created_by,
            created_by_username
        )
        SELECT
            id, receipt_id, variant_id, quantity, unit_price_cents,
            amount_due_cents, amount_given_cents, donation_cents,
            payment_method, is_paid, payment_follow_up, is_received,
            delivery_status, is_cancelled, customer_name, customer_address,
            event_name, sold_by, comment, sold_on, created_at, created_by,
            created_by_username
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
            created_by INTEGER,
            created_by_username TEXT
        );
        INSERT INTO purchases_multi_item_receipts (
            id, receipt_id, variant_id, quantity, unit_cost_cents, purchased_on,
            supplier, invoice_reference, invoice_file_path, comment, created_at, created_by,
            created_by_username
        )
        SELECT
            id, receipt_id, variant_id, quantity, unit_cost_cents, purchased_on,
            supplier, invoice_reference, invoice_file_path, comment, created_at, created_by,
            created_by_username
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


def upgrade_band_transactions_schema(connection: sqlite3.Connection) -> None:
    """Add cancellation metadata without ever rewriting band-ledger rows."""

    columns = set(table_columns(connection, "band_transactions"))
    migrations = (
        ("is_cancelled", "INTEGER NOT NULL DEFAULT 0"),
        ("cancelled_at", "TEXT"),
        ("cancelled_by_user_id", "INTEGER"),
        ("cancelled_by_username", "TEXT"),
    )
    for column_name, column_definition in migrations:
        if column_name not in columns:
            connection.execute(f"ALTER TABLE band_transactions ADD COLUMN {column_name} {column_definition}")


def users_table_needs_member_role_migration(connection: sqlite3.Connection) -> bool:
    """Return whether an older SQLite CHECK constraint still excludes Member."""

    row = connection.execute(
        "SELECT sql FROM sqlite_master WHERE type = 'table' AND name = 'users'"
    ).fetchone()
    if row is None:
        return False
    definition = str(row["sql"] if isinstance(row, sqlite3.Row) else row[0]).lower()
    return "'member'" not in definition and '"member"' not in definition


def rebuild_users_for_member_role(connection: sqlite3.Connection) -> None:
    """Replace the legacy role constraint and preserve former Seller rights.

    SQLite cannot add a value to an existing CHECK constraint.  Renaming and
    copying the small account table is atomic with the surrounding startup
    transaction, preserves every account field, and gives old ``seller`` rows
    their new ``member`` identity exactly once.
    """

    if not users_table_needs_member_role_migration(connection):
        return
    connection.execute("ALTER TABLE users RENAME TO users_before_member_role")
    connection.executescript(USERS_SCHEMA_SQL)
    connection.execute(
        """
        INSERT INTO users (
            id, username, password_hash, is_admin, role, is_active,
            must_set_password, setup_code_hash, setup_code_expires_at,
            mfa_secret_encrypted, mfa_pending_secret_encrypted,
            mfa_recovery_code_hashes_json, mfa_enabled, mfa_enrolled_at,
            session_version, last_login_at, ui_theme, ui_language,
            show_variant_photos, created_at
        )
        SELECT
            id, username, password_hash, is_admin,
            CASE
                WHEN role = 'admin' THEN 'admin'
                WHEN role = 'manager' THEN 'manager'
                WHEN role = 'member' THEN 'member'
                WHEN is_admin = 1 THEN 'admin'
                ELSE 'member'
            END,
            is_active, must_set_password, setup_code_hash,
            setup_code_expires_at, mfa_secret_encrypted,
            mfa_pending_secret_encrypted, mfa_recovery_code_hashes_json,
            mfa_enabled, mfa_enrolled_at, session_version, last_login_at,
            ui_theme, ui_language, show_variant_photos, created_at
        FROM users_before_member_role
        """
    )
    connection.execute("DROP TABLE users_before_member_role")


def upgrade_legacy_combined_database(app: Flask) -> None:
    """Bring a pre-split ``merch.sqlite3`` to the last combined schema.

    This is intentionally kept separate from normal startup: it runs only
    while the one-file database is being copied into ``merch.sqlite3`` and
    ``users.sqlite3``.  It guarantees that old releases can be migrated
    without losing columns that were introduced by earlier patches.
    """

    database_path = Path(app.config["DATABASE"])
    database_path.parent.mkdir(parents=True, exist_ok=True)
    connection = db_connect(database_path, app=app)
    try:
        connection.executescript(LEGACY_COMBINED_SCHEMA_SQL)
        upgrade_band_transactions_schema(connection)
        article_columns = {row["name"] for row in connection.execute("PRAGMA table_info(articles)").fetchall()}
        if "is_offered" not in article_columns:
            connection.execute("ALTER TABLE articles ADD COLUMN is_offered INTEGER NOT NULL DEFAULT 1")
        # The first released schema did not have this inventory convenience
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

        photo_columns = {row["name"] for row in connection.execute("PRAGMA table_info(variant_photos)").fetchall()}
        if "include_in_slideshow" not in photo_columns:
            connection.execute(
                "ALTER TABLE variant_photos ADD COLUMN include_in_slideshow INTEGER NOT NULL DEFAULT 1"
            )
        if "show_price" not in photo_columns:
            connection.execute(
                "ALTER TABLE variant_photos ADD COLUMN show_price INTEGER NOT NULL DEFAULT 1"
            )
        connection.execute(
            "CREATE INDEX IF NOT EXISTS idx_variant_photos_slideshow ON variant_photos(include_in_slideshow)"
        )
        extra_photo_columns = {
            row["name"] for row in connection.execute("PRAGMA table_info(slideshow_extra_photos)").fetchall()
        }
        if "include_in_slideshow" not in extra_photo_columns:
            connection.execute(
                "ALTER TABLE slideshow_extra_photos ADD COLUMN include_in_slideshow INTEGER NOT NULL DEFAULT 1"
            )
        if "show_price" not in extra_photo_columns:
            connection.execute(
                "ALTER TABLE slideshow_extra_photos ADD COLUMN show_price INTEGER NOT NULL DEFAULT 1"
            )

        # Invoice references used to be a single free-text field.  Preserve
        # those values and add a separate server-managed attachment path for
        # drag-and-drop PDF/image uploads.
        purchase_columns = {row["name"] for row in connection.execute("PRAGMA table_info(purchases)").fetchall()}
        if "invoice_file_path" not in purchase_columns:
            connection.execute("ALTER TABLE purchases ADD COLUMN invoice_file_path TEXT")
        if "created_by_username" not in purchase_columns:
            connection.execute("ALTER TABLE purchases ADD COLUMN created_by_username TEXT")
        attachment_columns = {
            row["name"] for row in connection.execute("PRAGMA table_info(purchase_receipt_attachments)").fetchall()
        }
        if "created_by_username" not in attachment_columns:
            connection.execute(
                "ALTER TABLE purchase_receipt_attachments ADD COLUMN created_by_username TEXT"
            )

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
        if "created_by_username" not in sales_columns:
            connection.execute("ALTER TABLE sales ADD COLUMN created_by_username TEXT")

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

        audit_columns = {row["name"] for row in connection.execute("PRAGMA table_info(audit_log)").fetchall()}
        if "user_username" not in audit_columns:
            connection.execute("ALTER TABLE audit_log ADD COLUMN user_username TEXT")
        sync_columns = {row["name"] for row in connection.execute("PRAGMA table_info(sync_events)").fetchall()}
        if "actor_username" not in sync_columns:
            connection.execute("ALTER TABLE sync_events ADD COLUMN actor_username TEXT")
        seed_sale_events_from_legacy_sales(connection)

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
            ("ui_theme", "TEXT NOT NULL DEFAULT 'aurora'"),
            ("ui_language", "TEXT NOT NULL DEFAULT 'de'"),
            ("show_variant_photos", "INTEGER NOT NULL DEFAULT 0"),
        )
        added_role_column = "role" not in user_columns
        for column_name, column_definition in user_column_migrations:
            if column_name not in user_columns:
                connection.execute(f"ALTER TABLE users ADD COLUMN {column_name} {column_definition}")
        user_audit_columns = {
            row["name"] for row in connection.execute("PRAGMA table_info(audit_log)").fetchall()
        }
        if "user_username" not in user_audit_columns:
            connection.execute("ALTER TABLE audit_log ADD COLUMN user_username TEXT")
        if added_role_column:
            connection.execute(
                "UPDATE users SET role = CASE WHEN is_admin = 1 THEN 'admin' ELSE 'seller' END"
            )
        connection.execute(
            "UPDATE users SET role = 'seller' WHERE role NOT IN ('seller', 'member', 'manager', 'admin') OR role IS NULL"
        )
        rebuild_users_for_member_role(connection)
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


def table_exists(connection: sqlite3.Connection, table_name: str) -> bool:
    """Return whether a table exists without treating user input as SQL."""

    return (
        connection.execute(
            "SELECT 1 FROM sqlite_master WHERE type = 'table' AND name = ?", (table_name,)
        ).fetchone()
        is not None
    )


def table_columns(connection: sqlite3.Connection, table_name: str) -> list[str]:
    """Return table columns in SQLite's stable declaration order."""

    return [row["name"] for row in connection.execute(f"PRAGMA table_info({table_name})").fetchall()]


def normalise_sale_event_name(value: Any, *, max_length: int | None = None) -> str | None:
    """Return a usable event label while keeping legacy free-text sales valid."""

    name = str(value or "").strip()
    if not name:
        return None
    if max_length is not None and len(name) > max_length:
        raise ValueError(f"Der Veranstaltungsname darf höchstens {max_length} Zeichen lang sein.")
    return name


def sale_event_catalogue(connection: sqlite3.Connection) -> dict[str, Any]:
    """Return the globally selected event plus the catalogue for the sales UI."""

    current = connection.execute(
        """
        SELECT e.id, e.name
        FROM sale_event_state state
        JOIN sale_events e ON e.id = state.event_id
        WHERE state.id = 1
        """
    ).fetchone()
    current_event_id = int(current["id"]) if current is not None else None
    events = connection.execute(
        """
        SELECT id, name
        FROM sale_events
        ORDER BY CASE WHEN id = ? THEN 0 ELSE 1 END,
                 last_selected_at DESC, id DESC
        """,
        (current_event_id,),
    ).fetchall()
    return {
        "current_event_id": current_event_id,
        "events": [{"id": int(row["id"]), "name": str(row["name"])} for row in events],
    }


def sale_event_by_id(connection: sqlite3.Connection, event_id: int) -> sqlite3.Row | None:
    return connection.execute("SELECT id, name FROM sale_events WHERE id = ?", (event_id,)).fetchone()


def select_sale_event(connection: sqlite3.Connection, event_id: int) -> dict[str, Any]:
    """Make one existing event the global default within the caller's transaction."""

    event = sale_event_by_id(connection, event_id)
    if event is None:
        raise LookupError("Die Veranstaltung wurde nicht gefunden.")
    now = utc_now()
    connection.execute("UPDATE sale_events SET last_selected_at = ? WHERE id = ?", (now, event_id))
    if connection.execute("SELECT 1 FROM sale_event_state WHERE id = 1").fetchone() is None:
        connection.execute(
            "INSERT INTO sale_event_state (id, event_id, updated_at) VALUES (1, ?, ?)",
            (event_id, now),
        )
    else:
        connection.execute(
            "UPDATE sale_event_state SET event_id = ?, updated_at = ? WHERE id = 1",
            (event_id, now),
        )
    return {"id": int(event["id"]), "name": str(event["name"])}


def create_sale_event(connection: sqlite3.Connection, name: str) -> dict[str, Any]:
    """Create a shared event or reuse its case-insensitive existing entry."""

    existing = connection.execute(
        "SELECT id FROM sale_events WHERE name = ? COLLATE NOCASE", (name,)
    ).fetchone()
    if existing is None:
        now = utc_now()
        cursor = connection.execute(
            "INSERT INTO sale_events (name, created_at, last_selected_at) VALUES (?, ?, ?)",
            (name, now, now),
        )
        event_id = int(cursor.lastrowid)
    else:
        event_id = int(existing["id"])
    return select_sale_event(connection, event_id)


def remember_legacy_sale_event(connection: sqlite3.Connection, name: str) -> str:
    """Add an old free-text event to the catalogue without changing the default."""

    existing = connection.execute(
        "SELECT name FROM sale_events WHERE name = ? COLLATE NOCASE", (name,)
    ).fetchone()
    if existing is not None:
        return str(existing["name"])
    now = utc_now()
    connection.execute(
        "INSERT INTO sale_events (name, created_at, last_selected_at) VALUES (?, ?, ?)",
        (name, now, now),
    )
    return name


def event_name_for_sale_payload(connection: sqlite3.Connection, payload: dict[str, Any]) -> str | None:
    """Resolve new event IDs while preserving old event_name-only sale payloads."""

    legacy_name = normalise_sale_event_name(payload.get("event_name"))
    raw_event_id = payload.get("event_id")
    if raw_event_id is not None and str(raw_event_id).strip():
        try:
            event_id = int(raw_event_id)
        except (TypeError, ValueError):
            event_id = None
        if event_id is not None:
            event = sale_event_by_id(connection, event_id)
            if event is not None:
                # The canonical stored label protects historic reporting if a
                # stale tab sends an old spelling together with a valid ID.
                return str(event["name"])
    if legacy_name is not None:
        # A queued pre-dropdown sale must never overwrite a more recent global
        # selection, but its historic label should become selectable later.
        return remember_legacy_sale_event(connection, legacy_name)
    if raw_event_id is not None and str(raw_event_id).strip():
        raise ValueError("Die ausgewählte Veranstaltung ist nicht mehr verfügbar.")
    return None


def seed_sale_events_from_legacy_sales(connection: sqlite3.Connection) -> None:
    """Seed the shared catalogue and first global default from historic sales.

    The helper is deliberately idempotent.  Once a real user has chosen an
    event, the singleton is never replaced merely because the app restarts.
    """

    if not table_exists(connection, "sales") or "event_name" not in set(table_columns(connection, "sales")):
        return
    rows = connection.execute(
        """
        SELECT TRIM(event_name) AS name,
               COALESCE(NULLIF(created_at, ''), sold_on || 'T00:00:00+00:00') AS occurred_at,
               id
        FROM sales
        WHERE NULLIF(TRIM(event_name), '') IS NOT NULL
        ORDER BY occurred_at DESC, id DESC
        """
    ).fetchall()
    for row in rows:
        name = normalise_sale_event_name(row["name"])
        if name is None:
            continue
        occurred_at = str(row["occurred_at"] or utc_now())
        # Newest historic spelling/time wins because the query is descending.
        connection.execute(
            "INSERT OR IGNORE INTO sale_events (name, created_at, last_selected_at) VALUES (?, ?, ?)",
            (name, occurred_at, occurred_at),
        )
    if connection.execute("SELECT 1 FROM sale_event_state WHERE id = 1").fetchone() is not None:
        return
    latest = connection.execute(
        "SELECT id FROM sale_events ORDER BY last_selected_at DESC, id DESC LIMIT 1"
    ).fetchone()
    if latest is not None:
        connection.execute(
            "INSERT INTO sale_event_state (id, event_id, updated_at) VALUES (1, ?, ?)",
            (int(latest["id"]), utc_now()),
        )


def upgrade_operations_schema(connection: sqlite3.Connection) -> None:
    """Create and safely upgrade the operational-only database schema."""

    connection.executescript(OPERATIONS_SCHEMA_SQL)
    upgrade_band_transactions_schema(connection)
    article_columns = set(table_columns(connection, "articles"))
    if "is_offered" not in article_columns:
        connection.execute("ALTER TABLE articles ADD COLUMN is_offered INTEGER NOT NULL DEFAULT 1")

    variant_columns = set(table_columns(connection, "variants"))
    if "no_reorder" not in variant_columns:
        connection.execute("ALTER TABLE variants ADD COLUMN no_reorder INTEGER NOT NULL DEFAULT 0")
    if "minimum_stock" not in variant_columns:
        connection.execute("ALTER TABLE variants ADD COLUMN minimum_stock INTEGER CHECK(minimum_stock >= 0)")
    if "is_offered" not in variant_columns:
        connection.execute("ALTER TABLE variants ADD COLUMN is_offered INTEGER NOT NULL DEFAULT 1")

    photo_columns = set(table_columns(connection, "variant_photos"))
    if "include_in_slideshow" not in photo_columns:
        connection.execute(
            "ALTER TABLE variant_photos ADD COLUMN include_in_slideshow INTEGER NOT NULL DEFAULT 1"
        )
    if "show_price" not in photo_columns:
        connection.execute(
            "ALTER TABLE variant_photos ADD COLUMN show_price INTEGER NOT NULL DEFAULT 1"
        )
    connection.execute(
        "CREATE INDEX IF NOT EXISTS idx_variant_photos_slideshow ON variant_photos(include_in_slideshow)"
    )
    extra_photo_columns = set(table_columns(connection, "slideshow_extra_photos"))
    if "include_in_slideshow" not in extra_photo_columns:
        connection.execute(
            "ALTER TABLE slideshow_extra_photos ADD COLUMN include_in_slideshow INTEGER NOT NULL DEFAULT 1"
        )
    if "show_price" not in extra_photo_columns:
        connection.execute(
            "ALTER TABLE slideshow_extra_photos ADD COLUMN show_price INTEGER NOT NULL DEFAULT 1"
        )

    purchase_columns = set(table_columns(connection, "purchases"))
    if "invoice_file_path" not in purchase_columns:
        connection.execute("ALTER TABLE purchases ADD COLUMN invoice_file_path TEXT")
    if "created_by_username" not in purchase_columns:
        connection.execute("ALTER TABLE purchases ADD COLUMN created_by_username TEXT")
    attachment_columns = set(table_columns(connection, "purchase_receipt_attachments"))
    if "created_by_username" not in attachment_columns:
        connection.execute(
            "ALTER TABLE purchase_receipt_attachments ADD COLUMN created_by_username TEXT"
        )
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

    sales_columns = set(table_columns(connection, "sales"))
    if "delivery_status" not in sales_columns:
        connection.execute("ALTER TABLE sales ADD COLUMN delivery_status TEXT NOT NULL DEFAULT 'not_applicable'")
        connection.execute("UPDATE sales SET delivery_status = 'pending' WHERE is_received = 0")
    if "payment_follow_up" not in sales_columns:
        connection.execute("ALTER TABLE sales ADD COLUMN payment_follow_up INTEGER NOT NULL DEFAULT 0")
        connection.execute("UPDATE sales SET payment_follow_up = 1 WHERE is_paid = 0")
    if "sold_by" not in sales_columns:
        connection.execute("ALTER TABLE sales ADD COLUMN sold_by TEXT")
    if "is_cancelled" not in sales_columns:
        connection.execute("ALTER TABLE sales ADD COLUMN is_cancelled INTEGER NOT NULL DEFAULT 0")
    if "created_by_username" not in sales_columns:
        connection.execute("ALTER TABLE sales ADD COLUMN created_by_username TEXT")
    if sales_receipt_id_is_unique(connection):
        rebuild_sales_for_multi_item_receipts(connection)
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

    audit_columns = set(table_columns(connection, "audit_log"))
    if "user_username" not in audit_columns:
        connection.execute("ALTER TABLE audit_log ADD COLUMN user_username TEXT")
    sync_columns = set(table_columns(connection, "sync_events"))
    if "actor_username" not in sync_columns:
        connection.execute("ALTER TABLE sync_events ADD COLUMN actor_username TEXT")
    connection.execute(
        "CREATE INDEX IF NOT EXISTS idx_sync_events_actor_created ON sync_events(actor_user_id, created_at)"
    )
    seed_sale_events_from_legacy_sales(connection)


def upgrade_admin_messages_schema(connection: sqlite3.Connection) -> None:
    """Add inbox fields before creating indexes that depend on them."""

    columns = set(table_columns(connection, "admin_messages"))
    migrations = (
        ("sender_email", "TEXT"),
        ("is_resolved", "INTEGER NOT NULL DEFAULT 0"),
        ("resolved_at", "TEXT"),
        ("resolved_by_user_id", "INTEGER"),
        ("resolved_by_username", "TEXT"),
    )
    for column_name, column_definition in migrations:
        if column_name not in columns:
            connection.execute(f"ALTER TABLE admin_messages ADD COLUMN {column_name} {column_definition}")
    connection.execute(
        "CREATE INDEX IF NOT EXISTS idx_admin_messages_resolution "
        "ON admin_messages(is_resolved, created_at DESC, id DESC)"
    )


def upgrade_users_schema(
    connection: sqlite3.Connection, app: Flask, *, bootstrap_administrator: bool = True
) -> None:
    """Create/upgrade the account database and enforce the one-admin rule."""

    connection.executescript(USERS_SCHEMA_SQL)
    upgrade_admin_messages_schema(connection)
    user_columns = set(table_columns(connection, "users"))
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
        ("ui_theme", "TEXT NOT NULL DEFAULT 'aurora'"),
        ("ui_language", "TEXT NOT NULL DEFAULT 'de'"),
        ("show_variant_photos", "INTEGER NOT NULL DEFAULT 0"),
    )
    added_role_column = "role" not in user_columns
    for column_name, column_definition in user_column_migrations:
        if column_name not in user_columns:
            connection.execute(f"ALTER TABLE users ADD COLUMN {column_name} {column_definition}")
    audit_columns = set(table_columns(connection, "audit_log"))
    if "user_username" not in audit_columns:
        connection.execute("ALTER TABLE audit_log ADD COLUMN user_username TEXT")
    if added_role_column:
        connection.execute("UPDATE users SET role = CASE WHEN is_admin = 1 THEN 'admin' ELSE 'seller' END")
    connection.execute(
        "UPDATE users SET role = 'seller' WHERE role NOT IN ('seller', 'member', 'manager', 'admin') OR role IS NULL"
    )
    rebuild_users_for_member_role(connection)
    admin_rows = connection.execute("SELECT id FROM users WHERE role = 'admin' ORDER BY id").fetchall()
    if len(admin_rows) > 1:
        connection.executemany(
            "UPDATE users SET role = 'manager' WHERE id = ?", [(row["id"],) for row in admin_rows[1:]]
        )
    elif not admin_rows:
        legacy_admin = connection.execute(
            "SELECT id FROM users WHERE is_admin = 1 ORDER BY id LIMIT 1"
        ).fetchone()
        if legacy_admin is not None:
            connection.execute("UPDATE users SET role = 'admin' WHERE id = ?", (legacy_admin["id"],))
    connection.execute("UPDATE users SET is_admin = CASE WHEN role = 'admin' THEN 1 ELSE 0 END")
    connection.execute("CREATE INDEX IF NOT EXISTS idx_users_role_active ON users(role, is_active)")

    user_count = int(connection.execute("SELECT COUNT(*) FROM users").fetchone()[0])
    if user_count == 0 and bootstrap_administrator:
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


def database_contains_table(database_path: Path, table_name: str) -> bool:
    """Inspect an existing SQLite file without creating a missing one."""

    if not database_path.is_file():
        return False
    connection = sqlite3.connect(database_path)
    try:
        return (
            connection.execute(
                "SELECT 1 FROM sqlite_master WHERE type = 'table' AND name = ?", (table_name,)
            ).fetchone()
            is not None
        )
    finally:
        connection.close()


def create_user_split_archive(app: Flask) -> Path:
    """Archive the original combined database before changing any rows."""

    archive_dir = Path(app.config["MIGRATION_ARCHIVE_DIR"])
    archive_dir.mkdir(parents=True, exist_ok=True)
    timestamp = datetime.now().strftime("%Y-%m-%d_%H-%M-%S")
    archive_path = archive_dir / f"merch-before-user-split-{timestamp}.zip"
    suffix = 1
    while archive_path.exists():
        suffix += 1
        archive_path = archive_dir / f"merch-before-user-split-{timestamp}-{suffix}.zip"

    snapshot_file = tempfile.NamedTemporaryFile(prefix="merch-user-split-", suffix=".sqlite3", delete=False)
    snapshot_path = Path(snapshot_file.name)
    snapshot_file.close()
    source = sqlite3.connect(app.config["DATABASE"])
    try:
        destination = sqlite3.connect(snapshot_path)
        try:
            source.backup(destination)
        finally:
            destination.close()
        with ZipFile(archive_path, "w", ZIP_DEFLATED) as archive:
            archive.write(snapshot_path, "data/merch.sqlite3")
            invoice_dir = Path(app.config["INVOICE_UPLOAD_DIR"])
            if invoice_dir.is_dir():
                for item in invoice_dir.rglob("*"):
                    if item.is_file():
                        archive.write(item, Path("data/invoices") / item.relative_to(invoice_dir))
            photo_dir = Path(app.config["VARIANT_PHOTO_UPLOAD_DIR"])
            if photo_dir.is_dir():
                for item in photo_dir.rglob("*"):
                    if item.is_file():
                        archive.write(item, Path("data/variant-photos") / item.relative_to(photo_dir))
    except Exception:
        archive_path.unlink(missing_ok=True)
        raise
    finally:
        source.close()
        snapshot_path.unlink(missing_ok=True)
    return archive_path


def copy_matching_table_rows(
    source: sqlite3.Connection, target: sqlite3.Connection, table_name: str
) -> None:
    """Copy one known table using the receiving schema's safe column subset.

    This is intentionally never called with a request-provided table name.  It
    lets a newer application keep the columns it introduced after an older
    source backup was made without executing source-controlled schema SQL.
    """

    if not table_exists(source, table_name) or not table_exists(target, table_name):
        return
    source_columns = set(table_columns(source, table_name))
    target_columns = [column for column in table_columns(target, table_name) if column in source_columns]
    if not target_columns:
        return
    rows = source.execute(f"SELECT {', '.join(target_columns)} FROM {table_name} ORDER BY rowid").fetchall()
    if not rows:
        return
    placeholders = ", ".join("?" for _ in target_columns)
    target.executemany(
        f"INSERT INTO {table_name} ({', '.join(target_columns)}) VALUES ({placeholders})",
        [tuple(row[column] for column in target_columns) for row in rows],
    )


def copy_users_to_separate_database(
    source: sqlite3.Connection,
    target: sqlite3.Connection,
    app: Flask,
    *,
    copy_audit_log: bool = False,
) -> None:
    """Copy account rows verbatim, retaining IDs used by historic bookings."""

    upgrade_users_schema(target, app, bootstrap_administrator=False)
    source_rows = source.execute("SELECT * FROM users ORDER BY id").fetchall()
    target_count = int(target.execute("SELECT COUNT(*) FROM users").fetchone()[0])
    if target_count:
        existing = {
            int(row["id"]): (str(row["username"]), str(row["password_hash"]))
            for row in target.execute("SELECT id, username, password_hash FROM users").fetchall()
        }
        expected = {
            int(row["id"]): (str(row["username"]), str(row["password_hash"])) for row in source_rows
        }
        if existing != expected:
            raise RuntimeError(
                "users.sqlite3 enthält andere Konten als die bisherige merch.sqlite3. "
                "Die automatische Migration wurde zur Sicherheit nicht fortgesetzt."
            )
    else:
        columns = [column for column in table_columns(target, "users") if column in set(source_rows[0].keys())] if source_rows else []
        if columns:
            placeholders = ", ".join("?" for _ in columns)
            target.executemany(
                f"INSERT INTO users ({', '.join(columns)}) VALUES ({placeholders})",
                [tuple(row[column] for column in columns) for row in source_rows],
            )
    if copy_audit_log and not target.execute("SELECT 1 FROM audit_log LIMIT 1").fetchone():
        copy_matching_table_rows(source, target, "audit_log")
    upgrade_users_schema(target, app, bootstrap_administrator=True)


def copy_operational_tables(
    source: sqlite3.Connection, target: sqlite3.Connection, usernames: dict[int, str]
) -> None:
    """Copy operational rows, adding immutable actor-name snapshots."""

    snapshot_columns = {
        "variant_photos": ("created_by", "created_by_username"),
        "slideshow_extra_photos": ("created_by", "created_by_username"),
        "purchases": ("created_by", "created_by_username"),
        "purchase_receipt_attachments": ("created_by", "created_by_username"),
        "band_transactions": ("created_by", "created_by_username"),
        "band_transaction_attachments": ("created_by", "created_by_username"),
        "sales": ("created_by", "created_by_username"),
        "audit_log": ("user_id", "user_username"),
        "sync_events": ("actor_user_id", "actor_username"),
    }
    for table_name in OPERATION_TABLES:
        source_columns = set(table_columns(source, table_name))
        target_columns = table_columns(target, table_name)
        rows = source.execute(f"SELECT * FROM {table_name} ORDER BY rowid").fetchall()
        if not rows:
            continue
        placeholders = ", ".join("?" for _ in target_columns)
        values: list[tuple[Any, ...]] = []
        actor_columns = snapshot_columns.get(table_name)
        for row in rows:
            row_values: list[Any] = []
            for column in target_columns:
                value = row[column] if column in source_columns else None
                if actor_columns and column == actor_columns[1] and not value:
                    actor_id = row[actor_columns[0]] if actor_columns[0] in source_columns else None
                    value = usernames.get(int(actor_id)) if actor_id is not None else None
                row_values.append(value)
            values.append(tuple(row_values))
        target.executemany(
            f"INSERT INTO {table_name} ({', '.join(target_columns)}) VALUES ({placeholders})", values
        )


def migrate_combined_database(app: Flask) -> None:
    """Atomically replace the old combined file after a verified copy.

    The backup is intentionally created first.  If anything below fails, the
    original ``merch.sqlite3`` remains authoritative and its exact pre-migrate
    state is available in ``migration-archives``.
    """

    database_path = Path(app.config["DATABASE"])
    users_path = Path(app.config["USERS_DATABASE"])
    archive_path = create_user_split_archive(app)
    upgrade_legacy_combined_database(app)
    source = db_connect(database_path, app=app)
    temporary_path = database_path.with_name(f".{database_path.name}.user-split-{uuid.uuid4().hex}.tmp")
    target: sqlite3.Connection | None = None
    users_connection: sqlite3.Connection | None = None
    try:
        users_path.parent.mkdir(parents=True, exist_ok=True)
        users_connection = db_connect(users_path, app=app)
        copy_users_to_separate_database(source, users_connection, app)
        users_connection.commit()
        usernames = {
            int(row["id"]): str(row["username"])
            for row in source.execute("SELECT id, username FROM users").fetchall()
        }

        target = db_connect(temporary_path, app=app)
        upgrade_operations_schema(target)
        copy_operational_tables(source, target, usernames)
        target.commit()
        target.close()
        target = None
        source.close()
        source = None  # type: ignore[assignment]

        # WAL sidecars still belong to the former combined file.  The copied
        # temporary database is self-contained, so removing them after the
        # atomic replacement prevents SQLite from replaying stale pages.
        os.replace(temporary_path, database_path)
        Path(f"{database_path}-wal").unlink(missing_ok=True)
        Path(f"{database_path}-shm").unlink(missing_ok=True)
        app.logger.info("Migrated combined merch database; original archive: %s", archive_path.name)
    except Exception:
        for temporary_file in (
            temporary_path,
            Path(f"{temporary_path}-wal"),
            Path(f"{temporary_path}-shm"),
        ):
            temporary_file.unlink(missing_ok=True)
        raise
    finally:
        if source is not None:
            source.close()
        if target is not None:
            target.close()
        if users_connection is not None:
            users_connection.close()
        for temporary_file in (Path(f"{temporary_path}-wal"), Path(f"{temporary_path}-shm")):
            temporary_file.unlink(missing_ok=True)


def initialise_operations_database(app: Flask) -> None:
    database_path = Path(app.config["DATABASE"])
    database_path.parent.mkdir(parents=True, exist_ok=True)
    connection = db_connect(database_path, app=app)
    try:
        upgrade_operations_schema(connection)
        connection.commit()
    finally:
        connection.close()


def initialise_users_database(app: Flask) -> None:
    users_path = Path(app.config["USERS_DATABASE"])
    users_path.parent.mkdir(parents=True, exist_ok=True)
    connection = db_connect(users_path, app=app)
    try:
        upgrade_users_schema(connection, app)
        connection.commit()
    finally:
        connection.close()


def initialise_database(app: Flask) -> None:
    """Initialise separate operational and account files, migrating safely."""

    database_path = Path(app.config["DATABASE"])
    users_path = Path(app.config["USERS_DATABASE"])
    if database_path.resolve() == users_path.resolve():
        raise RuntimeError("DATABASE und USERS_DATABASE müssen unterschiedliche Dateien sein.")
    # An encrypted installation is always split from its first setup. Looking
    # at it with the standard sqlite3 module would both fail and be wrong.
    if not database_encryption_enabled(app) and database_contains_table(database_path, "users"):
        migrate_combined_database(app)
    initialise_operations_database(app)
    initialise_users_database(app)


def configured_bootstrap_admin(app: Flask) -> tuple[str, str]:
    """Return the existing first-start credentials without treating them as a DB key."""

    username = str(app.config.get("ADMIN_USERNAME", "")).strip()
    password = str(app.config.get("ADMIN_PASSWORD", ""))
    if not username or not password or password.startswith("replace-this"):
        raise DatabaseEncryptionError(
            "Setze vor der ersten Einrichtung ADMIN_USERNAME und ADMIN_PASSWORD in der .env."
        )
    return username, password


def _remember_pending_database_recovery_key(app: Flask, recovery_key: str) -> str:
    """Keep a one-time display value only in process memory, never in a cookie or file."""

    token = secrets.token_urlsafe(32)
    pending = app.extensions.setdefault("pending_database_recovery_keys", {})
    now = time.time()
    for existing_token, entry in list(pending.items()):
        if float(entry.get("expires_at", 0)) < now:
            pending.pop(existing_token, None)
    pending[token] = {
        "recovery_key": recovery_key,
        "expires_at": now + DATABASE_ENCRYPTION_PENDING_RECOVERY_TTL_SECONDS,
    }
    return token


def pending_database_recovery_key(app: Flask, token: Any) -> str | None:
    pending = app.extensions.get("pending_database_recovery_keys", {})
    entry = pending.get(str(token or "")) if isinstance(pending, dict) else None
    if not isinstance(entry, dict) or float(entry.get("expires_at", 0)) < time.time():
        if isinstance(pending, dict):
            pending.pop(str(token or ""), None)
        return None
    recovery_key = entry.get("recovery_key")
    return str(recovery_key) if recovery_key else None


def discard_pending_database_recovery_key(app: Flask, token: Any) -> None:
    pending = app.extensions.get("pending_database_recovery_keys", {})
    if isinstance(pending, dict):
        pending.pop(str(token or ""), None)


def setup_encrypted_databases(
    app: Flask, *, bootstrap_password: Any, database_passphrase: Any, confirmation: Any
) -> tuple[str, str]:
    """Create a fresh encrypted installation and return its one-time recovery key.

    The caller must already be in the dedicated setup state.  Existing plain
    databases are deliberately not overwritten; they need the explicit legacy
    import workflow after a fresh encrypted store has been created.
    """

    if database_encryption_state(app) != "setup":
        raise DatabaseEncryptionError("Die Datenbankverschlüsselung wurde bereits eingerichtet.")
    _, expected_bootstrap_password = configured_bootstrap_admin(app)
    if not secrets.compare_digest(expected_bootstrap_password, str(bootstrap_password or "")):
        raise ValueError("Das Einrichtungs-Admin-Passwort ist nicht korrekt.")
    passphrase = validate_database_passphrase(database_passphrase)
    if passphrase != str(confirmation or ""):
        raise ValueError("Die beiden Datenbank-Passphrasen stimmen nicht überein.")

    database_key = secrets.token_bytes(DATABASE_ENCRYPTION_KEY_BYTES)
    recovery_key = generate_database_recovery_key()
    metadata = new_database_encryption_metadata(passphrase, recovery_key, database_key)
    # Persist the wrapped key first.  If a power loss interrupts initialization,
    # the passphrase can still resume it; the raw database key never needs to be
    # reconstructed or placed into .env.
    write_database_encryption_metadata(app, metadata)
    app.extensions["database_encryption_key"] = database_key
    try:
        initialise_database(app)
        metadata["databases_ready"] = True
        write_database_encryption_metadata(app, metadata)
    except Exception:
        app.extensions.pop("database_encryption_key", None)
        raise
    return _remember_pending_database_recovery_key(app, recovery_key), recovery_key


def unlock_encrypted_databases(
    app: Flask, *, database_passphrase: Any = None, recovery_key: Any = None
) -> str:
    """Unwrap the in-memory SQLCipher key using exactly one supported secret."""

    state = database_encryption_state(app)
    if state not in {"locked", "setup_pending"}:
        raise DatabaseEncryptionError("Die Datenbank kann in ihrem aktuellen Zustand nicht entsperrt werden.")
    provided_passphrase = str(database_passphrase or "")
    provided_recovery_key = str(recovery_key or "")
    if bool(provided_passphrase) == bool(provided_recovery_key):
        raise ValueError("Bitte gib entweder die Datenbank-Passphrase oder den Wiederherstellungsschlüssel ein.")
    metadata = load_database_encryption_metadata(app)
    if metadata is None:  # Defensive guard for the state check above.
        raise DatabaseEncryptionError("Die Verschlüsselungs-Konfiguration fehlt.")
    method = "passphrase"
    if provided_recovery_key:
        method = "recovery"
        secret = _normalised_recovery_key(provided_recovery_key)
    else:
        secret = provided_passphrase
    database_key = _unwrap_database_key(metadata[method], secret)

    app.extensions["database_encryption_key"] = database_key
    try:
        database_paths = (Path(app.config["DATABASE"]), Path(app.config["USERS_DATABASE"]))
        if not metadata["databases_ready"]:
            if any(path.exists() for path in database_paths):
                raise DatabaseEncryptionError(
                    "Die erste Verschlüsselungs-Einrichtung wurde unterbrochen. Bitte die Daten nicht manuell "
                    "ändern; stelle sie aus einer Sicherung wieder her oder richte einen leeren Datenordner ein."
                )
            initialise_database(app)
            metadata["databases_ready"] = True
            write_database_encryption_metadata(app, metadata)
        else:
            # Running the normal schema upgrade after every successful unlock
            # keeps a future release migration encrypted as well.
            initialise_database(app)
    except Exception:
        app.extensions.pop("database_encryption_key", None)
        raise
    return method


def change_database_passphrase(app: Flask, *, passphrase: Any, confirmation: Any) -> None:
    """Re-wrap the in-memory database key with a newly chosen unlock passphrase."""

    new_passphrase = validate_database_passphrase(passphrase)
    if new_passphrase != str(confirmation or ""):
        raise ValueError("Die beiden Datenbank-Passphrasen stimmen nicht überein.")
    metadata = load_database_encryption_metadata(app)
    if metadata is None:
        raise DatabaseEncryptionError("Die Verschlüsselungs-Konfiguration fehlt.")
    metadata["passphrase"] = _wrap_database_key(active_database_key(app), new_passphrase)
    metadata["updated_at"] = utc_now()
    write_database_encryption_metadata(app, metadata)


def regenerate_database_recovery_key(app: Flask) -> tuple[str, str]:
    """Invalidate the former recovery key and return a new one-time display value."""

    metadata = load_database_encryption_metadata(app)
    if metadata is None:
        raise DatabaseEncryptionError("Die Verschlüsselungs-Konfiguration fehlt.")
    recovery_key = generate_database_recovery_key()
    metadata["recovery"] = _wrap_database_key(active_database_key(app), _normalised_recovery_key(recovery_key))
    metadata["updated_at"] = utc_now()
    write_database_encryption_metadata(app, metadata)
    return _remember_pending_database_recovery_key(app, recovery_key), recovery_key


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


def member_required(view):
    """Permit the former Seller workflow to Member, Manager and Admin."""

    return role_required("member")(view)


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
    username = str(g.user["username"]) if g.get("user") else None
    if username is None and user_id is not None and table_exists(connection, "users"):
        actor = connection.execute("SELECT username FROM users WHERE id = ?", (user_id,)).fetchone()
        username = str(actor["username"]) if actor is not None else None
    connection.execute(
        """
        INSERT INTO audit_log (
            created_at, user_id, user_username, action, entity_type, entity_id, details_json
        ) VALUES (?, ?, ?, ?, ?, ?, ?)
        """,
        (
            utc_now(),
            user_id,
            username,
            action,
            entity_type,
            entity_id,
            json.dumps(details or {}, ensure_ascii=False),
        ),
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
               v.default_purchase_price_cents, v.minimum_stock, v.is_offered, v.no_reorder, v.is_active,
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
            SELECT ov.id, ov.value, ov.position AS value_position, ov.is_active,
                   og.name AS group_name, og.position AS group_position,
                   og.is_active AS group_is_active
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
                        "value_position": value["value_position"],
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
    connection: sqlite3.Connection,
    purchase_rows: Iterable[sqlite3.Row],
    *,
    timezone_name: str = "Europe/Berlin",
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
                "recorded_at": first_item["created_at"],
                "recorded_at_time": display_recorded_time(first_item["created_at"], timezone_name),
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
    connection: sqlite3.Connection,
    sale_rows: Iterable[sqlite3.Row],
    *,
    timezone_name: str = "Europe/Berlin",
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
                "recorded_at": first_item["created_at"],
                "recorded_at_time": display_recorded_time(first_item["created_at"], timezone_name),
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
    connection: sqlite3.Connection, *, offered_only: bool = False, include_variant_photos: bool = False
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
        if include_variant_photos:
            fallback_variant_rows = connection.execute(
                "SELECT id FROM variants WHERE article_id = ? AND is_active = 1 ORDER BY id",
                (article["id"],),
            ).fetchall()
            fallback_labels = variant_label_map(connection, [row["id"] for row in fallback_variant_rows])
            fallback_candidates = [fallback_labels[row["id"]] for row in fallback_variant_rows]
            add_variant_photo_fallbacks(
                variants,
                variant_photos_by_variant(connection, [row["id"] for row in fallback_variant_rows]),
                fallback_candidates=fallback_candidates,
            )

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
    photos = variant_photos_by_variant(connection, [variant["id"] for variant in variants])
    for variant in variants:
        variant["photos"] = photos.get(int(variant["id"]), [])
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


def preserve_variants_for_new_option_groups(
    connection: sqlite3.Connection, article_id: int, first_value_ids: Iterable[int]
) -> None:
    """Assign existing variants to each newly introduced option's first value.

    A new option dimension changes every combination key.  Without this small
    migration, the old variants would merely become inactive and take their
    stock, prices and photos out of the active catalogue.  The first value is
    deliberately the explicit mapping target, so users can move it before or
    after saving without losing the original variant records.
    """

    defaults = [int(value_id) for value_id in first_value_ids]
    if not defaults:
        return
    now = utc_now()
    rows = connection.execute(
        "SELECT id, option_value_ids_json FROM variants WHERE article_id = ? AND is_active = 1",
        (article_id,),
    ).fetchall()
    for row in rows:
        try:
            option_ids = [int(value_id) for value_id in json.loads(row["option_value_ids_json"] or "[]")]
        except (TypeError, ValueError, json.JSONDecodeError):
            continue
        migrated_ids = option_ids + [value_id for value_id in defaults if value_id not in option_ids]
        if migrated_ids == option_ids:
            continue
        connection.execute(
            """
            UPDATE variants SET option_value_ids_json = ?, combination_key = ?, updated_at = ?
            WHERE id = ?
            """,
            (json.dumps(migrated_ids), sorted_combination_key(migrated_ids), now, row["id"]),
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
) -> list[int]:
    """Apply an option-grid edit using soft deletion for removed values/groups."""

    now = utc_now()
    known_group_rows = connection.execute(
        "SELECT id FROM option_groups WHERE article_id = ?", (article_id,)
    ).fetchall()
    known_group_ids = {row["id"] for row in known_group_rows}
    submitted_group_ids: set[int] = set()
    new_group_first_value_ids: list[int] = []

    for group in option_groups:
        group_id = group["id"]
        if group_id is not None and group_id not in known_group_ids:
            raise ValueError("Eine übermittelte Option gehört nicht zu diesem Artikel.")
        is_new_group = group_id is None
        if is_new_group:
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
        for value_position, value in enumerate(group["values"]):
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
            if is_new_group and value_position == 0:
                new_group_first_value_ids.append(int(value_id))

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
    return new_group_first_value_ids


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
                        "nein" if variant["no_reorder"] else "ja",
                        "ja" if article["is_offered"] and variant["is_offered"] else "nein",
                        "aktiv" if variant["is_active"] else "inaktiv",
                    ]
                )
        return (
            "artikel",
            [
                "Artikel-ID", "Artikel", "Varianten-ID", "Optionen", "Bestand", "Mindestbestand",
                "Mindestbestandswarnung", "Verkaufspreis", "Standard-Einkaufspreis", "Nachbestellen", "Angeboten", "Status",
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
                    "nein" if label["no_reorder"] else "ja",
                    "ja" if label["article_is_offered"] and label["is_offered"] else "nein",
                ]
            )
        return (
            "bestand",
            [
                "Artikel", "Optionen", "Gekauft", "Verkauft", "Aktueller Bestand", "Mindestbestand",
                "Mindestbestandswarnung", "Nachbestellen", "Angeboten",
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


def _normalised_csv_header(value: Any) -> str:
    return re.sub(r"[\s_-]+", "", str(value or "").strip().casefold())


def _transaction_csv_options(raw_value: Any, *, line_number: int) -> list[dict[str, str]]:
    """Parse ``Optionsname=Wert`` pairs separated inside the third CSV field."""

    raw = str(raw_value or "").strip()
    if not raw:
        return []
    options: list[dict[str, str]] = []
    seen_groups: set[str] = set()
    for token in raw.split(";"):
        token = token.strip()
        if not token or "=" not in token:
            raise ValueError(
                f"Zeile {line_number}: Optionen müssen als Optionsname=Wert angegeben werden."
            )
        group_name, value = (part.strip() for part in token.split("=", 1))
        if not group_name or not value:
            raise ValueError(
                f"Zeile {line_number}: Optionsname und Optionswert dürfen nicht leer sein."
            )
        if len(group_name) > 120 or len(value) > 120:
            raise ValueError(f"Zeile {line_number}: Eine Option oder ihr Wert ist zu lang.")
        group_key = group_name.casefold()
        if group_key in seen_groups:
            raise ValueError(f"Zeile {line_number}: Die Option „{group_name}“ kommt doppelt vor.")
        seen_groups.add(group_key)
        options.append(
            {
                "group_name": group_name,
                "group_key": group_key,
                "value": value,
                "value_key": value.casefold(),
            }
        )
    return options


def transaction_csv_rows(uploaded_file: Any, kind: str) -> list[dict[str, Any]]:
    """Validate a five-column transaction CSV without changing database state."""

    if kind not in TRANSACTION_CSV_HEADERS:
        raise ValueError("Unbekannte Importart.")
    filename = str(getattr(uploaded_file, "filename", "") or "").strip()
    if not filename:
        raise ValueError("Bitte eine CSV-Datei auswählen.")
    if Path(filename).suffix.casefold() != ".csv":
        raise ValueError("Die Importdatei muss die Endung .csv haben.")
    content = uploaded_file.read(MAX_TRANSACTION_CSV_BYTES + 1)
    if len(content) > MAX_TRANSACTION_CSV_BYTES:
        raise ValueError("Die CSV-Datei darf höchstens 2 MB groß sein.")
    if not content:
        raise ValueError("Die CSV-Datei ist leer.")
    try:
        text = content.decode("utf-8-sig")
    except UnicodeDecodeError:
        try:
            text = content.decode("cp1252")
        except UnicodeDecodeError as exc:
            raise ValueError("Die CSV-Datei muss als UTF-8 oder Windows-1252 gespeichert sein.") from exc
    if "\x00" in text:
        raise ValueError("Die CSV-Datei enthält ungültige Nullzeichen.")

    reader = csv.reader(io.StringIO(text, newline=""), delimiter=";", strict=True)
    try:
        header = next(reader)
    except StopIteration as exc:
        raise ValueError("Die CSV-Datei ist leer.") from exc
    except csv.Error as exc:
        raise ValueError(f"Die Kopfzeile ist keine gültige CSV-Zeile: {exc}") from exc
    expected_header = TRANSACTION_CSV_HEADERS[kind]
    if [_normalised_csv_header(value) for value in header] != [
        _normalised_csv_header(value) for value in expected_header
    ]:
        raise ValueError(
            "Die Kopfzeile muss exakt diese fünf Spalten enthalten: " + ";".join(expected_header)
        )

    rows: list[dict[str, Any]] = []
    group_keys_by_article: dict[str, frozenset[str]] = {}
    try:
        for values in reader:
            line_number = reader.line_num
            if not any(str(value).strip() for value in values):
                continue
            if len(values) != 5:
                raise ValueError(
                    f"Zeile {line_number}: Erwartet werden fünf Spalten, gefunden wurden {len(values)}."
                )
            quantity_raw, article_raw, options_raw, price_raw, party_raw = values
            try:
                quantity = parse_positive_int(quantity_raw, field_name="Anzahl")
            except ValueError as exc:
                raise ValueError(f"Zeile {line_number}: {exc}") from exc
            article_name = str(article_raw).strip()
            if not article_name:
                raise ValueError(f"Zeile {line_number}: Der Artikelname darf nicht leer sein.")
            if len(article_name) > 200:
                raise ValueError(f"Zeile {line_number}: Der Artikelname ist zu lang.")
            options = _transaction_csv_options(options_raw, line_number=line_number)
            party = str(party_raw).strip()
            if not party:
                party_label = "Lieferant" if kind == "purchases" else "Kunde"
                raise ValueError(f"Zeile {line_number}: {party_label} darf nicht leer sein.")
            if len(party) > 300:
                raise ValueError(f"Zeile {line_number}: Der Name in der fünften Spalte ist zu lang.")
            price_cents: int | None = None
            if str(price_raw).strip():
                try:
                    price_cents = money_to_cents(price_raw, field_name=expected_header[3])
                except ValueError as exc:
                    raise ValueError(f"Zeile {line_number}: {exc}") from exc

            article_key = article_name.casefold()
            option_key = tuple(sorted((option["group_key"], option["value_key"]) for option in options))
            group_keys = frozenset(option["group_key"] for option in options)
            previous_group_keys = group_keys_by_article.setdefault(article_key, group_keys)
            if previous_group_keys != group_keys:
                raise ValueError(
                    f"Zeile {line_number}: Für „{article_name}“ müssen in jeder Zeile dieselben Optionsgruppen stehen."
                )
            rows.append(
                {
                    "index": len(rows),
                    "line_number": line_number,
                    "quantity": quantity,
                    "article_name": article_name,
                    "article_key": article_key,
                    "options": options,
                    "option_key": option_key,
                    "group_keys": group_keys,
                    "price_cents": price_cents,
                    "price_source": "explicit" if price_cents is not None else None,
                    "party": party,
                }
            )
            if len(rows) > MAX_TRANSACTION_CSV_ROWS:
                raise ValueError(f"Die CSV-Datei darf höchstens {MAX_TRANSACTION_CSV_ROWS} Datenzeilen enthalten.")
    except csv.Error as exc:
        raise ValueError(f"Zeile {reader.line_num}: Ungültige CSV-Formatierung: {exc}") from exc
    if not rows:
        raise ValueError("Die CSV-Datei enthält unterhalb der Kopfzeile keine Daten.")
    return rows


def _transaction_rows_by_article(rows: list[dict[str, Any]]) -> dict[str, list[dict[str, Any]]]:
    grouped: dict[str, list[dict[str, Any]]] = {}
    for row in rows:
        grouped.setdefault(row["article_key"], []).append(row)
    return grouped


def _catalog_article_for_import(connection: sqlite3.Connection, article_name: str) -> sqlite3.Row | None:
    for article in connection.execute(
        "SELECT * FROM articles ORDER BY is_active DESC, id"
    ).fetchall():
        if str(article["name"]).casefold() == article_name.casefold():
            return article
    return None


def preflight_transaction_import(connection: sqlite3.Connection, rows: list[dict[str, Any]]) -> None:
    """Reject ambiguous existing catalogues and excessive Cartesian products."""

    for article_rows in _transaction_rows_by_article(rows).values():
        article_name = article_rows[0]["article_name"]
        article = _catalog_article_for_import(connection, article_name)
        active_groups: dict[str, sqlite3.Row] = {}
        if article is not None:
            for group in connection.execute(
                """
                SELECT * FROM option_groups
                WHERE article_id = ? AND is_active = 1
                ORDER BY position, id
                """,
                (article["id"],),
            ).fetchall():
                key = str(group["name"]).casefold()
                if key in active_groups:
                    raise ValueError(
                        f"„{article_name}“ enthält doppelte aktive Optionsgruppen und kann nicht sicher importiert werden."
                    )
                active_groups[key] = group
        file_group_keys = set(article_rows[0]["group_keys"])
        missing_groups = set(active_groups) - file_group_keys
        if missing_groups:
            names = ", ".join(str(active_groups[key]["name"]) for key in sorted(missing_groups))
            raise ValueError(
                f"Bei „{article_name}“ fehlen bestehende Optionsgruppen in der CSV: {names}."
            )

        variant_count = 1
        for group_key in file_group_keys:
            imported_values = {
                option["value_key"]
                for row in article_rows
                for option in row["options"]
                if option["group_key"] == group_key
            }
            existing_values: set[str] = set()
            group = active_groups.get(group_key)
            if group is not None:
                existing_values = {
                    str(value["value"]).casefold()
                    for value in connection.execute(
                        "SELECT value FROM option_values WHERE option_group_id = ? AND is_active = 1",
                        (group["id"],),
                    ).fetchall()
                }
            variant_count *= len(existing_values | imported_values)
            if variant_count > MAX_IMPORTED_VARIANTS_PER_ARTICLE:
                raise ValueError(
                    f"„{article_name}“ würde mehr als {MAX_IMPORTED_VARIANTS_PER_ARTICLE} Varianten erzeugen."
                )


def _catalog_price_for_import(
    connection: sqlite3.Connection, row: dict[str, Any], kind: str
) -> int | None:
    article = _catalog_article_for_import(connection, row["article_name"])
    if article is None:
        return None
    price_column = "default_purchase_price_cents" if kind == "purchases" else "sale_price_cents"
    variants = connection.execute(
        f"SELECT id, {price_column}, is_active FROM variants WHERE article_id = ? ORDER BY is_active DESC, id",
        (article["id"],),
    ).fetchall()
    if not variants:
        article_column = "default_purchase_price_cents" if kind == "purchases" else "default_sale_price_cents"
        return int(article[article_column])
    labels = variant_label_map(connection, [variant["id"] for variant in variants])
    desired_pairs = set(row["option_key"])
    ranked: list[tuple[int, int, int, int]] = []
    for variant in variants:
        label = labels[int(variant["id"])]
        candidate_pairs = {
            (str(option["group_name"]).casefold(), str(option["value"]).casefold())
            for option in label.get("options", [])
        }
        matching_pairs = len(desired_pairs & candidate_pairs)
        ranked.append(
            (-matching_pairs, 0 if variant["is_active"] else 1, int(variant["id"]), int(variant[price_column]))
        )
    ranked.sort()
    return ranked[0][3]


def resolve_transaction_import_prices(
    connection: sqlite3.Connection, rows: list[dict[str, Any]], kind: str
) -> None:
    """Fill blanks from the nearest exact option, then the closest article option."""

    for article_rows in _transaction_rows_by_article(rows).values():
        explicitly_priced = [row for row in article_rows if row["price_cents"] is not None]
        for row in article_rows:
            if row["price_cents"] is not None:
                continue
            exact_candidates = [
                candidate for candidate in explicitly_priced if candidate["option_key"] == row["option_key"]
            ]
            candidates = exact_candidates or explicitly_priced
            if candidates:
                desired_pairs = set(row["option_key"])

                def candidate_rank(candidate: dict[str, Any]) -> tuple[int, int, int, int]:
                    matching_pairs = len(desired_pairs & set(candidate["option_key"]))
                    return (
                        -matching_pairs if not exact_candidates else 0,
                        abs(int(candidate["index"]) - int(row["index"])),
                        0 if int(candidate["index"]) < int(row["index"]) else 1,
                        int(candidate["index"]),
                    )

                selected = min(candidates, key=candidate_rank)
                row["price_cents"] = int(selected["price_cents"])
                row["price_source"] = "exact_option" if exact_candidates else "closest_option"
                continue
            catalog_price = _catalog_price_for_import(connection, row, kind)
            if catalog_price is None:
                raise ValueError(
                    f"Für den neuen Artikel „{row['article_name']}“ ist in keiner Zeile ein Preis eingetragen."
                )
            row["price_cents"] = catalog_price
            row["price_source"] = "catalog"


def _imported_values_for_group(
    article_rows: list[dict[str, Any]], group_key: str
) -> list[dict[str, str]]:
    values: list[dict[str, str]] = []
    seen: set[str] = set()
    for row in article_rows:
        for option in row["options"]:
            if option["group_key"] != group_key or option["value_key"] in seen:
                continue
            seen.add(option["value_key"])
            values.append(option)
    return values


def _upsert_transaction_import_article(
    connection: sqlite3.Connection,
    article_rows: list[dict[str, Any]],
    kind: str,
) -> bool:
    """Merge CSV options into one article and attach every row to its variant."""

    article_name = article_rows[0]["article_name"]
    article = _catalog_article_for_import(connection, article_name)
    now = utc_now()
    created_article = article is None
    first_price = int(article_rows[0]["price_cents"])
    if article is None:
        default_sale_price = first_price if kind == "sales" else 0
        default_purchase_price = first_price if kind == "purchases" else 0
        cursor = connection.execute(
            """
            INSERT INTO articles (
                name, default_sale_price_cents, default_purchase_price_cents,
                is_offered, is_active, created_at, updated_at
            ) VALUES (?, ?, ?, 1, 1, ?, ?)
            """,
            (article_name, default_sale_price, default_purchase_price, now, now),
        )
        article_id = int(cursor.lastrowid)
    else:
        article_id = int(article["id"])
        connection.execute(
            "UPDATE articles SET is_active = 1, is_offered = 1, updated_at = ? WHERE id = ?",
            (now, article_id),
        )

    all_groups = connection.execute(
        """
        SELECT * FROM option_groups
        WHERE article_id = ?
        ORDER BY is_active DESC, position, id
        """,
        (article_id,),
    ).fetchall()
    active_groups = [group for group in all_groups if group["is_active"]]
    active_by_key = {str(group["name"]).casefold(): group for group in active_groups}
    all_by_key: dict[str, list[sqlite3.Row]] = defaultdict(list)
    for group in all_groups:
        all_by_key[str(group["name"]).casefold()].append(group)

    first_row_group_order = [option["group_key"] for option in article_rows[0]["options"]]
    selected_groups: list[tuple[str, sqlite3.Row | None]] = [
        (str(group["name"]).casefold(), group) for group in active_groups
    ]
    for group_key in first_row_group_order:
        if group_key in active_by_key:
            continue
        reusable = next((group for group in all_by_key.get(group_key, []) if not group["is_active"]), None)
        selected_groups.append((group_key, reusable))

    raw_configuration: list[dict[str, Any]] = []
    for position, (group_key, group) in enumerate(selected_groups):
        imported_values = _imported_values_for_group(article_rows, group_key)
        group_name = str(group["name"]) if group is not None and group["is_active"] else imported_values[0]["group_name"]
        all_values = (
            connection.execute(
                """
                SELECT * FROM option_values
                WHERE option_group_id = ?
                ORDER BY is_active DESC, position, id
                """,
                (group["id"],),
            ).fetchall()
            if group is not None
            else []
        )
        values_by_key: dict[str, sqlite3.Row] = {}
        for value in all_values:
            values_by_key.setdefault(str(value["value"]).casefold(), value)
        values: list[dict[str, Any]] = []
        included_value_keys: set[str] = set()
        if group is not None and group["is_active"]:
            for value in all_values:
                value_key = str(value["value"]).casefold()
                if not value["is_active"] or value_key in included_value_keys:
                    continue
                included_value_keys.add(value_key)
                values.append({"id": int(value["id"]), "value": str(value["value"])})
        for imported_value in imported_values:
            value_key = imported_value["value_key"]
            if value_key in included_value_keys:
                continue
            included_value_keys.add(value_key)
            existing_value = values_by_key.get(value_key)
            values.append(
                {
                    "id": int(existing_value["id"]) if existing_value is not None else None,
                    "value": (
                        str(existing_value["value"])
                        if existing_value is not None and existing_value["is_active"]
                        else imported_value["value"]
                    ),
                }
            )
        raw_configuration.append(
            {
                "id": int(group["id"]) if group is not None else None,
                "name": group_name,
                "position": position,
                "values": values,
            }
        )

    configuration = validate_option_configuration(raw_configuration)
    apply_option_configuration(connection, article_id, configuration)
    sync_variants(connection, article_id)

    group_map: dict[str, sqlite3.Row] = {}
    value_maps: dict[str, dict[str, sqlite3.Row]] = {}
    for group in connection.execute(
        """
        SELECT * FROM option_groups
        WHERE article_id = ? AND is_active = 1
        ORDER BY position, id
        """,
        (article_id,),
    ).fetchall():
        group_key = str(group["name"]).casefold()
        group_map[group_key] = group
        value_maps[group_key] = {
            str(value["value"]).casefold(): value
            for value in connection.execute(
                """
                SELECT * FROM option_values
                WHERE option_group_id = ? AND is_active = 1
                ORDER BY position, id
                """,
                (group["id"],),
            ).fetchall()
        }

    selected_variant_ids: set[int] = set()
    price_column = "default_purchase_price_cents" if kind == "purchases" else "sale_price_cents"
    for row in article_rows:
        option_value_ids: list[int] = []
        for option in row["options"]:
            group = group_map.get(option["group_key"])
            value = value_maps.get(option["group_key"], {}).get(option["value_key"])
            if group is None or value is None:
                raise ValueError(
                    f"Zeile {row['line_number']}: Die importierte Variante konnte nicht angelegt werden."
                )
            option_value_ids.append(int(value["id"]))
        combination_key = sorted_combination_key(option_value_ids)
        variant = connection.execute(
            "SELECT id FROM variants WHERE article_id = ? AND combination_key = ? AND is_active = 1",
            (article_id, combination_key),
        ).fetchone()
        if variant is None:
            raise ValueError(f"Zeile {row['line_number']}: Die Variante konnte nicht aufgelöst werden.")
        variant_id = int(variant["id"])
        row["variant_id"] = variant_id
        selected_variant_ids.add(variant_id)
        connection.execute(
            f"UPDATE variants SET {price_column} = ?, updated_at = ? WHERE id = ?",
            (int(row["price_cents"]), now, variant_id),
        )

    connection.execute(
        "UPDATE variants SET is_offered = 0, updated_at = ? WHERE article_id = ? AND is_active = 1",
        (now, article_id),
    )
    if selected_variant_ids:
        placeholders = ",".join("?" for _ in selected_variant_ids)
        connection.execute(
            f"UPDATE variants SET is_offered = 1, updated_at = ? WHERE id IN ({placeholders})",
            [now, *sorted(selected_variant_ids)],
        )
    return created_article


def import_transaction_rows(
    connection: sqlite3.Connection, rows: list[dict[str, Any]], kind: str
) -> dict[str, int]:
    """Create catalog entries and ledger rows after a successful full preflight."""

    grouped = _transaction_rows_by_article(rows)
    created_articles = sum(
        int(_upsert_transaction_import_article(connection, article_rows, kind))
        for article_rows in grouped.values()
    )
    on_date = today_iso()
    prefix = "E" if kind == "purchases" else "V"
    first_receipt = next_receipt_id(connection, prefix, on_date)
    receipt_match = re.search(r"-(\d+)$", first_receipt)
    first_sequence = int(receipt_match.group(1)) if receipt_match else 1
    receipt_stem = first_receipt.rsplit("-", 1)[0]
    first_ledger_id: int | None = None
    created_at = utc_now()

    for offset, row in enumerate(rows):
        receipt_id = f"{receipt_stem}-{first_sequence + offset:03d}"
        if kind == "purchases":
            cursor = connection.execute(
                """
                INSERT INTO purchases (
                    receipt_id, variant_id, quantity, unit_cost_cents, purchased_on,
                    supplier, invoice_reference, invoice_file_path, comment, created_at,
                    created_by, created_by_username
                ) VALUES (?, ?, ?, ?, ?, ?, NULL, NULL, ?, ?, ?, ?)
                """,
                (
                    receipt_id,
                    row["variant_id"],
                    row["quantity"],
                    row["price_cents"],
                    on_date,
                    row["party"],
                    "CSV-Import",
                    created_at,
                    g.user["id"],
                    g.user["username"],
                ),
            )
        else:
            amount_due = int(row["quantity"]) * int(row["price_cents"])
            cursor = connection.execute(
                """
                INSERT INTO sales (
                    receipt_id, variant_id, quantity, unit_price_cents, amount_due_cents,
                    amount_given_cents, donation_cents, payment_method, is_paid, payment_follow_up,
                    is_received, delivery_status, customer_name, customer_address, event_name,
                    sold_by, comment, sold_on, created_at, created_by, created_by_username
                ) VALUES (?, ?, ?, ?, ?, ?, 0, 'Sonstiges', 1, 0, 1, 'not_applicable', ?, NULL, NULL, ?, ?, ?, ?, ?, ?)
                """,
                (
                    receipt_id,
                    row["variant_id"],
                    row["quantity"],
                    row["price_cents"],
                    amount_due,
                    amount_due,
                    row["party"],
                    g.user["username"],
                    "CSV-Import",
                    on_date,
                    created_at,
                    g.user["id"],
                    g.user["username"],
                ),
            )
        if first_ledger_id is None:
            first_ledger_id = int(cursor.lastrowid)

    fallback_prices = sum(row["price_source"] != "explicit" for row in rows)
    audit(
        connection,
        "import_csv",
        "purchases" if kind == "purchases" else "sales",
        first_ledger_id,
        {
            "row_count": len(rows),
            "article_count": len(grouped),
            "created_article_count": created_articles,
            "fallback_price_count": fallback_prices,
        },
    )
    return {
        "row_count": len(rows),
        "article_count": len(grouped),
        "created_article_count": created_articles,
        "fallback_price_count": fallback_prices,
    }


def create_backup(app: Flask, *, force: bool = False) -> Path | None:
    """Create a restorable encrypted SQLite snapshot and invoice files.

    It runs after every successful write.  The application database itself stays
    authoritative. CSV files remain deliberate browser exports; encrypted
    installations never create them automatically next to a backup.
    """

    if not force and not app.config.get("AUTO_BACKUP", True):
        return None
    backup_root = Path(app.config["BACKUP_DIR"])
    backup_root.mkdir(parents=True, exist_ok=True)
    timestamp = datetime.now().strftime("%Y-%m-%d_%H-%M-%S")
    target = backup_root / timestamp
    suffix = 1
    while target.exists():
        suffix += 1
        target = backup_root / f"{timestamp}_{suffix}"
    target.mkdir()

    source = db_connect(app.config["DATABASE"], app=app)
    destination = db_connect(target / "merch.sqlite3", app=app)
    try:
        source.backup(destination)
        destination.close()
        # A CSV is intentionally an explicit browser export, never an
        # unencrypted sidecar of an automatic encrypted backup.
        if not database_encryption_enabled(app):
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
        photo_source = Path(app.config["VARIANT_PHOTO_UPLOAD_DIR"])
        if photo_source.is_dir():
            photo_target = target / "variant-photos"
            photo_target.mkdir()
            for photo in photo_source.iterdir():
                if not photo.is_file():
                    continue
                try:
                    os.link(photo, photo_target / photo.name)
                except OSError:
                    shutil.copy2(photo, photo_target / photo.name)
        if database_encryption_enabled(app):
            metadata_path = database_encryption_metadata_path(app)
            if metadata_path.is_file():
                # It contains only wrapped keys, but is required together with
                # an offline recovery key to open a copied SQLCipher database.
                shutil.copy2(metadata_path, target / "encryption.json")
    finally:
        source.close()
        try:
            destination.close()
        except Exception:
            pass

    retention_days = int(app.config["BACKUP_RETENTION_DAYS"])
    cutoff = datetime.now().timestamp() - retention_days * 24 * 60 * 60
    for child in backup_root.iterdir():
        if child.is_dir() and child.stat().st_mtime < cutoff:
            shutil.rmtree(child)
    return target


def backup_after_commit() -> None:
    """Run backup without risking a successfully committed sale on backup failure."""

    try:
        create_backup(current_app._get_current_object())
    except Exception:  # pragma: no cover - failure is logged, not hidden from ops
        current_app.logger.exception("Automatic backup failed after a committed write")


def operational_backup_points(app: Flask) -> list[dict[str, Any]]:
    """Return only valid, direct-child operational restore points for the UI."""

    backup_root = Path(app.config["BACKUP_DIR"])
    if not backup_root.is_dir():
        return []
    points: list[dict[str, Any]] = []
    for directory in backup_root.iterdir():
        snapshot = directory / "merch.sqlite3"
        if not directory.is_dir() or not snapshot.is_file():
            continue
        invoices = directory / "invoices"
        photos = directory / "variant-photos"
        points.append(
            {
                "name": directory.name,
                "modified_at": datetime.fromtimestamp(directory.stat().st_mtime).strftime("%d.%m.%Y %H:%M:%S"),
                "database_bytes": snapshot.stat().st_size,
                "invoice_count": sum(1 for item in invoices.rglob("*") if item.is_file()) if invoices.is_dir() else 0,
                "photo_count": sum(1 for item in photos.rglob("*") if item.is_file()) if photos.is_dir() else 0,
            }
        )
    return sorted(points, key=lambda point: point["name"], reverse=True)


def selected_operational_backup(app: Flask, backup_name: Any) -> Path:
    """Resolve an admin-selected backup without accepting traversal paths."""

    name = str(backup_name or "").strip()
    root = Path(app.config["BACKUP_DIR"]).resolve()
    candidate = (root / name).resolve()
    if not name or candidate.parent != root or not candidate.is_dir():
        raise ValueError("Der ausgewählte Sicherungspunkt wurde nicht gefunden.")
    if not (candidate / "merch.sqlite3").is_file():
        raise ValueError("Dieser Sicherungspunkt enthält keine Datenbankkopie.")
    return candidate


def validate_operational_snapshot(app: Flask, snapshot_path: Path) -> None:
    """Reject malformed or old combined-file backups before any replacement."""

    connection: sqlite3.Connection | None = None
    try:
        connection = db_connect(snapshot_path, app=app)
        tables = {
            row[0]
            for row in connection.execute("SELECT name FROM sqlite_master WHERE type = 'table'").fetchall()
        }
    except Exception as exc:
        raise ValueError("Die Sicherungsdatei ist keine lesbare SQLite-Datenbank.") from exc
    finally:
        if connection is not None:
            connection.close()
    required = {"articles", "variants", "purchases", "sales"}
    if not required.issubset(tables):
        raise ValueError("Die Sicherung enthält keine vollständigen Betriebsdaten.")
    if "users" in tables:
        raise ValueError(
            "Diese alte Sicherung enthält noch Benutzerkonten und wird deshalb nicht automatisch wiederhergestellt."
        )


def restore_operational_backup(app: Flask, backup_name: Any) -> tuple[Path, Path]:
    """Restore one operational backup while keeping ``users.sqlite3`` intact.

    A fresh backup is forced first, even when automatic backups are disabled.
    Database, invoices and product photos are staged beside the live files and then
    swapped with rollback protection, so an invalid/incomplete backup cannot
    leave the running installation half-restored.
    """

    source_directory = selected_operational_backup(app, backup_name)
    source_database = source_directory / "merch.sqlite3"
    validate_operational_snapshot(app, source_database)
    safety_backup = create_backup(app, force=True)
    if safety_backup is None:  # Defensive: force=True always creates one.
        raise RuntimeError("Vor der Wiederherstellung konnte keine Sicherheitskopie angelegt werden.")

    database_path = Path(app.config["DATABASE"])
    invoice_dir = Path(app.config["INVOICE_UPLOAD_DIR"])
    photo_dir = Path(app.config["VARIANT_PHOTO_UPLOAD_DIR"])
    staging_dir = database_path.parent / f".restore-{uuid.uuid4().hex}"
    staged_database = staging_dir / "merch.sqlite3"
    staged_invoices = staging_dir / "invoices"
    staged_photos = staging_dir / "variant-photos"
    previous_database = staging_dir / "previous-merch.sqlite3"
    previous_invoices = staging_dir / "previous-invoices"
    previous_photos = staging_dir / "previous-variant-photos"
    staging_dir.mkdir(parents=True, exist_ok=False)
    try:
        source = db_connect(source_database, app=app)
        try:
            destination = db_connect(staged_database, app=app)
            try:
                source.backup(destination)
            finally:
                destination.close()
        finally:
            source.close()
        validate_operational_snapshot(app, staged_database)
        staged_invoices.mkdir()
        staged_photos.mkdir()
        source_invoices = source_directory / "invoices"
        if source_invoices.is_dir():
            for item in source_invoices.rglob("*"):
                if item.is_file():
                    target = staged_invoices / item.relative_to(source_invoices)
                    target.parent.mkdir(parents=True, exist_ok=True)
                    shutil.copy2(item, target)
        source_photos = source_directory / "variant-photos"
        if source_photos.is_dir():
            for item in source_photos.rglob("*"):
                if item.is_file():
                    target = staged_photos / item.relative_to(source_photos)
                    target.parent.mkdir(parents=True, exist_ok=True)
                    shutil.copy2(item, target)

        close_operational_db()
        moved_old_database = False
        moved_old_invoices = False
        moved_old_photos = False
        placed_new_database = False
        placed_new_invoices = False
        placed_new_photos = False
        try:
            if database_path.exists():
                os.replace(database_path, previous_database)
                moved_old_database = True
            for sidecar in (Path(f"{database_path}-wal"), Path(f"{database_path}-shm")):
                sidecar.unlink(missing_ok=True)
            if invoice_dir.exists():
                os.replace(invoice_dir, previous_invoices)
                moved_old_invoices = True
            if photo_dir.exists():
                os.replace(photo_dir, previous_photos)
                moved_old_photos = True
            os.replace(staged_database, database_path)
            placed_new_database = True
            os.replace(staged_invoices, invoice_dir)
            placed_new_invoices = True
            os.replace(staged_photos, photo_dir)
            placed_new_photos = True
            initialise_operations_database(app)
        except Exception:
            if placed_new_database:
                database_path.unlink(missing_ok=True)
            for sidecar in (Path(f"{database_path}-wal"), Path(f"{database_path}-shm")):
                sidecar.unlink(missing_ok=True)
            if moved_old_database and previous_database.exists():
                os.replace(previous_database, database_path)
            if placed_new_invoices:
                shutil.rmtree(invoice_dir, ignore_errors=True)
            if moved_old_invoices and previous_invoices.exists():
                os.replace(previous_invoices, invoice_dir)
            if placed_new_photos:
                shutil.rmtree(photo_dir, ignore_errors=True)
            if moved_old_photos and previous_photos.exists():
                os.replace(previous_photos, photo_dir)
            raise
    finally:
        shutil.rmtree(staging_dir, ignore_errors=True)
    return source_directory, safety_backup


def create_reset_archive(app: Flask, source_connection: sqlite3.Connection) -> Path:
    """Archive only the operational database and its attachments before a reset.

    ``users.sqlite3`` is deliberately never included here: accounts, password
    hashes, MFA settings and their security audit are independent from the
    merchandise ledger and survive an operational reset unchanged.
    """

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
        snapshot_connection = db_connect(snapshot_path, app=app)
        try:
            source_connection.backup(snapshot_connection)
        finally:
            snapshot_connection.close()
        with ZipFile(archive_path, "w", ZIP_DEFLATED) as archive:
            archive.write(snapshot_path, "data/merch.sqlite3")
            invoice_dir = Path(app.config["INVOICE_UPLOAD_DIR"])
            if invoice_dir.is_dir():
                for item in invoice_dir.rglob("*"):
                    if item.is_file():
                        archive.write(item, Path("data/invoices") / item.relative_to(invoice_dir))
            photo_dir = Path(app.config["VARIANT_PHOTO_UPLOAD_DIR"])
            if photo_dir.is_dir():
                for item in photo_dir.rglob("*"):
                    if item.is_file():
                        archive.write(item, Path("data/variant-photos") / item.relative_to(photo_dir))
    except Exception:
        archive_path.unlink(missing_ok=True)
        raise
    finally:
        snapshot_path.unlink(missing_ok=True)
    return archive_path


def reset_data_store(app: Flask) -> None:
    """Replace only catalogue, ledger and attachment data with a fresh database."""

    database_path = Path(app.config["DATABASE"])
    invoice_dir = Path(app.config["INVOICE_UPLOAD_DIR"])
    photo_dir = Path(app.config["VARIANT_PHOTO_UPLOAD_DIR"])
    for path in (database_path, Path(f"{database_path}-wal"), Path(f"{database_path}-shm")):
        path.unlink(missing_ok=True)
    shutil.rmtree(invoice_dir, ignore_errors=True)
    shutil.rmtree(photo_dir, ignore_errors=True)
    database_path.parent.mkdir(parents=True, exist_ok=True)
    invoice_dir.mkdir(parents=True, exist_ok=True)
    photo_dir.mkdir(parents=True, exist_ok=True)
    initialise_operations_database(app)


def band_finance_summary_payload(connection: sqlite3.Connection) -> dict[str, int]:
    """Return the independent band-ledger totals without touching merch data."""

    totals = connection.execute(
        """
        SELECT
            COALESCE(SUM(CASE WHEN transaction_type = 'income' THEN amount_cents ELSE 0 END), 0)
                AS income_cents,
            COALESCE(SUM(CASE WHEN transaction_type = 'expense' THEN amount_cents ELSE 0 END), 0)
                AS expense_cents
        FROM band_transactions
        WHERE is_cancelled = 0
        """
    ).fetchone()
    income_cents = int(totals["income_cents"])
    expense_cents = int(totals["expense_cents"])
    categories = [
        dict(row)
        for row in connection.execute(
            """
            SELECT category,
                   COALESCE(SUM(CASE WHEN transaction_type = 'income' THEN amount_cents ELSE 0 END), 0)
                       AS income_cents,
                   COALESCE(SUM(CASE WHEN transaction_type = 'expense' THEN amount_cents ELSE 0 END), 0)
                       AS expense_cents
            FROM band_transactions
            WHERE is_cancelled = 0
            GROUP BY category
            ORDER BY category COLLATE NOCASE
            """
        ).fetchall()
    ]
    for category in categories:
        category["balance_cents"] = int(category["income_cents"]) - int(category["expense_cents"])
    return {
        "income_cents": income_cents,
        "expense_cents": expense_cents,
        "balance_cents": income_cents - expense_cents,
        "categories": categories,
    }


def band_transactions_payload(connection: sqlite3.Connection) -> list[dict[str, Any]]:
    """Return the band ledger with public attachment metadata grouped by row."""

    transactions = [
        dict(row)
        for row in connection.execute(
            """
            SELECT id, transaction_type, transaction_on, category, description, amount_cents,
                   is_cancelled, cancelled_at, cancelled_by_user_id, cancelled_by_username,
                   created_at, created_by, created_by_username
            FROM band_transactions
            ORDER BY transaction_on DESC, id DESC
            """
        ).fetchall()
    ]
    attachment_rows = connection.execute(
        """
        SELECT id, transaction_id, original_filename
        FROM band_transaction_attachments
        ORDER BY transaction_id, id
        """
    ).fetchall()
    attachments_by_transaction: dict[int, list[dict[str, Any]]] = defaultdict(list)
    for row in attachment_rows:
        attachments_by_transaction[int(row["transaction_id"])].append(
            {"id": int(row["id"]), "original_filename": str(row["original_filename"])}
        )
    for transaction in transactions:
        transaction["attachments"] = attachments_by_transaction[int(transaction["id"])]
    return transactions


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
                "sale_price_cents": int(label["sale_price_cents"]),
                "default_purchase_price_cents": int(label["default_purchase_price_cents"]),
                "no_reorder": bool(label["no_reorder"]),
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
    rows.sort(
        key=lambda item: (
            item["article_name"].casefold(),
            tuple(
                (
                    int(option.get("position", 9999)),
                    int(option.get("value_position", 9999)),
                    str(option.get("value", "")).casefold(),
                )
                for option in labels[int(item["variant_id"])].get("options", [])
            ),
            int(item["variant_id"]),
        )
    )
    reorder_rows = [row for row in rows if not row["no_reorder"]]
    obsolete_rows = [row for row in rows if row["no_reorder"]]

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
    #
    # Profit uses the weighted average of the purchase ledger for a variant.
    # If a variant has never been bought, its maintained standard purchase
    # price is the best available cost estimate.  This keeps rankings useful
    # for pre-orders without pretending that the current stock is consumed in
    # strict FIFO order.
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
    cost_basis_cte = """
        WITH cost_basis AS (
            SELECT v.id AS variant_id,
                   CASE WHEN COALESCE(SUM(p.quantity), 0) > 0
                        THEN CAST(ROUND(
                            CAST(SUM(p.quantity * p.unit_cost_cents) AS REAL) / SUM(p.quantity)
                        ) AS INTEGER)
                        ELSE v.default_purchase_price_cents
                   END AS unit_cost_cents
            FROM variants v
            LEFT JOIN purchases p ON p.variant_id = v.id
            GROUP BY v.id, v.default_purchase_price_cents
        )
    """
    top_revenue_items = [
        dict(row)
        for row in connection.execute(
            cost_basis_cte
            + """
            SELECT a.name AS label,
                   COALESCE(SUM(s.quantity), 0) AS quantity,
                   COALESCE(SUM(CASE WHEN s.is_paid = 1
                                     THEN s.amount_due_cents + s.donation_cents
                                     ELSE 0 END), 0) AS income_cents,
                   COALESCE(SUM(CASE WHEN s.is_paid = 1
                                     THEN s.amount_due_cents + s.donation_cents
                                          - s.quantity * cost_basis.unit_cost_cents
                                     ELSE 0 END), 0) AS profit_cents
            FROM sales s
            JOIN variants v ON v.id = s.variant_id
            JOIN articles a ON a.id = v.article_id
            JOIN cost_basis ON cost_basis.variant_id = s.variant_id
            WHERE s.is_cancelled = 0
            GROUP BY a.id, a.name
            ORDER BY income_cents DESC, profit_cents DESC, quantity DESC, a.name COLLATE NOCASE
            """
        ).fetchall()
    ]
    top_events = [
        dict(row)
        for row in connection.execute(
            cost_basis_cte
            + """
            SELECT COALESCE(NULLIF(TRIM(s.event_name), ''), 'Ohne Veranstaltung') AS label,
                   COALESCE(SUM(s.quantity), 0) AS quantity,
                   COALESCE(SUM(CASE WHEN s.is_paid = 1
                                     THEN s.amount_due_cents + s.donation_cents
                                     ELSE 0 END), 0) AS income_cents,
                   COALESCE(SUM(CASE WHEN s.is_paid = 1
                                     THEN s.amount_due_cents + s.donation_cents
                                          - s.quantity * cost_basis.unit_cost_cents
                                     ELSE 0 END), 0) AS profit_cents
            FROM sales s
            JOIN cost_basis ON cost_basis.variant_id = s.variant_id
            WHERE s.is_cancelled = 0
            GROUP BY COALESCE(NULLIF(TRIM(s.event_name), ''), 'Ohne Veranstaltung')
            ORDER BY income_cents DESC, profit_cents DESC, quantity DESC, label COLLATE NOCASE
            """
        ).fetchall()
    ]
    top_sellers = [
        dict(row)
        for row in connection.execute(
            cost_basis_cte
            + """
            SELECT COALESCE(NULLIF(TRIM(s.sold_by), ''), 'Nicht angegeben') AS label,
                   COALESCE(SUM(s.quantity), 0) AS quantity,
                   COALESCE(SUM(CASE WHEN s.is_paid = 1
                                     THEN s.amount_due_cents + s.donation_cents
                                     ELSE 0 END), 0) AS income_cents,
                   COALESCE(SUM(CASE WHEN s.is_paid = 1
                                     THEN s.amount_due_cents + s.donation_cents
                                          - s.quantity * cost_basis.unit_cost_cents
                                     ELSE 0 END), 0) AS profit_cents
            FROM sales s
            JOIN cost_basis ON cost_basis.variant_id = s.variant_id
            WHERE s.is_cancelled = 0
            GROUP BY COALESCE(NULLIF(TRIM(s.sold_by), ''), 'Nicht angegeben')
            ORDER BY income_cents DESC, profit_cents DESC, quantity DESC, label COLLATE NOCASE
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
    cash_balance_cents = total_collected + total_donation - total_purchase_cost
    band_finances = band_finance_summary_payload(connection)
    band_finances["overall_balance_cents"] = cash_balance_cents + band_finances["balance_cents"]
    return {
        "rows": rows,
        "reorder_rows": reorder_rows,
        "obsolete_rows": obsolete_rows,
        "summary": {
            "purchase_cost_cents": total_purchase_cost,
            "revenue_cents": total_revenue,
            "collected_cents": total_collected,
            "donation_cents": total_donation,
            "cash_balance_cents": cash_balance_cents,
            "outstanding_cents": int(outstanding_paid),
            "pending_delivery_count": int(pending_delivery),
            "stock_count": sum(row["stock"] for row in rows),
            "minimum_stock_warning_count": sum(1 for row in rows if row["minimum_stock_warning"]),
        },
        "band_finances": band_finances,
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
        USERS_DATABASE=os.environ.get("USERS_DATABASE", str(data_dir / "users.sqlite3")),
        # Local development may deliberately use ordinary SQLite.  It is
        # opt-in and never changes the secure production default.
        LOCAL_DEV_MODE=environment_flag("LOCAL_DEV_MODE"),
        DATABASE_ENCRYPTION_ENABLED=not environment_flag("LOCAL_DEV_MODE"),
        DATABASE_ENCRYPTION_METADATA=str(data_dir / "encryption.json"),
        BACKUP_DIR=str(data_dir / "backups"),
        RESET_ARCHIVE_DIR=str(data_dir / "reset-archives"),
        MIGRATION_ARCHIVE_DIR=str(data_dir / "migration-archives"),
        INVOICE_UPLOAD_DIR=str(data_dir / "invoices"),
        VARIANT_PHOTO_UPLOAD_DIR=str(data_dir / "variant-photos"),
        MAX_INVOICE_FILE_BYTES=MAX_INVOICE_FILE_BYTES,
        MAX_VARIANT_PHOTO_FILE_BYTES=MAX_VARIANT_PHOTO_FILE_BYTES,
        MAX_VARIANT_PHOTO_PIXELS=MAX_VARIANT_PHOTO_PIXELS,
        MAX_VARIANT_PHOTO_DIMENSION=MAX_VARIANT_PHOTO_DIMENSION,
        VARIANT_PHOTO_JPEG_QUALITY=VARIANT_PHOTO_JPEG_QUALITY,
        BACKUP_RETENTION_DAYS=int(os.environ.get("BACKUP_RETENTION_DAYS", "90")),
        ADMIN_USERNAME=os.environ.get("ADMIN_USERNAME", "admin"),
        ADMIN_PASSWORD=os.environ.get("ADMIN_PASSWORD", "replace-this-password"),
        ACCOUNT_SETUP_CODE_DAYS=int(os.environ.get("ACCOUNT_SETUP_CODE_DAYS", "14")),
        PROFILE_REAUTH_SECONDS=int(os.environ.get("PROFILE_REAUTH_SECONDS", "600")),
        DISPLAY_TIMEZONE=os.environ.get("DISPLAY_TIMEZONE", "Europe/Berlin").strip(),
        MFA_ISSUER=os.environ.get("MFA_ISSUER", "Protovibe Merch Manager").strip(),
        EMAIL_NOTIFICATIONS_ENABLED=environment_flag("EMAIL_NOTIFICATIONS_ENABLED"),
        SMTP_HOST=os.environ.get("SMTP_HOST", "").strip(),
        SMTP_PORT=os.environ.get("SMTP_PORT", "465").strip(),
        SMTP_SECURITY=os.environ.get("SMTP_SECURITY", "ssl").strip().lower(),
        SMTP_USERNAME=os.environ.get("SMTP_USERNAME", "").strip(),
        SMTP_PASSWORD=os.environ.get("SMTP_PASSWORD", ""),
        SMTP_FROM=(os.environ.get("SMTP_FROM") or os.environ.get("SMTP_USERNAME", "")).strip(),
        ADMIN_NOTIFICATION_EMAIL=os.environ.get("ADMIN_NOTIFICATION_EMAIL", "").strip(),
        SMTP_TIMEOUT_SECONDS=os.environ.get("SMTP_TIMEOUT_SECONDS", "8").strip(),
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
    if app.config.get("LOCAL_DEV_MODE") and (not test_config or "DATABASE_ENCRYPTION_ENABLED" not in test_config):
        app.config["DATABASE_ENCRYPTION_ENABLED"] = False
    # Existing regression tests deliberately exercise old, plaintext schemas.
    # A production app never receives TESTING=True and therefore always uses
    # SQLCipher unless a test explicitly chooses otherwise.
    if app.config.get("TESTING") and (not test_config or "DATABASE_ENCRYPTION_ENABLED" not in test_config):
        app.config["DATABASE_ENCRYPTION_ENABLED"] = False
    # Test instances and manual local installations often override only the
    # old DATABASE setting.  Keep the account file next to it unless the
    # caller deliberately configured a different USERS_DATABASE path.
    if not test_config or "USERS_DATABASE" not in test_config:
        database_parent = Path(app.config["DATABASE"]).parent
        app.config["USERS_DATABASE"] = str(database_parent / "users.sqlite3")
    if not test_config or "DATABASE_ENCRYPTION_METADATA" not in test_config:
        app.config["DATABASE_ENCRYPTION_METADATA"] = str(
            Path(app.config["DATABASE"]).parent / "encryption.json"
        )
    if not test_config or "VARIANT_PHOTO_UPLOAD_DIR" not in test_config:
        app.config["VARIANT_PHOTO_UPLOAD_DIR"] = str(Path(app.config["DATABASE"]).parent / "variant-photos")
    if not test_config or "MIGRATION_ARCHIVE_DIR" not in test_config:
        app.config["MIGRATION_ARCHIVE_DIR"] = str(Path(app.config["DATABASE"]).parent / "migration-archives")
    if version_tuple(str(app.config["APP_VERSION"])) is None:
        raise RuntimeError("APP_VERSION muss dem Format vX.Y.Z entsprechen, zum Beispiel v0.3.0.")
    if app.config["SECRET_KEY"] == "development-only-change-me" and not app.config.get("TESTING"):
        raise RuntimeError("Set SECRET_KEY in .env before starting the app.")
    if app.config.get("LOCAL_DEV_MODE"):
        app.logger.warning(
            "LOCAL_DEV_MODE is active: database/files are unencrypted and MFA is not enforced."
        )

    Path(app.config["DATABASE"]).parent.mkdir(parents=True, exist_ok=True)
    Path(app.config["USERS_DATABASE"]).parent.mkdir(parents=True, exist_ok=True)
    database_encryption_metadata_path(app).parent.mkdir(parents=True, exist_ok=True)
    Path(app.config["BACKUP_DIR"]).mkdir(parents=True, exist_ok=True)
    Path(app.config["RESET_ARCHIVE_DIR"]).mkdir(parents=True, exist_ok=True)
    Path(app.config["MIGRATION_ARCHIVE_DIR"]).mkdir(parents=True, exist_ok=True)
    Path(app.config["INVOICE_UPLOAD_DIR"]).mkdir(parents=True, exist_ok=True)
    Path(app.config["VARIANT_PHOTO_UPLOAD_DIR"]).mkdir(parents=True, exist_ok=True)
    if app.config.get("LOCAL_DEV_MODE"):
        # Fail with a useful message instead of letting sqlite3 report a
        # mysterious "file is not a database" error when somebody points the
        # local mode at an encrypted production volume.
        for database_file in (Path(app.config["DATABASE"]), Path(app.config["USERS_DATABASE"])):
            if database_file.is_file():
                with database_file.open("rb") as stream:
                    is_plain_sqlite = stream.read(16) == b"SQLite format 3\x00"
            else:
                is_plain_sqlite = True
            if not is_plain_sqlite:
                raise DatabaseEncryptionError(
                    "LOCAL_DEV_MODE benötigt einen separaten, leeren Datenordner. "
                    f"Die Datei {database_file.name} ist verschlüsselt oder kein normales SQLite."
                )
    app.extensions["database_encryption_lock"] = Lock()
    app.extensions["pending_database_recovery_keys"] = {}
    if database_encryption_enabled(app):
        # Fail closed. A production deployment may never silently fall back to
        # ordinary SQLite just because a native SQLCipher dependency is absent.
        _sqlcipher_dbapi()
    else:
        initialise_database(app)
    app.teardown_appcontext(close_db)

    @app.template_filter("money")
    def money_filter(value: int | None) -> str:
        return cents_to_money(value, language=user_ui_language(g.get("user")))

    @app.context_processor
    def inject_template_values() -> dict[str, Any]:
        language = user_ui_language(g.get("user"))
        theme = user_ui_theme(g.get("user"))
        return {
            "csrf_token": csrf_token,
            "current_user": g.get("user"),
            "pos_mode": bool(session.get("pos_mode")),
            "role_labels": ROLE_LABELS,
            "payment_methods": PAYMENT_METHODS,
            "app_version": app.config["APP_VERSION"],
            "app_version_label": display_version(app.config["APP_VERSION"]),
            "local_dev_mode": bool(app.config.get("LOCAL_DEV_MODE")),
            "ui_theme": theme,
            "ui_theme_color": USER_THEMES[theme]["theme_color"],
            "ui_language": language,
            "ui_locale": USER_LANGUAGES[language]["locale"],
            "ui_text": UI_TRANSLATIONS[language],
        }

    @app.before_request
    def enforce_database_encryption_state():
        """Keep every data endpoint closed until the encrypted store is unlocked."""

        if not database_encryption_enabled(app):
            return None
        allowed_endpoints = {
            "static",
            "service_worker",
            "encryption_setup",
            "encryption_unlock",
            "encryption_recovery",
            "encryption_legacy_data",
        }
        if request.endpoint in allowed_endpoints:
            return None
        try:
            state = database_encryption_state(app)
        except DatabaseEncryptionError as exc:
            return render_template("error.html", title="Verschlüsselungsfehler", message=str(exc)), 503
        if state == "unlocked":
            return None
        session.clear()
        if request.path.startswith("/api/"):
            return jsonify({"ok": False, "error": "Die Datenbank ist derzeit gesperrt."}), 423
        if state == "legacy":
            return redirect(url_for("encryption_legacy_data"))
        if state == "setup":
            return redirect(url_for("encryption_setup"))
        return redirect(url_for("encryption_unlock"))

    @app.before_request
    def load_request_context() -> None:
        require_csrf()
        user_id = session.get("user_id")
        g.user = None
        if database_encryption_enabled(app) and database_encryption_state(app) != "unlocked":
            return
        if user_id:
            user = row_to_dict(get_user_db().execute("SELECT * FROM users WHERE id = ?", (user_id,)).fetchone())
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

    @app.before_request
    def enforce_pos_mode_restrictions() -> None:
        """Keep management/accounting pages closed while POS mode is active."""

        if g.get("user") is None or not session.get("pos_mode"):
            return None
        if request.path in POS_MODE_RESTRICTED_API_PATHS or request.path.startswith(POS_MODE_RESTRICTED_PATH_PREFIXES):
            abort(403)
        if request.method in {"POST", "PATCH", "DELETE"} and request.path.startswith("/api/varianten"):
            abort(403)
        return None

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
            "encryption_setup",
            "encryption_unlock",
            "encryption_recovery",
        }:
            response.headers["Cache-Control"] = "no-store, max-age=0"
            response.headers["Pragma"] = "no-cache"
        return response

    @app.get("/")
    def index():
        return redirect(url_for("sales_page"))

    @app.route("/system/verschluesselung/einrichten", methods=["GET", "POST"])
    def encryption_setup():
        """Create the first encrypted datastore without placing its key in .env."""

        if not database_encryption_enabled(app):
            return redirect(url_for("login"))
        state = database_encryption_state(app)
        if state == "legacy":
            return redirect(url_for("encryption_legacy_data"))
        if state == "unlocked":
            return redirect(url_for("login"))
        if state == "setup_pending":
            return redirect(url_for("encryption_unlock"))
        try:
            username, _ = configured_bootstrap_admin(app)
        except DatabaseEncryptionError as exc:
            return render_template("error.html", title="Einrichtung nicht möglich", message=str(exc)), 503
        if request.method == "POST":
            try:
                with app.extensions["database_encryption_lock"]:
                    token, _ = setup_encrypted_databases(
                        app,
                        bootstrap_password=request.form.get("bootstrap_password"),
                        database_passphrase=request.form.get("database_passphrase"),
                        confirmation=request.form.get("database_passphrase_confirmation"),
                    )
                session.clear()
                session["pending_database_recovery_token"] = token
                return redirect(url_for("encryption_recovery"))
            except (ValueError, DatabaseEncryptionError) as exc:
                flash(str(exc), "error")
            except Exception:
                current_app.logger.exception("Could not initialise encrypted databases")
                flash("Die verschlüsselte Datenbank konnte nicht eingerichtet werden. Es wurden keine Daten überschrieben.", "error")
        return render_template(
            "encryption_setup.html",
            title="Verschlüsselung einrichten",
            bootstrap_username=username,
        )

    @app.route("/system/verschluesselung/recovery", methods=["GET", "POST"])
    def encryption_recovery():
        """Show the initial recovery key exactly while it remains in process memory."""

        token = session.get("pending_database_recovery_token")
        recovery_key = pending_database_recovery_key(app, token)
        if recovery_key is None:
            flash(
                "Der einmalige Wiederherstellungsschlüssel wird nicht mehr angezeigt. Bewahre mindestens die "
                "Datenbank-Passphrase sicher auf.",
                "error",
            )
            return redirect(url_for("login" if database_encryption_state(app) == "unlocked" else "encryption_unlock"))
        if request.method == "POST":
            discard_pending_database_recovery_key(app, token)
            session.pop("pending_database_recovery_token", None)
            if g.get("user") is not None:
                flash("Der neue Wiederherstellungsschlüssel wurde aktiviert. Der bisherige Schlüssel ist ungültig.", "success")
                return redirect(url_for("administration_page"))
            flash("Verschlüsselung eingerichtet. Melde dich nun mit dem Admin-Konto an und richte dessen 2FA ein.", "success")
            return redirect(url_for("login"))
        return render_template(
            "encryption_recovery.html",
            title="Wiederherstellungsschlüssel sichern",
            recovery_key=recovery_key,
            return_to_administration=bool(g.get("user")),
        )

    @app.route("/system/verschluesselung/entsperren", methods=["GET", "POST"])
    def encryption_unlock():
        """Unlock SQLCipher only in memory after each container/application restart."""

        if not database_encryption_enabled(app):
            return redirect(url_for("login"))
        state = database_encryption_state(app)
        if state == "legacy":
            return redirect(url_for("encryption_legacy_data"))
        if state == "setup":
            return redirect(url_for("encryption_setup"))
        if state == "unlocked":
            return redirect(url_for("login"))
        if request.method == "POST":
            try:
                with app.extensions["database_encryption_lock"]:
                    method = unlock_encrypted_databases(
                        app,
                        database_passphrase=request.form.get("database_passphrase"),
                        recovery_key=request.form.get("recovery_key"),
                    )
                session.clear()
                flash(
                    "Die Datenbank wurde mit {} entsperrt. Bitte melde dich an.".format(
                        "dem Wiederherstellungsschlüssel" if method == "recovery" else "der Datenbank-Passphrase"
                    ),
                    "success",
                )
                return redirect(url_for("login"))
            except (ValueError, DatabaseEncryptionError) as exc:
                flash(str(exc), "error")
            except Exception:
                current_app.logger.exception("Could not unlock encrypted databases")
                flash("Die Datenbank konnte nicht entsperrt werden.", "error")
        return render_template("encryption_unlock.html", title="Datenbank entsperren")

    @app.get("/system/verschluesselung/altdaten")
    def encryption_legacy_data():
        """Fail safely when a deployment still points at unencrypted files."""

        if not database_encryption_enabled(app):
            return redirect(url_for("login"))
        state = database_encryption_state(app)
        if state == "setup":
            return redirect(url_for("encryption_setup"))
        if state in {"locked", "setup_pending"}:
            return redirect(url_for("encryption_unlock"))
        if state == "unlocked":
            return redirect(url_for("login"))
        return render_template("encryption_legacy.html", title="Ungesicherte Altdaten gefunden")

    @app.post("/verwaltung/verschluesselung/passphrase")
    @login_required
    @admin_required
    def update_database_passphrase():
        """Let the authenticated Admin recover from a forgotten unlock passphrase."""

        if request.form.get("confirmation", "").strip() != "DATENBANK-PASSPHRASE ÄNDERN":
            flash("Bitte die Bestätigung exakt als „DATENBANK-PASSPHRASE ÄNDERN“ eingeben.", "error")
            return redirect(url_for("administration_page"))
        connection = get_user_db()
        try:
            admin = verify_admin_sensitive_action(
                connection,
                password=request.form.get("password"),
                mfa_code=request.form.get("mfa_code"),
                context="change_database_passphrase",
            )
            with app.extensions["database_encryption_lock"]:
                change_database_passphrase(
                    app,
                    passphrase=request.form.get("database_passphrase"),
                    confirmation=request.form.get("database_passphrase_confirmation"),
                )
            audit(connection, "change_database_passphrase", "system", None, {}, user_id=admin["id"])
            connection.commit()
            flash("Die Datenbank-Passphrase wurde geändert. Der bisherige Wiederherstellungsschlüssel bleibt gültig.", "success")
        except (ValueError, DatabaseEncryptionError) as exc:
            connection.rollback()
            flash(str(exc), "error")
        except Exception:
            connection.rollback()
            current_app.logger.exception("Could not change database passphrase")
            flash("Die Datenbank-Passphrase konnte nicht geändert werden.", "error")
        return redirect(url_for("administration_page"))

    @app.post("/verwaltung/verschluesselung/wiederherstellungsschluessel")
    @login_required
    @admin_required
    def renew_database_recovery_key():
        """Replace a possibly lost recovery key after password and MFA confirmation."""

        if request.form.get("confirmation", "").strip() != "WIEDERHERSTELLUNGSSCHLÜSSEL ERNEUERN":
            flash("Bitte die Bestätigung exakt als „WIEDERHERSTELLUNGSSCHLÜSSEL ERNEUERN“ eingeben.", "error")
            return redirect(url_for("administration_page"))
        connection = get_user_db()
        try:
            admin = verify_admin_sensitive_action(
                connection,
                password=request.form.get("password"),
                mfa_code=request.form.get("mfa_code"),
                context="renew_database_recovery_key",
            )
            with app.extensions["database_encryption_lock"]:
                token, _ = regenerate_database_recovery_key(app)
            audit(connection, "renew_database_recovery_key", "system", None, {}, user_id=admin["id"])
            connection.commit()
            session["pending_database_recovery_token"] = token
            return redirect(url_for("encryption_recovery"))
        except (ValueError, DatabaseEncryptionError) as exc:
            connection.rollback()
            flash(str(exc), "error")
        except Exception:
            connection.rollback()
            current_app.logger.exception("Could not renew database recovery key")
            flash("Der Wiederherstellungsschlüssel konnte nicht erneuert werden.", "error")
        return redirect(url_for("administration_page"))

    @app.get("/service-worker.js")
    def service_worker():
        """Expose the worker at the origin root so it can cache /verkauf."""

        response = send_from_directory(app.static_folder, "service-worker.js", mimetype="application/javascript")
        # A browser must revalidate this small loader on each visit; the worker
        # itself controls versioned asset caches after it has been updated.
        response.headers["Cache-Control"] = "no-cache"
        response.headers["Service-Worker-Allowed"] = "/"
        return response

    @app.route("/login", methods=["GET", "POST"])
    def login():
        if request.method == "POST":
            username = request.form.get("username", "").strip()
            password_or_setup_code = request.form.get("password", "")
            user = get_user_db().execute("SELECT * FROM users WHERE username = ?", (username,)).fetchone()
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
                if not current_app.config.get("LOCAL_DEV_MODE") and normalized_role(user) == "admin" and not effective_mfa_enabled(user):
                    begin_auth_challenge("mfa_enrollment", user, request.args.get("next"))
                    return redirect(url_for("mfa_enroll"))
                if effective_mfa_enabled(user):
                    begin_auth_challenge("mfa_login", user, request.args.get("next"))
                    return redirect(url_for("mfa_login"))
                get_user_db().execute("UPDATE users SET last_login_at = ? WHERE id = ?", (utc_now(), user["id"]))
                get_user_db().commit()
                next_url = safe_next_url(request.args.get("next"), fallback=url_for("sales_page"))
                establish_authenticated_session(user)
                return redirect(next_url)
        return render_template("login.html", title="Anmelden")

    @app.route("/konto/einrichten", methods=["GET", "POST"])
    def account_setup():
        """Turn an admin-issued, one-time setup code into a private password."""

        user_id = session.get("password_setup_user_id")
        user = (
            get_user_db().execute("SELECT * FROM users WHERE id = ?", (user_id,)).fetchone()
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
                connection = get_user_db()
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
                if not current_app.config.get("LOCAL_DEV_MODE") and normalized_role(refreshed_user) == "admin" and not effective_mfa_enabled(refreshed_user):
                    begin_auth_challenge("mfa_enrollment", refreshed_user, next_url)
                    return redirect(url_for("mfa_enroll"))
                if effective_mfa_enabled(refreshed_user):
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

        if current_app.config.get("LOCAL_DEV_MODE"):
            session.clear()
            return redirect(url_for("login"))

        user_id = session.get("mfa_login_user_id")
        user = (
            get_user_db().execute("SELECT * FROM users WHERE id = ?", (user_id,)).fetchone()
            if user_id
            else None
        )
        if user is None or not bool(user["is_active"]) or not effective_mfa_enabled(user):
            session.clear()
            flash("Die Zwei-Faktor-Anmeldung ist nicht mehr gültig. Bitte erneut anmelden.", "error")
            return redirect(url_for("login"))
        if request.method == "POST":
            connection = get_user_db()
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
            return get_user_db().execute("SELECT * FROM users WHERE id = ?", (g.user["id"],)).fetchone(), False
        user_id = session.get("mfa_enrollment_user_id")
        if not user_id:
            return None, False
        user = get_user_db().execute("SELECT * FROM users WHERE id = ?", (user_id,)).fetchone()
        if user is None or not bool(user["is_active"]) or normalized_role(user) != "admin":
            return None, False
        return user, True

    @app.route("/profil/2fa/einrichten", methods=["GET", "POST"])
    @app.route("/mfa/einrichten", methods=["GET", "POST"])
    def mfa_enroll():
        """Show a QR code and only enable TOTP after a live-code confirmation."""

        if current_app.config.get("LOCAL_DEV_MODE"):
            return redirect(url_for("profile_page" if g.get("user") is not None else "login"))

        user, is_pre_auth = mfa_enrollment_target()
        if user is None:
            if g.get("user") is not None:
                return redirect(url_for("profile_reauth", next=request.path))
            session.clear()
            flash("Die Zwei-Faktor-Einrichtung ist nicht mehr gültig. Bitte erneut anmelden.", "error")
            return redirect(url_for("login"))
        connection = get_user_db()
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
        # POS mode deliberately requires an actual fresh password entry to
        # leave it; a previously cached profile confirmation is not enough.
        if has_profile_reauth(g.user) and not session.get("pos_mode"):
            return redirect(target)
        if request.method == "POST":
            password = request.form.get("password", "")
            connection = get_user_db()
            user = connection.execute("SELECT * FROM users WHERE id = ?", (g.user["id"],)).fetchone()
            if not check_password_hash(user["password_hash"], password):
                flash("Das Passwort ist nicht korrekt.", "error")
            else:
                method = "password"
                if effective_mfa_enabled(user):
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
                # A fresh password confirmation is the explicit way back out
                # of the restricted counter workflow without logging out.
                session.pop("pos_mode", None)
                session["profile_reauth_user_id"] = int(user["id"])
                session["profile_reauth_until"] = time.time() + int(current_app.config["PROFILE_REAUTH_SECONDS"])
                return redirect(target)
        return render_template(
            "profile_reauth.html",
            title="Zugriff bestätigen",
            target=target,
            needs_mfa=effective_mfa_enabled(g.user),
        )

    @app.get("/profil")
    @login_required
    @profile_reauth_required
    def profile_page():
        user = get_user_db().execute("SELECT * FROM users WHERE id = ?", (g.user["id"],)).fetchone()
        profile_user = user_capabilities(dict(user))
        return render_template(
            "profile.html",
            title=UI_TRANSLATIONS[user_ui_language(profile_user)]["profile.title"],
            profile_user=profile_user,
            recovery_code_count=len(recovery_code_hashes(user)),
            ui_themes=USER_THEMES,
            ui_languages=USER_LANGUAGES,
        )

    @app.post("/profil/personalisierung")
    @login_required
    @profile_reauth_required
    def update_own_personalization():
        """Persist display preferences without changing any shared catalogue data."""

        connection = get_user_db()
        try:
            theme = valid_ui_theme(request.form.get("ui_theme"))
            language = valid_ui_language(request.form.get("ui_language"))
            show_variant_photos = 1 if request.form.get("show_variant_photos") else 0
            connection.execute(
                """
                UPDATE users
                SET ui_theme = ?, ui_language = ?, show_variant_photos = ?
                WHERE id = ?
                """,
                (theme, language, show_variant_photos, g.user["id"]),
            )
            audit(
                connection,
                "update_personalization",
                "user",
                g.user["id"],
                {
                    "ui_theme": theme,
                    "ui_language": language,
                    "show_variant_photos": bool(show_variant_photos),
                },
            )
            connection.commit()
            flash(UI_TRANSLATIONS[language]["profile.personalization_saved"], "success")
        except ValueError as exc:
            connection.rollback()
            flash(str(exc), "error")
        return redirect(url_for("profile_page"))

    @app.post("/profil/passwort")
    @login_required
    @profile_reauth_required
    def update_own_password():
        try:
            connection = get_user_db()
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

        connection = get_user_db()
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
        connection = get_user_db()
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
        flash("Die Zwei-Faktor-Authentifizierung wurde deaktiviert.", "success")
        return redirect(url_for("profile_page"))

    @app.post("/profil/2fa/wiederherstellungscodes")
    @login_required
    @profile_reauth_required
    def regenerate_recovery_codes():
        connection = get_user_db()
        user = connection.execute("SELECT * FROM users WHERE id = ?", (g.user["id"],)).fetchone()
        if not effective_mfa_enabled(user):
            flash("Aktiviere zuerst die Zwei-Faktor-Authentifizierung.", "error")
            return redirect(url_for("profile_page"))
        recovery_codes = generate_recovery_codes()
        connection.execute(
            "UPDATE users SET mfa_recovery_code_hashes_json = ? WHERE id = ?",
            (json.dumps([generate_password_hash(item) for item in recovery_codes]), user["id"]),
        )
        audit(connection, "regenerate_recovery_codes", "user", user["id"], {})
        connection.commit()
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

    @app.post("/pos-modus")
    @login_required
    def toggle_pos_mode():
        """Enter POS mode, or require a fresh password to leave it."""

        if session.get("pos_mode"):
            destination = safe_next_url(request.form.get("next"), fallback=url_for("sales_page"))
            return redirect(url_for("profile_reauth", next=destination))
        session["pos_mode"] = True
        return redirect(url_for("sales_page"))

    @app.post("/admin-nachricht")
    @login_required
    def send_admin_message():
        """Persist an authenticated user's issue or question for the admin."""

        language = user_ui_language(g.user)
        strings = UI_TRANSLATIONS[language]
        destination = safe_next_url(request.form.get("next"), fallback=url_for("sales_page"))
        message_type = str(request.form.get("message_type", "")).strip()
        subject = str(request.form.get("subject", "")).strip()
        body = str(request.form.get("body", "")).strip()
        if message_type not in {"issue", "question"}:
            flash(strings["message.invalid_type"], "error")
            return redirect(destination)
        if not subject or len(subject) > 120:
            flash(strings["message.subject_required"], "error")
            return redirect(destination)
        if not body or len(body) > 4_000:
            flash(strings["message.body_required"], "error")
            return redirect(destination)
        try:
            sender_email = valid_email_address(request.form.get("sender_email"), field_name="Die E-Mail-Adresse")
        except ValueError as exc:
            flash(str(exc), "error")
            return redirect(destination)

        connection = get_user_db()
        created_at = utc_now()
        persisted_message: dict[str, Any] | None = None
        try:
            connection.execute("BEGIN IMMEDIATE")
            cursor = connection.execute(
                """
                INSERT INTO admin_messages (
                    sender_user_id, sender_username, sender_email, message_type, subject, body, created_at
                ) VALUES (?, ?, ?, ?, ?, ?, ?)
                """,
                (
                    g.user["id"],
                    g.user["username"],
                    sender_email,
                    message_type,
                    subject,
                    body,
                    created_at,
                ),
            )
            persisted_message = {
                "id": int(cursor.lastrowid),
                "sender_username": str(g.user["username"]),
                "sender_email": sender_email,
                "message_type": message_type,
                "subject": subject,
                "body": body,
                "created_at": created_at,
            }
            audit(
                connection,
                "send_admin_message",
                "admin_message",
                int(cursor.lastrowid),
                {"message_type": message_type, "subject": subject},
            )
            connection.commit()
        except sqlite3.DatabaseError:
            connection.rollback()
            current_app.logger.exception("Could not store admin message")
            flash(strings["message.save_failed"], "error")
            return redirect(destination)

        flash(strings["message.sent"], "success")
        notification_config = smtp_notification_config(connection, current_app)
        if notification_config.get("EMAIL_NOTIFICATIONS_ENABLED") and persisted_message is not None:
            try:
                send_admin_message_email(notification_config, persisted_message)
            except (ValueError, OSError, smtplib.SMTPException):
                current_app.logger.exception(
                    "Admin message %s was stored, but its email notification failed",
                    persisted_message["id"],
                )
        return redirect(destination)

    def administration_users() -> list[dict[str, Any]]:
        """Return safe, display-ready user records for the admin screen."""

        rows = get_user_db().execute(
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

    def administration_messages() -> list[dict[str, Any]]:
        """Return the private inbox exposed only through the admin view."""

        return [
            dict(row)
            for row in get_user_db().execute(
                """
                SELECT id, sender_user_id, sender_username, sender_email, message_type, subject, body,
                       created_at, is_resolved, resolved_at, resolved_by_user_id, resolved_by_username
                FROM admin_messages
                ORDER BY is_resolved ASC, created_at DESC, id DESC
                """
            ).fetchall()
        ]

    def render_administration(
        *,
        setup_credential: dict[str, str] | None = None,
        reset_archive_name: str | None = None,
    ):
        notification_config = smtp_notification_config(get_user_db(), app)
        return render_template(
            "admin.html",
            title="Verwaltung",
            users=administration_users(),
            admin_messages=administration_messages(),
            backups=operational_backup_points(app),
            setup_credential=setup_credential,
            reset_archive_name=reset_archive_name,
            setup_code_days=int(current_app.config["ACCOUNT_SETUP_CODE_DAYS"]),
            database_encryption_active=database_encryption_enabled(app),
            email_notification=smtp_notification_status(notification_config),
            email_settings=smtp_notification_settings_public(notification_config),
        )

    @app.get("/verwaltung")
    @login_required
    @admin_required
    def administration_page():
        return render_administration()

    @app.post("/verwaltung/email/test")
    @login_required
    @admin_required
    def test_admin_email_notification():
        """Let the admin verify SMTP without exposing account credentials."""

        notification_config = smtp_notification_config(get_user_db(), app)
        status = smtp_notification_status(notification_config)
        if not status["ready"]:
            details = [*status["missing"], *status["errors"]]
            flash(
                "Test-E-Mail nicht gesendet: "
                + (", ".join(details) if details else "E-Mail-Benachrichtigungen sind deaktiviert."),
                "error",
            )
            return redirect(url_for("administration_page"))
        try:
            send_smtp_notification(
                notification_config,
                subject="[Merch Manager] SMTP-Test",
                body=(
                    "Die SMTP-Konfiguration des Protovibe Merch Managers funktioniert.\n\n"
                    f"Ausgelöst von: {g.user['username']}\n"
                    f"Zeitpunkt: {utc_now()}\n"
                ),
            )
        except (ValueError, OSError, smtplib.SMTPException):
            current_app.logger.exception("Could not send SMTP test notification")
            flash(
                "Die Test-E-Mail konnte nicht gesendet werden. Details stehen im Server-Log.",
                "error",
            )
        else:
            flash(f"Test-E-Mail an {status['recipient']} wurde gesendet.", "success")
        return redirect(url_for("administration_page"))

    @app.post("/verwaltung/nachrichten/<int:message_id>/status")
    @login_required
    @admin_required
    def update_admin_message_resolution(message_id: int):
        """Mark a private inbox item done or reopen it without deleting it."""

        is_resolved = request.form.get("is_resolved") == "1"
        connection = get_user_db()
        if connection.execute("SELECT id FROM admin_messages WHERE id = ?", (message_id,)).fetchone() is None:
            abort(404)
        try:
            connection.execute("BEGIN IMMEDIATE")
            if is_resolved:
                connection.execute(
                    """
                    UPDATE admin_messages
                    SET is_resolved = 1, resolved_at = ?, resolved_by_user_id = ?, resolved_by_username = ?
                    WHERE id = ?
                    """,
                    (utc_now(), g.user["id"], g.user["username"], message_id),
                )
            else:
                connection.execute(
                    """
                    UPDATE admin_messages
                    SET is_resolved = 0, resolved_at = NULL, resolved_by_user_id = NULL, resolved_by_username = NULL
                    WHERE id = ?
                    """,
                    (message_id,),
                )
            audit(
                connection,
                "resolve_admin_message" if is_resolved else "reopen_admin_message",
                "admin_message",
                message_id,
                {"is_resolved": is_resolved},
            )
            connection.commit()
        except sqlite3.DatabaseError:
            connection.rollback()
            current_app.logger.exception("Could not update admin message resolution")
            flash("Der Nachrichtenstatus konnte nicht gespeichert werden.", "error")
        else:
            flash("Nachricht als erledigt markiert." if is_resolved else "Nachricht wieder geöffnet.", "success")
        return redirect(url_for("administration_page"))

    @app.post("/verwaltung/email/einstellungen")
    @login_required
    @admin_required
    def save_admin_email_notification_settings():
        """Persist SMTP credentials encrypted after a fresh admin confirmation."""

        connection = get_user_db()
        try:
            enabled = 1 if request.form.get("enabled") else 0
            host = str(request.form.get("host", "")).strip()
            username = str(request.form.get("username", "")).strip()
            sender_address = str(request.form.get("sender_address", "")).strip()
            recipient_address = str(request.form.get("recipient_address", "")).strip()
            security = str(request.form.get("security", "ssl")).strip().lower()
            if len(host) > 255 or len(username) > 254:
                raise ValueError("SMTP-Server und Benutzername dürfen höchstens 254 Zeichen lang sein.")
            if security not in {"ssl", "starttls"}:
                raise ValueError("Bitte SSL oder STARTTLS auswählen.")
            try:
                port = int(request.form.get("port", ""))
                timeout_seconds = float(request.form.get("timeout_seconds", ""))
            except (TypeError, ValueError) as exc:
                raise ValueError("SMTP-Port und Zeitlimit müssen Zahlen sein.") from exc
            if not 1 <= port <= 65_535 or not 0 < timeout_seconds <= 60:
                raise ValueError("SMTP-Port oder Zeitlimit liegt außerhalb des erlaubten Bereichs.")
            if sender_address:
                sender_address = valid_email_address(sender_address, field_name="Absenderadresse")
            if recipient_address:
                recipient_address = valid_email_address(recipient_address, field_name="Empfängeradresse")
            new_password = str(request.form.get("password", ""))
            if len(new_password) > 2_000:
                raise ValueError("Das SMTP-Passwort ist zu lang.")

            existing = connection.execute(
                "SELECT password_encrypted FROM smtp_notification_settings WHERE id = 1"
            ).fetchone()
            password_encrypted = existing["password_encrypted"] if existing is not None else None
            if new_password:
                password_encrypted = encrypt_smtp_password(new_password, app)
            if request.form.get("clear_password"):
                password_encrypted = None

            connection.execute("BEGIN IMMEDIATE")
            verify_admin_sensitive_action(
                connection,
                password=request.form.get("current_password"),
                mfa_code=request.form.get("mfa_code"),
                context="save_smtp_notification_settings",
            )
            connection.execute(
                """
                INSERT INTO smtp_notification_settings (
                    id, enabled, host, port, security, username, password_encrypted, sender_address,
                    recipient_address, timeout_seconds, updated_at, updated_by_user_id, updated_by_username
                ) VALUES (1, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
                ON CONFLICT(id) DO UPDATE SET
                    enabled = excluded.enabled, host = excluded.host, port = excluded.port,
                    security = excluded.security, username = excluded.username,
                    password_encrypted = excluded.password_encrypted, sender_address = excluded.sender_address,
                    recipient_address = excluded.recipient_address, timeout_seconds = excluded.timeout_seconds,
                    updated_at = excluded.updated_at, updated_by_user_id = excluded.updated_by_user_id,
                    updated_by_username = excluded.updated_by_username
                """,
                (
                    enabled, host, port, security, username, password_encrypted, sender_address,
                    recipient_address, timeout_seconds, utc_now(), g.user["id"], g.user["username"],
                ),
            )
            audit(connection, "save_smtp_notification_settings", "smtp_notification_settings", 1, {"enabled": bool(enabled)})
            connection.commit()
        except (ValueError, sqlite3.DatabaseError) as exc:
            connection.rollback()
            flash("E-Mail-Einstellungen konnten nicht gespeichert werden: " + str(exc), "error")
        else:
            flash("E-Mail-Einstellungen wurden verschlüsselt gespeichert.", "success")
        return redirect(url_for("administration_page"))

    @app.post("/verwaltung/benutzer")
    @login_required
    @admin_required
    def create_user():
        try:
            username = valid_username(request.form.get("username"))
            role = str(request.form.get("role", "seller")).strip().lower()
            if role not in MANAGED_USER_ROLES:
                raise ValueError("Neue Benutzer können nur die Rollen Seller, Member oder Manager erhalten.")
            setup_code = generate_setup_code()
            connection = get_user_db()
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
        connection = get_user_db()
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
        return render_administration(
            setup_credential={"username": user["username"], "code": setup_code, "purpose": "reset"}
        )

    @app.post("/verwaltung/benutzer/<int:user_id>/rolle")
    @login_required
    @admin_required
    def update_user_role(user_id: int):
        role = str(request.form.get("role", "")).strip().lower()
        if role not in MANAGED_USER_ROLES:
            flash("Es sind nur die Rollen Seller, Member und Manager auswählbar.", "error")
            return redirect(url_for("administration_page"))
        connection = get_user_db()
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
        flash("Die Rolle von „{}“ wurde geändert.".format(user["username"]), "success")
        return redirect(url_for("administration_page"))

    @app.post("/verwaltung/benutzer/<int:user_id>/aktiv")
    @login_required
    @admin_required
    def update_user_active_state(user_id: int):
        connection = get_user_db()
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
        flash("Der Benutzer wurde {}.".format("aktiviert" if active else "deaktiviert"), "success")
        return redirect(url_for("administration_page"))

    @app.post("/verwaltung/benutzer/<int:user_id>/2fa-zuruecksetzen")
    @login_required
    @admin_required
    def reset_user_mfa(user_id: int):
        connection = get_user_db()
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
        flash("Die 2FA von „{}“ wurde zurückgesetzt.".format(user["username"]), "success")
        return redirect(url_for("administration_page"))

    @app.post("/verwaltung/benutzer/<int:user_id>/loeschen")
    @login_required
    @admin_required
    def delete_user(user_id: int):
        """Remove a non-admin account without changing historic bookings."""

        if request.form.get("confirmation", "").strip() != "BENUTZER LÖSCHEN":
            flash("Bitte die Bestätigung exakt als „BENUTZER LÖSCHEN“ eingeben.", "error")
            return redirect(url_for("administration_page"))
        connection = get_user_db()
        user = connection.execute("SELECT * FROM users WHERE id = ?", (user_id,)).fetchone()
        if user is None:
            abort(404)
        if int(user["id"]) == int(g.user["id"]) or normalized_role(user) == "admin":
            flash("Das eigene oder das einzige Admin-Konto kann nicht gelöscht werden.", "error")
            return redirect(url_for("administration_page"))
        try:
            admin = verify_admin_sensitive_action(
                connection,
                password=request.form.get("password"),
                mfa_code=request.form.get("mfa_code"),
                context="delete_user",
            )
            connection.execute("DELETE FROM users WHERE id = ?", (user_id,))
            audit(
                connection,
                "delete",
                "user",
                user_id,
                {
                    "username": user["username"],
                    "role": normalized_role(user),
                    "performed_by": admin["username"],
                    "historic_bookings_preserved": True,
                },
            )
            connection.commit()
        except ValueError as exc:
            flash(str(exc), "error")
            return redirect(url_for("administration_page"))
        flash(
            "Das Konto „{}“ wurde gelöscht. Historische Buchungen bleiben unverändert erhalten.".format(
                user["username"]
            ),
            "success",
        )
        return redirect(url_for("administration_page"))

    @app.post("/verwaltung/daten-zuruecksetzen")
    @login_required
    @admin_required
    def reset_application_data():
        """Archive and reset only the operational ledger after fresh MFA."""

        if request.form.get("confirmation", "").strip() != "DATEN ZURÜCKSETZEN":
            flash("Bitte die Bestätigung exakt als „DATEN ZURÜCKSETZEN“ eingeben.", "error")
            return render_administration()
        connection = get_user_db()
        try:
            admin = verify_admin_sensitive_action(
                connection,
                password=request.form.get("password"),
                mfa_code=request.form.get("mfa_code"),
                context="data_reset",
            )
        except ValueError as exc:
            flash(str(exc), "error")
            return render_administration()
        try:
            archive_path = create_reset_archive(app, get_db())
            close_operational_db()
            reset_data_store(app)
            fresh_connection = get_db()
            audit(
                fresh_connection,
                "reset_application_data",
                "system",
                None,
                {"archive": archive_path.name, "preserved_admin": admin["username"]},
                user_id=admin["id"],
            )
            fresh_connection.commit()
        except Exception:
            current_app.logger.exception("Could not reset application data")
            flash("Die Daten konnten nicht zurückgesetzt werden. Das Reset-Archiv wurde nicht gelöscht.", "error")
            return redirect(url_for("administration_page"))
        session.clear()
        flash(
            "Artikel, Buchungen und Anhänge wurden zurückgesetzt. Benutzerkonten, Rollen und 2FA bleiben erhalten. "
            f"Das Archiv „{archive_path.name}“ wurde angelegt.",
            "success",
        )
        return redirect(url_for("login"))

    @app.post("/verwaltung/backups/wiederherstellen")
    @login_required
    @admin_required
    def restore_application_backup():
        """Restore an explicitly selected operational backup after fresh MFA."""

        if request.form.get("confirmation", "").strip() != "SICHERUNG WIEDERHERSTELLEN":
            flash("Bitte die Bestätigung exakt als „SICHERUNG WIEDERHERSTELLEN“ eingeben.", "error")
            return redirect(url_for("administration_page"))
        try:
            preview_backup = selected_operational_backup(app, request.form.get("backup_name"))
            validate_operational_snapshot(app, preview_backup / "merch.sqlite3")
        except ValueError as exc:
            flash(str(exc), "error")
            return redirect(url_for("administration_page"))
        connection = get_user_db()
        try:
            admin = verify_admin_sensitive_action(
                connection,
                password=request.form.get("password"),
                mfa_code=request.form.get("mfa_code"),
                context="restore_operational_backup",
            )
            restored_backup, safety_backup = restore_operational_backup(
                app, request.form.get("backup_name")
            )
            restored_connection = get_db()
            audit(
                restored_connection,
                "restore_operational_backup",
                "system",
                None,
                {
                    "restored_backup": restored_backup.name,
                    "safety_backup": safety_backup.name,
                    "performed_by": admin["username"],
                },
                user_id=admin["id"],
            )
            restored_connection.commit()
        except ValueError as exc:
            flash(str(exc), "error")
            return redirect(url_for("administration_page"))
        except Exception:
            current_app.logger.exception("Could not restore operational backup")
            flash("Die Sicherung konnte nicht wiederhergestellt werden. Die aktuelle Sicherheitskopie bleibt erhalten.", "error")
            return redirect(url_for("administration_page"))
        flash(
            "Die Betriebsdaten aus „{}“ wurden wiederhergestellt. Die vorherigen Daten liegen zusätzlich in „{}“. "
            "Benutzerkonten und 2FA wurden nicht verändert.".format(restored_backup.name, safety_backup.name),
            "success",
        )
        return redirect(url_for("administration_page"))

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
        connection = get_db()
        show_variant_photos = user_shows_variant_photos(g.user)
        event_catalogue = sale_event_catalogue(connection)
        return render_template(
            "sales.html",
            title="Verkauf",
            articles=article_payload(
                connection, offered_only=True, include_variant_photos=show_variant_photos
            ),
            show_variant_photos=show_variant_photos,
            sale_events=event_catalogue["events"],
            current_sale_event_id=event_catalogue["current_event_id"],
            today=today_iso(),
        )

    @app.get("/api/sale-events")
    @login_required
    def sale_events_api():
        """Expose the shared event catalogue and the current global default."""

        return jsonify({"ok": True, **sale_event_catalogue(get_db())})

    @app.post("/api/sale-events")
    @login_required
    def create_sale_event_api():
        """Create/select one global event from the compact sales dialog."""

        payload = request.get_json(silent=True) or {}
        if not isinstance(payload, dict):
            return jsonify({"ok": False, "error": "Ungültige Veranstaltungsdaten."}), 400
        connection = get_db()
        try:
            name = normalise_sale_event_name(
                payload.get("name"), max_length=MAX_SALE_EVENT_NAME_LENGTH
            )
            if name is None:
                raise ValueError("Bitte einen Namen für die Veranstaltung eingeben.")
            connection.execute("BEGIN IMMEDIATE")
            event = create_sale_event(connection, name)
            audit(connection, "create", "sale_event", int(event["id"]), {"name": event["name"]})
            connection.commit()
            backup_after_commit()
            return jsonify({"ok": True, "event": event, **sale_event_catalogue(connection)}), 201
        except ValueError as exc:
            connection.rollback()
            return jsonify({"ok": False, "error": str(exc)}), 400
        except Exception:
            connection.rollback()
            current_app.logger.exception("Could not create sale event")
            return jsonify({"ok": False, "error": "Die Veranstaltung konnte nicht gespeichert werden."}), 500

    @app.post("/api/sale-events/<int:event_id>/select")
    @login_required
    def select_sale_event_api(event_id: int):
        """Set one existing event as the global sales-page default."""

        connection = get_db()
        try:
            connection.execute("BEGIN IMMEDIATE")
            event = select_sale_event(connection, event_id)
            audit(connection, "select", "sale_event", event_id, {"name": event["name"]})
            connection.commit()
            backup_after_commit()
            return jsonify({"ok": True, "event": event, **sale_event_catalogue(connection)})
        except LookupError as exc:
            connection.rollback()
            return jsonify({"ok": False, "error": str(exc)}), 404
        except Exception:
            connection.rollback()
            current_app.logger.exception("Could not select sale event")
            return jsonify({"ok": False, "error": "Die Veranstaltung konnte nicht ausgewählt werden."}), 500

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
            sync_event = offline_sync_event(payload, int(g.user["id"]))
            # A duplicate must be recognized before current article rules are
            # evaluated: a valid historic offline sale remains idempotent even
            # if somebody withdrew its variant before the browser retried.
            # The lock also closes the race between two simultaneous retries.
            if sync_event is not None:
                connection.execute("BEGIN IMMEDIATE")
                duplicate_response = duplicate_sync_event_response(connection, sync_event)
                if duplicate_response is not None:
                    connection.rollback()
                    return jsonify(duplicate_response)
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
            comment = str(payload.get("comment", "")).strip() or None

            if sync_event is None:
                connection.execute("BEGIN IMMEDIATE")
            event_name = event_name_for_sale_payload(connection, payload)
            # The inventory is a ledger, not a hard sales lock: a missed
            # purchase entry or a later shipment must not prevent the merch
            # stand from recording a real sale.  Holding the write lock gives
            # the response authoritative post-sale stock values for every
            # basket line.
            receipt_id = unique_receipt_id(connection, "V", payload.get("receipt_id"), sold_on)
            created_at = utc_now()
            for item in basket_items:
                cursor = connection.execute(
                    """
                    INSERT INTO sales (
                        receipt_id, variant_id, quantity, unit_price_cents, amount_due_cents,
                        amount_given_cents, donation_cents, payment_method, is_paid, payment_follow_up, is_received,
                        delivery_status, customer_name, customer_address, event_name, sold_by, comment,
                        sold_on, created_at, created_by, created_by_username
                    ) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
                    """,
                    (
                        receipt_id, item["variant_id"], item["quantity"], item["unit_price_cents"],
                        item["amount_due_cents"], item["amount_given_cents"], item["donation_cents"],
                        payment_method, int(is_paid), payment_follow_up, int(is_received), delivery_status,
                        customer_name or None, customer_address or None, event_name, sold_by or None,
                        comment, sold_on, created_at, g.user["id"], g.user["username"],
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
                        "offline_event_id": sync_event["event_id"] if sync_event else None,
                    },
                )
            for item in basket_items:
                item["stock_after_sale"] = stock_for_variant(connection, item["variant_id"])
            first_item = basket_items[0]
            response_payload = {
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
            if sync_event is not None:
                connection.execute(
                    """
                    INSERT INTO sync_events (
                        event_id, event_type, actor_user_id, actor_username, device_id,
                        payload_hash, client_created_at, response_json, created_at
                    ) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)
                    """,
                    (
                        sync_event["event_id"],
                        sync_event["event_type"],
                        sync_event["actor_user_id"],
                        g.user["username"],
                        sync_event["device_id"],
                        sync_event["payload_hash"],
                        sync_event["client_created_at"],
                        json.dumps(response_payload, ensure_ascii=False, separators=(",", ":")),
                        utc_now(),
                    ),
                )
            connection.commit()
            backup_after_commit()
            return jsonify(response_payload)
        except SyncEventConflict as exc:
            connection.rollback()
            return jsonify({"ok": False, "error": str(exc)}), 409
        except (ValueError, TypeError) as exc:
            connection.rollback()
            return jsonify({"ok": False, "error": str(exc)}), 400
        except Exception:
            connection.rollback()
            current_app.logger.exception("Could not create sale")
            return jsonify({"ok": False, "error": "Der Kauf konnte nicht gespeichert werden."}), 500

    @app.get("/einkaeufe")
    @login_required
    @member_required
    def purchases_page():
        connection = get_db()
        purchase_rows = connection.execute("SELECT * FROM purchases ORDER BY purchased_on DESC, id DESC").fetchall()
        return render_template(
            "purchases.html",
            title="Einkäufe",
            articles=article_payload(connection),
            receipts=purchase_receipt_payload(
                connection, purchase_rows, timezone_name=app.config["DISPLAY_TIMEZONE"]
            ),
            today=today_iso(),
            can_manage_purchases=has_role(g.user, "manager"),
        )

    @app.route("/band-finanzen", methods=["GET", "POST"])
    @login_required
    @member_required
    def band_finances_page():
        """Show the shared band ledger; managers may append immutable entries."""

        connection = get_db()
        can_manage_band_finances = has_role(g.user, "manager")

        def render_band_finances() -> str:
            return render_template(
                "band_finances.html",
                title="Band-Ein- und Ausgaben",
                band_finance_summary=band_finance_summary_payload(connection),
                band_transactions=band_transactions_payload(connection),
                today=today_iso(),
                can_manage_band_finances=can_manage_band_finances,
                band_category_presets=BAND_TRANSACTION_CATEGORY_PRESETS,
            )

        if request.method == "POST":
            if not can_manage_band_finances:
                abort(403)
            stored_files: list[str] = []
            try:
                values = band_transaction_values_from_form(request.form)
                uploaded_files = [
                    uploaded_file
                    for uploaded_file in request.files.getlist("attachments")
                    if getattr(uploaded_file, "filename", "")
                ]
                connection.execute("BEGIN IMMEDIATE")
                transaction_cursor = connection.execute(
                    """
                    INSERT INTO band_transactions (
                        transaction_type, transaction_on, category, description, amount_cents,
                        created_at, created_by, created_by_username
                    ) VALUES (?, ?, ?, ?, ?, ?, ?, ?)
                    """,
                    (
                        values["transaction_type"],
                        values["transaction_on"],
                        values["category"],
                        values["description"],
                        values["amount_cents"],
                        utc_now(),
                        g.user["id"],
                        g.user["username"],
                    ),
                )
                transaction_id = int(transaction_cursor.lastrowid)
                for uploaded_file in uploaded_files:
                    original_filename = band_attachment_original_filename(uploaded_file)
                    file_path = save_band_transaction_attachment(uploaded_file, uuid.uuid4().hex)
                    if file_path is None:
                        continue
                    stored_files.append(file_path)
                    connection.execute(
                        """
                        INSERT INTO band_transaction_attachments (
                            transaction_id, file_path, original_filename,
                            created_at, created_by, created_by_username
                        ) VALUES (?, ?, ?, ?, ?, ?)
                        """,
                        (
                            transaction_id,
                            file_path,
                            original_filename,
                            utc_now(),
                            g.user["id"],
                            g.user["username"],
                        ),
                    )
                audit(
                    connection,
                    "create",
                    "band_transaction",
                    transaction_id,
                    {
                        "transaction_type": values["transaction_type"],
                        "transaction_on": values["transaction_on"],
                        "category": values["category"],
                        "amount_cents": values["amount_cents"],
                        "attachment_count": len(stored_files),
                    },
                )
                connection.commit()
            except (ValueError, TypeError) as exc:
                connection.rollback()
                for file_path in stored_files:
                    delete_invoice_file(file_path)
                flash(str(exc), "error")
                return render_band_finances(), 400
            except Exception:
                connection.rollback()
                for file_path in stored_files:
                    delete_invoice_file(file_path)
                current_app.logger.exception("Could not create band transaction")
                flash("Die Band-Buchung konnte nicht gespeichert werden.", "error")
            else:
                backup_after_commit()
                flash("Band-Buchung wurde gespeichert.", "success")
            return redirect(url_for("band_finances_page"))

        return render_band_finances()

    @app.post("/band-finanzen/<int:transaction_id>/stornieren")
    @login_required
    def cancel_band_transaction(transaction_id: int):
        """Cancel a band booking without erasing its evidence or audit trail."""

        if not has_role(g.user, "manager"):
            abort(403)
        connection = get_db()
        transaction = connection.execute(
            "SELECT id, is_cancelled FROM band_transactions WHERE id = ?", (transaction_id,)
        ).fetchone()
        if transaction is None:
            abort(404)
        if bool(transaction["is_cancelled"]):
            flash("Diese Band-Buchung ist bereits storniert.", "error")
            return redirect(url_for("band_finances_page"))
        try:
            connection.execute("BEGIN IMMEDIATE")
            connection.execute(
                """
                UPDATE band_transactions
                SET is_cancelled = 1, cancelled_at = ?, cancelled_by_user_id = ?, cancelled_by_username = ?
                WHERE id = ?
                """,
                (utc_now(), g.user["id"], g.user["username"], transaction_id),
            )
            audit(
                connection,
                "cancel",
                "band_transaction",
                transaction_id,
                {"is_cancelled": True},
            )
            connection.commit()
        except sqlite3.DatabaseError:
            connection.rollback()
            current_app.logger.exception("Could not cancel band transaction")
            flash("Die Band-Buchung konnte nicht storniert werden.", "error")
        else:
            backup_after_commit()
            flash("Band-Buchung storniert. Die Historie und Anhänge bleiben erhalten.", "success")
        return redirect(url_for("band_finances_page"))

    @app.get("/api/band-finanzen/<int:transaction_id>/anhaenge/<int:attachment_id>")
    @login_required
    @member_required
    def band_transaction_attachment(transaction_id: int, attachment_id: int):
        """Serve a managed band attachment only to an authenticated user."""

        attachment = get_db().execute(
            """
            SELECT file_path, original_filename
            FROM band_transaction_attachments
            WHERE id = ? AND transaction_id = ?
            """,
            (attachment_id, transaction_id),
        ).fetchone()
        filename = str(attachment["file_path"]) if attachment and attachment["file_path"] else None
        try:
            content = read_invoice_bytes(filename) if filename else None
        except ValueError:
            abort(404)
        if content is None:
            abort(404)
        original_filename = str(attachment["original_filename"]).replace("\x00", "").replace("\\", "/")
        download_name = Path(original_filename).name or filename
        return send_file(
            io.BytesIO(content),
            mimetype=invoice_mimetype(filename),
            as_attachment=False,
            download_name=download_name,
        )

    @app.get("/api/variants/<int:variant_id>/last-purchase-price")
    @login_required
    @member_required
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
            created_at = utc_now()
            for item_index, item in enumerate(cart_items):
                invoice_file_path = save_invoice_file(item["uploaded_invoice"], receipt_id)
                if invoice_file_path:
                    stored_files.append(invoice_file_path)
                cursor = connection.execute(
                    """
                    INSERT INTO purchases (
                        receipt_id, variant_id, quantity, unit_cost_cents, purchased_on,
                        supplier, invoice_reference, invoice_file_path, comment, created_at, created_by,
                        created_by_username
                    ) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
                    """,
                    (
                        receipt_id, item["variant_id"], item["quantity"], item["unit_cost_cents"], purchased_on,
                        item["supplier"], item["invoice_reference"], invoice_file_path, item["comment"],
                        created_at, g.user["id"], g.user["username"],
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
                            receipt_id, file_path, created_at, created_by, created_by_username
                        ) VALUES (?, ?, ?, ?, ?)
                        """,
                        (receipt_id, invoice_file_path, utc_now(), g.user["id"], g.user["username"]),
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
    @member_required
    def purchase_invoice(purchase_id: int):
        """Serve an invoice only when it belongs to an existing booking."""

        purchase = get_db().execute(
            "SELECT invoice_file_path FROM purchases WHERE id = ?", (purchase_id,)
        ).fetchone()
        filename = str(purchase["invoice_file_path"]) if purchase and purchase["invoice_file_path"] else None
        content = read_invoice_bytes(filename) if filename else None
        if content is None:
            abort(404)
        return send_file(
            io.BytesIO(content),
            mimetype=invoice_mimetype(filename),
            as_attachment=False,
            download_name=filename,
        )

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
                    INSERT INTO purchase_receipt_attachments (
                        receipt_id, file_path, created_at, created_by, created_by_username
                    ) VALUES (?, ?, ?, ?, ?)
                    """,
                    (receipt_id, file_path, utc_now(), g.user["id"], g.user["username"]),
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
    @member_required
    def purchase_receipt_attachment(receipt_id: str, attachment_id: int):
        """Serve a cart invoice only when it belongs to that purchase receipt."""

        attachment = get_db().execute(
            "SELECT file_path FROM purchase_receipt_attachments WHERE id = ? AND receipt_id = ?",
            (attachment_id, receipt_id),
        ).fetchone()
        filename = str(attachment["file_path"]) if attachment and attachment["file_path"] else None
        content = read_invoice_bytes(filename) if filename else None
        if content is None:
            abort(404)
        return send_file(
            io.BytesIO(content),
            mimetype=invoice_mimetype(filename),
            as_attachment=False,
            download_name=filename,
        )

    @app.get("/historie")
    @login_required
    @member_required
    def history_page():
        connection = get_db()
        sale_rows = connection.execute("SELECT * FROM sales ORDER BY sold_on DESC, id DESC").fetchall()
        return render_template(
            "history.html",
            title="Historie",
            receipts=receipt_history_payload(
                connection, sale_rows, timezone_name=app.config["DISPLAY_TIMEZONE"]
            ),
        )

    @app.patch("/api/sales/<int:sale_id>/cancel")
    @login_required
    @member_required
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
    @member_required
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
    @member_required
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
    @member_required
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
    @member_required
    def balances_page():
        return render_template("balances.html", title="Bilanzen", balances=balance_payload(get_db()))

    @app.get("/export/<kind>.csv")
    @login_required
    @member_required
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
    @member_required
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

    @app.post("/artikelverwaltung/import/<import_kind>")
    @login_required
    @manager_required
    def import_article_transactions(import_kind: str):
        """Import a fully validated purchase or sale CSV as one atomic batch."""

        kind = {"einkaeufe": "purchases", "verkaeufe": "sales"}.get(import_kind)
        if kind is None:
            abort(404)
        connection = get_db()
        try:
            rows = transaction_csv_rows(request.files.get("csv_file"), kind)
            connection.execute("BEGIN IMMEDIATE")
            # Catalogue compatibility and every price fallback are resolved
            # before the first INSERT. Any later constraint failure still
            # rolls the complete article/variant/ledger batch back.
            preflight_transaction_import(connection, rows)
            resolve_transaction_import_prices(connection, rows, kind)
            result = import_transaction_rows(connection, rows, kind)
            connection.commit()
            backup_after_commit()
            transaction_label = "Einkäufe" if kind == "purchases" else "Verkäufe"
            flash(
                f"{result['row_count']} {transaction_label} aus der CSV importiert; "
                f"{result['created_article_count']} neue Artikel angelegt.",
                "success",
            )
        except (ValueError, TypeError, sqlite3.IntegrityError) as exc:
            connection.rollback()
            message = (
                "Die CSV-Daten kollidieren mit bestehenden Artikeln."
                if isinstance(exc, sqlite3.IntegrityError)
                else str(exc)
            )
            flash(f"CSV-Import abgebrochen: {message}", "error")
        except Exception:
            connection.rollback()
            current_app.logger.exception("Could not import transaction CSV")
            flash("CSV-Import abgebrochen: Die Datei konnte nicht verarbeitet werden.", "error")
        return redirect(url_for("article_management_page"))

    @app.get("/produktpalette")
    @login_required
    def product_slideshow_page():
        catalogue = product_slideshow_catalogue(get_db())
        language = user_ui_language(g.user)
        return render_template(
            "slideshow.html",
            title=UI_TRANSLATIONS[language]["slideshow.title"],
            slideshow_photos=catalogue["photos"],
            slideshow_variants=catalogue["variants"],
            slideshow_settings=catalogue["settings"],
            can_manage_slideshow=has_role(g.user, "manager"),
        )

    @app.patch("/api/diashow/einstellungen")
    @login_required
    @manager_required
    def update_slideshow_settings():
        """Persist the shared price-display preference for closing collages."""

        payload = request.get_json(silent=True)
        collage_show_prices = payload.get("collage_show_prices") if isinstance(payload, dict) else None
        if not isinstance(collage_show_prices, bool):
            return jsonify({"ok": False, "error": "Die Collage-Preis-Anzeige muss als Ja oder Nein übergeben werden."}), 400
        connection = get_db()
        try:
            connection.execute("BEGIN IMMEDIATE")
            updated = connection.execute(
                "UPDATE slideshow_settings SET collage_show_prices = ? WHERE id = 1",
                (int(collage_show_prices),),
            )
            if updated.rowcount == 0:
                connection.execute(
                    "INSERT INTO slideshow_settings (id, collage_show_prices) VALUES (1, ?)",
                    (int(collage_show_prices),),
                )
            settings = slideshow_settings_payload(connection)
            audit(
                connection,
                "set_slideshow_collage_price_visibility",
                "slideshow_settings",
                1,
                settings,
            )
            connection.commit()
        except sqlite3.DatabaseError:
            connection.rollback()
            current_app.logger.exception("Could not update slideshow settings")
            return jsonify({"ok": False, "error": "Die Collage-Einstellung konnte nicht gespeichert werden."}), 500
        backup_after_commit()
        return jsonify({"ok": True, "settings": settings, **settings})

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

    @app.post("/api/varianten/<int:variant_id>/fotos")
    @login_required
    @manager_required
    def upload_variant_photos(variant_id: int):
        """Attach one or more optimised JPEG files to an active variant."""

        connection = get_db()
        variant = connection.execute(
            """
            SELECT v.id
            FROM variants v
            JOIN articles a ON a.id = v.article_id
            WHERE v.id = ? AND v.is_active = 1 AND a.is_active = 1
            """,
            (variant_id,),
        ).fetchone()
        if variant is None:
            abort(404)
        uploaded_files = [
            uploaded_file
            for uploaded_file in request.files.getlist("photos")
            if uploaded_file is not None and getattr(uploaded_file, "filename", "")
        ]
        if not uploaded_files:
            return jsonify({"ok": False, "error": "Bitte mindestens ein Produktfoto auswählen."}), 400
        stored_filenames: list[str] = []
        try:
            prepared_photos = [normalized_variant_photo_upload(uploaded_file) for uploaded_file in uploaded_files]
            connection.execute("BEGIN IMMEDIATE")
            next_position = int(
                connection.execute(
                    "SELECT COALESCE(MAX(position), -1) + 1 FROM variant_photos WHERE variant_id = ?",
                    (variant_id,),
                ).fetchone()[0]
            )
            for offset, (original_filename, jpeg_bytes) in enumerate(prepared_photos):
                filename = f"variant-{variant_id}-{uuid.uuid4().hex}.jpg"
                store_variant_photo_bytes(filename, jpeg_bytes)
                stored_filenames.append(filename)
                connection.execute(
                    """
                    INSERT INTO variant_photos (
                        variant_id, file_path, original_filename, position, created_at, created_by, created_by_username
                    ) VALUES (?, ?, ?, ?, ?, ?, ?)
                    """,
                    (
                        variant_id,
                        filename,
                        original_filename,
                        next_position + offset,
                        utc_now(),
                        g.user["id"],
                        g.user["username"],
                    ),
                )
            audit(
                connection,
                "upload_photos",
                "variant",
                variant_id,
                {"count": len(prepared_photos)},
            )
            connection.commit()
        except ValueError as exc:
            connection.rollback()
            return jsonify({"ok": False, "error": str(exc)}), 400
        except Exception:
            connection.rollback()
            for filename in stored_filenames:
                try:
                    delete_variant_photo_file(filename)
                except OSError:
                    current_app.logger.exception("Could not remove incomplete product photo upload")
            current_app.logger.exception("Could not store product photos")
            return jsonify({"ok": False, "error": "Die Produktfotos konnten nicht gespeichert werden."}), 500
        backup_after_commit()
        return jsonify(
            {
                "ok": True,
                "photos": variant_photos_by_variant(connection, [variant_id]).get(variant_id, []),
            }
        )

    @app.post("/api/diashow/fotos")
    @login_required
    @manager_required
    def upload_slideshow_extra_photos():
        """Store independent shop-display pictures outside the product catalogue."""

        connection = get_db()
        uploaded_files = [
            uploaded_file
            for uploaded_file in request.files.getlist("photos")
            if uploaded_file is not None and getattr(uploaded_file, "filename", "")
        ]
        if not uploaded_files:
            return jsonify({"ok": False, "error": "Bitte mindestens ein Bild auswählen."}), 400
        stored_filenames: list[str] = []
        inserted_photo_ids: list[int] = []
        try:
            prepared_photos = [normalized_variant_photo_upload(uploaded_file) for uploaded_file in uploaded_files]
            connection.execute("BEGIN IMMEDIATE")
            next_position = int(
                connection.execute("SELECT COALESCE(MAX(position), -1) + 1 FROM slideshow_extra_photos").fetchone()[0]
            )
            for offset, (original_filename, jpeg_bytes) in enumerate(prepared_photos):
                filename = f"slideshow-extra-{uuid.uuid4().hex}.jpg"
                store_variant_photo_bytes(filename, jpeg_bytes)
                stored_filenames.append(filename)
                cursor = connection.execute(
                    """
                    INSERT INTO slideshow_extra_photos (
                        file_path, original_filename, position, created_at, created_by, created_by_username
                    ) VALUES (?, ?, ?, ?, ?, ?)
                    """,
                    (
                        filename,
                        original_filename,
                        next_position + offset,
                        utc_now(),
                        g.user["id"],
                        g.user["username"],
                    ),
                )
                inserted_photo_ids.append(int(cursor.lastrowid))
            audit(
                connection,
                "upload_slideshow_extra_photos",
                "slideshow_extra_photo",
                inserted_photo_ids[0] if len(inserted_photo_ids) == 1 else None,
                {"count": len(prepared_photos)},
            )
            connection.commit()
        except ValueError as exc:
            connection.rollback()
            return jsonify({"ok": False, "error": str(exc)}), 400
        except Exception:
            connection.rollback()
            for filename in stored_filenames:
                try:
                    delete_variant_photo_file(filename)
                except OSError:
                    current_app.logger.exception("Could not remove incomplete slideshow picture upload")
            current_app.logger.exception("Could not store independent slideshow pictures")
            return jsonify({"ok": False, "error": "Die Bilder konnten nicht gespeichert werden."}), 500
        backup_after_commit()
        return jsonify({"ok": True, "photos": slideshow_extra_photo_metadata(connection, inserted_photo_ids)})

    @app.delete("/api/diashow/fotos/<int:photo_id>")
    @login_required
    @manager_required
    def delete_slideshow_extra_photo(photo_id: int):
        """Remove an independent shop-display picture and its local JPEG."""

        connection = get_db()
        photo = connection.execute(
            "SELECT id, file_path FROM slideshow_extra_photos WHERE id = ?", (photo_id,)
        ).fetchone()
        if photo is None:
            abort(404)
        try:
            connection.execute("BEGIN IMMEDIATE")
            connection.execute("DELETE FROM slideshow_extra_photos WHERE id = ?", (photo_id,))
            audit(connection, "delete_slideshow_extra_photo", "slideshow_extra_photo", photo_id, {})
            connection.commit()
        except sqlite3.DatabaseError:
            connection.rollback()
            current_app.logger.exception("Could not delete independent slideshow picture metadata")
            return jsonify({"ok": False, "error": "Das Dia konnte nicht gelöscht werden."}), 500
        try:
            delete_variant_photo_file(str(photo["file_path"]))
        except OSError:
            current_app.logger.exception("Could not remove independent slideshow picture file: %s", photo["file_path"])
        backup_after_commit()
        return jsonify({"ok": True, "photo_id": photo_id})

    @app.patch("/api/diashow/fotos/<int:photo_id>")
    @login_required
    @manager_required
    def update_slideshow_extra_photo_inclusion(photo_id: int):
        """Persist an independent slide's inclusion or price-display setting."""

        try:
            field, value = slideshow_photo_setting_from_payload(request.get_json(silent=True))
        except ValueError as exc:
            return jsonify({"ok": False, "error": str(exc)}), 400
        connection = get_db()
        photo = connection.execute("SELECT id FROM slideshow_extra_photos WHERE id = ?", (photo_id,)).fetchone()
        if photo is None:
            abort(404)
        try:
            connection.execute("BEGIN IMMEDIATE")
            column = "include_in_slideshow" if field == "include_in_slideshow" else "show_price"
            connection.execute(
                f"UPDATE slideshow_extra_photos SET {column} = ? WHERE id = ?",
                (int(value), photo_id),
            )
            audit(
                connection,
                (
                    "set_slideshow_extra_photo_inclusion"
                    if field == "include_in_slideshow"
                    else "set_slideshow_extra_photo_price_visibility"
                ),
                "slideshow_extra_photo",
                photo_id,
                {field: value},
            )
            connection.commit()
        except sqlite3.DatabaseError:
            connection.rollback()
            current_app.logger.exception("Could not update independent slideshow picture setting")
            return jsonify({"ok": False, "error": "Die Dia-Einstellung konnte nicht gespeichert werden."}), 500
        backup_after_commit()
        return jsonify({"ok": True, "photo_id": photo_id, field: value})

    @app.get("/api/diashow/fotos/<int:photo_id>")
    @login_required
    def slideshow_extra_photo_file(photo_id: int):
        """Serve an authorised independent slideshow JPEG from managed storage."""

        photo = get_db().execute(
            "SELECT file_path FROM slideshow_extra_photos WHERE id = ?", (photo_id,)
        ).fetchone()
        if photo is None:
            abort(404)
        try:
            content = read_variant_photo_bytes(str(photo["file_path"]))
        except ValueError:
            abort(404)
        if content is None:
            abort(404)
        response = send_file(io.BytesIO(content), mimetype="image/jpeg", max_age=0, conditional=False)
        response.headers["Cache-Control"] = "private, no-store"
        return response

    @app.delete("/api/variantenfotos/<int:photo_id>")
    @login_required
    @manager_required
    def delete_variant_photo(photo_id: int):
        """Remove one metadata row and its managed local JPEG file."""

        connection = get_db()
        photo = connection.execute(
            """
            SELECT vp.id, vp.variant_id, vp.file_path
            FROM variant_photos vp
            JOIN variants v ON v.id = vp.variant_id
            JOIN articles a ON a.id = v.article_id
            WHERE vp.id = ? AND a.is_active = 1
            """,
            (photo_id,),
        ).fetchone()
        if photo is None:
            abort(404)
        try:
            connection.execute("BEGIN IMMEDIATE")
            connection.execute("DELETE FROM variant_photos WHERE id = ?", (photo_id,))
            audit(
                connection,
                "delete_photo",
                "variant",
                int(photo["variant_id"]),
                {"photo_id": photo_id},
            )
            connection.commit()
        except sqlite3.DatabaseError:
            connection.rollback()
            current_app.logger.exception("Could not delete product photo metadata")
            return jsonify({"ok": False, "error": "Das Produktfoto konnte nicht gelöscht werden."}), 500
        try:
            delete_variant_photo_file(str(photo["file_path"]))
        except OSError:
            # The metadata is already gone, so the file cannot be reached by
            # the application any more.  Keep the deletion durable and leave
            # a diagnostic for a future administrator cleanup.
            current_app.logger.exception("Could not remove product photo file: %s", photo["file_path"])
        backup_after_commit()
        return jsonify({"ok": True, "photo_id": photo_id})

    @app.patch("/api/variantenfotos/<int:photo_id>/diashow")
    @login_required
    @manager_required
    def update_variant_photo_slideshow_inclusion(photo_id: int):
        """Persist a product photo's slideshow inclusion or price visibility."""

        try:
            field, value = slideshow_photo_setting_from_payload(request.get_json(silent=True))
        except ValueError as exc:
            return jsonify({"ok": False, "error": str(exc)}), 400
        connection = get_db()
        photo = connection.execute(
            """
            SELECT vp.id, vp.variant_id
            FROM variant_photos vp
            JOIN variants v ON v.id = vp.variant_id
            JOIN articles a ON a.id = v.article_id
            WHERE vp.id = ? AND v.is_active = 1 AND a.is_active = 1
            """,
            (photo_id,),
        ).fetchone()
        if photo is None:
            abort(404)
        try:
            connection.execute("BEGIN IMMEDIATE")
            column = "include_in_slideshow" if field == "include_in_slideshow" else "show_price"
            connection.execute(
                f"UPDATE variant_photos SET {column} = ? WHERE id = ?",
                (int(value), photo_id),
            )
            audit(
                connection,
                (
                    "set_slideshow_photo_inclusion"
                    if field == "include_in_slideshow"
                    else "set_slideshow_photo_price_visibility"
                ),
                "variant_photo",
                photo_id,
                {"variant_id": int(photo["variant_id"]), field: value},
            )
            connection.commit()
        except sqlite3.DatabaseError:
            connection.rollback()
            current_app.logger.exception("Could not update product slideshow setting")
            return jsonify({"ok": False, "error": "Die Dia-Einstellung konnte nicht gespeichert werden."}), 500
        backup_after_commit()
        return jsonify({"ok": True, "photo_id": photo_id, field: value})

    @app.get("/api/variantenfotos/<int:photo_id>")
    @login_required
    def variant_photo_file(photo_id: int):
        """Serve an authorised product JPEG without exposing its filesystem path."""

        photo = get_db().execute(
            """
            SELECT vp.file_path
            FROM variant_photos vp
            JOIN variants v ON v.id = vp.variant_id
            JOIN articles a ON a.id = v.article_id
            WHERE vp.id = ? AND a.is_active = 1
            """,
            (photo_id,),
        ).fetchone()
        if photo is None:
            abort(404)
        try:
            content = read_variant_photo_bytes(str(photo["file_path"]))
        except ValueError:
            abort(404)
        if content is None:
            abort(404)
        response = send_file(io.BytesIO(content), mimetype="image/jpeg", max_age=0, conditional=False)
        response.headers["Cache-Control"] = "private, no-store"
        return response

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
            new_group_first_value_ids = apply_option_configuration(connection, article_id, option_groups)
            preserve_variants_for_new_option_groups(connection, article_id, new_group_first_value_ids)
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
            # Reordering is an independent lifecycle decision: an obsolete
            # variant may remain offered until its remaining stock is sold.
            # Reset all active rows first because unchecked HTML checkboxes are
            # omitted from the submitted form.
            connection.execute(
                "UPDATE variants SET no_reorder = 0, updated_at = ? WHERE article_id = ? AND is_active = 1",
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
                    no_reorder_match = re.fullmatch(r"no_reorder_(\d+)", key)
                    if no_reorder_match:
                        connection.execute(
                            "UPDATE variants SET no_reorder = 1, updated_at = ? WHERE id = ? AND article_id = ?",
                            (utc_now(), int(no_reorder_match.group(1)), article_id),
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
                {
                    "name": name,
                    "options_changed": True,
                    "is_offered": bool(is_offered),
                    "new_option_default_value_ids": new_group_first_value_ids,
                },
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
        if (
            (request.path.startswith("/api/varianten/") and request.path.endswith("/fotos"))
            or request.path == "/api/diashow/fotos"
        ):
            message = "Ein Bild darf höchstens 10 MB groß sein."
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
