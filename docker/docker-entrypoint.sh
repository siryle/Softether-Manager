#!/bin/sh
set -eu
mkdir -p "${SEM_DATA_DIR:-/data}"
if [ -f "/app/data/ip2region.xdb" ] && [ ! -f "${SEM_DATA_DIR:-/data}/ip2region.xdb" ]; then
    cp "/app/data/ip2region.xdb" "${SEM_DATA_DIR:-/data}/ip2region.xdb" 2>/dev/null || true
fi
exec "$@"
