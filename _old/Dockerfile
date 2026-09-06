FROM python:3.12-slim

# The application deliberately uses a single small Python container and SQLite.
# That keeps the footprint appropriate for a DS225+ while still providing proper
# transactions and a durable database.
ARG APP_VERSION=0.0.0
ENV PYTHONDONTWRITEBYTECODE=1 \
    PYTHONUNBUFFERED=1 \
    APP_VERSION=${APP_VERSION}

WORKDIR /app
COPY requirements.txt .
# Transitional migration support: sqlcipher3 and its compiler fallback remain
# in this release only so an existing SQLCipher installation can be read once
# and converted to ordinary SQLite/files. New installations and normal runtime
# use plaintext SQLite; remove these build tools after the migration window.
RUN apt-get update \
    && apt-get install -y --no-install-recommends build-essential pkg-config \
    && CONAN_HOME=/tmp/sqlcipher-conan pip install --no-cache-dir -r requirements.txt \
    && rm -rf /var/lib/apt/lists/* /tmp/sqlcipher-conan

COPY app.py .
COPY templates ./templates
COPY static ./static

EXPOSE 8000

# One worker is intentional: SQLite serialises writes safely and the workload is
# a handful of short sales transactions, not a high-volume public web service.
CMD ["gunicorn", "--bind", "0.0.0.0:8000", "--workers", "1", "--threads", "4", "--timeout", "60", "app:create_app()"]
