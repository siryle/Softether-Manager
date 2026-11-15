#!/usr/bin/env bash
set -e

# Ensure data directory exists
mkdir -p "${SEM_DATA_DIR:-/data}"

# If the command starts with an option flag or "run.py", prefix with python
if [ "${1#-}" != "$1" ]; then
    set -- python run.py "$@"
elif [ "$1" = "run.py" ]; then
    set -- python "$@"
elif [ "$1" = "sem" ]; then
    exec /usr/local/bin/sem "${@:2}"
fi

# If starting the main server, execute any initial auto-setup commands if configured
if [ "$1" = "python" ] && [ "${2:-}" = "run.py" ]; then
    echo "==> Starting SoftEther Manager container..."

    # Pre-configure web path if specified in environment
    if [ -n "${SEM_WEB_PATH:-}" ]; then
        python -m app.manage webpath "$SEM_WEB_PATH" >/dev/null 2>&1 || true
    fi

    # Pre-configure admin user if username and password are provided
    if [ -n "${SEM_ADMIN_USERNAME:-}" ] && [ -n "${SEM_ADMIN_PASSWORD:-}" ]; then
        echo "==> Configuring administrator account: ${SEM_ADMIN_USERNAME}..."
        python -m app.manage password --username "$SEM_ADMIN_USERNAME" --password "$SEM_ADMIN_PASSWORD" || true
    fi

    # Pre-configure SoftEther connection if password is provided
    if [ -n "${SEM_SE_PASSWORD:-}" ]; then
        TARGET_HOST="${SEM_SE_HOST:-127.0.0.1}"
        TARGET_PORT="${SEM_SE_PORT:-5555}"
        echo "==> Configuring SoftEther target: ${TARGET_HOST}:${TARGET_PORT}..."
        python -m app.manage connect --host "$TARGET_HOST" --port "$TARGET_PORT" --password "$SEM_SE_PASSWORD" || true
    fi
fi

exec "$@"
