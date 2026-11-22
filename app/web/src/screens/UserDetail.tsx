"use client";

import { useCallback, useEffect, useMemo, useRef, useState } from "react";
import {
  ConfirmSheet,
  Empty,
  KV,
  LoadingBlock,
  MoreRows,
  PageHead,
  SectionTitle,
  usePoll,
  useReveal,
} from "../components/bits";
import { PolicyEditor, extractPolicy } from "../components/PolicyEditor";
import { QuotaEnforceSwitch, QuotaSummary, netBytes } from "../components/QuotaCard";
import { RangeSeg, TrafficChart, useTrafficUsage } from "../components/TrafficChart";
import { UserSheet } from "../components/UserSheet";
import { VpnFileSheet, downloadOpenVpnConfig } from "../components/VpnFileSheet";
import {
  CopyTextButton,
  WireGuardAddPeerSheet,
  WireGuardConfigSheet,
  WireGuardQrSheet,
  waitForWireGuardReady,
} from "../components/WireGuardPeerSheet";
import { ApiError, api, useServerApi, type Quota, type Usage, type Wire, type WireGuardPeer, type WireGuardState } from "../lib/api";
import { Sheet } from "../ui/Sheet";
import { Spark } from "../components/ResourceCards";
import { Link, navigate, seg } from "../lib/router";
import { AUTH_TYPES, isNever, userBytes, SESSION_TYPE } from "../lib/se";
import { useToast } from "../lib/toast";
import { useI18n, t } from "../lib/i18n";
import { formatBytes, formatCount, formatDate, formatDuration, formatIPLocation, hasIPLocation, timeAgo } from "../lib/util";
import { IconOpenVpnDownload, IconPlus, IconQrCode, IconTrash, IconVpnDownload, IconWireGuardDownload } from "../ui/Icon";
import { Pill } from "../ui/Status";
import { UserStatePill } from "./hub/HubUsers";
import { EndpointCell, SessionSheet, SessionsTable } from "./hub/HubSessions";

/** Sessions with no client transport of their own -- SecureNAT and the like
 *  -- move bytes that SoftEther reports without a direction split. One line
 *  of what actually moved is the honest picture; two lines of zeroes are not. */
function CombinedSessionChart({ usage }: { usage: Wire }) {
  const points: Wire[] = Array.isArray(usage.combined) ? usage.combined : [];
  const values = points.map((p) => Number(p.total) || 0);
  if (!values.some((v) => v > 0)) {
    return (
      <p className="micro">
        {t("Nothing moved during this session, or it was shorter than one sampling interval.")}
      </p>
    );
  }
  return (
    <div style={{ display: "grid", gap: "var(--s2)" }}>
      <div className="chart__legend">
        <span className="chart__key">
          <i style={{ background: "var(--chart-recv)" }} /> {t("Traffic")}
          <b className="mono">{formatBytes(Number(usage.total_combined) || 0)}</b>
        </span>
      </div>
      <Spark values={values} color="var(--chart-recv)" />
      <p className="micro">
        {t("This session reports no split between download and upload, so this is the total it moved over its life, sampled every interval.")}
      </p>
    </div>
  );
}

function filterIpv4(ipStr?: any): string {
  if (!ipStr) return "";
  const parts = String(ipStr).split(/[\s,]+/);
  const v4 = parts.filter((p) => /^(\d{1,3}\.){3}\d{1,3}$/.test(p.trim()));
  return v4.join(", ");
}

export function cleanClientHost(host?: any, ip?: any): string {
  const h = String(host || "").trim();
  const cIp = String(ip || "").trim();
  if (!h || h === "—" || h === cIp || /^(\d{1,3}\.){3}\d{1,3}$/.test(h) || h.includes(":")) {
    return "—";
  }
  return h;
}

function localDayKey(d: Date): string {
  const y = d.getFullYear();
  const m = String(d.getMonth() + 1).padStart(2, "0");
  const day = String(d.getDate()).padStart(2, "0");
  return `${y}-${m}-${day}`;
}

function LoginHeatmap({
  history,
  selectedDay,
  onSelectDay,
}: {
  history: Wire[];
  selectedDay: string | null;
  onSelectDay: (day: string | null) => void;
}) {
  const { days, activeDays, totalLogins } = useMemo(() => {
    const counts = new Map<string, { sessions: number; bytes: number }>();
    for (const h of history) {
      if (!h.started_date) continue;
      const dt = new Date(String(h.started_date));
      if (Number.isNaN(dt.getTime())) continue;
      const key = localDayKey(dt);
      const cur = counts.get(key) || { sessions: 0, bytes: 0 };
      cur.sessions += 1;
      cur.bytes += Number(h.bytes_total) || 0;
      counts.set(key, cur);
    }

    const today = new Date();
    today.setHours(0, 0, 0, 0);
    const list: { key: string; label: string; sessions: number; bytes: number }[] = [];
    let active = 0;
    let total = 0;
    for (let i = 29; i >= 0; i--) {
      const d = new Date(today.getTime() - i * 86_400_000);
      const key = localDayKey(d);
      const stat = counts.get(key) || { sessions: 0, bytes: 0 };
      if (stat.sessions > 0) active += 1;
      total += stat.sessions;
      list.push({
        key,
        label: d.toLocaleDateString(undefined, { month: "numeric", day: "numeric" }),
        sessions: stat.sessions,
        bytes: stat.bytes,
      });
    }
    return { days: list, activeDays: active, totalLogins: total };
  }, [history]);

  const maxSessions = Math.max(1, ...days.map((d) => d.sessions));

  return (
    <div className="viz-heatmap">
      <div className="viz-heatmap__head">
        <span className="viz-heatmap__title">
          {t("30-day login activity")}
          <span className="micro mono">
            {t("{active}/30d active · {n} logins", { active: activeDays, n: totalLogins })}
          </span>
        </span>
        <div className="viz-heatmap__legend">
          {selectedDay && (
            <button
              type="button"
              className="filterhubs__pill on"
              onClick={() => onSelectDay(null)}
              title={t("Clear day filter")}
            >
              {selectedDay} ×
            </button>
          )}
          <span className="micro">{t("Less")}</span>
          {[0, 1, 2, 3, 4].map((lvl) => (
            <i key={lvl} className="viz-heatmap__swatch" data-lvl={lvl} aria-hidden="true" />
          ))}
          <span className="micro">{t("More")}</span>
        </div>
      </div>
      <div className="viz-heatmap__cells" role="group" aria-label={t("30-day login activity")}>
        {days.map((d) => {
          const lvl =
            d.sessions === 0
              ? 0
              : Math.min(4, Math.max(1, Math.ceil((d.sessions / maxSessions) * 4)));
          const isSelected = selectedDay === d.key;
          const tip =
            d.sessions > 0
              ? `${d.key}: ${t("{n} logins", { n: d.sessions })} · ${formatBytes(d.bytes)}`
              : `${d.key}: ${t("No logins")}`;
          return (
            <button
              key={d.key}
              type="button"
              className={`viz-heatmap__cell${isSelected ? " is-selected" : ""}`}
              data-lvl={lvl}
              title={tip}
              aria-label={tip}
              aria-pressed={isSelected}
              disabled={d.sessions === 0 && !isSelected}
              onClick={() => onSelectDay(isSelected ? null : d.key)}
            />
          );
        })}
      </div>
      <div className="viz-heatmap__axis micro mono">
        <span>{days[0]?.label}</span>
        <span>{days[14]?.label}</span>
        <span>{t("Today")}</span>
      </div>
    </div>
  );
}

/**
 * One recorded session, charted. The series behind it is the sampler's
 * per-session snapshots, so the resolution is the sampling interval -- a
 * session shorter than one tick has totals but no curve to draw, which the
 * chart says rather than drawing a flat line that means nothing.
 */
export function SessionUsageSheet({ hub, row, onClose }: { hub: string; row: Wire; onClose: () => void }) {
  const [usage, setUsage] = useState<(Usage & { session: Wire } & Wire) | null>(null);
  const [error, setError] = useState<string | null>(null);
  useEffect(() => {
    void api
      .sessionUsage(hub, Number(row.id))
      .then(setUsage)
      .catch((e) => setError(e instanceof Error ? e.message : String(e)));
  }, [hub, row.id]);

  const started = new Date(String(row.started_date)).getTime();
  const endedAt = row.ended_date ? new Date(String(row.ended_date)).getTime() : null;
  const duration = endedAt ? (endedAt - started) / 1000 : (Date.now() - started) / 1000;
  const proto = String(row.protocol || usage?.session?.protocol || "");
  const clientIp = String(row.client_ip || usage?.session?.client_ip || "");
  const clientPort = Number(row.client_port || usage?.session?.client_port || 0);
  const serverPort = Number(row.server_port || usage?.session?.server_port || 0);
  const disconnectReason = String(row.disconnect_reason || usage?.session?.disconnect_reason || "");
  const downBytes = Number(row.download_bytes ?? usage?.session?.download_bytes ?? 0);
  const upBytes = Number(row.upload_bytes ?? usage?.session?.upload_bytes ?? 0);
  const splitSum = downBytes + upBytes;
  const downPct = splitSum > 0 ? Math.round((downBytes / splitSum) * 100) : 0;

  return (
    <Sheet
      title={t("Session")}
      subtitle={`${row.username}${proto ? ` · ${proto}` : ""} · ${formatDate(String(row.started_date))}`}
      onClose={onClose}
      wide
      footer={<button className="btn" onClick={onClose}>{t("Close")}</button>}
    >
      <div className="session-panel">
        <div className="session-grid-2">
          {/* Left Card: Network & Host */}
          <div className="session-card">
            <div className="session-card__head">
              <span className="session-card__dot" style={{ background: "var(--accent-strong, #3b82f6)" }} />
              {t("Network & Host")}
            </div>
            <div style={{ display: "grid", gridTemplateColumns: "repeat(2, minmax(0, 1fr))", gap: "var(--s3)" }}>
              <div>
                <div className="micro">{t("client IP")}</div>
                <div className="mono" style={{ fontSize: "var(--t-data)" }}>
                  {clientIp ? (clientPort > 0 ? `${clientIp}:${clientPort}` : clientIp) : "—"}
                </div>
              </div>
              <div>
                <div className="micro">{t("Location & ISP")}</div>
                <div className="mono" style={{ fontSize: "var(--t-data)", color: "var(--accent)" }}>
                  {formatIPLocation(row.ip_location || usage?.session?.ip_location, row.ip_isp || usage?.session?.ip_isp) || "—"}
                </div>
              </div>
              <div>
                <div className="micro">{t("client host")}</div>
                <div className="mono" style={{ fontSize: "var(--t-data)" }}>
                  {cleanClientHost(row.client_hostname || usage?.session?.client_hostname, clientIp)}
                </div>
              </div>
              <div>
                <div className="micro">{t("virtual IP")}</div>
                <div className="mono" style={{ fontSize: "var(--t-data)", color: "var(--ok)" }}>
                  {(row.virtual_ip || usage?.session?.virtual_ip)
                    ? filterIpv4(row.virtual_ip || usage?.session?.virtual_ip) || "—"
                    : "—"}
                </div>
              </div>
              <div>
                <div className="micro">{t("Protocol")}</div>
                <div className="mono" style={{ fontSize: "var(--t-data)" }}>
                  {proto || "—"}{serverPort > 0 ? ` · :${serverPort}` : ""}
                </div>
              </div>
              <div>
                <div className="micro">{t("duration")}</div>
                <div className="mono" style={{ fontSize: "var(--t-data)" }}>{formatDuration(duration)}</div>
              </div>
            </div>
          </div>

          {/* Right Card: Time & Traffic */}
          <div className="session-card">
            <div className="session-card__head">
              <span className="session-card__dot" style={{ background: "var(--ok, #10b981)" }} />
              {t("Time & Traffic")}
            </div>
            <div style={{ display: "grid", gridTemplateColumns: "repeat(2, minmax(0, 1fr))", gap: "var(--s3)" }}>
              <div>
                <div className="micro">{t("started")}</div>
                <div className="mono" style={{ fontSize: "var(--t-data)" }}>{formatDate(String(row.started_date))}</div>
              </div>
              <div>
                <div className="micro">{t("ended")}</div>
                <div className="mono" style={{ fontSize: "var(--t-data)" }}>{row.ended_date ? formatDate(String(row.ended_date)) : t("still connected")}</div>
              </div>
              <div>
                <div className="micro" style={{ color: "var(--ok)" }}>{t("downloaded")}</div>
                <div className="mono" style={{ fontSize: "var(--t-data)", fontWeight: 700, color: "var(--ok)" }}>
                  {formatBytes(downBytes)} {splitSum > 0 ? `(${downPct}%)` : ""}
                </div>
              </div>
              <div>
                <div className="micro" style={{ color: "var(--accent-strong, #3b82f6)" }}>{t("uploaded")}</div>
                <div className="mono" style={{ fontSize: "var(--t-data)", fontWeight: 700, color: "var(--accent-strong, #3b82f6)" }}>
                  {formatBytes(upBytes)} {splitSum > 0 ? `(${100 - downPct}%)` : ""}
                </div>
              </div>
              {splitSum > 0 && (
                <div style={{ gridColumn: "1 / -1" }}>
                  <div className="viz-bar">
                    <div className="viz-bar__seg" style={{ width: `${downPct}%`, background: "var(--chart-recv)" }} />
                    <div className="viz-bar__seg" style={{ width: `${100 - downPct}%`, background: "var(--chart-send)" }} />
                  </div>
                </div>
              )}
              {disconnectReason && (
                <div style={{ gridColumn: "1 / -1" }}>
                  <div className="micro">{t("Disconnect reason")}</div>
                  <div className="mono" style={{ fontSize: "var(--t-data)", color: !disconnectReason.toLowerCase().includes("no error") && !disconnectReason.toLowerCase().includes("code 0") ? "var(--warn)" : "var(--text-2)" }}>
                    {disconnectReason}
                  </div>
                </div>
              )}
            </div>
          </div>
        </div>

        {error && <div className="alert alert--err">{error}</div>}
        {!usage && !error ? (
          <LoadingBlock label="loading this session's traffic" />
        ) : usage ? (
          <div className="session-card" style={{ padding: "var(--s4)" }}>
            {usage.split_available ? (
              <TrafficChart
                usage={usage}
                emptyLabel="This session was shorter than one sampling interval, so there is no curve to draw — the totals above are what it moved."
              />
            ) : (
              <CombinedSessionChart usage={usage} />
            )}
          </div>
        ) : null}
      </div>
    </Sheet>
  );
}

/**
 * One user, whole: identity, policy, usage, live sessions.
 *
 * The identity block renders as a card -- the thing you'd hand the user if
 * VPN accounts were physical. Everything on it is real data; nothing is
 * decoration.
 */
export function UserDetail({ hub, name }: { hub: string; name: string }) {
  const serverApi = useServerApi();
  const { t } = useI18n();
  const [user, setUser] = useState<Wire | null>(null);
  const [sessions, setSessions] = useState<Wire[] | null>(null);
  const [history, setHistory] = useState<Wire[] | null>(null);
  const [historyExhausted, setHistoryExhausted] = useState(false);
  const [quota, setQuota] = useState<Quota | null>(null);
  const [wg, setWg] = useState<WireGuardState | null>(null);
  const [addingWg, setAddingWg] = useState(false);
  const [downloadingOvpn, setDownloadingOvpn] = useState(false);
  const [editingWg, setEditingWg] = useState<WireGuardPeer | null>(null);
  const [qrWg, setQrWg] = useState<WireGuardPeer | null>(null);
  const [deletingWg, setDeletingWg] = useState<WireGuardPeer | null>(null);
  const [hours, setHours] = useState(24);
  const [editing, setEditing] = useState(false);
  const [deleting, setDeleting] = useState(false);
  const [downloading, setDownloading] = useState(false);
  const [policy, setPolicy] = useState<Wire | null>(null);
  const [policyDirty, setPolicyDirty] = useState(false);
  const initialPolicy = useRef<Wire | null>(null);
  const [savingPolicy, setSavingPolicy] = useState(false);
  const [openSession, setOpenSession] = useState<string | null>(null);
  const [openHistory, setOpenHistory] = useState<Wire | null>(null);
  const [selectedDay, setSelectedDay] = useState<string | null>(null);
  const { guard, push } = useToast();

  const load = useCallback(async () => {
    const u = await api.user(hub, name).catch(() => null);
    if (u) {
      setUser(u);
      setPolicy((current) => (policyDirty ? current : extractPolicy(u)));
    }
    const s = await api.userSessions(hub, name).catch(() => null);
    if (s) setSessions((s.SessionList as Wire[]) ?? []);
    // The limit fills as the sampler ticks, so it re-reads with the rest of
    // the page. Nothing here (a 404 for "no limit on this config" included) is
    // worth an error: the section simply does not render.
    setQuota(await api.userQuota(hub, name).catch(() => null));
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [hub, name, policyDirty]);
  usePoll(load, "detail", [hub, name]);

  const loadWg = useCallback(async () => {
    setWg(await serverApi.wireguard().catch(() => null));
  }, [serverApi]);
  usePoll(loadWg, "list", [hub, name]);

  const usage = useTrafficUsage(() => api.userUsage(hub, name, hours), "list", [hub, name, hours]);

  // A busy account accumulates hundreds of logins; the newest few answer the
  // usual question, and the rest are one button away.
  const { visible: historyVisible, reveal: revealHistory, reset: resetHistory } = useReveal(5);

  const loadHistory = useCallback(async (beforeId = 0) => {
    const batch = await api.userSessionHistory(hub, name, beforeId).catch(() => null);
    if (!batch) return;
    if (beforeId) {
      setHistory((current) => [...(current ?? []), ...batch]);
      if (batch.length < 100) setHistoryExhausted(true);
      return;
    }
    setHistory((current) => {
      if (!current || current.length <= 100 || batch.length === 0) return batch;
      const oldestNewId = Number(batch[batch.length - 1].id);
      return [...batch, ...current.filter((item) => Number(item.id) < oldestNewId)];
    });
    if (batch.length < 100) setHistoryExhausted(true);
  }, [hub, name]);
  useEffect(() => {
    setHistory(null);
    setHistoryExhausted(false);
    setSelectedDay(null);
    resetHistory();
  }, [hub, name, resetHistory]);
  usePoll(() => loadHistory(0), "list", [hub, name]);
  const filteredHistory = useMemo(() => {
    if (!history) return [];
    if (!selectedDay) return history;
    return history.filter((h) => {
      if (!h.started_date) return false;
      const dt = new Date(String(h.started_date));
      return !Number.isNaN(dt.getTime()) && localDayKey(dt) === selectedDay;
    });
  }, [history, selectedDay]);
  const shownHistory = selectedDay ? filteredHistory : filteredHistory.slice(0, historyVisible);

  const savePolicy = async () => {
    if (!user || !policy || !initialPolicy.current) return;
    setSavingPolicy(true);
    const baseline = initialPolicy.current;
    const body = Object.fromEntries(Object.entries(policy).filter(([key, value]) => value !== baseline[key]));
    const ok = Object.keys(body).length === 0 || await guard(() => api.setUser(hub, name, body), "Policy saved.");
    setSavingPolicy(false);
    if (ok) {
      setPolicyDirty(false);
      void load();
    }
  };

  if (!user) {
    return (
      <div className="page">
        <LoadingBlock label={t("loading {name}", { name })} />
      </div>
    );
  }

  // What the panel calls this config's Transfer: SoftEther's lifetime counter
  // less whatever a reset put behind us.
  const bytes = netBytes(userBytes(user), quota);

  return (
    <div className="page user-detail">
      <PageHead
        title={<span className="mono">{name}</span>}
        sub={
          <>
            <Link to={`/hub/${seg(hub)}/users`} className="linkish">← {t("Users")}</Link>
            {" "}· {t("Hub")} <span className="mono">{hub}</span>
          </>
        }
        actions={
          <>
            <button
              className="btn"
              disabled={downloadingOvpn}
              onClick={() => void (async () => {
                setDownloadingOvpn(true);
                await guard(() => downloadOpenVpnConfig(serverApi), t("OpenVPN configuration (.ovpn) downloaded."));
                setDownloadingOvpn(false);
              })()}
            >
              {downloadingOvpn ? <span className="spin" /> : <IconOpenVpnDownload size={15} />} {t("Download .ovpn")}
            </button>
            <button className="btn btn--primary" onClick={() => setDownloading(true)}>
              <IconVpnDownload size={15} /> {t("Download .vpn")}
            </button>
            <button className="btn" onClick={() => setEditing(true)}>{t("Edit profile")}</button>
            <button className="btn btn--danger" onClick={() => setDeleting(true)}>
              <IconTrash size={14} /> {t("Delete")}
            </button>
          </>
        }
      />

      <div className="usergrid">
        {/* the identity card */}
        <div className={`idcard${(sessions?.length ?? 0) > 0 ? " idcard--online" : ""}`}>
          <div>
            <div className="idcard__head">
              <span className="idcard__ava">{name.slice(0, 2).toUpperCase()}</span>
              <span className="idcard__hub mono">{hub}</span>
              <UserStatePill user={user} online={(sessions?.length ?? 0) > 0} />
            </div>
            <div className="idcard__name mono">{name}</div>
            {user.Realname_utf ? <div className="idcard__real">{String(user.Realname_utf)}</div> : null}
          </div>
          <div className="idcard__meta">
            <span className="chip"><i>{t("auth")}</i>{AUTH_TYPES[Number(user.AuthType_u32)] ? t(AUTH_TYPES[Number(user.AuthType_u32)]) : "?"}</span>
            <span className="chip"><i>{t("group")}</i>{String(user.GroupName_str || "—")}</span>
            <span className="chip"><i>{t("Expires")}</i>{isNever(user.ExpireTime_dt as string) ? t("never") : formatDate(user.ExpireTime_dt as string)}</span>
          </div>
        </div>

        <div className="card user-summary">
          <div className="user-summary__stats">
            <div className="stat"><span className="stat__n">{formatCount(Number(user.NumLogin_u32))}</span><span className="micro">{t("logins")}</span></div>
            <div className="stat"><span className="stat__n">{formatBytes(bytes.recv)}</span><span className="micro">{t("downloaded")}</span></div>
            <div className="stat"><span className="stat__n">{formatBytes(bytes.send)}</span><span className="micro">{t("uploaded")}</span></div>
            <div className="stat"><span className="stat__n">{sessions?.length ?? 0}</span><span className="micro">{t("online now")}</span></div>
          </div>
          <div className="user-summary__details">
            <KV
              rows={[
                [t("Created"), formatDate(user.CreatedTime_dt as string)],
                [t("Updated"), formatDate(user.UpdatedTime_dt as string)],
                [t("Note"), String(user.Note_utf || "—")],
                ...(Number(user.AuthType_u32) === 3 && user.CommonName_utf ? [[t("required CN"), String(user.CommonName_utf)] as [string, string]] : []),
                ...(Number(user.AuthType_u32) === 4 ? [[t("RADIUS user"), String(user.RadiusUsername_utf || name)] as [string, string]] : []),
                ...(Number(user.AuthType_u32) === 5 ? [[t("NT user"), String(user.NtUsername_utf || name)] as [string, string]] : []),
              ]}
            />
          </div>
        </div>
      </div>

      {quota && (quota.has_limit || quota.blocked) && (
        <>
          <SectionTitle
            actions={
              <button className="btn btn--sm" onClick={() => setEditing(true)}>{t("Change")}</button>
            }
          >
            {t("Traffic limit")}
          </SectionTitle>
          <div className="card" style={{ padding: "var(--s4)" }}>
            {quota.has_limit && <QuotaEnforceSwitch quota={quota} subject="user" onChanged={setQuota} />}
            <QuotaSummary quota={quota} subject="user" net={bytes} />
          </div>
        </>
      )}

      {wg?.supported ? (() => {
        const userPeers = (wg.peers ?? []).filter(
          (p) => p.hub === hub && p.user.toLowerCase() === name.toLowerCase(),
        );
        return (
          <div className="user-midgrid">
            <div>
              <SectionTitle
                count={userPeers.length}
                actions={
                  <button type="button" className="btn btn--sm" onClick={() => setAddingWg(true)}>
                    <IconPlus size={14} /> {t("Add WireGuard peer")}
                  </button>
                }
              >
                {t("WireGuard")}
              </SectionTitle>
              {userPeers.length === 0 ? (
                <Empty title={t("no WireGuard peers")}>
                  {t("Bind a WireGuard public key to let this user connect with a standard WireGuard client.")}
                </Empty>
              ) : (
                <div className="card tcard">
                  <div className="tscroll">
                    <table className="dtable">
                      <thead>
                        <tr>
                          <th scope="col">{t("Device")}</th>
                          <th scope="col">{t("Tunnel IPv4")}</th>
                          <th scope="col">{t("Client public key")}</th>
                          <th scope="col" className="tact" aria-label={t("Actions")} />
                        </tr>
                      </thead>
                      <tbody>
                        {userPeers.map((peer) => (
                          <tr key={peer.public_key}>
                            <td className="tname mono">{peer.label || t("WireGuard peer")}</td>
                            <td>
                              {peer.assigned_ip ? (
                                <span className="chip chip--ok mono">{peer.assigned_ip}/32</span>
                              ) : (
                                <span className="micro">—</span>
                              )}
                            </td>
                            <td>
                              <div style={{ display: "inline-flex", alignItems: "center", gap: "var(--s2)", whiteSpace: "nowrap" }}>
                                <span className="mono" title={peer.public_key}>{peer.public_key.slice(0, 12)}…</span>
                                <CopyTextButton text={peer.public_key} ariaLabel="Copy client public key" />
                              </div>
                            </td>
                            <td className="tact">
                              <div style={{ display: "inline-flex", alignItems: "center", gap: "var(--s1)", whiteSpace: "nowrap" }}>
                                <button
                                  type="button"
                                  className="btn btn--sm"
                                  onClick={() => setQrWg(peer)}
                                  title={t("WireGuard configuration QR code")}
                                  aria-label={t("WireGuard configuration QR code")}
                                >
                                  <IconQrCode size={14} /> {t("QR code")}
                                </button>
                                <button type="button" className="btn btn--sm" onClick={() => setEditingWg(peer)}>
                                  <IconWireGuardDownload size={14} /> {t(".conf")}
                                </button>
                                <button
                                  type="button"
                                  className="btn btn--sm btn--ghost"
                                  aria-label={t("Delete peer")}
                                  onClick={() => setDeletingWg(peer)}
                                >
                                  <IconTrash size={14} />
                                </button>
                              </div>
                            </td>
                          </tr>
                        ))}
                      </tbody>
                    </table>
                  </div>
                </div>
              )}
            </div>

            <div>
              <SectionTitle actions={<RangeSeg hours={hours} onChange={setHours} />}>{t("Usage")}</SectionTitle>
              <div className="card" style={{ padding: "var(--s4)" }}>
                {usage ? (
                  <TrafficChart
                    usage={usage}
                    emptyLabel="No samples in this window — usage builds up as the panel's sampler runs."
                  />
                ) : (
                  <LoadingBlock label="loading usage" />
                )}
              </div>
            </div>
          </div>
        );
      })() : (
        <>
          <SectionTitle actions={<RangeSeg hours={hours} onChange={setHours} />}>{t("Usage")}</SectionTitle>
          <div className="card" style={{ padding: "var(--s4)" }}>
            {usage ? (
              <TrafficChart
                usage={usage}
                emptyLabel="No samples in this window — usage builds up as the panel's sampler runs."
              />
            ) : (
              <LoadingBlock label="loading usage" />
            )}
          </div>
        </>
      )}

      <SectionTitle count={sessions?.length}>{t("Live sessions")}</SectionTitle>
      {sessions === null ? (
        <LoadingBlock />
      ) : sessions.length === 0 ? (
        <Empty title={t("not connected right now")}>
          {t("Sessions appear here the moment this user connects, and can be cut from here too.")}
        </Empty>
      ) : (
        <SessionsTable
          sessions={sessions}
          onOpen={(s) => setOpenSession(String(s.Name_str))}
          onKill={(s) =>
            guard(async () => {
              await api.killSession(hub, String(s.Name_str));
              await load();
            }, "Session disconnected.")
          }
        />
      )}

      <SectionTitle count={selectedDay ? filteredHistory.length : history?.length}>{t("Session history")}</SectionTitle>
      {history === null ? (
        <LoadingBlock />
      ) : history.length === 0 ? (
        <Empty title={t("no logins recorded yet")}>
          {t("Every connection this user makes lands here — client IP, when it started and ended, and the bytes it moved — kept for the retention window set in Settings.")}
        </Empty>
      ) : (
        <div className="card tcard">
          <LoginHeatmap history={history} selectedDay={selectedDay} onSelectDay={setSelectedDay} />
          <div className="tscroll" tabIndex={0} role="region" aria-label={t("Session history")}>
            <table className="dtable">
              <thead>
                <tr>
                  <th>{t("Started")}</th>
                  <th>{t("Ended")}</th>
                  <th>{t("Duration")}</th>
                  <th>{t("IP / Host")}</th>
                  <th>{t("Transferred")}</th>
                </tr>
              </thead>
              <tbody>
                {shownHistory.map((h) => {
                  const started = new Date(String(h.started_date)).getTime();
                  const endedAt = h.ended_date ? new Date(String(h.ended_date)).getTime() : null;
                  const duration = endedAt ? (endedAt - started) / 1000 : (Date.now() - started) / 1000;
                  const v4 = filterIpv4(h.virtual_ip);
                  const host = cleanClientHost(h.client_hostname, h.client_ip);
                  const proto = String(h.protocol || "").trim();
                  const reason = String(h.disconnect_reason || "").trim();
                  const abnormal = Boolean(reason && !/normal|client|closed by user|graceful/i.test(reason));
                  return (
                    <tr key={String(h.id)} className="clickable" onClick={() => setOpenHistory(h)}>
                      <td className="tmono">
                        <div>{formatDate(String(h.started_date))}</div>
                        {proto && <div className="micro">{proto}</div>}
                      </td>
                      <td className="tmono">
                        <div>
                          {h.ended_date ? formatDate(String(h.ended_date)) : <Pill kind="ok" label="connected" />}
                        </div>
                        {reason && (
                          <div className={abnormal ? "micro viz-text-warn" : "micro"} title={reason}>
                            {reason}
                          </div>
                        )}
                      </td>
                      <td className="tmono">{formatDuration(duration)}</td>
                      <td className="tmono">
                        <EndpointCell
                          virtualIp={v4}
                          clientIp={h.client_ip}
                          location={hasIPLocation(h.ip_location, h.ip_isp) ? formatIPLocation(h.ip_location, h.ip_isp) : ""}
                          host={host}
                        />
                      </td>
                      <td className="tmono" title={`↓ ${formatBytes(Number(h.download_bytes))} · ↑ ${formatBytes(Number(h.upload_bytes))}`}>
                        {formatBytes(Number(h.bytes_total))}
                      </td>
                    </tr>
                  );
                })}
              </tbody>
            </table>
          </div>
          {!selectedDay && (
            <MoreRows
              shown={shownHistory.length}
              loaded={history.length}
              exhausted={historyExhausted}
              noun="logins"
              onReveal={revealHistory}
              onLoad={() => loadHistory(Number(history[history.length - 1].id))}
            />
          )}
        </div>
      )}

      <SectionTitle>{t("Security policy")}</SectionTitle>
      <div className="card" style={{ padding: "var(--s4)", maxWidth: 720 }}>
        {policy && (
          <>
            <PolicyEditor
              value={policy}
              subject="user"
              onChange={(next) => {
                if (!policyDirty) initialPolicy.current = policy;
                setPolicy(next);
                setPolicyDirty(true);
              }}
            />
            {policyDirty && (
              <div style={{ display: "flex", gap: "var(--s2)", marginTop: "var(--s3)" }}>
                <button className="btn btn--primary" onClick={savePolicy} disabled={savingPolicy}>
                  {savingPolicy && <span className="spin" />} {t("Save policy")}
                </button>
                <button
                  className="btn"
                  onClick={() => {
                    setPolicy(extractPolicy(user));
                    setPolicyDirty(false);
                  }}
                >
                  {t("Discard")}
                </button>
              </div>
            )}
          </>
        )}
      </div>

      {downloading && (
        <VpnFileSheet hub={hub} name={name} onClose={() => setDownloading(false)} />
      )}
      {editing && (
        <UserSheet
         
          hub={hub}
          existing={user}
          onClose={() => setEditing(false)}
          onSaved={() => {
            setEditing(false);
            void load();
          }}
        />
      )}
      {deleting && (
        <ConfirmSheet
          title={t("Delete {name}?", { name })}
          verb="Delete user"
          typed={name}
          body={
            <>
              {t("The user is removed from hub {hub} and any live session it has is cut. This cannot be undone.", { hub })}
            </>
          }
          onClose={() => setDeleting(false)}
          onConfirm={async () => {
            await api.deleteUser(hub, name);
            push("ok", t("{name} deleted.", { name }));
            navigate(`/hub/${seg(hub)}/users`);
          }}
        />
      )}
      {openHistory && (
        <SessionUsageSheet hub={hub} row={openHistory} onClose={() => setOpenHistory(null)} />
      )}
      {openSession && (
        <SessionSheet
         
          hub={hub}
          name={openSession}
          onClose={() => setOpenSession(null)}
        />
      )}
      {addingWg && wg && (
        <WireGuardAddPeerSheet
          state={wg}
          fixedHub={hub}
          fixedUser={name}
          onClose={() => setAddingWg(false)}
          onChanged={(fresh) => {
            if (fresh) setWg(fresh);
            else void loadWg();
          }}
        />
      )}
      {editingWg && wg && (
        <WireGuardConfigSheet
          state={wg}
          peer={editingWg}
          onClose={() => setEditingWg(null)}
          onMetaSaved={(fresh) => {
            if (fresh) setWg(fresh);
            else void loadWg();
          }}
        />
      )}
      {qrWg && wg && (
        <WireGuardQrSheet
          state={wg}
          peer={qrWg}
          onClose={() => setQrWg(null)}
          onPeerUpdated={(fresh) => {
            if (fresh) setWg(fresh);
            else void loadWg();
          }}
        />
      )}
      {deletingWg && (
        <ConfirmSheet
          title={t("Remove WireGuard peer and restart?")}
          verb="Apply & restart"
          body={t("This writes vpn_server.config via SetConfig and restarts the SoftEther service, which disconnects all active VPN sessions.")}
          onClose={() => setDeletingWg(null)}
          onConfirm={async () => {
            try {
              await serverApi.deleteWireguardPeer(hub, {
                public_key: deletingWg.public_key,
                confirm_restart: true,
              });
            } catch (error) {
              if (!(error instanceof ApiError) || error.seError !== "TransportError") throw error;
            }
            const fresh = await waitForWireGuardReady(serverApi, (s) =>
              !s.peers?.some((p) => p.public_key === deletingWg.public_key),
            );
            setWg(fresh);
            push("ok", t("WireGuard peer removed."));
          }}
        />
      )}
    </div>
  );
}
