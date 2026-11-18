"use client";

import { useCallback, useMemo, useState } from "react";
import { Empty, ErrorAlert, LoadingBlock, PageHead, usePoll } from "../components/bits";
import { RangeSeg, TrafficChart, useTrafficUsage } from "../components/TrafficChart";
import { api, type Wire, type WireGuardState } from "../lib/api";
import { useI18n } from "../lib/i18n";
import { useServer } from "../lib/server";
import { Link } from "../lib/router";
import { formatBytes, formatCount, formatDuration } from "../lib/util";
import { IconChevron, IconPlus, IconSettings } from "../ui/Icon";

import { CreateHubSheet, HubList } from "./Hubs";

/**
 * Operational Dashboard for SoftEther Manager.
 *
 * 1. Unified PageHead: Server title, live running badge, endpoint metadata, primary actions.
 * 2. 4-Column Stat Cards: Hero live sessions anchor + users + RX/TX cumulative with live rate badges.
 * 3. Middle 12-Col Split (8 + 4): Throughput Telemetry chart & Host Diagnostics + Runtime strip.
 * 4. Bottom 12-Col Split (7 + 5): Virtual Hub Topology & Listening Ports / Protocols matrix.
 */
export function Dashboard() {
  const { probe, health, refresh, currentServer, clientCount, resources } = useServer();
  const { t } = useI18n();
  const [overview, setOverview] = useState<Wire | null>(null);
  const [wg, setWg] = useState<WireGuardState | null>(null);
  const [trafficHours, setTrafficHours] = useState(24);
  const [creatingHub, setCreatingHub] = useState(false);

  const loadOverview = useCallback(async () => {
    if (health === "online") setOverview(await api.overview().catch(() => null));
  }, [health]);
  usePoll(loadOverview, "detail", [health]);

  const loadWireGuard = useCallback(async () => {
    if (health === "online") setWg(await api.wireguard().catch(() => null));
  }, [health]);
  usePoll(loadWireGuard, "list", [health]);

  const serverUsage = useTrafficUsage(
    () => health === "online" ? api.serverTraffic(trafficHours) : Promise.resolve(null),
    "detail", [health, trafficHours],
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
  if (health === "offline") return <div className="page"><h1>{t("Server overview")} · {currentServer?.name}</h1><ErrorAlert><div className="alert__t">{t("The VPN server is not answering.")}</div>{probe?.error}</ErrorAlert><div className="server-actions"><button className="btn" onClick={() => void refresh()}>{t("Retry")}</button><Link className="btn" to="/connect">{t("Edit connection")}</Link><Link className="btn" to="/servers">{t("Choose another server")}</Link></div></div>;

  const activeSessions = Number(status?.NumSessionsTotal_u32) || 0;
  const totalUsers = Number(status?.NumUsers_u32) || 0;
  const rxTotal = (Number(status?.["Recv.UnicastBytes_u64"]) || 0) + (Number(status?.["Recv.BroadcastBytes_u64"]) || 0);
  const txTotal = (Number(status?.["Send.UnicastBytes_u64"]) || 0) + (Number(status?.["Send.BroadcastBytes_u64"]) || 0);
  const sessionsUrl = "/clients";

  const net = resources && resources.available !== false ? (resources.network as Wire | undefined) : undefined;
  const netRx = Number(net?.rx_bytes_per_second) || 0;
  const netTx = Number(net?.tx_bytes_per_second) || 0;

  const protocols = [
    {
      name: "SoftEther SSL-VPN",
      val: (listeners ?? []).filter((l) => l.Enables_bool).map((l) => l.Ports_u32).join(", ") || t("no listeners"),
      on: (listeners ?? []).some((l) => l.Enables_bool),
    },
    {
      name: "WireGuard",
      val: !wg ? "—" : !wg.supported ? t("Unsupported") : wg.enabled ? `UDP ${wg.default_udp_port ?? 51820}` : t("Off"),
      on: Boolean(wg?.supported && wg.enabled),
    },
    {
      name: "OpenVPN",
      val: openvpn?.EnableOpenVPN_bool ? `UDP ${openvpn.OpenVPNPortList_str || "1194"}` : t("Off"),
      on: Boolean(openvpn?.EnableOpenVPN_bool),
    },
    {
      name: "L2TP / IPsec",
      val: ipsec ? (ipsec.L2TP_IPsec_bool ? (String(ipsec.L2TP_DefaultHub_str || t("Enabled"))) : t("Off")) : "—",
      on: Boolean(ipsec?.L2TP_IPsec_bool),
    },
    {
      name: "MS-SSTP",
      val: openvpn?.EnableSSTP_bool ? t("Enabled (TCP 443)") : t("Off"),
      on: Boolean(openvpn?.EnableSSTP_bool),
    },
    {
      name: "VPN Azure",
      val: azure?.IsEnabled_bool ? (ddns?.CurrentHostName_str ? `${ddns.CurrentHostName_str}.vpnazure.net` : t("Enabled")) : t("Off"),
      on: Boolean(azure?.IsEnabled_bool),
    },
  ];
  const activeProtocolCount = protocols.filter((p) => p.on).length;

  return (
    <div className="page space-y-6">
      {/* ── 1. Unified Page Header ─────────────────────────────────────── */}
      <PageHead
        title={
          <span className="flex items-center gap-3 flex-wrap">
            <span>{t("Server overview")} · {currentServer?.name || probe?.hostname || "vpnserver"}</span>
            <span className="live-badge">
              <span className="live-badge__dot" />
              {t("Running")}{uptimeSeconds ? ` · ${formatDuration(uptimeSeconds)}` : ""}
            </span>
          </span>
        }
        sub={
          <span className="font-mono text-xs">
            {probe?.version || "SoftEther VPN"} · {currentServer ? `${currentServer.host}:${currentServer.port}` : "—"} · {t("{count} hubs on this server", { count: hubs?.length ?? 0 })}
          </span>
        }
        actions={
          <>
            <Link to="/server-settings" className="btn-secondary">
              <IconSettings size={14} /> {t("Server config")}
            </Link>
            <button onClick={() => setCreatingHub(true)} className="btn-primary">
              <IconPlus size={14} /> {t("New Virtual HUB")}
            </button>
          </>
        }
      />

      {/* ── 2. Stat KPI Cards ─────────────────────────────────────────── */}
      <div className="grid grid-cols-2 lg:grid-cols-4 gap-4">
        <Link to={sessionsUrl} className="stat-card stat-card--hero stat-card--clickable">
          <div className="stat-card__l flex items-center justify-between">
            <span className="flex items-center gap-2">
              <span className="stat-card__dot" />
              <span>{t("Online clients")}</span>
            </span>
            <IconChevron size={12} className="stat-card__chevron" />
          </div>
          <div className="stat-card__v">{formatCount(clientCount ?? activeSessions)}</div>
          <div className="stat-card__s">{t("Across all {count} hubs", { count: hubs?.length ?? 1 })}</div>
        </Link>

        <Link to="/users" className="stat-card stat-card--clickable">
          <div className="stat-card__l flex items-center justify-between">
            <span>{t("Configured users")}</span>
            <IconChevron size={12} className="stat-card__chevron" />
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
            {uptimeSeconds ? t("Since {duration} ago", { duration: formatDuration(uptimeSeconds) }) : t("since server start")}
          </div>
        </div>

        <div className="stat-card">
          <div className="stat-card__l flex items-center justify-between flex-wrap gap-2">
            <span>{t("Cumulative outbound (TX)")}</span>
            {net && <span className="stat-card__badge stat-card__badge--tx">↑ {formatBytes(netTx)}/s</span>}
          </div>
          <div className="stat-card__v">{formatBytes(txTotal)}</div>
          <div className="stat-card__s">
            {uptimeSeconds ? t("Since {duration} ago", { duration: formatDuration(uptimeSeconds) }) : t("since server start")}
          </div>
        </div>
      </div>

      {/* ── 3. Middle 12-Col Band: Telemetry (8) + Host Diagnostics (4) ─ */}
      <div className="grid grid-cols-1 lg:grid-cols-12 gap-5 items-stretch">
        <div className="lg:col-span-8 panel-card panel-card--split">
          <div className="panel-card__h">
            <div>
              <h2 className="panel-card__t">{t("Throughput Telemetry")}</h2>
              <p className="panel-card__s">{t(trafficHours >= 48 ? "Past {days} days · Recorded samples only; gaps are left blank." : "Past {hours} hours · Recorded samples only; gaps are left blank.", { days: trafficHours / 24, hours: trafficHours })}</p>
            </div>
            <div className="flex items-center gap-3 text-xs font-mono">
              <RangeSeg hours={trafficHours} onChange={setTrafficHours} />
            </div>
          </div>
          <div style={{ paddingTop: 4 }}>
            {serverUsage ? (
              <TrafficChart usage={serverUsage} emptyLabel={t("No server-wide traffic recorded in this time window yet.")} />
            ) : (
              <LoadingBlock label={t("Loading throughput telemetry")} />
            )}
          </div>
        </div>

        <div className="lg:col-span-4 panel-card panel-card--split">
          <div>
            <div className="panel-card__h">
              <div>
                <h2 className="panel-card__t">{t("Panel host resources")}</h2>
                <p className="panel-card__s">{t("Linux kernel rolling performance window")}</p>
              </div>
              {resources && resources.available !== false && (
                <span className="text-[11px] font-mono text-muted">{t("Sampling interval 3s")}</span>
              )}
            </div>

            {resources && resources.available !== false ? (
              <div className="space-y-3">
                {/* CPU */}
                <div className="p-3 rounded-md bg-raised border border-hairline space-y-1.5">
                  <div className="flex justify-between text-xs">
                    <span className="text-secondary font-medium">{t("CPU load")}</span>
                    <span className="font-mono font-semibold text-primary">
                      {(Number(resources.cpu?.overall?.usage_percent) || 0).toFixed(1)}%
                    </span>
                  </div>
                  <div className="w-full bg-slate-500/20 h-1.5 rounded-full overflow-hidden">
                    <div
                      className="bg-blue-500 h-full transition-all"
                      style={{ width: `${Math.min(100, Math.max(0, Number(resources.cpu?.overall?.usage_percent) || 0))}%` }}
                    />
                  </div>
                  <div className="text-[11px] text-muted font-mono truncate">
                    {t("Load")}: {resources.load?.one ?? "—"} (1m) · {resources.load?.five ?? "—"} (5m)
                  </div>
                </div>

                {/* RAM */}
                {(() => {
                  const mem = (resources.memory as Wire) ?? {};
                  const used = Number(mem.used_bytes) || 0;
                  const total = Number(mem.total_bytes) || 1;
                  const percent = Math.round((used / total) * 100);
                  return (
                    <div className="p-3 rounded-md bg-raised border border-hairline space-y-1.5">
                      <div className="flex justify-between text-xs">
                        <span className="text-secondary font-medium">{t("Memory")}</span>
                        <span className={`font-mono font-semibold ${percent >= 85 ? "text-amber-500" : "text-primary"}`}>
                          {percent}%
                        </span>
                      </div>
                      <div className="w-full bg-slate-500/20 h-1.5 rounded-full overflow-hidden">
                        <div
                          className={`h-full transition-all ${percent >= 85 ? "bg-amber-500" : "bg-blue-500"}`}
                          style={{ width: `${percent}%` }}
                        />
                      </div>
                      <div className="text-[11px] text-muted font-mono truncate">
                        {formatBytes(used)} / {formatBytes(total)}
                      </div>
                    </div>
                  );
                })()}

                {/* Network Throughput */}
                {(() => {
                  const totalMB = ((netRx + netTx) / (1024 * 1024)).toFixed(1);
                  return (
                    <div className="p-3 rounded-md bg-raised border border-hairline space-y-1.5">
                      <div className="flex justify-between text-xs">
                        <span className="text-secondary font-medium">{t("Network")}</span>
                        <span className="font-mono font-semibold text-emerald-500">
                          {totalMB} MB/s
                        </span>
                      </div>
                      <div className="w-full bg-slate-500/20 h-1.5 rounded-full overflow-hidden">
                        <div
                          className="bg-emerald-500 h-full transition-all"
                          style={{ width: `${Math.min(100, (Number(totalMB) / 20) * 100)}%` }}
                        />
                      </div>
                      <div className="text-[11px] text-muted font-mono truncate">
                        ↓ {formatBytes(netRx)}/s · ↑ {formatBytes(netTx)}/s
                      </div>
                    </div>
                  );
                })()}
              </div>
            ) : (
              <div className="space-y-2 text-xs font-mono text-secondary">
                <div className="flex justify-between py-2 border-b border-hairline">
                  <span className="text-muted">{t("Authentication protocol")}</span>
                  <span className="text-primary">Header / Basic</span>
                </div>
                <div className="flex justify-between py-2 border-b border-hairline">
                  <span className="text-muted">{t("Session retention policy")}</span>
                  <span className="text-primary">{t("30 days")}</span>
                </div>
                <div className="flex justify-between py-2">
                  <span className="text-muted">{t("Traffic sampling period")}</span>
                  <span className="text-primary">{t("5 minutes")}</span>
                </div>
              </div>
            )}
          </div>

          <div className="panel-card__foot">
            <span>{t("Traffic sampling period")}: {t("5 minutes")} · {t("30 days")}</span>
            <span>Header / Basic</span>
          </div>
        </div>
      </div>

      {/* ── 4. Bottom 12-Col Band: Virtual Hubs (7) + Protocols (5) ───── */}
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

          <HubList hubs={hubs} />
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

          <div className="protocol-grid">
            {protocols.map((p) => (
              <div key={p.name} className="protocol-cell">
                <div className="protocol-cell__info">
                  <div className="protocol-cell__name">{p.name}</div>
                  <div className="protocol-cell__val" title={p.val}>{p.val}</div>
                </div>
                <span className={`protocol-cell__dot${p.on ? " protocol-cell__dot--on" : ""}`} />
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
