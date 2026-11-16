#!/usr/bin/env bash
#
# SoftEther Manager installer.
#
# The panel is a Go binary and a built single-page frontend. Its database
# is a single SQLite file, so installing it is a download and a systemd unit. There is no database server to set up, no
# role to create, no password to store in a file. Everything the operator is
# asked for -- username, password, port, web path -- is asked once, here, and
# never asked again: the answers end up in the database, which is what an
# upgrade keeps, and the login password can be changed later from the panel or
# with 'sem password'.
#
# Usage:
#   bash <(curl -Ls https://raw.githubusercontent.com/siryle/Softether-Manager/master/scripts/install.sh)
#   bash <(curl -Ls .../install.sh) --non-interactive \
#        --username admin --password-stdin --port 8443 --path abc123 --json
#
set -euo pipefail

# A transient systemd unit -- which is how the panel's self-update runs this
# script -- has no HOME, and the gh CLI needs one to find its credentials for
# the private-repository fallback below.
export HOME="${HOME:-/root}"

# ---------------------------------------------------------------- exit codes
readonly EXIT_OK=0
readonly EXIT_NOT_ROOT=10
readonly EXIT_UNSUPPORTED_OS=11
readonly EXIT_NO_SYSTEMD=12
readonly EXIT_PORT_IN_USE=13
readonly EXIT_BAD_ARGUMENTS=14
readonly EXIT_DOWNLOAD_FAILED=15
readonly EXIT_CHECKSUM_FAILED=16
readonly EXIT_SERVICE_FAILED=17
readonly EXIT_NO_CONNECTIVITY=18
readonly EXIT_DEPENDENCY_FAILED=19

readonly SERVICE_NAME="softether-manager"
readonly APP_DIR="/opt/softether-manager"
readonly DATA_DIR="/var/lib/softether-manager"
readonly ENV_PATH="/etc/softether-manager.env"
readonly UNIT_PATH="/etc/systemd/system/softether-manager.service"
# The management CLI, installed beside the panel. It is what an operator gets
# when they run this script again on a host that already has it, and it is the
# only thing on the system that can change the port or reset a password when
# nobody can log in.
readonly CLI_NAME="sem"
readonly CLI_PATH="/usr/local/bin/sem"
readonly SUPPORT_DIR="/usr/local/share/softether-manager"
readonly CACHED_INSTALLER="$SUPPORT_DIR/install.sh"
readonly CLI_ENV_PATH="$SUPPORT_DIR/cli.env"

readonly DEFAULT_REPO="siryle/Softether-Manager"
readonly DEFAULT_RELEASE_BASE="https://github.com/$DEFAULT_REPO/releases/download"
readonly DEFAULT_INSTALLER_URL="https://raw.githubusercontent.com/$DEFAULT_REPO/master/scripts/install.sh"
readonly ASSET="softether-manager.tar.gz"

# Any password the operator is willing to type is accepted: this panel is
# often put on a throwaway box behind a secret web path, and refusing a short
# one there only trains people to fight the installer. Length is a
# recommendation, printed once, not a rule.
readonly RECOMMENDED_PASSWORD_LENGTH=12

# The SoftEther VPN Server this panel manages. When the host has none, the
# installer puts one here and points the panel at it, so a bare machine ends
# up with a working VPN rather than an empty panel asking to be connected to
# something that does not exist.
readonly SE_DIR="/opt/vpnserver"
readonly SE_SERVICE="vpnserver"
readonly SE_MGMT_PORT=5555
# Pin upstream archives because these releases publish no checksum sidecar.
readonly SE_VERSION="v4.44-9807-rtm"
readonly SE_DATE="2025.04.16"

# ---------------------------------------------------------------- arguments

USERNAME=""
PASSWORD=""
PASSWORD_STDIN=0
PORT=""
WEB_PATH=""
BIND_ADDRESS="0.0.0.0"
VERSION="latest"
NON_INTERACTIVE=0
JSON_OUTPUT=0
ASSUME_YES=0
MODE="install"
PURGE_DATA=0
REMOVE_CLI=0
REPO="${SEM_REPO:-$DEFAULT_REPO}"
RELEASE_BASE="${SEM_RELEASE_BASE:-$DEFAULT_RELEASE_BASE}"
INSTALLER_URL="${SEM_INSTALLER_URL:-}"
NO_MENU="${SEM_NO_MENU:-0}"
INSTALL_SOFTETHER=1
SE_PASSWORD=""

usage() {
  cat >&2 <<'USAGE'
Install the SoftEther Manager panel.

  --username <str>     Operator account to create on first run
  --password <str>     Its password (visible in process arguments; prefer stdin)
  --password-stdin     Read the panel password from standard input
  --port <int>         Port the panel listens on
  --path <str>         Secret URL prefix the panel is served under ('' for none)
  --bind <ip>          Address to bind (default 0.0.0.0)
  --version <tag>      Release to install (default: latest)
  --non-interactive    Never prompt; every required value must be given
  --json               Print a machine-readable result on stdout
  --yes                Do not ask for confirmation
  --upgrade            Upgrade in place, keeping the database and settings
  --uninstall          Remove the panel
  --purge-data         With --uninstall, also delete the database and its data
  --remove-cli         With --uninstall, also remove the sem management CLI
  --no-menu            Install even if sem is present, instead of launching it

  --no-softether       Do not install a SoftEther VPN Server, even if none is here
  --softether-password <str>
                       Administrator password for a SoftEther installed by this
                       script (default: generated on this machine)

  --repo <owner/name>  Repository to install from and check for updates
  --release-base <url> Where release assets live
  --installer-url <u>  Where sem fetches a fresh copy of this script from
  -h, --help           Show this message

Running this script with no arguments on a host that already has sem installed
opens sem instead of reinstalling. Pass --no-menu, or any other flag, to
install regardless. The web path may be empty: --path '' serves the panel at
the root.

There is no database to configure: the panel stores everything in one SQLite
file under /var/lib/softether-manager. Backing the panel up is copying that
directory.

The panel manages the SoftEther VPN Server on this machine. On a fresh install
where the host has none, one is installed to /opt/vpnserver as the vpnserver
service and the panel is pointed at it with a password generated here. A
SoftEther that is already installed is never touched -- connect it from the
panel, which is also what --no-softether leaves you to do. Removing the panel
never removes the VPN server.

Exit codes: 0 ok, 10 not root, 11 unsupported OS, 12 no systemd, 13 port in
use, 14 bad arguments, 15 download failed, 16 checksum failed, 17 service
failed, 18 no outbound connectivity, 19 a dependency could not be installed.
USAGE
}

# Everything human-readable goes to stderr, so --json can own stdout entirely.
say() { printf '%s\n' "$*" >&2; }
step() { printf '\033[1;34m==>\033[0m %s\n' "$*" >&2; }
warn() { printf '\033[1;33mwarning:\033[0m %s\n' "$*" >&2; }
fail() {
  local code="$1"
  shift
  printf '\033[1;31merror:\033[0m %s\n' "$*" >&2
  exit "$code"
}

require_value() {
  local flag="$1" value="${2-}"
  if [[ -z "$value" || "$value" == --* ]]; then
    usage
    fail "$EXIT_BAD_ARGUMENTS" "$flag requires a value."
  fi
}

# require_present is require_value for a flag whose value may legitimately be
# empty. --path '' is the request to serve the panel at the root, and
# require_value would reject it as a missing value -- which it is not.
require_present() {
  local flag="$1" count="$2"
  if (( count < 2 )); then
    usage
    fail "$EXIT_BAD_ARGUMENTS" "$flag requires a value; pass '' for none."
  fi
}

WEB_PATH_GIVEN=0
ARGUMENT_COUNT=$#

while [[ $# -gt 0 ]]; do
  case "$1" in
    --username) require_value "$1" "${2-}"; USERNAME="$2"; shift 2 ;;
    --password-stdin) PASSWORD_STDIN=1; shift ;;
    --password) require_value "$1" "${2-}"; PASSWORD="$2"; shift 2 ;;
    --port) require_value "$1" "${2-}"; PORT="$2"; shift 2 ;;
    --path|--web-path) require_present "$1" "$#"; WEB_PATH="$2"; WEB_PATH_GIVEN=1; shift 2 ;;
    --bind) require_value "$1" "${2-}"; BIND_ADDRESS="$2"; shift 2 ;;
    --version) require_value "$1" "${2-}"; VERSION="$2"; shift 2 ;;
    --repo) require_value "$1" "${2-}"; REPO="$2"; RELEASE_BASE="https://github.com/$2/releases/download"; shift 2 ;;
    --release-base) require_value "$1" "${2-}"; RELEASE_BASE="$2"; shift 2 ;;
    --installer-url) require_value "$1" "${2-}"; INSTALLER_URL="$2"; shift 2 ;;
    --non-interactive) NON_INTERACTIVE=1; shift ;;
    --json) JSON_OUTPUT=1; shift ;;
    --yes|-y) ASSUME_YES=1; shift ;;
    --upgrade) MODE="upgrade"; shift ;;
    --uninstall) MODE="uninstall"; shift ;;
    --purge-data) PURGE_DATA=1; shift ;;
    --remove-cli) REMOVE_CLI=1; shift ;;
    --no-menu) NO_MENU=1; shift ;;
    --no-softether) INSTALL_SOFTETHER=0; shift ;;
    --softether-password) require_value "$1" "${2-}"; SE_PASSWORD="$2"; shift 2 ;;
    -h|--help) usage; exit "$EXIT_OK" ;;
    *) usage; fail "$EXIT_BAD_ARGUMENTS" "Unknown argument: $1" ;;
  esac
done

if (( PASSWORD_STDIN )); then
  [[ -z "$PASSWORD" ]] || fail "$EXIT_BAD_ARGUMENTS" "Use either --password or --password-stdin."
  PASSWORD="$(head -c 1025)"
  (( ${#PASSWORD} <= 1024 )) || fail "$EXIT_BAD_ARGUMENTS" "Password input is too long."
fi

# ---------------------------------------------------------------- checks

[[ $EUID -eq 0 ]] || fail "$EXIT_NOT_ROOT" "This installer must run as root. It installs a system service."

# A bare re-run on a host that already has the panel opens sem rather than
# reinstalling: it is almost always somebody who wants to change the port,
# reset a password or check whether the thing is running.
if (( ARGUMENT_COUNT == 0 )) && [[ "$NO_MENU" != "1" && -x "$CLI_PATH" && -d "$APP_DIR" ]]; then
  if [[ -e /dev/tty ]]; then
    say "The panel is already installed here, so this is opening $CLI_NAME."
    say "To install over it instead, run this script with --no-menu."
    say ""
    exec "$CLI_PATH"
  fi
fi

[[ "$(uname -s)" == "Linux" ]] || fail "$EXIT_UNSUPPORTED_OS" "This panel runs on Linux only."
command -v systemctl >/dev/null 2>&1 || fail "$EXIT_NO_SYSTEMD" "systemd is required and systemctl was not found."
[[ -d /run/systemd/system ]] || fail "$EXIT_NO_SYSTEMD" "systemd is not the running init system."

DOWNLOADER=""
if command -v curl >/dev/null 2>&1; then
  DOWNLOADER="curl"
elif command -v wget >/dev/null 2>&1; then
  DOWNLOADER="wget"
fi

# Fetches a URL to a local path. A file:// source, or a bare absolute path, is
# copied rather than downloaded: it is what makes the whole flow exercisable
# from a build directory without pretending to be a release host.
fetch() {
  local url="$1" destination="$2"
  case "$url" in
    file://*) cp -f "${url#file://}" "$destination" 2>/dev/null ;;
    /*) cp -f "$url" "$destination" 2>/dev/null ;;
    *)
      case "$DOWNLOADER" in
        curl) curl -fsSL --connect-timeout 15 --retry 2 -o "$destination" "$url" ;;
        wget) wget -q --timeout=15 --tries=3 -O "$destination" "$url" ;;
        *) return 1 ;;
      esac
      ;;
  esac
}

# Release assets may live on a private repository, where the anonymous URL
# answers 404. The gh CLI, when present and authenticated, can still fetch
# them -- so it is the fallback, tried only after the plain download failed.
fetch_release_asset() {
  local file="$1" destination="$2" url
  url="$(release_url "$file")"
  if fetch "$url" "$destination"; then
    return 0
  fi
  if command -v gh >/dev/null 2>&1; then
    local gh_version="$VERSION"
    if [[ "$gh_version" == "latest" ]]; then
      gh_version="$(gh release view --repo "$REPO" --json tagName -q .tagName 2>/dev/null || true)"
    fi
    if [[ -n "$gh_version" ]]; then
      local dir
      dir="$(mktemp -d)"
      if gh release download "$gh_version" --repo "$REPO" --pattern "$file" --dir "$dir" >/dev/null 2>&1; then
        mv -f "$dir/$file" "$destination"
        rm -rf "$dir"
        return 0
      fi
      rm -rf "$dir"
    fi
  fi
  return 1
}

# Where one release artefact lives. A directory-served base is
# <base>/<version>/<file>; GitHub Releases serves the moving latest pointer at
# .../releases/latest/download/<file>, with the two segments swapped.
release_url() {
  local file="$1"
  if [[ "$VERSION" == "latest" && "$RELEASE_BASE" == */releases/download ]]; then
    printf '%s/latest/download/%s\n' "${RELEASE_BASE%/download}" "$file"
    return
  fi
  printf '%s/%s/%s\n' "$RELEASE_BASE" "$VERSION" "$file"
}

source_is_remote() {
  case "$RELEASE_BASE" in
    file://*|/*) return 1 ;;
    *) return 0 ;;
  esac
}

# ---------------------------------------------------------------- validation

validate_supplied() {
  if [[ -n "$PORT" ]]; then
    [[ "$PORT" =~ ^[0-9]+$ ]] || fail "$EXIT_BAD_ARGUMENTS" "--port must be a number."
    (( PORT > 0 && PORT < 65536 )) || fail "$EXIT_BAD_ARGUMENTS" "--port must be between 1 and 65535."
  fi
  if [[ -n "$WEB_PATH" ]]; then
    [[ "$WEB_PATH" =~ ^[A-Za-z0-9._~-]+$ ]] ||
      fail "$EXIT_BAD_ARGUMENTS" "--path may contain only letters, digits, dot, underscore, tilde and hyphen. Pass '' to serve the panel at the root."
  fi
}
validate_supplied

# ---------------------------------------------------------------- helpers

read_env_value() {
  local file="$1" key="$2"
  [[ -f "$file" ]] || return 1
  sed -n "s/^${key}=//p" "$file" | tail -n1
}

random_hex() {
  local bytes="${1:-32}"
  if command -v openssl >/dev/null 2>&1; then
    openssl rand -hex "$bytes"
  else
    head -c "$((bytes * 2))" /dev/urandom | od -An -tx1 | tr -d ' \n' | cut -c "1-$((bytes * 2))"
  fi
}

port_in_use() {
  local port="$1"
  if command -v ss >/dev/null 2>&1; then
    ss -Hltn "sport = :$port" 2>/dev/null | grep -q . && return 0
    return 1
  fi
  if command -v netstat >/dev/null 2>&1; then
    netstat -ltn 2>/dev/null | awk '{print $4}' | grep -qE "[:.]$port\$" && return 0
    return 1
  fi
  return 1
}

random_port() {
  local candidate
  for _ in $(seq 1 40); do
    candidate=$(( (RANDOM % 20000) + 20000 ))
    if ! port_in_use "$candidate"; then
      printf '%s' "$candidate"
      return 0
    fi
  done
  printf '18080'
}

random_web_path() { random_hex 12; }

prompt() {
  local label="$1" default="$2" answer=""
  read -r -p "$label [$default]: " answer </dev/tty || true
  printf '%s' "${answer:-$default}"
}

prompt_secret() {
  local label="$1" first="" second=""
  while :; do
    read -r -s -p "$label: " first </dev/tty || true
    printf '\n' >&2
    if [[ -z "$first" ]]; then
      warn "The password cannot be empty."
      continue
    fi
    if (( ${#first} < RECOMMENDED_PASSWORD_LENGTH )); then
      warn "That is shorter than the $RECOMMENDED_PASSWORD_LENGTH characters recommended -- accepted anyway."
    fi
    read -r -s -p "Confirm password: " second </dev/tty || true
    printf '\n' >&2
    if [[ "$first" != "$second" ]]; then
      warn "The two passwords do not match."
      continue
    fi
    printf '%s' "$first"
    return 0
  done
}

# base_path renders a web path as a URL prefix; the one place that knows an
# empty one means the root, so it never produces the double slash of "$host//".
base_path() {
  if [[ -z "${1-}" ]]; then printf '/'; else printf '/%s/' "$1"; fi
}

MANAGER_BIN="$APP_DIR/softether-manager"

# --------------------------------------------------------------- packages

PACKAGE_MANAGER=""
detect_package_manager() {
  if command -v apt-get >/dev/null 2>&1; then
    PACKAGE_MANAGER="apt"
  elif command -v dnf >/dev/null 2>&1; then
    PACKAGE_MANAGER="dnf"
  elif command -v yum >/dev/null 2>&1; then
    PACKAGE_MANAGER="yum"
  else
    PACKAGE_MANAGER=""
  fi
}

APT_UPDATED=0
install_packages() {
  (( $# )) || return 0
  case "$PACKAGE_MANAGER" in
    apt)
      if (( APT_UPDATED == 0 )); then
        DEBIAN_FRONTEND=noninteractive apt-get update -qq >/dev/null 2>&1 || true
        APT_UPDATED=1
      fi
      DEBIAN_FRONTEND=noninteractive apt-get install -y -qq --no-install-recommends "$@" >/dev/null
      ;;
    dnf) dnf install -y -q "$@" >/dev/null ;;
    yum) yum install -y -q "$@" >/dev/null ;;
    *) return 1 ;;
  esac
}

# --------------------------------------------------------- SoftEther server
#
# The panel manages the SoftEther instance on its own machine. If the host
# already runs one, it is left exactly as it is -- the operator knows its
# password and connects it from the panel. If it runs none, one is installed
# here and the panel is pointed at it with a password generated on this
# machine, so a bare server ends up with a working VPN and a panel already
# talking to it.

# Where an existing SoftEther lives, if anywhere. Printed on stdout; empty
# when the host has none.
softether_dir() {
  local dir
  for dir in "$SE_DIR" /usr/local/vpnserver /usr/vpnserver /opt/softether/vpnserver; do
    if [[ -x "$dir/vpnserver" ]]; then printf '%s' "$dir"; return 0; fi
  done
  return 1
}

softether_present() {
  softether_dir >/dev/null && return 0
  # A unit or a listening management port both mean "something is already
  # serving here"; installing a second one over it would be destructive.
  systemctl list-unit-files 2>/dev/null | grep -q "^${SE_SERVICE}\.service" && return 0
  port_in_use "$SE_MGMT_PORT" && return 0
  return 1
}

# Use only archives whose hashes were checked when this installer was released.
softether_download_url() {
  local arch
  case "$(uname -m)" in
    x86_64|amd64) arch="linux-x64-64bit" ;;
    aarch64|arm64) arch="linux-arm64-64bit" ;;
    *) return 1 ;;
  esac
  printf 'https://github.com/SoftEtherVPN/SoftEtherVPN_Stable/releases/download/%s/softether-vpnserver-%s-%s-%s.tar.gz' "$SE_VERSION" "$SE_VERSION" "$SE_DATE" "$arch"
}

softether_checksum() {
  case "$(uname -m)" in
    x86_64|amd64) printf '%s' 'e423e7f94a9f1f97a77b6dd425ba54505f69978964ff97843c90cc619fb70599' ;;
    aarch64|arm64) printf '%s' '9ca568a8fc62e166370d3988c592830665a51b4d1a3874e9cb80e45c51083eae' ;;
    *) return 1 ;;
  esac
}

SE_INSTALLED=0
install_softether() {
  local url tmp
  step "No SoftEther VPN Server was found; installing one"

  # build-essential rather than a bare gcc: --no-install-recommends leaves out
  # libc6-dev, and without Scrt1.o, crti.o and -lm the link at the end of
  # SoftEther's make fails. glibc-devel is the same thing on the RPM side.
  case "$PACKAGE_MANAGER" in
    apt) install_packages build-essential curl tar || true ;;
    dnf|yum) install_packages gcc make glibc-devel curl tar || true ;;
  esac
  for tool in gcc make tar; do
    command -v "$tool" >/dev/null 2>&1 || {
      warn "$tool is missing and could not be installed, so SoftEther was not installed."
      warn "Install it yourself and connect it from the panel."
      return 1
    }
  done

  if ! url="$(softether_download_url)"; then
    warn "No SoftEther build is published for $(uname -m), so none was installed."
    return 1
  fi

  tmp="$(mktemp -d)"
  step "Downloading SoftEther VPN Server"
  if ! fetch "$url" "$tmp/vpnserver.tar.gz"; then
    warn "SoftEther could not be downloaded from $url; it was not installed."
    rm -rf "$tmp"
    return 1
  fi

  local expected actual
  expected="$(softether_checksum)" || { rm -rf "$tmp"; return 1; }
  actual="$(sha256sum "$tmp/vpnserver.tar.gz" | awk '{print $1}')"
  if [[ "$actual" != "$expected" ]]; then
    warn "SoftEther checksum mismatch; refusing to unpack or execute the archive."
    rm -rf "$tmp"
    return 1
  fi

  step "Building and installing SoftEther to $SE_DIR"
  if ! tar -xzf "$tmp/vpnserver.tar.gz" -C "$tmp"; then
    warn "The SoftEther archive could not be unpacked; it was not installed."
    rm -rf "$tmp"
    return 1
  fi
  # `make` here only compiles the license checker and accepts the licence; the
  # server binaries ship precompiled. `yes 1` answers however many questions
  # this build asks.
  # `yes` is killed by SIGPIPE the moment make stops reading, which under
  # `set -o pipefail` marks a perfectly good build as failed. The exit status
  # of this pipeline says nothing useful, so it is dropped: the binary the
  # build was supposed to produce is the only proof that counts.
  (cd "$tmp/vpnserver" && set +o pipefail && yes 1 | make >"$tmp/make.log" 2>&1) || true
  if [[ ! -x "$tmp/vpnserver/vpnserver" ]]; then
    warn "The SoftEther build produced no vpnserver binary; it was not installed. Last lines:"
    tail -n 12 "$tmp/make.log" >&2 || true
    rm -rf "$tmp"
    return 1
  fi
  rm -rf "$SE_DIR"
  mv "$tmp/vpnserver" "$SE_DIR"
  chmod 600 "$SE_DIR"/* 2>/dev/null || true
  chmod 700 "$SE_DIR/vpnserver" "$SE_DIR/vpncmd" 2>/dev/null || true
  rm -rf "$tmp"

  step "Installing the $SE_SERVICE service"
  cat > "/etc/systemd/system/$SE_SERVICE.service" <<SEUNIT
[Unit]
Description=SoftEther VPN Server
After=network-online.target
Wants=network-online.target

[Service]
Type=forking
ExecStart=$SE_DIR/vpnserver start
ExecStop=$SE_DIR/vpnserver stop
WorkingDirectory=$SE_DIR
Restart=always
RestartSec=3

[Install]
WantedBy=multi-user.target
SEUNIT
  systemctl daemon-reload
  systemctl enable "$SE_SERVICE" >/dev/null 2>&1 || true
  if ! systemctl restart "$SE_SERVICE"; then
    warn "SoftEther was installed but its service would not start."
    return 1
  fi

  # The management port answering is what "started" means here.
  local ready=0 _i
  for _i in $(seq 1 30); do
    if port_in_use "$SE_MGMT_PORT"; then ready=1; break; fi
    sleep 1
  done
  if (( ready == 0 )); then
    warn "SoftEther started but nothing answered on port $SE_MGMT_PORT."
    return 1
  fi

  # Use JSON-RPC and standard input so the password never appears in argv.
  [[ -n "$SE_PASSWORD" ]] || SE_PASSWORD="$(random_hex 18)"
  if ! (cd "$APP_DIR" && set -a && . "$ENV_PATH" && set +a &&
      printf '%s' "$SE_PASSWORD" | "$MANAGER_BIN" connect --host 127.0.0.1 \
        --port "$SE_MGMT_PORT" --password-stdin --initialize-password) >/dev/null 2>&1; then
    warn "The SoftEther administrator password could not be set; connect it from the panel yourself."
    return 1
  fi

  SE_INSTALLED=1
  SE_CONNECTED=1
  step "SoftEther VPN Server is running"
  return 0
}

# --------------------------------------------------------------- uninstall

if [[ "$MODE" == "uninstall" ]]; then
  if [[ $ASSUME_YES -eq 0 ]]; then
    if [[ $NON_INTERACTIVE -eq 1 ]]; then
      fail "$EXIT_BAD_ARGUMENTS" "--uninstall without --yes needs a terminal to confirm on."
    fi
    say ""
    say "This removes the SoftEther Manager panel from this server."
    say "The SoftEther VPN Server keeps running: it is its own service and is not touched."
    say ""
    if [[ $PURGE_DATA -eq 0 ]]; then
      say "The database is one file under $DATA_DIR. It holds the account, the"
      say "registered servers with their encrypted credentials, the traffic history,"
      say "the port and the web path."
      say "Keep it and installing again brings all of that back, at the same address."
      read -r -p "Delete the panel data? [y/N] " reply </dev/tty || true
      [[ "$reply" =~ ^[Yy]$ ]] && PURGE_DATA=1
    fi
    say ""
    if [[ $PURGE_DATA -eq 1 ]]; then
      say "  data: DELETED"
    else
      say "  data: kept at $DATA_DIR"
    fi
    read -r -p "Continue? [y/N] " reply </dev/tty || true
    [[ "$reply" =~ ^[Yy]$ ]] || { say "Nothing was changed."; exit "$EXIT_OK"; }
  fi

  step "Stopping the service"
  systemctl stop "$SERVICE_NAME" 2>/dev/null || true
  systemctl disable "$SERVICE_NAME" 2>/dev/null || true

  if [[ $PURGE_DATA -eq 1 ]]; then
    step "Deleting the data directory"
    rm -rf "$DATA_DIR"
    say "The database and the generated keys were deleted."
  else
    say "The data directory was kept at $DATA_DIR."
    say "Installing again restores the account and servers at the same address."
  fi

  rm -f "$UNIT_PATH" "$ENV_PATH"
  rm -rf "$APP_DIR"
  systemctl daemon-reload

  CLI_REMOVED=0
  if [[ $REMOVE_CLI -eq 1 ]]; then
    step "Removing the $CLI_NAME management CLI"
    rm -f "$CLI_PATH" "$CACHED_INSTALLER" "$CLI_ENV_PATH"
    rmdir "$SUPPORT_DIR" 2>/dev/null || true
    CLI_REMOVED=1
  elif [[ -x "$CLI_PATH" ]]; then
    say "The $CLI_NAME CLI was left at $CLI_PATH; remove it with '$CLI_NAME uninstall --remove-cli --yes'."
  fi

  if [[ $JSON_OUTPUT -eq 1 ]]; then
    printf '{"action":"uninstall","purged_data":%s,"removed_cli":%s,"service":"%s"}\n' \
      "$([[ $PURGE_DATA -eq 1 ]] && echo true || echo false)" \
      "$([[ $CLI_REMOVED -eq 1 ]] && echo true || echo false)" "$SERVICE_NAME"
  fi
  step "The panel has been removed."
  exit "$EXIT_OK"
fi

# ---------------------------------------------------------------- upgrade?

UPGRADE_EXISTING=0
if [[ -f "$UNIT_PATH" || -d "$APP_DIR" ]]; then
  UPGRADE_EXISTING=1
fi
[[ "$MODE" == "upgrade" ]] && UPGRADE_EXISTING=1

# ---------------------------------------------------------------- gather input

if [[ $UPGRADE_EXISTING -eq 1 ]]; then
  step "An existing installation was found; upgrading in place"
else
  if [[ $NON_INTERACTIVE -eq 1 ]]; then
    missing=()
    [[ -n "$USERNAME" ]] || missing+=("--username")
    [[ -n "$PASSWORD" ]] || missing+=("--password or --password-stdin")
    [[ -n "$PORT" ]] || missing+=("--port")
    (( WEB_PATH_GIVEN )) || missing+=("--path")
    if (( ${#missing[@]} )); then
      fail "$EXIT_BAD_ARGUMENTS" "--non-interactive needs every value. Missing: ${missing[*]}"
    fi
  else
    [[ -t 0 || -e /dev/tty ]] || fail "$EXIT_BAD_ARGUMENTS" "No terminal to prompt on. Use --non-interactive with every flag."
    say ""
    say "Installing the SoftEther Manager panel."
    say ""
    [[ -n "$USERNAME" ]] || USERNAME="$(prompt 'Admin username' 'admin')"
    [[ -n "$PASSWORD" ]] || PASSWORD="$(prompt_secret "Admin password (${RECOMMENDED_PASSWORD_LENGTH}+ characters recommended)")"
    [[ -n "$PORT" ]] || PORT="$(prompt 'Panel port' "$(random_port)")"
    if (( WEB_PATH_GIVEN == 0 )); then
      say ""
      say "The web path is a secret prefix the panel is served under."
      say "Type a value, or the word none to serve it at the root."
      WEB_PATH="$(prompt 'Web path' "$(random_web_path)")"
      [[ "${WEB_PATH,,}" == "none" ]] && WEB_PATH=""
      WEB_PATH_GIVEN=1
    fi
  fi

  validate_supplied
  if port_in_use "$PORT"; then
    fail "$EXIT_PORT_IN_USE" "Port $PORT is already in use. Choose another with --port."
  fi
fi

# ---------------------------------------------------------------- dependencies

detect_package_manager
[[ -n "$PACKAGE_MANAGER" ]] || warn "No supported package manager was found; assuming curl and tar are already installed."

step "Installing the packages the panel needs"
case "$PACKAGE_MANAGER" in
  apt) install_packages ca-certificates curl tar openssl ||
         fail "$EXIT_DEPENDENCY_FAILED" "apt-get could not install the required system tools." ;;
  dnf|yum) install_packages ca-certificates curl tar openssl ||
         fail "$EXIT_DEPENDENCY_FAILED" "The package manager could not install the required system tools." ;;
esac

if [[ -z "$DOWNLOADER" ]]; then
  if command -v curl >/dev/null 2>&1; then DOWNLOADER="curl"
  elif command -v wget >/dev/null 2>&1; then DOWNLOADER="wget"
  fi
fi
if source_is_remote && [[ -z "$DOWNLOADER" ]]; then
  fail "$EXIT_NO_CONNECTIVITY" "Neither curl nor wget is installed, so the release cannot be downloaded."
fi

case "$(uname -m)" in
  x86_64|amd64) MANAGER_ARCH=amd64 ;;
  aarch64|arm64) MANAGER_ARCH=arm64 ;;
  *) fail "$EXIT_UNSUPPORTED_OS" "This release supports Linux amd64 and arm64." ;;
esac

# ---------------------------------------------------------------- download

STAGING="$(mktemp -d)"
NEW_APP=""
PREVIOUS_APP=""
ROLLBACK=0
SERVICE_STOPPED=0
ROLLBACK_DATA_DIR="$DATA_DIR"
BACKUP_FILES=("$ENV_PATH" "$UNIT_PATH" "$CLI_PATH" "$CLI_ENV_PATH" "$CACHED_INSTALLER")
cleanup_install() {
  local result=$? recovery_failed=0 i
  set +e
  if (( result != 0 && ROLLBACK == 1 )); then
    warn "Installation failed; restoring the previous installation and data."
    systemctl stop "$SERVICE_NAME" >/dev/null 2>&1
    if ! rm -rf "$APP_DIR" || ! mv "$PREVIOUS_APP" "$APP_DIR"; then recovery_failed=1; fi
    if [[ -d "$STAGING/previous-data" ]]; then
      if ! rm -rf "$ROLLBACK_DATA_DIR" || ! cp -a "$STAGING/previous-data" "$ROLLBACK_DATA_DIR"; then recovery_failed=1; fi
    fi
    for i in "${!BACKUP_FILES[@]}"; do
      if [[ -f "$STAGING/previous-file-$i" ]]; then
        cp -a "$STAGING/previous-file-$i" "${BACKUP_FILES[$i]}" || recovery_failed=1
      else
        rm -f "${BACKUP_FILES[$i]}" || recovery_failed=1
      fi
    done
    systemctl daemon-reload || recovery_failed=1
  fi
  if (( result != 0 && SERVICE_STOPPED == 1 )); then
    systemctl restart "$SERVICE_NAME" || recovery_failed=1
  fi
  [[ -z "$NEW_APP" ]] || rm -rf "$NEW_APP"
  if (( recovery_failed == 1 )); then
    warn "Recovery needs attention. Backups were kept at $STAGING and $PREVIOUS_APP."
  else
    [[ -z "$PREVIOUS_APP" ]] || rm -rf "$PREVIOUS_APP"
    rm -rf "$STAGING"
  fi
  return "$result"
}
trap cleanup_install EXIT

step "Downloading $ASSET ($VERSION)"
if ! fetch_release_asset "$ASSET" "$STAGING/$ASSET"; then
  fail "$EXIT_DOWNLOAD_FAILED" "Could not download $(release_url "$ASSET"). The existing installation was not touched."
fi

step "Verifying the checksum"
if ! fetch_release_asset "$ASSET.sha256" "$STAGING/$ASSET.sha256"; then
  fail "$EXIT_DOWNLOAD_FAILED" "Could not download the checksum. Refusing to install an unverified archive."
fi
EXPECTED_SUM="$(awk '{print $1}' "$STAGING/$ASSET.sha256" | head -n1)"
ACTUAL_SUM="$(sha256sum "$STAGING/$ASSET" | awk '{print $1}')"
if [[ -z "$EXPECTED_SUM" || "$EXPECTED_SUM" != "$ACTUAL_SUM" ]]; then
  fail "$EXIT_CHECKSUM_FAILED" "Checksum mismatch. Expected $EXPECTED_SUM, got $ACTUAL_SUM. Nothing was installed."
fi

step "Unpacking"
mkdir -p "$STAGING/tree"
tar -xzf "$STAGING/$ASSET" -C "$STAGING/tree" ||
  fail "$EXIT_DOWNLOAD_FAILED" "The archive could not be unpacked."
SOURCE_TREE="$STAGING/tree"
if [[ ! -d "$SOURCE_TREE/app" ]]; then
  SOURCE_TREE="$(find "$STAGING/tree" -mindepth 1 -maxdepth 1 -type d | head -n1)"
fi
[[ -d "$SOURCE_TREE/app/web/out" && -f "$SOURCE_TREE/bin/softether-manager-linux-$MANAGER_ARCH" ]] ||
  fail "$EXIT_DOWNLOAD_FAILED" "The archive did not contain a Go binary for this architecture and the built frontend."
INSTALLED_VERSION="$(cat "$SOURCE_TREE/VERSION" 2>/dev/null | head -n1 || true)"
INSTALLED_VERSION="${INSTALLED_VERSION:-unknown}"

# ---------------------------------------------------------------- install

# Prepare the entire application on the destination filesystem before stopping the old service.
step "Preparing the new application"
mkdir -p "$(dirname "$APP_DIR")"
NEW_APP="$(mktemp -d "${APP_DIR}.new.XXXXXX")"
chmod 0755 "$NEW_APP"
install -m 0755 "$SOURCE_TREE/bin/softether-manager-linux-$MANAGER_ARCH" "$NEW_APP/softether-manager"
mkdir -p "$NEW_APP/app/web" "$NEW_APP/scripts"
cp -a "$SOURCE_TREE/app/web/out" "$NEW_APP/app/web/out"
[[ -d "$SOURCE_TREE/scripts" ]] && cp -a "$SOURCE_TREE/scripts/." "$NEW_APP/scripts/"
[[ -f "$SOURCE_TREE/VERSION" ]] && cp -a "$SOURCE_TREE/VERSION" "$NEW_APP/VERSION"
[[ -f "$SOURCE_TREE/README.md" ]] && cp -a "$SOURCE_TREE/README.md" "$NEW_APP/README.md"

if (( UPGRADE_EXISTING == 1 )) && [[ -d "$APP_DIR" ]]; then
  step "Stopping the service and backing up the current installation"
  systemctl stop "$SERVICE_NAME"
  SERVICE_STOPPED=1
  ROLLBACK_DATA_DIR="$(read_env_value "$ENV_PATH" SEM_DATA_DIR || true)"
  ROLLBACK_DATA_DIR="${ROLLBACK_DATA_DIR:-$DATA_DIR}"
  [[ ! -d "$ROLLBACK_DATA_DIR" ]] || cp -a "$ROLLBACK_DATA_DIR" "$STAGING/previous-data"
  for i in "${!BACKUP_FILES[@]}"; do
    [[ ! -f "${BACKUP_FILES[$i]}" ]] || cp -a "${BACKUP_FILES[$i]}" "$STAGING/previous-file-$i"
  done
  PREVIOUS_APP="$(mktemp -d "${APP_DIR}.previous.XXXXXX")"
  rmdir "$PREVIOUS_APP"
  mv "$APP_DIR" "$PREVIOUS_APP"
  ROLLBACK=1
fi
step "Installing to $APP_DIR"
mv "$NEW_APP" "$APP_DIR"
NEW_APP=""

# The data directory holds the SQLite database and the generated keys. It is
# created here so the service's first start writes into a directory that
# already has the right owner and mode.
install -d -m 0700 "$DATA_DIR"

# ---------------------------------------------------------------- environment
#
# This file holds deployment facts only -- where the data lives, what the
# service is called, which repository to follow, the seed port and web path.
# It holds no secret and no password: the session and encryption keys are
# generated by the panel into the data directory on first start, and the login
# password is in the database, changeable from the panel and 'sem password'.

write_env_file() {
  step "Writing $ENV_PATH"
  umask 022
  cat > "$ENV_PATH" <<ENV
# Read by systemd and handed to the SoftEther Manager service at startup.
#
# Only deployment facts live here -- no passwords, no keys. SEM_WEB_PATH is a
# seed; the live value lives in the database, which the service can write and
# this file it cannot. Change a line and restart the service, and the panel
# adopts it. 'sem status' reports what is actually in effect.
SEM_BIND_HOST=$BIND_ADDRESS
SEM_BIND_PORT=$PORT
SEM_WEB_PATH=$WEB_PATH

SEM_DATA_DIR=$DATA_DIR
SEM_SERVICE_NAME=$SERVICE_NAME
SEM_RELEASE_REPO=$REPO
SEM_ENV_FILE=$ENV_PATH
SEM_CLI_PATH=$CLI_PATH
SEM_CLI_ENV_PATH=$CLI_ENV_PATH
ENV
  chmod 0600 "$ENV_PATH"
}

if (( UPGRADE_EXISTING == 1 )) && [[ -f "$ENV_PATH" ]]; then
  BIND_ADDRESS="$(read_env_value "$ENV_PATH" SEM_BIND_HOST || true)"; BIND_ADDRESS="${BIND_ADDRESS:-0.0.0.0}"
  PORT="$(read_env_value "$ENV_PATH" SEM_BIND_PORT || true)"
  WEB_PATH="$(read_env_value "$ENV_PATH" SEM_WEB_PATH || true)"
  # A release that added a setting needs it present.
  grep -q '^SEM_RELEASE_REPO=' "$ENV_PATH" || printf 'SEM_RELEASE_REPO=%s\n' "$REPO" >> "$ENV_PATH"
  grep -q '^SEM_DATA_DIR=' "$ENV_PATH" || printf 'SEM_DATA_DIR=%s\n' "$DATA_DIR" >> "$ENV_PATH"
  grep -q '^SEM_SERVICE_NAME=' "$ENV_PATH" || printf 'SEM_SERVICE_NAME=%s\n' "$SERVICE_NAME" >> "$ENV_PATH"
  grep -q '^SEM_ENV_FILE=' "$ENV_PATH" || printf 'SEM_ENV_FILE=%s\n' "$ENV_PATH" >> "$ENV_PATH"
  grep -q '^SEM_CLI_PATH=' "$ENV_PATH" || printf 'SEM_CLI_PATH=%s\n' "$CLI_PATH" >> "$ENV_PATH"
  grep -q '^SEM_CLI_ENV_PATH=' "$ENV_PATH" || printf 'SEM_CLI_ENV_PATH=%s\n' "$CLI_ENV_PATH" >> "$ENV_PATH"
else
  write_env_file
fi
chmod 0600 "$ENV_PATH"

# Where the panel actually serves is asked of the panel, not assumed from the
# file: on an upgrade the two can differ, because an operator may have moved
# the web path from the Settings page, which the panel stores in its database
# and cannot write to /etc.
resolve_address() {
  local json
  json="$(cd "$APP_DIR" && set -a && . "$ENV_PATH" && set +a && "$MANAGER_BIN" address 2>/dev/null)" || return 1
  [[ -n "$json" ]] || return 1
  local resolved_port resolved_path
  resolved_port="$(printf '%s' "$json" | sed -n 's/.*"port"[[:space:]]*:[[:space:]]*\([0-9]\{1,\}\).*/\1/p' | head -n1)"
  resolved_path="$(printf '%s' "$json" | sed -n 's/.*"web_path"[[:space:]]*:[[:space:]]*"\([^"]*\)".*/\1/p' | head -n1)"
  [[ -n "$resolved_port" ]] || return 1
  PORT="$resolved_port"
  WEB_PATH="$resolved_path"
  return 0
}

if (( UPGRADE_EXISTING == 1 )); then
  if resolve_address; then
    step "The panel is configured for port $PORT and web path '${WEB_PATH:-(root)}'"
  else
    warn "The panel could not report where it listens; using $ENV_PATH, which may be out of date."
  fi
fi

# ---------------------------------------------------------------- the CLI

step "Installing the $CLI_NAME management CLI"
install -d -m 0755 "$SUPPORT_DIR"
if [[ -f "$SOURCE_TREE/scripts/sem" ]]; then
  install -m 0755 "$SOURCE_TREE/scripts/sem" "$CLI_PATH"
else
  warn "This release does not carry the $CLI_NAME CLI, so it was not installed."
fi

# A copy of this script, so uninstalling and reinstalling do not need the
# network. $0 is a real file when run as `bash install.sh`, and a consumed
# pipe when run as `bash <(curl ...)`; the release's own copy covers both.
if [[ -f "$SOURCE_TREE/scripts/install.sh" ]]; then
  install -m 0755 "$SOURCE_TREE/scripts/install.sh" "$CACHED_INSTALLER"
elif [[ -f "$0" ]] && cp -f "$0" "$CACHED_INSTALLER" 2>/dev/null; then
  chmod 0755 "$CACHED_INSTALLER"
else
  rm -f "$CACHED_INSTALLER"
fi

if [[ -z "$INSTALLER_URL" ]]; then
  case "$RELEASE_BASE" in
    */releases/download) INSTALLER_URL="https://raw.githubusercontent.com/$REPO/master/scripts/install.sh" ;;
    *) INSTALLER_URL="${RELEASE_BASE%/dist/release}/scripts/install.sh" ;;
  esac
fi
[[ -n "$INSTALLER_URL" ]] || INSTALLER_URL="$DEFAULT_INSTALLER_URL"

umask 022
cat > "$CLI_ENV_PATH" <<CLIENV
# Written by the installer so sem and the panel know where this installation
# came from, rather than assuming a default that may point somewhere else.
RELEASE_REPO=$REPO
RELEASE_BASE=$RELEASE_BASE
INSTALLER_URL=$INSTALLER_URL
INSTALLED_VERSION=$INSTALLED_VERSION
APP_DIR=$APP_DIR
ENV_PATH=$ENV_PATH
DATA_DIR=$DATA_DIR
SERVICE_NAME=$SERVICE_NAME
CLIENV
chmod 0644 "$CLI_ENV_PATH"

# ------------------------------------------------------------- the VPN server
#
# Only on a fresh install: on an upgrade the panel already has a connection,
# which may well point at a SoftEther somewhere other than this machine, and
# installing one here would be answering a question nobody asked.

SE_PRESENT_ALREADY=0
SE_CONNECTED=0
if (( UPGRADE_EXISTING == 0 && INSTALL_SOFTETHER == 1 )); then
  if softether_present; then
    SE_PRESENT_ALREADY=1
    step "A SoftEther VPN Server is already installed here; leaving it as it is"
  else
    install_softether || true
  fi
fi

# ---------------------------------------------------------------- the unit

step "Installing the systemd unit"
cat > "$UNIT_PATH" <<UNIT
[Unit]
Description=SoftEther Manager
Documentation=https://github.com/$REPO
After=network-online.target
Wants=network-online.target

[Service]
# Type=exec reports startup only after the Go executable has been started.
Type=exec
WorkingDirectory=$APP_DIR
EnvironmentFile=$ENV_PATH
ExecStart=$APP_DIR/softether-manager serve
Restart=always
RestartSec=3s

# The panel runs as root because updating and restarting itself go through
# systemd-run. The hardening below is what is safe with that:
#
#   NoNewPrivileges  - it never needs privileges it did not start with
#   ProtectHome      - it has no business in /home or /root
#   ProtectSystem=full - /usr, /boot AND /etc read-only. This is why the live
#                      web path lives in the database: the panel cannot write
#                      the environment file that seeded it.
#   PrivateTmp       - its temporary files are its own
#
# The one directory it must write is the data directory, which holds the
# SQLite database and the generated keys.
User=root
NoNewPrivileges=yes
ProtectHome=yes
ProtectSystem=full
PrivateTmp=yes
ReadWritePaths=$DATA_DIR

[Install]
WantedBy=multi-user.target
UNIT

# Create the account before enabling or opening the network listener.
ACCOUNT_CREATED=0
if (( UPGRADE_EXISTING == 0 )) && [[ -n "$USERNAME" ]]; then
  step "Creating the operator account"
  EXISTING_USERS="$(cd "$APP_DIR" && set -a && . "$ENV_PATH" && set +a && "$MANAGER_BIN" users)"
  if [[ -n "$EXISTING_USERS" ]]; then
    warn "An account already exists; its password was kept."
  else
    (cd "$APP_DIR" && set -a && . "$ENV_PATH" && set +a &&
      printf '%s' "$PASSWORD" | "$MANAGER_BIN" password --username "$USERNAME" --password-stdin) >/dev/null ||
      fail "$EXIT_SERVICE_FAILED" "Could not create the operator account; the panel was not started."
    ACCOUNT_CREATED=1
  fi
fi

systemctl daemon-reload
systemctl enable "$SERVICE_NAME" >/dev/null 2>&1 || true
step "Starting the service"
systemctl restart "$SERVICE_NAME" ||
  fail "$EXIT_SERVICE_FAILED" "systemctl could not start $SERVICE_NAME. Run: journalctl -u $SERVICE_NAME -n 50"

# ---------------------------------------------------------------- readiness

HEALTH_HOST="$BIND_ADDRESS"
[[ "$HEALTH_HOST" == "0.0.0.0" || "$HEALTH_HOST" == "::" ]] && HEALTH_HOST="127.0.0.1"
HEALTH_URL="http://$HEALTH_HOST:$PORT$(base_path "$WEB_PATH")api/v1/system/health"

step "Waiting for the panel to answer"
READY=0
for _ in $(seq 1 60); do
  if [[ "$DOWNLOADER" == "curl" ]]; then
    if curl -fsS --max-time 3 "$HEALTH_URL" >/dev/null 2>&1; then READY=1; break; fi
  else
    if wget -q --timeout=3 -O /dev/null "$HEALTH_URL" 2>/dev/null; then READY=1; break; fi
  fi
  sleep 1
done

if [[ $READY -eq 0 ]]; then
  say ""
  systemctl status "$SERVICE_NAME" --no-pager -n 25 >&2 || true
  fail "$EXIT_SERVICE_FAILED" "The service started but the panel never answered at $HEALTH_URL."
fi

ROLLBACK=0
SERVICE_STOPPED=0

# ---------------------------------------------------------------- result

PUBLIC_HOST="$BIND_ADDRESS"
if [[ "$PUBLIC_HOST" == "0.0.0.0" || "$PUBLIC_HOST" == "::" ]]; then
  PUBLIC_HOST="$(hostname -I 2>/dev/null | awk '{print $1}')"
  [[ -n "$PUBLIC_HOST" ]] || PUBLIC_HOST="127.0.0.1"
fi
PANEL_URL="http://$PUBLIC_HOST:$PORT$(base_path "$WEB_PATH")"
# A panel that already has a domain with a certificate is reached there.
if (( UPGRADE_EXISTING == 1 )); then
  ADDRESS_JSON="$(cd "$APP_DIR" && set -a && . "$ENV_PATH" && set +a && "$MANAGER_BIN" address 2>/dev/null || true)"
  if [[ -n "$ADDRESS_JSON" ]]; then
    PANEL_DOMAIN="$(printf '%s' "$ADDRESS_JSON" | sed -n 's/.*"domain"[[:space:]]*:[[:space:]]*"\([^"]*\)".*/\1/p' | head -n1)"
    PANEL_HTTPS_PORT="$(printf '%s' "$ADDRESS_JSON" | sed -n 's/.*"https_port"[[:space:]]*:[[:space:]]*\([0-9]\{1,\}\).*/\1/p' | head -n1)"
    if [[ -n "$PANEL_DOMAIN" ]] && printf '%s' "$ADDRESS_JSON" | grep -q '"https_ready"[[:space:]]*:[[:space:]]*true'; then
      if [[ "${PANEL_HTTPS_PORT:-443}" == "443" ]]; then
        PANEL_URL="https://$PANEL_DOMAIN$(base_path "$WEB_PATH")"
      else
        PANEL_URL="https://$PANEL_DOMAIN:$PANEL_HTTPS_PORT$(base_path "$WEB_PATH")"
      fi
    fi
  fi
fi

if [[ $JSON_OUTPUT -eq 1 ]]; then
  printf '{"action":"%s","url":"%s","port":%s,"web_path":"%s","served_at_root":%s,"username":"%s","account_created":%s,"service":"%s","version":"%s","app_dir":"%s","data_dir":"%s","cli":"%s","softether_installed":%s,"softether_present":%s,"softether_connected":%s}\n' \
    "$([[ $UPGRADE_EXISTING -eq 1 ]] && echo upgrade || echo install)" \
    "$PANEL_URL" "$PORT" "$WEB_PATH" \
    "$([[ -z "$WEB_PATH" ]] && echo true || echo false)" \
    "$USERNAME" "$([[ $ACCOUNT_CREATED -eq 1 ]] && echo true || echo false)" \
    "$SERVICE_NAME" "$INSTALLED_VERSION" "$APP_DIR" "$DATA_DIR" "$CLI_PATH" \
    "$([[ $SE_INSTALLED -eq 1 ]] && echo true || echo false)" \
    "$([[ $SE_PRESENT_ALREADY -eq 1 ]] && echo true || echo false)" \
    "$([[ $SE_CONNECTED -eq 1 ]] && echo true || echo false)"
fi

say ""
step "The panel is running."
say ""
say "  URL:      $PANEL_URL"
if [[ -z "$WEB_PATH" ]]; then
  say "            (no web path: the panel is served at the root)"
fi
if (( UPGRADE_EXISTING == 0 )); then
  say "  Login:    $USERNAME"
fi
say "  Service:  $SERVICE_NAME"
if (( SE_CONNECTED == 1 )); then
  say "  VPN:      SoftEther on 127.0.0.1:$SE_MGMT_PORT ($SE_SERVICE service) -- already connected"
elif (( SE_INSTALLED == 1 )); then
  say "  VPN:      SoftEther installed; connect it at 127.0.0.1:$SE_MGMT_PORT"
elif (( SE_PRESENT_ALREADY == 1 )); then
  say "  VPN:      a SoftEther was already here and was left alone; connect it from the panel"
fi
say "  Files:    $APP_DIR"
say "  Data:     $DATA_DIR   (one SQLite file -- back this up)"
say "  Version:  $INSTALLED_VERSION"
if [[ -x "$CLI_PATH" ]]; then
  say "  Manage:   $CLI_NAME   (or run this installer again with no arguments)"
fi
say ""

case "$BIND_ADDRESS" in
  127.*|::1|localhost) ;;
  *)
    if [[ "$PANEL_URL" != https://* ]]; then
      warn "The panel is bound to $BIND_ADDRESS and serves plain HTTP."
      warn "Passwords and session tokens will cross the network unencrypted."
      warn "Give it a domain under Settings -> Domain & HTTPS (or 'sem domain set <name>')"
      warn "and it obtains a Let's Encrypt certificate and serves HTTPS on its own."
    fi
    ;;
esac

exit "$EXIT_OK"
