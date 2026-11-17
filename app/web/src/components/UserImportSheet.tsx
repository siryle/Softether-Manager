"use client";

import { useRef, useState } from "react";
import { api, type Wire } from "../lib/api";
import { useI18n } from "../lib/i18n";
import { downloadText } from "../lib/util";
import { Sheet } from "../ui/Sheet";
import { Field } from "./bits";
import { useToast } from "../lib/toast";

const TEMPLATE = [
  "name,realname,note,group,auth_type,password,password_hash,expires",
  "alice,Alice Chen,on call this week,staff,password,s3cretpet,,",
  "bob,Bob Li,,,password,,<base64 SHA-0 hash from an export>,2027-01-01",
].join("\n");

/**
 * Bulk-create users from a CSV. The header names the columns, so a file
 * exported from this panel can be filled in and imported back: existing names
 * are skipped rather than overwritten, and every row that fails reports why.
 */
export function UserImportSheet({ hub, onClose, onImported }: { hub: string; onClose: () => void; onImported: () => void }) {
  const { t } = useI18n();
  const { push } = useToast();
  const [text, setText] = useState("");
  const [fileName, setFileName] = useState("");
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState<string | null>(null);
  const [result, setResult] = useState<{ created: string[]; skipped: Wire[]; failed: Wire[] } | null>(null);
  const fileInput = useRef<HTMLInputElement>(null);

  const rows = text.trim() ? Math.max(0, text.trim().split("\n").length - 1) : 0;

  const pick = async (file: File | null | undefined) => {
    if (!file) return;
    setError(null);
    setResult(null);
    try {
      setText(await file.text());
      setFileName(file.name);
    } catch (e) {
      setError(e instanceof Error ? e.message : String(e));
    }
  };

  const run = async () => {
    setBusy(true);
    setError(null);
    try {
      const out = await api.importUsers(hub, text);
      setResult({ created: out.created ?? [], skipped: out.skipped ?? [], failed: out.failed ?? [] });
      if ((out.created ?? []).length) {
        push("ok", t("{count} users created.", { count: (out.created ?? []).length }));
        onImported();
      }
    } catch (e) {
      setError(e instanceof Error ? e.message : String(e));
    } finally {
      setBusy(false);
    }
  };

  return (
    <Sheet
      title={t("Import users")}
      subtitle={`${t("Hub")} ${hub}`}
      onClose={onClose}
      wide
      footer={
        <>
          <button className="btn" onClick={onClose}>{result ? t("Close") : t("Cancel")}</button>
          <button className="btn btn--primary" onClick={() => void run()} disabled={busy || !text.trim()}>
            {busy && <span className="spin" />} {t(busy ? "Importing…" : "Import")}
          </button>
        </>
      }
    >
      {error && <div className="alert alert--err" style={{ marginBottom: "var(--s3)" }}>{error}</div>}

      {result ? (
        <>
          <p className="lede" style={{ marginBottom: "var(--s3)" }}>
            {t("{created} created · {skipped} skipped · {failed} failed", {
              created: result.created.length,
              skipped: result.skipped.length,
              failed: result.failed.length,
            })}
          </p>
          {result.created.length > 0 && (
            <div style={{ marginBottom: "var(--s3)" }}>
              <div className="section__t">{t("Created users")}</div>
              <p className="mono" style={{ margin: 0, overflowWrap: "anywhere" }}>{result.created.join(", ")}</p>
            </div>
          )}
          {result.skipped.length > 0 && (
            <div style={{ marginBottom: "var(--s3)" }}>
              <div className="section__t">{t("Skipped")}</div>
              <ul style={{ margin: 0, paddingInlineStart: "1.2em" }}>
                {result.skipped.map((row, i) => (
                  <li key={i} className="mono">{String(row.name)} — {t(String(row.reason))}</li>
                ))}
              </ul>
            </div>
          )}
          {result.failed.length > 0 && (
            <div>
              <div className="section__t">{t("Failed")}</div>
              <ul style={{ margin: 0, paddingInlineStart: "1.2em" }}>
                {result.failed.map((row, i) => (
                  <li key={i} className="mono">
                    {t("Line {line}", { line: String(row.line) })} · {String(row.name)} — {String(row.error)}
                  </li>
                ))}
              </ul>
            </div>
          )}
        </>
      ) : (
        <>
          <p className="lede" style={{ marginBottom: "var(--s3)" }}>
            {t("The first line names the columns; every following line is one user. A file exported from this panel can be imported back as it is: the password_hash column restores the original passwords.")}
          </p>
          <Field label={t("CSV file")} hint={t("Or paste the rows below.")}>
            <input
              ref={fileInput}
              className="input"
              type="file"
              accept=".csv,text/csv,text/plain"
              onChange={(e) => void pick(e.target.files?.[0])}
            />
          </Field>
          <Field
            label={t("Rows")}
            hint={t("Columns: name, realname, note, group, auth_type, password, password_hash, expires. Give a password, or the password_hash an export carries — that keeps the original password working. auth_type accepts password, anonymous, certificate, signed, radius, nt — or 0–5. Existing users are skipped.")}
          >
            <textarea
              className="input mono"
              style={{ minHeight: 180, resize: "vertical" }}
              value={text}
              onChange={(e) => { setText(e.target.value); setFileName(""); setResult(null); }}
              placeholder={TEMPLATE}
              spellCheck={false}
            />
          </Field>
          <div style={{ display: "flex", gap: "var(--s2)", flexWrap: "wrap", alignItems: "center" }}>
            <button className="btn btn--sm" onClick={() => downloadText("users-template.csv", TEMPLATE + "\n", "text/csv")}>
              {t("Download CSV template")}
            </button>
            <span className="hint" style={{ margin: 0 }}>
              {fileName ? `${fileName} · ${t("{count} rows", { count: rows })}` : rows ? t("{count} rows", { count: rows }) : ""}
            </span>
          </div>
        </>
      )}
    </Sheet>
  );
}
