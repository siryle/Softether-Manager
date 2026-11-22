"use client";

import { useCallback, useMemo, useRef, useState } from "react";
import {
  ConfirmSheet,
  Empty,
  LoadingBlock,
  PageHead,
  SearchBox,
  SortTh,
  sortRows,
  usePoll,
  useSort,
} from "../components/bits";
import { api, type Wire } from "../lib/api";
import { useI18n } from "../lib/i18n";
import { useServer } from "../lib/server";
import { Link, seg } from "../lib/router";
import { formatBytes, formatCount, formatIPLocation, hasIPLocation, timeAgo } from "../lib/util";
import { IconClose, IconRefresh, IconReport } from "../ui/Icon";
import { cleanHost, EndpointCell, SessionSheet } from "./hub/HubSessions";

function clientProtocol(s: Wire): string {
  if (s.Protocol_str) return String(s.Protocol_str);
  const raw = `${s.UnderlayProtocol_str || ""} ${s.ClientProductName_str || ""} ${s.ClientOsName_str || ""} ${s.Name_str || ""} ${s.Hostname_str || ""}`.toLowerCase();
  const sPort = Number(s.ServerPort_u32 || s.ServerPort2_u32 || 0);
  const cPort = Number(s.ClientPort_u32 || 0);
  if (raw.includes("wireguard") || raw.includes("sid-wg-") || raw.includes("-[wg]-") || raw.includes("[wg]")) return "WireGuard";
  if (raw.includes("openvpn") || raw.includes("sid-ovpn") || raw.includes("-[ovpn") || raw.includes("[ovpn")) return "OpenVPN";
  if (raw.includes("l2tp")) return "L2TP/IPsec";
  if (raw.includes("etherip")) return "EtherIP/IPsec";
  if (raw.includes("ikev2")) return "IKEv2/IPsec";
  if (raw.includes("ipsec") || raw.includes("isakmp") || /(^|[^a-z0-9])ppp([^a-z0-9]|$)/.test(raw)) return "L2TP/IPsec";
  if (raw.includes("sstp")) return "MS-SSTP";
  if (raw.includes("rudp") || raw.includes("udp acceleration")) return "SoftEther RUDP";
  if (sPort === 1701 || sPort === 500 || sPort === 4500 || cPort === 1701) return "L2TP/IPsec";
  if (sPort === 51820) return "WireGuard";
  if (sPort === 1194) return "OpenVPN";
  if (raw.includes("ssl") || raw.includes("tls") || raw.includes("softether") || raw.includes("standard tcp/ip") || raw.includes("tcp/ip")) {
    return "SoftEther SSL-VPN";
  }
  return "";
}

/**
 * Aggregated online clients radar across all Virtual Hubs.
 * Tracks per-session live throughput between polls, exposes protocol/hub
 * filters, inline traffic bars, NAT connection warnings, and one-click disconnect.
 */
export function AllClients() {
  const { t } = useI18n();
  const { setClientCount } = useServer();
  const [data, setData] = useState<{ hubs: string[]; clients: Wire[]; errors: { hub: string; error: string }[] } | null>(null);
  const [rates, setRates] = useState<Record<string, number>>({});
  const [query, setQuery] = useState("");
  const [hubFilter, setHubFilter] = useState("ALL");
  const [protoFilter, setProtoFilter] = useState("ALL");
  const [openSession, setOpenSession] = useState<{ hub: string; name: string } | null>(null);
  const [killing, setKilling] = useState<Wire | null>(null);
  const [refreshing, setRefreshing] = useState(false);
  const inFlight = useRef(false);
  const prevBytesRef = useRef<Map<string, { bytes: number; at: number }>>(new Map());
  const { sort, toggle } = useSort({ key: "rate", dir: "desc" });

  const load = useCallback(async () => {
    if (inFlight.current) return;
    inFlight.current = true;
    try {
      const res = await api.allClients().catch(() => null);
      const nowMs = Date.now();
      if (res && Array.isArray(res.clients)) {
        const nextRates: Record<string, number> = {};
        const nextSnap = new Map<string, { bytes: number; at: number }>();
        for (const s of res.clients) {
          const k = `${String(s.HubName_str || "")}:${String(s.Name_str || "")}`;
          const bytes = Number(s.PacketSize_u64 ?? 0);
          const prev = prevBytesRef.current.get(k);
          if (prev && nowMs > prev.at && bytes >= prev.bytes) {
            nextRates[k] = (bytes - prev.bytes) / ((nowMs - prev.at) / 1000);
          } else {
            nextRates[k] = 0;
          }
          nextSnap.set(k, { bytes, at: nowMs });
        }
        prevBytesRef.current = nextSnap;
        setRates(nextRates);
        setClientCount(res.clients.length);
      }
      setData(res);
    } finally {
      inFlight.current = false;
      setRefreshing(false);
    }
  }, [setClientCount]);
  usePoll(load, "detail", []);

  const protocols = useMemo(() => {
    if (!data) return [];
    const counts = new Map<string, number>();
    for (const s of data.clients) {
      const p = clientProtocol(s);
      if (p) counts.set(p, (counts.get(p) ?? 0) + 1);
    }
    return [...counts.entries()].sort((a, b) => b[1] - a[1]);
  }, [data]);

  const summary = useMemo(() => {
    if (!data) return null;
    let activeTransfer = 0;
    let totalRate = 0;
    let highNat = 0;
    const geoCounts = new Map<string, number>();
    for (const s of data.clients) {
      const k = `${String(s.HubName_str || "")}:${String(s.Name_str || "")}`;
      const rate = rates[k] ?? 0;
      totalRate += rate;
      if (rate >= 1024) activeTransfer++;
      if ((Number(s.ActiveNatTcp_u32) || 0) >= 100) highNat++;
      const loc = String(s.IpLocation_str || "").trim();
      if (loc && loc !== "—") geoCounts.set(loc, (geoCounts.get(loc) ?? 0) + 1);
    }
    const topGeo = [...geoCounts.entries()].sort((a, b) => b[1] - a[1]).slice(0, 3);
    return { activeTransfer, totalRate, highNat, topGeo };
  }, [data, rates]);

  const filtered = useMemo(() => {
    if (!data) return null;
    const q = query.trim().toLowerCase();
    const matched = data.clients.filter((s) => {
      if (hubFilter !== "ALL" && String(s.HubName_str || "") !== hubFilter) return false;
      if (protoFilter !== "ALL" && clientProtocol(s) !== protoFilter) return false;
      if (!q) return true;
      return [
        s.Name_str,
        s.Username_str,
        s.Realname_utf,
        s.GroupName_str,
        s.Note_utf,
        s.HubName_str,
        s.ClientIP_ip,
        s.VirtualIP_ip,
        s.IpLocation_str,
        s.Hostname_str,
        s.RealHostname_str,
        s.ClientProductName_str,
        s.UnderlayProtocol_str,
        clientProtocol(s),
      ]
        .filter(Boolean)
        .some((v) => String(v).toLowerCase().includes(q));
    });
    return sortRows(matched, sort, (s, key) => {
      const k = `${String(s.HubName_str || "")}:${String(s.Name_str || "")}`;
      if (key === "rate") return rates[k] ?? 0;
      if (key === "bytes") return Number(s.PacketSize_u64 ?? 0);
      if (key === "nat") return Number(s.ActiveNatTcp_u32 ?? 0);
      if (key === "started") return Date.parse(String(s.CreatedTime_dt || "")) || 0;
      return String(s.Username_str || "");
    });
  }, [data, query, hubFilter, protoFilter, sort, rates]);

  const maxBytes = useMemo(
    () => Math.max(1, ...(filtered ?? []).map((s) => Number(s.PacketSize_u64 ?? 0))),
    [filtered],
  );

  return (
    <div className="page">
      <PageHead
        title={t("Online Clients")}
        sub={
          data
            ? t("{clients} across {hubs} hubs", {
                clients: data.clients.length,
                hubs: data.hubs.length,
              })
            : t("Connected clients across all Virtual Hubs.")
        }
        actions={
          <div className="users-toolbar">
            <SearchBox value={query} onChange={setQuery} placeholder={t("user, host, IP…")} />
            <Link to="/audit-report" className="btn btn--sm" title={t("Audit Report")}>
              <IconReport size={14} /> <span className="btn__label">{t("Audit Report")}</span>
            </Link>
            <button
              className="btn btn--sm"
              onClick={() => {
                setRefreshing(true);
                void load();
              }}
              title={t("Refresh")}
              disabled={refreshing}
            >
              {refreshing ? <span className="spin" /> : <IconRefresh size={14} />}
            </button>
          </div>
        }
      />

      {data?.errors.map((e) => (
        <div key={e.hub} className="alert alert--warn">
          {t("Hub {hub} could not be listed: {error}", { hub: e.hub, error: e.error })}
        </div>
      ))}

      {/* Live Radar Summary Strip */}
      {data && data.clients.length > 0 && summary && (
        <div className="card viz-strip">
          <div className="viz-strip__metrics mono">
            <span>
              <small>{t("Online")}</small>
              <b>{formatCount(data.clients.length)}</b>
            </span>
            <span>
              <small>{t("Active transfer")}</small>
              <b style={{ color: summary.activeTransfer > 0 ? "var(--ok)" : undefined }}>
                {formatCount(summary.activeTransfer)}
              </b>
            </span>
            <span>
              <small>{t("Live throughput")}</small>
              <b style={{ color: "var(--accent-strong)" }}>{formatBytes(summary.totalRate)}/s</b>
            </span>
            {summary.highNat > 0 && (
              <span>
                <small>{t("High NAT")}</small>
                <b style={{ color: "var(--warn)" }}>{formatCount(summary.highNat)}</b>
              </span>
            )}
          </div>
          {summary.topGeo.length > 0 && (
            <div className="viz-strip__chips">
              <span className="micro">{t("Top regions")}:</span>
              {summary.topGeo.map(([loc, cnt]) => (
                <button
                  key={loc}
                  type="button"
                  className="chip"
                  onClick={() => setQuery((cur) => (cur === loc ? "" : loc))}
                >
                  {loc} <b className="mono">{cnt}</b>
                </button>
              ))}
            </div>
          )}
        </div>
      )}

      {/* Quick Filter Chips (Hub + Protocol) */}
      {data && data.clients.length > 0 && (data.hubs.length > 1 || protocols.length > 1) && (
        <div className="viz-filter-row">
          {data.hubs.length > 1 && (
            <div className="filterseg" role="group" aria-label={t("Virtual Hub")}>
              <button
                type="button"
                className={`filterseg__btn${hubFilter === "ALL" ? " on" : ""}`}
                onClick={() => setHubFilter("ALL")}
              >
                {t("All hubs")}
              </button>
              {data.hubs.map((h) => (
                <button
                  key={h}
                  type="button"
                  className={`filterseg__btn${hubFilter === h ? " on" : ""}`}
                  onClick={() => setHubFilter(h)}
                >
                  {h}
                </button>
              ))}
            </div>
          )}
          {protocols.length > 1 && (
            <div className="filterseg" role="group" aria-label={t("Protocol")}>
              <button
                type="button"
                className={`filterseg__btn${protoFilter === "ALL" ? " on" : ""}`}
                onClick={() => setProtoFilter("ALL")}
              >
                {t("All protocols")}
              </button>
              {protocols.map(([p, cnt]) => (
                <button
                  key={p}
                  type="button"
                  className={`filterseg__btn${protoFilter === p ? " on" : ""}`}
                  onClick={() => setProtoFilter(p)}
                >
                  {p} ({cnt})
                </button>
              ))}
            </div>
          )}
        </div>
      )}

      {filtered === null ? (
        <LoadingBlock label="loading sessions" />
      ) : filtered.length === 0 ? (
        <Empty title={query || hubFilter !== "ALL" || protoFilter !== "ALL" ? t("nothing matches") : t("no online clients")}>
          {query || hubFilter !== "ALL" || protoFilter !== "ALL"
            ? t("No client, host or IP matches that search.")
            : t("No clients are connected right now.")}
        </Empty>
      ) : (
        <>
          {/* Desktop Table View */}
          <div className="only-desktop-b">
            <div className="card tcard">
              <div className="tscroll" tabIndex={0} role="region" aria-label={t("Online Clients")}>
                <table className="dtable">
                  <thead>
                    <tr>
                      <th scope="col" style={{ minWidth: 130 }}>{t("User")}</th>
                      <th scope="col" style={{ minWidth: 100 }}>{t("Real name")}</th>
                      <th scope="col">{t("Virtual HUB")}</th>
                      <th scope="col">{t("IP / Host")}</th>
                      <SortTh sortKey="rate" sort={sort} onSort={toggle}>{t("Live rate")}</SortTh>
                      <SortTh sortKey="bytes" sort={sort} onSort={toggle}>{t("Transfer")}</SortTh>
                      <SortTh sortKey="nat" sort={sort} onSort={toggle}>{t("NAT TCP")}</SortTh>
                      <SortTh sortKey="started" sort={sort} onSort={toggle}>{t("Started")}</SortTh>
                      <th scope="col" className="tact" style={{ width: 96 }} aria-label={t("Actions")} />
                    </tr>
                  </thead>
                  <tbody>
                    {filtered.map((s) => {
                      const hub = String(s.HubName_str || "");
                      const name = String(s.Name_str || "");
                      const key = `${hub}:${name}`;
                      const rate = rates[key] ?? 0;
                      const bytes = Number(s.PacketSize_u64 ?? 0);
                      const bytePct = Math.round((bytes / maxBytes) * 100);
                      const nat = Number(s.ActiveNatTcp_u32 ?? 0);
                      const proto = clientProtocol(s);
                      return (
                        <tr
                          key={key}
                          className="clickable"
                          onClick={() => setOpenSession({ hub, name })}
                        >
                          <td style={{ minWidth: 130 }}>
                            {s.Username_str ? (
                              <Link
                                to={`/hub/${seg(hub)}/user/${seg(String(s.Username_str))}`}
                                className="linkish mono bold"
                                onClick={(e) => e.stopPropagation()}
                              >
                                {String(s.Username_str)}
                              </Link>
                            ) : (
                              <span className="tmono">{name || "—"}</span>
                            )}
                            {s.Note_utf ? (
                              <div className="tsub truncate" style={{ maxWidth: 170 }} title={String(s.Note_utf)}>
                                {String(s.Note_utf)}
                              </div>
                            ) : null}
                          </td>
                          <td style={{ minWidth: 100, whiteSpace: "nowrap" }}>
                            {s.Realname_utf ? (
                              <span>{String(s.Realname_utf)}</span>
                            ) : (
                              <span className="tmono" style={{ color: "var(--text-faint)" }}>—</span>
                            )}
                          </td>
                          <td style={{ whiteSpace: "nowrap" }}>
                            <div className="viz-stack">
                              <Link
                                to={`/hub/${seg(hub)}`}
                                className="chip chip--sm mono"
                                onClick={(e) => e.stopPropagation()}
                              >
                                {hub}
                              </Link>
                              {proto && <span className="micro mono" style={{ color: "var(--text-muted)" }}>{proto}</span>}
                            </div>
                          </td>
                          <td className="tmono">
                            <EndpointCell
                              virtualIp={s.VirtualIP_ip}
                              clientIp={s.ClientIP_ip}
                              location={hasIPLocation(s.IpLocation_str, s.IpIsp_str) ? formatIPLocation(s.IpLocation_str, s.IpIsp_str) : ""}
                              host={cleanHost(s)}
                            />
                          </td>
                          <td className="tmono">
                            {rate >= 1024 ? (
                              <span style={{ color: rate >= 2 * 1024 * 1024 ? "var(--ok)" : "var(--accent-strong)", fontWeight: 600 }}>
                                {formatBytes(rate)}/s
                              </span>
                            ) : (
                              <span style={{ color: "var(--text-faint)" }}>{t("Idle")}</span>
                            )}
                          </td>
                          <td className="tmono" style={{ minWidth: 120 }}>
                            <div>{formatBytes(bytes)}</div>
                            <div className="viz-bar viz-bar--sm" style={{ marginTop: 4 }}>
                              <div className="viz-bar__seg" style={{ width: `${Math.max(bytes > 0 ? 4 : 0, bytePct)}%`, background: "var(--chart-send)" }} />
                            </div>
                          </td>
                          <td className="tmono">
                            {nat >= 100 ? (
                              <span className="viz-badge viz-badge--warn">{formatCount(nat)}</span>
                            ) : (
                              <span>{formatCount(nat)}</span>
                            )}
                          </td>
                          <td className="tmono">{timeAgo(s.CreatedTime_dt as string)}</td>
                          <td className="tact">
                            <button
                              className="btn btn--sm btn--danger"
                              onClick={(e) => {
                                e.stopPropagation();
                                setKilling(s);
                              }}
                            >
                              <IconClose size={13} /> {t("Disconnect")}
                            </button>
                          </td>
                        </tr>
                      );
                    })}
                  </tbody>
                </table>
              </div>
            </div>
          </div>

          {/* Mobile Card View */}
          <div className="rows only-mobile-b">
            {filtered.map((s) => {
              const hub = String(s.HubName_str || "");
              const name = String(s.Name_str || "");
              const key = `${hub}:${name}`;
              const rate = rates[key] ?? 0;
              const proto = clientProtocol(s);
              return (
                <button
                  key={key}
                  className="row"
                  onClick={() => setOpenSession({ hub, name })}
                >
                  <div className="row__main">
                    <div className="row__name">
                      <span className="mono truncate">{String(s.Username_str || name)}</span>
                      {s.Realname_utf ? (
                        <span style={{ fontSize: "12px", color: "var(--muted)" }}>({String(s.Realname_utf)})</span>
                      ) : null}
                      <span className="chip chip--sm mono" style={{ marginLeft: 4 }}>{hub}</span>
                      {proto && <span className="chip chip--sm mono">{proto}</span>}
                    </div>
                    {s.Note_utf ? (
                      <div className="tsub truncate" style={{ fontSize: "12px", color: "var(--muted)", marginBottom: 4 }}>
                        {String(s.Note_utf)}
                      </div>
                    ) : null}
                    <div className="spec">
                      {rate >= 1024 && (
                        <span className="chip mono" style={{ color: "var(--accent-strong)" }}>
                          {formatBytes(rate)}/s
                        </span>
                      )}
                      {s.VirtualIP_ip ? (
                        <span className="chip mono" style={{ color: "var(--ok)" }}>
                          {String(s.VirtualIP_ip)}
                        </span>
                      ) : null}
                      {s.ClientIP_ip ? (
                        <span className="chip">
                          <i>{t("WAN")}</i>
                          {String(s.ClientIP_ip)}
                          {s.IpLocation_str ? ` · ${String(s.IpLocation_str)}` : ""}
                        </span>
                      ) : null}
                      <span className="chip">
                        <i>{t("data")}</i>
                        {formatBytes(Number(s.PacketSize_u64 ?? 0))}
                      </span>
                      <span className="chip">
                        <i>{t("since")}</i>
                        {timeAgo(s.CreatedTime_dt as string)}
                      </span>
                    </div>
                  </div>
                  <div className="row__side">
                    <button
                      className="btn btn--sm btn--danger"
                      onClick={(e) => {
                        e.stopPropagation();
                        setKilling(s);
                      }}
                    >
                      <IconClose size={13} /> {t("Disconnect")}
                    </button>
                  </div>
                </button>
              );
            })}
          </div>
        </>
      )}

      {openSession && (
        <SessionSheet
          hub={openSession.hub}
          name={openSession.name}
          onClose={() => setOpenSession(null)}
        />
      )}

      {killing && (
        <ConfirmSheet
          title={t("Disconnect this session?")}
          verb="Disconnect"
          body={
            <>
              <span className="mono">{String(killing.Name_str)}</span>
              {killing.Username_str ? <> {t("— user {name}", { name: String(killing.Username_str) })}</> : null}{" "}
              {t("is cut immediately. The client may simply reconnect unless the user is disabled first.")}
            </>
          }
          onClose={() => setKilling(null)}
          onConfirm={async () => {
            await api.killSession(String(killing.HubName_str), String(killing.Name_str));
            await load();
          }}
        />
      )}
    </div>
  );
}
