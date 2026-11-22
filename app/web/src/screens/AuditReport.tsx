"use client";

import { useCallback, useEffect, useMemo, useRef, useState } from "react";
import { Empty, ErrorAlert, LoadingBlock, PageHead } from "../components/bits";
import {
  api,
  type SessionAnalytics,
  type SessionReportQuery,
  type SessionReportSummary,
  type Wire,
} from "../lib/api";
import { useI18n } from "../lib/i18n";
import { Link, seg } from "../lib/router";
import { useToast } from "../lib/toast";
import {
  downloadBlob,
  formatBytes,
  formatCount,
  formatDateTime,
  formatDuration,
  formatIPLocation,
  hasIPLocation,
  timeAgo,
} from "../lib/util";
import { IconChevron, IconDownload, IconRefresh, IconSearch } from "../ui/Icon";
import { Pill } from "../ui/Status";
import { isSystemSession } from "../lib/se";
import { EndpointCell, filterIpv4 } from "./hub/HubSessions";
import { cleanClientHost, SessionUsageSheet } from "./UserDetail";

type TimePreset = "all" | "today" | "24h" | "7d" | "30d" | "custom";
type StatusFilter = "all" | "active" | "ended";
type SortMode = "started" | "bytes" | "duration";

const PAGE_SIZE = 50;

type SessionGroup = { key: string; latest: Wire; earlier: Wire[] };

function isAbnormalDisconnect(reason?: string): boolean {
  const s = String(reason || "").trim().toLowerCase();
  return Boolean(s) && !s.includes("no error") && !s.includes("code 0");
}

function rowInfo(h: Wire) {
  const started = new Date(String(h.started_date)).getTime();
  const endedAt = h.ended_date ? new Date(String(h.ended_date)).getTime() : null;
  const clientIp = String(h.client_ip || "");
  const clientPort = Number(h.client_port) || 0;
  return {
    rowHub: String(h.hub || ""),
    username: String(h.username || ""),
    realName: String(h.real_name || h.Realname_utf || ""),
    note: String(h.note || h.Note_utf || ""),
    protocol: String(h.protocol || ""),
    disconnectReason: String(h.disconnect_reason || ""),
    clientIpWithPort: clientIp && clientPort > 0 ? `${clientIp}:${clientPort}` : clientIp,
    duration: endedAt ? (endedAt - started) / 1000 : (Date.now() - started) / 1000,
    v4: filterIpv4(h.virtual_ip),
    location: hasIPLocation(h.ip_location, h.ip_isp) ? formatIPLocation(h.ip_location, h.ip_isp) : "",
  };
}

const EMPTY_SUMMARY: SessionReportSummary = {
  total_sessions: 0,
  active_sessions: 0,
  unique_users: 0,
  total_bytes: 0,
  download_bytes: 0,
  upload_bytes: 0,
};

function toLocalInputValue(date: Date): string {
  const pad = (n: number) => String(n).padStart(2, "0");
  return `${date.getFullYear()}-${pad(date.getMonth() + 1)}-${pad(date.getDate())}T${pad(date.getHours())}:${pad(date.getMinutes())}`;
}

function presetRange(preset: Exclude<TimePreset, "all" | "custom">): { from: string; to: string } {
  const now = new Date();
  let start: Date;
  if (preset === "today") {
    start = new Date(now.getFullYear(), now.getMonth(), now.getDate(), 0, 0, 0, 0);
  } else {
    const hours = preset === "24h" ? 24 : preset === "7d" ? 24 * 7 : 24 * 30;
    start = new Date(now.getTime() - hours * 3600 * 1000);
  }
  return { from: toLocalInputValue(start), to: toLocalInputValue(now) };
}

function localInputToIso(val: string): string | undefined {
  if (!val) return undefined;
  const d = new Date(val);
  return isFinite(d.getTime()) ? d.toISOString() : undefined;
}

/**
 * Historical VPN session audit report across all Virtual Hubs on the active server.
 * Includes interactive visual analytics (trend bar chart, top users/regions/protocols,
 * anomaly badges), protocol & sort filters, drill-down session usage drawer, and CSV export.
 */
export function AuditReport() {
  const { t, lang } = useI18n();
  const { push } = useToast();

  const [preset, setPreset] = useState<TimePreset>("all");
  const [fromLocal, setFromLocal] = useState("");
  const [toLocal, setToLocal] = useState("");
  const [hub, setHub] = useState("");
  const [status, setStatus] = useState<StatusFilter>("all");
  const [protocol, setProtocol] = useState("");
  const [sortMode, setSortMode] = useState<SortMode>("started");
  const [queryInput, setQueryInput] = useState("");
  const [query, setQuery] = useState("");
  const [analyticsOpen, setAnalyticsOpen] = useState(true);

  const [items, setItems] = useState<Wire[] | null>(null);
  const [total, setTotal] = useState(0);
  const [summary, setSummary] = useState<SessionReportSummary>(EMPTY_SUMMARY);
  const [analytics, setAnalytics] = useState<SessionAnalytics | null>(null);
  const [hubs, setHubs] = useState<string[]>([]);
  const [error, setError] = useState<string | null>(null);
  const [loading, setLoading] = useState(false);
  const [loadingMore, setLoadingMore] = useState(false);
  const [exporting, setExporting] = useState(false);
  const [openSession, setOpenSession] = useState<Wire | null>(null);

  const reqSeq = useRef(0);

  useEffect(() => {
    const timer = setTimeout(() => setQuery(queryInput.trim()), 250);
    return () => clearTimeout(timer);
  }, [queryInput]);

  const invalidRange = useMemo(() => {
    if (!fromLocal || !toLocal) return false;
    return new Date(fromLocal).getTime() > new Date(toLocal).getTime();
  }, [fromLocal, toLocal]);

  const displayItems = useMemo(() => {
    if (!items) return null;
    return items.filter((item) => !isSystemSession(item));
  }, [items]);

  // Group by hub+user in default chronological mode; keep flat ranked order when sorting by bytes/duration.
  const groups = useMemo(() => {
    if (!displayItems) return [];
    if (sortMode !== "started") {
      return displayItems.map((item) => ({ key: String(item.id), latest: item, earlier: [] as Wire[] }));
    }
    const byKey = new Map<string, SessionGroup>();
    for (const item of displayItems) {
      const key = `${String(item.hub || "")}\u0000${String(item.username || "")}`;
      const group = byKey.get(key);
      if (group) group.earlier.push(item);
      else byKey.set(key, { key, latest: item, earlier: [] });
    }
    return [...byKey.values()];
  }, [displayItems, sortMode]);

  const [expanded, setExpanded] = useState<Set<string>>(() => new Set());
  const toggleGroup = (key: string) =>
    setExpanded((prev) => {
      const next = new Set(prev);
      if (next.has(key)) next.delete(key);
      else next.add(key);
      return next;
    });

  const filterParams = useMemo<SessionReportQuery>(() => ({
    from: localInputToIso(fromLocal),
    to: localInputToIso(toLocal),
    hub: hub || undefined,
    status,
    protocol: protocol || undefined,
    sort: sortMode,
    q: query || undefined,
  }), [fromLocal, toLocal, hub, status, protocol, sortMode, query]);

  const fetchReport = useCallback(async (offset = 0, append = false) => {
    if (invalidRange) return;
    const seq = ++reqSeq.current;
    if (append) {
      setLoadingMore(true);
    } else {
      setLoading(true);
      setError(null);
    }
    try {
      const tz = typeof Intl !== "undefined" ? Intl.DateTimeFormat().resolvedOptions().timeZone : undefined;
      const tzOffset = new Date().getTimezoneOffset();
      const [res, anRes] = await Promise.all([
        api.sessionReport({ ...filterParams, limit: PAGE_SIZE, offset }),
        append
          ? Promise.resolve(null)
          : api.sessionReportAnalytics({ ...filterParams, tz, tz_offset: tzOffset }).catch(() => null),
      ]);
      if (seq !== reqSeq.current) return;
      setItems((prev) => (append && prev ? [...prev, ...(res.items ?? [])] : (res.items ?? [])));
      setTotal(res.total ?? 0);
      setSummary(res.summary ?? EMPTY_SUMMARY);
      if (Array.isArray(res.hubs)) setHubs(res.hubs);
      if (!append && anRes) setAnalytics(anRes);
    } catch (e) {
      if (seq !== reqSeq.current) return;
      setError(e instanceof Error ? e.message : String(e));
      if (!append) setItems([]);
    } finally {
      if (seq === reqSeq.current) {
        setLoading(false);
        setLoadingMore(false);
      }
    }
  }, [filterParams, invalidRange]);

  useEffect(() => {
    void fetchReport(0, false);
  }, [fetchReport]);

  const applyPreset = (next: TimePreset) => {
    setPreset(next);
    if (next === "all") {
      setFromLocal("");
      setToLocal("");
    } else if (next !== "custom") {
      const range = presetRange(next);
      setFromLocal(range.from);
      setToLocal(range.to);
    }
  };

  const narrowToDay = (key: string) => {
    // key is YYYY-MM-DD (daily) or YYYY-MM-DDTHH (hourly)
    if (key.length === 10) {
      setPreset("custom");
      setFromLocal(`${key}T00:00`);
      setToLocal(`${key}T23:59`);
    } else if (key.length === 13) {
      setPreset("custom");
      setFromLocal(`${key}:00`);
      setToLocal(`${key}:59`);
    }
  };

  const resetFilters = () => {
    setPreset("all");
    setFromLocal("");
    setToLocal("");
    setHub("");
    setStatus("all");
    setProtocol("");
    setSortMode("started");
    setQueryInput("");
    setQuery("");
  };

  const hasFilters =
    preset !== "all" ||
    Boolean(fromLocal || toLocal || hub || protocol || queryInput || status !== "all" || sortMode !== "started");

  const handleExport = async () => {
    if (invalidRange) return;
    setExporting(true);
    try {
      const tz = typeof Intl !== "undefined" ? Intl.DateTimeFormat().resolvedOptions().timeZone : undefined;
      const tzOffset = new Date().getTimezoneOffset();
      const blob = await api.exportSessionReport({
        ...filterParams,
        lang,
        tz,
        tz_offset: tzOffset,
      });
      const stamp = new Date().toISOString().slice(0, 19).replace(/[:T]/g, "-");
      downloadBlob(`session-audit-report-${stamp}.csv`, blob);
      push("ok", t("Audit report CSV exported."));
    } catch (e) {
      push("err", e instanceof Error ? e.message : String(e));
    } finally {
      setExporting(false);
    }
  };

  const presetOptions: { id: TimePreset; label: string }[] = [
    { id: "all", label: t("All time") },
    { id: "today", label: t("Today") },
    { id: "24h", label: t("Last 24h") },
    { id: "7d", label: t("Last 7d") },
    { id: "30d", label: t("Last 30d") },
  ];

  const expandButton = (group: SessionGroup, className: string) => {
    const open = expanded.has(group.key);
    return (
      <button
        type="button"
        className={`${className}${open ? " on" : ""}`}
        aria-expanded={open}
        title={t(open ? "Hide earlier sessions" : "Show earlier sessions")}
        onClick={(e) => {
          e.stopPropagation();
          toggleGroup(group.key);
        }}
      >
        <IconChevron size={12} className="audit-expand__icon" />
        {t("{count} earlier", { count: group.earlier.length })}
      </button>
    );
  };

  const desktopRow = (h: Wire, group?: SessionGroup) => {
    const {
      rowHub,
      username,
      realName,
      note,
      protocol: proto,
      disconnectReason,
      clientIpWithPort,
      duration,
      v4,
      location,
    } = rowInfo(h);
    const host = cleanClientHost(h.client_hostname, h.client_ip);
    const abnormal = isAbnormalDisconnect(disconnectReason);
    return (
      <tr
        key={String(h.id)}
        className={`clickable${group ? "" : " audit-history"}`}
        onClick={() => setOpenSession(h)}
      >
        <td>
          {group ? (
            <div style={{ display: "flex", alignItems: "center", gap: 8, flexWrap: "wrap" }}>
              {username ? (
                <Link
                  to={`/hub/${seg(rowHub)}/user/${seg(username)}`}
                  className="linkish mono bold"
                  onClick={(e) => e.stopPropagation()}
                >
                  {username}
                </Link>
              ) : (
                <span className="tmono">—</span>
              )}
              {rowHub && (
                <Link
                  to={`/hub/${seg(rowHub)}`}
                  className="chip mono"
                  onClick={(e) => e.stopPropagation()}
                >
                  {rowHub}
                </Link>
              )}
              {group.earlier.length > 0 && expandButton(group, "audit-expand")}
            </div>
          ) : (
            <span className="audit-history__name mono">↳ {username || "—"}</span>
          )}
          {note ? (
            <div className="tsub truncate" style={{ maxWidth: 180 }} title={note}>
              {note}
            </div>
          ) : null}
        </td>
        <td style={{ whiteSpace: "nowrap" }}>
          {realName ? (
            <span>{realName}</span>
          ) : (
            <span className="tmono" style={{ color: "var(--text-faint)" }}>—</span>
          )}
        </td>
        <td>
          {proto ? (
            <span className="chip mono">{proto}</span>
          ) : (
            <span className="tmono" style={{ color: "var(--text-faint)" }}>—</span>
          )}
        </td>
        <td className="tmono" title={timeAgo(String(h.started_date))}>
          <div>{formatDateTime(String(h.started_date))}</div>
          <span className="tsub">{timeAgo(String(h.started_date))}</span>
        </td>
        <td className="tmono">
          {h.ended_date ? (
            <>
              <div>{formatDateTime(String(h.ended_date))}</div>
              {disconnectReason ? (
                <span
                  className={`tsub truncate${abnormal ? " viz-text-warn" : ""}`}
                  style={{ maxWidth: 180, display: "block" }}
                  title={disconnectReason}
                >
                  {disconnectReason}
                </span>
              ) : (
                <span className="tsub">{timeAgo(String(h.ended_date))}</span>
              )}
            </>
          ) : (
            <Pill kind="ok" label="connected" />
          )}
        </td>
        <td className="tmono">{formatDuration(duration)}</td>
        <td className="tmono">
          <EndpointCell virtualIp={v4} clientIp={clientIpWithPort} location={location} host={host} />
        </td>
        <td className="tmono">
          <div>{formatBytes(Number(h.bytes_total))}</div>
          <span className="tsub">
            ↓ {formatBytes(Number(h.download_bytes))} · ↑ {formatBytes(Number(h.upload_bytes))}
          </span>
        </td>
      </tr>
    );
  };

  const mobileRow = (h: Wire, history = false) => {
    const { rowHub, username, realName, note, protocol: proto, disconnectReason, clientIpWithPort, duration, v4, location } =
      rowInfo(h);
    const abnormal = isAbnormalDisconnect(disconnectReason);
    return (
      <button
        key={String(h.id)}
        type="button"
        className={`row${history ? " audit-history-card" : ""}`}
        onClick={() => setOpenSession(h)}
      >
        <div className="row__main">
          <div className="row__name">
            <span className="mono truncate">{username || "—"}</span>
            {realName ? <span style={{ fontSize: "12px", color: "var(--muted)" }}>({realName})</span> : null}
            {rowHub && <span className="chip mono" style={{ fontSize: "10px" }}>{rowHub}</span>}
            {proto && <span className="chip mono" style={{ fontSize: "10px" }}>{proto}</span>}
            {h.ended_date ? (
              <span className="chip">{formatDuration(duration)}</span>
            ) : (
              <Pill kind="ok" label="connected" />
            )}
          </div>
          {note ? (
            <div className="tsub truncate" style={{ fontSize: "12px", color: "var(--muted)", marginBottom: 4 }}>
              {note}
            </div>
          ) : null}

          <div className="audit-card-times mono">
            <span>{t("Connected at")}: {formatDateTime(String(h.started_date))}</span>
            <span>
              {t("Disconnected at")}: {h.ended_date ? formatDateTime(String(h.ended_date)) : t("still connected")}
            </span>
            {disconnectReason && (
              <span className={abnormal ? "viz-text-warn" : ""}>{disconnectReason}</span>
            )}
          </div>

          <div className="spec">
            {clientIpWithPort ? <span className="chip mono">{clientIpWithPort}</span> : null}
            {location ? (
              <span className="chip" style={{ color: "var(--accent)", borderColor: "var(--accent-line)" }}>
                {location}
              </span>
            ) : null}
            {v4 ? <span className="chip mono" style={{ color: "var(--ok)" }}>{v4}</span> : null}
          </div>
        </div>
        <div className="row__meta">
          <span className="mono bold">{formatBytes(Number(h.bytes_total))}</span>
          <span className="micro">{t("Duration")}: {formatDuration(duration)}</span>
        </div>
      </button>
    );
  };

  const buckets = analytics?.buckets ?? [];
  const maxBucketSess = Math.max(1, ...buckets.map((b) => b.sessions));
  const anTopUsers = analytics?.top_users ?? [];
  const maxAnUserBytes = Math.max(1, ...anTopUsers.map((u) => u.total_bytes || u.sessions));
  const anTopLoc = analytics?.top_locations ?? [];
  const maxAnLoc = Math.max(1, ...anTopLoc.map((l) => l.sessions));
  const anProtos = analytics?.protocols ?? [];
  const maxAnProto = Math.max(1, ...anProtos.map((p) => p.sessions));

  const availableProtos = useMemo(() => {
    const set = new Set<string>(["SoftEther SSL-VPN", "WireGuard", "OpenVPN", "L2TP/IPsec", "MS-SSTP"]);
    for (const p of anProtos) {
      if (p.protocol && p.protocol !== "Unknown") set.add(p.protocol);
    }
    return [...set];
  }, [anProtos]);

  return (
    <div className="page audit-page">
      <PageHead
        title={t("Audit Report")}
        sub={t("Historical VPN user sessions, connect/disconnect times, and IP geolocation.")}
        actions={
          <div className="audit-head-actions">
            <button
              type="button"
              className="btn btn--sm"
              onClick={() => setAnalyticsOpen((o) => !o)}
              aria-expanded={analyticsOpen}
            >
              {analyticsOpen ? t("Hide charts") : t("Show charts")}
            </button>
            <button
              type="button"
              className="btn btn--sm"
              onClick={() => void fetchReport(0, false)}
              disabled={loading || invalidRange}
              title={t("Refresh")}
            >
              {loading ? <span className="spin" /> : <IconRefresh size={14} />}
              {t("Refresh")}
            </button>
            <button
              type="button"
              className="btn btn--sm btn--primary"
              onClick={() => void handleExport()}
              disabled={exporting || invalidRange || total === 0}
            >
              {exporting ? <span className="spin" /> : <IconDownload size={14} />}
              {t("Export CSV")}
            </button>
          </div>
        }
      />

      {/* Filter Bar */}
      <search className="card audit-filters">
        <form
          onSubmit={(e) => {
            e.preventDefault();
            setQuery(queryInput.trim());
          }}
        >
          <div className="audit-presets-row">
            <div className="filterseg" role="group" aria-label={t("Time range")}>
              {presetOptions.map((opt) => (
                <button
                  key={opt.id}
                  type="button"
                  className={`filterseg__btn${preset === opt.id ? " on" : ""}`}
                  onClick={() => applyPreset(opt.id)}
                >
                  {opt.label}
                </button>
              ))}
            </div>
            <div style={{ display: "flex", alignItems: "center", gap: "var(--s2)", flexWrap: "wrap" }}>
              <select
                aria-label={t("Sort by")}
                className="select"
                style={{ height: 34, minHeight: 34 }}
                value={sortMode}
                onChange={(e) => setSortMode(e.target.value as SortMode)}
              >
                <option value="started">{t("Sort: Latest")}</option>
                <option value="bytes">{t("Sort: Transfer")}</option>
                <option value="duration">{t("Sort: Duration")}</option>
              </select>
              {hasFilters && (
                <button type="button" className="btn btn--sm btn--ghost" onClick={resetFilters}>
                  {t("Reset filters")}
                </button>
              )}
            </div>
          </div>

          <div className="audit-controls audit-controls--wide">
            <div className="audit-field">
              <label htmlFor="audit-from">{t("Start time")}</label>
              <input
                id="audit-from"
                name="from"
                type="datetime-local"
                className="input mono"
                value={fromLocal}
                onChange={(e) => {
                  setPreset("custom");
                  setFromLocal(e.target.value);
                }}
              />
            </div>

            <div className="audit-field">
              <label htmlFor="audit-to">{t("End time")}</label>
              <input
                id="audit-to"
                name="to"
                type="datetime-local"
                className="input mono"
                value={toLocal}
                onChange={(e) => {
                  setPreset("custom");
                  setToLocal(e.target.value);
                }}
              />
            </div>

            <div className="audit-field">
              <label htmlFor="audit-hub">{t("Virtual Hub")}</label>
              <select
                id="audit-hub"
                name="hub"
                className="select"
                value={hub}
                onChange={(e) => setHub(e.target.value)}
              >
                <option value="">{t("All hubs")}</option>
                {hubs.map((h) => (
                  <option key={h} value={h}>{h}</option>
                ))}
              </select>
            </div>

            <div className="audit-field">
              <label htmlFor="audit-status">{t("Session status")}</label>
              <select
                id="audit-status"
                name="status"
                className="select"
                value={status}
                onChange={(e) => setStatus(e.target.value as StatusFilter)}
              >
                <option value="all">{t("All statuses")}</option>
                <option value="active">{t("Active only")}</option>
                <option value="ended">{t("Ended only")}</option>
              </select>
            </div>

            <div className="audit-field">
              <label htmlFor="audit-proto">{t("Protocol")}</label>
              <select
                id="audit-proto"
                name="protocol"
                className="select"
                value={protocol}
                onChange={(e) => setProtocol(e.target.value)}
              >
                <option value="">{t("All protocols")}</option>
                {availableProtos.map((p) => (
                  <option key={p} value={p}>{p}</option>
                ))}
              </select>
            </div>

            <div className="audit-field audit-field--search">
              <label htmlFor="audit-q">{t("Search")}</label>
              <div className="audit-search-wrap">
                <span className="audit-search-icon" aria-hidden="true">
                  <IconSearch size={15} />
                </span>
                <input
                  id="audit-q"
                  name="q"
                  type="search"
                  className="input"
                  value={queryInput}
                  onChange={(e) => setQueryInput(e.target.value)}
                  placeholder={t("user, host, IP, region…")}
                  autoCapitalize="none"
                  autoCorrect="off"
                  spellCheck={false}
                />
              </div>
            </div>
          </div>

          {invalidRange && (
            <div className="alert alert--warn" style={{ marginTop: "var(--s3)" }}>
              {t("Start time must be earlier than end time.")}
            </div>
          )}
        </form>
      </search>

      {/* Summary KPIs */}
      <div className="kpis audit-kpis">
        <div className="kpi">
          <div className="kpi__l">{t("Total sessions")}</div>
          <div className="kpi__v">{formatCount(summary.total_sessions)}</div>
          <div className="kpi__s">{t("{count} active now", { count: summary.active_sessions })}</div>
        </div>
        <div className="kpi">
          <div className="kpi__l">{t("Unique users")}</div>
          <div className="kpi__v">{formatCount(summary.unique_users)}</div>
          <div className="kpi__s">{t("Matched by filter")}</div>
        </div>
        <div className="kpi">
          <div className="kpi__l">{t("Downloaded")}</div>
          <div className="kpi__v">{formatBytes(summary.download_bytes)}</div>
          <div className="kpi__s">{t("Total: {size}", { size: formatBytes(summary.total_bytes) })}</div>
        </div>
        <div className="kpi">
          <div className="kpi__l">{t("Uploaded")}</div>
          <div className="kpi__v">{formatBytes(summary.upload_bytes)}</div>
          <div className="kpi__s">{t("Total: {size}", { size: formatBytes(summary.total_bytes) })}</div>
        </div>
      </div>

      {/* Collapsible Visual Analytics Panel */}
      {analyticsOpen && analytics && summary.total_sessions > 0 && (
        <div className="audit-analytics">
          {(analytics.anomalies.abnormal_disconnects > 0 ||
            analytics.anomalies.concurrent_multi_ip > 0 ||
            analytics.anomalies.short_sessions > 0) && (
            <div className="audit-anomalies">
              {analytics.anomalies.concurrent_multi_ip > 0 && (
                <span className="viz-badge viz-badge--warn">
                  {t("{count} multi-IP concurrent users", { count: analytics.anomalies.concurrent_multi_ip })}
                </span>
              )}
              {analytics.anomalies.abnormal_disconnects > 0 && (
                <span className="viz-badge viz-badge--err">
                  {t("{count} abnormal disconnects", { count: analytics.anomalies.abnormal_disconnects })}
                </span>
              )}
              {analytics.anomalies.short_sessions > 0 && (
                <span className="viz-badge">
                  {t("{count} short sessions (≤60s)", { count: analytics.anomalies.short_sessions })}
                </span>
              )}
            </div>
          )}

          <div className="audit-analytics__grid">
            {/* Trend Bar Chart */}
            <div className="card audit-viz-card">
              <div className="audit-viz-card__head">
                <h4>{analytics.granularity === "hour" ? t("Hourly login trend") : t("Daily login trend")}</h4>
                <span className="micro">{t("Click a bar to narrow range")}</span>
              </div>
              {buckets.length === 0 ? (
                <p className="micro">{t("No trend buckets available.")}</p>
              ) : (
                <svg className="audit-bars-svg" viewBox="0 0 420 148" role="img" aria-label={t("Session trend chart")}>
                  {(() => {
                    const count = buckets.length;
                    const padX = 16;
                    const usableW = 420 - padX * 2;
                    const slot = usableW / Math.max(1, count);
                    const barW = Math.max(6, Math.min(34, slot * 0.68));
                    const labelEvery = Math.max(1, Math.ceil(count / 7));
                    return buckets.map((b, i) => {
                      const h = Math.max(4, Math.round((b.sessions / maxBucketSess) * 94));
                      const cx = padX + i * slot + slot / 2;
                      const x = cx - barW / 2;
                      const y = 118 - h;
                      return (
                        <g key={b.key} className="audit-bar-col" onClick={() => narrowToDay(b.key)}>
                          <title>{`${b.key}: ${b.sessions} sessions · ${formatBytes(b.total_bytes)}`}</title>
                          <rect x={x} y={y} width={barW} height={h} rx={4} fill="var(--chart-send)" fillOpacity={0.82} />
                          {count <= 14 && (
                            <text x={cx} y={y - 5} textAnchor="middle" className="chart__tick">
                              {b.sessions}
                            </text>
                          )}
                          {(i % labelEvery === 0 || i === count - 1) && (
                            <text x={cx} y={138} textAnchor="middle" className="chart__tick">
                              {b.label}
                            </text>
                          )}
                        </g>
                      );
                    });
                  })()}
                </svg>
              )}
            </div>

            {/* 3 Interactive Breakdown Cards */}
            <div className="audit-breakdowns">
              <div className="card audit-viz-card">
                <div className="audit-viz-card__head">
                  <h4>{t("Top users")}</h4>
                  <span className="micro">{t("Click to filter")}</span>
                </div>
                <div className="viz-mini-list">
                  {anTopUsers.slice(0, 5).map((u) => {
                    const metric = u.total_bytes > 0 ? u.total_bytes : u.sessions;
                    const pct = Math.max(6, Math.round((metric / maxAnUserBytes) * 100));
                    return (
                      <button
                        key={`${u.hub}:${u.username}`}
                        type="button"
                        className={`viz-mini-item${query === u.username ? " on" : ""}`}
                        onClick={() => {
                          const next = query === u.username ? "" : u.username;
                          setQueryInput(next);
                          setQuery(next);
                        }}
                      >
                        <div className="viz-mini-item__row mono">
                          <span className="truncate">{u.username}</span>
                          <span>{u.total_bytes > 0 ? formatBytes(u.total_bytes) : `${u.sessions}`}</span>
                        </div>
                        <div className="viz-bar viz-bar--sm">
                          <div className="viz-bar__seg" style={{ width: `${pct}%`, background: "var(--chart-send)" }} />
                        </div>
                      </button>
                    );
                  })}
                </div>
              </div>

              <div className="card audit-viz-card">
                <div className="audit-viz-card__head">
                  <h4>{t("Top regions")}</h4>
                  <span className="micro">{t("Sessions")}</span>
                </div>
                <div className="viz-mini-list">
                  {anTopLoc.slice(0, 5).map((loc) => {
                    const pct = Math.max(6, Math.round((loc.sessions / maxAnLoc) * 100));
                    const clickable = loc.location !== "Unknown";
                    return (
                      <button
                        key={loc.location}
                        type="button"
                        disabled={!clickable}
                        className={`viz-mini-item${query === loc.location ? " on" : ""}`}
                        onClick={() => {
                          if (!clickable) return;
                          const next = query === loc.location ? "" : loc.location;
                          setQueryInput(next);
                          setQuery(next);
                        }}
                      >
                        <div className="viz-mini-item__row">
                          <span className="truncate">{loc.location === "Unknown" ? t("Unknown") : loc.location}</span>
                          <span className="mono">{loc.sessions}</span>
                        </div>
                        <div className="viz-bar viz-bar--sm">
                          <div className="viz-bar__seg" style={{ width: `${pct}%`, background: "var(--chart-recv)" }} />
                        </div>
                      </button>
                    );
                  })}
                </div>
              </div>

              <div className="card audit-viz-card">
                <div className="audit-viz-card__head">
                  <h4>{t("Protocols")}</h4>
                  <span className="micro">{t("Click to filter")}</span>
                </div>
                <div className="viz-mini-list">
                  {anProtos.slice(0, 5).map((p) => {
                    const pct = Math.max(6, Math.round((p.sessions / maxAnProto) * 100));
                    const filterVal = p.protocol === "Unknown" ? "unknown" : p.protocol;
                    return (
                      <button
                        key={p.protocol}
                        type="button"
                        className={`viz-mini-item${protocol.toLowerCase() === filterVal.toLowerCase() ? " on" : ""}`}
                        onClick={() => setProtocol((cur) => (cur.toLowerCase() === filterVal.toLowerCase() ? "" : filterVal))}
                      >
                        <div className="viz-mini-item__row mono">
                          <span className="truncate">{p.protocol === "Unknown" ? t("Unknown") : p.protocol}</span>
                          <span>{p.sessions}</span>
                        </div>
                        <div className="viz-bar viz-bar--sm">
                          <div className="viz-bar__seg" style={{ width: `${pct}%`, background: "var(--warn)" }} />
                        </div>
                      </button>
                    );
                  })}
                </div>
              </div>
            </div>
          </div>
        </div>
      )}

      {error && <ErrorAlert>{error}</ErrorAlert>}

      {displayItems === null ? (
        <LoadingBlock label="loading session history" />
      ) : displayItems.length === 0 ? (
        <Empty
          title={hasFilters ? t("nothing matches") : t("no logins recorded yet")}
          action={
            hasFilters ? (
              <button type="button" className="btn btn--sm" onClick={resetFilters}>
                {t("Reset filters")}
              </button>
            ) : (
              <Link to="/settings#s-monitoring" className="btn btn--sm">
                {t("Monitoring settings")}
              </Link>
            )
          }
        >
          {hasFilters
            ? t("No session history matches the selected time range or filter.")
            : t("Session history is recorded periodically while users connect. Make sure session monitoring is enabled in Panel settings.")}
        </Empty>
      ) : (
        <>
          {/* Desktop Table */}
          <div className="only-desktop-b">
            <div className="card tcard audit-table-wrap">
              <div className="tscroll" tabIndex={0} role="region" aria-label={t("Session audit records")}>
                <table className="dtable">
                  <thead>
                    <tr>
                      <th scope="col">{t("User")}</th>
                      <th scope="col">{t("Real name")}</th>
                      <th scope="col">{t("Protocol")}</th>
                      <th scope="col">{t("Connected at")}</th>
                      <th scope="col">{t("Disconnected at")}</th>
                      <th scope="col">{t("Duration")}</th>
                      <th scope="col">{t("IP / Location")}</th>
                      <th scope="col">{t("Transferred")}</th>
                    </tr>
                  </thead>
                  <tbody>
                    {groups.flatMap((g) => [
                      desktopRow(g.latest, g),
                      ...(expanded.has(g.key) ? g.earlier.map((h) => desktopRow(h)) : []),
                    ])}
                  </tbody>
                </table>
              </div>

              {displayItems.length < total && (
                <div className="morebar">
                  <span className="micro">
                    {displayItems.length} / {total} {t("logins")}
                  </span>
                  <button
                    type="button"
                    className="btn btn--sm"
                    disabled={loadingMore}
                    onClick={() => void fetchReport(items?.length ?? 0, true)}
                  >
                    {loadingMore && <span className="spin" />} {t("Show more")}
                  </button>
                </div>
              )}
            </div>
          </div>

          {/* Mobile Card List */}
          <div className="rows only-mobile-b">
            {groups.map((g) => (
              <div key={g.key} className="audit-group">
                {mobileRow(g.latest)}
                {g.earlier.length > 0 && expandButton(g, "audit-expand audit-expand--card")}
                {expanded.has(g.key) && g.earlier.map((h) => mobileRow(h, true))}
              </div>
            ))}

            {displayItems.length < total && (
              <div className="morebar card">
                <span className="micro">
                  {displayItems.length} / {total} {t("logins")}
                </span>
                <button
                  type="button"
                  className="btn btn--sm"
                  disabled={loadingMore}
                  onClick={() => void fetchReport(items?.length ?? 0, true)}
                >
                  {loadingMore && <span className="spin" />} {t("Show more")}
                </button>
              </div>
            )}
          </div>
        </>
      )}

      {openSession && (
        <SessionUsageSheet
          hub={String(openSession.hub || "")}
          row={openSession}
          onClose={() => setOpenSession(null)}
        />
      )}
    </div>
  );
}
