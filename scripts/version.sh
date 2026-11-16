#!/usr/bin/env bash
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
version=""
if [[ "${GITHUB_REF:-}" == refs/tags/v* ]]; then
  version="${GITHUB_REF#refs/tags/}"
else
  version="$(git -C "$ROOT" describe --tags --exact-match --match 'v[0-9]*' 2>/dev/null || true)"
fi
if [[ -z "$version" ]]; then
  version="$(cat "$ROOT/VERSION")"
  if [[ ! "$version" =~ ^[0-9]+\.[0-9]+\.[0-9]+$ ]]; then
    printf 'error: VERSION must contain MAJOR.MINOR.PATCH\n' >&2
    exit 1
  fi
  version="v$version"
fi
if [[ ! "$version" =~ ^v[0-9]+\.[0-9]+\.[0-9]+(-[a-zA-Z0-9.-]+)?(\+[a-zA-Z0-9.-]+)?$ ]]; then
  printf 'error: release tag must be vMAJOR.MINOR.PATCH (with optional prerelease/build suffix)\n' >&2
  exit 1
fi
printf '%s\n' "$version"
