FROM python:3.12-slim

# The application deliberately uses a single small Python container and SQLite.
# That keeps the footprint appropriate for a DS225+ while still providing proper
# transactions and a durable database.
ENV PYTHONDONTWRITEBYTECODE=1 \
    PYTHONUNBUFFERED=1

WORKDIR /app
COPY requirements.txt .
RUN pip install --no-cache-dir -r requirements.txt

COPY VERSION .
COPY app.py .
COPY templates ./templates
COPY static ./static
COPY scripts ./scripts

EXPOSE 8000

# One worker is intentional: SQLite serialises writes safely and the workload is
# a handful of short sales transactions, not a high-volume public web service.
CMD ["gunicorn", "--bind", "0.0.0.0:8000", "--workers", "1", "--threads", "4", "--timeout", "60", "app:create_app()"]
