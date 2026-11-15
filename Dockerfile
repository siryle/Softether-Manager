# ========================================================
# Stage 1: Build the Next.js Frontend
# ========================================================
FROM --platform=$BUILDPLATFORM node:20-alpine AS frontend-builder

WORKDIR /build

# Copy dependency manifests first to leverage Docker layer caching
COPY app/web/package.json app/web/package-lock.json ./
RUN npm ci || npm install

# Copy frontend source and build static export (app/web/out)
COPY app/web/ ./
RUN npm run build

# ========================================================
# Stage 2: Python Runtime Environment
# ========================================================
FROM python:3.12-slim AS runtime

LABEL maintainer="DrSaeedHub"
LABEL description="SoftEther Manager Web UI & Management Panel in Docker"

ENV PYTHONUNBUFFERED=1 \
    PYTHONDONTWRITEBYTECODE=1 \
    SEM_DATA_DIR=/data \
    SEM_BIND_HOST=0.0.0.0 \
    SEM_BIND_PORT=8000 \
    SEM_CLI_PATH=/usr/local/bin/sem

# Install curl and ca-certificates for healthcheck and TLS operations
RUN apt-get update && apt-get install -y --no-install-recommends \
    curl \
    ca-certificates \
    && rm -rf /var/lib/apt/lists/*

WORKDIR /app

# Install Python requirements
COPY requirements.txt .
RUN pip install --no-cache-dir -r requirements.txt

# Copy application backend and libraries
COPY run.py VERSION .
COPY app/ ./app/
COPY Library/ ./Library/

# Copy built frontend assets from builder stage
COPY --from=frontend-builder /build/out /app/app/web/out

# Install container scripts and CLI tools
COPY docker/docker-entrypoint.sh /docker-entrypoint.sh
COPY docker/healthcheck.py /app/healthcheck.py
COPY docker/sem /usr/local/bin/sem

RUN chmod +x /docker-entrypoint.sh /usr/local/bin/sem /app/healthcheck.py

# Create persistent storage volume directory
RUN mkdir -p /data

EXPOSE 8000

VOLUME ["/data"]

HEALTHCHECK --interval=30s --timeout=5s --start-period=10s --retries=3 \
    CMD python /app/healthcheck.py || exit 1

ENTRYPOINT ["/docker-entrypoint.sh"]
CMD ["python", "run.py"]
