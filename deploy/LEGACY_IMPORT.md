# Legacy SQLite import

`importlegacy` is a one-shot migration tool for moving one installation of the
old Flask/SQLite Merch Manager into one new MariaDB band tenant.

It does **not** modify the legacy databases. Both SQLite files are opened
read-only and must already be decrypted/plain SQLite snapshots. Do not point
the importer at database files that the old server is still changing; create a
consistent snapshot first.

## Source layout

Prepare one directory with the two SQLite snapshots and the old managed files:

```text
legacy/
├── merch.sqlite3
├── users.sqlite3
├── invoices/
└── variant-photos/
```

If the old installation used SQLCipher or encrypted `.enc` attachments, decrypt
that snapshot first. The importer deliberately rejects encrypted input rather
than guessing keys or rewriting the source. Existing full-backup archives from
the old application already use the same `data/merch.sqlite3`,
`data/users.sqlite3`, `data/invoices/` and `data/variant-photos/` layout; after
extracting/decrypting them, the contained `data/` directory can be used as the
legacy source directory.

## What is migrated

The importer creates a **new band** and assigns all imported operational rows to
its newly allocated `band_id`. It generates new MariaDB IDs and remaps relations
between articles, options, variants, purchases, sales, events, finance entries,
users and audit records. Product photos and documents are copied into the new
band-prefixed storage and verified by SHA-256 after copying.

Legacy passwords, MFA secrets, sessions, setup/reset challenges and SMTP
credentials are intentionally not copied. Each imported user receives a new
one-time setup code. Old `support_admin` and `system_admin` accounts are reduced
to `band_admin`; a legacy account never gains platform access through an import.
At least one active imported account is guaranteed to be a `band_admin`.

Ephemeral payment-QR intents and offline-sync idempotency records are not
migrated. The dry-run report calls these out when they exist.

## Dry run first

The target MariaDB must already contain the current application schema. A dry
run reads and validates all source rows, checks foreign-key-style references,
checks that the requested slug is available and hashes every referenced file.
It creates no band and copies no files.

From a checked-out repository:

```bash
cd backend
go run ./cmd/importlegacy \
  -operational-db /path/to/legacy/merch.sqlite3 \
  -users-db /path/to/legacy/users.sqlite3 \
  -legacy-storage /path/to/legacy \
  -band "Protovibe" \
  -slug protovibe \
  -dry-run=true \
  -report-out /path/to/migration/dry-run.json
```

The command uses the normal backend environment (`DATABASE_DSN`, `SECRET_KEY`,
and related settings). The secret key is required by the application config but
no legacy password or encryption key is consumed by the importer.

## Real import

Only proceed after the dry run is clean and the new MariaDB/storage have a
current backup. A real import is transactional for MariaDB. If an ordinary
error occurs, newly copied storage objects are deleted as part of cleanup.

```bash
cd backend
go run ./cmd/importlegacy \
  -operational-db /path/to/legacy/merch.sqlite3 \
  -users-db /path/to/legacy/users.sqlite3 \
  -legacy-storage /path/to/legacy \
  -band "Protovibe" \
  -slug protovibe \
  -contact-email "band@example.org" \
  -dry-run=false \
  -credentials-out /path/to/migration/protovibe-setup-codes.json \
  -report-out /path/to/migration/import-report.json
```

`-credentials-out` is mandatory for a real import, is created with mode `0600`
and is never overwritten. Its setup codes are the only credentials users need
from the migration. Keep the file private and delete it after the users have
completed password setup.

## Synology / Docker

The backend image already contains `/usr/local/bin/importlegacy`. With the
Synology compose project, mount the snapshot read-only and a separate directory
for the reports/setup codes. Run this from the directory containing the compose
file and its `.env` file.

Dry run:

```bash
docker compose --env-file .env.synology -f docker-compose.synology.yml run --rm \
  -v /volume1/docker/merch-legacy:/legacy:ro \
  -v /volume1/docker/merch-migration:/migration \
  --entrypoint /usr/local/bin/importlegacy backend \
  -operational-db /legacy/merch.sqlite3 \
  -users-db /legacy/users.sqlite3 \
  -legacy-storage /legacy \
  -band "Protovibe" \
  -slug protovibe \
  -dry-run=true \
  -report-out /migration/dry-run.json
```

Real import:

```bash
docker compose --env-file .env.synology -f docker-compose.synology.yml run --rm \
  -v /volume1/docker/merch-legacy:/legacy:ro \
  -v /volume1/docker/merch-migration:/migration \
  --entrypoint /usr/local/bin/importlegacy backend \
  -operational-db /legacy/merch.sqlite3 \
  -users-db /legacy/users.sqlite3 \
  -legacy-storage /legacy \
  -band "Protovibe" \
  -slug protovibe \
  -dry-run=false \
  -credentials-out /migration/protovibe-setup-codes.json \
  -report-out /migration/import-report.json
```

The importer refuses to reuse an existing band slug. This makes rerunning a
successful import fail safely instead of duplicating the same legacy data into
the same tenant.
