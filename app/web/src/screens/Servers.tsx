"use client";

import { useCallback, useEffect, useState } from "react";
import { CheckRow, ErrorAlert, Field, LoadingBlock, PageHead } from "../components/bits";
import { api, type ManagedServer } from "../lib/api";
import { useI18n } from "../lib/i18n";
import { navigate, useRoute } from "../lib/router";
import { useServer } from "../lib/server";
import { useToast } from "../lib/toast";
import {
  IconAlert,
  IconCheck,
  IconNode,
  IconPlus,
  IconPulse,
  IconRefresh,
  IconTrash,
} from "../ui/Icon";
import { Sheet } from "../ui/Sheet";

interface LatencyMap {
  [serverId: number]: {
    latencyMs?: number;
    online: boolean;
    version?: string;
    error?: string;
    testing?: boolean;
  };
}

export function Servers() {
  const { servers, currentServer, switchServer, refreshServers, refresh: refreshActive } = useServer();
  const { t } = useI18n();
  const { push } = useToast();
  const route = useRoute();

  const [latencies, setLatencies] = useState<LatencyMap>({});
  const [editingServer, setEditingServer] = useState<ManagedServer | "new" | null>(null);
  const [deleteConfirmId, setDeleteConfirmId] = useState<number | null>(null);
  const [switchingId, setSwitchingId] = useState<number | null>(null);
  const [testingAll, setTestingAll] = useState(false);

  // Check if route requests adding a server directly (/servers/add)
  useEffect(() => {
    if (route.parts[1] === "add") {
      setEditingServer("new");
    }
  }, [route.parts]);

  const testSingleServer = useCallback(
    async (s: ManagedServer) => {
      setLatencies((prev) => ({
        ...prev,
        [s.id]: { ...prev[s.id], testing: true },
      }));
      try {
        const res = await api.testServerId(s.id);
        setLatencies((prev) => ({
          ...prev,
          [s.id]: {
            latencyMs: res.latency_ms,
            online: res.online,
            version: res.version,
            error: res.error,
            testing: false,
          },
        }));
      } catch (err) {
        setLatencies((prev) => ({
          ...prev,
          [s.id]: {
            online: false,
            error: err instanceof Error ? err.message : String(err),
            testing: false,
          },
        }));
      }
    },
    [],
  );

  const testAllServers = useCallback(async () => {
    if (!servers || servers.length === 0) return;
    setTestingAll(true);
    await Promise.allSettled(servers.map((s) => testSingleServer(s)));
    setTestingAll(false);
  }, [servers, testSingleServer]);

  // Initial latency probe
  useEffect(() => {
    if (servers && servers.length > 0 && Object.keys(latencies).length === 0) {
      void testAllServers();
    }
  }, [servers, testAllServers, latencies]);

  const handleSwitch = async (id: number) => {
    if (id === currentServer?.id) return;
    setSwitchingId(id);
    try {
      await switchServer(id);
      const target = servers?.find((s) => s.id === id);
      push("ok", `${t("Switched to server")}: ${target?.name || id}`);
    } catch (e) {
      push("err", t("Failed to switch server"));
    } finally {
      setSwitchingId(null);
    }
  };

  const handleSetDefault = async (s: ManagedServer) => {
    try {
      await api.setDefaultServer(s.id);
      push("ok", `${s.name} ${t("is now the default server")}`);
      await refreshServers();
    } catch (e) {
      push("err", t("Failed to set default server"));
    }
  };

  const handleDelete = async (s: ManagedServer) => {
    try {
      await api.deleteServer(s.id);
      push("ok", `${s.name} ${t("deleted successfully")}`);
      setDeleteConfirmId(null);
      await refreshServers();
    } catch (e) {
      push("err", e instanceof Error ? e.message : String(e));
    }
  };

  if (!servers) {
    return <LoadingBlock label="Loading servers..." />;
  }

  const totalNodes = servers.length;
  const onlineCount = servers.filter((s) => {
    const lat = latencies[s.id];
    return lat ? lat.online : s.is_enabled;
  }).length;

  return (
    <div className="page">
      <PageHead
        title={t("Server Cluster")}
        sub={t("Multi-node SoftEther VPN instances, routing & diagnostics")}
        actions={
          <div style={{ display: "flex", alignItems: "center", gap: 8 }}>
            <button
              className="btn btn--sm"
              onClick={testAllServers}
              disabled={testingAll}
              title={t("Test all nodes")}
            >
              <IconRefresh size={14} className={testingAll ? "spin" : ""} />
              <span>{testingAll ? t("Testing...") : t("Test pings")}</span>
            </button>
            <button
              className="btn-primary"
              onClick={() => setEditingServer("new")}
            >
              <IconPlus size={14} />
              <span>{t("Add server")}</span>
            </button>
          </div>
        }
      />

      {/* Cluster Overview Stats */}
      <div
        style={{
          display: "grid",
          gridTemplateColumns: "repeat(auto-fit, minmax(200px, 1fr))",
          gap: "var(--s3)",
          marginBottom: "var(--s5)",
        }}
      >
        <div className="card" style={{ padding: "var(--s4)" }}>
          <div style={{ fontSize: "var(--t-micro)", color: "var(--text-muted)", textTransform: "uppercase", letterSpacing: "0.05em" }}>
            {t("Total nodes")}
          </div>
          <div className="mono" style={{ fontSize: "1.75rem", fontWeight: 700, marginTop: 4, color: "var(--text-primary)" }}>
            {totalNodes}
          </div>
          <div style={{ fontSize: "11px", color: "var(--text-secondary)", marginTop: 2 }}>
            {t("Configured VPN server instances")}
          </div>
        </div>

        <div className="card" style={{ padding: "var(--s4)" }}>
          <div style={{ fontSize: "var(--t-micro)", color: "var(--text-muted)", textTransform: "uppercase", letterSpacing: "0.05em" }}>
            {t("Cluster Health")}
          </div>
          <div className="mono" style={{ fontSize: "1.75rem", fontWeight: 700, marginTop: 4, color: onlineCount === totalNodes ? "var(--color-emerald, #10b981)" : "var(--color-amber, #f59e0b)" }}>
            {onlineCount} / {totalNodes}
          </div>
          <div style={{ fontSize: "11px", color: "var(--text-secondary)", marginTop: 2 }}>
            {onlineCount === totalNodes ? t("All nodes responsive") : t("Some nodes require attention")}
          </div>
        </div>

        <div className="card" style={{ padding: "var(--s4)" }}>
          <div style={{ fontSize: "var(--t-micro)", color: "var(--text-muted)", textTransform: "uppercase", letterSpacing: "0.05em" }}>
            {t("Active Instance")}
          </div>
          <div style={{ fontSize: "1.2rem", fontWeight: 700, marginTop: 6, color: "var(--accent)", overflow: "hidden", textOverflow: "ellipsis", whiteSpace: "nowrap" }}>
            {currentServer?.name || "—"}
          </div>
          <div className="mono" style={{ fontSize: "11px", color: "var(--text-secondary)", marginTop: 4 }}>
            {currentServer ? `${currentServer.host}:${currentServer.port}` : "—"}
          </div>
        </div>
      </div>

      {/* Nodes Cards Grid */}
      <div
        style={{
          display: "grid",
          gridTemplateColumns: "repeat(auto-fill, minmax(330px, 1fr))",
          gap: "var(--s4)",
        }}
      >
        {servers.map((s) => {
          const isActive = s.id === currentServer?.id;
          const lat = latencies[s.id];
          const isTesting = lat?.testing || false;
          const isOnline = lat ? lat.online : s.is_enabled;
          const latencyMs = lat?.latencyMs;

          return (
            <div
              key={s.id}
              className="card"
              style={{
                padding: "var(--s4)",
                display: "flex",
                flexDirection: "column",
                justifyContent: "space-between",
                borderColor: isActive ? "var(--accent-glow, rgba(160, 227, 95, 0.4))" : undefined,
                boxShadow: isActive ? "0 0 0 1px var(--accent-glow, rgba(160, 227, 95, 0.3))" : undefined,
              }}
            >
              <div>
                {/* Header: Status, Name, Badges */}
                <div style={{ display: "flex", alignItems: "flex-start", justifyContent: "space-between", gap: 8, marginBottom: "var(--s3)" }}>
                  <div style={{ display: "flex", alignItems: "center", gap: 8, minWidth: 0 }}>
                    <span
                      style={{
                        width: 8,
                        height: 8,
                        borderRadius: "50%",
                        backgroundColor: !s.is_enabled
                          ? "var(--text-muted)"
                          : isOnline
                            ? "var(--color-emerald, #10b981)"
                            : "var(--color-rose, #f43f5e)",
                        boxShadow: isOnline && s.is_enabled ? "0 0 8px var(--color-emerald, #10b981)" : undefined,
                        flexShrink: 0,
                      }}
                    />
                    <div style={{ minWidth: 0 }}>
                      <div style={{ display: "flex", alignItems: "center", gap: 6, flexWrap: "wrap" }}>
                        <span style={{ fontWeight: 700, fontSize: "14px", color: "var(--text-primary)" }}>
                          {s.name}
                        </span>
                        {isActive && (
                          <span
                            style={{
                              fontSize: "10px",
                              fontFamily: "var(--font-mono)",
                              padding: "1px 6px",
                              borderRadius: "var(--r-chip)",
                              background: "var(--accent-soft)",
                              color: "var(--accent-strong)",
                              border: "1px solid var(--accent-glow)",
                              fontWeight: 600,
                            }}
                          >
                            {t("Active")}
                          </span>
                        )}
                        {s.is_default && (
                          <span
                            style={{
                              fontSize: "10px",
                              fontFamily: "var(--font-mono)",
                              padding: "1px 6px",
                              borderRadius: "var(--r-chip)",
                              background: "var(--bg-raised)",
                              color: "var(--text-secondary)",
                              border: "1px solid var(--border-hairline)",
                            }}
                          >
                            {t("Default")}
                          </span>
                        )}
                      </div>
                    </div>
                  </div>

                  <div style={{ display: "flex", alignItems: "center", gap: 4 }}>
                    <button
                      className="icon-btn"
                      onClick={() => setEditingServer(s)}
                      title={t("Edit server")}
                      style={{ width: 28, height: 28 }}
                    >
                      <IconNode size={14} />
                    </button>
                    {!s.is_default && servers.length > 1 && (
                      <button
                        className="icon-btn"
                        onClick={() => setDeleteConfirmId(s.id)}
                        title={t("Delete server")}
                        style={{ width: 28, height: 28, color: "var(--color-rose, #f43f5e)" }}
                      >
                        <IconTrash size={14} />
                      </button>
                    )}
                  </div>
                </div>

                {/* Details: Host, Port, Latency, Version */}
                <div
                  style={{
                    padding: "var(--s3)",
                    background: "var(--bg-deep)",
                    borderRadius: "var(--r-ctl)",
                    border: "1px solid var(--border-hairline)",
                    marginBottom: "var(--s3)",
                  }}
                >
                  <div style={{ display: "flex", alignItems: "center", justifyContent: "space-between", marginBottom: 6 }}>
                    <span style={{ fontSize: "11px", color: "var(--text-muted)" }}>{t("Address")}</span>
                    <span className="mono" style={{ fontSize: "12px", color: "var(--text-primary)", fontWeight: 500 }}>
                      {s.host}:{s.port}
                    </span>
                  </div>

                  <div style={{ display: "flex", alignItems: "center", justifyContent: "space-between", marginBottom: 6 }}>
                    <span style={{ fontSize: "11px", color: "var(--text-muted)" }}>{t("Latency")}</span>
                    <span
                      className="mono"
                      style={{
                        fontSize: "11px",
                        display: "flex",
                        alignItems: "center",
                        gap: 4,
                        color: isTesting
                          ? "var(--text-muted)"
                          : latencyMs !== undefined
                            ? latencyMs < 50
                              ? "var(--color-emerald, #10b981)"
                              : latencyMs < 150
                                ? "var(--color-amber, #f59e0b)"
                                : "var(--color-rose, #f43f5e)"
                            : "var(--text-muted)",
                      }}
                    >
                      {isTesting ? (
                        <>
                          <IconRefresh size={10} className="spin" />
                          <span>{t("Pinging...")}</span>
                        </>
                      ) : latencyMs !== undefined ? (
                        `${latencyMs} ms`
                      ) : lat?.error ? (
                        <span style={{ color: "var(--color-rose, #f43f5e)" }}>{t("Unreachable")}</span>
                      ) : (
                        "—"
                      )}
                    </span>
                  </div>

                  {lat?.version && (
                    <div style={{ display: "flex", alignItems: "center", justifyContent: "space-between" }}>
                      <span style={{ fontSize: "11px", color: "var(--text-muted)" }}>{t("Version")}</span>
                      <span className="mono" style={{ fontSize: "10px", color: "var(--text-secondary)", maxWidth: 170, overflow: "hidden", textOverflow: "ellipsis", whiteSpace: "nowrap" }}>
                        {lat.version}
                      </span>
                    </div>
                  )}
                </div>
              </div>

              {/* Actions Footer */}
              <div style={{ display: "flex", alignItems: "center", gap: 8, marginTop: "var(--s2)", paddingTop: "var(--s3)", borderTop: "1px solid var(--border-hairline)" }}>
                {isActive ? (
                  <button
                    className="btn btn--sm"
                    disabled
                    style={{ flex: 1, opacity: 0.8, cursor: "default", borderColor: "var(--accent-glow)" }}
                  >
                    <IconCheck size={14} style={{ color: "var(--accent)" }} />
                    <span style={{ color: "var(--accent)" }}>{t("Active node")}</span>
                  </button>
                ) : (
                  <button
                    className="btn btn-primary btn--sm"
                    onClick={() => handleSwitch(s.id)}
                    disabled={switchingId === s.id || !s.is_enabled}
                    style={{ flex: 1 }}
                  >
                    <IconPulse size={14} />
                    <span>{switchingId === s.id ? t("Switching...") : t("Switch to this server")}</span>
                  </button>
                )}

                <button
                  className="btn btn--sm"
                  onClick={() => testSingleServer(s)}
                  disabled={isTesting}
                  title={t("Test ping")}
                >
                  <IconRefresh size={13} className={isTesting ? "spin" : ""} />
                </button>

                {!s.is_default && (
                  <button
                    className="btn btn--sm"
                    onClick={() => handleSetDefault(s)}
                    title={t("Set as default")}
                  >
                    {t("Default")}
                  </button>
                )}
              </div>
            </div>
          );
        })}
      </div>

      {/* Delete Confirmation Dialog */}
      {deleteConfirmId !== null && (
        <Sheet
          title={t("Confirm Delete")}
          onClose={() => setDeleteConfirmId(null)}
          footer={
            <div style={{ display: "flex", justifyContent: "flex-end", gap: 8 }}>
              <button className="btn" onClick={() => setDeleteConfirmId(null)}>
                {t("Cancel")}
              </button>
              <button
                className="btn-danger"
                onClick={() => {
                  const target = servers.find((s) => s.id === deleteConfirmId);
                  if (target) void handleDelete(target);
                }}
              >
                {t("Delete")}
              </button>
            </div>
          }
        >
          <div style={{ padding: "var(--s3) 0" }}>
            <p style={{ color: "var(--text-secondary)", fontSize: "var(--t-small)", lineHeight: 1.6 }}>
              {t("Are you sure you want to delete this server? Historical traffic and session samples associated with this node will be permanently removed.")}
            </p>
          </div>
        </Sheet>
      )}

      {/* Add / Edit Server Drawer */}
      {editingServer !== null && (
        <ServerFormSheet
          server={editingServer === "new" ? null : editingServer}
          onClose={() => {
            setEditingServer(null);
            if (route.parts[1] === "add") navigate("/servers");
          }}
          onSaved={async () => {
            setEditingServer(null);
            if (route.parts[1] === "add") navigate("/servers");
            await refreshServers();
            await refreshActive();
          }}
        />
      )}
    </div>
  );
}

function ServerFormSheet({
  server,
  onClose,
  onSaved,
}: {
  server: ManagedServer | null;
  onClose: () => void;
  onSaved: () => Promise<void>;
}) {
  const { t } = useI18n();
  const { push } = useToast();

  const isNew = !server;
  const [name, setName] = useState(server?.name ?? "");
  const [host, setHost] = useState(server?.host ?? "127.0.0.1");
  const [port, setPort] = useState(server?.port ?? 5555);
  const [password, setPassword] = useState("");
  const [sortOrder, setSortOrder] = useState(server?.sort_order ?? 0);
  const [isEnabled, setIsEnabled] = useState(server?.is_enabled ?? true);
  const [isDefault, setIsDefault] = useState(server?.is_default ?? false);

  const [testing, setTesting] = useState(false);
  const [testResult, setTestResult] = useState<{
    ok: boolean;
    version?: string;
    latencyMs?: number;
    error?: string;
  } | null>(null);

  const [saving, setSaving] = useState(false);
  const [error, setError] = useState<string | null>(null);

  const handleTest = async () => {
    setTesting(true);
    setTestResult(null);
    setError(null);
    try {
      const res = await api.testServerConnection({
        host,
        port,
        password: password || (server ? undefined : ""),
      });
      setTestResult({
        ok: res.online,
        version: res.version,
        latencyMs: res.latency_ms,
        error: res.error,
      });
    } catch (err) {
      setTestResult({
        ok: false,
        error: err instanceof Error ? err.message : String(err),
      });
    } finally {
      setTesting(false);
    }
  };

  const handleSave = async (e: React.FormEvent) => {
    e.preventDefault();
    if (!name.trim()) {
      setError(t("Please enter a server name / alias"));
      return;
    }
    if (!host.trim()) {
      setError(t("Please enter a host or IP"));
      return;
    }
    setSaving(true);
    setError(null);

    try {
      if (isNew) {
        await api.createServer({
          name: name.trim(),
          host: host.trim(),
          port: Number(port),
          password: password,
          auth_type: "admin_password",
          is_enabled: isEnabled,
          is_default: isDefault,
          sort_order: Number(sortOrder),
        });
        push("ok", t("Server added successfully"));
      } else {
        await api.updateServer(server.id, {
          name: name.trim(),
          host: host.trim(),
          port: Number(port),
          password: password || undefined,
          auth_type: "admin_password",
          is_enabled: isEnabled,
          is_default: isDefault,
          sort_order: Number(sortOrder),
        });
        push("ok", t("Server updated successfully"));
      }
      await onSaved();
    } catch (err) {
      setError(err instanceof Error ? err.message : String(err));
    } finally {
      setSaving(false);
    }
  };

  return (
    <Sheet
      title={isNew ? t("Add Server") : t("Edit Server")}
      subtitle={isNew ? t("Register a new SoftEther VPN server node") : server.name}
      onClose={onClose}
      footer={
        <div style={{ display: "flex", justifyContent: "space-between", alignItems: "center", width: "100%" }}>
          <button
            type="button"
            className="btn btn--sm"
            onClick={handleTest}
            disabled={testing || saving}
          >
            <IconRefresh size={14} className={testing ? "spin" : ""} />
            <span>{testing ? t("Testing...") : t("Test connection")}</span>
          </button>
          <div style={{ display: "flex", gap: 8 }}>
            <button type="button" className="btn" onClick={onClose} disabled={saving}>
              {t("Cancel")}
            </button>
            <button type="submit" form="server-form" className="btn-primary" disabled={saving}>
              {saving ? t("Saving...") : t("Save server")}
            </button>
          </div>
        </div>
      }
    >
      <form id="server-form" onSubmit={handleSave} style={{ display: "flex", flexDirection: "column", gap: "var(--s4)" }}>
        {error && <ErrorAlert>{error}</ErrorAlert>}

        {testResult && (
          <div
            className={`alert ${testResult.ok ? "alert--ok" : "alert--err"}`}
            style={{ display: "flex", alignItems: "center", gap: 8 }}
          >
            {testResult.ok ? (
              <>
                <IconCheck size={16} />
                <div>
                  <div style={{ fontWeight: 600 }}>{t("Connection successful")}</div>
                  <div className="mono" style={{ fontSize: "11px" }}>
                    {testResult.version} · {testResult.latencyMs} ms
                  </div>
                </div>
              </>
            ) : (
              <>
                <IconAlert size={16} />
                <div>
                  <div style={{ fontWeight: 600 }}>{t("Connection failed")}</div>
                  <div style={{ fontSize: "11px" }}>{testResult.error || t("Target host did not respond")}</div>
                </div>
              </>
            )}
          </div>
        )}

        <Field label={t("Server name / alias")} hint={t("Descriptive alias for this VPN instance")}>
          <input
            className="input"
            type="text"
            required
            placeholder="e.g. Tokyo Edge, US-West Node"
            value={name}
            onChange={(e) => setName(e.target.value)}
          />
        </Field>

        <div className="row2">
          <Field label={t("Host / IP")} hint={t("IP address or domain")}>
            <input
              className="input mono"
              type="text"
              required
              placeholder="127.0.0.1 or vpn.example.com"
              value={host}
              onChange={(e) => setHost(e.target.value)}
            />
          </Field>
          <Field label={t("Management Port")} hint={t("Default 5555 (also 443, 992)")}>
            <input
              className="input mono"
              type="number"
              min={1}
              max={65535}
              required
              value={port}
              onChange={(e) => setPort(Number(e.target.value))}
            />
          </Field>
        </div>

        <Field
          label={t("Administrator Password")}
          hint={!isNew ? t("Leave blank to keep existing password") : t("SoftEther server management password")}
        >
          <input
            className="input mono"
            type="password"
            placeholder={!isNew ? "••••••••" : ""}
            value={password}
            onChange={(e) => setPassword(e.target.value)}
          />
        </Field>

        <div className="row2">
          <Field label={t("Sort Order")} hint={t("Display sequence in switcher")}>
            <input
              className="input mono"
              type="number"
              value={sortOrder}
              onChange={(e) => setSortOrder(Number(e.target.value))}
            />
          </Field>
          <div style={{ display: "flex", flexDirection: "column", justifyContent: "center", gap: 12, paddingTop: 16 }}>
            <CheckRow
              checked={isEnabled}
              onChange={setIsEnabled}
              label={t("Enable server")}
              hint={t("Poll and route to this instance")}
            />
            <CheckRow
              checked={isDefault}
              onChange={setIsDefault}
              label={t("Set as default node")}
              hint={t("Fallback node when none selected")}
            />
          </div>
        </div>
      </form>
    </Sheet>
  );
}
