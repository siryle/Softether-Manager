"use client";

import { useEffect, useRef, useState } from "react";
import { CheckRow, Empty, ErrorAlert, Field, LoadingBlock, PageHead } from "../components/bits";
import { api, type ConnectionTest, type ManagedServer, type Probe } from "../lib/api";
import { useI18n } from "../lib/i18n";
import { navigate, useRoute } from "../lib/router";
import { useServer } from "../lib/server";
import { useToast } from "../lib/toast";
import { IconCheck, IconNode, IconPlus, IconRefresh } from "../ui/Icon";
import { Sheet } from "../ui/Sheet";

type Check = Probe & { checking?: boolean };

export function Servers() {
  const { servers, currentServer, switchServer, refreshServers, refresh, error } = useServer();
  const { t } = useI18n();
  const { push } = useToast();
  const route = useRoute();
  const [filter, setFilter] = useState("");
  const [checks, setChecks] = useState<Record<number, Check>>({});
  const [checkVersion, setCheckVersion] = useState(0);
  const [editing, setEditing] = useState<ManagedServer | "new" | null>(null);
  const [removing, setRemoving] = useState<ManagedServer | null>(null);
  const [busy, setBusy] = useState(false);
  const addRoute = route.parts[1] === "add";

  useEffect(() => { if (addRoute) setEditing("new"); }, [addRoute]);
  useEffect(() => {
    let alive = true;
    for (const s of servers ?? []) {
      if (!s.configured || !s.is_enabled) continue;
      setChecks(prev => ({ ...prev, [s.id]: { ...prev[s.id], online: false, configured: true, checking: true } }));
      void api.probeServer(s.id).then(result => {
        if (alive) setChecks(prev => ({ ...prev, [s.id]: result }));
      }).catch(e => {
        if (alive) setChecks(prev => ({ ...prev, [s.id]: { online: false, configured: true, error: e instanceof Error ? e.message : String(e) } }));
      });
    }
    return () => { alive = false; };
  }, [servers, checkVersion]);

  const closeForm = () => { setEditing(null); if (addRoute) navigate("/servers"); };
  const onSaved = async () => { closeForm(); await refreshServers(); await refresh(); };
  const switchTo = async (id: number) => {
    try { await switchServer(id); } catch (e) { push("err", e instanceof Error ? e.message : String(e)); }
  };
  const remove = async () => {
    if (!removing || busy) return;
    setBusy(true);
    try {
      await api.deleteServer(removing.id);
      setRemoving(null);
      await refreshServers();
      push("ok", t("Server connection removed."));
    } catch (e) { push("err", e instanceof Error ? e.message : String(e)); }
    finally { setBusy(false); }
  };

  if (!servers) return <LoadingBlock label={t("Loading servers...")} />;
  const filtered = servers.filter(s => `${s.name} ${s.host}:${s.port}`.toLowerCase().includes(filter.trim().toLowerCase()));
  const checking = Object.values(checks).some(c => c.checking);
  const online = servers.filter(s => s.is_enabled && s.configured && checks[s.id]?.online).length;

  return <div className="page servers-page">
    <PageHead title={t("Servers")} sub={t("Manage multiple SoftEther servers from one panel. Each server has its own management space.")} actions={<button className="btn-primary" onClick={() => setEditing("new")}><IconPlus size={14} />{t("Add server")}</button>} />
    {error && <ErrorAlert>{error}</ErrorAlert>}
    <div className="servers-toolbar">
      <input className="input servers-search" aria-label={t("Search servers")} placeholder={t("Search name or address...")} value={filter} onChange={e => setFilter(e.target.value)} />
      <div className="servers-summary"><span>{t("{count} saved", { count: servers.length })}</span><span><i className="server-dot server-dot--online" />{t("{count} online", { count: online })}</span><button className="btn btn--sm" disabled={checking} onClick={() => setCheckVersion(v => v + 1)}><IconRefresh size={13} className={checking ? "spin" : ""} />{t("Refresh status")}</button></div>
    </div>
    {!filtered.length ? <Empty title={t("No matching servers")}>{t("Try another name or address.")}</Empty> : <div className="servers-table"><table className="dtable"><thead><tr><th>{t("Server / management address")}</th><th>{t("Connection status")}</th><th>{t("Current load")}</th><th>{t("Details")}</th><th>{t("Actions")}</th></tr></thead><tbody>{filtered.map(s => {
      const active = s.id === currentServer?.id;
      const c = checks[s.id];
      const state = !s.is_enabled ? "disabled" : !s.configured ? "unconfigured" : c?.checking ? "probing" : !c ? "unknown" : c.online ? "online" : "offline";
      const label = { disabled: "Disabled", unconfigured: "Not configured", probing: "Checking...", unknown: "Not checked", online: "Online", offline: "Offline" }[state];
      return <tr key={s.id}>
        <td><div className="servers-name"><strong>{s.name}</strong>{active && <span className="server-tag server-tag--current">{t("Current")}</span>}{s.is_default && <span className="server-tag">{t("Default")}</span>}</div><div className="servers-address mono">{s.host}:{s.port}</div></td>
        <td><span className={`server-state server-state--${state}`}><i className={`server-dot server-dot--${state}`} />{t(label)}</span></td>
        <td className="mono">{state === "online" ? t("{sessions} sessions · {hubs} HUB", { sessions: c?.sessions ?? 0, hubs: c?.hubs ?? 0 }) : "—"}</td>
        <td className="servers-detail" title={c?.error}>{state === "offline" ? c?.error : state === "online" ? c?.version : "—"}</td>
        <td><div className="server-actions"><button className="btn btn--sm" disabled={!s.is_enabled} onClick={() => void switchTo(s.id)}>{t(active ? "Open overview" : "Switch to server")}</button><button className="btn btn--sm" onClick={() => setEditing(s)}>{t("Edit")}</button><button className="btn btn--sm btn--ghost" disabled={active || servers.length <= 1} title={active ? t("Switch to another server before removing this connection.") : undefined} onClick={() => setRemoving(s)}>{t("Remove")}</button></div></td>
      </tr>;
    })}</tbody></table></div>}
    <p className="servers-hint"><IconNode size={15} />{t("Switching changes the current management target. Existing VPN client connections stay connected.")}</p>
    <div className="servers-scope"><span>{t("Currently managing")}: <strong>{currentServer?.name}</strong></span><span>{t("Panel accounts, domain, HTTPS and appearance are shared across servers.")}</span></div>
    {editing && <ServerForm server={editing === "new" ? null : editing} onClose={closeForm} onSaved={onSaved} />}
    {removing && <Sheet title={t("Remove server connection")} onClose={() => { if (!busy) setRemoving(null); }} beforeClose={() => !busy} footer={<><button className="btn" disabled={busy} onClick={() => setRemoving(null)}>{t("Cancel")}</button><button className="btn btn--danger" disabled={busy} onClick={() => void remove()}>{t(busy ? "Removing..." : "Remove")}</button></>}><p>{t("Remove {name} from this panel?", { name: removing.name })}</p><p className="hint">{t("This removes the saved connection and its local traffic, quota and session history. Remote HUBs, users and VPN configuration are kept.")}</p></Sheet>}
  </div>;
}

function ServerForm({ server, onClose, onSaved }: { server: ManagedServer | null; onClose: () => void; onSaved: () => Promise<void> }) {
  const { t } = useI18n();
  const { push } = useToast();
  const form = useRef<HTMLFormElement>(null);
  const [name, setName] = useState(server?.name ?? "");
  const [host, setHost] = useState(server?.host ?? "");
  const [port, setPort] = useState(server?.port ?? 5555);
  const [password, setPassword] = useState("");
  const [isDefault, setIsDefault] = useState(server?.is_default ?? false);
  const [skipCertCheck, setSkipCertCheck] = useState(server?.skip_cert_check ?? false);
  const [busy, setBusy] = useState<"test" | "save" | null>(null);
  const [test, setTest] = useState<ConnectionTest | null>(null);
  const [testError, setTestError] = useState<string | null>(null);
  const [certificate, setCertificate] = useState<{ fingerprint: string; pinned: string } | null>(null);
  const [certificateBusy, setCertificateBusy] = useState(false);
  const [error, setError] = useState<string | null>(null);
  const inspectCertificate = async () => {
    if (!server) return;
    setCertificateBusy(true); setError(null);
    try { setCertificate(await api.serverCertificate(server.id)); }
    catch (e) { setError(e instanceof Error ? e.message : String(e)); }
    finally { setCertificateBusy(false); }
  };
  const trustCertificate = async () => {
    if (!server || !certificate || !window.confirm(t("Confirm that this SHA-256 fingerprint matches the certificate on your VPN server.") + "\n\n" + certificate.fingerprint)) return;
    setCertificateBusy(true); setError(null);
    try {
      await api.trustServerCertificate(server.id, certificate.fingerprint);
      setCertificate({ ...certificate, pinned: certificate.fingerprint });
      push("ok", t("VPN server certificate trusted."));
    } catch (e) { setError(e instanceof Error ? e.message : String(e)); }
    finally { setCertificateBusy(false); }
  };
  const dirty = name !== (server?.name ?? "") || host !== (server?.host ?? "") || port !== (server?.port ?? 5555) || password !== "" || isDefault !== (server?.is_default ?? false) || skipCertCheck !== (server?.skip_cert_check ?? false);
  const beforeClose = () => !busy && (!dirty || window.confirm(t("Discard unsaved connection changes?")));
  const body = { name: name.trim(), host: host.trim(), port, password: server && !password ? null : password, is_default: isDefault, skip_cert_check: skipCertCheck, auth: test?.auth ?? server?.auth, skip_test: true };
  const testConnection = async () => {
    if (!form.current?.reportValidity()) return;
    setBusy("test"); setTest(null); setTestError(null); setError(null);
    try {
      setTest(server ? await api.testServerId(server.id, body) : await api.testServerConnection(body));
    } catch (e) { setTestError(e instanceof Error ? e.message : String(e)); }
    finally { setBusy(null); }
  };
  const save = async (e: React.FormEvent) => {
    e.preventDefault(); setBusy("save"); setError(null);
    try {
      if (server) await api.updateServer(server.id, body); else await api.createServer(body);
      push("ok", t(server ? "Server connection updated." : "Server connection saved."));
      await onSaved();
    } catch (e) { setError(e instanceof Error ? e.message : String(e)); }
    finally { setBusy(null); }
  };
  return <Sheet side title={t(server ? "Edit server" : "Add server")} subtitle={t("Save a SoftEther management connection")} onClose={onClose} beforeClose={beforeClose} footer={<><button type="button" className="btn" disabled={!!busy} onClick={() => { if (beforeClose()) onClose(); }}>{t("Cancel")}</button><button type="submit" form="server-form" className="btn-primary" disabled={!!busy}>{t(busy === "save" ? "Saving..." : "Save server")}</button></>}>
    <form id="server-form" ref={form} onSubmit={save} onChange={() => { setTest(null); setTestError(null); }}>
      {error && <ErrorAlert>{error}</ErrorAlert>}
      <fieldset disabled={!!busy} className="server-form-fields">
        <Field label={t("Server name")} hint={t("Use a location or purpose that is easy to recognize.")}><input className="input" value={name} onChange={e => setName(e.target.value)} required maxLength={64} autoFocus placeholder={t("e.g. Shanghai · Primary")} /></Field>
        <div className="server-form-address"><Field label={t("Host / IP")}><input className="input mono" value={host} onChange={e => setHost(e.target.value)} required maxLength={255} autoCapitalize="none" spellCheck={false} placeholder="vpn.example.com" /></Field><Field label={t("Management port")}><input className="input mono" type="number" min={1} max={65535} value={port} onChange={e => setPort(Number(e.target.value))} required /></Field></div>
        <Field label={t(server ? "Administrator password (empty keeps the stored one)" : "Administrator password")}><input className="input" type="password" autoComplete="new-password" value={password} onChange={e => setPassword(e.target.value)} maxLength={256} /></Field>
        <CheckRow checked={isDefault} onChange={setIsDefault} disabled={server?.is_default} label={t("Use as default server")} hint={t("Used when a browser tab has no server selected.")} />
        <CheckRow checked={skipCertCheck} onChange={setSkipCertCheck} label={t("Skip certificate verification (not recommended)")} hint={t("Off: the panel pins the server certificate and refuses to send the administrator password if it changes. On: the password is sent to whatever answers on this address.")} />
        <button className="btn" type="button" onClick={() => void testConnection()}><IconRefresh size={14} className={busy === "test" ? "spin" : ""} />{t(busy === "test" ? "Testing..." : "Test connection")}</button>
      </fieldset>
      {server && <div style={{ marginTop: "var(--s4)" }}>
        <button type="button" className="btn" disabled={certificateBusy || !!busy} onClick={() => void inspectCertificate()}>{t("Inspect saved server certificate")}</button>
        {certificate && <>
          <p className="hint">{t("Compare this fingerprint with the certificate on your VPN server before trusting a replacement.")}</p>
          <p className="mono" style={{ overflowWrap: "anywhere" }}>SHA-256: {certificate.fingerprint}</p>
          {certificate.pinned && <p className="hint mono" style={{ overflowWrap: "anywhere" }}>{t("Previously trusted")}: {certificate.pinned}</p>}
          {certificate.pinned !== certificate.fingerprint && <button type="button" className="btn" disabled={certificateBusy || !!busy} onClick={() => void trustCertificate()}>{t("Trust this certificate")}</button>}
        </>}
      </div>}
      {test && <div className="alert alert--ok"><IconCheck size={14} /><span>{t("Connection successful")} · <span className="mono">{test.version} · {test.rtt_ms} ms</span></span></div>}
      {testError && <ErrorAlert>{testError}</ErrorAlert>}
      <p className="servers-form-hint">{t("Saving a connection keeps the current server selected. Use the top switcher to manage the new server.")}</p>
    </form>
  </Sheet>;
}
