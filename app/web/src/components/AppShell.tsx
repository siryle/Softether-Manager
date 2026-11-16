"use client";

import { useEffect, useRef, useState } from "react";
import { useAuth } from "../lib/auth";
import { useServer } from "../lib/server";
import { Link, navigate, seg, useRoute } from "../lib/router";
import { LanguageButton, useI18n, t } from "../lib/i18n";
import { api, type Wire } from "../lib/api";
import { formatBytes } from "../lib/util";
import {
  BrandMark,
  IconCheck,
  IconChevron,
  IconHub,
  IconLogs,
  IconMoon,
  IconNode,
  IconPanels,
  IconPulse,
  IconSearch,
  IconSettings,
  IconSignOut,
  IconSun,
  IconTerminal,
  IconUsers,
} from "../ui/Icon";
import { useTheme } from "../ui/theme";

/**
 * Unslopped Quiet Chrome Shell for SoftEther Manager.
 *
 * Sticky Topbar:
 *   - Identity with original lime-coin BrandMark
 *   - Restrained LED connection status indicator (IP:Port · Running)
 *   - Monospace search bar with ⌘K keycap
 *   - Live dual-stream telemetry readout (RX/TX)
 *   - Language, Theme, Version and Administrator controls
 *
 * Sidebar:
 *   - Core Network: Dashboard, Hubs, Users, Sessions (with count badges)
 *   - System Management: Server Configuration, Logs, API Console, Panel Settings
 *   - Bottom Hardware Telemetry box (CPU & RAM status)
 */
export function AppShell({ children }: { children: React.ReactNode }) {
  const { user, logout } = useAuth();
  const { probe, hubs, health, servers } = useServer();
  const { t } = useI18n();
  const route = useRoute();
  const p = route.parts;
  const active = p[0] ?? "";

  const [resources, setResources] = useState<Wire | null>(null);

  // Poll resources for live topbar telemetry & sidebar health box
  useEffect(() => {
    let unmounted = false;
    const fetchRes = async () => {
      try {
        const data = await api.resources();
        if (!unmounted && data) setResources(data);
      } catch {}
    };
    fetchRes();
    const timer = setInterval(fetchRes, 5000);
    return () => {
      unmounted = true;
      clearInterval(timer);
    };
  }, []);

  const net = (resources?.network as Wire) ?? {};
  const rxRate = Number(net.rx_bytes_per_second) || 0;
  const txRate = Number(net.tx_bytes_per_second) || 0;
  const cpuPercent = (Number(resources?.cpu?.overall?.percent) || 0).toFixed(1);
  const memUsed = Number(resources?.memory?.used_bytes) || 0;
  const memTotal = Number(resources?.memory?.total_bytes) || 1;
  const memPercent = Math.round((memUsed / memTotal) * 100);

  // Total session and user counts from hubs
  const totalSessions = hubs?.reduce((acc, h) => acc + (Number(h.NumSessions_u32) || 0), 0) ?? 0;
  const totalUsers = hubs?.reduce((acc, h) => acc + (Number(h.NumUsers_u32) || 0), 0) ?? 0;

  return (
    <div className="shell">
      {/* ── Top Navigation Bar (Quiet Chrome) ────────────────────────── */}
      <header className="topbar">
        {/* Left: Identity + Restrained Status Indicator */}
        <div className="topbar__left">
          <Link to="/" className="topbar__brand" aria-label="SoftEther Manager">
            <BrandMark size={28} />
            <span className="topbar__brand-title">SoftEther Manager</span>
            <span className="topbar__brand-ver">v1.0.13</span>
          </Link>

          <ServerSwitcher />
        </div>

        {/* Center: Monospace Search (Cmd+K) */}
        <div className="topbar__center">
          <div className="topbar__search">
            <IconSearch size={14} className="topbar__search-icon" />
            <input
              type="text"
              placeholder={t("Search hubs, sessions, users...")}
              className="topbar__search-input"
              onKeyDown={(e) => {
                if (e.key === "Enter") {
                  navigate("/users");
                }
              }}
            />
            <span className="keycap">⌘K</span>
          </div>
        </div>

        {/* Right: Live Telemetry + Controls */}
        <div className="topbar__right">
          <div className="topbar__telemetry mono">
            <span className="topbar__rx">
              <span className="topbar__rx-lbl">↓ RX</span> {formatBytes(rxRate)}/s
            </span>
            <span style={{ color: "var(--text-muted)" }}>·</span>
            <span className="topbar__tx">
              <span className="topbar__tx-lbl">↑ TX</span> {formatBytes(txRate)}/s
            </span>
          </div>

          <LanguageButton />
          <ThemeToggle />

          {user && (
            <div className="topbar__user">
              <span className="topbar__avatar mono">
                {(user || "A").slice(0, 1).toUpperCase()}
              </span>
              <span className="topbar__username mono">{user}</span>
              <button
                className="icon-btn"
                onClick={logout}
                title={t("Sign out")}
                aria-label={t("Sign out")}
                style={{ width: 26, height: 26 }}
              >
                <IconSignOut size={14} />
              </button>
            </div>
          )}
        </div>
      </header>

      {/* ── App Layout Body ─────────────────────────────────────────── */}
      <div className="shell__body">
        {/* Sidebar: Strict rows & status telemetry */}
        <aside className="side">
          <div>
            {/* Core Network */}
            <div className="side__section">
              <div className="side__gtitle">{t("Core Network")}</div>
              <nav className="side__nav">
                <Link
                  to="/"
                  className={`side__item${active === "" ? " on" : ""}`}
                >
                  <span className="side__item-left">
                    <IconPanels size={16} />
                    <span>{t("Console overview")}</span>
                  </span>
                </Link>

                <Link
                  to="/servers"
                  className={`side__item${active === "servers" ? " on" : ""}`}
                >
                  <span className="side__item-left">
                    <IconNode size={16} />
                    <span>{t("Server nodes")}</span>
                  </span>
                  <span className="side__count">{servers?.length ?? 1}</span>
                </Link>

                <Link
                  to="/hubs"
                  className={`side__item${active === "hubs" || active === "hub" ? " on" : ""}`}
                >
                  <span className="side__item-left">
                    <IconHub size={16} />
                    <span>{t("Virtual Hubs")}</span>
                  </span>
                  <span className="side__count">{hubs?.length ?? "—"}</span>
                </Link>

                <Link
                  to="/users"
                  className={`side__item${active === "users" ? " on" : ""}`}
                >
                  <span className="side__item-left">
                    <IconUsers size={16} />
                    <span>{t("Users")}</span>
                  </span>
                  <span className="side__count">{totalUsers}</span>
                </Link>

                <Link
                  to="/connections"
                  className={`side__item${active === "connections" ? " on" : ""}`}
                >
                  <span className="side__item-left">
                    <IconPulse size={16} />
                    <span>{t("Active sessions")}</span>
                  </span>
                  <span className="side__count">{totalSessions}</span>
                </Link>
              </nav>
            </div>

            {/* System Management */}
            <div className="side__section">
              <div className="side__gtitle">{t("System Management")}</div>
              <nav className="side__nav">
                <Link
                  to="/server-settings"
                  className={`side__item${active === "server-settings" ? " on" : ""}`}
                  aria-current={active === "server-settings" ? "page" : undefined}
                >
                  <span className="side__item-left">
                    <IconNode size={16} />
                    <span>{t("Server configuration")}</span>
                  </span>
                </Link>

                <Link
                  to="/logs"
                  className={`side__item${active === "logs" ? " on" : ""}`}
                >
                  <span className="side__item-left">
                    <IconLogs size={16} />
                    <span>{t("Logs")}</span>
                  </span>
                </Link>

                <Link
                  to="/console"
                  className={`side__item${active === "console" ? " on" : ""}`}
                >
                  <span className="side__item-left">
                    <IconTerminal size={16} />
                    <span>{t("API console")}</span>
                  </span>
                </Link>

                <Link
                  to="/settings"
                  className={`side__item${active === "settings" ? " on" : ""}`}
                  aria-current={active === "settings" ? "page" : undefined}
                >
                  <span className="side__item-left">
                    <IconSettings size={16} />
                    <span>{t("Panel settings")}</span>
                  </span>
                </Link>
              </nav>
            </div>
          </div>

          {/* Bottom Telemetry Status Box */}
          <div className="side__telemetry-box">
            <div className="side__telemetry-h">
              <span>{probe?.hostname || "vpnserver"}</span>
              <span className="side__telemetry-uptime">
                {probe?.online ? t("Online") : t("Offline")}
              </span>
            </div>
            <div className="side__telemetry-row">
              <span className="side__telemetry-lbl">{t("CPU Load")}</span>
              <span className="side__telemetry-v">{cpuPercent}%</span>
            </div>
            <div className="side__telemetry-row">
              <span className="side__telemetry-lbl">{t("Memory usage")}</span>
              <span className="side__telemetry-v" style={{ color: memPercent >= 85 ? "var(--color-amber)" : undefined }}>
                {memPercent}%
              </span>
            </div>
          </div>
        </aside>

        {/* Main View Area */}
        <main className="shell__scroll">
          {children}
        </main>
      </div>

      {/* Mobile Tabbar (< 768px) */}
      <TabBar />
    </div>
  );
}

function ServerSwitcher() {
  const { servers, currentServer, currentServerId, switchServer, probe } = useServer();
  const { t } = useI18n();
  const [isOpen, setIsOpen] = useState(false);
  const [filter, setFilter] = useState("");
  const [switchingId, setSwitchingId] = useState<number | null>(null);
  const dropdownRef = useRef<HTMLDivElement>(null);

  useEffect(() => {
    if (!isOpen) return;
    const handleClick = (e: MouseEvent) => {
      if (dropdownRef.current && !dropdownRef.current.contains(e.target as Node)) {
        setIsOpen(false);
      }
    };
    document.addEventListener("mousedown", handleClick);
    return () => document.removeEventListener("mousedown", handleClick);
  }, [isOpen]);

  const activeId = currentServer?.id ?? currentServerId;
  const filtered = (servers ?? []).filter((s) => {
    if (!filter.trim()) return true;
    const q = filter.toLowerCase();
    return s.name.toLowerCase().includes(q) || `${s.host}:${s.port}`.includes(q);
  });

  return (
    <div className="topbar__switcher" ref={dropdownRef}>
      <button
        type="button"
        className="topbar__switcher-btn"
        onClick={() => setIsOpen((prev) => !prev)}
        title={t("Switch server node")}
      >
        <span
          style={{
            width: 7,
            height: 7,
            borderRadius: "50%",
            backgroundColor: probe?.online ? "var(--color-emerald, #10b981)" : "var(--color-amber, #f59e0b)",
            boxShadow: probe?.online ? "0 0 6px var(--color-emerald, #10b981)" : undefined,
            flexShrink: 0,
          }}
        />
        <span className="mono" style={{ fontWeight: 600, maxWidth: 130, overflow: "hidden", textOverflow: "ellipsis", whiteSpace: "nowrap" }}>
          {currentServer?.name || probe?.hostname || "127.0.0.1"}
        </span>
        <span style={{ color: "var(--text-muted)", fontSize: 11 }} className="mono">
          :{currentServer?.port || 5555}
        </span>
        <span style={{ display: "inline-flex", transform: isOpen ? "rotate(-90deg)" : "rotate(90deg)", transition: "transform 0.15s ease", opacity: 0.6 }}>
          <IconChevron size={11} />
        </span>
      </button>

      {isOpen && (
        <div className="topbar__dropdown">
          <input
            type="text"
            className="topbar__dropdown-search"
            placeholder={t("Filter servers...")}
            value={filter}
            onChange={(e) => setFilter(e.target.value)}
            autoFocus
          />
          <div className="topbar__dropdown-list">
            {filtered.map((s) => {
              const isActive = s.id === activeId;
              return (
                <button
                  key={s.id}
                  type="button"
                  className={`topbar__dropdown-item ${isActive ? "active" : ""}`}
                  onClick={async () => {
                    if (!isActive) {
                      setSwitchingId(s.id);
                      await switchServer(s.id);
                      setSwitchingId(null);
                    }
                    setIsOpen(false);
                  }}
                >
                  <div style={{ display: "flex", alignItems: "center", gap: 8, minWidth: 0 }}>
                    <span
                      style={{
                        width: 6,
                        height: 6,
                        borderRadius: "50%",
                        backgroundColor: s.is_enabled
                          ? isActive && probe?.online
                            ? "var(--color-emerald, #10b981)"
                            : "var(--text-muted)"
                          : "var(--color-rose, #f43f5e)",
                        flexShrink: 0,
                      }}
                    />
                    <div style={{ minWidth: 0, textAlign: "left" }}>
                      <div style={{ fontWeight: isActive ? 600 : 400, color: "var(--text-primary)", overflow: "hidden", textOverflow: "ellipsis", whiteSpace: "nowrap" }}>
                        {s.name}
                        {s.is_default && (
                          <span style={{ marginLeft: 6, fontSize: 9, padding: "1px 4px", borderRadius: 3, background: "var(--bg-raised)", color: "var(--text-muted)" }}>
                            {t("Default")}
                          </span>
                        )}
                      </div>
                      <div className="mono" style={{ fontSize: 10, color: "var(--text-muted)" }}>
                        {s.host}:{s.port}
                      </div>
                    </div>
                  </div>
                  {isActive && (
                    <span style={{ color: "var(--accent, #a0e35f)", marginLeft: 6, flexShrink: 0 }}>
                      <IconCheck size={14} />
                    </span>
                  )}
                  {switchingId === s.id && (
                    <span className="spin" style={{ fontSize: 10, marginLeft: 6 }}>⟳</span>
                  )}
                </button>
              );
            })}
            {filtered.length === 0 && (
              <div style={{ padding: "8px 12px", fontSize: 11, color: "var(--text-muted)", textAlign: "center" }}>
                {t("No matching servers")}
              </div>
            )}
          </div>
          <div className="topbar__dropdown-footer">
            <Link
              to="/servers"
              onClick={() => setIsOpen(false)}
              style={{ color: "var(--text-muted)", textDecoration: "none", fontSize: 11 }}
            >
              {t("Manage cluster →")}
            </Link>
            <Link
              to="/servers/add"
              onClick={() => setIsOpen(false)}
              style={{ color: "var(--accent, #a0e35f)", textDecoration: "none", fontSize: 11, fontWeight: 500 }}
            >
              + {t("Add server")}
            </Link>
          </div>
        </div>
      )}
    </div>
  );
}

function TabBar() {
  const route = useRoute();
  const { t } = useI18n();
  const active = route.parts[0] ?? "";
  const tab = (match: (p: string) => boolean, to: string, label: string, icon: React.ReactNode) => (
    <Link to={to} className="tab" aria-current={match(active) ? "page" : undefined}>
      {icon}
      <span>{t(label)}</span>
    </Link>
  );
  return (
    <nav className="tabbar" aria-label={t("Primary")}>
      {tab((p) => p === "" || p === "connect", "/", "Dashboard", <IconPanels size={18} />)}
      {tab((p) => p === "servers", "/servers", "Servers", <IconNode size={18} />)}
      {tab((p) => p === "hubs" || p === "hub", "/hubs", "Virtual Hubs", <IconHub size={18} />)}
      {tab((p) => p === "users", "/users", "Users", <IconUsers size={18} />)}
      {tab((p) => p === "connections", "/connections", "Sessions", <IconPulse size={18} />)}
      {tab((p) => p === "server-settings", "/server-settings", "Server", <IconNode size={18} />)}
      {tab((p) => p === "settings", "/settings", "Settings", <IconSettings size={18} />)}
    </nav>
  );
}

export function ThemeToggle() {
  const { resolved, toggle } = useTheme();
  const dark = resolved === "dark";
  return (
    <button
      className="icon-btn"
      onClick={toggle}
      title={dark ? t("Switch to light") : t("Switch to dark")}
      aria-label={dark ? t("Switch to light theme") : t("Switch to dark theme")}
    >
      {dark ? <IconSun size={15} /> : <IconMoon size={15} />}
    </button>
  );
}

export { seg };
