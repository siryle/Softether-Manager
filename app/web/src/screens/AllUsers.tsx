"use client";

import { useCallback, useMemo, useState } from "react";
import {
  Empty,
  LoadingBlock,
  PageHead,
  SearchBox,
  SortTh,
  sortRows,
  usePoll,
  useSort,
} from "../components/bits";
import {
  QuotaCell,
  meteredBytes,
  netBytes,
  quotaKey,
  quotaSortValue,
  useQuotaIndex,
} from "../components/QuotaCard";
import { UserSheet } from "../components/UserSheet";
import { VpnFileSheet } from "../components/VpnFileSheet";
import {
  WireGuardUserActionSheet,
  type WireGuardUserAction,
  downloadWireGuardPeerConfig,
  groupWireGuardPeers,
  wireguardPeerKey,
} from "../components/WireGuardPeerSheet";
import { api, useServerApi, type Wire, type WireGuardPeer, type WireGuardState } from "../lib/api";
import { navigate, seg } from "../lib/router";
import { AUTH_TYPES, isNever, userBytes, userSortValue } from "../lib/se";
import { formatBytes, formatCount, formatDate, timeAgo } from "../lib/util";
import { useI18n, t } from "../lib/i18n";
import { useToast } from "../lib/toast";
import { IconChevron, IconPlus, IconQrCode, IconVpnDownload, IconWireGuardDownload } from "../ui/Icon";
import { UserStatePill } from "./hub/HubUsers";

/**
 * Every user on the server, across all Virtual Hubs -- the one table an
 * operator scans when the question is "who exists here", not "what is in
 * this hub". Rows link into the user's own page; creating asks which hub
 * the user should live in.
 */
export function AllUsers() {
  const { t } = useI18n();
  const serverApi = useServerApi();
  const { guard } = useToast();
  const [data, setData] = useState<{ hubs: string[]; users: Wire[]; errors: { hub: string; error: string }[] } | null>(null);
  const [wg, setWg] = useState<WireGuardState | null>(null);
  const [query, setQuery] = useState("");
  const [statusFilter, setStatusFilter] = useState<"all" | "online" | "warn" | "expired">("all");
  const [hubFilter, setHubFilter] = useState<string>("ALL");
  const [creating, setCreating] = useState(false);
  const [createHub, setCreateHub] = useState("");
  const [vpnFor, setVpnFor] = useState<{ hub: string; name: string } | null>(null);
  const [wgAction, setWgAction] = useState<WireGuardUserAction | null>(null);
  const [downloadingWgKey, setDownloadingWgKey] = useState<string | null>(null);
  const quotas = useQuotaIndex([]);
  const { sort, toggle } = useSort();
  const quotaOf = (u: Wire) =>
    quotas.get(quotaKey("user", String(u.HubName_str), String(u.Name_str)));
  // Transfer is the lifetime counter less whatever a reset put behind us, so
  // the column and the meter next to it are the same number.
  const movedBy = (u: Wire) => netBytes(userBytes(u), quotaOf(u));
  const isExpired = (u: Wire) => {
    const expires = u.Expires_dt ?? u.ExpireTime_dt;
    return !isNever(expires as string) && new Date(expires as string).getTime() < Date.now();
  };
  const isQuotaWarn = (u: Wire) => {
    const q = quotaOf(u);
    if (!q?.has_limit) return false;
    if (q.blocked) return true;
    const used = meteredBytes(movedBy(u), q.metric);
    return q.limit_bytes > 0 && used >= q.limit_bytes * 0.85;
  };

  const load = useCallback(async () => {
    setData(await api.allUsers().catch(() => null));
  }, []);
  usePoll(load, "list", []);

  const loadWg = useCallback(async () => {
    setWg(await serverApi.wireguard().catch(() => null));
  }, [serverApi]);
  usePoll(loadWg, "list", []);

  const wgByUser = useMemo(() => groupWireGuardPeers(wg), [wg]);
  const handleQrWg = (hub: string, user: string, peers: WireGuardPeer[]) => {
    if (!wg || peers.length === 0) return;
    if (peers.length > 1) {
      setWgAction({ kind: "choose", hub, name: user, peers, mode: "qr" });
      return;
    }
    setWgAction({ kind: "qr", peer: peers[0] });
  };

  const handleDownloadWg = async (hub: string, user: string, peers: WireGuardPeer[]) => {
    if (!wg || peers.length === 0) return;
    if (peers.length > 1) {
      setWgAction({ kind: "choose", hub, name: user, peers, mode: "download" });
      return;
    }
    if (peers[0].has_private_key === false) {
      setWgAction({ kind: "edit", peer: peers[0] });
      return;
    }
    const key = wireguardPeerKey(hub, user);
    setDownloadingWgKey(key);
    await guard(
      () => downloadWireGuardPeerConfig(serverApi, wg, peers[0]),
      t("WireGuard configuration downloaded."),
    );
    setDownloadingWgKey(null);
  };

  const counts = useMemo(() => {
    if (!data) return { all: 0, online: 0, warn: 0, expired: 0 };
    let online = 0;
    let warn = 0;
    let expired = 0;
    for (const u of data.users) {
      if (Boolean(u.Online_bool) && !u.DenyAccess_bool && !isExpired(u)) online++;
      if (isQuotaWarn(u)) warn++;
      if (isExpired(u)) expired++;
    }
    return { all: data.users.length, online, warn, expired };
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [data, quotas]);

  const filtered = useMemo(() => {
    if (!data) return null;
    const q = query.trim().toLowerCase();
    const matched = data.users.filter((u) => {
      if (hubFilter !== "ALL" && String(u.HubName_str) !== hubFilter) return false;
      if (statusFilter === "online" && !(Boolean(u.Online_bool) && !u.DenyAccess_bool && !isExpired(u))) return false;
      if (statusFilter === "warn" && !isQuotaWarn(u)) return false;
      if (statusFilter === "expired" && !isExpired(u)) return false;
      if (!q) return true;
      return [u.Name_str, u.Realname_utf, u.GroupName_str, u.Note_utf, u.HubName_str]
        .filter(Boolean)
        .some((v) => String(v).toLowerCase().includes(q));
    });
    // Sorting is applied after the poll refreshes the list, so a chosen order
    // survives every refresh rather than snapping back to the server's.
    return sortRows(matched, sort, (u, key) => {
      if (key === "quota") return quotaSortValue(quotaOf(u), movedBy(u));
      // Transfer sorts by what the panel shows, which a reset changes.
      if (key === "transfer") {
        const moved = movedBy(u);
        return moved.send + moved.recv;
      }
      return userSortValue(u, key);
    });
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [data, query, statusFilter, hubFilter, sort, quotas]);

  const open = (u: Wire) => navigate(`/hub/${seg(String(u.HubName_str))}/user/${seg(String(u.Name_str))}`);

  return (
    <div className="page">
      <PageHead
        title={t("All Users")}
        sub={data ? t("{users} across {hubs} hubs", { users: data.users.length, hubs: data.hubs.length }) : t("Every user on this server.")}
        actions={
          <div className="users-toolbar">
            <SearchBox value={query} onChange={setQuery} placeholder={t("name, hub, group, note…")} />
            <button
              className="btn btn--primary"
              onClick={() => {
                setCreateHub(data?.hubs[0] ?? "");
                setCreating(true);
              }}
              disabled={!data || data.hubs.length === 0}
            >
              <IconPlus size={15} /> <span className="btn__label">{t("New user")}</span>
            </button>
          </div>
        }
      />

      {data && data.users.length > 0 && (
        <div className="filterbar">
          <div className="filterseg" role="tablist" aria-label={t("Status")}>
            {([
              { key: "all", label: "All", count: counts.all },
              { key: "online", label: "Online", count: counts.online, dot: "var(--brand-fg)" },
              { key: "warn", label: "Quota alert", count: counts.warn, dot: "var(--warn)" },
              { key: "expired", label: "Expired", count: counts.expired, dot: "var(--err)" },
            ] as const).map((item) => (
              <button
                key={item.key}
                type="button"
                role="tab"
                aria-selected={statusFilter === item.key}
                className={`filterseg__btn${statusFilter === item.key ? " on" : ""}`}
                onClick={() => setStatusFilter(item.key)}
              >
                {"dot" in item && <span className="filterseg__dot" style={{ background: item.dot }} />}
                <span>{t(item.label)}</span>
                <span className="filterseg__n">{item.count}</span>
              </button>
            ))}
          </div>

          {data.hubs.length > 1 && (
            <div className="filterhubs">
              <button
                type="button"
                className={`filterhubs__pill${hubFilter === "ALL" ? " on" : ""}`}
                onClick={() => setHubFilter("ALL")}
              >
                {t("All")}
              </button>
              {data.hubs.map((h) => (
                <button
                  key={h}
                  type="button"
                  className={`filterhubs__pill${hubFilter === h ? " on" : ""}`}
                  onClick={() => setHubFilter(h)}
                >
                  {h}
                </button>
              ))}
            </div>
          )}
        </div>
      )}

      {data?.errors.map((e) => (
        <div key={e.hub} className="alert alert--warn">
          {t("Hub {hub} could not be listed: {error}", { hub: e.hub, error: e.error })}
        </div>
      ))}

      {filtered === null ? (
        <LoadingBlock label="loading users" />
      ) : filtered.length === 0 ? (
        <Empty title={query ? "nothing matches" : "no users yet"}>
          {query
            ? "No user, hub, group or note matches that search."
            : "A user is one VPN identity, living in one Virtual Hub."}
        </Empty>
      ) : (
        <>
          {/* desktop table */}
          <div className="only-desktop-b">
            <div className="card tcard">
              <div className="tscroll">
                <table className="dtable">
                  <thead>
                    <tr>
                      <SortTh sortKey="user" sort={sort} onSort={toggle}>{t("User")}</SortTh>
                      <SortTh sortKey="hub" sort={sort} onSort={toggle}>{t("Hub")}</SortTh>
                      <SortTh sortKey="status" sort={sort} onSort={toggle} style={{ width: 110 }}>{t("Status")}</SortTh>
                      <SortTh sortKey="group" sort={sort} onSort={toggle}>{t("Group")}</SortTh>
                      <SortTh sortKey="auth" sort={sort} onSort={toggle}>{t("Auth")}</SortTh>
                      <SortTh sortKey="login" sort={sort} onSort={toggle}>{t("Last login")}</SortTh>
                      <SortTh sortKey="logins" sort={sort} onSort={toggle} style={{ width: 90 }}>{t("Logins")}</SortTh>
                      <SortTh sortKey="transfer" sort={sort} onSort={toggle}>{t("Transfer")}</SortTh>
                      <SortTh sortKey="quota" sort={sort} onSort={toggle}>{t("Limit")}</SortTh>
                      <SortTh sortKey="expires" sort={sort} onSort={toggle}>{t("Expires")}</SortTh>
                      <th className="tact" style={{ width: 148 }} aria-label={t("Actions")} />
                    </tr>
                  </thead>
                  <tbody>
                    {filtered.map((u) => {
                      const bytes = movedBy(u);
                      const expires = u.Expires_dt ?? u.ExpireTime_dt;
                      const hubName = String(u.HubName_str);
                      const userName = String(u.Name_str);
                      const wgKey = wireguardPeerKey(hubName, userName);
                      const wgPeers = wgByUser.get(wgKey) ?? [];
                      return (
                        <tr key={`${u.HubName_str}/${u.Name_str}`} className="clickable" onClick={() => open(u)}>
                          <td>
                            <span className="tname mono">{String(u.Name_str)}</span>
                            {u.Realname_utf ? <span className="tsub">{String(u.Realname_utf)}</span> : null}
                          </td>
                          <td><span className="chip chip--brand">{String(u.HubName_str)}</span></td>
                          <td><UserStatePill user={u} /></td>
                          <td className="tmono">{String(u.GroupName_str || "—")}</td>
                          <td>{AUTH_TYPES[Number(u.AuthType_u32)] ? t(AUTH_TYPES[Number(u.AuthType_u32)]) : "?"}</td>
                          <td className="tmono">{timeAgo(u.LastLoginTime_dt as string)}</td>
                          <td className="tmono">{formatCount(Number(u.NumLogin_u32))}</td>
                          <td className="tmono">{formatBytes(bytes.send + bytes.recv)}</td>
                          <td><QuotaCell quota={quotaOf(u)} net={bytes} /></td>
                          <td className="tmono">{isNever(expires as string) ? t("never") : formatDate(expires as string)}</td>
                          <td className="tact">
                            <div style={{ display: "inline-flex", alignItems: "center", gap: 4 }}>
                              {wgPeers.length > 0 && (
                                <>
                                  <button
                                    type="button"
                                    className="btn btn--sm btn--ghost"
                                    title={t("WireGuard configuration QR code")}
                                    aria-label={t("WireGuard configuration QR code")}
                                    onClick={(e) => {
                                      e.stopPropagation();
                                      handleQrWg(hubName, userName, wgPeers);
                                    }}
                                  >
                                    <IconQrCode size={15} />
                                  </button>
                                  <button
                                    type="button"
                                    className="btn btn--sm btn--ghost"
                                    title={wgPeers.length > 1 ? t("Choose WireGuard .conf ({count})", { count: wgPeers.length }) : t("Download WireGuard .conf")}
                                    aria-label={t("Download WireGuard .conf")}
                                    disabled={downloadingWgKey === wgKey}
                                    onClick={(e) => {
                                      e.stopPropagation();
                                      void handleDownloadWg(hubName, userName, wgPeers);
                                    }}
                                  >
                                    {downloadingWgKey === wgKey ? <span className="spin" /> : <IconWireGuardDownload size={15} />}
                                  </button>
                                </>
                              )}
                              <button
                                type="button"
                                className="btn btn--sm btn--ghost"
                                title={t("Download .vpn connection file")}
                                aria-label={t("Download .vpn connection file")}
                                onClick={(e) => {
                                  e.stopPropagation();
                                  setVpnFor({ hub: hubName, name: userName });
                                }}
                              >
                                <IconVpnDownload size={15} />
                              </button>
                              <span className="pcard__go"><IconChevron size={15} /></span>
                            </div>
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
            {filtered.map((u) => {
              const bytes = movedBy(u);
              const quota = quotaOf(u);
              return (
                <button key={`${u.HubName_str}/${u.Name_str}`} className="row" onClick={() => open(u)}>
                  <div className="row__main">
                    <div className="row__name">
                      <span className="mono">{String(u.Name_str)}</span>
                      <UserStatePill user={u} />
                    </div>
                    <div className="spec">
                      <span className="chip chip--brand">{String(u.HubName_str)}</span>
                      <span className="chip"><i>{t("group")}</i>{String(u.GroupName_str || "—")}</span>
                      <span className="chip"><i>{t("data")}</i>{formatBytes(bytes.send + bytes.recv)}</span>
                      <span className="chip"><i>{t("seen")}</i>{timeAgo(u.LastLoginTime_dt as string)}</span>
                      {quota?.has_limit && (
                        <span className={`chip${quota.blocked ? "" : " chip--brand"}`}>
                          <i>{t("limit")}</i>
                          {formatBytes(meteredBytes(bytes, quota.metric))} /{" "}
                          {formatBytes(quota.limit_bytes)}
                        </span>
                      )}
                    </div>
                  </div>
                  <div className="row__side">
                    <IconChevron size={15} />
                  </div>
                </button>
              );
            })}
          </div>
        </>
      )}

      {vpnFor && <VpnFileSheet hub={vpnFor.hub} name={vpnFor.name} onClose={() => setVpnFor(null)} />}
      {wgAction && wg && (
        <WireGuardUserActionSheet
          state={wg}
          action={wgAction}
          onClose={() => setWgAction(null)}
          onPeerUpdated={(fresh) => {
            if (fresh) setWg(fresh);
            else void loadWg();
          }}
        />
      )}
      {creating && createHub && (
        <UserSheet
          hub={createHub}
          hubChoices={data?.hubs}
          onHubChange={setCreateHub}
          onClose={() => setCreating(false)}
          onSaved={(name) => {
            setCreating(false);
            void load();
            navigate(`/hub/${seg(createHub)}/user/${seg(name)}`);
          }}
        />
      )}
    </div>
  );
}
