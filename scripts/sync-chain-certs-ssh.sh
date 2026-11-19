#!/bin/sh
# Sync SoftEther intermediate CA certificates (sem-chain-*.cer) to a remote
# SoftEther VPN server's chain_certs/ directory over SSH.
#
# Intended for use as SoftEther Manager's cert_renew_hook (copy to data/ssl/hook.sh
# and configure data/ssl/ssh-sync.env), or run standalone with CERT_PEM / CERT_CHAIN_DIR.

set -eu

SCRIPT_DIR="$(CDPATH= cd -- "$(dirname -- "$0")" && pwd)"
CONF_FILE="${SSH_SYNC_CONF:-$SCRIPT_DIR/ssh-sync.env}"
if [ -f "$CONF_FILE" ]; then
  # shellcheck disable=SC1090
  . "$CONF_FILE"
fi

SSH_HOST="${SSH_HOST:-${SE_SERVER_HOST:-}}"
SSH_PORT="${SSH_PORT:-22}"
SSH_USER="${SSH_USER:-root}"
SSH_KEY="${SSH_KEY:-}"
SSH_KNOWN_HOSTS="${SSH_KNOWN_HOSTS:-}"
SSH_STRICT_HOST_KEY_CHECKING="${SSH_STRICT_HOST_KEY_CHECKING:-accept-new}"
REMOTE_CHAIN_DIR="${REMOTE_CHAIN_DIR:-}"

if [ -z "$SSH_HOST" ]; then
  echo "sync-chain-certs-ssh: SSH_HOST (or SE_SERVER_HOST) is required" >&2
  exit 1
fi

if ! command -v ssh >/dev/null 2>&1; then
  echo "sync-chain-certs-ssh: ssh client not found in PATH" >&2
  exit 1
fi

if [ -z "$SSH_KEY" ]; then
  for candidate in "$SCRIPT_DIR/id_ed25519" "$SCRIPT_DIR/id_rsa"; do
    if [ -f "$candidate" ]; then
      SSH_KEY="$candidate"
      break
    fi
  done
fi

if [ -z "$SSH_KNOWN_HOSTS" ] && [ -d "$SCRIPT_DIR" ]; then
  SSH_KNOWN_HOSTS="$SCRIPT_DIR/known_hosts"
fi

set -- -p "$SSH_PORT" \
  -o BatchMode=yes \
  -o ConnectTimeout=15 \
  -o "StrictHostKeyChecking=$SSH_STRICT_HOST_KEY_CHECKING"

if [ -n "$SSH_KEY" ]; then
  if [ ! -f "$SSH_KEY" ]; then
    echo "sync-chain-certs-ssh: SSH_KEY not found: $SSH_KEY" >&2
    exit 1
  fi
  set -- "$@" -i "$SSH_KEY" -o IdentitiesOnly=yes
fi

if [ -n "$SSH_KNOWN_HOSTS" ]; then
  set -- "$@" -o "UserKnownHostsFile=$SSH_KNOWN_HOSTS"
fi

TARGET="${SSH_USER}@${SSH_HOST}"

TMP_DIR=""
cleanup() {
  if [ -n "$TMP_DIR" ] && [ -d "$TMP_DIR" ]; then
    rm -rf "$TMP_DIR"
  fi
}
trap cleanup EXIT INT TERM

WORK_DIR=""
if [ -n "${CERT_CHAIN_DIR:-}" ] && [ -d "$CERT_CHAIN_DIR" ]; then
  WORK_DIR="$CERT_CHAIN_DIR"
elif [ -n "${CERT_PEM:-}" ] && [ -f "$CERT_PEM" ]; then
  TMP_DIR="$(mktemp -d)"
  WORK_DIR="$TMP_DIR"
  awk -v outdir="$WORK_DIR" '
    /-----BEGIN CERTIFICATE-----/ { in_cert=1; idx++; buf=$0 "\n"; next }
    in_cert { buf = buf $0 "\n" }
    /-----END CERTIFICATE-----/ {
      if (in_cert) {
        if (idx > 1) {
          file = sprintf("%s/sem-chain-%d.cer", outdir, idx - 1)
          printf "%s", buf > file
          close(file)
        }
        in_cert=0
        buf=""
      }
    }
  ' "$CERT_PEM"
else
  echo "sync-chain-certs-ssh: CERT_CHAIN_DIR or CERT_PEM is required" >&2
  exit 1
fi

CER_FILES=""
for f in "$WORK_DIR"/sem-chain-*.cer; do
  if [ -f "$f" ]; then
    CER_FILES="$CER_FILES $(basename "$f")"
  fi
done
CER_FILES="${CER_FILES# }"

if [ -z "$REMOTE_CHAIN_DIR" ]; then
  REMOTE_CHAIN_DIR="$(ssh "$@" "$TARGET" '
    for d in /var/lib/softether /opt/vpnserver /usr/local/vpnserver /usr/vpnserver /opt/softether/vpnserver; do
      if [ -d "$d/chain_certs" ] || [ -x "$d/vpnserver" ] || [ -f "$d/vpn_server.config" ]; then
        printf "%s" "$d/chain_certs"
        exit 0
      fi
    done
    exit 1
  ' || true)"
  if [ -z "$REMOTE_CHAIN_DIR" ]; then
    echo "sync-chain-certs-ssh: could not detect remote SoftEther directory; set REMOTE_CHAIN_DIR in $CONF_FILE" >&2
    exit 1
  fi
fi

case "$REMOTE_CHAIN_DIR" in
  *"'"*|*"
"*)
    echo "sync-chain-certs-ssh: invalid characters in REMOTE_CHAIN_DIR" >&2
    exit 1
    ;;
esac

if [ -z "$CER_FILES" ]; then
  ssh "$@" "$TARGET" "rm -f '$REMOTE_CHAIN_DIR'/sem-chain-*.cer"
  if [ -n "${CERT_CHAIN_DIR:-}" ]; then
    rm -f "$CERT_CHAIN_DIR/.synced"
  fi
  exit 0
fi

# shellcheck disable=SC2086
tar -C "$WORK_DIR" -cf - $CER_FILES | ssh "$@" "$TARGET" \
  "mkdir -p '$REMOTE_CHAIN_DIR' && rm -f '$REMOTE_CHAIN_DIR'/sem-chain-*.cer && tar -C '$REMOTE_CHAIN_DIR' -xf - && chmod 644 '$REMOTE_CHAIN_DIR'/sem-chain-*.cer"

if [ -n "${CERT_CHAIN_DIR:-}" ] && [ -d "$CERT_CHAIN_DIR" ]; then
  : > "$CERT_CHAIN_DIR/.synced"
fi

echo "Synced ($CER_FILES) to ${TARGET}:${REMOTE_CHAIN_DIR}"
