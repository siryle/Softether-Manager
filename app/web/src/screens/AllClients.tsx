"use client";

import { useCallback, useMemo, useRef, useState } from "react";
import { ConfirmSheet, Empty, LoadingBlock, PageHead, SearchBox, usePoll } from "../components/bits";
import { api, type Wire } from "../lib/api";
import { useI18n } from "../lib/i18n";
import { useServer } from "../lib/server";
import { Link, navigate, seg } from "../lib/router";
import { useToast } from "../lib/toast";
import { formatBytes, formatIPLocation, hasIPLocation, timeAgo } from "../lib/util";
import { IconClose, IconRefresh, IconReport } from "../ui/Icon";
import { Pill } from "../ui/Status";
import { cleanHost, EndpointCell, parseClientInfo, SessionSheet } from "./hub/HubSessions";

/**
 * Aggregated online clients view across all Virtual Hubs.
 * Operators monitor who is connected on any hub right now and can inspect
 * details or disconnect clients across the entire server.
 */
export function AllClients() {
  const { t } = useI18n();
  const { setClientCount } = useServer();
  const [data, setData] = useState<{ hubs: string[]; clients: Wire[]; errors: { hub: string; error: string }[] } | null>(null);
  const [query, setQuery] = useState("");
  const [openSession, setOpenSession] = useState<{ hub: string; name: string } | null>(null);
  const [killing, setKilling] = useState<Wire | null>(null);
  const [refreshing, setRefreshing] = useState(false);
  const inFlight = useRef(false);
  const { guard } = useToast();

  const load = useCallback(async () => {
    if (inFlight.current) return;
    inFlight.current = true;
    try {
      const res = await api.allClients().catch(() => null);
      setData(res);
      if (res && Array.isArray(res.clients)) {
        setClientCount(res.clients.length);
      }
    } finally {
      inFlight.current = false;
      setRefreshing(false);
    }
  }, [setClientCount]);
  usePoll(load, "list", []);

  const filtered = useMemo(() => {
    if (!data) return null;
    const q = query.trim().toLowerCase();
    if (!q) return data.clients;
    return data.clients.filter((s) =>
      [
        s.Name_str,
        s.Username_str,
        s.Realname_utf,
        s.GroupName_str,
        s.Note_utf,
        s.HubName_str,
        s.ClientIP_ip,
        s.VirtualIP_ip,
        s.Hostname_str,
        s.RealHostname_str,
        s.ClientProductName_str,
        s.UnderlayProtocol_str,
      ]
        .filter(Boolean)
        .some((v) => String(v).toLowerCase().includes(q)),
    );
  }, [data, query]);

  return (
    <div className="page">
      <PageHead
        title={t("Online clients")}
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
            <Link to="/audit-report" className="btn btn--sm" title={t("Audit report")}>
              <IconReport size={14} /> <span className="btn__label">{t("Audit report")}</span>
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

      {filtered === null ? (
        <LoadingBlock label="loading sessions" />
      ) : filtered.length === 0 ? (
        <Empty title={query ? t("nothing matches") : t("no online clients")}>
          {query
            ? t("No client, host or IP matches that search.")
            : t("No clients are connected right now.")}
        </Empty>
      ) : (
        <>
          {/* Desktop Table View */}
          <div className="only-desktop-b">
            <div className="card tcard">
              <div className="tscroll">
                <table className="dtable">
                  <thead>
                    <tr>
                      <th>{t("Session / Client")}</th>
                      <th>{t("User")}</th>
                      <th>{t("Virtual HUB")}</th>
                      <th>{t("IP / Host")}</th>
                      <th>{t("Packets")}</th>
                      <th>{t("Transfer")}</th>
                      <th>{t("Started")}</th>
                      <th className="tact" style={{ width: 100 }} aria-label={t("Actions")} />
                    </tr>
                  </thead>
                  <tbody>
                    {filtered.map((s) => {
                      const clientInfo = parseClientInfo(s);
                      const hub = String(s.HubName_str || "");
                      const name = String(s.Name_str || "");
                      return (
                        <tr
                          key={`${hub}:${name}`}
                          className="clickable"
                          onClick={() => setOpenSession({ hub, name })}
                        >
                          <td className="tmono">
                            <div className="bold">{name}</div>
                            {clientInfo.app && clientInfo.app !== "—" && (
                              <div className="tsub truncate" style={{ maxWidth: 220, fontSize: "11px", color: "var(--text-muted)" }}>
                                {clientInfo.app}
                              </div>
                            )}
                          </td>
                          <td>
                            {s.Username_str ? (
                              <div>
                                <Link
                                  to={`/hub/${seg(hub)}/user/${seg(String(s.Username_str))}`}
                                  className="linkish mono bold"
                                  onClick={(e) => e.stopPropagation()}
                                >
                                  {String(s.Username_str)}
                                </Link>
                                {s.Realname_utf ? (
                                  <span style={{ marginLeft: 6, fontSize: "12px", color: "var(--fg)" }}>
                                    ({String(s.Realname_utf)})
                                  </span>
                                ) : null}
                                {s.Note_utf ? (
                                  <div className="tsub truncate" style={{ maxWidth: 160, color: "var(--muted)" }}>
                                    {String(s.Note_utf)}
                                  </div>
                                ) : null}
                              </div>
                            ) : (
                              <span className="tmono">—</span>
                            )}
                          </td>
                          <td>
                            <Link
                              to={`/hub/${seg(hub)}`}
                              className="chip mono"
                              onClick={(e) => e.stopPropagation()}
                              style={{ fontWeight: 600 }}
                            >
                              {hub}
                            </Link>
                          </td>
                          <td className="tmono">
                            <EndpointCell
                              virtualIp={s.VirtualIP_ip}
                              clientIp={s.ClientIP_ip}
                              location={hasIPLocation(s.IpLocation_str, s.IpIsp_str) ? formatIPLocation(s.IpLocation_str, s.IpIsp_str) : ""}
                              host={cleanHost(s)}
                            />
                          </td>
                          <td className="tmono">{Number(s.PacketNum_u64 ?? s.NumPacket_u32 ?? 0).toLocaleString()}</td>
                          <td className="tmono">{formatBytes(Number(s.PacketSize_u64 ?? 0))}</td>
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
              return (
                <button
                  key={`${hub}:${name}`}
                  className="row"
                  onClick={() => setOpenSession({ hub, name })}
                >
                  <div className="row__main">
                    <div className="row__name">
                      <span className="mono truncate">{String(s.Username_str || name)}</span>
                      {s.Realname_utf ? (
                        <span style={{ fontSize: "12px", color: "var(--muted)" }}>({String(s.Realname_utf)})</span>
                      ) : null}
                      <span className="chip mono" style={{ fontSize: "10px", marginLeft: 4 }}>{hub}</span>
                    </div>
                    {s.Note_utf ? (
                      <div className="tsub truncate" style={{ fontSize: "12px", color: "var(--muted)", marginBottom: 4 }}>
                        {String(s.Note_utf)}
                      </div>
                    ) : null}
                    <div className="spec">
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

      {/* Session Details Modal */}
      {openSession && (
        <SessionSheet
          hub={openSession.hub}
          name={openSession.name}
          onClose={() => setOpenSession(null)}
        />
      )}

      {/* Disconnect Confirmation Sheet */}
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
