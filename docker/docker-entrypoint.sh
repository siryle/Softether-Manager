#!/bin/sh
set -eu
mkdir -p "${SEM_DATA_DIR:-/data}"
exec "$@"
