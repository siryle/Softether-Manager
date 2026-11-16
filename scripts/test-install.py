#!/usr/bin/env python3
"""Exercise the installer in a temporary filesystem with fake system services."""
import hashlib
import io
import json
import os
from pathlib import Path
import subprocess
import tarfile
import tempfile
import unittest

ROOT = Path(__file__).resolve().parent.parent
SOURCE = (ROOT / "scripts/install.sh").read_text()


class InstallerTests(unittest.TestCase):
    def setUp(self):
        self.temp = tempfile.TemporaryDirectory()
        self.addCleanup(self.temp.cleanup)
        self.root = Path(self.temp.name)
        self.bin = self.root / "bin"
        self.bin.mkdir()
        self.env = dict(os.environ, PATH=str(self.bin) + os.pathsep + os.environ["PATH"],
                        TEST_INSTALL_ROOT=str(self.root), EXPECTED_PASSWORD='  test "password" \\ 密码  ')
        self.executable("uname", '#!/bin/sh\ncase "$1" in -s) echo Linux;; -m) echo x86_64;; esac\n')
        self.executable("systemctl", '''#!/bin/sh
printf '%s\n' "$*" >> "$TEST_INSTALL_ROOT/services.log"
if [ "$1" = restart ] && [ "$2" = softether-manager ]; then
  test -f "$TEST_INSTALL_ROOT/data/account" || exit 1
fi
''')
        self.executable("curl", '''#!/bin/sh
case "$*" in *auth/setup*) echo 'network setup must never run' >&2; exit 1;; esac
exit 0
''')
        for name in ("apt-get", "dnf", "yum", "ss", "netstat"):
            self.executable(name, "#!/bin/sh\nexit 0\n")
        text = SOURCE
        for original, replacement in {
            "/opt/softether-manager": self.root / "app",
            "/var/lib/softether-manager": self.root / "data",
            "/etc/softether-manager.env": self.root / "panel.env",
            "/etc/systemd/system": self.root / "units",
            "/usr/local/bin/sem": self.root / "sem",
            "/usr/local/share/softether-manager": self.root / "support",
            "/opt/vpnserver": self.root / "vpnserver",
        }.items():
            text = text.replace(original, str(replacement))
        # Bypass only platform checks; all install logic runs unchanged in the sandbox.
        text = text.replace('[[ $EUID -eq 0 ]]', 'true', 1)
        text = text.replace('[[ -d /run/systemd/system ]]', 'true', 1)
        (self.root / "units").mkdir()
        self.script = self.root / "install.sh"
        self.script.write_text(text)
        self.release = self.root / "releases" / "vtest"
        self.release.mkdir(parents=True)
        archive = self.release / "softether-manager.tar.gz"
        manager = '''#!/usr/bin/env python3
import json, os, sys
from pathlib import Path
args = sys.argv[1:]
root = Path(os.environ["TEST_INSTALL_ROOT"])
account = root / "data/account"
if args[0] == "password":
    assert "--password-stdin" in args and "--password" not in args
    assert sys.stdin.read() == os.environ["EXPECTED_PASSWORD"]
    if os.environ.get("FAIL_ACCOUNT"): sys.exit(1)
    account.write_text("operator")
    print("Account operator updated.")
elif args[0] == "users":
    if account.exists(): print(account.read_text())
elif args[0] == "address":
    print(json.dumps({"port": 18080, "web_path": "secret", "domain": "", "https_ready": False}))
else:
    raise AssertionError(args)
'''
        with tarfile.open(archive, "w:gz") as tar:
            for name, content, mode in [
                ("app/web/out/index.html", "test page", 0o644),
                ("bin/softether-manager-linux-amd64", manager, 0o755),
                ("scripts/sem", "#!/bin/sh\n", 0o755),
                ("scripts/install.sh", text, 0o755),
                ("VERSION", "1.0.0\n", 0o644),
            ]:
                data = content.encode()
                entry = tarfile.TarInfo(name)
                entry.size, entry.mode = len(data), mode
                tar.addfile(entry, io.BytesIO(data))
        digest = hashlib.sha256(archive.read_bytes()).hexdigest()
        (self.release / "softether-manager.tar.gz.sha256").write_text(digest + "\n")

    def executable(self, name, content):
        path = self.bin / name
        path.write_text(content)
        path.chmod(0o755)

    def run_install(self, *extra):
        return subprocess.run([
            "bash", str(self.script), "--no-menu", "--no-softether", "--non-interactive",
            "--username", "operator", "--password-stdin",
            "--port", "18080", "--path", "secret", "--bind", "127.0.0.1",
            "--version", "vtest", "--release-base", str(self.root / "releases"),
            "--json", *extra,
        ], env=self.env, input=self.env["EXPECTED_PASSWORD"], capture_output=True, text=True, timeout=15)

    def test_account_precedes_listener_and_upgrade_preserves_it(self):
        first = self.run_install()
        self.assertEqual(first.returncode, 0, first.stderr)
        self.assertTrue(json.loads(first.stdout)["account_created"])
        self.assertEqual((self.root / "panel.env").stat().st_mode & 0o777, 0o600)
        (self.root / "panel.env").chmod(0o644)
        second = self.run_install("--upgrade")
        self.assertEqual(second.returncode, 0, second.stderr)
        self.assertFalse(json.loads(second.stdout)["account_created"])
        self.assertEqual((self.root / "data/account").read_text(), "operator")
        self.assertEqual((self.root / "panel.env").stat().st_mode & 0o777, 0o600)

    def test_failed_account_never_starts_listener(self):
        self.env["FAIL_ACCOUNT"] = "1"
        result = self.run_install()
        self.assertEqual(result.returncode, 17, result.stderr)
        log = self.root / "services.log"
        services = log.read_text() if log.exists() else ""
        self.assertNotIn("restart softether-manager", services)
        self.assertNotIn("enable softether-manager", services)

    def test_bad_panel_checksum_never_installs(self):
        (self.release / "softether-manager.tar.gz.sha256").write_text("0" * 64)
        result = self.run_install()
        self.assertEqual(result.returncode, 16, result.stderr)
        self.assertFalse((self.root / "app").exists())

    def test_bad_softether_checksum_never_unpacks_or_builds(self):
        # Execute the production VPN installer function with a deliberately bad archive.
        functions = SOURCE[SOURCE.index("# Use only archives"):SOURCE.index("# --------------------------------------------------------------- uninstall")]
        script = '''set -euo pipefail
SE_VERSION=v4.44-9807-rtm
SE_DATE=2025.04.16
PACKAGE_MANAGER=''
SE_DIR="$TEST_INSTALL_ROOT/vpnserver"
step() { :; }
warn() { :; }
fetch() { printf 'tampered archive' > "$2"; }
make() { touch "$TEST_INSTALL_ROOT/make-ran"; }
tar() { touch "$TEST_INSTALL_ROOT/tar-ran"; }
''' + functions + '\nif install_softether; then exit 99; fi\n'
        result = subprocess.run(["bash", "-c", script], env=self.env, capture_output=True, text=True, timeout=10)
        self.assertEqual(result.returncode, 0, result.stderr)
        self.assertFalse((self.root / "tar-ran").exists())
        self.assertFalse((self.root / "make-ran").exists())


if __name__ == "__main__":
    unittest.main()
