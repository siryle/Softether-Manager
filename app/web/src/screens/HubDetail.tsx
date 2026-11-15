"use client";

import { useCallback, useState } from "react";
import { LoadingBlock, PageHead, SectionTitle, usePoll } from "../components/bits";
import { QuotaStatePill, quotaKey, useQuotaIndex } from "../components/QuotaCard";
import { RangeSeg, TrafficChart } from "../components/TrafficChart";
import { api, type Usage, type Wire } from "../lib/api";
import { HUB_TYPES } from "../lib/se";
import { Link, navigate, seg } from "../lib/router";
import { formatBytes, formatCount, formatDate } from "../lib/util";
import { useI18n, t } from "../lib/i18n";
import { OnlinePill } from "../ui/Status";
import { OutcomeNote, Switch, useToggle } from "../ui/Switch";
import { HubUsers } from "./hub/HubUsers";
import { HubGroups } from "./hub/HubGroups";
import { HubSessions } from "./hub/HubSessions";
import { HubAccess } from "./hub/HubAccess";
import { HubSecurity } from "./hub/HubSecurity";
import { HubSecureNat } from "./hub/HubSecureNat";
import { HubLinks } from "./hub/HubLinks";
import { HubTables } from "./hub/HubTables";
import { HubSettings } from "./hub/HubSettings";

/**
 * One Virtual Hub. The hub is where SoftEther's day-to-day lives -- users,
 * sessions, access control -- so this page is a small application of its own:
 * a header that says how the hub is doing, and a tab strip for its rooms.
 */

const TABS: { key: string; label: string }[] = [
  { key: "overview", label: "Overview" },
  { key: "users", label: "Users" },
  { key: "groups", label: "Groups" },
  { key: "sessions", label: "Sessions" },
  { key: "access", label: "Access control" },
  { key: "security", label: "Security" },
  { key: "securenat", label: "SecureNAT" },
  { key: "links", label: "Cascade" },
  { key: "tables", label: "Tables" },
  { key: "settings", label: "Hub settings" },
];

export function HubDetail({ hub, tab }: { hub: string; tab: string }) {
  const { t } = useI18n();
  const [status, setStatus] = useState<Wire | null>(null);
  const quotas = useQuotaIndex([hub]);

  const load = useCallback(async () => {
    const next = await api.hubStatus(hub).catch(() => null);
    setStatus(next);
    return next ? Boolean(next.Online_bool) : null;
  }, [hub]);
  usePoll(load, "detail", [hub]);

  const online = Boolean(status?.Online_bool);

  // The hub's switch: the request flips it, the read-back decides what the
  // header shows, and the control is locked in between.
  const onlineToggle = useToggle({
    value: status ? online : null,
    apply: async (next) => {
      const r = await api.hubOnline(hub, next);
      setStatus((current) => ({ ...(current ?? {}), ...r }));
      return Boolean(r.online);
    },
    reload: load,
    noun: t("Hub {hub}", { hub }),
    onWord: "online",
    offWord: "offline",
  });

  return (
    <div className="page">
      <PageHead
        title={
          <span style={{ display: "inline-flex", alignItems: "center", gap: "var(--s3)", minWidth: 0, flexWrap: "wrap" }}>
            <span className="truncate">{hub}</span>
            {status && <OnlinePill online={online} />}
            <QuotaStatePill quota={quotas.get(quotaKey("hub", hub))} />
          </span>
        }
        sub={
          <>
            <Link to={`/`} className="linkish">← {t("Dashboard")}</Link>
            {status && (
              <>
                {" "}· {t(HUB_TYPES[Number(status.HubType_u32 ?? 0)]?.split(" ")[0] ?? "")} {t("Hub")}
              </>
            )}
          </>
        }
        actions={
          status && (
            <span className="switchbox">
              <Switch
                on={online}
                pending={onlineToggle.pending}
                target={onlineToggle.target}
                onToggle={() => void onlineToggle.toggle()}
                label={t("Hub online")}
                onWord="online"
                offWord="offline"
              />
              <OutcomeNote outcome={onlineToggle.outcome} />
            </span>
          )
        }
      />

      <div className="tabs" role="tablist" aria-label={t("Hub sections")}>
        {TABS.map((tabItem) => (
          <button
            key={tabItem.key}
            role="tab"
            aria-selected={tab === tabItem.key}
            className={`tabs__i${tab === tabItem.key ? " on" : ""}`}
            onClick={() => navigate(`/hub/${seg(hub)}${tabItem.key === "overview" ? "" : `/${tabItem.key}`}`)}
          >
            {t(tabItem.label)}
          </button>
        ))}
      </div>

      {tab === "overview" && <HubOverview hub={hub} status={status} />}
      {tab === "users" && <HubUsers hub={hub} />}
      {tab === "groups" && <HubGroups hub={hub} />}
      {tab === "sessions" && <HubSessions hub={hub} />}
      {tab === "access" && <HubAccess hub={hub} />}
      {tab === "security" && <HubSecurity hub={hub} />}
      {tab === "securenat" && <HubSecureNat hub={hub} />}
      {tab === "links" && <HubLinks hub={hub} />}
      {tab === "tables" && <HubTables hub={hub} />}
      {tab === "settings" && <HubSettings hub={hub} onChanged={() => void load()} />}
    </div>
  );
}

function HubOverview({ hub, status }: { hub: string; status: Wire | null }) {
  const { t } = useI18n();
  const [hours, setHours] = useState(24);
  const [usage, setUsage] = useState<Usage | null>(null);

  const load = useCallback(async () => {
    setUsage(await api.hubTraffic(hub, hours).catch(() => null));
  }, [hub, hours]);
  usePoll(load, "list", [hub, hours]);

  if (!status) return <LoadingBlock label="asking the hub" />;

  const recv = (Number(status["Recv.UnicastBytes_u64"]) || 0) + (Number(status["Recv.BroadcastBytes_u64"]) || 0);
  const send = (Number(status["Send.UnicastBytes_u64"]) || 0) + (Number(status["Send.BroadcastBytes_u64"]) || 0);

  return (
    <>
      <div className="fleet stagger">
        <div className="fleet__cell">
          <div className="fleet__n">{formatCount(status.NumSessions_u32)}</div>
          <div className="micro">{t("Sessions")}</div>
        </div>
        <div className="fleet__cell">
          <div className="fleet__n">{formatCount(status.NumUsers_u32)}</div>
          <div className="micro">{t("Users")}</div>
        </div>
        <div className="fleet__cell">
          <div className="fleet__n">{formatCount(status.NumGroups_u32)}</div>
          <div className="micro">{t("Groups")}</div>
        </div>
        <div className="fleet__cell">
          <div className="fleet__n">{formatCount(status.NumMacTables_u32)}</div>
          <div className="micro">{t("MAC entries")}</div>
        </div>
        <div className="fleet__cell">
          <div className="fleet__n">{formatBytes(send + recv)}</div>
          <div className="micro">{t("lifetime traffic")}</div>
        </div>
      </div>

      <SectionTitle actions={<RangeSeg hours={hours} onChange={setHours} />}>{t("Throughput")}</SectionTitle>
      <div className="card" style={{ padding: "var(--s4)" }}>
        {usage ? <TrafficChart usage={usage} /> : <LoadingBlock label="loading samples" />}
      </div>

      <SectionTitle>{t("Details")}</SectionTitle>
      <div className="card" style={{ padding: "var(--s4)" }}>
        <div className="kv">
          <div><div className="micro">{t("Created")}</div><div className="mono">{formatDate(status.CreatedTime_dt as string)}</div></div>
          <div><div className="micro">{t("last comm")}</div><div className="mono">{formatDate(status.LastCommTime_dt as string)}</div></div>
          <div><div className="micro">{t("last login")}</div><div className="mono">{formatDate(status.LastLoginTime_dt as string)}</div></div>
          <div><div className="micro">{t("Logins")}</div><div className="mono">{formatCount(status.NumLogin_u32 as number)}</div></div>
          <div><div className="micro">{t("IP entries")}</div><div className="mono">{formatCount(status.NumIpTables_u32 as number)}</div></div>
          <div><div className="micro">{t("SecureNAT")}</div><div className="mono">{status.SecureNATEnabled_bool ? t("enabled") : t("off")}</div></div>
        </div>
      </div>
    </>
  );
}
