"""The panel's one door to its SoftEther server.

This panel manages the SoftEther VPN Server on the machine it is installed on
-- one server, one connection. The connection facts live in the settings
store: the management host (127.0.0.1 unless deliberately pointed elsewhere),
the port, and the administrator password, Fernet-encrypted. One
:class:`SoftEtherClient` is kept alive per process (the library holds a
keep-alive HTTPS connection and serialises calls, so sharing it between
requests is both safe and much faster than reconnecting), and the library's
error taxonomy is translated into HTTP answers the frontend can act on.
"""
from __future__ import annotations

import threading
from typing import Any, Optional

from fastapi import HTTPException

from Library import softether

from .secrets_store import decrypt, encrypt
from .settings_store import get_setting, set_setting

_client: Optional[softether.SoftEtherClient] = None
_client_lock = threading.Lock()


def connection() -> dict[str, Any]:
    """The stored connection facts. The password stays out of this."""
    return {
        "host": str(get_setting("se_host") or "127.0.0.1"),
        "port": int(get_setting("se_port") or 5555),
        "auth": str(get_setting("se_auth") or "header"),
        "configured": bool(get_setting("se_password")),
    }


def set_connection(host: str, port: int, password: Optional[str], auth: Optional[str] = None) -> None:
    """Store new connection facts. ``password=None`` keeps the stored one."""
    set_setting("se_host", host.strip() or "127.0.0.1")
    set_setting("se_port", int(port))
    if auth is not None:
        set_setting("se_auth", auth)
    if password is not None:
        set_setting("se_password", encrypt(password))
    drop_client()


def stored_password() -> str:
    return decrypt(str(get_setting("se_password") or ""))


def build_client(
    host: str,
    port: int,
    password: str,
    timeout: float = 15.0,
    auth: Optional[str] = None,
) -> softether.SoftEtherClient:
    auth_mode = auth or str(get_setting("se_auth") or "header")
    if auth_mode not in ("header", "basic", "both"):
        auth_mode = "header"
    return softether.SoftEtherClient(
        host,
        port,
        password=password,
        verify=False,
        timeout=timeout,
        retries=2,
        auth=auth_mode,
        suppress_insecure_warning=True,
    )


def get_client() -> softether.SoftEtherClient:
    global _client
    with _client_lock:
        if _client is not None:
            return _client
    info = connection()
    if not info["configured"]:
        raise HTTPException(
            status_code=409,
            detail={
                "message": "The panel is not connected to SoftEther yet. Set the management "
                "port and administrator password first.",
                "se_error": "NotConfigured",
                "se_code": None,
            },
        )
    client = build_client(info["host"], info["port"], stored_password())
    with _client_lock:
        if _client is None:
            _client = client
        return _client


def drop_client() -> None:
    """Forget the cached client after the connection facts changed."""
    global _client
    with _client_lock:
        client, _client = _client, None
    if client is not None:
        try:
            client.close()
        except Exception:  # noqa: BLE001 - closing is best-effort
            pass


def rpc(method: str, params: Optional[dict[str, Any]] = None) -> dict[str, Any]:
    """Call one RPC on the managed server and return the wire-format result.

    ``RpcResult.raw`` is the server's own JSON, untouched -- datetimes stay
    ISO strings, binaries stay base64 -- which is exactly what a JSON API
    should hand to the browser.
    """
    client = get_client()
    try:
        return client.call(method, params or {}).raw
    except softether.SoftEtherError as exc:
        raise to_http_error(exc) from exc


def to_http_error(exc: softether.SoftEtherError) -> HTTPException:
    """The library's taxonomy, translated once.

    The panel is a gateway in front of the VPN server, so transport and
    authentication failures against *it* are 502s -- the browser's request was
    fine; the upstream was not. The server refusing an operation keeps its own
    meaning: 404 for a missing object, 409 for a duplicate, 403 for denied.
    """
    if isinstance(exc, softether.ValidationError):
        return HTTPException(status_code=422, detail=str(exc))
    if isinstance(exc, softether.AuthenticationError):
        return _http(502, "SoftEther 服务器拒绝了管理员密码，请检查密码是否正确 (HTTP 401/403)。", exc)
    if isinstance(exc, softether.ApiDisabledError):
        return _http(
            502,
            "SoftEther 服务器返回 404：目标服务器未启用 JSON-RPC Web API，或版本过旧（请确认 vpn_server.config 中 bool DisableJsonRpcWebApi 为 false）。",
            exc,
        )
    if isinstance(exc, softether.TransportError):
        err_msg = str(exc)
        if "Broken pipe" in err_msg or "Errno 32" in err_msg:
            return _http(
                502,
                "连接到 SoftEther 服务器失败 ([Errno 32] Broken pipe)：服务器在收到握手后切断了连接。"
                "请检查：1. 管理员密码是否正确；2. 目标服务器是否在 vpn_server.config 中启用了 JSON-RPC Web API (bool DisableJsonRpcWebApi false)；3. 管理端口是否正确 (默认5555/443/992)。",
                exc,
            )
        return _http(502, f"无法连接到 SoftEther 服务器: {exc}", exc)
    if isinstance(exc, softether.NotFoundError):
        return _http(404, str(exc), exc)
    if isinstance(exc, softether.AlreadyExistsError):
        return _http(409, str(exc), exc)
    if isinstance(exc, softether.PermissionError):
        return _http(403, str(exc), exc)
    if isinstance(exc, softether.InvalidParameterError):
        return _http(400, str(exc), exc)
    if isinstance(exc, softether.RpcError):
        return _http(400, exc.message or exc.name or str(exc), exc)
    return _http(502, str(exc), exc)


def _http(status: int, detail: str, exc: softether.SoftEtherError) -> HTTPException:
    name = getattr(exc, "name", "") or type(exc).__name__
    code = getattr(exc, "code", None)
    return HTTPException(status_code=status, detail={"message": detail, "se_error": name, "se_code": code})
