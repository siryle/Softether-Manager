#!/usr/bin/env python3
"""Container healthcheck for SoftEther Manager."""
import sys
from pathlib import Path

# Ensure application root is in python path
APP_ROOT = Path(__file__).resolve().parent if (Path(__file__).resolve().parent / "app").is_dir() else Path(__file__).resolve().parents[1]
if str(APP_ROOT) not in sys.path:
    sys.path.insert(0, str(APP_ROOT))

import urllib.request
from app.config import settings

try:
    from app.settings_store import get_setting
    web_path = str(get_setting("web_path") or "").strip().strip("/")
except Exception:
    web_path = settings.normalised_web_path

prefix = f"/{web_path}" if web_path else ""
url = f"http://127.0.0.1:{settings.bind_port}{prefix}/api/v1/auth/state"

try:
    req = urllib.request.Request(url, headers={"User-Agent": "Docker-Healthcheck"})
    with urllib.request.urlopen(req, timeout=4) as resp:
        if resp.status == 200:
            sys.exit(0)
        sys.exit(1)
except Exception:
    sys.exit(1)
