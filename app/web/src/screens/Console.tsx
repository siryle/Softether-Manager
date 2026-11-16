"use client";

import { useEffect, useMemo, useState } from "react";
import { Empty, ErrorAlert, LoadingBlock, PageHead, SearchBox } from "../components/bits";
import { api, ApiError, type Wire } from "../lib/api";
import { Link } from "../lib/router";
import { useToast } from "../lib/toast";
import { useI18n, t } from "../lib/i18n";
import { IconPlay, IconTerminal } from "../ui/Icon";

/**
 * The API console: all 135 documented RPC methods, callable raw.
 *
 * The REST screens cover everything in a shaped way; this is the unshaped
 * way, for the operator following the SoftEther reference -- and the proof
 * that nothing the server can do is beyond the panel's reach.
 */
export function Console() {
  const { t } = useI18n();
  const [methods, setMethods] = useState<Wire[] | null>(null);
  const [query, setQuery] = useState("");
  const [selected, setSelected] = useState<Wire | null>(null);
  const [params, setParams] = useState("{}");
  const [result, setResult] = useState<string | null>(null);
  const [error, setError] = useState<string | null>(null);
  const [notice, setNotice] = useState<string | null>(null);
  const [busy, setBusy] = useState(false);
  const { push } = useToast();

  useEffect(() => {
    void api.rpcMethods().then(setMethods).catch(() => setMethods([]));
  }, []);

  const filtered = useMemo(() => {
    if (!methods) return null;
    const q = query.trim().toLowerCase();
    if (!q) return methods;
    return methods.filter(
      (m) => String(m.name).toLowerCase().includes(q) || String(m.title).toLowerCase().includes(q),
    );
  }, [methods, query]);

  const pick = (m: Wire) => {
    setSelected(m);
    setResult(null);
    setError(null);
    setNotice(null);
    setParams(JSON.stringify(m.input ?? {}, null, 2));
  };

  const call = async () => {
    if (!selected) return;
    setNotice(null);
    let parsed: Wire;
    try {
      parsed = params.trim() ? JSON.parse(params) : {};
    } catch {
      setError("The parameters are not valid JSON.");
      return;
    }
    setBusy(true);
    setError(null);
    try {
      const r = await api.rpcCall(String(selected.name), parsed);
      setResult(JSON.stringify(r, null, 2));
      push("ok", t("{name} answered.", { name: String(selected.name) }));
    } catch (e) {
      const message = e instanceof Error ? e.message : String(e);
      setError(message);
      setResult(null);
      if (e instanceof ApiError && e.seError === "UnsupportedError") {
        let hint = t("This RPC method is not supported by the current server or is unavailable in its current configuration.");
        if (selected.name === "GetDDnsClientStatus") {
          try {
            const status = await api.ddns();
            if (status.enabled === false) hint = t("Dynamic DNS is disabled. SoftEther returns code 33 for this status query while it is off. You can enable it in Server settings.");
          } catch (statusError) {
            if (!(statusError instanceof ApiError) || statusError.seError !== "UnsupportedError") {
              setError(`${message}\n${t("Could not load dynamic DNS: {error}", { error: statusError instanceof Error ? statusError.message : String(statusError) })}`);
              return;
            }
          }
        }
        setNotice(hint);
      }
    } finally {
      setBusy(false);
    }
  };

  return (
    <div className="page">
      <PageHead
        title={t("API console")}
        sub={t("every documented RPC, raw")}
      />
      <div className="console">
        <div className="console__list">
          {/* the box flexes horizontally; left in the column flow it would
              stretch its height instead */}
          <div style={{ display: "flex" }}>
            <SearchBox value={query} onChange={setQuery} placeholder={t("method name…")} />
          </div>
          <div className="console__methods">
            {filtered === null ? (
              <LoadingBlock />
            ) : (
              filtered.map((m) => (
                <button
                  key={String(m.name)}
                  disabled={busy}
                  className={`console__m${selected?.name === m.name ? " on" : ""}`}
                  onClick={() => pick(m)}
                >
                  <span className="mono">{String(m.name)}</span>
                  <span className="micro truncate">{String(m.title)}</span>
                </button>
              ))
            )}
          </div>
        </div>

        <div className="console__work">
          {!selected ? (
            <Empty title={t("pick a method")}>
              {t("The parameters template fills in with the documented example values; edit and run. Writes here are as real as anywhere else in the panel.")}
            </Empty>
          ) : (
            <>
              <div className="section__t" style={{ display: "flex", alignItems: "center", gap: "var(--s2)" }}>
                <IconTerminal size={16} />
                <span className="mono">{String(selected.name)}</span>
              </div>
              <p className="lede" style={{ margin: "var(--s2) 0 var(--s3)" }}>{String(selected.desc || selected.title)}</p>
              <div className="micro" style={{ marginBottom: 6 }}>{t("parameters")}</div>
              <textarea
                className="textarea mono"
                rows={Math.min(16, Math.max(4, params.split("\n").length))}
                value={params}
                onChange={(e) => setParams(e.target.value)}
                spellCheck={false}
              />
              <div style={{ display: "flex", gap: "var(--s2)", margin: "var(--s3) 0" }}>
                <button className="btn btn--primary" onClick={call} disabled={busy}>
                  {busy ? <span className="spin" /> : <IconPlay size={14} />} {t("Call {name}", { name: String(selected.name) })}
                </button>
              </div>
              {error && (notice ? (
                <div className="alert" role="status">
                  <div>
                    <p>{notice}</p>
                    {selected.name === "GetDDnsClientStatus" && <p><Link className="linkish" to="/server-settings/protocols">{t("Open Server settings")}</Link></p>}
                    <details>
                      <summary>{t("Original RPC error")}</summary>
                      <pre className="mono" style={{ whiteSpace: "pre-wrap", overflowWrap: "anywhere" }}>{error}</pre>
                    </details>
                  </div>
                </div>
              ) : <ErrorAlert>{error}</ErrorAlert>)}
              {result && (
                <>
                  <div className="micro" style={{ margin: "var(--s2) 0 6px" }}>{t("result")}</div>
                  <pre className="console__result mono">{result}</pre>
                </>
              )}
            </>
          )}
        </div>
      </div>
    </div>
  );
}
