"use client";

import { useEffect, useState } from "react";
import { ErrorAlert, Field, PageHead } from "../components/bits";
import { api } from "../lib/api";
import { useI18n } from "../lib/i18n";
import { navigate } from "../lib/router";
import { useServer } from "../lib/server";
import { useToast } from "../lib/toast";
import { IconCheck } from "../ui/Icon";

/**
 * Connecting the panel to the SoftEther instance on this machine: the
 * management port and the administrator password, tested before saved.
 * The host stays editable for the odd deployment where SoftEther listens
 * somewhere unusual, but 127.0.0.1 is the design.
 */
export function Connect() {
  const { refresh, refreshServers } = useServer();
  const { push } = useToast();
  const { t } = useI18n();
  const [host, setHost] = useState("127.0.0.1");
  const [port, setPort] = useState(5555);
  const [password, setPassword] = useState("");
  const [configured, setConfigured] = useState(false);
  const [publicHost, setPublicHost] = useState("");
  const [publicPort, setPublicPort] = useState("443");
  const [testResult, setTestResult] = useState<string | null>(null);
  const [error, setError] = useState<string | null>(null);
  const [busy, setBusy] = useState<"test" | "save" | "public" | null>(null);

  useEffect(() => {
    void api
      .connection()
      .then((c) => {
        setHost(c.host);
        setPort(c.port);
        setConfigured(c.configured);
        setPublicHost(String(c.public_host || ""));
        setPublicPort(String(c.public_port || 443));
      })
      .catch(() => {});
  }, []);

  const test = async () => {
    setBusy("test");
    setError(null);
    setTestResult(null);
    try {
      const r = await api.testConnection({ host, port, password });
      setTestResult(`${r.version} ${t("answered on")} ${host}:${port}.`);
    } catch (e) {
      setError(e instanceof Error ? e.message : String(e));
    } finally {
      setBusy(null);
    }
  };

  // The public address is what clients dial and what certificates are issued
  // for, so it can be saved on its own without re-testing the connection.
  const savePublic = async () => {
    const p = Number(publicPort);
    if (!Number.isInteger(p) || p < 1 || p > 65535) {
      setError(t("The port must be a number between 1 and 65535."));
      return;
    }
    setBusy("public");
    setError(null);
    try {
      await api.saveConnection({ public_host: publicHost.trim(), public_port: p });
      push("ok", t("Server public connection info saved."));
    } catch (e) {
      setError(e instanceof Error ? e.message : String(e));
    } finally {
      setBusy(null);
    }
  };

  const save = async () => {
    setBusy("save");
    setError(null);
    try {
      await api.saveConnection({
        host,
        port,
        password: password || (configured ? null : ""),
        public_host: publicHost.trim(),
        public_port: Number(publicPort) || 443,
      });
      push("ok", t("Connected."));
      await refreshServers();
      await refresh();
      navigate("/");
    } catch (e) {
      setError(e instanceof Error ? e.message : String(e));
    } finally {
      setBusy(null);
    }
  };

  return (
    <div className="page">
      <PageHead
        title={t("Connect to SoftEther")}
        sub={t("The management port and administrator password of the instance on this machine.")}
      />
      <div className="connect-grid">
        <div className="card" style={{ padding: "var(--s5)" }}>
          {error && <ErrorAlert>{error}</ErrorAlert>}
          {testResult && (
            <div className="alert alert--ok">
              <IconCheck size={14} /> {testResult}
            </div>
          )}
          <div className="row2">
            <Field label={t("Host")} hint={t("127.0.0.1 for local, or LAN IP (e.g. 192.168.1.x) / hostname.")}>
              <input className="input mono" value={host} onChange={(e) => setHost(e.target.value)}
                autoCapitalize="none" spellCheck={false} inputMode="url" />
            </Field>
            <Field label={t("Management port")} hint={t("5555 on a stock install; 443 and 992 also answer.")}>
              <input className="input mono" type="number" min={1} max={65535} value={port}
              onChange={(e) => setPort(Number(e.target.value))} inputMode="numeric" />
          </Field>
        </div>
        <Field
          label={configured ? t("Administrator password (empty keeps the stored one)") : t("Administrator password")}
        >
          <input className="input" type="password" value={password}
            onChange={(e) => setPassword(e.target.value)} autoComplete="off" />
        </Field>
        <div className="section__t" style={{ margin: "var(--s4) 0 var(--s2)" }}>{t("Server public information")}</div>
        <p className="hint" style={{ marginTop: 0, marginBottom: "var(--s2)" }}>
          {t("The public domain is used as the default server address for .vpn, .ovpn, and WireGuard client configs; the public port is used as the default port for .vpn downloads.")}
        </p>
        <div className="row2">
          <Field label={t("Public domain / host")} hint={t("Public hostname or IP (e.g. vpn.example.com).")}>
            <input className="input mono" value={publicHost} onChange={(e) => setPublicHost(e.target.value)}
              autoCapitalize="none" spellCheck={false} inputMode="url" placeholder="vpn.example.com" />
          </Field>
          <Field label={t("Public port")} hint={t("Open port for VPN clients; default 443.")}>
            <input className="input mono" type="number" min={1} max={65535} value={publicPort}
              onChange={(e) => setPublicPort(e.target.value)} inputMode="numeric" />
          </Field>
        </div>
        <button className="btn btn--sm" onClick={savePublic} disabled={busy !== null}>
          {busy === "public" && <span className="spin" />} {t("Save server info")}
        </button>
        <div style={{ display: "flex", gap: "var(--s2)", marginTop: "var(--s4)" }}>
          <button className="btn" onClick={test} disabled={busy !== null}>
            {busy === "test" && <span className="spin" />} {t("Test connection")}
          </button>
          <button className="btn btn--primary" onClick={save} disabled={busy !== null}>
            {busy === "save" && <span className="spin" />} {t("Save & connect")}
          </button>
        </div>
        <p className="hint" style={{ marginTop: "var(--s3)" }}>
          {t("No SoftEther on this machine yet? Install it first — the panel manages an existing instance; it does not install one.")}
        </p>
        </div>

        <aside className="panel-card space-y-4">
          <div>
            <h2 className="panel-card__t">{t("Connection guide")}</h2>
            <p className="panel-card__s">{t("Management RPC & client endpoints")}</p>
          </div>
          <div className="space-y-3 text-xs">
            <div className="p-3 rounded-md bg-raised border border-hairline space-y-1">
              <div className="font-semibold text-primary">{t("Management ports")}</div>
              <p className="text-muted leading-relaxed">
                {t("5555 on a stock install; 443 and 992 also answer.")}
              </p>
            </div>
            <div className="p-3 rounded-md bg-raised border border-hairline space-y-1">
              <div className="font-semibold text-primary">{t("Public endpoint")}</div>
              <p className="text-muted leading-relaxed">
                {t("The public domain is used as the default server address for .vpn, .ovpn, and WireGuard client configs; the public port is used as the default port for .vpn downloads.")}
              </p>
            </div>
          </div>
        </aside>
      </div>
    </div>
  );
}
