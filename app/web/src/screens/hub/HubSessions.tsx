"use client";

import { t } from "../../lib/i18n";
import { useCallback, useEffect, useState } from "react";
import { ConfirmSheet, Empty, KV, LoadingBlock, SearchBox, SectionTitle, usePoll } from "../../components/bits";
import { api, type Wire } from "../../lib/api";
import { navigate, seg } from "../../lib/router";
import { SESSION_TYPE } from "../../lib/se";
import { useToast } from "../../lib/toast";
import { formatBytes, formatCount, formatDate, timeAgo } from "../../lib/util";
import { IconClose } from "../../ui/Icon";
import { Sheet } from "../../ui/Sheet";
import { Pill } from "../../ui/Status";

/**
 * Live sessions: who is on the wire right now. Polled every few seconds --
 * this is the screen an operator keeps open during an incident, and the one
 * whose kill button has to work the moment it is pressed.
 */
function isIp(val?: string): boolean {
  if (!val) return false;
  return /^(\d{1,3}\.){3}\d{1,3}$/.test(val.trim()) || val.includes(":");
}

function filterIpv4(ipStr?: any): string {
  if (!ipStr) return "";
  const parts = String(ipStr).split(/[\s,]+/);
  const v4 = parts.filter((p) => /^(\d{1,3}\.){3}\d{1,3}$/.test(p.trim()));
  return v4.join(", ");
}

function cleanHost(d: Wire): string {
  const serverName = String(d.ServerName_str || d.RemoteHostname_str || "").trim().toLowerCase();
  const clientReported = String(d.ClientHostname_str || "").trim();
  if (clientReported && !isIp(clientReported) && (!serverName || clientReported.toLowerCase() !== serverName)) {
    return clientReported;
  }
  if (d.RealHostname_str) {
    const s = String(d.RealHostname_str).trim();
    if (s && !isIp(s) && (!serverName || s.toLowerCase() !== serverName)) {
      return s;
    }
  }
  const h = String(d.SessionStatus_ClientHostName_str || d.Hostname_str || "").trim();
  const ip = String(d.Client_Ip_Address_ip || d.ClientIP_ip || "").trim();
  if (!h || h === ip || isIp(h) || (serverName && h.toLowerCase() === serverName)) {
    return "—";
  }
  return h;
}

function formatTcpConnections(d: Wire, t: (s: string) => string): string {
  const proto = String(d.UnderlayProtocol_str || d.Name_str || "");
  const isUdp = proto.includes("L2TP") || proto.includes("UDP") || proto.includes("IPsec");
  if (isUdp) {
    if (d.ActiveNatTcp_u32 != null && Number(d.ActiveNatTcp_u32) > 0) {
      return `UDP (${d.ActiveNatTcp_u32} ${t("active sessions")})`;
    }
    return t("UDP tunnel");
  }
  const num = d.NumTcpConnections_u32 ?? d.CurrentNumTcp_u32 ?? 0;
  const max = d.MaxTcpConnections_u32 ?? d.MaxNumTcp_u32 ?? 0;
  if (num === 0 && max === 0) return "—";
  return `${num} / ${max}`;
}

function parseClientInfo(d: Wire) {
  const prod = String(d.ClientProductName_str || "").trim();
  const osName = String(d.ClientOsName_str || "").trim();
  const osVer = String(d.ClientOsVer_str || "").trim();
  const host = String(d.ClientHostname_str || d.RealHostname_str || d.Hostname_str || "").trim();
  const hostLower = host.toLowerCase();

  const isDull = (s: string) => !s || /^[-—\s]+$/.test(s) || s.toLowerCase() === "unknown" || s.toLowerCase() === "none";

  // Deduce real operating system
  let os = osName;
  if (isDull(os) || os.toLowerCase().includes("l2tp")) {
    if (hostLower.includes("iphone")) {
      os = "Apple iOS (iPhone)";
    } else if (hostLower.includes("ipad")) {
      os = "Apple iPadOS";
    } else if (hostLower.includes("macbook") || hostLower.includes("imac") || hostLower.includes("mac")) {
      os = "Apple macOS";
    } else if (
      hostLower.includes("android") ||
      /^sm-[a-z0-9]+/i.test(host) ||
      /^[a-z]\d{2}[a-z]?$/i.test(host) ||
      /^(redmi|xiaomi|oppo|vivo|oneplus|pixel)/i.test(host)
    ) {
      os = host ? `Android (${host})` : "Android";
    } else if (
      prod.toLowerCase().includes("microsoft") ||
      osName.toLowerCase().includes("microsoft") ||
      hostLower.startsWith("desktop-") ||
      hostLower.startsWith("laptop-")
    ) {
      os = "Windows";
    } else {
      os = !isDull(osName) ? osName : "—";
    }
  }

  if (!isDull(osVer)) {
    os = `${os} ${osVer}`.trim();
  }

  // Ensure no trailing hyphens/dashes remain on os
  os = os.replace(/\s*[-—]+\s*$/, "").trim() || "—";

  // Deduce client software
  let app = !isDull(prod) ? prod : "—";
  if (d.ClientProductVer_u32 != null) {
    const v = Number(d.ClientProductVer_u32);
    if (v > 0 && v < 10000) {
      app += ` v${(v / 100).toFixed(2)}`;
    }
  }
  if (d.ClientProductBuild_u32 != null) {
    const b = Number(d.ClientProductBuild_u32);
    if (b > 0 && b < 100000) {
      app += ` (Build ${b})`;
    }
  }

  return { os, app };
}

export function HubSessions({ hub }: { hub: string }) {
  const [sessions, setSessions] = useState<Wire[] | null>(null);
  const [query, setQuery] = useState("");
  const [open, setOpen] = useState<string | null>(null);
  const [killing, setKilling] = useState<Wire | null>(null);
  const { guard } = useToast();

  const load = useCallback(async () => {
    try {
      const [r, ipRes] = await Promise.all([
        api.sessions(hub).catch(() => null),
        api.ipTable(hub).catch(() => null),
      ]);
      const list = (r?.SessionList as Wire[]) ?? [];
      const ipTable = (ipRes?.IpTable as Wire[]) ?? [];
      const ipMap = new Map<string, string>();
      for (const entry of ipTable) {
        const ip = String(entry.IpAddress_ip || "").trim();
        if (entry.SessionName_str && /^(\d{1,3}\.){3}\d{1,3}$/.test(ip)) {
          const prev = ipMap.get(String(entry.SessionName_str));
          if (!prev) {
            ipMap.set(String(entry.SessionName_str), ip);
          }
        }
      }
      for (const s of list) {
        s.VirtualIP_ip = filterIpv4(s.VirtualIP_ip) || ipMap.get(String(s.Name_str)) || "";
      }
      setSessions(list);
    } catch {
      setSessions([]);
    }
  }, [hub]);
  usePoll(load, "live", [hub]);

  const q = query.trim().toLowerCase();
  const filtered = sessions?.filter(
    (s) =>
      !q ||
      [s.Name_str, s.Username_str, s.Hostname_str, s.ClientIP_ip, s.VirtualIP_ip, s.RealHostname_str]
        .filter(Boolean)
        .some((v) => String(v).toLowerCase().includes(q)),
  );

  return (
    <>
      <SectionTitle
        count={sessions?.length}
        actions={<SearchBox value={query} onChange={setQuery} placeholder={t("user, host, IP…")} />}
      >
        {t("Sessions")}
      </SectionTitle>

      {filtered == null ? (
        <LoadingBlock label="loading sessions" />
      ) : filtered.length === 0 ? (
        <Empty title={q ? "nothing matches" : "nobody is connected"}>
          {q ? "No session matches that search." : "Sessions appear here live as clients connect."}
        </Empty>
      ) : (
        <SessionsTable
          sessions={filtered}
          userLink={(u) => navigate(`/hub/${seg(hub)}/user/${seg(u)}`)}
          onOpen={(s) => setOpen(String(s.Name_str))}
          onKill={(s) => setKilling(s)}
        />
      )}

      {open && <SessionSheet hub={hub} name={open} onClose={() => setOpen(null)} />}
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
            await api.killSession(hub, String(killing.Name_str));
            await load();
          }}
        />
      )}
    </>
  );
}

/** Shared by the hub page and the user page. */
export function SessionsTable({
  sessions,
  onOpen,
  onKill,
  userLink,
}: {
  sessions: Wire[];
  onOpen: (s: Wire) => void;
  onKill: (s: Wire) => void | Promise<unknown>;
  userLink?: (username: string) => void;
}) {
  return (
    <>
      {/* desktop table */}
      <div className="only-desktop-b">
        <div className="card tcard">
          <div className="tscroll">
            <table className="dtable">
              <thead>
                <tr>
                  <th>{t("Session")}</th>
                  <th>{t("User")}</th>
                  <th>{t("Kind")}</th>
                  <th>{t("IP / Host")}</th>
                  <th style={{ width: 80 }}>{t("TCP")}</th>
                  <th>{t("Packets")}</th>
                  <th>{t("Transfer")}</th>
                  <th>{t("Started")}</th>
                  <th className="tact" style={{ width: 120 }} aria-label={t("Actions")} />
                </tr>
              </thead>
              <tbody>
                {sessions.map((s) => {
                  const kind = SESSION_TYPE(s);
                  const system = kind !== "client";
                  return (
                    <tr key={String(s.Name_str)} className="clickable" onClick={() => onOpen(s)}>
                      <td className="tmono">{String(s.Name_str)}</td>
                      <td>
                        {s.Username_str && userLink && !system ? (
                          <button
                            className="linkish mono"
                            onClick={(e) => {
                              e.stopPropagation();
                              userLink(String(s.Username_str));
                            }}
                          >
                            {String(s.Username_str)}
                          </button>
                        ) : (
                          <span className="tmono">{String(s.Username_str || "—")}</span>
                        )}
                      </td>
                      <td>
                        <Pill kind={system ? "idle" : "ok"} label={kind} />
                      </td>
                      <td className="tmono">
                        {s.VirtualIP_ip ? (
                          <div>
                            <span className="chip chip--ok" style={{ padding: "0 5px", fontSize: "10px", marginRight: 4 }}>
                              {t("LAN")}
                            </span>
                            <span>{String(s.VirtualIP_ip)}</span>
                          </div>
                        ) : null}
                        {s.ClientIP_ip ? (
                          <div className={s.VirtualIP_ip ? "tsub" : ""} style={{ marginTop: s.VirtualIP_ip ? 2 : 0 }}>
                            <span className="chip" style={{ padding: "0 5px", fontSize: "10px", marginRight: 4 }}>
                              {t("WAN")}
                            </span>
                            <span>{String(s.ClientIP_ip)}</span>
                          </div>
                        ) : null}
                        {cleanHost(s) !== "—" ? (
                          <div className="tsub" style={{ marginTop: 2, color: "var(--muted)" }}>
                            {cleanHost(s)}
                          </div>
                        ) : null}
                        {!s.VirtualIP_ip && !s.ClientIP_ip && !s.Hostname_str ? "—" : null}
                      </td>
                      <td className="tmono">{formatTcpConnections(s, t)}</td>
                      <td className="tmono">{formatCount(Number(s.PacketNum_u64))}</td>
                      <td className="tmono">{formatBytes(Number(s.PacketSize_u64))}</td>
                      <td className="tmono">{timeAgo(s.CreatedTime_dt as string)}</td>
                      <td className="tact">
                        {!system && (
                          <button
                            className="btn btn--sm btn--danger"
                            onClick={(e) => {
                              e.stopPropagation();
                              void onKill(s);
                            }}
                          >
                            <IconClose size={13} /> {t("Kill")}
                          </button>
                        )}
                      </td>
                    </tr>
                  );
                })}
              </tbody>
            </table>
          </div>
        </div>
      </div>

      {/* mobile cards */}
      <div className="rows only-mobile-b">
        {sessions.map((s) => {
          const kind = SESSION_TYPE(s);
          const system = kind !== "client";
          return (
            <button key={String(s.Name_str)} className="row" onClick={() => onOpen(s)}>
              <div className="row__main">
                <div className="row__name">
                  <span className="mono truncate">{String(s.Username_str || s.Name_str)}</span>
                  <Pill kind={system ? "idle" : "ok"} label={kind} />
                </div>
                <div className="spec">
                  {s.VirtualIP_ip ? <span className="chip"><i>{t("LAN")}</i>{String(s.VirtualIP_ip)}</span> : null}
                  {s.ClientIP_ip ? <span className="chip"><i>{t("WAN")}</i>{String(s.ClientIP_ip)}</span> : null}
                  <span className="chip"><i>{t("data")}</i>{formatBytes(Number(s.PacketSize_u64))}</span>
                  <span className="chip"><i>{t("since")}</i>{timeAgo(s.CreatedTime_dt as string)}</span>
                </div>
              </div>
              <div className="row__side">
                {!system && (
                  <button
                    className="btn btn--sm btn--danger"
                    onClick={(e) => {
                      e.stopPropagation();
                      void onKill(s);
                    }}
                  >
                    Kill
                  </button>
                )}
              </div>
            </button>
          );
        })}
      </div>
    </>
  );
}

/** Everything the server knows about one session, in a sheet. */
export function SessionSheet({ hub,
  name,
  onClose,
}: {
    hub: string;
  name: string;
  onClose: () => void;
}) {
  const [detail, setDetail] = useState<Wire | null>(null);
  const [error, setError] = useState<string | null>(null);

  useEffect(() => {
    let alive = true;
    api
      .sessionStatus(hub, name)
      .then((d) => alive && setDetail(d))
      .catch((e) => alive && setError(e instanceof Error ? e.message : String(e)));
    return () => {
      alive = false;
    };
  }, [hub, name]);

    return (
      <Sheet title={t("Session")} subtitle={name} onClose={onClose} wide>
        {error && <div className="alert alert--err">{error}</div>}
        {!detail && !error && <LoadingBlock />}
        {detail && (() => {
          const clientInfo = parseClientInfo(detail);
          return (
            <div className="session-panel">
              {/* Group 1: Network & Address */}
              <div className="session-card">
                <div className="session-card__head">
                  <span className="session-card__dot" style={{ background: "var(--accent-strong, #3b82f6)" }} />
                  {t("Network & Address")}
                </div>
                <div className="session-grid-4">
                  <div>
                    <div className="micro">{t("user")}</div>
                    <div className="mono" style={{ fontSize: "var(--t-data)", fontWeight: 600 }}>{String(detail.Username_str || "—")}</div>
                  </div>
                  <div>
                    <div className="micro">{t("client IP")}</div>
                    <div className="mono" style={{ fontSize: "var(--t-data)" }}>{String(detail.Client_Ip_Address_ip || "—")}</div>
                  </div>
                  <div>
                    <div className="micro">{t("virtual IP")}</div>
                    <div className="mono" style={{ fontSize: "var(--t-data)", color: "var(--ok)" }}>
                      {detail.VirtualIP_ip ? (
                        <span style={{ display: "inline-flex", alignItems: "center", gap: 4 }}>
                          <span className="chip chip--ok" style={{ padding: "0 5px", fontSize: "10px" }}>{t("LAN")}</span>
                          {filterIpv4(detail.VirtualIP_ip)}
                        </span>
                      ) : "—"}
                    </div>
                  </div>
                  <div>
                    <div className="micro">{t("client host")}</div>
                    <div className="mono" style={{ fontSize: "var(--t-data)" }}>{cleanHost(detail)}</div>
                  </div>
                </div>
              </div>

              {/* Group 2: Connection & Protocol */}
              <div className="session-card">
                <div className="session-card__head">
                  <span className="session-card__dot" style={{ background: "#818cf8" }} />
                  {t("Connection & Protocol")}
                </div>
                <div className="session-grid-4">
                  <div>
                    <div className="micro">{t("protocol")}</div>
                    <div className="mono" style={{ fontSize: "var(--t-data)" }}>{String(detail.UnderlayProtocol_str || "—")}</div>
                  </div>
                  <div>
                    <div className="micro">{t("cipher")}</div>
                    <div className="mono" style={{ fontSize: "var(--t-data)" }}>{String(detail.CipherName_str || "plain")}</div>
                  </div>
                  <div>
                    <div className="micro">{t("TCP connections")}</div>
                    <div className="mono" style={{ fontSize: "var(--t-data)" }}>{formatTcpConnections(detail, t)}</div>
                  </div>
                  <div>
                    <div className="micro">{t("UDP / Compress")}</div>
                    <div className="mono" style={{ fontSize: "var(--t-data)" }}>
                      {(detail.IsUsingUdpAcceleration_bool ? t("in use") : detail.IsUdpAccelerationEnabled_bool ? "enabled" : "off") + " / " + (detail.UseCompress_bool ? t("yes") : "no")}
                    </div>
                  </div>
                </div>
              </div>

              {/* Group 3: Client Info & Traffic */}
              <div className="session-grid-2">
                {/* Client Info */}
                <div className="session-card">
                  <div className="session-card__head">
                    <span className="session-card__dot" style={{ background: "#22d3ee" }} />
                    {t("Client Environment")}
                  </div>
                  <div style={{ display: "grid", gridTemplateColumns: "repeat(2, minmax(0, 1fr))", gap: "var(--s3)" }}>
                    <div>
                      <div className="micro">{t("started")}</div>
                      <div className="mono" style={{ fontSize: "var(--t-data)" }}>{formatDate(detail.StartTime_dt as string)}</div>
                    </div>
                    <div>
                      <div className="micro">{t("half connection")}</div>
                      <div className="mono" style={{ fontSize: "var(--t-data)" }}>{detail.HalfConnection_bool ? t("yes") : "no"}</div>
                    </div>
                    <div>
                      <div className="micro">{t("Client software")}</div>
                      <div className="mono truncate" title={clientInfo.app} style={{ fontSize: "var(--t-data)" }}>
                        {clientInfo.app}
                      </div>
                    </div>
                    <div>
                      <div className="micro">{t("Client OS")}</div>
                      <div className="mono truncate" title={clientInfo.os} style={{ fontSize: "var(--t-data)" }}>
                        {clientInfo.os}
                      </div>
                    </div>
                  </div>
                </div>

                {/* Traffic Statistics */}
                <div className="session-card" style={{ display: "flex", flexDirection: "column", justifyContent: "space-between" }}>
                  <div className="session-card__head">
                    <span className="session-card__dot" style={{ background: "#fbbf24" }} />
                    {t("Traffic Statistics")}
                  </div>
                  <div style={{ display: "grid", gridTemplateColumns: "1fr 1fr", gap: "var(--s3)", paddingBlock: "var(--s1)" }}>
                    <div className="session-stat-card">
                      <div className="micro" style={{ color: "var(--accent-strong, #3b82f6)", marginBottom: 4, display: "flex", alignItems: "center", gap: 4 }}>
                        <span>↑</span> {t("Sent (Upload)")}
                      </div>
                      <div className="mono" style={{ fontSize: "1.1rem", fontWeight: 700 }}>
                        {formatBytes(Number(detail.TotalSendSizeReal_u64 ?? detail.TotalSendSize_u64))}
                      </div>
                    </div>
                    <div className="session-stat-card">
                      <div className="micro" style={{ color: "var(--ok, #10b981)", marginBottom: 4, display: "flex", alignItems: "center", gap: 4 }}>
                        <span>↓</span> {t("Received (Download)")}
                      </div>
                      <div className="mono" style={{ fontSize: "1.1rem", fontWeight: 700 }}>
                        {formatBytes(Number(detail.TotalRecvSizeReal_u64 ?? detail.TotalRecvSize_u64))}
                      </div>
                    </div>
                  </div>
                </div>
              </div>
            </div>
          );
        })()}
      </Sheet>
    );
}
