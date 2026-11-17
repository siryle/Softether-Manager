#!/usr/bin/env bash
set -euo pipefail

# This test owns its temporary repository, including its release refs.
unset GITHUB_REF

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
TEST_DIR="$(mktemp -d)"
trap 'rm -rf "$TEST_DIR"' EXIT

git init --quiet "$TEST_DIR"
cd "$TEST_DIR"
git config user.name 'Version Test'
git config user.email 'version-test@example.invalid'
git config commit.gpgsign false
git config tag.gpgsign false
printf '1.0.0\n' > VERSION
printf 'initial\n' > changed.txt
printf 'initial\n' > other.txt
git add .
git -c core.hooksPath=/dev/null commit --quiet -m initial
mkdir .githooks scripts
cp "$ROOT/.githooks/pre-commit" .githooks/pre-commit
cp "$ROOT/.githooks/post-commit" .githooks/post-commit
cp "$ROOT/scripts/version.sh" scripts/version.sh
git config core.hooksPath .githooks

expect_version() {
  local actual
  actual="$(git show HEAD:VERSION)"
  if [[ "$actual" != "$1" ]]; then
    printf 'FAIL: expected committed version %s, got %s\n' "$1" "$actual" >&2
    exit 1
  fi
}

printf 'first change\n' >> changed.txt
git add changed.txt
git commit --quiet -m first
expect_version 1.0.1
git diff --quiet -- VERSION
git diff --cached --quiet

git commit --quiet --allow-empty -m empty
expect_version 1.0.1

printf 'retry change\n' >> changed.txt
git add changed.txt
printf '#!/usr/bin/env bash\nexit 1\n' > .githooks/commit-msg
chmod +x .githooks/commit-msg
if git commit --quiet -m rejected; then
  printf 'FAIL: the commit-msg hook should reject the commit\n' >&2
  exit 1
fi
expect_version 1.0.1
[[ "$(git show :VERSION)" == 1.0.2 ]]
rm .githooks/commit-msg
git commit --quiet -m retry
expect_version 1.0.2

printf '1.1.0\n' > VERSION
git add VERSION
git commit --quiet -m 'manual version'
expect_version 1.1.0

printf '1.9.0\n' > VERSION
printf 'unstaged version test\n' >> changed.txt
git add changed.txt
if git commit --quiet -m 'must not overwrite unstaged version'; then
  printf 'FAIL: unstaged VERSION was overwritten\n' >&2
  exit 1
fi
[[ "$(cat VERSION)" == 1.9.0 ]]
git restore -- VERSION
git commit --quiet -m 'after restoring version'
expect_version 1.1.1

printf 'unrelated staged change\n' >> other.txt
git add other.txt
printf 'selected change\n' >> changed.txt
git commit --quiet --only changed.txt -m 'partial commit'
expect_version 1.1.2
[[ "$(git diff --cached --name-only)" == other.txt ]]
git commit --quiet -m 'remaining staged change'
expect_version 1.1.3
git commit --quiet --amend --no-edit
expect_version 1.1.3

[[ "$(bash scripts/version.sh)" == v1.1.3 ]]
[[ "$(GITHUB_REF=refs/tags/v3.2.1 bash scripts/version.sh)" == v3.2.1 ]]
[[ "$(GITHUB_REF=refs/tags/v3.2.1-rc.1 bash scripts/version.sh)" == v3.2.1-rc.1 ]]
git rev-parse -q --verify "refs/tags/v1.1.3" >/dev/null
printf '2.0.0\n' > VERSION
[[ "$(bash scripts/version.sh)" == v1.1.3 ]]
git tag -d v1.1.3 >/dev/null
[[ "$(bash scripts/version.sh)" == v2.0.0 ]]
git restore -- VERSION

# Verify decimal rollover rules:
# 1.0.9 -> 1.1.0
printf '1.0.9\n' > VERSION
git add VERSION
git commit --quiet -m 'set 1.0.9'
expect_version 1.0.9
printf 'rollover 1\n' >> changed.txt
git add changed.txt
git commit --quiet -m 'test rollover to 1.1.0'
expect_version 1.1.0

# 1.1.9 -> 1.2.0
printf '1.1.9\n' > VERSION
git add VERSION
git commit --quiet -m 'set 1.1.9'
expect_version 1.1.9
printf 'rollover 2\n' >> changed.txt
git add changed.txt
git commit --quiet -m 'test rollover to 1.2.0'
expect_version 1.2.0

# 1.9.9 -> 2.0.0
printf '1.9.9\n' > VERSION
git add VERSION
git commit --quiet -m 'set 1.9.9'
expect_version 1.9.9
printf 'rollover 3\n' >> changed.txt
git add changed.txt
git commit --quiet -m 'test rollover to 2.0.0'
expect_version 2.0.0

# 1.0.31 -> 1.1.0 (recovering from legacy >9 patch)
printf '1.0.31\n' > VERSION
git add VERSION
git commit --quiet -m 'set 1.0.31'
expect_version 1.0.31
printf 'legacy rollover\n' >> changed.txt
git add changed.txt
git commit --quiet -m 'test rollover from 1.0.31 to 1.1.0'
expect_version 1.1.0

# Verify auxiliary files (docs, screenshots, test files) do not bump version
printf 'doc update\n' >> README.md
git add README.md
git commit --quiet -m 'docs update'
expect_version 1.1.0

mkdir -p docs/screenshots
printf 'image data\n' > docs/screenshots/dashboard.png
git add docs/screenshots/dashboard.png
git commit --quiet -m 'image update'
expect_version 1.1.0

printf 'func TestDummy() {}\n' > dummy_test.go
git add dummy_test.go
git commit --quiet -m 'test update'
expect_version 1.1.0

printf 'invalid\n' > VERSION
git add VERSION
if git commit --quiet -m 'invalid version'; then
  printf 'FAIL: invalid VERSION was committed\n' >&2
  exit 1
fi
if bash scripts/version.sh; then
  printf 'FAIL: invalid VERSION was used for a build\n' >&2
  exit 1
fi

if GITHUB_REF=refs/tags/v-invalid bash scripts/version.sh; then
  printf 'FAIL: invalid release tag was accepted\n' >&2
  exit 1
fi

printf 'PASS: commit bumps, retry, manual versions, partial commits, unstaged protection, amend, and build version selection\n'
