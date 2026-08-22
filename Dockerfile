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
# sqlcipher3 distributes self-contained wheels and, where a wheel is not
# available, builds its bundled SQLCipher sources.  The compiler tools keep
# that ARM/x86 fallback available without linking the old Debian SQLCipher 3
# package (the application deliberately writes SQLCipher-4 files).
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
