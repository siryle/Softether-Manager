"use client";

import { t } from "../../lib/i18n";
import { useCallback, useEffect, useState } from "react";
import { ConfirmSheet, Empty, KV, LoadingBlock, SearchBox, SectionTitle, usePoll } from "../../components/bits";
import { api, type Wire } from "../../lib/api";
import { navigate, seg } from "../../lib/router";
import { SESSION_TYPE } from "../../lib/se";
import { useToast } from "../../lib/toast";
import { formatBytes, formatCount, formatDate, formatIPLocation, hasIPLocation, timeAgo } from "../../lib/util";
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

export function filterIpv4(ipStr?: any): string {
  if (!ipStr) return "";
  const parts = String(ipStr).split(/[\s,]+/);
  const v4 = parts.filter((p) => /^(\d{1,3}\.){3}\d{1,3}$/.test(p.trim()));
  return v4.join(", ");
}

export function cleanHost(d: Wire): string {
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

export function formatTcpConnections(d: Wire, t: (s: string) => string): string {
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

export function parseClientInfo(d: Wire) {
  const prod = String(d.ClientProductName_str || "").trim();
  const isDull = (s: string) => !s || /^[-—\s]+$/.test(s) || s.toLowerCase() === "unknown" || s.toLowerCase() === "none";

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

  return { app };
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
      const clientList = list.filter((s) => SESSION_TYPE(s) === "client");
      setSessions(clientList);
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
        {t("Online clients")}
      </SectionTitle>

      {filtered == null ? (
        <LoadingBlock label="loading sessions" />
      ) : filtered.length === 0 ? (
        <Empty title={q ? "nothing matches" : "nobody is connected"}>
          {q
            ? "No client, host or IP matches that search."
            : "No clients are connected right now."}
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
                  <th style={{ width: 80 }}>{t("Connection count")}</th>
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
                          <div>
                            <button
                              className="linkish mono bold"
                              onClick={(e) => {
                                e.stopPropagation();
                                userLink(String(s.Username_str));
                              }}
                            >
                              {String(s.Username_str)}
                            </button>
                            {s.Realname_utf ? <span style={{ marginLeft: 6, fontSize: "12px", color: "var(--fg)" }}>({String(s.Realname_utf)})</span> : null}
                            {s.Note_utf ? <div className="tsub truncate" style={{ maxWidth: 160, color: "var(--muted)" }}>{String(s.Note_utf)}</div> : null}
                          </div>
                        ) : (
                          <span className="tmono">{String(s.Username_str || "—")}</span>
                        )}
                      </td>
                      <td>
                        <Pill kind={system ? "idle" : "ok"} label={kind} />
                      </td>
                      <td className="tmono">
                        <EndpointCell
                          virtualIp={s.VirtualIP_ip}
                          clientIp={s.ClientIP_ip}
                          location={hasIPLocation(s.IpLocation_str, s.IpIsp_str) ? formatIPLocation(s.IpLocation_str, s.IpIsp_str) : ""}
                          host={cleanHost(s)}
                        />
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
                  {s.Realname_utf ? <span style={{ fontSize: "12px", color: "var(--muted)" }}>({String(s.Realname_utf)})</span> : null}
                  <Pill kind={system ? "idle" : "ok"} label={kind} />
                </div>
                {s.Note_utf ? <div className="tsub truncate" style={{ fontSize: "12px", color: "var(--muted)", marginBottom: 4 }}>{String(s.Note_utf)}</div> : null}
                <div className="spec">
                  {s.VirtualIP_ip ? <span className="chip mono" style={{ color: "var(--ok)" }}>{String(s.VirtualIP_ip)}</span> : null}
                  {s.ClientIP_ip ? (
                    <span className="chip">
                      <i>{t("WAN")}</i>
                      {String(s.ClientIP_ip)}
                      {s.IpLocation_str ? ` · ${String(s.IpLocation_str)}` : ""}
                    </span>
                  ) : null}
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

export function EndpointCell({
  virtualIp,
  clientIp,
  location,
  host,
}: {
  virtualIp?: unknown;
  clientIp?: unknown;
  location?: string;
  host?: string;
}) {
  const lan = virtualIp ? filterIpv4(virtualIp) || String(virtualIp).trim() : "";
  const wan = clientIp ? String(clientIp).trim() : "";
  const cleanHostname = host && host !== "—" ? host.trim() : "";
  const loc = location ? location.trim() : "";

  if (!lan && !wan && !cleanHostname) return <>—</>;

  const showPrimaryRow = Boolean(lan || (cleanHostname && !wan));
  return (
    <div className="endpoint">
      {showPrimaryRow ? (
        <div className="endpoint__row">
          {lan ? <span className="endpoint__lan">{lan}</span> : null}
          {lan && cleanHostname ? <span className="endpoint__sep" aria-hidden="true">·</span> : null}
          {cleanHostname ? (
            <span className="endpoint__host" title={cleanHostname}>
              {cleanHostname}
            </span>
          ) : null}
        </div>
      ) : null}
      {wan ? (
        <div className="endpoint__row">
          <span className={lan ? "endpoint__wan" : "endpoint__wan--solo"}>{wan}</span>
          {loc ? <span className="endpoint__loc">{loc}</span> : null}
        </div>
      ) : null}
      {!lan && wan && cleanHostname ? (
        <div className="endpoint__row">
          <span className="endpoint__host" title={cleanHostname}>
            {cleanHostname}
          </span>
        </div>
      ) : null}
    </div>
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
                    <div className="mono" style={{ fontSize: "var(--t-data)", fontWeight: 600 }}>
                      {String(detail.Username_str || "—")}
                      {detail.Realname_utf ? <span style={{ marginLeft: 6, fontWeight: "normal", color: "var(--fg)" }}>({String(detail.Realname_utf)})</span> : null}
                    </div>
                    {detail.Note_utf ? <div className="tsub truncate" style={{ fontSize: "11px", color: "var(--muted)", marginTop: 2 }}>{String(detail.Note_utf)}</div> : null}
                  </div>
                  <div>
                    <div className="micro">{t("client IP")}</div>
                    <div className="mono" style={{ fontSize: "var(--t-data)" }}>
                      {String(detail.Client_Ip_Address_ip || "—")}
                      {hasIPLocation(detail.IpLocation_str, detail.IpIsp_str) ? (
                        <div style={{ marginTop: 3 }}>
                          <span className="endpoint__loc">
                            {formatIPLocation(detail.IpLocation_str, detail.IpIsp_str)}
                          </span>
                        </div>
                      ) : null}
                    </div>
                  </div>
                  <div>
                    <div className="micro">{t("virtual IP")}</div>
                    <div className="mono" style={{ fontSize: "var(--t-data)", color: "var(--ok)" }}>
                      {detail.VirtualIP_ip ? filterIpv4(detail.VirtualIP_ip) || "—" : "—"}
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
                    <div className="micro">{t("Connection count")}</div>
                    <div className="mono" style={{ fontSize: "var(--t-data)" }}>{formatTcpConnections(detail, t)}</div>
                  </div>
                  <div>
                    <div className="micro">{t("UDP / Compress")}</div>
                    <div className="mono" style={{ fontSize: "var(--t-data)" }}>
                      {(detail.IsUsingUdpAcceleration_bool ? t("in use") : detail.IsUdpAccelerationEnabled_bool ? t("enabled") : t("off")) + " / " + (detail.UseCompress_bool ? t("yes") : t("no"))}
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
                      <div className="mono" style={{ fontSize: "var(--t-data)" }}>{detail.HalfConnection_bool ? t("yes") : t("no")}</div>
                    </div>
                    <div style={{ gridColumn: "1 / -1" }}>
                      <div className="micro">{t("Client software")}</div>
                      <div className="mono" title={clientInfo.app} style={{ fontSize: "var(--t-data)", wordBreak: "break-word", lineHeight: 1.4 }}>
                        {clientInfo.app}
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
