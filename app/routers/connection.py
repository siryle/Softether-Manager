"""How the panel reaches its SoftEther server: /server/connection.

The panel manages the instance on the machine it is installed on, so the
connection is 127.0.0.1 and a management port -- stored once, changed rarely.
The host stays editable for the odd deployment where SoftEther listens
elsewhere (a container, a jail, a second machine during a migration), but the
default is always the machine itself.
"""
from __future__ import annotations

from typing import Any, Optional

from fastapi import APIRouter, HTTPException
from pydantic import BaseModel, Field

from Library import softether

from ..audit import record
from ..deps import CurrentUser
from ..se import build_client, connection, rpc, set_connection, stored_password, to_http_error

router = APIRouter(prefix="/server", tags=["connection"])


class ConnectionIn(BaseModel):
    host: str = Field(default="127.0.0.1", min_length=1, max_length=255)
    port: int = Field(default=5555, ge=1, le=65535)
    # None keeps the stored password; a string replaces it.
    password: Optional[str] = Field(default=None, max_length=256)
    auth: Optional[str] = Field(default=None, max_length=16)
    skip_test: bool = False


def _test(host: str, port: int, password: str, auth: Optional[str] = None) -> dict[str, Any]:
    modes = [auth] if auth in ("header", "basic", "both") else ["header", "basic", "both"]
    last_exc: Optional[Exception] = None
    for mode in modes:
        client = build_client(host, port, password, timeout=10.0, auth=mode)
        try:
            info = client.call("GetServerInfo", {}).raw
            status = client.call("GetServerStatus", {}).raw
            client.close()
            return {
                "ok": True,
                "auth": mode,
                "version": info.get("ServerVersionString_str", ""),
                "hostname": info.get("ServerHostName_str", ""),
                "sessions": status.get("NumSessionsTotal_u32", 0),
            }
        except softether.SoftEtherError as exc:
            last_exc = exc
            client.close()
            continue
    if last_exc:
        raise to_http_error(last_exc) from last_exc
    raise HTTPException(status_code=502, detail="Connection test failed.")


@router.get("/connection")
def get_connection(user: dict = CurrentUser) -> dict[str, Any]:
    return connection()


@router.put("/connection")
def put_connection(body: ConnectionIn, user: dict = CurrentUser) -> dict[str, Any]:
    password = body.password if body.password is not None else stored_password()
    auth_mode = body.auth
    if not body.skip_test:
        test_res = _test(body.host.strip(), body.port, password or "", auth=body.auth)
        if test_res.get("auth"):
            auth_mode = test_res["auth"]
    set_connection(body.host, body.port, body.password, auth=auth_mode)
    record(user, "connection.updated", "server", "", f"{body.host}:{body.port}")
    return connection()


class ConnectionTest(BaseModel):
    host: str = "127.0.0.1"
    port: int = 5555
    password: str = ""
    auth: Optional[str] = None


@router.post("/connection/test")
def test_connection(body: ConnectionTest, user: dict = CurrentUser) -> dict[str, Any]:
    """Try credentials without saving anything. An empty password tries the
    stored one if configured, or tests with blank password if unconfigured."""
    pwd = body.password
    if pwd == "" and stored_password():
        pwd = stored_password()
    return _test(body.host.strip(), body.port, pwd or "", auth=body.auth)


@router.post("/probe")
def probe(user: dict = CurrentUser) -> dict[str, Any]:
    """A quick liveness answer: reachable, version, load. 200 either way --
    unreachable is an answer, not an error, on a screen that polls."""
    info_state = connection()
    if not info_state["configured"]:
        return {"online": False, "configured": False, "error": "not connected yet"}
    try:
        info = rpc("GetServerInfo")
        status = rpc("GetServerStatus")
    except HTTPException as exc:
        detail = exc.detail if isinstance(exc.detail, dict) else {"message": str(exc.detail)}
        return {"online": False, "configured": True, "error": detail.get("message", "unreachable")}
    return {
        "online": True,
        "configured": True,
        "version": info.get("ServerVersionString_str", ""),
        "server_type": info.get("ServerType_u32", 0),
        "hostname": info.get("ServerHostName_str", ""),
        "sessions": status.get("NumSessionsTotal_u32", 0),
        "users": status.get("NumUsers_u32", 0),
        "hubs": status.get("NumHubTotal_u32", 0),
        "send_bytes": status.get("SendBytes_u64", 0),
        "recv_bytes": status.get("RecvBytes_u64", 0),
        "started": status.get("StartTime_dt", None),
        "current_time": status.get("CurrentTime_dt", None),
    }
