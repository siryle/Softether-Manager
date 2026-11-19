"use client";

import { useCallback, useEffect, useMemo, useRef, useState } from "react";
import { Empty, ErrorAlert, LoadingBlock, PageHead } from "../components/bits";
import { api, type SessionReportQuery, type SessionReportSummary, type Wire } from "../lib/api";
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
import { IconDownload, IconRefresh, IconSearch } from "../ui/Icon";
import { Pill } from "../ui/Status";
import { EndpointCell, filterIpv4 } from "./hub/HubSessions";
import { cleanClientHost, SessionUsageSheet } from "./UserDetail";

type TimePreset = "all" | "today" | "24h" | "7d" | "30d" | "custom";
type StatusFilter = "all" | "active" | "ended";

const PAGE_SIZE = 50;

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
 * Supports time-window presets and custom date-time ranges, hub/status/keyword filtering,
 * session detail drill-down, and UTF-8 CSV export.
 */
export function AuditReport() {
  const { t, lang } = useI18n();
  const { push } = useToast();

  const [preset, setPreset] = useState<TimePreset>("all");
  const [fromLocal, setFromLocal] = useState("");
  const [toLocal, setToLocal] = useState("");
  const [hub, setHub] = useState("");
  const [status, setStatus] = useState<StatusFilter>("all");
  const [queryInput, setQueryInput] = useState("");
  const [query, setQuery] = useState("");

  const [items, setItems] = useState<Wire[] | null>(null);
  const [total, setTotal] = useState(0);
  const [summary, setSummary] = useState<SessionReportSummary>(EMPTY_SUMMARY);
  const [hubs, setHubs] = useState<string[]>([]);
  const [error, setError] = useState<string | null>(null);
  const [loading, setLoading] = useState(false);
  const [loadingMore, setLoadingMore] = useState(false);
  const [exporting, setExporting] = useState(false);
  const [openSession, setOpenSession] = useState<Wire | null>(null);

  const reqSeq = useRef(0);

  // Debounce keyword input so typing doesn't issue a query per keystroke.
  useEffect(() => {
    const timer = setTimeout(() => setQuery(queryInput.trim()), 250);
    return () => clearTimeout(timer);
  }, [queryInput]);

  const invalidRange = useMemo(() => {
    if (!fromLocal || !toLocal) return false;
    return new Date(fromLocal).getTime() > new Date(toLocal).getTime();
  }, [fromLocal, toLocal]);

  const filterParams = useMemo<SessionReportQuery>(() => ({
    from: localInputToIso(fromLocal),
    to: localInputToIso(toLocal),
    hub: hub || undefined,
    status,
    q: query || undefined,
  }), [fromLocal, toLocal, hub, status, query]);

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
      const res = await api.sessionReport({ ...filterParams, limit: PAGE_SIZE, offset });
      if (seq !== reqSeq.current) return;
      setItems((prev) => (append && prev ? [...prev, ...(res.items ?? [])] : (res.items ?? [])));
      setTotal(res.total ?? 0);
      setSummary(res.summary ?? EMPTY_SUMMARY);
      if (Array.isArray(res.hubs)) setHubs(res.hubs);
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

  const resetFilters = () => {
    setPreset("all");
    setFromLocal("");
    setToLocal("");
    setHub("");
    setStatus("all");
    setQueryInput("");
    setQuery("");
  };

  const hasFilters = preset !== "all" || Boolean(fromLocal || toLocal || hub || queryInput || status !== "all");

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

  return (
    <div className="page audit-page">
      <PageHead
        title={t("Audit report")}
        sub={t("Historical VPN user sessions, connect/disconnect times, and IP geolocation.")}
        actions={
          <div className="audit-head-actions">
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
            {hasFilters && (
              <button type="button" className="btn btn--sm btn--ghost" onClick={resetFilters}>
                {t("Reset filters")}
              </button>
            )}
          </div>

          <div className="audit-controls">
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
                  placeholder={t("user, host, IP…")}
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

      {error && <ErrorAlert>{error}</ErrorAlert>}

      {items === null ? (
        <LoadingBlock label="loading session history" />
      ) : items.length === 0 ? (
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
                      <th scope="col">{t("Connected at")}</th>
                      <th scope="col">{t("Disconnected at")}</th>
                      <th scope="col">{t("Duration")}</th>
                      <th scope="col">{t("IP / Location")}</th>
                      <th scope="col">{t("Transferred")}</th>
                    </tr>
                  </thead>
                  <tbody>
                    {items.map((h) => {
                      const rowHub = String(h.hub || "");
                      const username = String(h.username || "");
                      const started = new Date(String(h.started_date)).getTime();
                      const endedAt = h.ended_date ? new Date(String(h.ended_date)).getTime() : null;
                      const duration = endedAt ? (endedAt - started) / 1000 : (Date.now() - started) / 1000;
                      const v4 = filterIpv4(h.virtual_ip);
                      const host = cleanClientHost(h.client_hostname, h.client_ip);
                      const location = hasIPLocation(h.ip_location, h.ip_isp)
                        ? formatIPLocation(h.ip_location, h.ip_isp)
                        : "";
                      return (
                        <tr key={String(h.id)} className="clickable" onClick={() => setOpenSession(h)}>
                          <td>
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
                            </div>
                          </td>
                          <td className="tmono" title={timeAgo(String(h.started_date))}>
                            <div>{formatDateTime(String(h.started_date))}</div>
                            <span className="tsub">{timeAgo(String(h.started_date))}</span>
                          </td>
                          <td className="tmono">
                            {h.ended_date ? (
                              <>
                                <div>{formatDateTime(String(h.ended_date))}</div>
                                <span className="tsub">{timeAgo(String(h.ended_date))}</span>
                              </>
                            ) : (
                              <Pill kind="ok" label="connected" />
                            )}
                          </td>
                          <td className="tmono">{formatDuration(duration)}</td>
                          <td className="tmono">
                            <EndpointCell
                              virtualIp={v4}
                              clientIp={h.client_ip}
                              location={location}
                              host={host}
                            />
                          </td>
                          <td className="tmono">
                            <div>{formatBytes(Number(h.bytes_total))}</div>
                            <span className="tsub">
                              ↓ {formatBytes(Number(h.download_bytes))} · ↑ {formatBytes(Number(h.upload_bytes))}
                            </span>
                          </td>
                        </tr>
                      );
                    })}
                  </tbody>
                </table>
              </div>

              {items.length < total && (
                <div className="morebar">
                  <span className="micro">
                    {items.length} / {total} {t("logins")}
                  </span>
                  <button
                    type="button"
                    className="btn btn--sm"
                    disabled={loadingMore}
                    onClick={() => void fetchReport(items.length, true)}
                  >
                    {loadingMore && <span className="spin" />} {t("Show more")}
                  </button>
                </div>
              )}
            </div>
          </div>

          {/* Mobile Card List */}
          <div className="rows only-mobile-b">
            {items.map((h) => {
              const rowHub = String(h.hub || "");
              const username = String(h.username || "");
              const started = new Date(String(h.started_date)).getTime();
              const endedAt = h.ended_date ? new Date(String(h.ended_date)).getTime() : null;
              const duration = endedAt ? (endedAt - started) / 1000 : (Date.now() - started) / 1000;
              const v4 = filterIpv4(h.virtual_ip);
              const location = hasIPLocation(h.ip_location, h.ip_isp)
                ? formatIPLocation(h.ip_location, h.ip_isp)
                : "";
              return (
                <button
                  key={String(h.id)}
                  type="button"
                  className="row"
                  onClick={() => setOpenSession(h)}
                >
                  <div className="row__main">
                    <div className="row__name">
                      <span className="mono truncate">{username || "—"}</span>
                      {rowHub && <span className="chip mono" style={{ fontSize: "10px" }}>{rowHub}</span>}
                      {h.ended_date ? (
                        <span className="chip">{formatDuration(duration)}</span>
                      ) : (
                        <Pill kind="ok" label="connected" />
                      )}
                    </div>

                    <div className="audit-card-times mono">
                      <span>{t("Connected at")}: {formatDateTime(String(h.started_date))}</span>
                      <span>
                        {t("Disconnected at")}: {h.ended_date ? formatDateTime(String(h.ended_date)) : t("still connected")}
                      </span>
                    </div>

                    <div className="spec">
                      {h.client_ip ? <span className="chip mono">{String(h.client_ip)}</span> : null}
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
            })}

            {items.length < total && (
              <div className="morebar card">
                <span className="micro">
                  {items.length} / {total} {t("logins")}
                </span>
                <button
                  type="button"
                  className="btn btn--sm"
                  disabled={loadingMore}
                  onClick={() => void fetchReport(items.length, true)}
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
