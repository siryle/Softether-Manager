FROM --platform=$BUILDPLATFORM node:22-alpine AS frontend
WORKDIR /build
COPY app/web/package.json app/web/package-lock.json ./
RUN npm ci
COPY app/web/ ./
RUN npm run build

FROM --platform=$BUILDPLATFORM golang:1.27.3-alpine AS backend
WORKDIR /build
COPY go.mod go.sum ./
RUN go mod download
COPY cmd/ ./cmd/
COPY internal/ ./internal/
ARG TARGETOS
ARG TARGETARCH
RUN CGO_ENABLED=0 GOOS=$TARGETOS GOARCH=$TARGETARCH \
    go build -trimpath -ldflags="-s -w" -o /softether-manager ./cmd/softether-manager

FROM alpine:3.22
RUN apk add --no-cache ca-certificates \
    && mkdir -p /data /app/data \
    && ln -s /usr/local/bin/softether-manager /usr/local/bin/sem \
    && (wget -q --timeout=15 -O /app/data/ip2region.xdb https://cdn.jsdelivr.net/gh/lionsoul2014/ip2region@master/data/ip2region_v4.xdb || true)
WORKDIR /app
ENV SEM_APP_ROOT=/app \
    SEM_DATA_DIR=/data \
    SEM_BIND_HOST=0.0.0.0 \
    SEM_BIND_PORT=8000 \
    SEM_CLI_PATH=/usr/local/bin/sem
COPY --from=backend /softether-manager /usr/local/bin/softether-manager
COPY --from=frontend /build/out /app/app/web/out
COPY VERSION /app/VERSION
ARG RELEASE_VERSION
RUN if [ -n "$RELEASE_VERSION" ]; then printf '%s\n' "$RELEASE_VERSION" > /app/VERSION; fi
COPY --chmod=755 docker/docker-entrypoint.sh /docker-entrypoint.sh
EXPOSE 8000
VOLUME ["/data"]
HEALTHCHECK --interval=30s --timeout=5s --start-period=10s --retries=3 \
    CMD ["softether-manager", "healthcheck"]
ENTRYPOINT ["/docker-entrypoint.sh"]
CMD ["softether-manager", "serve"]
