"use client";

import { useCallback, useState } from "react";
import { ConfirmSheet, Empty, KV, LoadingBlock, PageHead, usePoll } from "../components/bits";
import { api, type Wire } from "../lib/api";
import { useToast } from "../lib/toast";
import { useI18n, t } from "../lib/i18n";
import { formatDate, formatIPLocation, timeAgo } from "../lib/util";
import { IconClose } from "../ui/Icon";
import { Sheet } from "../ui/Sheet";
import { Pill } from "../ui/Status";

/**
 * Raw TCP connections into the VPN server -- including half-open clients,
 * management links, and this very panel. The lowest-level live view there is.
 */

const CONNECTION_TYPES: Record<number, string> = {
  0: "client",
  1: "initializing",
  2: "login",
  3: "additional",
  4: "RPC / farm",
  5: "admin",
  6: "management",
  7: "management",
};

export function Connections() {
  const { t } = useI18n();
  const [connections, setConnections] = useState<Wire[] | null>(null);
  const [open, setOpen] = useState<string | null>(null);
  const [killing, setKilling] = useState<Wire | null>(null);

  const load = useCallback(async () => {
    const r = await api.connections().catch(() => null);
    if (r) setConnections((r.ConnectionList as Wire[]) ?? []);
  }, []);
  usePoll(load, "live", []);

  return (
    <div className="page">
      <PageHead title={t("Connections")} sub={t("live TCP connections into the VPN server")} />
      {connections === null ? (
        <LoadingBlock label="loading connections" />
      ) : connections.length === 0 ? (
        <Empty title={t("no connections")}>{t("Which would be odd, since this panel is one.")}</Empty>
      ) : (
        <div className="card tcard">
          <div className="tscroll">
            <table className="dtable">
              <thead>
                <tr>
                  <th>{t("CID / Name")}</th>
                  <th>{t("User")}</th>
                  <th>{t("Source & Location")}</th>
                  <th>{t("Type")}</th>
                  <th>{t("Connected")}</th>
                  <th className="tact" style={{ width: 130 }} aria-label={t("Actions")} />
                </tr>
              </thead>
              <tbody>
                {connections.map((c) => (
                  <tr key={String(c.Name_str)} className="clickable" onClick={() => setOpen(String(c.Name_str))}>
                    <td className="tmono bold">{String(c.Name_str)}</td>
                    <td>
                      {c.Username_str ? (
                        <div>
                          <span className="mono bold">{String(c.Username_str)}</span>
                          {c.Realname_utf ? <span style={{ marginLeft: 6, color: "var(--fg)" }}>({String(c.Realname_utf)})</span> : null}
                          {c.Note_utf ? <div className="tsub truncate" style={{ maxWidth: 200, color: "var(--muted)" }}>{String(c.Note_utf)}</div> : null}
                        </div>
                      ) : (
                        <span className="tsub">—</span>
                      )}
                    </td>
                    <td className="tmono">
                      <div>{String(c.Ip_ip)}:{String(c.Port_u32)}</div>
                      {c.IpLocation_str || c.IpIsp_str ? (
                        <div className="tsub" style={{ marginTop: 2, color: "var(--accent)" }}>
                          {formatIPLocation(c.IpLocation_str, c.IpIsp_str)}
                        </div>
                      ) : c.Hostname_str && c.Hostname_str !== c.Ip_ip ? (
                        <div className="tsub">{String(c.Hostname_str)}</div>
                      ) : null}
                    </td>
                    <td><Pill kind="idle" label={CONNECTION_TYPES[Number(c.Type_u32)] ?? t("type {n}", { n: String(c.Type_u32) })} /></td>
                    <td className="tmono">{timeAgo(c.ConnectedTime_dt as string)}</td>
                    <td className="tact">
                      <button
                        className="btn btn--sm btn--danger"
                        onClick={(e) => {
                          e.stopPropagation();
                          setKilling(c);
                        }}
                      >
                        <IconClose size={13} /> {t("Disconnect")}
                      </button>
                    </td>
                  </tr>
                ))}
              </tbody>
            </table>
          </div>
        </div>
      )}

      {open && <ConnectionSheet name={open} onClose={() => setOpen(null)} />}
      {killing && (
        <ConfirmSheet
          title={t("Disconnect this connection?")}
          verb="Disconnect"
          body={
            <>
              <span className="mono">{String(killing.Name_str)}</span> from{" "}
              <span className="mono">{String(killing.Ip_ip)}</span> is cut. Cutting a management
              connection may sign out an administrator — possibly you.
            </>
          }
          onClose={() => setKilling(null)}
          onConfirm={async () => {
            await api.disconnectConnection(String(killing.Name_str));
            await load();
          }}
        />
      )}
    </div>
  );
}

function ConnectionSheet({ name, onClose }: { name: string; onClose: () => void }) {
  const [detail, setDetail] = useState<Wire | null>(null);
  const [error, setError] = useState<string | null>(null);
  const { t } = useI18n();

  usePoll(
    async () => {
      try {
        setDetail(await api.connectionInfo(name));
      } catch (e) {
        setError(e instanceof Error ? e.message : String(e));
      }
    },
    3600_000,
    [name],
  );

  const rows: [string, React.ReactNode][] = [];
  if (detail) {
    if (detail.Username_str) {
      rows.push(["user", String(detail.Username_str)]);
    }
    if (detail.Realname_utf) {
      rows.push(["full name", String(detail.Realname_utf)]);
    }
    if (detail.Note_utf) {
      rows.push(["note", String(detail.Note_utf)]);
    }
    rows.push(["source", `${detail.Hostname_str || detail.Ip_ip}:${detail.Port_u32}`]);
    if (detail.IpLocation_str || detail.IpIsp_str) {
      rows.push(["location", formatIPLocation(detail.IpLocation_str, detail.IpIsp_str)]);
    }
    rows.push(["connected", formatDate(detail.ConnectedTime_dt as string)]);
    rows.push(["client", `${detail.ClientStr_str ?? "—"} ${detail.ClientVer_u32 ?? ""}`]);
    rows.push(["server", `${detail.ServerStr_str ?? "—"} ${detail.ServerVer_u32 ?? ""}`]);
  }

  return (
    <Sheet title={t("Connection")} subtitle={name} onClose={onClose}>
      {error && <div className="alert alert--err">{error}</div>}
      {!detail && !error && <LoadingBlock />}
      {detail && <KV rows={rows} />}
    </Sheet>
  );
}
