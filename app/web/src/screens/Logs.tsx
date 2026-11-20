"use client";

import { useCallback, useEffect, useState } from "react";
import { Empty, ErrorAlert, LoadingBlock, PageHead, usePoll } from "../components/bits";
import { Terminal } from "../components/Terminal";
import { api, type Wire } from "../lib/api";
import { Link } from "../lib/router";
import { useToast } from "../lib/toast";
import { useI18n } from "../lib/i18n";
import { formatBytes, timeAgo } from "../lib/util";
import { IconLogs, IconRefresh, IconReport } from "../ui/Icon";
import { AuditCard } from "./Settings";

/**
 * The server's log files: server log, per-hub security and packet logs.
 * Reading is chunked -- ReadLogFile pages by offset -- and rendered in the
 * same terminal well the installer uses.
 */

const CHUNK_HINT = 256 * 1024; // what one ReadLogFile answer tends to carry

export function Logs() {
  const { t } = useI18n();
  const [files, setFiles] = useState<Wire[] | null>(null);
  const [error, setError] = useState<string | null>(null);
  const [serverVer, setServerVer] = useState<string>("");
  const [refreshing, setRefreshing] = useState(false);
  const [query, setQuery] = useState("");
  const [current, setCurrent] = useState<Wire | null>(null);
  const [text, setText] = useState("");
  const [offset, setOffset] = useState(0);
  const [loading, setLoading] = useState(false);
  const { push } = useToast();

  useEffect(() => {
    void api.serverInfo()
      .then((info) => setServerVer(String(info.ServerVersionString_str ?? "")))
      .catch(() => {});
  }, []);

  const load = useCallback(async () => {
    try {
      const r = await api.logFiles();
      const rawList = Array.isArray(r?.LogFiles)
        ? (r.LogFiles as Wire[])
        : Array.isArray(r?.LogFileList)
          ? (r.LogFileList as Wire[])
          : [];
      setFiles(rawList);
      setError(null);
    } catch (e) {
      setError(e instanceof Error ? e.message : String(e));
      setFiles((prev) => prev ?? []);
    } finally {
      setRefreshing(false);
    }
  }, []);
  usePoll(load, "list", []);

  const read = async (file: Wire, fromOffset: number, append: boolean) => {
    setLoading(true);
    try {
      const filePath = String(file.FilePath_str ?? "");
      const serverName = String(file.ServerName_str ?? "");
      const r = await api.readLog(filePath, fromOffset, serverName);
      setCurrent(file);
      setText((prev) => (append ? prev + r.text : r.text));
      setOffset(fromOffset + (Number((r as Wire).bytes) || r.text.length));
    } catch (e) {
      push("err", e instanceof Error ? e.message : String(e));
    } finally {
      setLoading(false);
    }
  };

  const tail = (file: Wire) => {
    // Start near the end for big files: the last chunk is what an operator
    // almost always wants first.
    const size = Number(file.FileSize_u32) || 0;
    const start = size > CHUNK_HINT ? size - CHUNK_HINT : 0;
    setText("");
    void read(file, start, false);
  };

  const q = query.trim().toLowerCase();
  const filteredFiles = (files ?? []).filter((f) =>
    !q || String(f.FilePath_str ?? "").toLowerCase().includes(q),
  );
  const isDevV5 = /\b5\.\d+/i.test(serverVer);

  return (
    <div className="page">
      <PageHead
        title={t("Logs")}
        sub={t("server, hub security and packet logs")}
        actions={
          <div style={{ display: "flex", alignItems: "center", gap: "var(--s2)", flexWrap: "wrap" }}>
            {files !== null && (
              <span className="chip mono">
                {files.length} {t("Files")}
              </span>
            )}
            <button
              type="button"
              className="btn btn--sm"
              disabled={refreshing}
              onClick={() => {
                setRefreshing(true);
                void load();
              }}
              title={t("Refresh")}
            >
              {refreshing ? <span className="spin" /> : <IconRefresh size={14} />}
              {t("Refresh")}
            </button>
          </div>
        }
      />

      {error && (
        <ErrorAlert>
          {error}
        </ErrorAlert>
      )}

      {files === null ? (
        <LoadingBlock />
      ) : files.length === 0 ? (
        <div className="logs-empty-stack">
          <Empty
            title={t("no log files")}
            action={
              <div className="logs-empty-actions">
                <Link to="/audit-report" className="btn btn--sm btn--primary">
                  <IconReport size={14} /> {t("Audit Report")}
                </Link>
                <Link to="/hubs" className="btn btn--sm">
                  {t("Virtual Hubs")}
                </Link>
                <Link to="/server-settings#ss-keep" className="btn btn--sm">
                  {t("Keep-alive & syslog")}
                </Link>
              </div>
            }
          >
            {isDevV5
              ? t("SoftEther 5.x (--foreground mode, common on OpenWrt/Docker/systemd) writes logs to stdout instead of disk .log files, so EnumLogFile returns 0 files. Check system logs (logread / docker logs / journalctl) or configure Syslog forwarding.")
              : t("Logging may be switched off, running in foreground stdout mode (SoftEther 5.x --foreground), or pruned when free disk space drops below 100 MB.")}
          </Empty>
          <AuditCard />
        </div>
      ) : (
        <div className="card logbench">
          <div className="lograil">
            <input
              type="search"
              className="input mono"
              style={{ minHeight: 36, fontSize: "var(--t-micro)" }}
              placeholder={t("Filter log files...")}
              value={query}
              onChange={(e) => setQuery(e.target.value)}
            />
            <div className="lograil__list">
              {filteredFiles.map((f) => {
                const path = String(f.FilePath_str ?? "");
                const srv = String(f.ServerName_str ?? "");
                const on =
                  String(current?.FilePath_str ?? "") === path &&
                  String(current?.ServerName_str ?? "") === srv;
                return (
                  <button
                    key={`${srv}:${path}`}
                    type="button"
                    className={`lograil__item${on ? " on" : ""}`}
                    onClick={() => tail(f)}
                  >
                    <div className="lograil__top">
                      <span>{formatBytes(Number(f.FileSize_u32))}</span>
                      <span>{timeAgo(f.UpdatedTime_dt as string)}</span>
                    </div>
                    <div className="lograil__path" title={path}>
                      <IconLogs size={12} style={{ display: "inline", marginInlineEnd: 6, verticalAlign: -1 }} />
                      {path}
                    </div>
                  </button>
                );
              })}
              {!filteredFiles.length && (
                <div className="micro" style={{ padding: "var(--s4)", textAlign: "center" }}>
                  {t("No matching results")}
                </div>
              )}
            </div>
          </div>

          <div className="logbench__view">
            {current ? (
              <>
                <Terminal lines={text ? text.split("\n") : []} live={false} label={String(current.FilePath_str)} />
                <div className="logbench__actions">
                  <button className="btn btn--sm" disabled={loading} onClick={() => void read(current, offset, true)}>
                    {loading && <span className="spin" />} {t("Read more")}
                  </button>
                  <button className="btn btn--sm" disabled={loading} onClick={() => tail(current)}>
                    {t("Jump to tail")}
                  </button>
                </div>
              </>
            ) : (
              <div style={{ padding: "var(--s12) var(--s6)", textAlign: "center", margin: "auto" }}>
                <div className="empty__t">{t("pick a file")}</div>
                <p className="empty__s">{t("The last chunk of the file loads first; page forward from there.")}</p>
              </div>
            )}
          </div>
        </div>
      )}
    </div>
  );
}
