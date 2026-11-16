#!/usr/bin/env bash
#
# Build the release tarball: the one artefact the installer downloads.
#
# It carries Go executables, the built frontend, the installer, the
# management CLI and a VERSION file. The name
# deliberately has no version in it, because GitHub serves the moving latest
# pointer at .../releases/latest/download/<file> and a filename with the tag
# in it could not be fetched from there. The tag lives inside, in VERSION.
#
# Usage:
#   bash scripts/build-release.sh --version v0.1.0 --output dist/release
#
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
VERSION=""
OUTPUT="$ROOT/dist/release"
SKIP_FRONTEND=0

readonly NAME="softether-manager"

usage() {
  cat >&2 <<'USAGE'
Build the SoftEther Manager release tarball.

  --version <tag>    Version to stamp into VERSION (default: git describe)
  --output <dir>     Where to write the tarball (default: dist/release)
  --skip-frontend    Use the existing app/web/out instead of rebuilding
  -h, --help         Show this message
USAGE
}

while [[ $# -gt 0 ]]; do
  case "$1" in
    --version) VERSION="${2:-}"; shift 2 ;;
    --output) OUTPUT="${2:-}"; shift 2 ;;
    --skip-frontend) SKIP_FRONTEND=1; shift ;;
    -h|--help) usage; exit 0 ;;
    *) usage; printf 'error: unknown argument %s\n' "$1" >&2; exit 2 ;;
  esac
done

step() { printf '\033[1;34m==>\033[0m %s\n' "$*" >&2; }

if [[ -z "$VERSION" ]]; then
  # The same rule CI uses: the exact tag on HEAD, or an unversioned build that
  # is deliberately not installable.
  VERSION="$(git -C "$ROOT" describe --tags --exact-match 2>/dev/null || true)"
  if [[ -z "$VERSION" ]]; then
    VERSION="0.0.0-$(git -C "$ROOT" rev-parse --short HEAD 2>/dev/null || echo unknown)"
  fi
fi

if (( SKIP_FRONTEND == 0 )); then
  step "Building the frontend"
  (cd "$ROOT/app/web" && (npm ci || npm install) && npm run build)
fi
[[ -f "$ROOT/app/web/out/index.html" ]] ||
  { printf 'error: app/web/out/index.html is missing; build the frontend first\n' >&2; exit 1; }

STAGING="$(mktemp -d)"
trap 'rm -rf "$STAGING"' EXIT
TREE="$STAGING/$NAME"

step "Assembling $VERSION"
mkdir -p "$TREE/scripts"
printf '%s\n' "$VERSION" > "$TREE/VERSION"

# Keep one release asset name so existing installers can migrate in place.
# Both Linux architectures are included; the installer chooses its executable.
mkdir -p "$TREE/app/web" "$TREE/bin"
cp -a "$ROOT/app/web/out" "$TREE/app/web/out"
for arch in amd64 arm64; do
  step "Building Go backend for linux/$arch"
  (cd "$ROOT" && CGO_ENABLED=0 GOOS=linux GOARCH="$arch" \
    go build -trimpath -ldflags="-s -w" -o "$TREE/bin/$NAME-linux-$arch" ./cmd/softether-manager)
done

cp -a "$ROOT/scripts/install.sh" "$TREE/scripts/install.sh"
cp -a "$ROOT/scripts/sem" "$TREE/scripts/sem"
chmod 0755 "$TREE/scripts/install.sh" "$TREE/scripts/sem"
[[ -f "$ROOT/README.md" ]] && cp -a "$ROOT/README.md" "$TREE/README.md"

mkdir -p "$OUTPUT"
ARCHIVE="$OUTPUT/$NAME.tar.gz"
step "Writing $ARCHIVE"
tar -czf "$ARCHIVE" -C "$STAGING" "$NAME"

( cd "$OUTPUT"
  if command -v sha256sum >/dev/null 2>&1; then
    sha256sum "$NAME.tar.gz" > "$NAME.tar.gz.sha256"
  else
    shasum -a 256 "$NAME.tar.gz" > "$NAME.tar.gz.sha256"
  fi
)

step "Done."
printf '%s\n' "$ARCHIVE"
printf '%s\n' "$ARCHIVE.sha256"
