"use client";

import { t } from "../lib/i18n";
import { useCallback, useEffect, useRef, useState, type ReactNode } from "react";
import { CheckRow, ConfirmSheet, Empty, ErrorAlert, Field, KV, LoadingBlock, PageHead, SectionTitle, usePoll } from "../components/bits";
import { api, getStoredServerId, useServerApi, ApiError, type BackupInfo, type BackupSettings, type DomainStatus, type Wire } from "../lib/api";
import { Link } from "../lib/router";
import { useToast } from "../lib/toast";
import { downloadBase64, downloadText, fileToBase64, formatBytes, formatDate, timeAgo } from "../lib/util";
import { IconDownload, IconPlus, IconRefresh, IconTrash, IconUpload } from "../ui/Icon";
import { Sheet } from "../ui/Sheet";
import { OnlinePill, Pill } from "../ui/Status";
import { OutcomeNote, Switch, SwitchRow, useToggle } from "../ui/Switch";

/**
 * Everything server-wide, as one scrolling document of cards -- listeners,
 * protocols, encryption, bridges, layer-3 switches, clustering, keep-alive,
 * syslog, the configuration file, and the danger zone. Tabs would hide
 * state; a document lets an operator scan all of it.
 */
export function ServerSettings({ section }: { section?: string }) {
  useEffect(() => {
    if (section) {
      requestAnimationFrame(() =>
        document.getElementById(`ss-${section}`)?.scrollIntoView({ behavior: "smooth", block: "start" }),
      );
    }
  }, [section]);

  return (
    <div className="page">
      <PageHead title={t("Server settings")} sub={<Link to="/" className="linkish">{t("← dashboard")}</Link>} />
      <div className="setdoc">
        <ListenersCard />
        <ProtocolsCard />
        <EncryptionCard />
        <BridgeCard />
        <L3Card />
        <EtherIpCard />
        <FarmCard />
        <KeepSyslogCard />
        <ConfigCard />
        <DangerZone />
      </div>
    </div>
  );
}

/* ── listeners ────────────────────────────────────────────────────────────── */

function ListenersCard() {
  const [listeners, setListeners] = useState<Wire[] | null>(null);
  const [special, setSpecial] = useState<Wire | null>(null);
  const [adding, setAdding] = useState(false);
  const [newPort, setNewPort] = useState(5555);
  const { guard } = useToast();

  const load = useCallback(async () => {
    const r = await api.listeners().catch(() => null);
    const list = r ? ((r.ListenerList as Wire[]) ?? []) : null;
    if (list) setListeners(list);
    setSpecial(await api.specialListener().catch(() => null));
    return list;
  }, []);
  usePoll(load, "list", []);

  return (
    <section id="ss-listeners">
      <SectionTitle
        count={listeners?.length}
        actions={
          <button className="btn btn--sm" onClick={() => setAdding(true)}>
            <IconPlus size={14} /> {t("Add port")}
          </button>
        }
      >
        {t("TCP listeners")}
      </SectionTitle>
      <div className="card listeners-card" style={{ padding: "var(--s3) var(--s4)" }}>
        {listeners === null ? (
          <LoadingBlock />
        ) : (
          <div className="listener-grid">
            {listeners.map((l) => (
              <div key={String(l.Ports_u32)} className="lrow">
                <span className="mono" style={{ fontSize: "var(--t-data)", fontWeight: 600 }}>{String(l.Ports_u32)}</span>
                {l.Errors_bool ? (
                  <span title={t("The port is enabled but could not be opened — something else holds it.")}><Pill kind="err" label="cannot open" /></span>
                ) : (
                  <OnlinePill online={Boolean(l.Enables_bool)} onLabel="listening" offLabel="disabled" />
                )}
                <span style={{ flex: 1 }} />
                <ListenerSwitch listener={l} reload={load} />
                <button
                  className="btn btn--sm btn--ghost"
                  onClick={() => void guard(async () => {
                    await api.deleteListener(Number(l.Ports_u32));
                    await load();
                  }, t("Listener {port} deleted.", { port: String(l.Ports_u32) }))}
                  aria-label={t("Delete listener")}
                >
                  <IconTrash size={14} />
                </button>
              </div>
            ))}
          </div>
        )}
        {special && (
          <div className="row2" style={{ marginTop: "var(--s3)" }}>
            <SpecialListenerSwitch
              special={special}
              field="VpnOverIcmpListener_bool"
              label="VPN over ICMP"
              hint="Clients tunnel in ping packets — crosses networks that allow nothing else."
              onFresh={setSpecial}
            />
            <SpecialListenerSwitch
              special={special}
              field="VpnOverDnsListener_bool"
              label="VPN over DNS"
              hint="Tunnels in DNS queries on UDP 53. Slow, and a last resort."
              onFresh={setSpecial}
            />
          </div>
        )}
      </div>
      {adding && (
        <Sheet
          title={t("New listener")}
          onClose={() => setAdding(false)}
          footer={
            <>
              <button className="btn" onClick={() => setAdding(false)}>{t("Cancel")}</button>
              <button
                className="btn btn--primary"
                onClick={() => void guard(async () => {
                  await api.createListener(newPort, true);
                  setAdding(false);
                  await load();
                }, t("Listening on {port}.", { port: newPort }))}
              >
                {t("Add")}
              </button>
            </>
          }
        >
          <Field label="TCP port">
            <input className="input mono" type="number" min={1} max={65535} value={newPort} onChange={(e) => setNewPort(Number(e.target.value))} inputMode="numeric" />
          </Field>
        </Sheet>
      )}
    </section>
  );
}

/** One listener's switch. The word beside it is the request; the pill in the
 *  row is the server's answer, read back after every change. */
function ListenerSwitch({ listener, reload }: { listener: Wire; reload: () => Promise<Wire[] | null> }) {
  const port = Number(listener.Ports_u32);
  const toggle = useToggle({
    value: Boolean(listener.Enables_bool),
    apply: async (next) => {
      const r = await api.toggleListener(port, next);
      return Boolean(r.enabled);
    },
    reload: async () => {
      const list = await reload();
      const me = list?.find((x) => Number(x.Ports_u32) === port);
      return me ? Boolean(me.Enables_bool) : null;
    },
    noun: t("Listener {port}", { port: String(port) }),
    onWord: "enabled",
    offWord: "disabled",
  });
  return (
    <span className="switchbox">
      <Switch
        on={Boolean(listener.Enables_bool)}
        pending={toggle.pending}
        target={toggle.target}
        onToggle={() => void toggle.toggle()}
        label={t("Listener on port {port}", { port: String(port) })}
      />
      <OutcomeNote outcome={toggle.outcome} />
    </span>
  );
}

/** VPN-over-ICMP and VPN-over-DNS share one settings object; each switch
 *  rewrites its own flag and reads the object back. */
function SpecialListenerSwitch({
  special,
  field,
  label,
  hint,
  onFresh,
}: {
  special: Wire;
  field: "VpnOverIcmpListener_bool" | "VpnOverDnsListener_bool";
  label: string;
  hint: string;
  onFresh: (fresh: Wire) => void;
}) {
  const on = Boolean(special[field]);
  const toggle = useToggle({
    value: on,
    apply: async (next) => {
      const r = await api.setSpecialListener({ ...special, [field]: next });
      onFresh(r);
      return Boolean(r[field]);
    },
    reload: async () => {
      const r = await api.specialListener().catch(() => null);
      if (r) onFresh(r);
      return r ? Boolean(r[field]) : null;
    },
    noun: label,
    onWord: "enabled",
    offWord: "disabled",
  });
  return (
    <SwitchRow
      label={label}
      hint={hint}
      toggle={toggle}
      on={on}
      status={<Pill kind={on ? "ok" : "idle"} label={on ? "enabled" : "disabled"} />}
    />
  );
}

/* ── protocols ────────────────────────────────────────────────────────────── */

/** VPN Azure: the switch, and beside it what the relay itself reports --
 *  enabled is a wish until the server says "connected". */
function AzureSwitch({ azure, onFresh, ddnsDisabled = false }: { azure: Wire | null; onFresh: (fresh: Wire) => void; ddnsDisabled?: boolean }) {
  const on = !ddnsDisabled && Boolean(azure?.IsEnabled_bool);
  const connected = Boolean(azure?.IsConnected_bool);
  const toggle = useToggle({
    value: ddnsDisabled ? null : on,
    apply: async (next) => {
      const r = await api.setAzure(next);
      onFresh(r);
      return Boolean(r.IsEnabled_bool);
    },
    reload: async () => {
      const r = await api.azure().catch(() => null);
      if (r) onFresh(r);
      return r ? Boolean(r.IsEnabled_bool) : null;
    },
    noun: "VPN Azure",
    onWord: "enabled",
    offWord: "disabled",
  });
  return (
    <SwitchRow
      label="VPN Azure relay"
      hint={ddnsDisabled ? "VPN Azure requires dynamic DNS. Enable dynamic DNS first." : "Reachable through azure even behind NAT, at <hostname>.vpnazure.net."}
      toggle={toggle}
      on={on}
      disabled={ddnsDisabled}
      status={
        ddnsDisabled ? (
          <Pill kind="idle" label="Requires DDNS" />
        ) : on ? (
          <Pill kind={connected ? "ok" : "busy"} label={connected ? "connected" : "connecting"} />
        ) : (
          <Pill kind="idle" label="disabled" />
        )
      }
    />
  );
}

function AzureDdnsCard() {
  const [azure, setAzure] = useState<Wire | null>(null);
  const [ddns, setDdns] = useState<Wire | null>(null);
  const [azureError, setAzureError] = useState<Error | null>(null);
  const [ddnsError, setDdnsError] = useState<Error | null>(null);
  const [refreshing, setRefreshing] = useState(false);
  const azureRequest = useRef(0);
  const ddnsEnabled = ddns?.enabled;

  const loadAzure = useCallback(async () => {
    const request = ++azureRequest.current;
    try {
      const fresh = await api.azure();
      if (request !== azureRequest.current) return;
      setAzure(fresh);
      setAzureError(null);
    } catch (error) {
      if (request === azureRequest.current) setAzureError(error instanceof Error ? error : new Error(String(error)));
    }
  }, []);

  const onDdnsFresh = useCallback((fresh: Wire) => {
    setDdns(fresh);
    setDdnsError(null);
    if (ddnsEnabled !== undefined && fresh.enabled !== ddnsEnabled) {
      ++azureRequest.current;
      setAzure(null);
      setAzureError(null);
      if (fresh.enabled === true) void loadAzure();
    }
  }, [ddnsEnabled, loadAzure]);

  const load = useCallback(async () => {
    setRefreshing(true);
    await Promise.all([
      ddnsEnabled === false ? Promise.resolve() : loadAzure(),
      api.ddns().then(onDdnsFresh).catch((e: unknown) => setDdnsError(e instanceof Error ? e : new Error(String(e)))),
    ]);
    setRefreshing(false);
  }, [ddnsEnabled, loadAzure, onDdnsFresh]);
  usePoll(load, "list", []);

  const azureUnsupported = azureError instanceof ApiError && azureError.seError === "UnsupportedError";
  const ddnsUnsupported = ddnsError instanceof ApiError && ddnsError.seError === "UnsupportedError";
  const canRetry = (ddnsEnabled !== false && azureError && !azureUnsupported) || (ddnsError && !ddnsUnsupported);
  const azureControl = ddnsEnabled === false ? (
    <AzureSwitch key="ddns-disabled" azure={null} onFresh={setAzure} ddnsDisabled />
  ) : azureUnsupported ? (
    !ddns && !ddnsError ? <LoadingBlock label="Loading dynamic DNS" /> :
      <div className="alert" role="status">{t("VPN Azure is not supported by this server.")}</div>
  ) : azureError ? (
    <ErrorAlert><span role="alert">{t("Could not load VPN Azure: {error}", { error: azureError.message })}</span></ErrorAlert>
  ) : azure ? (
    <AzureSwitch azure={azure} onFresh={setAzure} />
  ) : <LoadingBlock label="Loading VPN Azure" />;

  return (
    <div className="card" style={{ padding: "var(--s4)" }}>
      <div className="section__t" style={{ marginBottom: "var(--s3)" }}>{t("VPN Azure & dynamic DNS")}</div>
      <div className="row2" style={{ alignItems: "start", gap: "var(--s4)" }}>
        <div>
          {ddnsUnsupported ? (
            <div className="alert" role="status">{t("Dynamic DNS is not supported by this server.")}</div>
          ) : ddnsError ? (
            <ErrorAlert><span role="alert">{t("Could not load dynamic DNS: {error}", { error: ddnsError.message })}</span></ErrorAlert>
          ) : ddns ? (
            <DdnsSettings ddns={ddns} onFresh={onDdnsFresh} />
          ) : (
            <LoadingBlock label="Loading dynamic DNS" />
          )}
        </div>
        <div>
          {azureControl}
        </div>
      </div>
      {canRetry && (
        <div style={{ marginTop: "var(--s3)" }}>
          <button className="btn" disabled={refreshing} onClick={() => void load()}>{t("Retry")}</button>
        </div>
      )}
    </div>
  );
}

function DdnsSettings({ ddns, onFresh }: { ddns: Wire; onFresh: (fresh: Wire) => void }) {
  const api = useServerApi();
  const [confirming, setConfirming] = useState(false);
  const [ddnsHost, setDdnsHost] = useState("");
  const { guard } = useToast();
  const on = ddns.enabled === true;
  const toggle = useToggle({
    value: on,
    noun: "Dynamic DNS",
    onWord: "enabled",
    offWord: "disabled",
    apply: async (next) => {
      try {
        await api.setDdnsEnabled(next);
      } catch (error) {
        // SetConfig may restart the server before its response reaches the panel.
        if (!(error instanceof ApiError) || error.seError !== "TransportError") throw error;
      }
      const deadline = Date.now() + 45000;
      while (Date.now() < deadline) {
        await new Promise((resolve) => window.setTimeout(resolve, 1000));
        try {
          const fresh = await api.ddns();
          onFresh(fresh);
          if (fresh.enabled === next) return next;
        } catch (error) {
          if (error instanceof ApiError && error.seError !== "TransportError") throw error;
        }
      }
      throw new Error(t("The VPN server has not confirmed the dynamic DNS change. Check its status and retry."));
    },
  });

  return (
    <>
      <SwitchRow
        label="Dynamic DNS"
        hint={toggle.pending ? t("Restarting the VPN server; waiting for dynamic DNS status…") : t("Keep a softether.net hostname updated. Changing this setting restarts the VPN service.")}
        on={on}
        toggle={toggle}
        onToggle={() => setConfirming(true)}
        status={<Pill kind={on ? "ok" : "idle"} label={on ? "enabled" : "disabled"} />}
      />
      {on && (
        <fieldset disabled={toggle.pending} style={{ border: 0, padding: 0, margin: "var(--s3) 0 0", minWidth: 0 }}>
          <KV rows={[
            ["DDNS hostname", String(ddns.CurrentHostName_str || "—")],
            ["FQDN", String(ddns.CurrentFqdn_str || "—")],
            ["IPv4", String(ddns.CurrentIPv4_str || "—")],
            ["last error", Number(ddns.Err_IPv4_u32 ?? 0) === 0 ? t("none") : String(ddns.ErrStr_IPv4_utf || t("code {n}", { n: String(ddns.Err_IPv4_u32) }))],
          ]} />
          <Field label="Change DDNS hostname" hint="The name half of <name>.softether.net — it must be globally unused.">
            <div style={{ display: "flex", gap: "var(--s2)" }}>
              <input className="input mono" value={ddnsHost} onChange={(e) => setDdnsHost(e.target.value)} placeholder={String(ddns.CurrentHostName_str ?? "")} autoCapitalize="none" spellCheck={false} />
              <button className="btn" disabled={!ddnsHost.trim()} onClick={() => void guard(async () => {
                await api.setDdnsHostname(ddnsHost.trim());
                setDdnsHost("");
                onFresh(await api.ddns());
              }, "DDNS hostname changed.")}>
                {t("Apply")}
              </button>
            </div>
          </Field>
        </fieldset>
      )}
      {confirming && (
        <ConfirmSheet
          title={t(on ? "Disable dynamic DNS and restart?" : "Enable dynamic DNS and restart?")}
          verb="Apply & restart"
          body={t("This restarts the VPN service and disconnects all sessions. After dynamic DNS is disabled, enabling it again may assign a new hostname. VPN Azure also relies on dynamic DNS.")}
          onClose={() => setConfirming(false)}
          onConfirm={() => {
            setConfirming(false);
            void toggle.toggle();
          }}
        />
      )}
    </>
  );
}

function ProtocolsCard() {
  const [ipsec, setIpsec] = useState<Wire | null>(null);
  const [openvpn, setOpenvpn] = useState<Wire | null>(null);
  const [hubs, setHubs] = useState<string[]>([]);
  const [secret, setSecret] = useState("");
  const [dirty, setDirty] = useState<Record<string, boolean>>({});
  const { guard } = useToast();

  const load = useCallback(async () => {
    const [i, o, h] = await Promise.all([
      api.ipsec().catch(() => null),
      api.openvpn().catch(() => null),
      api.hubs().catch(() => null),
    ]);
    if (!dirty.ipsec && i) { setIpsec(i); setSecret(String(i.IPsec_Secret_str ?? "")); }
    if (!dirty.openvpn && o) setOpenvpn(o);
    if (h) setHubs(((h.HubList as Wire[]) ?? []).map((x) => String(x.HubName_str)));
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, []);
  usePoll(load, "list", []);

  return (
    <section id="ss-protocols">
      <SectionTitle>{t("Access protocols")}</SectionTitle>
      <div className="setgrid">
        {/* IPsec / L2TP */}
        <div className="card" style={{ padding: "var(--s4)" }}>
          <div className="section__t" style={{ marginBottom: "var(--s3)" }}>{t("L2TP / IPsec / EtherIP")}</div>
          {!ipsec ? (
            <LoadingBlock />
          ) : (
            <>
              <CheckRow checked={Boolean(ipsec.L2TP_IPsec_bool)} onChange={(v) => { setIpsec({ ...ipsec, L2TP_IPsec_bool: v }); setDirty((d) => ({ ...d, ipsec: true })); }}
                label="L2TP over IPsec" hint="The built-in VPN of iOS, Android and Windows." />
              <CheckRow checked={Boolean(ipsec.L2TP_Raw_bool)} onChange={(v) => { setIpsec({ ...ipsec, L2TP_Raw_bool: v }); setDirty((d) => ({ ...d, ipsec: true })); }}
                label="Raw L2TP (no encryption)" hint="Only for equipment that cannot do IPsec." />
              <CheckRow checked={Boolean(ipsec.EtherIP_IPsec_bool)} onChange={(v) => { setIpsec({ ...ipsec, EtherIP_IPsec_bool: v }); setDirty((d) => ({ ...d, ipsec: true })); }}
                label="EtherIP / L2TPv3 over IPsec" hint="Site-to-site from routers; client IDs are defined below." />
              <div className="row2">
                <Field label="IPsec pre-shared key" hint="What clients type as the 'secret'. Keep it short — some devices truncate at 9 characters.">
                  <input className="input mono" value={secret} onChange={(e) => { setSecret(e.target.value); setDirty((d) => ({ ...d, ipsec: true })); }} autoCapitalize="none" spellCheck={false} />
                </Field>
                <Field label="Default hub" hint="Where L2TP users land when they do not name a hub.">
                  <select className="select" value={String(ipsec.L2TP_DefaultHub_str ?? "")} onChange={(e) => { setIpsec({ ...ipsec, L2TP_DefaultHub_str: e.target.value }); setDirty((d) => ({ ...d, ipsec: true })); }}>
                    {hubs.map((h) => <option key={h} value={h}>{h}</option>)}
                  </select>
                </Field>
              </div>
              {dirty.ipsec && (
                <button className="btn btn--primary" onClick={() => void guard(async () => {
                  await api.setIpsec({
                    L2TP_Raw_bool: Boolean(ipsec.L2TP_Raw_bool),
                    L2TP_IPsec_bool: Boolean(ipsec.L2TP_IPsec_bool),
                    EtherIP_IPsec_bool: Boolean(ipsec.EtherIP_IPsec_bool),
                    IPsec_Secret_str: secret,
                    L2TP_DefaultHub_str: String(ipsec.L2TP_DefaultHub_str ?? ""),
                  });
                  setDirty((d) => ({ ...d, ipsec: false }));
                }, "IPsec settings saved.")}>
                  {t("Save IPsec")}
                </button>
              )}
            </>
          )}
        </div>

        {/* OpenVPN / SSTP */}
        <div className="card" style={{ padding: "var(--s4)" }}>
          <div className="section__t" style={{ marginBottom: "var(--s3)" }}>{t("OpenVPN & SSTP clone")}</div>
          {!openvpn ? (
            <LoadingBlock />
          ) : (
            <>
              <CheckRow checked={Boolean(openvpn.EnableOpenVPN_bool)} onChange={(v) => { setOpenvpn({ ...openvpn, EnableOpenVPN_bool: v }); setDirty((d) => ({ ...d, openvpn: true })); }}
                label="OpenVPN server" hint="Stock OpenVPN clients connect straight to this server." />
              <Field label="OpenVPN UDP ports" hint="Comma separated.">
                <input className="input mono" value={String(openvpn.OpenVPNPortList_str ?? "")} onChange={(e) => { setOpenvpn({ ...openvpn, OpenVPNPortList_str: e.target.value }); setDirty((d) => ({ ...d, openvpn: true })); }} spellCheck={false} inputMode="numeric" />
              </Field>
              <CheckRow checked={Boolean(openvpn.EnableSSTP_bool)} onChange={(v) => { setOpenvpn({ ...openvpn, EnableSSTP_bool: v }); setDirty((d) => ({ ...d, openvpn: true })); }}
                label="SSTP server" hint="Microsoft SSTP over 443. Needs the server certificate to be trusted by clients." />
              <div style={{ display: "flex", gap: "var(--s2)", flexWrap: "wrap" }}>
                {dirty.openvpn && (
                  <button className="btn btn--primary" onClick={() => void guard(async () => {
                    await api.setOpenvpn({
                      EnableOpenVPN_bool: Boolean(openvpn.EnableOpenVPN_bool),
                      OpenVPNPortList_str: String(openvpn.OpenVPNPortList_str ?? "1194"),
                      EnableSSTP_bool: Boolean(openvpn.EnableSSTP_bool),
                    });
                    setDirty((d) => ({ ...d, openvpn: false }));
                  }, t("OpenVPN/SSTP settings saved."))}>
                    {t("Save")}
                  </button>
                )}
                <button className="btn" disabled={!openvpn.EnableOpenVPN_bool} onClick={() => void guard(async () => {
                  const r = await api.openvpnSample();
                  downloadText(r.filename || "openvpn-client.ovpn", r.config, "application/x-openvpn-profile");
                }, t("OpenVPN client template downloaded."))}>
                  <IconDownload size={14} /> {t("OpenVPN client template (.ovpn)")}
                </button>
              </div>
            </>
          )}
        </div>
      </div>

      <div style={{ marginTop: "var(--s4)" }}>
        <AzureDdnsCard />
      </div>
    </section>
  );
}

/* ── encryption & certificate ─────────────────────────────────────────────── */

function EncryptionCard() {
  const api = useServerApi();
  const [cipher, setCipher] = useState<string | null>(null);
  const [cipherDirty, setCipherDirty] = useState(false);
  const [adminPw, setAdminPw] = useState("");
  const [regenCn, setRegenCn] = useState("");
  const [confirmingRegen, setConfirmingRegen] = useState(false);
  const { guard } = useToast();

  useEffect(() => {
    void api.cipher().then((r) => setCipher(String(r.String_str ?? ""))).catch(() => {});
  }, []);

  const uploadCertAndKey = async (certFile: File, keyFile: File) => {
    const [c, k] = await Promise.all([fileToBase64(certFile), fileToBase64(keyFile)]);
    await guard(() => api.setCert(c, k), "Certificate replaced.");
  };

  return (
    <section id="ss-encryption">
      <SectionTitle>{t("Encryption & certificate")}</SectionTitle>
      <div style={{ display: "grid", gap: "var(--s4)", minWidth: 0 }}>
        <div className="card" style={{ padding: "var(--s4)" }}>
          <div className="row2">
            <div>
              <div className="section__t" style={{ marginBottom: "var(--s3)" }}>{t("Cipher")}</div>
              <Field label="TLS cipher for VPN sessions" hint="As SoftEther names them, e.g. AES128-SHA. What the server accepts depends on its build.">
                <input className="input mono" value={cipher ?? ""} onChange={(e) => { setCipher(e.target.value); setCipherDirty(true); }} spellCheck={false} />
              </Field>
              {cipherDirty && (
                <button className="btn btn--primary" onClick={() => void guard(async () => {
                  await api.setCipher(cipher ?? "");
                  setCipherDirty(false);
                }, "Cipher saved.")}>
                  {t("Save cipher")}
                </button>
              )}
            </div>
            <div>
              <div className="section__t" style={{ marginBottom: "var(--s3)" }}>{t("Administrator password")}</div>
              <Field label="New password" hint="Changes the VPN server's own admin password; the panel updates its stored copy in the same motion.">
                <div style={{ display: "flex", gap: "var(--s2)" }}>
                  <input className="input" type="password" value={adminPw} onChange={(e) => setAdminPw(e.target.value)} autoComplete="off" />
                  <button className="btn" disabled={!adminPw} onClick={() => void guard(async () => {
                    await api.setAdminPassword(adminPw);
                    setAdminPw("");
                  }, "Administrator password changed.")}>
                    {t("Change")}
                  </button>
                </div>
              </Field>
            </div>
          </div>
        </div>

        <div className="card" style={{ padding: "var(--s4)" }}>
          <div className="section__t" style={{ marginBottom: "var(--s2)" }}>{t("Server certificate")}</div>
          <IssueCertificateSection />
          <div className="section__t" style={{ margin: "var(--s4) 0 var(--s3)" }}>{t("Install a certificate you already have")}</div>
          <div style={{ display: "flex", gap: "var(--s2)", flexWrap: "wrap", marginBottom: "var(--s3)" }}>
            <button className="btn" onClick={() => void guard(async () => {
              const r = await api.cert();
              downloadBase64("server.cer", String(r.cert_base64 ?? ""), "application/pkix-cert");
            })}>
              <IconDownload size={14} /> {t("Download certificate")}
            </button>
            <button className="btn" onClick={() => setConfirmingRegen(true)}>{t("Regenerate self-signed")}</button>
          </div>
          <CertUpload onUpload={uploadCertAndKey} />
        </div>
      </div>
      {confirmingRegen && (
        <Sheet
          title={t("Regenerate the server certificate")}
          onClose={() => setConfirmingRegen(false)}
          footer={
            <>
              <button className="btn" onClick={() => setConfirmingRegen(false)}>{t("Cancel")}</button>
              <button className="btn btn--danger" disabled={!regenCn.trim()} onClick={() => void guard(async () => {
                await api.regenerateCert(regenCn.trim());
                setConfirmingRegen(false);
              }, t("Certificate regenerated."))}>
                {t("Regenerate")}
              </button>
            </>
          }
        >
          <div className="lede" style={{ marginBottom: "var(--s3)" }}>
            {t("A new self-signed certificate replaces the current one immediately. Clients that pinned the old one (and SSTP clients) will complain until they trust the new one.")}
          </div>
          <Field label="Common name (CN)" hint="Usually the server's public hostname.">
            <input className="input mono" value={regenCn} onChange={(e) => setRegenCn(e.target.value)} placeholder="vpn.example.net" spellCheck={false} autoCapitalize="none" />
          </Field>
        </Sheet>
      )}
    </section>
  );
}

function CertUpload({ onUpload }: { onUpload: (cert: File, key: File) => Promise<void> }) {
  const [cert, setCert] = useState<File | null>(null);
  const [key, setKey] = useState<File | null>(null);
  const [busy, setBusy] = useState(false);
  return (
    <>
      <div className="row2">
        <Field label="Replace with your own (certificate + private key)">
          <input className="input" type="file" accept=".cer,.crt,.pem" onChange={(e) => setCert(e.target.files?.[0] ?? null)} />
        </Field>
        <Field label="Private key">
          <input className="input" type="file" accept=".key,.pem" onChange={(e) => setKey(e.target.files?.[0] ?? null)} />
        </Field>
      </div>
      <div style={{ marginTop: "var(--s3)" }}>
        <button
          className="btn btn--primary"
          disabled={!cert || !key || busy}
          onClick={async () => {
            if (!cert || !key) return;
            setBusy(true);
            await onUpload(cert, key);
            setBusy(false);
            setCert(null);
            setKey(null);
          }}
        >
          {busy && <span className="spin" />} <IconUpload size={14} /> {t("Install certificate")}
        </button>
      </div>
    </>
  );
}


/* ── Let's Encrypt certificate for this server ────────────────────────────── */

const issueForm = (s: DomainStatus | null) => ({
  domain: s?.domain ?? (s?.vpn_public_host ?? ""),
  acme_email: s?.acme_email ?? "",
  acme_staging: Boolean(s?.acme_staging),
  acme_challenge_type: (s?.acme_challenge_type as "dns-01" | "http-01") || "dns-01",
  cf_dns_token: s?.cf_dns_token ?? "",
  cert_key_type: (s?.cert_key_type as "rsa2048" | "ecdsa") || "rsa2048",
  cert_auto_push: Boolean(s?.cert_auto_push && s.cert_push_server_id && s.cert_push_server_id === getStoredServerId()),
  cert_renew_hook: s?.cert_renew_hook ?? "",
});

/** Label, verdict, explanation: one line per thing that can be wrong. */
function CertRow({ label, pill, children, error }: { label: string; pill: ReactNode; children?: ReactNode; error?: boolean }) {
  return (
    <div className="statrow">
      <span className="statrow__k">{label}</span>
      <span>{pill}</span>
      <span className={`statrow__v${error ? " statrow__v--err" : ""}`}>{children}</span>
    </div>
  );
}

/**
 * The certificate this server presents, obtained from Let's Encrypt. DNS-01
 * through Cloudflare needs no open port; HTTP-01 uses the panel's port-80
 * listener. The pair lands in ssl/ and, when auto push is on, is installed on
 * the current server over RPC -- on issue and on every renewal.
 */
function IssueCertificateSection() {
  const api = useServerApi();
  const [serverId] = useState(getStoredServerId);
  const [state, setState] = useState<DomainStatus | null>(null);
  const [form, setForm] = useState(issueForm(null));
  const [dirty, setDirty] = useState(false);
  const [saving, setSaving] = useState(false);
  const [acting, setActing] = useState<"" | "issue" | "check">("");
  const [pushing, setPushing] = useState(false);
  const [showEvents, setShowEvents] = useState(false);
  const { guard, push } = useToast();

  const absorb = useCallback(
    (s: DomainStatus) => {
      setState(s);
      if (!dirty) setForm(issueForm(s));
    },
    [dirty],
  );
  const load = useCallback(async () => {
    const s = await api.domain().catch(() => null);
    if (s) absorb(s);
    return s;
  }, [absorb]);
  // While a certificate is being obtained the card follows along closely;
  // otherwise it re-reads at the detail tier like everything else.
  usePoll(load, state?.busy ? 2500 : "detail", [state?.busy]);

  const set = (patch: Partial<ReturnType<typeof issueForm>>) => {
    setForm((f) => ({ ...f, ...patch }));
    setDirty(true);
  };

  const save = async () => {
    if (form.acme_challenge_type === "dns-01" && !form.cf_dns_token.trim()) {
      push("err", t("Cloudflare DNS API Token is required when using DNS-01 verification."));
      return;
    }
    setSaving(true);
    await guard(async () => {
      const s = await api.saveDomain({
        domain: form.domain.trim(),
        acme_email: form.acme_email.trim(),
        acme_staging: form.acme_staging,
        acme_challenge_type: form.acme_challenge_type,
        cf_dns_token: form.cf_dns_token.trim(),
        cert_key_type: form.cert_key_type,
        cert_auto_push: form.cert_auto_push || Boolean(state?.cert_auto_push && state.cert_push_server_id && state.cert_push_server_id !== serverId),
        cert_push_server_id: form.cert_auto_push ? serverId ?? undefined : state?.cert_push_server_id === serverId ? 0 : undefined,
        cert_renew_hook: form.cert_renew_hook.trim(),
      });
      setDirty(false);
      setState(s);
      setForm(issueForm(s));
    }, form.domain.trim() ? t("Domain saved — obtaining certificate.") : t("Domain removed."));
    setSaving(false);
  };

  const issue = async () => {
    setActing("issue");
    await guard(async () => {
      const s = await api.issueCertificate();
      absorb(s);
    }, t("Certificate request started."));
    setActing("");
  };

  const check = async () => {
    setActing("check");
    await guard(async () => {
      const s = await api.checkDomain();
      absorb(s);
    });
    setActing("");
  };

  const pushCert = async () => {
    setPushing(true);
    await guard(async () => {
      const res = await api.pushDomainCert();
      push("ok", res.message || t("Certificate pushed to SoftEther VPN server."));
    }, t("Pushing certificate to SoftEther VPN server…"));
    setPushing(false);
  };

  const s = state;
  const busy = Boolean(s?.busy);
  const cert = s?.certificate;
  const certOk = Boolean(cert?.present && !cert?.stale);
  const daysLeft = cert?.days_left ?? null;
  const challenge = form.acme_challenge_type;

  // One line per problem. The last issuance error usually *is* the DNS or
  // port-80 problem already listed, so it is added only when it says
  // something the stage rows do not.
  const attention: string[] = [];
  if (s && s.configured && !busy) {
    if (s.config_error) attention.push(s.config_error);
    if (s.deployment_error) attention.push(s.deployment_error);
    if (s.dns?.error) attention.push(s.dns.error);
    if (challenge === "http-01" && s.http.error) attention.push(t("Port 80: {error}", { error: s.http.error }));
    const stageError = s.renewal.last_error_type === "dns" || s.renewal.last_error_type === "port80";
    if (s.renewal.last_error && (!certOk || s.renewal.due) && !stageError) attention.push(s.renewal.last_error);
  }
  const blockedBy =
    s?.renewal.last_error_type === "dns"
      ? t("Waiting on the DNS record above.")
      : s?.renewal.last_error_type === "port80"
        ? t("Waiting on port 80 above.")
        : "";

  return (
    <>
      <div className="section__t" style={{ margin: "var(--s4) 0 var(--s2)" }}>{t("Let's Encrypt certificate")}</div>
      <p className="hint" style={{ marginTop: 0, marginBottom: "var(--s3)" }}>
        {t("Issue a certificate for the name below and install it on this server. Cloudflare DNS-01 needs no open port; HTTP-01 uses the panel's port 80.")}
      </p>

      <Field
        label={t("Certificate domain")}
        hint={
          s?.vpn_public_host
            ? t("Leave empty to use server public domain ({host}).", { host: s.vpn_public_host })
            : t("A public hostname that resolves to this server, e.g. vpn.example.com. Empty means no certificate.")
        }
      >
        <input
          className="input mono"
          value={form.domain}
          onChange={(e) => set({ domain: e.target.value })}
          placeholder={s?.vpn_public_host || "vpn.example.com"}
          autoCapitalize="none"
          spellCheck={false}
          inputMode="url"
        />
      </Field>

      <div className="row2">
        <Field label={t("Verification method")} hint={t("DNS-01 uses Cloudflare DNS API without requiring open port 80.")}>
          <select className="input" value={challenge} onChange={(e) => set({ acme_challenge_type: e.target.value as "dns-01" | "http-01" })}>
            <option value="dns-01">{t("Cloudflare DNS (DNS-01) - Recommended")}</option>
            <option value="http-01">{t("HTTP-01 (Requires public Port 80)")}</option>
          </select>
        </Field>
        <Field label={t("Certificate Key Type")} hint={t("SoftEther VPN requires RSA 2048 for full compatibility.")}>
          <select className="input" value={form.cert_key_type} onChange={(e) => set({ cert_key_type: e.target.value as "rsa2048" | "ecdsa" })}>
            <option value="rsa2048">{t("RSA 2048 (SoftEther compatible / Default)")}</option>
            <option value="ecdsa">{t("ECDSA P-256")}</option>
          </select>
        </Field>
      </div>

      {challenge === "dns-01" && (
        <Field label={t("Cloudflare API Token")} hint={t("API token with Zone:Read and DNS:Edit permissions for the domain on Cloudflare.")}>
          <input
            className="input mono"
            type="password"
            value={form.cf_dns_token}
            onChange={(e) => set({ cf_dns_token: e.target.value })}
            placeholder={t("Cloudflare DNS API Token")}
            autoCapitalize="none"
            spellCheck={false}
          />
        </Field>
      )}

      <div className="row2">
        <Field label={t("Contact email (optional)")} hint={t("Let's Encrypt writes to it if a certificate is about to expire unrenewed. Nothing else is done with it.")}>
          <input className="input mono" value={form.acme_email} onChange={(e) => set({ acme_email: e.target.value })} placeholder="ops@example.com" autoCapitalize="none" spellCheck={false} inputMode="email" />
        </Field>
        <Field label={t("Custom renewal hook (optional)")} hint={t("Optional script path executed when certificate is renewed (e.g. ssl/hook.sh).")}>
          <input
            className="input mono"
            value={form.cert_renew_hook}
            onChange={(e) => set({ cert_renew_hook: e.target.value })}
            placeholder="ssl/hook.sh"
            autoCapitalize="none"
            spellCheck={false}
          />
        </Field>
      </div>

      <div className="row2" style={{ marginBottom: "var(--s2)" }}>
        <CheckRow
          checked={form.cert_auto_push}
          onChange={(v) => set({ cert_auto_push: v })}
          label={t("Auto push certificate to SoftEther VPN server")}
          hint={t("When the certificate is issued or renewed, automatically update the VPN server certificate via RPC.")}
        />
        <CheckRow
          checked={form.acme_staging}
          onChange={(v) => set({ acme_staging: v })}
          label={t("Use Let's Encrypt's staging environment")}
          hint={t("Issues a certificate browsers do not trust, without counting against the real rate limits — for checking DNS and firewall before asking for the real thing. Switch it off afterwards and a trusted certificate is obtained in its place.")}
        />
      </div>
      <p className="hint" style={{ marginTop: 0 }}>
        {challenge === "dns-01"
          ? t("Using Cloudflare DNS-01 challenge. Automatically creates and cleans up _acme-challenge TXT records.")
          : t("Only http-01 validation is used, so the name must resolve straight to this server: no wildcards, no names behind a CDN.")}
      </p>

      {dirty && (
        <div style={{ display: "flex", gap: "var(--s2)", flexWrap: "wrap" }}>
          <button className="btn btn--primary" onClick={() => void save()} disabled={saving}>
            {saving && <span className="spin" />} {form.domain.trim() ? t("Save and obtain the certificate") : t("Save")}
          </button>
          <button className="btn" onClick={() => { setForm(issueForm(state)); setDirty(false); }}>{t("Discard")}</button>
        </div>
      )}

      {s === null ? (
        <LoadingBlock />
      ) : s.configured ? (
        <>
          {attention.length > 0 && (
            <div className="alert alert--err" style={{ display: "block", marginTop: "var(--s3)" }}>
              <div className="alert__t">{t("Needs attention")}</div>
              <ul style={{ margin: "4px 0 0", paddingInlineStart: "1.2em" }}>
                {attention.map((line, i) => <li key={i}>{line}</li>)}
              </ul>
            </div>
          )}
          <div className="statrows" style={{ marginTop: "var(--s3)" }}>
            {busy && (
              <div className="progressline" role="status">
                <span className="spin" /> {s.phase === "renewing" ? t("Renewing the certificate") : t("Obtaining the certificate")}
                {s.step ? ` — ${t(s.step)}` : "…"}
              </div>
            )}

            <CertRow label={t("Domain")} pill={<Pill kind="ok" label="configured" />}>
              <span className="mono">{s.domain}</span>
              {s.acme_staging ? t(" · staging environment") : ""}
            </CertRow>

            <CertRow
              label="DNS"
              error={Boolean(s.dns?.error)}
              pill={
                !s.dns?.checked_at ? (
                  <Pill kind="idle" label="not checked yet" />
                ) : s.dns.error ? (
                  <Pill kind="err" label="not resolving" />
                ) : s.dns.match ? (
                  <Pill kind="ok" label="points here" />
                ) : (
                  <Pill kind="warn" label="points elsewhere" />
                )
              }
            >
              {!s.dns?.checked_at
                ? t("Checked when the certificate is requested, or with Re-check.")
                : s.dns.error
                  ? s.dns.error
                  : s.dns.match
                    ? <>{t("Resolves to ")}<span className="mono">{(s.dns.addresses ?? []).join(", ")}</span>{t(" — one of this machine's addresses.")}</>
                    : <>{t("Resolves to ")}<span className="mono">{(s.dns.addresses ?? []).join(", ")}</span>{t(", but this machine's addresses are ")}<span className="mono">{(s.dns.local_addresses ?? []).join(", ") || t("unknown")}</span>{t(". Behind NAT or a port forward that can still validate; otherwise fix the record.")}</>}
            </CertRow>

            {challenge === "http-01" && (
              <CertRow
                label={t("Port 80")}
                error={Boolean(s.http.error)}
                pill={s.http.listening ? <Pill kind="ok" label="listening" /> : <Pill kind="err" label="not available" />}
              >
                {s.http.listening
                  ? t("Answers Let's Encrypt's validation requests; will redirect to HTTPS once the certificate is in place.")
                  : `${s.http.error || t("The listener is not running.")} ${t("Validation arrives on port 80 and nowhere else, so nothing can be issued until it is free.")}`}
              </CertRow>
            )}

            <CertRow
              label={t("Certificate")}
              error={!busy && !certOk && Boolean(s.renewal.last_error)}
              pill={
                busy ? (
                  <Pill kind="busy" label={s.phase === "renewing" ? "renewing…" : "issuing…"} />
                ) : certOk ? (
                  <Pill kind={daysLeft !== null && daysLeft < s.renewal.renew_before_days ? "warn" : "ok"} label={daysLeft !== null && daysLeft < s.renewal.renew_before_days ? "renewal due" : "valid"} />
                ) : cert?.present ? (
                  <Pill kind="warn" label="needs replacing" />
                ) : s.renewal.last_error ? (
                  <Pill kind="err" label="failed" />
                ) : (
                  <Pill kind="idle" label="none yet" />
                )
              }
            >
              {busy ? (
                s.step ? t(s.step) : t("Working…")
              ) : certOk ? (
                <>
                  {t("Valid until")} <span className="mono">{formatDate(cert!.expires_at)}</span>
                  {daysLeft !== null ? t(" ({days} days)", { days: Math.max(0, Math.floor(daysLeft)) }) : ""}
                  {t(" · issued by {issuer}", { issuer: cert!.issuer || "Let's Encrypt" })}
                  {cert!.staging ? t(" — a staging certificate, which browsers will not trust.") : "."}
                </>
              ) : cert?.present ? (
                cert.expired
                  ? t("The certificate on disk expired {date}; a new one is being requested.", { date: formatDate(cert.expires_at) })
                  : cert.domain !== s.domain
                    ? t("The certificate on disk is for {have}; one for {want} is being requested.", { have: cert.domain, want: s.domain })
                    : t("The certificate on disk came from the {from} environment; one from the {to} environment is being requested.", {
                        from: t(cert.staging ? "staging" : "production"),
                        to: t(s.acme_staging ? "staging" : "production"),
                      })
              ) : s.renewal.last_error ? (
                <>
                  {blockedBy || s.renewal.last_error}
                  {s.renewal.next_attempt_at ? (
                    timeAgo(s.renewal.next_attempt_at) === t("just now")
                      ? t(" Next automatic attempt shortly; or use Issue now once it is fixed.")
                      : t(" Next automatic attempt at {date}; or use Issue now once it is fixed.", { date: formatDate(s.renewal.next_attempt_at) })
                  ) : null}
                </>
              ) : (
                t("Requested shortly after the domain is saved.")
              )}
            </CertRow>

            <CertRow label={t("SSL storage")} pill={<Pill kind="ok" label="ssl/" />}>
              {s.ssl_dir ? (
                <>
                  {t("Saved to ")}<span className="mono">{s.ssl_dir}</span>
                  {s.ssl_files && (
                    <span className="tsub" style={{ marginLeft: 8 }}>
                      ({s.ssl_files.pem ? `${s.domain}.pem, ` : ""}{s.ssl_files.key ? `${s.domain}.key, ${s.domain}.pem.key` : ""})
                    </span>
                  )}
                </>
              ) : (
                t("Saves <domain>.pem, <domain>.key, <domain>.pem.key into ssl/")
              )}
            </CertRow>

            <CertRow
              label={t("Renewal")}
              error={certOk && s.renewal.due && Boolean(s.renewal.last_error)}
              pill={
                !certOk ? (
                  <Pill kind="idle" label="—" />
                ) : s.renewal.due && s.renewal.last_error ? (
                  <Pill kind="err" label="failing" />
                ) : s.renewal.due ? (
                  <Pill kind="busy" label="due" />
                ) : (
                  <Pill kind="ok" label="automatic" />
                )
              }
            >
              {!certOk ? (
                t("Starts once a certificate is in place: renewed automatically a month before it expires.")
              ) : (
                <>
                  {t("Renews on ")}<span className="mono">{formatDate(s.renewal.renew_at)}</span>
                  {t(", {days} days before expiry, checked hourly.", { days: s.renewal.renew_before_days })}
                  {s.renewal.last_attempt_at ? (
                    <>
                      {" "}
                      {t("Last {kind} {when}:", { kind: t(s.renewal.last_attempt_kind || "attempt"), when: timeAgo(s.renewal.last_attempt_at) })}{" "}
                      {s.renewal.last_success_at && s.renewal.last_success_at >= s.renewal.last_attempt_at ? t("succeeded.") : s.renewal.last_error ? t("failed — {error}", { error: s.renewal.last_error }) : t("in progress.")}
                    </>
                  ) : null}
                </>
              )}
            </CertRow>
          </div>

          <div style={{ display: "flex", gap: "var(--s2)", flexWrap: "wrap", marginTop: "var(--s3)" }}>
            <button className={`btn ${certOk ? "" : "btn--primary"}`} onClick={() => void issue()} disabled={busy || acting !== "" || dirty}>
              {acting === "issue" && <span className="spin" />} {certOk ? t("Renew now") : t("Issue certificate now")}
            </button>
            <button className="btn" onClick={() => void check()} disabled={acting !== ""}>
              {acting === "check" ? <span className="spin" /> : <IconRefresh size={14} />} {t("Re-check")}
            </button>
            <button className="btn" onClick={() => void pushCert()} disabled={busy || pushing || dirty || !certOk}>
              {pushing && <span className="spin" />} {t("Push to VPN server")}
            </button>
          </div>
          {dirty && <p className="hint">{t("Save the changes above before requesting a certificate for them.")}</p>}

          {s.events.length > 0 && (
            <>
              <button className="btn btn--sm btn--ghost" onClick={() => setShowEvents((v) => !v)} aria-expanded={showEvents} style={{ marginTop: "var(--s2)" }}>
                {showEvents ? t("Hide history") : t("History ({count})", { count: s.events.length })}
              </button>
              {showEvents && (
                <div className="eventlist">
                  {s.events.map((e, i) => (
                    <div key={i} className={`eventlist__i${e.kind === "failed" ? " eventlist__i--failed" : ""}`}>
                      <span className="mono">{formatDate(e.at)}</span>
                      <span>{e.message}</span>
                    </div>
                  ))}
                </div>
              )}
            </>
          )}
        </>
      ) : (
        <p className="hint" style={{ marginBottom: 0 }}>
          {t("No certificate yet: enter the name this server should answer for and save.")}
        </p>
      )}
    </>
  );
}

/* ── local bridge ─────────────────────────────────────────────────────────── */

function BridgeCard() {
  const [bridges, setBridges] = useState<Wire[] | null>(null);
  const [support, setSupport] = useState<Wire | null>(null);
  const [adding, setAdding] = useState(false);
  const { guard } = useToast();

  const load = useCallback(async () => {
    const [b, s] = await Promise.all([
      api.bridges().catch(() => null),
      api.bridgeSupport().catch(() => null),
    ]);
    if (b) setBridges((b.LocalBridgeList as Wire[]) ?? []);
    if (s) setSupport(s);
  }, []);
  usePoll(load, "list", []);

  return (
    <section id="ss-bridge">
      <SectionTitle
        count={bridges?.length}
        actions={
          <button className="btn btn--sm" onClick={() => setAdding(true)} disabled={support ? !support.IsBridgeSupportedOs_bool : false}>
            <IconPlus size={14} /> {t("New bridge")}
          </button>
        }
      >
        {t("Local bridges")}
      </SectionTitle>
      {support && !support.IsBridgeSupportedOs_bool && (
        <div className="alert alert--warn">{t("This operating system does not support local bridging.")}</div>
      )}
      {bridges === null ? (
        <LoadingBlock />
      ) : bridges.length === 0 ? (
        <Empty title={t("no local bridges")}>
          {t("A local bridge joins a Virtual Hub to a physical network adapter, making VPN clients full members of the LAN behind this server.")}
        </Empty>
      ) : (
        <div className="rows">
          {bridges.map((b, i) => (
            <div key={i} className="row">
              <div className="row__main">
                <div className="row__name">
                  <span className="mono">{String(b.DeviceName_str)}</span>
                  <span className="micro">↔</span>
                  <span className="mono">{String(b.HubNameLB_str)}</span>
                  {b.TapMode_bool && <Pill kind="idle" label="tap" />}
                  <Pill kind={b.Active_bool ? "ok" : b.Online_bool ? "busy" : "idle"} label={b.Active_bool ? "operating" : b.Online_bool ? "starting" : "offline"} />
                </div>
              </div>
              <div className="row__side">
                <button className="btn btn--sm btn--ghost" aria-label={t("Delete bridge")} onClick={() => void guard(async () => {
                  await api.deleteBridge(String(b.DeviceName_str), String(b.HubNameLB_str));
                  await load();
                }, "Bridge deleted.")}>
                  <IconTrash size={14} />
                </button>
              </div>
            </div>
          ))}
        </div>
      )}
      {adding && <BridgeSheet onClose={() => setAdding(false)} onSaved={() => { setAdding(false); void load(); }} />}
    </section>
  );
}

function BridgeSheet({ onClose, onSaved }: { onClose: () => void; onSaved: () => void }) {
  const [devices, setDevices] = useState<Wire[] | null>(null);
  const [hubs, setHubs] = useState<string[]>([]);
  const [device, setDevice] = useState("");
  const [hub, setHub] = useState("");
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState<string | null>(null);
  const { push } = useToast();

  useEffect(() => {
    void api.bridgeDevices().then((r) => setDevices((r.EthList as Wire[]) ?? [])).catch(() => setDevices([]));
    void api.hubs().then((r) => setHubs(((r.HubList as Wire[]) ?? []).map((h) => String(h.HubName_str)))).catch(() => {});
  }, []);

  const save = async () => {
    setBusy(true);
    setError(null);
    try {
      await api.addBridge(device, hub);
      push("ok", t("Bridge created."));
      onSaved();
    } catch (e) {
      setError(e instanceof Error ? e.message : String(e));
      setBusy(false);
    }
  };

  return (
    <Sheet
      title={t("New local bridge")}
      onClose={onClose}
      footer={
        <>
          <button className="btn" onClick={onClose}>{t("Cancel")}</button>
          <button className="btn btn--primary" onClick={save} disabled={busy || !device || !hub}>
            {busy && <span className="spin" />} {t("Create bridge")}
          </button>
        </>
      }
    >
      {error && <ErrorAlert>{error}</ErrorAlert>}
      <Field label="Network adapter">
        {devices === null ? (
          <LoadingBlock />
        ) : (
          <select className="select" value={device} onChange={(e) => setDevice(e.target.value)}>
            <option value="">{t("— choose —")}</option>
            {devices.map((d) => (
              <option key={String(d.DeviceName_str)} value={String(d.DeviceName_str)}>
                {String(d.DeviceName_str)}{d.NetworkConnectionName_utf ? ` (${d.NetworkConnectionName_utf})` : ""}
              </option>
            ))}
          </select>
        )}
      </Field>
      <Field label="Virtual Hub">
        <select className="select" value={hub} onChange={(e) => setHub(e.target.value)}>
          <option value="">{t("— choose —")}</option>
          {hubs.map((h) => <option key={h} value={h}>{h}</option>)}
        </select>
      </Field>
      <div className="hint">
        {t("Bridging the adapter the server itself uses can drop its own connectivity on some NICs; a dedicated adapter is the safe choice.")}
      </div>
    </Sheet>
  );
}

/* ── L3 switches ──────────────────────────────────────────────────────────── */

function L3Card() {
  const [switches, setSwitches] = useState<Wire[] | null>(null);
  const [name, setName] = useState("");
  const [open, setOpen] = useState<string | null>(null);
  const { guard } = useToast();

  const load = useCallback(async () => {
    const r = await api.l3().catch(() => null);
    const list = r ? ((r.L3SWList as Wire[]) ?? []) : null;
    if (list) setSwitches(list);
    return list;
  }, []);
  usePoll(load, "list", []);

  return (
    <section id="ss-l3">
      <SectionTitle count={switches?.length}>{t("Virtual layer-3 switches")}</SectionTitle>
      <div className="card" style={{ padding: "var(--s4)" }}>
        <div className="lede" style={{ marginBottom: "var(--s3)" }}>
          {t("An IP router between Virtual Hubs: each interface sits in one hub with one address, and the routing table moves packets between them.")}
        </div>
        {switches === null ? (
          <LoadingBlock />
        ) : (
          <>
            {switches.map((s) => (
              <div key={String(s.Name_str)} className="lrow">
                <span className="mono" style={{ fontWeight: 600 }}>{String(s.Name_str)}</span>
                <Pill kind={s.Active_bool ? "ok" : "idle"} label={s.Active_bool ? "running" : "stopped"} />
                <span className="micro">{t("{n} if · {routes} routes", { n: String(s.NumInterfaces_u32 ?? 0), routes: String(s.NumTables_u32 ?? 0) })}</span>
                <span style={{ flex: 1 }} />
                <button className="btn btn--sm" onClick={() => setOpen(String(s.Name_str))}>{t("Configure")}</button>
                <L3Switch sw={s} reload={load} />
                <button className="btn btn--sm btn--ghost" aria-label={t("Delete")} onClick={() => void guard(async () => {
                  await api.delL3(String(s.Name_str));
                  await load();
                }, "Switch deleted.")}>
                  <IconTrash size={14} />
                </button>
              </div>
            ))}
            <div style={{ display: "flex", gap: "var(--s2)", marginTop: "var(--s3)" }}>
              <input className="input mono" placeholder={t("switch name")} value={name} onChange={(e) => setName(e.target.value)} style={{ maxWidth: 240 }} autoCapitalize="none" spellCheck={false} />
              <button className="btn" disabled={!name.trim()} onClick={() => void guard(async () => {
                await api.addL3(name.trim());
                setName("");
                await load();
              }, "Switch created.")}>
                <IconPlus size={14} /> {t("Add")}
              </button>
            </div>
          </>
        )}
      </div>
      {open && <L3Sheet name={open} onClose={() => { setOpen(null); void load(); }} />}
    </section>
  );
}

/** Running / stopped for one layer-3 switch. */
function L3Switch({ sw, reload }: { sw: Wire; reload: () => Promise<Wire[] | null> }) {
  const name = String(sw.Name_str);
  const toggle = useToggle({
    value: Boolean(sw.Active_bool),
    apply: async (next) => {
      const r = await (next ? api.startL3(name) : api.stopL3(name));
      return Boolean(r.active);
    },
    reload: async () => {
      const list = await reload();
      const me = list?.find((x) => String(x.Name_str) === name);
      return me ? Boolean(me.Active_bool) : null;
    },
    noun: t("Layer-3 switch {name}", { name }),
    onWord: "running",
    offWord: "stopped",
  });
  return (
    <span className="switchbox">
      <Switch
        on={Boolean(sw.Active_bool)}
        pending={toggle.pending}
        target={toggle.target}
        onToggle={() => void toggle.toggle()}
        label={t("Layer-3 switch {name}", { name })}
        onWord="running"
        offWord="stopped"
      />
      <OutcomeNote outcome={toggle.outcome} />
    </span>
  );
}

function L3Sheet({ name, onClose }: { name: string; onClose: () => void }) {
  const [ifs, setIfs] = useState<Wire[] | null>(null);
  const [routes, setRoutes] = useState<Wire[] | null>(null);
  const [hubs, setHubs] = useState<string[]>([]);
  const [ifDraft, setIfDraft] = useState({ hub: "", ip: "", mask: "255.255.255.0" });
  const [routeDraft, setRouteDraft] = useState({ net: "", mask: "255.255.255.0", gw: "", metric: 1 });
  const { guard } = useToast();

  const load = useCallback(async () => {
    const [i, r, h] = await Promise.all([
      api.l3Ifs(name).catch(() => null),
      api.l3Routes(name).catch(() => null),
      api.hubs().catch(() => null),
    ]);
    if (i) setIfs((i.L3IFList as Wire[]) ?? []);
    if (r) setRoutes((r.L3Table as Wire[]) ?? []);
    if (h) setHubs(((h.HubList as Wire[]) ?? []).map((x) => String(x.HubName_str)));
  }, [name]);
  useEffect(() => void load(), [load]);

  return (
    <Sheet title={t("Layer-3 switch {name}", { name })} subtitle={t("Stop the switch before changing it; SoftEther refuses edits while it runs.")} onClose={onClose} wide>
      <SectionTitle count={ifs?.length ?? undefined}>{t("Interfaces")}</SectionTitle>
      {(ifs ?? []).map((i, index) => (
        <div key={index} className="lrow">
          <span className="mono">{String(i.HubName_str)}</span>
          <span className="mono">{String(i.IpAddress_ip)}/{String(i.SubnetMask_ip)}</span>
          <span style={{ flex: 1 }} />
          <button className="btn btn--sm btn--ghost" aria-label={t("Delete")} onClick={() => void guard(async () => {
            await api.delL3If(name, { HubName_str: i.HubName_str });
            await load();
          })}>
            <IconTrash size={14} />
          </button>
        </div>
      ))}
      <div className="row2" style={{ marginTop: "var(--s2)" }}>
        <select className="select" value={ifDraft.hub} onChange={(e) => setIfDraft({ ...ifDraft, hub: e.target.value })}>
          <option value="">— hub —</option>
          {hubs.map((h) => <option key={h} value={h}>{h}</option>)}
        </select>
        <div style={{ display: "flex", gap: "var(--s2)" }}>
          <input className="input mono" placeholder="192.168.1.1" value={ifDraft.ip} onChange={(e) => setIfDraft({ ...ifDraft, ip: e.target.value })} spellCheck={false} />
          <input className="input mono" placeholder={t("mask")} value={ifDraft.mask} onChange={(e) => setIfDraft({ ...ifDraft, mask: e.target.value })} spellCheck={false} />
        </div>
      </div>
      <button className="btn btn--sm" style={{ marginTop: "var(--s2)" }} disabled={!ifDraft.hub || !ifDraft.ip} onClick={() => void guard(async () => {
        await api.addL3If(name, { HubName_str: ifDraft.hub, IpAddress_ip: ifDraft.ip, SubnetMask_ip: ifDraft.mask });
        setIfDraft({ hub: "", ip: "", mask: "255.255.255.0" });
        await load();
      }, "Interface added.")}>
        <IconPlus size={14} /> {t("Add interface")}
      </button>

      <SectionTitle count={routes?.length ?? undefined}>{t("Routing table")}</SectionTitle>
      {(routes ?? []).map((r, index) => (
        <div key={index} className="lrow">
          <span className="mono">{String(r.NetworkAddress_ip)}/{String(r.SubnetMask_ip)}</span>
          <span className="micro">{t("via")}</span>
          <span className="mono">{String(r.GatewayAddress_ip)}</span>
          <span className="micro">metric {String(r.Metric_u32)}</span>
          <span style={{ flex: 1 }} />
          <button className="btn btn--sm btn--ghost" aria-label={t("Delete")} onClick={() => void guard(async () => {
            await api.delL3Route(name, {
              NetworkAddress_ip: r.NetworkAddress_ip, SubnetMask_ip: r.SubnetMask_ip,
              GatewayAddress_ip: r.GatewayAddress_ip, Metric_u32: r.Metric_u32,
            });
            await load();
          })}>
            <IconTrash size={14} />
          </button>
        </div>
      ))}
      <div className="row2" style={{ marginTop: "var(--s2)" }}>
        <div style={{ display: "flex", gap: "var(--s2)" }}>
          <input className="input mono" placeholder="10.0.0.0" value={routeDraft.net} onChange={(e) => setRouteDraft({ ...routeDraft, net: e.target.value })} spellCheck={false} />
          <input className="input mono" placeholder={t("mask")} value={routeDraft.mask} onChange={(e) => setRouteDraft({ ...routeDraft, mask: e.target.value })} spellCheck={false} />
        </div>
        <div style={{ display: "flex", gap: "var(--s2)" }}>
          <input className="input mono" placeholder={t("gateway")} value={routeDraft.gw} onChange={(e) => setRouteDraft({ ...routeDraft, gw: e.target.value })} spellCheck={false} />
          <input className="input mono" type="number" min={1} style={{ maxWidth: 90 }} value={routeDraft.metric} onChange={(e) => setRouteDraft({ ...routeDraft, metric: Number(e.target.value) })} />
        </div>
      </div>
      <button className="btn btn--sm" style={{ marginTop: "var(--s2)" }} disabled={!routeDraft.net || !routeDraft.gw} onClick={() => void guard(async () => {
        await api.addL3Route(name, {
          NetworkAddress_ip: routeDraft.net, SubnetMask_ip: routeDraft.mask,
          GatewayAddress_ip: routeDraft.gw, Metric_u32: routeDraft.metric,
        });
        setRouteDraft({ net: "", mask: "255.255.255.0", gw: "", metric: 1 });
        await load();
      }, "Route added.")}>
        <IconPlus size={14} /> {t("Add route")}
      </button>
    </Sheet>
  );
}

/* ── EtherIP ids ──────────────────────────────────────────────────────────── */

function EtherIpCard() {
  const [ids, setIds] = useState<Wire[] | null>(null);
  const [adding, setAdding] = useState(false);
  const [hubs, setHubs] = useState<string[]>([]);
  const [draft, setDraft] = useState({ id: "", hub: "", user: "", password: "" });
  const { guard } = useToast();

  const load = useCallback(async () => {
    const r = await api.etherip().catch(() => null);
    if (r) setIds((r.Settings as Wire[]) ?? []);
  }, []);
  usePoll(load, "list", []);

  useEffect(() => {
    void api.hubs().then((r) => setHubs(((r.HubList as Wire[]) ?? []).map((h) => String(h.HubName_str)))).catch(() => {});
  }, []);

  return (
    <section id="ss-etherip">
      <SectionTitle
        count={ids?.length}
        actions={
          <button className="btn btn--sm" onClick={() => setAdding(true)}>
            <IconPlus size={14} /> {t("Add client ID")}
          </button>
        }
      >
        {t("EtherIP / L2TPv3 client IDs")}
      </SectionTitle>
      {ids === null ? (
        <LoadingBlock />
      ) : ids.length === 0 ? (
        <Empty title={t("no client IDs")}>
          {t("Router-to-router EtherIP/L2TPv3 over IPsec needs each device's ISAKMP ID mapped to a hub and a user here.")}
        </Empty>
      ) : (
        <div className="rows">
          {ids.map((entry) => (
            <div key={String(entry.Id_str)} className="row">
              <div className="row__main">
                <div className="row__name mono">{String(entry.Id_str)}</div>
                <div className="spec">
                  <span className="chip"><i>{t("hub")}</i>{String(entry.HubName_str ?? "?")}</span>
                  <span className="chip"><i>{t("user")}</i>{String(entry.UserName_str ?? "?")}</span>
                </div>
              </div>
              <div className="row__side">
                <button className="btn btn--sm btn--ghost" aria-label={t("Delete")} onClick={() => void guard(async () => {
                  await api.deleteEtherip(String(entry.Id_str));
                  await load();
                }, t("Client ID removed."))}>
                  <IconTrash size={14} />
                </button>
              </div>
            </div>
          ))}
        </div>
      )}
      {adding && (
        <Sheet
          title={t("EtherIP client ID")}
          onClose={() => setAdding(false)}
          footer={
            <>
              <button className="btn" onClick={() => setAdding(false)}>{t("Cancel")}</button>
              <button className="btn btn--primary" disabled={!draft.id || !draft.hub || !draft.user} onClick={() => void guard(async () => {
                await api.addEtherip({
                  Id_str: draft.id.trim(), HubName_str: draft.hub, UserName_str: draft.user.trim(), Password_str: draft.password,
                });
                setAdding(false);
                setDraft({ id: "", hub: "", user: "", password: "" });
                await load();
              }, "Client ID added.")}>
                {t("Add")}
              </button>
            </>
          }
        >
          <Field label="ISAKMP phase-1 ID" hint="What the device announces, e.g. its hostname or IP.">
            <input className="input mono" value={draft.id} onChange={(e) => setDraft({ ...draft, id: e.target.value })} spellCheck={false} autoCapitalize="none" />
          </Field>
          <Field label="Virtual Hub">
            <select className="select" value={draft.hub} onChange={(e) => setDraft({ ...draft, hub: e.target.value })}>
              <option value="">{t("— choose —")}</option>
              {hubs.map((h) => <option key={h} value={h}>{h}</option>)}
            </select>
          </Field>
          <div className="row2">
            <Field label="Username">
              <input className="input mono" value={draft.user} onChange={(e) => setDraft({ ...draft, user: e.target.value })} spellCheck={false} autoCapitalize="none" />
            </Field>
            <Field label="Password">
              <input className="input" type="password" value={draft.password} onChange={(e) => setDraft({ ...draft, password: e.target.value })} autoComplete="off" />
            </Field>
          </div>
        </Sheet>
      )}
    </section>
  );
}

/* ── clustering ───────────────────────────────────────────────────────────── */

function FarmCard() {
  const [farm, setFarm] = useState<Wire | null>(null);
  const [members, setMembers] = useState<Wire[] | null>(null);

  const load = useCallback(async () => {
    setFarm(await api.farm().catch(() => null));
    const m = await api.farmMembers().catch(() => null);
    if (m) setMembers((m.FarmMemberList as Wire[]) ?? []);
  }, []);
  usePoll(load, "list", []);

  const TYPES: Record<number, string> = { 0: t("standalone"), 1: t("farm controller"), 2: t("farm member") };

  return (
    <section id="ss-farm">
      <SectionTitle>{t("Clustering")}</SectionTitle>
      <div className="card" style={{ padding: "var(--s4)" }}>
        {!farm ? (
          <LoadingBlock />
        ) : (
          <>
            <KV rows={[
              ["role", TYPES[Number(farm.ServerType_u32)] ?? "?"],
              ["controller", Number(farm.ServerType_u32) === 2 ? `${farm.ControllerName_str}:${farm.ControllerPort_u32}` : "—"],
              ["public IP", String(farm.PublicIp_ip || "—")],
              ["weight", String(farm.Weight_u32 ?? "—")],
            ]} />
            {Number(farm.ServerType_u32) === 1 && members && (
              <>
                <div className="section__t" style={{ margin: "var(--s4) 0 var(--s2)" }}>{t("Members")}</div>
                {members.map((m) => (
                  <div key={String(m.Id_u32)} className="lrow">
                    <Pill kind={m.Controller_bool ? "busy" : "ok"} label={m.Controller_bool ? "controller" : "member"} />
                    <span className="mono">{String(m.Hostname_str)}</span>
                    <span className="micro">{t("{n} sessions · {hubs} hubs", { n: String(m.NumSessions_u32 ?? 0), hubs: String(m.NumHubs_u32 ?? 0) })}</span>
                  </div>
                ))}
              </>
            )}
            <div className="hint" style={{ marginTop: "var(--s3)" }}>
              {t("Changing the clustering role restarts the VPN server and erases parts of its state. Use the SoftEther Server Manager to provision the cluster role.")}
            </div>
          </>
        )}
      </div>
    </section>
  );
}

/* ── keep-alive & syslog ──────────────────────────────────────────────────── */

function KeepSyslogCard() {
  const [keep, setKeep] = useState<Wire | null>(null);
  const [syslog, setSyslog] = useState<Wire | null>(null);
  const [dirty, setDirty] = useState<Record<string, boolean>>({});
  const { guard } = useToast();

  useEffect(() => {
    void api.keepalive().then(setKeep).catch(() => {});
    void api.syslog().then(setSyslog).catch(() => {});
  }, []);

  const SYSLOG_MODES = [
    "off",
    "server log only",
    "server + hub security logs",
    "server + hub security + packet logs",
  ];

  return (
    <section id="ss-keep">
      <SectionTitle>{t("Keep-alive & syslog")}</SectionTitle>
      <div className="setgrid">
        <div className="card" style={{ padding: "var(--s4)" }}>
          <div className="section__t" style={{ marginBottom: "var(--s3)" }}>{t("Internet keep-alive")}</div>
          {!keep ? (
            <LoadingBlock />
          ) : (
            <>
              <CheckRow checked={Boolean(keep.UseKeepConnect_bool)} onChange={(v) => { setKeep({ ...keep, UseKeepConnect_bool: v }); setDirty((d) => ({ ...d, keep: true })); }}
                label="Send keep-alive packets" hint="Stops an idle dial-up/NAT path from being torn down." />
              <div className="row2">
                <Field label="Host">
                  <input className="input mono" value={String(keep.KeepConnectHost_str ?? "")} onChange={(e) => { setKeep({ ...keep, KeepConnectHost_str: e.target.value }); setDirty((d) => ({ ...d, keep: true })); }} spellCheck={false} />
                </Field>
                <Field label="Port">
                  <input className="input mono" type="number" value={Number(keep.KeepConnectPort_u32) || 80} onChange={(e) => { setKeep({ ...keep, KeepConnectPort_u32: Number(e.target.value) }); setDirty((d) => ({ ...d, keep: true })); }} />
                </Field>
              </div>
              <div className="row2">
                <Field label="Protocol">
                  <select className="select" value={Number(keep.KeepConnectProtocol_u32) || 0} onChange={(e) => { setKeep({ ...keep, KeepConnectProtocol_u32: Number(e.target.value) }); setDirty((d) => ({ ...d, keep: true })); }}>
                    <option value={0}>{t("TCP")}</option>
                    <option value={1}>{t("UDP")}</option>
                  </select>
                </Field>
                <Field label="Interval (s)">
                  <input className="input mono" type="number" min={5} value={Number(keep.KeepConnectInterval_u32) || 50} onChange={(e) => { setKeep({ ...keep, KeepConnectInterval_u32: Number(e.target.value) }); setDirty((d) => ({ ...d, keep: true })); }} />
                </Field>
              </div>
              {dirty.keep && (
                <button className="btn btn--primary" onClick={() => void guard(async () => {
                  await api.setKeepalive({
                    UseKeepConnect_bool: Boolean(keep.UseKeepConnect_bool),
                    KeepConnectHost_str: String(keep.KeepConnectHost_str ?? ""),
                    KeepConnectPort_u32: Number(keep.KeepConnectPort_u32) || 80,
                    KeepConnectProtocol_u32: Number(keep.KeepConnectProtocol_u32) || 0,
                    KeepConnectInterval_u32: Number(keep.KeepConnectInterval_u32) || 50,
                  });
                  setDirty((d) => ({ ...d, keep: false }));
                }, "Keep-alive saved.")}>
                  {t("Save keep-alive")}
                </button>
              )}
            </>
          )}
        </div>

        <div className="card" style={{ padding: "var(--s4)" }}>
          <div className="section__t" style={{ marginBottom: "var(--s3)" }}>{t("Syslog forwarding")}</div>
          {!syslog ? (
            <LoadingBlock />
          ) : (
            <>
              <Field label="Mode">
                <select className="select" value={Number(syslog.SaveType_u32) || 0} onChange={(e) => { setSyslog({ ...syslog, SaveType_u32: Number(e.target.value) }); setDirty((d) => ({ ...d, syslog: true })); }}>
                  {SYSLOG_MODES.map((m, i) => <option key={i} value={i}>{t(m)}</option>)}
                </select>
              </Field>
              <div className="row2">
                <Field label="Syslog host">
                  <input className="input mono" value={String(syslog.Hostname_str ?? "")} onChange={(e) => { setSyslog({ ...syslog, Hostname_str: e.target.value }); setDirty((d) => ({ ...d, syslog: true })); }} spellCheck={false} />
                </Field>
                <Field label="Port">
                  <input className="input mono" type="number" value={Number(syslog.Port_u32) || 514} onChange={(e) => { setSyslog({ ...syslog, Port_u32: Number(e.target.value) }); setDirty((d) => ({ ...d, syslog: true })); }} />
                </Field>
              </div>
              {dirty.syslog && (
                <button className="btn btn--primary" onClick={() => void guard(async () => {
                  await api.setSyslog({
                    SaveType_u32: Number(syslog.SaveType_u32) || 0,
                    Hostname_str: String(syslog.Hostname_str ?? ""),
                    Port_u32: Number(syslog.Port_u32) || 514,
                  });
                  setDirty((d) => ({ ...d, syslog: false }));
                }, t("Syslog settings saved."))}>
                  {t("Save syslog")}
                </button>
              )}
            </>
          )}
        </div>
      </div>
    </section>
  );
}

/* ── configuration file ───────────────────────────────────────────────────── */

function ConfigCard() {
  const api = useServerApi();
  const [restoring, setRestoring] = useState<File | null>(null);
  const [backups, setBackups] = useState<BackupInfo[] | null>(null);
  const [settings, setSettings] = useState<BackupSettings | null>(null);
  const [loadingBackups, setLoadingBackups] = useState(false);
  const [backingUp, setBackingUp] = useState(false);
  const [savingSettings, setSavingSettings] = useState(false);

  // Form states for backup settings
  const [enabled, setEnabled] = useState(true);
  const [intervalHours, setIntervalHours] = useState(24);
  const [backupTime, setBackupTime] = useState("03:00");
  const [backupDir, setBackupDir] = useState("");

  const [confirmRestore, setConfirmRestore] = useState<string | null>(null);
  const [confirmDelete, setConfirmDelete] = useState<string | null>(null);

  const { guard } = useToast();

  const loadData = useCallback(async () => {
    setLoadingBackups(true);
    try {
      const [bRes, sRes] = await Promise.all([
        api.getBackups().catch(() => ({ backups: [] })),
        api.getBackupSettings().catch(() => null),
      ]);
      setBackups(bRes.backups || []);
      if (sRes) {
        setSettings(sRes);
        setEnabled(sRes.enabled);
        setIntervalHours(sRes.interval_hours || 24);
        setBackupTime(sRes.backup_time || "03:00");
        setBackupDir(sRes.configured_dir || "");
      }
    } finally {
      setLoadingBackups(false);
    }
  }, [api]);

  useEffect(() => {
    loadData();
  }, [loadData]);

  const handleManualBackup = async () => {
    setBackingUp(true);
    try {
      await guard(async () => {
        await api.createBackup();
        await loadData();
      }, t("Backup created successfully."));
    } finally {
      setBackingUp(false);
    }
  };

  const handleSaveSettings = async () => {
    setSavingSettings(true);
    try {
      await guard(async () => {
        const res = await api.saveBackupSettings({
          enabled,
          interval_hours: intervalHours,
          backup_time: backupTime,
          backup_dir: backupDir,
        });
        setSettings(res);
      }, t("Backup settings saved."));
    } finally {
      setSavingSettings(false);
    }
  };

  const handleDownloadBackup = async (filename: string) => {
    await guard(async () => {
      const blob = await api.downloadBackup(filename);
      const url = URL.createObjectURL(blob);
      const a = document.createElement("a");
      a.href = url;
      a.download = filename;
      a.click();
      URL.revokeObjectURL(url);
    }, t("Configuration downloaded."));
  };

  return (
    <section id="ss-config">
      <SectionTitle count={backups?.length}>{t("Configuration file")}</SectionTitle>
      <div className="card" style={{ padding: "var(--s4)" }}>
        <div className="lede" style={{ marginBottom: "var(--s3)" }}>
          {t("The server's entire state, as the vpn_server.config it keeps on disk. Download it as a backup; restoring replaces everything and restarts the service.")}
        </div>
        <div style={{ display: "flex", gap: "var(--s2)", flexWrap: "wrap", marginBottom: "var(--s4)" }}>
          <button className="btn btn--primary" onClick={handleManualBackup} disabled={backingUp}>
            <IconPlus size={14} /> {backingUp ? t("Backing up…") : t("Backup now")}
          </button>
          <button className="btn" onClick={() => void guard(async () => {
            const r = await api.getConfig();
            downloadBase64(r.filename || "vpn_server.config", r.config_base64, "text/plain");
          }, t("Configuration downloaded."))}>
            <IconDownload size={14} /> {t("Download current")}
          </button>
          <label className="btn">
            <IconUpload size={14} /> {t("Restore from file")}
            <input type="file" hidden onChange={(e) => setRestoring(e.target.files?.[0] ?? null)} />
          </label>
          <button className="btn" onClick={() => void guard(() => api.flush(), t("Volatile state written to disk."))}>
            {t("Flush to disk")}
          </button>
        </div>

        {/* Auto backup settings */}
        <div style={{ borderTop: "1px solid var(--border)", paddingTop: "var(--s4)", marginTop: "var(--s4)" }}>
          <h4 style={{ margin: "0 0 var(--s3) 0", fontSize: "14px", fontWeight: 600 }}>{t("Auto-backup settings")}</h4>
          <div style={{ display: "flex", flexDirection: "column", gap: "var(--s3)" }}>
            <label style={{ display: "flex", alignItems: "center", gap: "var(--s2)", cursor: "pointer", userSelect: "none" }}>
              <input
                type="checkbox"
                checked={enabled}
                onChange={(e) => setEnabled(e.target.checked)}
              />
              <span style={{ fontWeight: 500 }}>{t("Enable automatic backup")}</span>
            </label>

            {enabled && (
              <div style={{ display: "grid", gridTemplateColumns: "repeat(auto-fit, minmax(200px, 1fr))", gap: "var(--s3)", marginTop: "var(--s1)" }}>
                <div>
                  <label className="label" style={{ display: "block", marginBottom: 4, fontSize: "12px", color: "var(--muted)" }}>
                    {t("Backup schedule")}
                  </label>
                  <select
                    className="select"
                    style={{ width: "100%" }}
                    value={intervalHours}
                    onChange={(e) => setIntervalHours(Number(e.target.value))}
                  >
                    <option value={6}>{t("Every 6 hours")}</option>
                    <option value={12}>{t("Every 12 hours")}</option>
                    <option value={24}>{t("Every 24 hours (daily)")}</option>
                  </select>
                </div>

                {intervalHours >= 24 && (
                  <div>
                    <label className="label" style={{ display: "block", marginBottom: 4, fontSize: "12px", color: "var(--muted)" }}>
                      {t("Time of day")}
                    </label>
                    <input
                      type="time"
                      className="input"
                      style={{ width: "100%" }}
                      value={backupTime}
                      onChange={(e) => setBackupTime(e.target.value)}
                    />
                  </div>
                )}

                <div style={{ gridColumn: intervalHours >= 24 ? "span 2" : "span 1" }}>
                  <label className="label" style={{ display: "block", marginBottom: 4, fontSize: "12px", color: "var(--muted)" }}>
                    {t("Backup directory (mounted)")}
                  </label>
                  <input
                    type="text"
                    className="input mono"
                    style={{ width: "100%" }}
                    placeholder={settings?.backup_dir || "/data/backups"}
                    value={backupDir}
                    onChange={(e) => setBackupDir(e.target.value)}
                  />
                </div>
              </div>
            )}

            <div style={{ fontSize: "12px", color: "var(--muted)", lineHeight: 1.5, marginTop: "var(--s1)" }}>
              ℹ️ {t("Automatic backups retain only the 15 most recent versions. Older versions are deleted automatically.")}
              {settings?.last_run ? <> · {t("Last run")}: {formatDate(settings.last_run)}</> : null}
              {settings?.last_error && <ErrorAlert>{settings.last_error}</ErrorAlert>}
            </div>

            <div>
              <button
                className="btn btn--sm btn--primary"
                onClick={handleSaveSettings}
                disabled={savingSettings}
              >
                {savingSettings ? t("Saving…") : t("Save backup settings")}
              </button>
            </div>
          </div>
        </div>

        {/* Backups List */}
        <div style={{ borderTop: "1px solid var(--border)", paddingTop: "var(--s4)", marginTop: "var(--s4)" }}>
          <h4 style={{ margin: "0 0 var(--s3) 0", fontSize: "14px", fontWeight: 600 }}>
            {t("Backup files")} {backups ? `(${backups.length})` : ""}
          </h4>

          {loadingBackups && !backups ? (
            <LoadingBlock />
          ) : !backups || backups.length === 0 ? (
            <div style={{ color: "var(--muted)", padding: "var(--s2) 0", fontSize: "13px" }}>
              {t("No backups found.")}
            </div>
          ) : (
            <div className="tscroll">
              <table className="dtable">
                <thead>
                  <tr>
                    <th>{t("File")}</th>
                    <th style={{ width: 100 }}>{t("Type")}</th>
                    <th style={{ width: 110 }}>{t("Size")}</th>
                    <th>{t("Date")}</th>
                    <th className="tact" style={{ width: 140 }} aria-label={t("Actions")} />
                  </tr>
                </thead>
                <tbody>
                  {backups.map((b) => (
                    <tr key={b.filename}>
                      <td className="tmono" style={{ fontSize: "12px" }}>{b.filename}</td>
                      <td>
                        <Pill kind={b.is_auto ? "idle" : "ok"} label={b.is_auto ? t("Auto") : t("Manual")} />
                      </td>
                      <td className="tmono">{formatBytes(b.size)}</td>
                      <td className="tmono">{formatDate(b.created_at)}</td>
                      <td className="tact">
                        <div style={{ display: "inline-flex", gap: "var(--s1)" }}>
                          <button
                            className="btn btn--sm"
                            onClick={() => handleDownloadBackup(b.filename)}
                            title={t("Download backup")}
                          >
                            <IconDownload size={13} />
                          </button>
                          <button
                            className="btn btn--sm btn--warn"
                            onClick={() => setConfirmRestore(b.filename)}
                            title={t("Restore this configuration?")}
                          >
                            <IconRefresh size={13} /> {t("Restore")}
                          </button>
                          <button
                            className="btn btn--sm btn--danger"
                            onClick={() => setConfirmDelete(b.filename)}
                            title={t("Delete backup?")}
                          >
                            <IconTrash size={13} />
                          </button>
                        </div>
                      </td>
                    </tr>
                  ))}
                </tbody>
              </table>
            </div>
          )}
        </div>
      </div>

      {/* Restore from uploaded file sheet */}
      {restoring && (
        <ConfirmSheet
          title={t("Restore this configuration?")}
          verb="Restore & restart"
          typed="restore"
          body={
            <>
              <b>{restoring.name}</b> {t("replaces the entire configuration of this VPN server — hubs, users, everything — and the server restarts to load it.")}
            </>
          }
          onClose={() => setRestoring(null)}
          onConfirm={async () => {
            const b64 = await fileToBase64(restoring);
            await api.setConfig(b64);
          }}
        />
      )}

      {/* Restore from existing backup file sheet */}
      {confirmRestore && (
        <ConfirmSheet
          title={t("Restore this backup?")}
          verb="Restore & restart"
          typed="restore"
          body={
            <>
              <b>{confirmRestore}</b> {t("replaces the entire configuration of this VPN server — hubs, users, everything — and the server restarts to load it.")}
            </>
          }
          onClose={() => setConfirmRestore(null)}
          onConfirm={async () => {
            await api.restoreBackup(confirmRestore);
          }}
        />
      )}

      {/* Delete backup sheet */}
      {confirmDelete && (
        <ConfirmSheet
          title={t("Delete backup?")}
          verb="Delete"
          body={
            <>
              <b>{confirmDelete}</b> {t("will be permanently deleted from the backup directory.")}
            </>
          }
          onClose={() => setConfirmDelete(null)}
          onConfirm={async () => {
            await api.deleteBackup(confirmDelete);
            await loadData();
          }}
        />
      )}
    </section>
  );
}

/* ── the danger zone ──────────────────────────────────────────────────────── */

function DangerZone() {
  const [confirm, setConfirm] = useState<null | "reboot" | "crash">(null);
  return (
    <section id="ss-danger" className="danger">
      <SectionTitle>{t("Danger zone")}</SectionTitle>
      <div style={{ display: "flex", gap: "var(--s2)", flexWrap: "wrap" }}>
        <button className="btn" onClick={() => setConfirm("reboot")}>{t("Restart VPN service")}</button>
        <button className="btn" onClick={() => setConfirm("crash")}>{t("Force-crash process")}</button>
      </div>
      {confirm === "reboot" && (
        <ConfirmSheet
          title={t("Restart the VPN server service?")}
          verb="Restart"
          body={<>{t("Every session drops and reconnects. Takes a few seconds.")}</>}
          onClose={() => setConfirm(null)}
          onConfirm={() => api.reboot().then(() => undefined)}
        />
      )}
      {confirm === "crash" && (
        <ConfirmSheet
          title={t("Force-crash the VPN server?")}
          verb="Crash it"
          typed="crash"
          body={
            <>
              {t("The API's most violent call: the process aborts itself without saving anything. Only for a server too wedged to answer a normal restart. If it runs under systemd it will come back on its own.")}
            </>
          }
          onClose={() => setConfirm(null)}
          onConfirm={() => api.crashServer().then(() => undefined)}
        />
      )}
    </section>
  );
}
