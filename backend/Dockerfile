# backend/Dockerfile
# Python Celery worker — runs the ML extraction pipeline.
# No HTTP server inside this image — workers only (HTTP is the Go server).

FROM python:3.12-slim

WORKDIR /app

# System deps for pytesseract (OCR binary) and Pillow/image libs
RUN apt-get update && apt-get install -y --no-install-recommends \
    tesseract-ocr \
    libgl1 \
    && rm -rf /var/lib/apt/lists/*

RUN pip install "setuptools<82"

# Heavy ML libs that RARELY change — cached as their own layer.
# CPU-only torch build keeps the image far smaller than the CUDA default.
RUN pip install torch \
    --index-url https://download.pytorch.org/whl/cpu \
    --timeout 300 --retries 10

RUN pip install sentence-transformers \
    --timeout 300 --retries 10

# For Word → PDF
RUN apt-get update && apt-get install -y libreoffice-writer --no-install-recommends \
    && rm -rf /var/lib/apt/lists/*   
# worth adding — cleans apt cache, keeps image smaller

# Frequently changing requirements — separate layer so edits here don't
# invalidate the torch/sentence-transformers cache above.
COPY requirements.txt .
RUN pip install --no-cache-dir --timeout 300 --retries 10 -r requirements.txt

COPY . .

# Mount points — populated via docker-compose volumes
RUN mkdir -p /app/export /app/uploads /app/database

ENV PYTHONUNBUFFERED=1

# Health: container is healthy if celery responds to inspect ping
HEALTHCHECK --interval=30s --timeout=10s --retries=3 \
    CMD celery -A workers.celery_app inspect ping -d celery@$HOSTNAME || exit 1

# This image runs the Celery worker, not an HTTP server — main.py does
# not exist in this project, all HTTP is handled by hitl-go/.
CMD ["celery", "-A", "workers.celery_app", "worker", \
     "--loglevel=info", "--concurrency=2"]
