"use client";

import { useEffect, useId, useMemo, useRef, useState } from "react";
import { ApiError, useServerApi, type Wire, type WireGuardPeer, type WireGuardState } from "../lib/api";
import { useI18n } from "../lib/i18n";
import { encodeQrCode, qrSvgPath } from "../lib/qrcode";
import { useToast } from "../lib/toast";
import { downloadText } from "../lib/util";
import { IconCheck, IconCopy, IconQrCode, IconRefresh, IconWireGuardDownload } from "../ui/Icon";
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
  const [showCreatedQr, setShowCreatedQr] = useState(false);

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
    const filename = peerConfigFilename(created.peer);
    const hasPlaceholder = hasPlaceholderPrivateKey(created.client_config);
    return (
      <Sheet
        title={showCreatedQr ? t("WireGuard configuration QR code") : t("WireGuard client ready")}
        subtitle={`${created.peer.user} · ${t("Hub")} ${created.peer.hub}`}
        onClose={onClose}
        wide
        footer={
          <>
            <CopyTextButton text={created.client_config} label="Copy config" />
            <button
              type="button"
              className="btn"
              disabled={hasPlaceholder}
              onClick={() => setShowCreatedQr((v) => !v)}
            >
              <IconQrCode size={14} /> {showCreatedQr ? t("Back to details") : t("QR code")}
            </button>
            <button
              type="button"
              className="btn btn--primary"
              disabled={hasPlaceholder}
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
        {showCreatedQr ? (
          <WireGuardQrPreview configText={created.client_config} peer={created.peer} state={state} />
        ) : (
          <div style={{ display: "grid", gap: "var(--s3)" }}>
            {created.client_private_key ? (
              <div className="alert alert--ok">
                <div>
                  <div className="alert__t">{t("Client configuration ready")}</div>
                  <div>{t("The panel encrypted and saved this client private key so you can download .conf or scan its QR code anytime.")}</div>
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
        )}
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

export function peerConfigFilename(peer: Pick<WireGuardPeer, "hub" | "user" | "label">): string {
  const labelSlug = peer.label ? `-${peer.label.trim().replace(/[^a-zA-Z0-9._-]+/g, "-")}` : "";
  return `wg-${peer.hub}-${peer.user}${labelSlug}.conf`;
}

export function hasPlaceholderPrivateKey(config: string): boolean {
  return config.includes("<CLIENT_PRIVATE_KEY>");
}

function WireGuardQrBox({ configText }: { configText: string }) {
  const { t } = useI18n();
  const encoded = useMemo(() => {
    try {
      const matrix = encodeQrCode(configText);
      return { size: matrix.size + 8, path: qrSvgPath(matrix, 4), error: null as string | null };
    } catch (err) {
      return { size: 0, path: "", error: err instanceof Error ? err.message : String(err) };
    }
  }, [configText]);

  if (encoded.error) {
    return <ErrorAlert>{t(encoded.error)}</ErrorAlert>;
  }

  return (
    <div style={{ display: "grid", justifyItems: "center", gap: "var(--s2)" }}>
      <div
        style={{
          width: 248,
          maxWidth: "100%",
          padding: 12,
          borderRadius: "var(--r-card)",
          background: "#ffffff",
          border: "1px solid var(--border-hairline)",
          boxShadow: "var(--nm-raise-sm)",
        }}
      >
        <svg
          viewBox={`0 0 ${encoded.size} ${encoded.size}`}
          role="img"
          aria-label={t("WireGuard configuration QR code")}
          style={{ display: "block", width: "100%", height: "auto", shapeRendering: "crispEdges" }}
        >
          <rect width={encoded.size} height={encoded.size} fill="#ffffff" />
          <path d={encoded.path} fill="#0f172a" />
        </svg>
      </div>
      <p className="hint" style={{ margin: 0, textAlign: "center" }}>
        {t("Scan with the WireGuard app to import this tunnel.")}
      </p>
    </div>
  );
}

function WireGuardQrPreview({
  configText,
  peer,
  state,
}: {
  configText: string;
  peer: WireGuardPeer;
  state: WireGuardState;
}) {
  const { t } = useI18n();
  const endpoint = `${state.default_endpoint_host || "YOUR_SERVER_IP"}:${state.default_udp_port || 51820}`;
  return (
    <div style={{ display: "grid", gap: "var(--s3)" }}>
      <div style={{ display: "flex", alignItems: "center", justifyContent: "center", gap: "var(--s2)", flexWrap: "wrap" }}>
        <span className="chip mono">{peer.label || t("WireGuard peer")}</span>
        {peer.assigned_ip && <span className="chip chip--ok mono">{peer.assigned_ip}/32</span>}
        <span className="chip mono">{endpoint}</span>
      </div>
      <WireGuardQrBox configText={configText} />
    </div>
  );
}

function WireGuardMissingKeyPanel({
  peer,
  privateKeyDraft,
  onPrivateKeyChange,
  busy,
  onSaveKey,
  onRotateKey,
}: {
  peer: WireGuardPeer;
  privateKeyDraft: string;
  onPrivateKeyChange: (v: string) => void;
  busy: boolean;
  onSaveKey: () => void;
  onRotateKey: () => void;
}) {
  const { t } = useI18n();
  const uid = useId();
  return (
    <div className="alert" style={{ display: "grid", gap: "var(--s2)" }}>
      <div>
        <div className="alert__t">{t("Client private key is not stored for this peer")}</div>
        <div>
          {t("Importing a .conf or QR code with <CLIENT_PRIVATE_KEY> will fail in WireGuard. Paste the matching private key to save it, or regenerate the keypair.")}
        </div>
      </div>
      <Field label="Client private key" id={`${uid}-priv`}>
        <div style={{ display: "flex", gap: "var(--s2)", flexWrap: "wrap" }}>
          <input
            id={`${uid}-priv`}
            className="input mono"
            style={{ flex: "1 1 240px", minWidth: 0 }}
            type="password"
            value={privateKeyDraft}
            onChange={(e) => onPrivateKeyChange(e.target.value)}
            placeholder={t("Paste matching 32-byte base64 private key")}
            autoComplete="off"
          />
          <button
            type="button"
            className="btn btn--sm"
            disabled={busy || !privateKeyDraft.trim()}
            onClick={onSaveKey}
          >
            {t("Save private key")}
          </button>
          <button
            type="button"
            className="btn btn--sm btn--primary"
            disabled={busy}
            onClick={onRotateKey}
            title={`${t("Public key")}: ${peer.public_key.slice(0, 12)}…`}
          >
            {busy ? <span className="spin" /> : <IconRefresh size={13} />}
            {t("Regenerate keypair & restart")}
          </button>
        </div>
      </Field>
    </div>
  );
}

export function WireGuardQrSheet({
  state,
  peer: initialPeer,
  onClose,
  onPeerUpdated,
}: {
  state: WireGuardState;
  peer: WireGuardPeer;
  onClose: () => void;
  onPeerUpdated: (fresh?: WireGuardState) => void;
}) {
  const api = useServerApi();
  const { t } = useI18n();
  const { push } = useToast();

  const [peer, setPeer] = useState<WireGuardPeer>(initialPeer);
  const [configText, setConfigText] = useState("");
  const [filename, setFilename] = useState(() => peerConfigFilename(initialPeer));
  const [privateKeyDraft, setPrivateKeyDraft] = useState("");
  const [busy, setBusy] = useState(true);
  const [error, setError] = useState<string | null>(null);

  const loadConfig = async (targetPeer: WireGuardPeer, privOverride = "") => {
    setBusy(true);
    setError(null);
    try {
      const r = await api.wireguardClientConfig(targetPeer.hub, {
        public_key: targetPeer.public_key,
        assigned_ip: targetPeer.assigned_ip || "",
        endpoint_host: state.default_endpoint_host || "",
        endpoint_port: state.default_udp_port || 51820,
        dns: "1.1.1.1",
        allowed_ips: "0.0.0.0/0, ::/0",
        persistent_keepalive: 21,
        client_private_key: privOverride,
        filename: peerConfigFilename(targetPeer),
      });
      setConfigText(r.config);
      setFilename(r.filename || peerConfigFilename(targetPeer));
    } catch (err) {
      setError(err instanceof Error ? err.message : String(err));
    } finally {
      setBusy(false);
    }
  };

  useEffect(() => {
    void loadConfig(initialPeer);
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [initialPeer.public_key]);

  const savePrivateKey = async () => {
    const trimmed = privateKeyDraft.trim();
    if (!trimmed) return;
    setBusy(true);
    setError(null);
    try {
      const updated = await api.updateWireguardPeerMeta(peer.hub, {
        public_key: peer.public_key,
        label: peer.label || "",
        assigned_ip: peer.assigned_ip || "",
        client_private_key: trimmed,
      });
      setPeer(updated);
      setPrivateKeyDraft("");
      push("ok", t("Client private key saved."));
      onPeerUpdated();
      await loadConfig(updated);
    } catch (err) {
      setError(err instanceof Error ? err.message : String(err));
      setBusy(false);
    }
  };

  const rotateKeypair = async () => {
    setBusy(true);
    setError(null);
    try {
      let res: Awaited<ReturnType<typeof api.rotateWireguardPeer>> | null = null;
      try {
        res = await api.rotateWireguardPeer(peer.hub, {
          public_key: peer.public_key,
          confirm_restart: true,
        });
      } catch (err) {
        if (!(err instanceof ApiError) || err.seError !== "TransportError") throw err;
      }
      push("ok", t("WireGuard keypair rotated. Waiting for server restart…"));
      const fresh = await waitForWireGuardReady(api, (s) =>
        res?.peer.public_key ? Boolean(s.peers?.some((p) => p.public_key === res?.peer.public_key)) : true,
      ).catch(() => undefined);
      const nextPeer =
        res?.peer ??
        fresh?.peers?.find(
          (p) => p.hub === peer.hub && p.user.toLowerCase() === peer.user.toLowerCase() && (p.label || "") === (peer.label || ""),
        ) ??
        peer;
      setPeer(nextPeer);
      setPrivateKeyDraft("");
      onPeerUpdated(fresh);
      if (res?.client_config) {
        setConfigText(res.client_config);
        setFilename(peerConfigFilename(nextPeer));
        setBusy(false);
      } else {
        await loadConfig(nextPeer);
      }
    } catch (err) {
      setError(err instanceof Error ? err.message : String(err));
      setBusy(false);
    }
  };

  const hasPlaceholder = !configText || hasPlaceholderPrivateKey(configText);

  return (
    <Sheet
      title={t("WireGuard configuration QR code")}
      subtitle={`${peer.user} · ${t("Hub")} ${peer.hub}`}
      onClose={onClose}
      footer={
        <>
          {!hasPlaceholder && <CopyTextButton text={configText} label="Copy config" />}
          <button
            type="button"
            className="btn btn--primary"
            disabled={busy || hasPlaceholder}
            onClick={() => downloadText(filename, configText, "text/plain")}
          >
            <IconWireGuardDownload size={14} /> {t("Download .conf")}
          </button>
          <button type="button" className="btn" onClick={onClose}>
            {t("Close")}
          </button>
        </>
      }
    >
      <div style={{ display: "grid", gap: "var(--s3)" }}>
        {error && <ErrorAlert>{error}</ErrorAlert>}
        {busy && !configText ? (
          <div style={{ padding: "var(--s6)", textAlign: "center" }} className="hint">
            <span className="spin" /> {t("Loading…")}
          </div>
        ) : hasPlaceholder ? (
          <WireGuardMissingKeyPanel
            peer={peer}
            privateKeyDraft={privateKeyDraft}
            onPrivateKeyChange={setPrivateKeyDraft}
            busy={busy}
            onSaveKey={() => void savePrivateKey()}
            onRotateKey={() => void rotateKeypair()}
          />
        ) : (
          <WireGuardQrPreview configText={configText} peer={peer} state={state} />
        )}
      </div>
    </Sheet>
  );
}

export function WireGuardConfigSheet({
  state,
  peer: initialPeer,
  onClose,
  onMetaSaved,
}: {
  state: WireGuardState;
  peer: WireGuardPeer;
  onClose: () => void;
  onMetaSaved: (fresh?: WireGuardState) => void;
}) {
  const api = useServerApi();
  const { t } = useI18n();
  const { push } = useToast();
  const uid = useId();

  const [peer, setPeer] = useState<WireGuardPeer>(initialPeer);
  const [label, setLabel] = useState(initialPeer.label || "");
  const [assignedIp, setAssignedIp] = useState(initialPeer.assigned_ip || "");
  const [endpointHost, setEndpointHost] = useState(state.default_endpoint_host || "");
  const [endpointPort, setEndpointPort] = useState(String(state.default_udp_port || 51820));
  const [dns, setDns] = useState("1.1.1.1");
  const [allowedIps, setAllowedIps] = useState("0.0.0.0/0, ::/0");
  const [keepalive, setKeepalive] = useState("21");
  const [clientPriv, setClientPriv] = useState("");
  const [configText, setConfigText] = useState("");
  const [filename, setFilename] = useState(() => peerConfigFilename(initialPeer));
  const [showQr, setShowQr] = useState(false);
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState<string | null>(null);

  const renderConfig = async (targetPeer: WireGuardPeer = peer, overrideIp?: string, clearPrivDraft = false) => {
    setBusy(true);
    setError(null);
    try {
      const r = await api.wireguardClientConfig(targetPeer.hub, {
        public_key: targetPeer.public_key,
        assigned_ip: (overrideIp ?? assignedIp).trim(),
        endpoint_host: endpointHost.trim(),
        endpoint_port: Number(endpointPort) || 51820,
        dns: dns.trim(),
        allowed_ips: allowedIps.trim(),
        persistent_keepalive: Number(keepalive) || 0,
        client_private_key: clearPrivDraft ? "" : clientPriv.trim(),
        filename: peerConfigFilename({ ...targetPeer, label: label.trim() }),
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
    void renderConfig(initialPeer);
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [initialPeer.public_key]);

  const saveMeta = async (withPrivKey = false) => {
    setBusy(true);
    setError(null);
    try {
      const saved = await api.updateWireguardPeerMeta(peer.hub, {
        public_key: peer.public_key,
        label: label.trim(),
        assigned_ip: assignedIp.trim(),
        ...(withPrivKey || clientPriv.trim() ? { client_private_key: clientPriv.trim() } : {}),
      });
      setPeer(saved);
      if (clientPriv.trim()) setClientPriv("");
      push("ok", t(withPrivKey ? "Client private key saved." : "Peer metadata saved."));
      onMetaSaved();
      await renderConfig(saved, saved.assigned_ip, true);
    } catch (err) {
      setError(err instanceof Error ? err.message : String(err));
      setBusy(false);
    }
  };

  const rotateKeypair = async () => {
    setBusy(true);
    setError(null);
    try {
      let res: Awaited<ReturnType<typeof api.rotateWireguardPeer>> | null = null;
      try {
        res = await api.rotateWireguardPeer(peer.hub, {
          public_key: peer.public_key,
          confirm_restart: true,
        });
      } catch (err) {
        if (!(err instanceof ApiError) || err.seError !== "TransportError") throw err;
      }
      push("ok", t("WireGuard keypair rotated. Waiting for server restart…"));
      const fresh = await waitForWireGuardReady(api, (s) =>
        res?.peer.public_key ? Boolean(s.peers?.some((p) => p.public_key === res?.peer.public_key)) : true,
      ).catch(() => undefined);
      const nextPeer =
        res?.peer ??
        fresh?.peers?.find(
          (p) => p.hub === peer.hub && p.user.toLowerCase() === peer.user.toLowerCase() && (p.label || "") === (peer.label || ""),
        ) ??
        peer;
      setPeer(nextPeer);
      setClientPriv("");
      onMetaSaved(fresh);
      await renderConfig(nextPeer, nextPeer.assigned_ip || assignedIp, true);
    } catch (err) {
      setError(err instanceof Error ? err.message : String(err));
      setBusy(false);
    }
  };

  const hasPlaceholder = !configText || hasPlaceholderPrivateKey(configText);

  return (
    <Sheet
      title={showQr ? t("WireGuard configuration QR code") : t("WireGuard peer configuration")}
      subtitle={`${peer.user} · ${t("Hub")} ${peer.hub}`}
      onClose={onClose}
      wide
      footer={
        <>
          {!showQr && (
            <>
              <button type="button" className="btn" onClick={() => void saveMeta(false)} disabled={busy}>
                {t("Save label / IP")}
              </button>
              <button type="button" className="btn" onClick={() => void renderConfig()} disabled={busy}>
                {t("Refresh preview")}
              </button>
            </>
          )}
          {configText && !hasPlaceholder && <CopyTextButton text={configText} label="Copy config" />}
          <button
            type="button"
            className="btn"
            disabled={busy || hasPlaceholder}
            onClick={() => setShowQr((v) => !v)}
          >
            <IconQrCode size={14} /> {showQr ? t("Back to editor") : t("QR code")}
          </button>
          <button
            type="button"
            className="btn btn--primary"
            disabled={busy || hasPlaceholder}
            onClick={() => downloadText(filename, configText, "text/plain")}
          >
            <IconWireGuardDownload size={14} /> {t("Download .conf")}
          </button>
        </>
      }
    >
      {showQr ? (
        <WireGuardQrPreview configText={configText} peer={peer} state={state} />
      ) : (
        <div style={{ display: "grid", gap: "var(--s2)" }}>
          {error && <ErrorAlert>{error}</ErrorAlert>}
          {configText && hasPlaceholder && (
            <WireGuardMissingKeyPanel
              peer={peer}
              privateKeyDraft={clientPriv}
              onPrivateKeyChange={setClientPriv}
              busy={busy}
              onSaveKey={() => void saveMeta(true)}
              onRotateKey={() => void rotateKeypair()}
            />
          )}
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
            <Field
              label="Client private key (saved encrypted)"
              id={`${uid}-priv`}
              hint={
                peer.has_private_key
                  ? "Leave blank to use the encrypted key stored in the panel."
                  : "Optional: enter the matching private key to include and save it."
              }
            >
              <input
                id={`${uid}-priv`}
                className="input mono"
                type="password"
                value={clientPriv}
                onChange={(e) => setClientPriv(e.target.value)}
                placeholder={peer.has_private_key ? t("Stored encrypted in panel") : "<CLIENT_PRIVATE_KEY>"}
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
      )}
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
  const filename = peerConfigFilename(peer);
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
  mode = "download",
  onClose,
  onPeerUpdated,
}: {
  state: WireGuardState;
  hub: string;
  user: string;
  peers: WireGuardPeer[];
  mode?: "download" | "qr";
  onClose: () => void;
  onPeerUpdated?: (fresh?: WireGuardState) => void;
}) {
  const api = useServerApi();
  const { t } = useI18n();
  const { guard } = useToast();
  const [busyKey, setBusyKey] = useState<string | null>(null);
  const [editingPeer, setEditingPeer] = useState<WireGuardPeer | null>(null);
  const [qrPeer, setQrPeer] = useState<WireGuardPeer | null>(null);

  if (qrPeer) {
    return (
      <WireGuardQrSheet
        state={state}
        peer={qrPeer}
        onClose={() => setQrPeer(null)}
        onPeerUpdated={(fresh) => onPeerUpdated?.(fresh)}
      />
    );
  }

  if (editingPeer) {
    return (
      <WireGuardConfigSheet
        state={state}
        peer={editingPeer}
        onClose={() => setEditingPeer(null)}
        onMetaSaved={(fresh) => onPeerUpdated?.(fresh)}
      />
    );
  }

  const downloadPeer = async (peer: WireGuardPeer) => {
    if (peer.has_private_key === false) {
      setEditingPeer(peer);
      return;
    }
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
      title={mode === "qr" ? t("WireGuard configuration QR code") : t("Download WireGuard .conf")}
      subtitle={`${user} · ${t("Hub")} ${hub}`}
      onClose={onClose}
      footer={<button type="button" className="btn" onClick={onClose}>{t("Close")}</button>}
    >
      <div style={{ display: "grid", gap: "var(--s2)" }}>
        <p className="hint" style={{ margin: 0 }}>
          {t(
            mode === "qr"
              ? "Select a WireGuard client device below to view its QR code."
              : "Select a WireGuard client device below to download its .conf file.",
          )}
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
                <div style={{ display: "inline-flex", alignItems: "center", gap: "var(--s1)", flexShrink: 0, flexWrap: "wrap" }}>
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
                    className={mode === "qr" ? "btn btn--sm btn--primary" : "btn btn--sm"}
                    onClick={() => setQrPeer(peer)}
                    disabled={busyKey !== null}
                  >
                    <IconQrCode size={14} />
                    {t("QR code")}
                  </button>
                  <button
                    type="button"
                    className={mode === "qr" ? "btn btn--sm" : "btn btn--sm btn--primary"}
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

export type WireGuardUserAction =
  | { kind: "choose"; hub: string; name: string; peers: WireGuardPeer[]; mode: "download" | "qr" }
  | { kind: "qr"; peer: WireGuardPeer }
  | { kind: "edit"; peer: WireGuardPeer };

export function WireGuardUserActionSheet({
  state,
  action,
  onClose,
  onPeerUpdated,
}: {
  state: WireGuardState;
  action: WireGuardUserAction | null;
  onClose: () => void;
  onPeerUpdated: (fresh?: WireGuardState) => void;
}) {
  if (!action) return null;
  if (action.kind === "qr") {
    return (
      <WireGuardQrSheet
        state={state}
        peer={action.peer}
        onClose={onClose}
        onPeerUpdated={onPeerUpdated}
      />
    );
  }
  if (action.kind === "edit") {
    return (
      <WireGuardConfigSheet
        state={state}
        peer={action.peer}
        onClose={onClose}
        onMetaSaved={onPeerUpdated}
      />
    );
  }
  return (
    <WireGuardDownloadChooserSheet
      state={state}
      hub={action.hub}
      user={action.name}
      peers={action.peers}
      mode={action.mode}
      onClose={onClose}
      onPeerUpdated={onPeerUpdated}
    />
  );
}
