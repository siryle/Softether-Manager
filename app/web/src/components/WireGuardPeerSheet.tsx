"use client";

import { useEffect, useId, useRef, useState } from "react";
import { ApiError, useServerApi, type Wire, type WireGuardPeer, type WireGuardState } from "../lib/api";
import { useI18n } from "../lib/i18n";
import { useToast } from "../lib/toast";
import { downloadText } from "../lib/util";
import { IconCheck, IconCopy, IconWireGuardDownload } from "../ui/Icon";
import { Sheet } from "../ui/Sheet";
import { ErrorAlert, Field } from "./bits";

let lastCopyAnnouncement = "";

function legacyCopy(text: string): boolean {
  const field = Object.assign(document.createElement("textarea"), { value: text });
  field.setAttribute("readonly", "");
  field.style.cssText = "position:fixed;inset-block-start:0;opacity:0";
  document.body.append(field);
  field.select();
  try {
    return document.execCommand("copy");
  } finally {
    field.remove();
  }
}

/**
 * Accessible copy button: keeps the live region outside the button, never
 * disables itself during feedback, and falls back only when clipboard fails.
 */
export function CopyTextButton({
  text,
  label = "Copy",
  ariaLabel,
  className = "btn btn--sm",
}: {
  text: string;
  label?: string;
  ariaLabel?: string;
  className?: string;
}) {
  const { t } = useI18n();
  const [state, setState] = useState<"idle" | "copied" | "failed">("idle");
  const [liveText, setLiveText] = useState("");
  const timerRef = useRef<number | null>(null);

  useEffect(() => () => {
    if (timerRef.current !== null) window.clearTimeout(timerRef.current);
  }, []);

  const announce = (msg: string) => {
    const next = msg === lastCopyAnnouncement ? `${msg}\u00A0` : msg;
    lastCopyAnnouncement = msg;
    setLiveText("");
    requestAnimationFrame(() => setLiveText(next));
  };

  const handleCopy = async () => {
    if (!text) return;
    if (timerRef.current !== null) window.clearTimeout(timerRef.current);
    let ok = false;
    try {
      await navigator.clipboard.writeText(text);
      ok = true;
    } catch {
      ok = legacyCopy(text);
    }
    setState(ok ? "copied" : "failed");
    announce(t(ok ? "Copied" : "Copy failed"));
    timerRef.current = window.setTimeout(() => {
      setState("idle");
      setLiveText("");
    }, 2000);
  };

  const buttonText = state === "copied" ? t("Copied") : state === "failed" ? t("Copy failed") : t(label);

  return (
    <span style={{ display: "inline-flex", alignItems: "center", gap: "var(--s1)" }}>
      <button
        type="button"
        className={className}
        onClick={() => void handleCopy()}
        aria-label={ariaLabel ? t(ariaLabel) : undefined}
        data-copied={state === "copied" ? "" : undefined}
        data-failed={state === "failed" ? "" : undefined}
      >
        {state === "copied" ? <IconCheck size={14} aria-hidden="true" /> : <IconCopy size={14} aria-hidden="true" />}
        <span>{buttonText}</span>
      </button>
      <span
        role="status"
        style={{
          position: "absolute",
          clipPath: "inset(50%)",
          overflow: "hidden",
          width: 1,
          height: 1,
          margin: -1,
          whiteSpace: "nowrap",
        }}
      >
        {liveText}
      </span>
    </span>
  );
}

/** Poll until SoftEther comes back online after a SetConfig restart. */
export async function waitForWireGuardReady(
  api: ReturnType<typeof useServerApi>,
  check?: (fresh: WireGuardState) => boolean,
): Promise<WireGuardState> {
  const deadline = Date.now() + 45000;
  while (Date.now() < deadline) {
    await new Promise((resolve) => window.setTimeout(resolve, 1000));
    try {
      const fresh = await api.wireguard();
      if (!check || check(fresh)) return fresh;
    } catch (error) {
      if (error instanceof ApiError && error.seError !== "TransportError") throw error;
    }
  }
  throw new Error("The VPN server has not confirmed the WireGuard change yet. Check its status and retry.");
}

export function WireGuardAddPeerSheet({
  state,
  fixedHub,
  fixedUser,
  onClose,
  onChanged,
}: {
  state: WireGuardState;
  fixedHub?: string;
  fixedUser?: string;
  onClose: () => void;
  onChanged: (fresh?: WireGuardState) => void;
}) {
  const api = useServerApi();
  const { t } = useI18n();
  const { push } = useToast();
  const uid = useId();

  const hubs = (state.hub_networks ?? []).map((h) => h.hub);
  const [hub, setHub] = useState(fixedHub || hubs[0] || "");
  const [users, setUsers] = useState<string[]>(fixedUser ? [fixedUser] : []);
  const [user, setUser] = useState(fixedUser || "");
  const [keyMode, setKeyMode] = useState<"generate" | "paste">("generate");
  const [publicKey, setPublicKey] = useState("");
  const [label, setLabel] = useState("");
  const [assignedIp, setAssignedIp] = useState("");
  const [endpointHost, setEndpointHost] = useState(state.default_endpoint_host || "");
  const [endpointPort, setEndpointPort] = useState(String(state.default_udp_port || 51820));
  const [dns, setDns] = useState("1.1.1.1");
  const [allowedIps, setAllowedIps] = useState("0.0.0.0/0, ::/0");
  const [keepalive, setKeepalive] = useState("21");
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState<string | null>(null);
  const [created, setCreated] = useState<{
    peer: WireGuardPeer;
    client_private_key?: string;
    client_config: string;
  } | null>(null);

  useEffect(() => {
    if (fixedUser || !hub) return;
    let stale = false;
    void api
      .users(hub)
      .then((r) => {
        if (stale) return;
        const list = ((r.UserList as Wire[]) ?? []).map((u) => String(u.Name_str)).sort();
        setUsers(list);
        setUser((prev) => (list.includes(prev) ? prev : list[0] ?? ""));
      })
      .catch(() => {
        if (!stale) setUsers([]);
      });
    return () => {
      stale = true;
    };
  }, [api, fixedUser, hub]);

  const submit = async (e: React.FormEvent) => {
    e.preventDefault();
    if (!hub || !user) return;
    if (keyMode === "paste" && !publicKey.trim()) {
      setError(t("Paste the client's WireGuard public key, or choose auto-generate."));
      return;
    }
    const portNum = Number(endpointPort);
    if (!Number.isInteger(portNum) || portNum < 1 || portNum > 65535) {
      setError(t("The port must be a number between 1 and 65535."));
      return;
    }

    setBusy(true);
    setError(null);
    try {
      const res = await api.addWireguardPeer(hub, {
        user,
        generate: keyMode === "generate",
        public_key: keyMode === "paste" ? publicKey.trim() : undefined,
        label: label.trim(),
        assigned_ip: assignedIp.trim(),
        endpoint_host: endpointHost.trim(),
        endpoint_port: portNum,
        dns: dns.trim(),
        allowed_ips: allowedIps.trim(),
        persistent_keepalive: Number(keepalive) || 0,
        confirm_restart: true,
      });
      setCreated(res);
      push("ok", t("WireGuard peer added. Waiting for server restart…"));
      const fresh = await waitForWireGuardReady(api, (s) =>
        Boolean(s.peers?.some((p) => p.public_key === res.peer.public_key)),
      ).catch(() => undefined);
      onChanged(fresh);
    } catch (err) {
      setError(err instanceof Error ? err.message : String(err));
    } finally {
      setBusy(false);
    }
  };

  if (created) {
    const filename = `wg-${created.peer.hub}-${created.peer.user}.conf`;
    return (
      <Sheet
        title={t("WireGuard client ready")}
        subtitle={`${created.peer.user} · ${t("Hub")} ${created.peer.hub}`}
        onClose={onClose}
        wide
        footer={
          <>
            <CopyTextButton text={created.client_config} label="Copy config" />
            <button
              type="button"
              className="btn btn--primary"
              onClick={() => downloadText(filename, created.client_config, "text/plain")}
            >
              <IconWireGuardDownload size={14} /> {t("Download .conf")}
            </button>
            <button type="button" className="btn" onClick={onClose}>
              {t("Done")}
            </button>
          </>
        }
      >
        <div style={{ display: "grid", gap: "var(--s3)" }}>
          {created.client_private_key ? (
            <div className="alert alert--ok">
              <div>
                <div className="alert__t">{t("Save or download this configuration now")}</div>
                <div>{t("The panel generated this keypair on the fly and does not store the client private key.")}</div>
              </div>
            </div>
          ) : (
            <div className="alert">
              <div>{t("Replace <CLIENT_PRIVATE_KEY> with the matching private key on the client device.")}</div>
            </div>
          )}
          <Field label="Client public key" id={`${uid}-pub`}>
            <div style={{ display: "flex", gap: "var(--s2)", alignItems: "center" }}>
              <input id={`${uid}-pub`} className="input mono" value={created.peer.public_key} readOnly />
              <CopyTextButton text={created.peer.public_key} ariaLabel="Copy client public key" />
            </div>
          </Field>
          <Field label="WireGuard configuration (.conf)" id={`${uid}-conf`}>
            <pre
              id={`${uid}-conf`}
              className="mono"
              style={{
                margin: 0,
                padding: "var(--s3)",
                borderRadius: "var(--r2)",
                background: "var(--bg-sunk)",
                border: "1px solid var(--border)",
                fontSize: "var(--t-xs)",
                overflowX: "auto",
                whiteSpace: "pre-wrap",
                wordBreak: "break-all",
              }}
            >
              {created.client_config}
            </pre>
          </Field>
        </div>
      </Sheet>
    );
  }

  return (
    <Sheet
      title={t("Add WireGuard peer")}
      subtitle={fixedUser ? `${fixedUser} · ${t("Hub")} ${fixedHub}` : undefined}
      onClose={onClose}
      wide
      footer={
        <>
          <button type="button" className="btn" onClick={onClose} disabled={busy}>
            {t("Cancel")}
          </button>
          <button
            type="submit"
            form={`${uid}-form`}
            className="btn btn--primary"
            disabled={busy || !hub || !user}
          >
            {busy && <span className="spin" />}
            {busy ? t("Applying & restarting…") : t("Add & restart")}
          </button>
        </>
      }
    >
      <form id={`${uid}-form`} onSubmit={(e) => void submit(e)} style={{ display: "grid", gap: "var(--s2)" }}>
        {error && <ErrorAlert>{error}</ErrorAlert>}
        <div className="alert">
          <div>
            {t("Adding a WireGuard peer writes vpn_server.config via SetConfig and restarts the SoftEther service, which disconnects active sessions briefly.")}
          </div>
        </div>
        {!fixedUser && (
          <div className="row2">
            <Field label="Virtual Hub" id={`${uid}-hub`}>
              <select
                id={`${uid}-hub`}
                className="select"
                value={hub}
                onChange={(e) => setHub(e.target.value)}
              >
                {hubs.map((h) => (
                  <option key={h} value={h}>
                    {h}
                  </option>
                ))}
              </select>
            </Field>
            <Field label="User" id={`${uid}-user`}>
              <select
                id={`${uid}-user`}
                className="select"
                value={user}
                onChange={(e) => setUser(e.target.value)}
              >
                {users.length === 0 && <option value="">{t("— no users —")}</option>}
                {users.map((u) => (
                  <option key={u} value={u}>
                    {u}
                  </option>
                ))}
              </select>
            </Field>
          </div>
        )}
        <fieldset style={{ border: 0, padding: 0, margin: 0 }}>
          <legend style={{ fontSize: "var(--t-xs)", color: "var(--text-muted)", marginBottom: "var(--s1)" }}>
            {t("Key source")}
          </legend>
          <div style={{ display: "flex", gap: "var(--s3)", flexWrap: "wrap" }}>
            <label style={{ display: "inline-flex", alignItems: "center", gap: "var(--s1)", cursor: "pointer" }}>
              <input
                type="radio"
                name={`${uid}-mode`}
                checked={keyMode === "generate"}
                onChange={() => setKeyMode("generate")}
              />
              <span>{t("Generate new keypair")}</span>
            </label>
            <label style={{ display: "inline-flex", alignItems: "center", gap: "var(--s1)", cursor: "pointer" }}>
              <input
                type="radio"
                name={`${uid}-mode`}
                checked={keyMode === "paste"}
                onChange={() => setKeyMode("paste")}
              />
              <span>{t("Paste existing public key")}</span>
            </label>
          </div>
        </fieldset>
        {keyMode === "paste" && (
          <Field
            label="Client public key"
            id={`${uid}-pubkey`}
            hint="Base64-encoded 32-byte Curve25519 public key from the client device."
          >
            <input
              id={`${uid}-pubkey`}
              className="input mono"
              value={publicKey}
              onChange={(e) => setPublicKey(e.target.value)}
              autoCapitalize="none"
              autoCorrect="off"
              spellCheck={false}
              required
            />
          </Field>
        )}
        <div className="row2">
          <Field label="Device label" id={`${uid}-label`} hint="Optional note shown in the panel.">
            <input
              id={`${uid}-label`}
              className="input"
              value={label}
              onChange={(e) => setLabel(e.target.value)}
              placeholder="MacBook / iPhone"
            />
          </Field>
          <Field
            label="Tunnel IPv4"
            id={`${uid}-ip`}
            hint="Leave blank to auto-pick from the hub's static subnet."
          >
            <input
              id={`${uid}-ip`}
              className="input mono"
              value={assignedIp}
              onChange={(e) => setAssignedIp(e.target.value)}
              placeholder="192.168.30.2"
              inputMode="decimal"
              spellCheck={false}
            />
          </Field>
        </div>
        <div className="row2">
          <Field label="Endpoint host" id={`${uid}-host`}>
            <input
              id={`${uid}-host`}
              className="input mono"
              value={endpointHost}
              onChange={(e) => setEndpointHost(e.target.value)}
              autoCapitalize="none"
              autoCorrect="off"
              spellCheck={false}
              inputMode="url"
            />
          </Field>
          <Field label="Endpoint UDP port" id={`${uid}-port`}>
            <input
              id={`${uid}-port`}
              className="input mono"
              type="text"
              inputMode="numeric"
              value={endpointPort}
              onChange={(e) => setEndpointPort(e.target.value)}
            />
          </Field>
        </div>
        <div className="row2">
          <Field label="DNS" id={`${uid}-dns`}>
            <input
              id={`${uid}-dns`}
              className="input mono"
              value={dns}
              onChange={(e) => setDns(e.target.value)}
              spellCheck={false}
            />
          </Field>
          <Field label="PersistentKeepalive (s)" id={`${uid}-keep`}>
            <input
              id={`${uid}-keep`}
              className="input mono"
              type="text"
              inputMode="numeric"
              value={keepalive}
              onChange={(e) => setKeepalive(e.target.value)}
            />
          </Field>
        </div>
        <Field label="AllowedIPs" id={`${uid}-allowed`}>
          <input
            id={`${uid}-allowed`}
            className="input mono"
            value={allowedIps}
            onChange={(e) => setAllowedIps(e.target.value)}
            spellCheck={false}
          />
        </Field>
      </form>
    </Sheet>
  );
}

export function WireGuardConfigSheet({
  state,
  peer,
  onClose,
  onMetaSaved,
}: {
  state: WireGuardState;
  peer: WireGuardPeer;
  onClose: () => void;
  onMetaSaved: () => void;
}) {
  const api = useServerApi();
  const { t } = useI18n();
  const { push } = useToast();
  const uid = useId();

  const [label, setLabel] = useState(peer.label || "");
  const [assignedIp, setAssignedIp] = useState(peer.assigned_ip || "");
  const [endpointHost, setEndpointHost] = useState(state.default_endpoint_host || "");
  const [endpointPort, setEndpointPort] = useState(String(state.default_udp_port || 51820));
  const [dns, setDns] = useState("1.1.1.1");
  const [allowedIps, setAllowedIps] = useState("0.0.0.0/0, ::/0");
  const [keepalive, setKeepalive] = useState("21");
  const [clientPriv, setClientPriv] = useState("");
  const [configText, setConfigText] = useState("");
  const [filename, setFilename] = useState(`wg-${peer.hub}-${peer.user}.conf`);
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState<string | null>(null);

  const renderConfig = async (overrideIp?: string) => {
    setBusy(true);
    setError(null);
    try {
      const r = await api.wireguardClientConfig(peer.hub, {
        public_key: peer.public_key,
        assigned_ip: (overrideIp ?? assignedIp).trim(),
        endpoint_host: endpointHost.trim(),
        endpoint_port: Number(endpointPort) || 51820,
        dns: dns.trim(),
        allowed_ips: allowedIps.trim(),
        persistent_keepalive: Number(keepalive) || 0,
        client_private_key: clientPriv.trim(),
        filename: `wg-${peer.hub}-${peer.user}.conf`,
      });
      setConfigText(r.config);
      setFilename(r.filename);
      if (!assignedIp && r.assigned_ip) setAssignedIp(r.assigned_ip);
    } catch (err) {
      setError(err instanceof Error ? err.message : String(err));
    } finally {
      setBusy(false);
    }
  };

  useEffect(() => {
    void renderConfig();
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [peer.public_key]);

  const saveMeta = async () => {
    setBusy(true);
    setError(null);
    try {
      const saved = await api.updateWireguardPeerMeta(peer.hub, {
        public_key: peer.public_key,
        label: label.trim(),
        assigned_ip: assignedIp.trim(),
      });
      push("ok", t("Peer metadata saved."));
      onMetaSaved();
      await renderConfig(saved.assigned_ip);
    } catch (err) {
      setError(err instanceof Error ? err.message : String(err));
      setBusy(false);
    }
  };

  return (
    <Sheet
      title={t("WireGuard peer configuration")}
      subtitle={`${peer.user} · ${t("Hub")} ${peer.hub}`}
      onClose={onClose}
      wide
      footer={
        <>
          <button type="button" className="btn" onClick={() => void saveMeta()} disabled={busy}>
            {t("Save label / IP")}
          </button>
          <button type="button" className="btn" onClick={() => void renderConfig()} disabled={busy}>
            {t("Refresh preview")}
          </button>
          {configText && <CopyTextButton text={configText} label="Copy config" />}
          <button
            type="button"
            className="btn btn--primary"
            disabled={!configText}
            onClick={() => downloadText(filename, configText, "text/plain")}
          >
            <IconWireGuardDownload size={14} /> {t("Download .conf")}
          </button>
        </>
      }
    >
      <div style={{ display: "grid", gap: "var(--s2)" }}>
        {error && <ErrorAlert>{error}</ErrorAlert>}
        <Field label="Client public key" id={`${uid}-pub`}>
          <div style={{ display: "flex", gap: "var(--s2)", alignItems: "center" }}>
            <input id={`${uid}-pub`} className="input mono" value={peer.public_key} readOnly />
            <CopyTextButton text={peer.public_key} ariaLabel="Copy client public key" />
          </div>
        </Field>
        <div className="row2">
          <Field label="Device label" id={`${uid}-label`}>
            <input id={`${uid}-label`} className="input" value={label} onChange={(e) => setLabel(e.target.value)} />
          </Field>
          <Field label="Tunnel IPv4" id={`${uid}-ip`}>
            <input
              id={`${uid}-ip`}
              className="input mono"
              value={assignedIp}
              onChange={(e) => setAssignedIp(e.target.value)}
              inputMode="decimal"
              spellCheck={false}
            />
          </Field>
        </div>
        <div className="row2">
          <Field label="Endpoint host" id={`${uid}-host`}>
            <input
              id={`${uid}-host`}
              className="input mono"
              value={endpointHost}
              onChange={(e) => setEndpointHost(e.target.value)}
              spellCheck={false}
              autoCapitalize="none"
            />
          </Field>
          <Field label="Endpoint UDP port" id={`${uid}-port`}>
            <input
              id={`${uid}-port`}
              className="input mono"
              type="text"
              inputMode="numeric"
              value={endpointPort}
              onChange={(e) => setEndpointPort(e.target.value)}
            />
          </Field>
        </div>
        <div className="row2">
          <Field label="DNS" id={`${uid}-dns`}>
            <input id={`${uid}-dns`} className="input mono" value={dns} onChange={(e) => setDns(e.target.value)} spellCheck={false} />
          </Field>
          <Field label="AllowedIPs" id={`${uid}-allowed`}>
            <input id={`${uid}-allowed`} className="input mono" value={allowedIps} onChange={(e) => setAllowedIps(e.target.value)} spellCheck={false} />
          </Field>
        </div>
        <div className="row2">
          <Field label="PersistentKeepalive (s)" id={`${uid}-keep`}>
            <input id={`${uid}-keep`} className="input mono" type="text" inputMode="numeric" value={keepalive} onChange={(e) => setKeepalive(e.target.value)} />
          </Field>
          <Field label="Client private key (optional, never saved)" id={`${uid}-priv`}>
            <input
              id={`${uid}-priv`}
              className="input mono"
              type="password"
              value={clientPriv}
              onChange={(e) => setClientPriv(e.target.value)}
              placeholder="<CLIENT_PRIVATE_KEY>"
              autoComplete="off"
            />
          </Field>
        </div>
        {configText && (
          <Field label="WireGuard configuration (.conf)" id={`${uid}-conf`}>
            <pre
              id={`${uid}-conf`}
              className="mono"
              style={{
                margin: 0,
                padding: "var(--s3)",
                borderRadius: "var(--r2)",
                background: "var(--bg-sunk)",
                border: "1px solid var(--border)",
                fontSize: "var(--t-xs)",
                overflowX: "auto",
                whiteSpace: "pre-wrap",
                wordBreak: "break-all",
              }}
            >
              {configText}
            </pre>
          </Field>
        )}
      </div>
    </Sheet>
  );
}

export function wireguardPeerKey(hub: string, user: string) {
  return `${hub}\0${user.toLowerCase()}`;
}

export function groupWireGuardPeers(state: WireGuardState | null): Map<string, WireGuardPeer[]> {
  const map = new Map<string, WireGuardPeer[]>();
  if (!state?.supported || !state.peers?.length) return map;
  for (const peer of state.peers) {
    const k = wireguardPeerKey(peer.hub, peer.user);
    const list = map.get(k);
    if (list) list.push(peer);
    else map.set(k, [peer]);
  }
  return map;
}

export async function downloadWireGuardPeerConfig(
  api: ReturnType<typeof useServerApi>,
  state: WireGuardState,
  peer: WireGuardPeer,
) {
  const labelSlug = peer.label ? `-${peer.label.trim().replace(/[^a-zA-Z0-9._-]+/g, "-")}` : "";
  const filename = `wg-${peer.hub}-${peer.user}${labelSlug}.conf`;
  const res = await api.wireguardClientConfig(peer.hub, {
    public_key: peer.public_key,
    assigned_ip: peer.assigned_ip || "",
    endpoint_host: state.default_endpoint_host || "",
    endpoint_port: state.default_udp_port || 51820,
    dns: "1.1.1.1",
    allowed_ips: "0.0.0.0/0, ::/0",
    persistent_keepalive: 21,
    filename,
  });
  downloadText(res.filename || filename, res.config, "text/plain");
}

export function WireGuardDownloadChooserSheet({
  state,
  hub,
  user,
  peers,
  onClose,
}: {
  state: WireGuardState;
  hub: string;
  user: string;
  peers: WireGuardPeer[];
  onClose: () => void;
}) {
  const api = useServerApi();
  const { t } = useI18n();
  const { guard } = useToast();
  const [busyKey, setBusyKey] = useState<string | null>(null);
  const [editingPeer, setEditingPeer] = useState<WireGuardPeer | null>(null);

  if (editingPeer) {
    return (
      <WireGuardConfigSheet
        state={state}
        peer={editingPeer}
        onClose={() => setEditingPeer(null)}
        onMetaSaved={() => {}}
      />
    );
  }

  const downloadPeer = async (peer: WireGuardPeer) => {
    setBusyKey(peer.public_key);
    const ok = await guard(
      () => downloadWireGuardPeerConfig(api, state, peer),
      t("WireGuard configuration downloaded."),
    );
    setBusyKey(null);
    if (ok) onClose();
  };

  return (
    <Sheet
      title={t("Download WireGuard .conf")}
      subtitle={`${user} · ${t("Hub")} ${hub}`}
      onClose={onClose}
      footer={<button type="button" className="btn" onClick={onClose}>{t("Close")}</button>}
    >
      <div style={{ display: "grid", gap: "var(--s2)" }}>
        <p className="hint" style={{ margin: 0 }}>
          {t("Select a WireGuard client device below to download its .conf file.")}
        </p>
        <div style={{ display: "grid", gap: "var(--s2)" }}>
          {peers.map((peer) => {
            const busy = busyKey === peer.public_key;
            return (
              <div
                key={peer.public_key}
                className="card"
                style={{
                  padding: "var(--s3)",
                  display: "flex",
                  alignItems: "center",
                  justifyContent: "space-between",
                  gap: "var(--s3)",
                  flexWrap: "wrap",
                }}
              >
                <div style={{ minWidth: 0, display: "grid", gap: 4 }}>
                  <div style={{ display: "flex", alignItems: "center", gap: "var(--s2)", flexWrap: "wrap" }}>
                    <span className="tname mono">{peer.label || t("WireGuard peer")}</span>
                    {peer.assigned_ip && <span className="chip chip--ok mono">{peer.assigned_ip}/32</span>}
                  </div>
                  <div className="micro mono" title={peer.public_key}>
                    {peer.public_key.slice(0, 20)}…
                  </div>
                </div>
                <div style={{ display: "inline-flex", alignItems: "center", gap: "var(--s1)", flexShrink: 0 }}>
                  <button
                    type="button"
                    className="btn btn--sm"
                    onClick={() => setEditingPeer(peer)}
                    disabled={busyKey !== null}
                  >
                    {t("Customize")}
                  </button>
                  <button
                    type="button"
                    className="btn btn--sm btn--primary"
                    onClick={() => void downloadPeer(peer)}
                    disabled={busyKey !== null}
                  >
                    {busy ? <span className="spin" /> : <IconWireGuardDownload size={14} />}
                    {t("Download .conf")}
                  </button>
                </div>
              </div>
            );
          })}
        </div>
      </div>
    </Sheet>
  );
}
