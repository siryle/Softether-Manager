#!/usr/bin/env bash
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
version="$(git -C "$ROOT" describe --tags --exact-match 2>/dev/null || true)"
if [[ -z "$version" ]]; then
  version="$(cat "$ROOT/VERSION")"
  if [[ ! "$version" =~ ^[0-9]+\.[0-9]+\.[0-9]+$ ]]; then
    printf 'error: VERSION must contain MAJOR.MINOR.PATCH\n' >&2
    exit 1
  fi
  version="v$version"
fi
printf '%s\n' "$version"
