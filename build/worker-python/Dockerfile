# ---- Build Stage ----
FROM python:3.12-slim AS builder

WORKDIR /app
COPY sdk/python/requirements.txt ./
RUN pip install --no-cache-dir --prefix=/install -r requirements.txt

COPY sdk/python/ ./sdk/python/
COPY examples/python-worker/ ./examples/python-worker/

# ---- Runtime Stage ----
FROM python:3.12-slim

RUN groupadd -r forge && useradd -r -g forge forge

COPY --from=builder /install /usr/local
COPY --from=builder /app/sdk/python /app/sdk/python
COPY --from=builder /app/examples/python-worker /app/worker

WORKDIR /app/worker
USER forge

ENV PYTHONUNBUFFERED=1
ENV PYTHONPATH=/app

EXPOSE 50053
ENTRYPOINT ["python", "-m", "worker"]
