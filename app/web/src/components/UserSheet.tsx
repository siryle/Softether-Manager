"use client";

import { useEffect, useRef, useState } from "react";
import { useServerApi, ApiError, type Quota, type Wire } from "../lib/api";
import { AUTH_TYPE_OPTIONS, isNever, userBytes } from "../lib/se";
import { useToast } from "../lib/toast";
import { useI18n } from "../lib/i18n";
import { fileToBase64 } from "../lib/util";
import { Sheet } from "../ui/Sheet";
import { ErrorAlert, Field } from "./bits";
import {
  EMPTY_DRAFT,
  UserQuotaBlock,
  draftAmount,
  draftOf,
  saveUserQuotaDraft,
  type QuotaDraft,
} from "./QuotaCard";

/**
 * Create a user, or edit one's identity: name, group, authentication, expiry,
 * and how much traffic it may move. The security policy is deliberately NOT
 * here -- it is a different decision with forty knobs, and it lives on the
 * user's own page.
 *
 * Editing merges into what the server already has (GetUser -> SetUser), so an
 * untouched field survives the round trip. The password is only ever sent
 * when a new one was typed.
 *
 * The traffic limit is a panel record rather than a SoftEther one, so it is a
 * second write after the user exists -- which is also why it cannot be part
 * of the same atomic save. The amount is validated *before* anything is sent,
 * so a typo cannot leave a user created with no limit; if the second write
 * fails anyway, the sheet stays open and says exactly what did land.
 *
 * Resetting the transfer rides the same write. SoftEther counts forever and
 * has no RPC to zero a counter, so the panel keeps its own zero and every
 * figure it shows subtracts it -- which is why this is a checkbox on the
 * profile rather than a button that fires out of an unsaved form.
 */
export function UserSheet({
  hub,
  hubChoices,
  onHubChange,
  existing,
  onClose,
  onSaved,
}: {
  hub: string;
  /** When creating from the all-users page: pick which hub the user lives in. */
  hubChoices?: string[];
  onHubChange?: (hub: string) => void;
  /** The GetUser payload when editing; absent when creating. */
  existing?: Wire;
  onClose: () => void;
  onSaved: (name: string) => void;
}) {
  const api = useServerApi();
  const { t } = useI18n();
  const editing = Boolean(existing);
  const [name, setName] = useState(String(existing?.Name_str ?? ""));
  const [realname, setRealname] = useState(String(existing?.Realname_utf ?? ""));
  const [note, setNote] = useState(String(existing?.Note_utf ?? ""));
  const [group, setGroup] = useState(String(existing?.GroupName_str ?? ""));
  const [groups, setGroups] = useState<string[]>([]);
  const [authType, setAuthType] = useState<number>(Number(existing?.AuthType_u32 ?? 1));
  const [password, setPassword] = useState("");
  const [radiusUser, setRadiusUser] = useState(String(existing?.RadiusUsername_utf ?? ""));
  const [ntUser, setNtUser] = useState(String(existing?.NtUsername_utf ?? ""));
  const [commonName, setCommonName] = useState(String(existing?.CommonName_utf ?? ""));
  const [certBase64, setCertBase64] = useState("");
  const [expires, setExpires] = useState(() => {
    const raw = existing?.ExpireTime_dt as string | undefined;
    if (!raw || isNever(raw)) return "";
    const d = new Date(raw);
    d.setMinutes(d.getMinutes() - d.getTimezoneOffset());
    return d.toISOString().slice(0, 16);
  });
  const [error, setError] = useState<string | null>(null);
  const [busy, setBusy] = useState(false);
  const [quota, setQuota] = useState<Quota | null>(null);
  const [quotaDraft, setQuotaDraft] = useState<QuotaDraft>(EMPTY_DRAFT);
  const quotaTouched = useRef(false);
  const [quotaLoaded, setQuotaLoaded] = useState(!editing);
  const profile = [realname, note, group, authType, radiusUser, ntUser, commonName, expires];
  const initialProfile = useRef(profile);
  const changed = (index: number) => !editing || profile[index] !== initialProfile.current[index];
  const { push } = useToast();

  useEffect(() => {
    void api
      .groups(hub)
      .then((r) => setGroups(((r.GroupList as Wire[]) ?? []).map((g) => String(g.Name_str))))
      .catch(() => {});
  }, [hub]);

  // The config's ceiling, if it has one. A 404 is the ordinary answer for
  // "no limit here" and leaves the block switched off.
  useEffect(() => {
    if (!editing) return;
    let stale = false;
    void api
      .userQuota(hub, String(existing?.Name_str))
      .then((q) => {
        if (stale) return;
        setQuota(q);
        // Only seed the form if it has not been touched: the read is fast, but
        // an operator who got there first must not have their edit overwritten.
        setQuotaDraft((current) => (quotaTouched.current ? current : draftOf(q)));
        setQuotaLoaded(true);
      })
      .catch((e) => {
        if (stale) return;
        if (e instanceof ApiError && e.status === 404) {
          setQuotaLoaded(true);
        } else {
          push("err", e instanceof Error ? e.message : String(e));
        }
      });
    return () => {
      stale = true;
    };
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [editing, hub, existing, push]);

  const save = async () => {
    if (!quotaLoaded) return;
    setError(null);
    setBusy(true);
    try {
      const body: Wire = {};
      if (changed(0)) body.Realname_utf = realname;
      if (changed(1)) body.Note_utf = note;
      if (changed(2) && editing) body.GroupName_str = group;
      if (changed(3)) body.AuthType_u32 = authType;
      if (changed(4) && authType === 4) body.RadiusUsername_utf = radiusUser;
      if (changed(5) && authType === 5) body.NtUsername_utf = ntUser;
      if ((changed(3) || changed(6)) && authType === 3) body.CommonName_utf = commonName;
      if (changed(7)) body.ExpireTime_dt = expires ? new Date(expires).toISOString() : "1970-01-01T00:00:00.000";
      if (authType === 1 && password) body.Auth_Password_str = password;
      if (authType === 2 && certBase64) body.UserX_bin = certBase64;

      const userName = editing ? String(existing?.Name_str) : name.trim();
      // Validated before the first write, so a bad limit cannot leave a user
      // half-configured behind it.
      if (quotaDraft.on) draftAmount(quotaDraft);
      if (editing) {
        if (Object.keys(body).length) await api.setUser(hub, userName, body);
      } else {
        if (authType === 1 && !password) throw new Error("A password-authenticated user needs a password.");
        body.Name_str = userName;
        if (group) body.GroupName_str = group;
        await api.createUser(hub, body);
      }
      try {
        if (!editing || quotaTouched.current || quotaDraft.reset) {
          await saveUserQuotaDraft(api, hub, userName, quotaDraft, Boolean(quota));
        }
      } catch (e) {
        setError(
          editing
            ? t("The profile was saved, but its traffic limit was not: {error}", { error: e instanceof Error ? e.message : String(e) })
            : t("User {name} was created, but its traffic limit was not: {error}", { name: userName, error: e instanceof Error ? e.message : String(e) }),
        );
        return;
      }
      push("ok", editing ? t("User updated.") : t("User {name} created.", { name: userName }));
      onSaved(userName);
    } catch (e) {
      setError(e instanceof Error ? e.message : String(e));
    } finally {
      setBusy(false);
    }
  };

  return (
    <Sheet
      title={editing ? `${t("Edit user")} ${existing?.Name_str}` : t("New user")}
      subtitle={`${t("Hub")} ${hub}`}
      onClose={onClose}
      footer={
        <>
          <button className="btn" onClick={onClose}>{t("Cancel")}</button>
          <button
            className="btn btn--primary"
            onClick={save}
            disabled={busy || !quotaLoaded || (!editing && !name.trim())}
          >
            {(busy || !quotaLoaded) && <span className="spin" />}
            {editing ? t("Save") : t("Create user")}
          </button>
        </>
      }
    >
      <div style={{ display: "grid", gap: "var(--s1)" }}>
        {error && <ErrorAlert>{error}</ErrorAlert>}
        {!editing && hubChoices && hubChoices.length > 1 && onHubChange && (
          <Field label={t("Virtual Hub")} hint={t("Where this user lives.")}>
            <select className="select" value={hub} onChange={(e) => onHubChange(e.target.value)}>
              {hubChoices.map((h) => (
                <option key={h} value={h}>{h}</option>
              ))}
            </select>
          </Field>
        )}
        {!editing && (
          <Field label={t("Username")} hint={t("Letters, digits, - and _ are safe.")}>
            <input className="input mono" value={name} onChange={(e) => setName(e.target.value)}
              autoFocus autoCapitalize="none" autoCorrect="off" spellCheck={false} />
          </Field>
        )}
        <div className="row2">
          <Field label={t("Full name")}>
            <input className="input" value={realname} onChange={(e) => setRealname(e.target.value)} />
          </Field>
          <Field label={t("Group")} hint={t("Group members inherit the group's security policy.")}>
            <select className="select" value={group} onChange={(e) => setGroup(e.target.value)}>
              <option value="">— {t("none")} —</option>
              {groups.map((g) => (
                <option key={g} value={g}>{g}</option>
              ))}
            </select>
          </Field>
        </div>
        <Field label={t("Note")}>
          <input className="input" value={note} onChange={(e) => setNote(e.target.value)} />
        </Field>

        <Field label={t("Authentication")}>
          <select className="select" value={authType} onChange={(e) => setAuthType(Number(e.target.value))}>
            {AUTH_TYPE_OPTIONS.map((o) => (
              <option key={o.value} value={o.value}>{t(o.label)}</option>
            ))}
          </select>
          <div className="hint">{AUTH_TYPE_OPTIONS.find((o) => o.value === authType)?.hint}</div>
        </Field>

        {authType === 1 && (
          <Field label={editing ? t("New password (leave empty to keep)") : t("Password")}>
            <input className="input" type="password" value={password} onChange={(e) => setPassword(e.target.value)} autoComplete="new-password" />
          </Field>
        )}
        {authType === 2 && (
          <Field label={editing && existing?.UserX_bin ? t("Replace certificate (optional)") : t("User certificate")} hint="X.509, DER or PEM.">
            <input
              className="input"
              type="file"
              accept=".cer,.crt,.pem,.der"
              onChange={async (e) => {
                const f = e.target.files?.[0];
                if (f) setCertBase64(await fileToBase64(f));
              }}
            />
          </Field>
        )}
        {authType === 3 && (
          <Field label={t("Limit to common name")} hint="Optional: only certificates carrying this CN. Empty accepts any trusted-signed certificate.">
            <input className="input mono" value={commonName} onChange={(e) => setCommonName(e.target.value)} spellCheck={false} />
          </Field>
        )}
        {authType === 4 && (
          <Field label={t("RADIUS username")} hint="Empty uses the VPN username.">
            <input className="input mono" value={radiusUser} onChange={(e) => setRadiusUser(e.target.value)} spellCheck={false} autoCapitalize="none" />
          </Field>
        )}
        {authType === 5 && (
          <Field label={t("NT domain username")} hint="Empty uses the VPN username.">
            <input className="input mono" value={ntUser} onChange={(e) => setNtUser(e.target.value)} spellCheck={false} autoCapitalize="none" />
          </Field>
        )}

        <Field label={t("Expires")} hint={t("After this moment the user cannot connect. Empty means never.")}>
          <input className="input mono" type="datetime-local" value={expires} onChange={(e) => setExpires(e.target.value)} />
        </Field>

        {quotaLoaded && <UserQuotaBlock
          quota={quota}
          draft={quotaDraft}
          raw={existing ? userBytes(existing) : undefined}
          onChange={(next) => {
            setQuotaDraft(next);
            quotaTouched.current = true;
          }}
        />}
      </div>
    </Sheet>
  );
}
