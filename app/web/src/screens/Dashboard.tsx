"use client";

import { useCallback, useEffect, useMemo, useState } from "react";
import { Empty, ErrorAlert, LoadingBlock, PageHead, usePoll } from "../components/bits";
import { Meter, Spark } from "../components/ResourceCards";
import { QuotaMeter, QuotaStatePill, useQuotaIndex } from "../components/QuotaCard";
import { RangeSeg, TrafficChart, useTrafficUsage } from "../components/TrafficChart";
import { api, type Quota, type ServerAnalytics, type Wire, type WireGuardState } from "../lib/api";
import { useI18n } from "../lib/i18n";
import { useServer } from "../lib/server";
import { Link, seg } from "../lib/router";
import { formatBytes, formatCount, formatDuration } from "../lib/util";
import { IconChevron, IconPlus, IconSettings } from "../ui/Icon";

import { CreateHubSheet, HubList } from "./Hubs";

/**
 * Operational Dashboard for SoftEther Manager.
 *
 * 1. Unified PageHead + conditional Risk Alert Banner (quotas / disk / offline hubs).
 * 2. 4-Column Stat KPI Cards: Hero live sessions anchor + users + RX/TX cumulative.
 * 3. Middle 12-Col Split (8 + 4): Dual-axis Throughput Telemetry & Sparkline Host Diagnostics + Disk.
 * 4. Insights 12-Col Split (6 + 6): Top Traffic Users ranking & Quota Health Watchlist.
 * 5. Bottom 12-Col Split (7 + 5): Virtual Hub Load Topology & Active Protocol Distribution.
 */
export function Dashboard() {
  const { probe, health, refresh, currentServer, clientCount, resources } = useServer();
  const { t } = useI18n();
  const [overview, setOverview] = useState<Wire | null>(null);
  const [wg, setWg] = useState<WireGuardState | null>(null);
  const [analytics, setAnalytics] = useState<ServerAnalytics | null>(null);
  const [panelSettings, setPanelSettings] = useState<Wire | null>(null);
  const [trafficHours, setTrafficHours] = useState(24);
  const [creatingHub, setCreatingHub] = useState(false);

  const quotas = useQuotaIndex([health]);

  useEffect(() => {
    void api.settings().then(setPanelSettings).catch(() => {});
  }, []);

  const loadOverview = useCallback(async () => {
    if (health === "online") setOverview(await api.overview().catch(() => null));
  }, [health]);
  usePoll(loadOverview, "detail", [health]);

  const loadWireGuard = useCallback(async () => {
    if (health === "online") setWg(await api.wireguard().catch(() => null));
  }, [health]);
  usePoll(loadWireGuard, "list", [health]);

  const loadAnalytics = useCallback(async () => {
    if (health === "online") setAnalytics(await api.serverAnalytics(trafficHours).catch(() => null));
  }, [health, trafficHours]);
  usePoll(loadAnalytics, "list", [health, trafficHours]);

  const serverUsage = useTrafficUsage(
    () => (health === "online" ? api.serverTraffic(trafficHours) : Promise.resolve(null)),
    trafficHours >= 168 ? "list" : "detail",
    [health, trafficHours],
  );

  const section = (key: string): Wire | null =>
    overview?.[key]?.ok ? (overview[key].data as Wire) : null;
  const status = section("status");
  const hubs = section("hubs")?.HubList as Wire[] | undefined;
  const listeners = section("listeners")?.ListenerList as Wire[] | undefined;
  const ipsec = section("ipsec");
  const openvpn = section("openvpn");
  const azure = section("azure");
  const ddns = section("ddns");

  const uptimeSeconds = useMemo(() => {
    if (!status?.StartTime_dt || !status?.CurrentTime_dt) return null;
    return (new Date(status.CurrentTime_dt).getTime() - new Date(status.StartTime_dt).getTime()) / 1000;
  }, [status]);

  const quotaList = useMemo(() => {
    const items: Quota[] = [];
    for (const q of quotas.values()) {
      if (q.has_limit || q.blocked) items.push(q);
    }
    items.sort((a, b) => {
      if (a.blocked !== b.blocked) return a.blocked ? -1 : 1;
      return b.percent - a.percent;
    });
    return items;
  }, [quotas]);

  const warnQuotas = useMemo(
    () => quotaList.filter((q) => q.blocked || (q.enabled && q.percent >= 85)),
    [quotaList],
  );

  const hubTotalsMap = useMemo(() => {
    const m = new Map<string, { total_bytes: number; send_bytes: number; recv_bytes: number }>();
    for (const h of analytics?.hub_totals ?? []) {
      m.set(h.hub, h);
    }
    return m;
  }, [analytics]);

  const protoCountMap = useMemo(() => {
    const m = new Map<string, number>();
    for (const entry of analytics?.protocol_counts ?? []) {
      m.set(entry.protocol.toLowerCase(), entry.count);
    }
    return m;
  }, [analytics]);

  const resHistory = (resources?.history as Wire[] | undefined) ?? [];
  const cpuHist = useMemo(() => resHistory.map((p) => Number(p.cpu) || 0), [resHistory]);
  const memHist = useMemo(() => resHistory.map((p) => Number(p.memory) || 0), [resHistory]);
  const rxHist = useMemo(() => resHistory.map((p) => Number(p.rx) || 0), [resHistory]);
  const txHist = useMemo(() => resHistory.map((p) => Number(p.tx) || 0), [resHistory]);
  const netSparkMax = Math.max(1024, ...rxHist, ...txHist);
  const disks = (resources?.disks as Wire[] | undefined) ?? [];
  const highDisks = disks.filter((d) => (Number(d.used_percent) || 0) >= 85);
  const offlineHubs = (hubs ?? []).filter((h) => !Boolean(h.Online_bool));

  if (health === "unconfigured") {
    return (
      <div className="page">
        <Empty
          title={t("not connected to SoftEther yet")}
          action={
            <Link to="/connect" className="btn-primary">
              {t("Connect the server")}
            </Link>
          }
        >
          {t("Tell the panel the management port and administrator password of the SoftEther instance on this machine, and everything else lights up.")}
        </Empty>
      </div>
    );
  }

  if (health === "probing") return <LoadingBlock label={t("Checking server connection")} />;
  if (health === "offline") {
    return (
      <div className="page">
        <h1>{t("Server Overview")} · {currentServer?.name}</h1>
        <ErrorAlert>
          <div className="alert__t">{t("The VPN server is not answering.")}</div>
          {probe?.error}
        </ErrorAlert>
        <div className="server-actions">
          <button className="btn" onClick={() => void refresh()}>{t("Retry")}</button>
          <Link className="btn" to="/connect">{t("Edit connection")}</Link>
          <Link className="btn" to="/servers">{t("Choose another server")}</Link>
        </div>
      </div>
    );
  }

  const activeSessions = Number(status?.NumSessionsTotal_u32) || 0;
  const totalUsers = Number(status?.NumUsers_u32) || 0;
  const rxTotal = (Number(status?.["Recv.UnicastBytes_u64"]) || 0) + (Number(status?.["Recv.BroadcastBytes_u64"]) || 0);
  const txTotal = (Number(status?.["Send.UnicastBytes_u64"]) || 0) + (Number(status?.["Send.BroadcastBytes_u64"]) || 0);
  const sessionsUrl = "/clients";

  const net = resources && resources.available !== false ? (resources.network as Wire | undefined) : undefined;
  const netRx = Number(net?.rx_bytes_per_second) || 0;
  const netTx = Number(net?.tx_bytes_per_second) || 0;

  const countProto = (...keys: string[]) => {
    let total = 0;
    for (const [k, v] of protoCountMap.entries()) {
      if (keys.some((needle) => k.includes(needle))) total += v;
    }
    return total;
  };

  const protocols = [
    {
      name: "SoftEther SSL-VPN",
      val: (listeners ?? []).filter((l) => l.Enables_bool).map((l) => l.Ports_u32).join(", ") || t("no listeners"),
      on: (listeners ?? []).some((l) => l.Enables_bool),
      live: countProto("ssl", "rudp", "udp", "softether"),
      color: "var(--chart-send)",
    },
    {
      name: "WireGuard",
      val: !wg ? "—" : !wg.supported ? t("Unsupported") : wg.enabled ? `UDP ${wg.default_udp_port ?? 51820}` : t("Off"),
      on: Boolean(wg?.supported && wg.enabled),
      live: countProto("wireguard"),
      color: "var(--chart-recv)",
    },
    {
      name: "OpenVPN",
      val: openvpn?.EnableOpenVPN_bool ? `UDP ${openvpn.OpenVPNPortList_str || "1194"}` : t("Off"),
      on: Boolean(openvpn?.EnableOpenVPN_bool),
      live: countProto("openvpn"),
      color: "var(--warn)",
    },
    {
      name: "L2TP / IPsec",
      val: ipsec ? (ipsec.L2TP_IPsec_bool ? String(ipsec.L2TP_DefaultHub_str || t("Enabled")) : t("Off")) : "—",
      on: Boolean(ipsec?.L2TP_IPsec_bool),
      live: countProto("l2tp", "ipsec"),
      color: "var(--busy)",
    },
    {
      name: "MS-SSTP",
      val: openvpn?.EnableSSTP_bool ? t("Enabled (TCP 443)") : t("Off"),
      on: Boolean(openvpn?.EnableSSTP_bool),
      live: countProto("sstp"),
      color: "var(--accent-strong)",
    },
    {
      name: "VPN Azure",
      val: azure?.IsEnabled_bool ? (ddns?.CurrentHostName_str ? `${ddns.CurrentHostName_str}.vpnazure.net` : t("Enabled")) : t("Off"),
      on: Boolean(azure?.IsEnabled_bool),
      live: 0,
      color: "var(--idle)",
    },
  ];
  const activeProtocolCount = protocols.filter((p) => p.on).length;
  const totalProtoLive = protocols.reduce((sum, p) => sum + p.live, 0);

  const sampleMin = Number(panelSettings?.sample_interval_minutes) || 5;
  const retentionDays = Number(panelSettings?.sample_retention_days) || 90;
  const sessionDays = Number(panelSettings?.session_history_retention_days) || 30;
  const resSec = Number(panelSettings?.resource_interval_seconds) || 3;
  const authLabel = currentServer?.auth ? String(currentServer.auth).toUpperCase() : "HEADER";

  const riskItems: { text: string; to?: string }[] = [];
  if (warnQuotas.length > 0) {
    riskItems.push({
      text: t("{count} traffic quotas near or over limit", { count: warnQuotas.length }),
      to: "/users",
    });
  }
  if (highDisks.length > 0) {
    const topDisk = highDisks[0];
    riskItems.push({
      text: t("Disk {mount} at {pct}%", {
        mount: String(topDisk.mount_point),
        pct: Math.round(Number(topDisk.used_percent) || 0),
      }),
    });
  }
  if (offlineHubs.length > 0) {
    riskItems.push({
      text: t("{count} hubs offline", { count: offlineHubs.length }),
      to: "/hubs",
    });
  }

  const topUsers = analytics?.top_users ?? [];
  const maxUserBytes = Math.max(1, ...topUsers.map((u) => u.total_bytes));

  return (
    <div className="page space-y-6">
      {/* ── 1. Unified Page Header ─────────────────────────────────────── */}
      <PageHead
        title={
          <span className="flex items-center gap-3 flex-wrap">
            <span>{t("Server Overview")} · {currentServer?.name || probe?.hostname || "vpnserver"}</span>
            <span className="live-badge">
              <span className="live-badge__dot" />
              {t("Running")}{uptimeSeconds ? ` · ${formatDuration(uptimeSeconds)}` : ""}
            </span>
          </span>
        }
        sub={
          <span className="dash-page-sub font-mono text-xs">
            {probe?.version || "SoftEther VPN"} · {currentServer ? `${currentServer.host}:${currentServer.port}` : "—"} · {t("{count} hubs on this server", { count: hubs?.length ?? 0 })}
          </span>
        }
        actions={
          <>
            <Link to="/server-settings" className="btn-secondary">
              <IconSettings size={14} /> {t("Server Config")}
            </Link>
            <button onClick={() => setCreatingHub(true)} className="btn-primary">
              <IconPlus size={14} /> {t("New Virtual HUB")}
            </button>
          </>
        }
      />

      {/* ── Conditional Risk Banner (zero visual noise when healthy) ─── */}
      {riskItems.length > 0 && (
        <div className="viz-alert" role="status">
          <div className="viz-alert__items">
            <span className="viz-alert__lead">{t("Attention needed")} ({riskItems.length})</span>
            {riskItems.map((item, idx) => (
              <span key={idx} className="viz-alert__item">
                {idx > 0 && <span className="viz-alert__sep">·</span>}
                {item.to ? (
                  <Link to={item.to} className="linkish">{item.text}</Link>
                ) : (
                  <span>{item.text}</span>
                )}
              </span>
            ))}
          </div>
          <Link to="/clients" className="viz-alert__go">{t("Inspect live clients →")}</Link>
        </div>
      )}

      {/* ── 2. Stat KPI Cards ─────────────────────────────────────────── */}
      <div className="grid grid-cols-2 lg:grid-cols-4 gap-4">
        <Link to={sessionsUrl} className="stat-card stat-card--hero stat-card--clickable">
          <div className="stat-card__l flex items-center justify-between">
            <span className="flex items-center gap-2">
              <span className="stat-card__dot" />
              <span>{t("Online Clients")}</span>
            </span>
            <IconChevron size={12} className="stat-card__chevron" />
          </div>
          <div className="stat-card__v">{formatCount(clientCount ?? activeSessions)}</div>
          <div className="stat-card__s">{t("Across all {count} hubs", { count: hubs?.length ?? 1 })}</div>
        </Link>

        <Link to="/users" className="stat-card stat-card--clickable">
          <div className="stat-card__l flex items-center justify-between flex-wrap gap-2">
            <span>{t("Configured users")}</span>
            {warnQuotas.length > 0 ? (
              <span className="viz-badge viz-badge--warn">{t("{count} quota alert", { count: warnQuotas.length })}</span>
            ) : (
              <IconChevron size={12} className="stat-card__chevron" />
            )}
          </div>
          <div className="stat-card__v">{formatCount(totalUsers)}</div>
          <div className="stat-card__s">{t("Local passwords / certificates")}</div>
        </Link>

        <div className="stat-card">
          <div className="stat-card__l flex items-center justify-between flex-wrap gap-2">
            <span>{t("Cumulative inbound (RX)")}</span>
            {net && <span className="stat-card__badge stat-card__badge--rx">↓ {formatBytes(netRx)}/s</span>}
          </div>
          <div className="stat-card__v">{formatBytes(rxTotal)}</div>
          <div className="stat-card__s">
            {serverUsage && serverUsage.total_recv > 0
              ? t("Window: {size}", { size: formatBytes(serverUsage.total_recv) })
              : uptimeSeconds
                ? t("Since {duration} ago", { duration: formatDuration(uptimeSeconds) })
                : t("since server start")}
          </div>
        </div>

        <div className="stat-card">
          <div className="stat-card__l flex items-center justify-between flex-wrap gap-2">
            <span>{t("Cumulative outbound (TX)")}</span>
            {net && <span className="stat-card__badge stat-card__badge--tx">↑ {formatBytes(netTx)}/s</span>}
          </div>
          <div className="stat-card__v">{formatBytes(txTotal)}</div>
          <div className="stat-card__s">
            {serverUsage && serverUsage.total_send > 0
              ? t("Window: {size}", { size: formatBytes(serverUsage.total_send) })
              : uptimeSeconds
                ? t("Since {duration} ago", { duration: formatDuration(uptimeSeconds) })
                : t("since server start")}
          </div>
        </div>
      </div>

      {/* ── 3. Middle 12-Col Band: Telemetry (8) + Host Diagnostics (4) ─ */}
      <div className="grid grid-cols-1 lg:grid-cols-12 gap-5 items-stretch">
        <div className="lg:col-span-8 panel-card flex flex-col">
          <div className="panel-card__h">
            <div>
              <h2 className="panel-card__t">{t("Throughput Telemetry")}</h2>
              <p className="panel-card__s">
                {t(
                  trafficHours >= 48
                    ? "Past {days} days · Recorded samples only; gaps are left blank."
                    : "Past {hours} hours · Recorded samples only; gaps are left blank.",
                  { days: trafficHours / 24, hours: trafficHours },
                )}
              </p>
            </div>
            <div className="flex items-center gap-3 text-xs font-mono">
              <RangeSeg hours={trafficHours} onChange={setTrafficHours} />
            </div>
          </div>
          <div className="dash-telemetry__body">
            {serverUsage ? (
              <TrafficChart
                usage={serverUsage}
                defaultMode="rate"
                height={260}
                fill
                emptyLabel={t("No server-wide traffic recorded in this time window yet.")}
              />
            ) : (
              <LoadingBlock label={t("Loading throughput telemetry")} />
            )}
          </div>
        </div>

        <div className="lg:col-span-4 panel-card panel-card--split dash-host-card">
          <div>
            <div className="panel-card__h">
              <div>
                <h2 className="panel-card__t">{t("Panel host resources")}</h2>
                <p className="panel-card__s">{t("Linux kernel rolling performance window")}</p>
              </div>
              {resources && resources.available !== false && (
                <span className="text-[11px] font-mono text-muted">{t("{n}s", { n: String(resSec) })}</span>
              )}
            </div>

            {resources && resources.available !== false ? (
              <div className="space-y-3">
                {/* CPU with Sparkline */}
                {(() => {
                  const cpuPct = Number(resources.cpu?.overall?.usage_percent) || 0;
                  const cpuTone = cpuPct >= 90 ? "err" : cpuPct >= 75 ? "warn" : "accent";
                  return (
                    <div className="p-3 rounded-md bg-raised border border-hairline space-y-1.5">
                      <div className="flex justify-between text-xs">
                        <span className="text-secondary font-medium">
                          {t("CPU load")} <span className="font-mono text-muted">· {resources.load?.one ?? "—"}</span>
                        </span>
                        <span className="font-mono font-semibold text-primary">{cpuPct.toFixed(1)}%</span>
                      </div>
                      {cpuHist.length >= 2 ? (
                        <Spark values={cpuHist} toneOf={cpuTone} max={100} />
                      ) : (
                        <Meter value={cpuPct} toneOf={cpuTone} />
                      )}
                    </div>
                  );
                })()}

                {/* RAM with Sparkline */}
                {(() => {
                  const mem = (resources.memory as Wire) ?? {};
                  const used = Number(mem.used_bytes) || 0;
                  const total = Number(mem.total_bytes) || 1;
                  const percent = Math.round((used / total) * 100);
                  const memTone = percent >= 92 ? "err" : percent >= 80 ? "warn" : "accent";
                  return (
                    <div className="p-3 rounded-md bg-raised border border-hairline space-y-1.5">
                      <div className="flex justify-between text-xs">
                        <span className="text-secondary font-medium">
                          {t("Memory")} <span className="font-mono text-muted">· {formatBytes(used)} / {formatBytes(total)}</span>
                        </span>
                        <span className="font-mono font-semibold text-primary">{percent}%</span>
                      </div>
                      {memHist.length >= 2 ? (
                        <Spark values={memHist} toneOf={memTone} max={100} />
                      ) : (
                        <Meter value={percent} toneOf={memTone} />
                      )}
                    </div>
                  );
                })()}

                {/* Adaptive Network Throughput Sparkline */}
                <div className="p-3 rounded-md bg-raised border border-hairline space-y-1.5">
                  <div className="flex justify-between text-xs">
                    <span className="text-secondary font-medium">{t("Network")}</span>
                    <span className="font-mono font-semibold" style={{ color: "var(--chart-recv)" }}>
                      {formatBytes(netRx + netTx)}/s
                    </span>
                  </div>
                  {(rxHist.length >= 2 || txHist.length >= 2) && (
                    <div style={{ display: "grid", gap: 2 }}>
                      <Spark values={rxHist} color="var(--chart-recv)" max={netSparkMax} />
                      <Spark values={txHist} color="var(--chart-send)" max={netSparkMax} />
                    </div>
                  )}
                  <div className="text-[11px] text-muted font-mono truncate">
                    ↓ {formatBytes(netRx)}/s · ↑ {formatBytes(netTx)}/s
                  </div>
                </div>

                {/* Primary Disk Mounts */}
                {disks.length > 0 && (
                  <div className="p-3 rounded-md bg-raised border border-hairline space-y-2">
                    {disks.slice(0, 2).map((disk) => {
                      const pct = Number(disk.used_percent) || 0;
                      const dTone = pct >= 92 ? "err" : pct >= 80 ? "warn" : "accent";
                      return (
                        <div key={String(disk.mount_point)} className="space-y-1">
                          <div className="flex justify-between text-xs gap-2">
                            <span className="font-mono text-secondary truncate">{t("Disk")} {String(disk.mount_point)}</span>
                            <span className="font-mono font-semibold text-primary shrink-0">
                              {formatBytes(Number(disk.used_bytes))} / {formatBytes(Number(disk.total_bytes))} · {Math.round(pct)}%
                            </span>
                          </div>
                          <Meter value={pct} toneOf={dTone} />
                        </div>
                      );
                    })}
                  </div>
                )}
              </div>
            ) : (
              <div className="space-y-2 text-xs font-mono text-secondary">
                <div className="flex justify-between py-2 border-b border-hairline">
                  <span className="text-muted">{t("Authentication protocol")}</span>
                  <span className="text-primary">{authLabel}</span>
                </div>
                <div className="flex justify-between py-2 border-b border-hairline">
                  <span className="text-muted">{t("Session retention policy")}</span>
                  <span className="text-primary">{t("{n} days", { n: String(sessionDays) })}</span>
                </div>
                <div className="flex justify-between py-2">
                  <span className="text-muted">{t("Traffic sampling period")}</span>
                  <span className="text-primary">{t("{n} min", { n: String(sampleMin) })}</span>
                </div>
              </div>
            )}
          </div>

          <div className="panel-card__foot">
            <span>{t("Sampling {min}m · Retention {days}d", { min: String(sampleMin), days: String(retentionDays) })}</span>
            <span>{authLabel}</span>
          </div>
        </div>
      </div>

      {/* ── 4. Insights Band: Top Traffic Users (6) + Quota Health (6) ── */}
      <div className="grid grid-cols-1 lg:grid-cols-12 gap-5 items-start">
        <div className="lg:col-span-6 panel-card">
          <div className="panel-card__h">
            <div>
              <h3 className="panel-card__t">{t("Top traffic users")}</h3>
              <p className="panel-card__s">
                {t(trafficHours >= 48 ? "Past {days} days" : "Past {hours} hours", {
                  days: trafficHours / 24,
                  hours: trafficHours,
                })}
              </p>
            </div>
            <Link to="/audit-report" className="btn-secondary">{t("Audit Report")}</Link>
          </div>

          {topUsers.length === 0 ? (
            <p className="micro" style={{ padding: "var(--s3) 0" }}>
              {t("No user traffic recorded in this time window yet.")}
            </p>
          ) : (
            <div className="viz-rank-list">
              {topUsers.slice(0, 6).map((u) => {
                const recvPct = Math.round((u.recv_bytes / maxUserBytes) * 100);
                const sendPct = Math.round((u.send_bytes / maxUserBytes) * 100);
                return (
                  <Link
                    key={`${u.hub}:${u.username}`}
                    to={`/hub/${seg(u.hub)}/user/${seg(u.username)}`}
                    className="viz-rank-item"
                  >
                    <div className="viz-rank-item__head">
                      <span className="viz-rank-item__who">
                        <strong className="mono">{u.username}</strong>
                        {u.real_name && <span className="text-muted">{u.real_name}</span>}
                        <span className="chip chip--sm mono">{u.hub}</span>
                      </span>
                      <span className="viz-rank-item__val mono">
                        <b>{formatBytes(u.total_bytes)}</b>
                        <small>↓{formatBytes(u.recv_bytes)} · ↑{formatBytes(u.send_bytes)}</small>
                      </span>
                    </div>
                    <div className="viz-bar">
                      <div className="viz-bar__seg" style={{ width: `${recvPct}%`, background: "var(--chart-recv)" }} />
                      <div className="viz-bar__seg" style={{ width: `${sendPct}%`, background: "var(--chart-send)" }} />
                    </div>
                  </Link>
                );
              })}
            </div>
          )}
        </div>

        <div className="lg:col-span-6 panel-card">
          <div className="panel-card__h">
            <div>
              <h3 className="panel-card__t">{t("Quota health")}</h3>
              <p className="panel-card__s">{t("Highest utilization first")}</p>
            </div>
            <Link to="/users" className="btn-secondary">{t("Manage users")}</Link>
          </div>

          {quotaList.length === 0 ? (
            <p className="micro" style={{ padding: "var(--s3) 0" }}>
              {t("No traffic limits configured on this server.")}
            </p>
          ) : (
            <div className="viz-quota-list">
              {quotaList.slice(0, 5).map((q) => {
                const dest =
                  q.subject === "hub"
                    ? `/hub/${seg(q.hub)}/settings`
                    : `/hub/${seg(q.hub)}/user/${seg(q.username)}`;
                return (
                  <Link key={`${q.subject}:${q.hub}:${q.username}`} to={dest} className="viz-quota-item">
                    <div className="viz-quota-item__head">
                      <span className="viz-rank-item__who">
                        <strong className="mono">{q.subject === "hub" ? `HUB: ${q.hub}` : q.username}</strong>
                        {q.subject === "user" && <span className="chip chip--sm mono">{q.hub}</span>}
                        <QuotaStatePill quota={q} />
                      </span>
                      <span className="mono micro">
                        {formatBytes(q.used_bytes)} / {formatBytes(q.limit_bytes)} · {Math.round(q.percent)}%
                      </span>
                    </div>
                    <QuotaMeter quota={q} />
                  </Link>
                );
              })}
            </div>
          )}
        </div>
      </div>

      {/* ── 5. Bottom 12-Col Band: Virtual Hubs (7) + Protocols (5) ───── */}
      <div className="grid grid-cols-1 lg:grid-cols-12 gap-5 items-start">
        <div className="lg:col-span-7 panel-card">
          <div className="panel-card__h">
            <div>
              <h3 className="panel-card__t">{t("Virtual Hub Topology")}</h3>
              <p className="panel-card__s">{t("Broadcast domain isolation and policies")}</p>
            </div>
            <div className="flex items-center gap-2">
              <Link to="/hubs" className="btn-secondary">{t("View all")}</Link>
              <button onClick={() => setCreatingHub(true)} className="btn-secondary">
                <IconPlus size={13} /> {t("Create hub")}
              </button>
            </div>
          </div>

          <HubList hubs={hubs} hubTotals={hubTotalsMap} quotas={quotas} />
        </div>

        <div className="lg:col-span-5 panel-card">
          <div className="panel-card__h">
            <div>
              <h3 className="panel-card__t">{t("Listening ports & protocols")}</h3>
              <p className="panel-card__s">{t("Access protocols")}</p>
            </div>
            <span className="font-mono text-xs text-emerald-500 font-semibold">
              {activeProtocolCount} / {protocols.length}
            </span>
          </div>

          {totalProtoLive > 0 && (
            <div className="viz-bar" style={{ marginBottom: "var(--s3)" }} aria-label={t("Active protocol distribution")}>
              {protocols
                .filter((p) => p.live > 0)
                .map((p) => (
                  <div
                    key={p.name}
                    className="viz-bar__seg"
                    style={{ width: `${Math.max(4, Math.round((p.live / totalProtoLive) * 100))}%`, background: p.color }}
                    title={`${p.name}: ${p.live}`}
                  />
                ))}
            </div>
          )}

          <div className="protocol-grid">
            {protocols.map((p) => (
              <div key={p.name} className="protocol-cell">
                <div className="protocol-cell__info">
                  <div className="protocol-cell__name">{p.name}</div>
                  <div className="protocol-cell__val" title={p.val}>{p.val}</div>
                </div>
                <div className="flex items-center gap-2 shrink-0">
                  {p.live > 0 && (
                    <span className="mono micro" style={{ color: p.color, fontWeight: 600 }}>
                      {t("{count} live", { count: p.live })}
                    </span>
                  )}
                  <span className={`protocol-cell__dot${p.on ? " protocol-cell__dot--on" : ""}`} />
                </div>
              </div>
            ))}
          </div>
        </div>
      </div>

      {creatingHub && (
        <CreateHubSheet
          onClose={() => setCreatingHub(false)}
          onCreated={() => {
            setCreatingHub(false);
            refresh();
          }}
        />
      )}
    </div>
  );
}
