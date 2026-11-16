"use client";

import { useState } from "react";
import { Field, LoadingBlock, PageHead } from "../components/bits";
import { api, type Wire } from "../lib/api";
import { useI18n } from "../lib/i18n";
import { Link, seg } from "../lib/router";
import { useServer } from "../lib/server";
import { HUB_TYPES } from "../lib/se";
import { useToast } from "../lib/toast";
import { formatCount } from "../lib/util";
import { IconPlus } from "../ui/Icon";
import { Sheet } from "../ui/Sheet";

export function Hubs() {
  const { hubs, refresh } = useServer();
  const { t } = useI18n();
  const [creating, setCreating] = useState(false);

  return (
    <div className="page">
      <PageHead
        title={t("Virtual Hubs")}
        sub={t("Broadcast domain isolation and policies")}
        actions={
          <button className="btn-primary" onClick={() => setCreating(true)}>
            <IconPlus size={14} /> {t("Create hub")}
          </button>
        }
      />
      <div className="panel-card">
        <HubList hubs={hubs} />
      </div>
      {creating && (
        <CreateHubSheet
          onClose={() => setCreating(false)}
          onCreated={() => {
            setCreating(false);
            void refresh();
          }}
        />
      )}
    </div>
  );
}

export function HubList({ hubs }: { hubs: Wire[] | null | undefined }) {
  const { t } = useI18n();
  if (!hubs) return <LoadingBlock />;
  return (
    <div className="divide-y divide-hairline">
      {hubs.length === 0 ? (
        <p className="text-xs text-muted py-3">
          {t("A Virtual Hub is where users, sessions and access control live. Create the first one.")}
        </p>
      ) : (
        hubs.map((h) => {
          const name = String(h.HubName_str ?? "");
          const online = Boolean(h.Online_bool);
          const hubTypeRaw = h.HubType_u32 !== undefined ? HUB_TYPES[h.HubType_u32 as number]?.split(" ")[0] : "";
          const hubTypeTranslated = hubTypeRaw ? t(hubTypeRaw) : "Standalone";
          return (
            <Link
              key={name}
              to={`/hub/${seg(name)}`}
              className="hub-topology-row p-3 rounded-md bg-raised border border-hairline flex items-center justify-between hover:border-blue-500/40 transition-colors cursor-pointer"
              style={{ marginTop: 6 }}
            >
              <div className="flex items-center gap-3 min-w-0">
                <div className="w-8 h-8 rounded bg-blue-600/10 text-blue-500 font-mono font-semibold flex items-center justify-center text-xs border border-blue-500/20 shrink-0">
                  HUB
                </div>
                <div className="min-w-0">
                  <div className="flex items-center gap-2 flex-wrap">
                    <span className="font-mono font-semibold text-sm text-primary truncate">{name}</span>
                    <span className={`text-[10px] font-mono px-1.5 py-0.5 rounded border ${online ? "bg-emerald-500/10 text-emerald-500 border-emerald-500/20" : "bg-slate-500/10 text-slate-400 border-slate-500/20"}`}>
                      {online ? t("Online") : t("Offline")}
                    </span>
                    <span className="text-[11px] text-muted">{hubTypeTranslated}</span>
                  </div>
                  <div className="text-xs text-muted mt-1 font-mono flex items-center gap-2 flex-wrap">
                    <span>{formatCount(h.NumUsers_u32)} {t("users")}</span>
                    <span>·</span>
                    <span>{formatCount(h.NumSessions_u32)} {t("sessions")}</span>
                    <span>·</span>
                    <span>{formatCount(h.NumMacTables_u32 ?? 1)} MAC</span>
                  </div>
                </div>
              </div>
              <div className="hub-topology-row__go flex items-center gap-2 shrink-0">
                <span className="text-xs text-muted hover:text-primary font-medium transition-colors">
                  {t("View details →")}
                </span>
              </div>
            </Link>
          );
        })
      )}
    </div>
  );
}

export function CreateHubSheet({ onClose, onCreated }: { onClose: () => void; onCreated: () => void }) {
  const { t } = useI18n();
  const [name, setName] = useState("");
  const [password, setPassword] = useState("");
  const [online, setOnline] = useState(true);
  const [busy, setBusy] = useState(false);
  const { guard } = useToast();

  const create = async () => {
    setBusy(true);
    const body: Wire = { HubName_str: name.trim(), Online_bool: online, HubType_u32: 0 };
    if (password) body.AdminPasswordPlainText_str = password;
    const ok = await guard(() => api.createHub(body), t("Hub {name} created.", { name: name.trim() }));
    setBusy(false);
    if (ok) onCreated();
  };

  return (
    <Sheet
      title={t("Create hub")}
      onClose={onClose}
      footer={
        <>
          <button className="btn-secondary" onClick={onClose}>{t("Cancel")}</button>
          <button className="btn-primary" onClick={create} disabled={busy || !name.trim()}>
            {busy && <span className="spin" />} {t("Create hub")}
          </button>
        </>
      }
    >
      <Field label={t("Name")} hint={t("Letters, digits, - and _ are safe.")}>
        <input
          className="input mono"
          value={name}
          onChange={(e) => setName(e.target.value)}
          autoFocus
          autoCapitalize="none"
          spellCheck={false}
          placeholder="e.g. VPN_CORP"
        />
      </Field>
      <Field label={t("Hub password")} hint={t("Optional — lets someone administer only this hub.")}>
        <input
          className="input"
          type="password"
          value={password}
          onChange={(e) => setPassword(e.target.value)}
          autoComplete="off"
          placeholder={t("Leave blank for no dedicated password")}
        />
      </Field>
      <label className="checkrow" style={{ marginTop: 12 }}>
        <input type="checkbox" checked={online} onChange={(e) => setOnline(e.target.checked)} />
        <span><span className="t">{t("Bring it online now")}</span></span>
      </label>
    </Sheet>
  );
}
