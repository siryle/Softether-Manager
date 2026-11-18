"use client";

import { useEffect, useRef, useState } from "react";
import { useAuth } from "../lib/auth";
import { useServer } from "../lib/server";
import { Link, navigate, seg, useRoute } from "../lib/router";
import { LanguageButton, useI18n, t } from "../lib/i18n";
import { api, type Probe, type Wire } from "../lib/api";
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
  IconPlus,
  IconPulse,
  IconSearch,
  IconSettings,
  IconSignOut,
  IconSun,
  IconUsers,
} from "../ui/Icon";
import { useToast } from "../lib/toast";
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
 *   - Server Management: Server List, Server Configuration, Logs
 *   - Panel: Panel Settings
 */
export function AppShell({ children }: { children: React.ReactNode }) {
  const { user, logout } = useAuth();
  const { hubs, servers } = useServer();
  const { t } = useI18n();
  const route = useRoute();
  const p = route.parts;
  const active = p[0] ?? "";

  const [resources, setResources] = useState<Wire | null>(null);
  const [version, setVersion] = useState<string | null>(null);
  const [collapsed, setCollapsed] = useState<boolean>(() => {
    if (typeof window === "undefined") return false;
    try {
      return localStorage.getItem("sem_sidebar_collapsed") === "true";
    } catch {
      return false;
    }
  });

  const toggleCollapsed = () => {
    setCollapsed(prev => {
      const next = !prev;
      try {
        localStorage.setItem("sem_sidebar_collapsed", String(next));
      } catch {}
      return next;
    });
  };

  const searchRef = useRef<HTMLInputElement>(null);
  useEffect(() => { void api.systemInfo().then(info => setVersion(String(info.version))).catch(() => {}); }, []);
  useEffect(() => {
    const focusSearch = (event: KeyboardEvent) => {
      if ((event.metaKey || event.ctrlKey) && event.key.toLowerCase() === "k") {
        event.preventDefault();
        searchRef.current?.focus();
      }
    };
    window.addEventListener("keydown", focusSearch);
    return () => window.removeEventListener("keydown", focusSearch);
  }, []);

  // Total session and user counts from hubs
  const totalSessions = hubs?.reduce((acc, h) => acc + (Number(h.NumSessions_u32) || 0), 0) ?? 0;
  const totalUsers = hubs?.reduce((acc, h) => acc + (Number(h.NumUsers_u32) || 0), 0) ?? 0;

  const navLink = (to: string, label: string, icon: React.ReactNode, count?: React.ReactNode) => {
    const target = to.slice(1);
    const on = target === "" ? active === "" || active === "connect" : target === "hubs" ? active === "hubs" || active === "hub" : target === "clients" ? active === "clients" || active === "connections" : active === target;
    return <Link to={to} className={`side__item${on ? " on" : ""}`} aria-current={on ? "page" : undefined} title={collapsed ? t(label) : undefined}>
      <span className="side__item-left">{icon}<span className="side__item-label">{t(label)}</span></span>
      {count !== undefined && <span className="side__count">{count}</span>}
    </Link>;
  };

  return (
    <div className="shell">
      {/* ── Top Navigation Bar (Quiet Chrome) ────────────────────────── */}
      <header className="topbar">
        {/* Left: Identity + Restrained Status Indicator */}
        <div className="topbar__left">
          <Link to="/" className="topbar__brand" aria-label="SoftEther Manager">
            <BrandMark size={28} />
            <span className="topbar__brand-title">SoftEther Manager</span>
            {version && <span className="topbar__brand-ver">v{version.replace(/^v/, "")}</span>}
          </Link>

          <ServerSwitcher />
        </div>

        {/* Center: Monospace Search (Cmd+K) */}
        <div className="topbar__center">
          <div className="topbar__search">
            <IconSearch size={14} className="topbar__search-icon" />
            <input
              ref={searchRef}
              type="text"
              placeholder={t("Search hubs, sessions, users...")}
              aria-label={t("Search hubs, sessions, users...")}
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

        {/* Right: Controls */}
        <div className="topbar__right">
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
        {/* Sidebar: Core network, server management and panel navigation */}
        <aside className={`side${collapsed ? " side--collapsed" : ""}`}>
          <div className="side__body">
            <div className="side__section">
              <div className="side__gtitle">{t("Core Network")}</div>
              <nav className="side__nav">
                {navLink("/", "Console overview", <IconPanels size={18} />)}
                {navLink("/hubs", "Virtual Hubs", <IconHub size={18} />, hubs?.length ?? "—")}
                {navLink("/users", "Users", <IconUsers size={18} />, hubs ? totalUsers : "—")}
                {navLink("/clients", "Online clients", <IconPulse size={18} />, hubs ? totalSessions : "—")}
              </nav>
            </div>
            <div className="side__section">
              <div className="side__gtitle">{t("Server management")}</div>
              <nav className="side__nav">
                {navLink("/servers", "Server list", <IconNode size={18} />, servers?.length ?? "—")}
                {navLink("/server-settings", "Server config", <IconSettings size={18} />)}
                {navLink("/logs", "Logs", <IconLogs size={18} />)}
              </nav>
            </div>
            <div className="side__section">
              <div className="side__gtitle">{t("Panel navigation")}</div>
              <nav className="side__nav">
                {navLink("/settings", "Panel settings", <IconSettings size={18} />)}
              </nav>
            </div>
          </div>

          <div className="side__footer">
            <button
              type="button"
              className="side__toggle"
              onClick={toggleCollapsed}
              title={collapsed ? t("Expand") : t("Collapse")}
              aria-label={collapsed ? t("Expand") : t("Collapse")}
              aria-expanded={!collapsed}
            >
              <span className="side__toggle-icon">
                <IconChevron
                  size={14}
                  style={{
                    transform: collapsed ? "rotate(0deg)" : "rotate(180deg)",
                    transition: "transform var(--d-fast) ease",
                  }}
                />
              </span>
              {!collapsed && <span className="side__toggle-label">{t("Collapse")}</span>}
            </button>
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
  const { servers, currentServer, switchServer, health } = useServer();
  const { t } = useI18n();
  const { push } = useToast();
  const [open, setOpen] = useState(false);
  const [filter, setFilter] = useState("");
  const [states, setStates] = useState<Record<number, Probe>>({});
  useEffect(() => {
    if (!open) return;
    let alive = true;
    for (const s of servers ?? []) {
      if (!s.configured || !s.is_enabled) continue;
      void api.probeServer(s.id).then(p => { if (alive) setStates(prev => ({ ...prev, [s.id]: p })); }).catch(() => { if (alive) setStates(prev => ({ ...prev, [s.id]: { online: false, configured: true } })); });
    }
    return () => { alive = false; };
  }, [open, servers]);
  const dropdownRef = useRef<HTMLDivElement>(null);
  const triggerRef = useRef<HTMLButtonElement>(null);
  useEffect(() => {
    if (!open) return;
    const outside = (e: MouseEvent) => { if (!dropdownRef.current?.contains(e.target as Node)) setOpen(false); };
    const escape = (e: KeyboardEvent) => { if (e.key === "Escape") { setOpen(false); triggerRef.current?.focus(); } };
    document.addEventListener("mousedown", outside);
    document.addEventListener("keydown", escape);
    return () => { document.removeEventListener("mousedown", outside); document.removeEventListener("keydown", escape); };
  }, [open]);
  const filtered = (servers ?? []).filter(s => `${s.name} ${s.host}:${s.port}`.toLowerCase().includes(filter.trim().toLowerCase()));
  return <div className="topbar__switcher" ref={dropdownRef}>
    <button ref={triggerRef} type="button" className="topbar__switcher-btn" aria-label={t("Switch server")} aria-expanded={open} aria-controls="server-picker" onClick={() => { setFilter(""); setOpen(!open); }}>
      <span className={`server-dot server-dot--${health}`} />
      <span className="topbar__server-name">{currentServer?.name ?? t("Choose server")}</span>
      <IconChevron size={12} style={{ transform: "rotate(90deg)" }} />
    </button>
    {open && <div className="topbar__dropdown" id="server-picker">
      <div className="server-picker__heading"><span>{t("Switch server")}</span><span>{t("{count} saved", { count: servers?.length ?? 0 })}</span></div>
      <input className="topbar__dropdown-search" aria-label={t("Search servers")} placeholder={t("Search name or address...")} value={filter} onChange={e => setFilter(e.target.value)} autoFocus />
      <div className="topbar__dropdown-list">{filtered.map(s => <button key={s.id} type="button" className={`topbar__dropdown-item${s.id === currentServer?.id ? " active" : ""}`} disabled={!s.is_enabled} onClick={async () => {
        setOpen(false);
        try { await switchServer(s.id); } catch (e) { push("err", e instanceof Error ? e.message : String(e)); }
      }}><span className="server-picker__identity"><span className={`server-dot server-dot--${s.id === currentServer?.id ? health : states[s.id] ? states[s.id].online ? "online" : "offline" : "unknown"}`} /><span><strong>{s.name}</strong><span className="server-picker__address mono">{s.host}:{s.port}</span></span></span>{s.id === currentServer?.id ? <IconCheck size={15} /> : <span className="micro">{t(!s.is_enabled ? "Disabled" : !s.configured ? "Not configured" : !states[s.id] ? "Checking..." : states[s.id].online ? "Online" : "Offline")}</span>}</button>)}{!filtered.length && <p className="server-picker__empty">{t("No matching servers")}</p>}</div>
      <div className="topbar__dropdown-footer"><Link to="/servers" onClick={() => setOpen(false)}><IconNode size={14} />{t("Manage servers")}</Link><Link to="/servers/add" onClick={() => setOpen(false)}><IconPlus size={14} />{t("Add server")}</Link></div>
    </div>}
  </div>;
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
      {tab((p) => p === "clients" || p === "connections", "/clients", "Online clients", <IconPulse size={18} />)}
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
