"use client";

import { useCallback, useMemo, useState } from "react";
import { Empty, ErrorAlert, LoadingBlock, usePoll } from "../components/bits";
import { RangeSeg, TrafficChart } from "../components/TrafficChart";
import { api, type Wire, type Usage } from "../lib/api";
import { useI18n, t } from "../lib/i18n";
import { useServer } from "../lib/server";
import { Link } from "../lib/router";
import { formatBytes, formatCount, formatDuration } from "../lib/util";
import {
  IconChevron,
  IconLogs,
  IconPlus,
  IconPulse,
  IconSettings,
  IconTerminal,
} from "../ui/Icon";

import { CreateHubSheet, HubList } from "./Hubs";

/**
 * Unslopped Dashboard View for SoftEther Manager.
 *
 * 1. Calm Contextual Header: Server title, operational facts, primary actions.
 * 2. 4-Column Stat Cards: High-signal tabular figures without decorative micro-sparklines.
 * 3. 24h Dual Throughput Telemetry Chart: Full-width landmark (One Bright Moment).
 * 4. 2-Column Operational Grid:
 *    - Left: Virtual HUB Topology table & Host Machine performance sensors.
 *    - Right: Access Protocols & Ports list, Server Bootstrap info, and Quick Tools.
 */
export function Dashboard() {
  const { probe, health, refresh } = useServer();
  const { t } = useI18n();
  const [resources, setResources] = useState<Wire | null>(null);
  const [overview, setOverview] = useState<Wire | null>(null);
  const [trafficHours, setTrafficHours] = useState(24);
  const [serverUsage, setServerUsage] = useState<Usage | null>(null);
  const [creatingHub, setCreatingHub] = useState(false);

  const loadResources = useCallback(async () => {
    setResources(await api.resources().catch(() => null));
  }, []);
  usePoll(loadResources, "live", []);

  const loadOverview = useCallback(async () => {
    if (health === "online") setOverview(await api.overview().catch(() => null));
  }, [health]);
  usePoll(loadOverview, "detail", [health]);

  const loadTraffic = useCallback(async () => {
    if (health === "online") {
      setServerUsage(await api.serverTraffic(trafficHours).catch(() => null));
    }
  }, [health, trafficHours]);
  usePoll(loadTraffic, "detail", [health, trafficHours]);

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

  const activeSessions = Number(status?.NumSessionsTotal_u32) || 0;
  const totalUsers = Number(status?.NumUsers_u32) || 0;
  const rxTotal = (Number(status?.["Recv.UnicastBytes_u64"]) || 0) + (Number(status?.["Recv.BroadcastBytes_u64"]) || 0);
  const txTotal = (Number(status?.["Send.UnicastBytes_u64"]) || 0) + (Number(status?.["Send.BroadcastBytes_u64"]) || 0);

  return (
    <div className="dash-view space-y-6">
      {/* ── 1. Calm Contextual Header ─────────────────────────────────── */}
      <div className="dash-header pb-4 border-b border-hairline flex flex-col sm:flex-row sm:items-baseline justify-between gap-4">
        <div>
          <h1 className="text-xl font-semibold tracking-tight text-primary">
            {t("Server overview")} · {probe?.hostname || "vpnserver"}
          </h1>
          <p className="text-xs text-secondary mt-1 font-mono">
            {probe?.version || "SoftEther VPN"} {uptimeSeconds ? `· ${t("Continuous running")} ${formatDuration(uptimeSeconds)}` : ""} · {probe?.hostname?.includes(":") ? probe.hostname : `${probe?.hostname || "127.0.0.1"}:5555`}
          </p>
        </div>
        <div className="flex items-center gap-2 shrink-0">
          <button onClick={() => setCreatingHub(true)} className="btn-primary">
            <IconPlus size={14} /> {t("New Virtual HUB")}
          </button>
          <Link to="/server-settings" className="btn-secondary">
            <IconSettings size={14} /> {t("Server config")}
          </Link>
        </div>
      </div>

      {health === "offline" && (
        <ErrorAlert>
          <div className="alert__t">{t("The VPN server is not answering.")}</div>
          {probe?.error} — <Link to="/connect" className="linkish">{t("check the connection")}</Link>
        </ErrorAlert>
      )}

      {/* ── 2. Stat KPI Cards ─────────────────────────────────────────── */}
      <div className="grid grid-cols-2 lg:grid-cols-4 gap-4">
        <div className="stat-card">
          <div className="stat-card__l">{t("Active sessions")}</div>
          <div className="stat-card__v">{formatCount(activeSessions)}</div>
          <div className="stat-card__s">{t("Across all {count} hubs", { count: hubs?.length ?? 1 })}</div>
        </div>

        <div className="stat-card">
          <div className="stat-card__l">{t("Configured users")}</div>
          <div className="stat-card__v">{formatCount(totalUsers)}</div>
          <div className="stat-card__s">{t("Local passwords / certificates")}</div>
        </div>

        <div className="stat-card">
          <div className="stat-card__l">{t("Cumulative inbound (RX)")}</div>
          <div className="stat-card__v">{formatBytes(rxTotal)}</div>
          <div className="stat-card__s">
            {uptimeSeconds ? t("Since {duration} ago", { duration: formatDuration(uptimeSeconds) }) : t("since server start")}
          </div>
        </div>

        <div className="stat-card">
          <div className="stat-card__l">{t("Cumulative outbound (TX)")}</div>
          <div className="stat-card__v">{formatBytes(txTotal)}</div>
          <div className="stat-card__s">
            {uptimeSeconds ? t("Since {duration} ago", { duration: formatDuration(uptimeSeconds) }) : t("since server start")}
          </div>
        </div>
      </div>

      {/* ── 3. 24-hour Telemetry Chart (Landmark) ─────────────────────── */}
      <div className="panel-card space-y-4">
        <div className="panel-card__h">
          <div>
            <h2 className="panel-card__t">{t("Throughput Telemetry")}</h2>
            <p className="panel-card__s">{t("5-minute rolling samples · Smooth bandwidth distribution")}</p>
          </div>
          <div className="flex items-center gap-4 text-xs font-mono">
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

      {/* ── 4. 2-Column Operational Grid ─────────────────────────────── */}
      <div className="grid grid-cols-1 lg:grid-cols-3 gap-6">
        {/* Left Column (2 Cols) */}
        <div className="lg:col-span-2 space-y-5">
          {/* Virtual HUB Topology */}
          <div className="panel-card space-y-3">
            <div className="panel-card__h">
              <div>
                <h3 className="panel-card__t">{t("Virtual Hub Topology")}</h3>
                <p className="panel-card__s">{t("Broadcast domain isolation and policies")}</p>
              </div>
              <button onClick={() => setCreatingHub(true)} className="btn-secondary">
                <IconPlus size={13} /> {t("Create hub")}
              </button>
            </div>

            <HubList hubs={hubs} />
          </div>

          {/* Host Machine Performance Diagnostics */}
          {resources && resources.available !== false && (
            <div className="panel-card space-y-3">
              <div className="panel-card__h">
                <div>
                  <h3 className="panel-card__t">{t("Host machine performance")}</h3>
                  <p className="panel-card__s">{t("Linux kernel rolling performance window")}</p>
                </div>
                <span className="text-[11px] font-mono text-muted">{t("Sampling interval 3s")}</span>
              </div>

              <div className="grid grid-cols-1 sm:grid-cols-3 gap-3">
                {/* CPU */}
                <div className="p-3 rounded bg-raised border border-hairline space-y-1.5">
                  <div className="flex justify-between text-xs">
                    <span className="text-muted">{t("CPU load")}</span>
                    <span className="font-mono font-medium text-primary">
                      {(Number(resources.cpu?.overall?.percent) || 0).toFixed(1)}%
                    </span>
                  </div>
                  <div className="w-full bg-slate-500/20 h-1.5 rounded-full overflow-hidden">
                    <div
                      className="bg-blue-500 h-full transition-all"
                      style={{ width: `${Math.min(100, Math.max(0, Number(resources.cpu?.overall?.percent) || 0))}%` }}
                    />
                  </div>
                  <div className="text-[10px] text-muted font-mono truncate">
                    {t("Load")}: {resources.cpu?.load_1m ?? "—"} (1m) · {resources.cpu?.load_5m ?? "—"} (5m)
                  </div>
                </div>

                {/* RAM */}
                {(() => {
                  const mem = (resources.memory as Wire) ?? {};
                  const used = Number(mem.used_bytes) || 0;
                  const total = Number(mem.total_bytes) || 1;
                  const percent = Math.round((used / total) * 100);
                  return (
                    <div className="p-3 rounded bg-raised border border-hairline space-y-1.5">
                      <div className="flex justify-between text-xs">
                        <span className="text-muted">{t("Memory")}</span>
                        <span className={`font-mono font-medium ${percent >= 85 ? "text-amber-500" : "text-primary"}`}>
                          {percent}%
                        </span>
                      </div>
                      <div className="w-full bg-slate-500/20 h-1.5 rounded-full overflow-hidden">
                        <div
                          className={`h-full transition-all ${percent >= 85 ? "bg-amber-500" : "bg-blue-500"}`}
                          style={{ width: `${percent}%` }}
                        />
                      </div>
                      <div className="text-[10px] text-muted font-mono truncate">
                        {formatBytes(used)} / {formatBytes(total)}
                      </div>
                    </div>
                  );
                })()}

                {/* Network Throughput */}
                {(() => {
                  const net = (resources.network as Wire) ?? {};
                  const rx = Number(net.rx_bytes_per_second) || 0;
                  const tx = Number(net.tx_bytes_per_second) || 0;
                  const totalMB = ((rx + tx) / (1024 * 1024)).toFixed(1);
                  return (
                    <div className="p-3 rounded bg-raised border border-hairline space-y-1.5">
                      <div className="flex justify-between text-xs">
                        <span className="text-muted">{t("Network")}</span>
                        <span className="font-mono font-medium text-emerald-500">
                          {totalMB} MB/s
                        </span>
                      </div>
                      <div className="w-full bg-slate-500/20 h-1.5 rounded-full overflow-hidden">
                        <div
                          className="bg-emerald-500 h-full transition-all"
                          style={{ width: `${Math.min(100, (Number(totalMB) / 20) * 100)}%` }}
                        />
                      </div>
                      <div className="text-[10px] text-muted font-mono truncate">
                        ↓ {formatBytes(rx)}/s · ↑ {formatBytes(tx)}/s
                      </div>
                    </div>
                  );
                })()}
              </div>
            </div>
          )}
        </div>

        {/* Right Column (1 Col) */}
        <div className="space-y-5">
          {/* Listening Ports & Protocols */}
          <div className="panel-card space-y-3">
            <h3 className="panel-card__t">{t("Listening ports & protocols")}</h3>
            <div className="divide-y divide-hairline text-xs font-mono">
              <div className="py-2.5 flex items-center justify-between">
                <span className="text-secondary">SoftEther SSL-VPN</span>
                <span className="text-primary font-medium">
                  {(listeners ?? []).filter((l) => l.Enables_bool).map((l) => l.Ports_u32).join(", ") || t("no listeners")}
                </span>
              </div>
              <div className="py-2.5 flex items-center justify-between">
                <span className="text-secondary">L2TP / IPsec</span>
                <span className={ipsec?.L2TP_IPsec_bool ? "text-emerald-500 font-medium" : "text-muted"}>
                  {ipsec ? (ipsec.L2TP_IPsec_bool ? (ipsec.L2TP_DefaultHub_str || t("Enabled")) : t("Off")) : "—"}
                </span>
              </div>
              <div className="py-2.5 flex items-center justify-between">
                <span className="text-secondary">OpenVPN</span>
                <span className={openvpn?.EnableOpenVPN_bool ? "text-primary font-medium" : "text-muted"}>
                  {openvpn?.EnableOpenVPN_bool ? `UDP ${openvpn.OpenVPNPortList_str ?? "1194"}` : t("Off")}
                </span>
              </div>
              <div className="py-2.5 flex items-center justify-between">
                <span className="text-secondary">MS-SSTP</span>
                <span className={openvpn?.EnableSSTP_bool ? "text-emerald-500 font-medium" : "text-muted"}>
                  {openvpn?.EnableSSTP_bool ? t("Enabled (TCP 443)") : t("Off")}
                </span>
              </div>
              <div className="py-2.5 flex items-center justify-between">
                <span className="text-secondary">VPN Azure</span>
                <span className={azure?.IsEnabled_bool ? "text-emerald-500 font-medium" : "text-muted"}>
                  {azure?.IsEnabled_bool ? (ddns?.CurrentHostName_str ? `${ddns.CurrentHostName_str}.vpnazure.net` : t("Enabled")) : t("Off")}
                </span>
              </div>
            </div>
          </div>

          {/* Server Bootstrap Info */}
          <div className="panel-card space-y-2 text-xs">
            <div className="panel-card__t">{t("Server bootstrap info")}</div>
            <div className="space-y-1 font-mono text-[11px] text-secondary">
              <div className="flex justify-between py-1 border-b border-hairline">
                <span>{t("Authentication protocol")}</span>
                <span className="text-primary">Header / Basic</span>
              </div>
              <div className="flex justify-between py-1 border-b border-hairline">
                <span>{t("Session retention policy")}</span>
                <span className="text-primary">{t("30 days")}</span>
              </div>
              <div className="flex justify-between py-1">
                <span>{t("Traffic sampling period")}</span>
                <span className="text-primary">{t("5 minutes")}</span>
              </div>
            </div>
          </div>

          {/* Quick Tools */}
          <div className="panel-card space-y-2">
            <div className="panel-card__t">{t("Tools")}</div>
            <div className="divide-y divide-hairline">
              <ToolRow to="/connections" icon={<IconPulse size={15} />} title={t("Connections")} sub={t("TCP connections into the server, live")} />
              <ToolRow to="/logs" icon={<IconLogs size={15} />} title={t("Logs")} sub={t("server, hub security and packet logs")} />
              <ToolRow to="/console" icon={<IconTerminal size={15} />} title={t("API console")} sub={t("every RPC method, callable raw")} />
              <ToolRow to="/server-settings" icon={<IconSettings size={15} />} title={t("Server settings")} sub={t("listeners, certificate, bridges…")} />
            </div>
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

function ToolRow({ to, icon, title, sub }: { to: string; icon: React.ReactNode; title: string; sub: string }) {
  return (
    <Link to={to} className="flex items-center justify-between py-2.5 hover:text-primary transition-colors text-xs">
      <div className="flex items-center gap-2.5 min-w-0">
        <span className="text-blue-500 shrink-0">{icon}</span>
        <div className="min-w-0">
          <div className="font-medium text-primary truncate">{title}</div>
          <div className="text-[11px] text-muted truncate">{sub}</div>
        </div>
      </div>
      <IconChevron size={14} className="text-muted shrink-0" />
    </Link>
  );
}
