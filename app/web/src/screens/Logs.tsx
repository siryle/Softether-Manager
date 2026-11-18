"use client";

import { useCallback, useState } from "react";
import { Empty, LoadingBlock, PageHead, usePoll } from "../components/bits";
import { Terminal } from "../components/Terminal";
import { api, type Wire } from "../lib/api";
import { useToast } from "../lib/toast";
import { useI18n, t } from "../lib/i18n";
import { formatBytes, timeAgo } from "../lib/util";
import { IconLogs } from "../ui/Icon";

/**
 * The server's log files: server log, per-hub security and packet logs.
 * Reading is chunked -- ReadLogFile pages by offset -- and rendered in the
 * same terminal well the installer uses.
 */

const CHUNK_HINT = 256 * 1024; // what one ReadLogFile answer tends to carry

export function Logs() {
  const [files, setFiles] = useState<Wire[] | null>(null);
  const [query, setQuery] = useState("");
  const [current, setCurrent] = useState<Wire | null>(null);
  const [text, setText] = useState("");
  const [offset, setOffset] = useState(0);
  const [loading, setLoading] = useState(false);
  const { push } = useToast();

  const load = useCallback(async () => {
    const r = await api.logFiles().catch(() => null);
    if (r) setFiles((r.LogFiles as Wire[]) ?? []);
  }, []);
  usePoll(load, "list", []);

  const read = async (file: Wire, fromOffset: number, append: boolean) => {
    setLoading(true);
    try {
      const r = await api.readLog(String(file.FilePath_str), fromOffset);
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

  const { t } = useI18n();
  const q = query.trim().toLowerCase();
  const filteredFiles = (files ?? []).filter((f) =>
    !q || String(f.FilePath_str ?? "").toLowerCase().includes(q),
  );

  return (
    <div className="page">
      <PageHead
        title={t("Logs")}
        sub={t("server, hub security and packet logs")}
        actions={
          files !== null && (
            <span className="chip mono">
              {files.length} {t("Files")}
            </span>
          )
        }
      />

      {files === null ? (
        <LoadingBlock />
      ) : files.length === 0 ? (
        <Empty title={t("no log files")}>{t("Logging may be switched off, or nothing has happened yet.")}</Empty>
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
                const path = String(f.FilePath_str);
                const on = current?.FilePath_str === f.FilePath_str;
                return (
                  <button
                    key={path}
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
