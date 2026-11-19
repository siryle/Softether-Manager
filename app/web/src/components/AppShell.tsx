"use client";

import { useEffect, useRef, useState } from "react";
import { useAuth } from "../lib/auth";
import { useServer } from "../lib/server";
import { Link, navigate, seg, useRoute } from "../lib/router";
import { LanguageButton, useI18n, t } from "../lib/i18n";
import { api, type Probe, type Wire } from "../lib/api";
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
  IconReport,
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
 *   - Online client count shortcut pill
 *   - Monospace search bar with ⌘K keycap
 *   - Language, Theme, and Administrator controls
 *
 * Sidebar:
 *   - Core Network: Dashboard, Hubs, Online clients, Users
 *   - Server Management: Server List, Server Configuration, Logs
 *   - Panel: Panel Settings
 */
export function AppShell({ children }: { children: React.ReactNode }) {
  const { user, logout } = useAuth();
  const { hubs, clientCount } = useServer();
  const { t } = useI18n();
  const route = useRoute();
  const p = route.parts;
  const active = p[0] ?? "";

  const [version, setVersion] = useState<string | null>(null);
  const [cmdkOpen, setCmdkOpen] = useState(false);
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

  useEffect(() => { void api.systemInfo().then(info => setVersion(String(info.version))).catch(() => {}); }, []);
  useEffect(() => {
    const focusSearch = (event: KeyboardEvent) => {
      if ((event.metaKey || event.ctrlKey) && event.key.toLowerCase() === "k") {
        event.preventDefault();
        setCmdkOpen(prev => !prev);
      }
    };
    window.addEventListener("keydown", focusSearch);
    return () => window.removeEventListener("keydown", focusSearch);
  }, []);

  const onlineClients = hubs ? (clientCount ?? 0) : null;
  const liveClients = Boolean(onlineClients && onlineClients > 0);

  const navLink = (to: string, label: string, icon: React.ReactNode) => {
    const target = to.slice(1);
    const on = target === "" ? active === "" || active === "connect" : target === "hubs" ? active === "hubs" || active === "hub" : target === "clients" ? active === "clients" || active === "connections" : active === target;
    return <Link
      to={to}
      className={`side__item${on ? " on" : ""}`}
      aria-current={on ? "page" : undefined}
      title={collapsed ? t(label) : undefined}
      onClick={(e) => { e.currentTarget.blur(); }}
    >
      <span className="side__item-left">{icon}<span className="side__item-label">{t(label)}</span></span>
    </Link>;
  };

  return (
    <div className={`shell${collapsed ? " shell--collapsed" : ""}`}>
      {/* ── Top Navigation Bar (56px Single-Row Precision Chrome) ─────── */}
      <header className="topbar">
        {/* Left: Identity + Slash + Server Switcher */}
        <div className="topbar__left">
          <Link to="/" className="topbar__brand" aria-label="SoftEther Manager">
            <BrandMark size={26} />
            <span className="topbar__brand-title">SoftEther Manager</span>
            {version && <span className="topbar__brand-ver">v{version.replace(/^v/, "")}</span>}
          </Link>

          <span className="topbar__slash" aria-hidden="true">/</span>
          <ServerSwitcher />
        </div>

        {/* Right: Online Clients, Command Search (⌘K), Utilities & User */}
        <div className="topbar__right">
          <Link
            to="/clients"
            className={`topbar__clients${liveClients ? " topbar__clients--live" : ""}`}
            title={t("Online clients")}
            aria-label={`${t("Online clients")}: ${onlineClients ?? "—"}`}
          >
            <span className="topbar__clients-dot" aria-hidden="true" />
            <span className="topbar__clients-count">{onlineClients ?? "—"}</span>
            <span className="topbar__clients-label">{t("Online clients")}</span>
          </Link>

          <button
            type="button"
            className="cmdk-btn"
            onClick={() => setCmdkOpen(true)}
            aria-label={t("Quick jump")}
            title={t("Search hubs, sessions, users...")}
          >
            <IconSearch size={13} />
            <span className="cmdk-btn__label">{t("Quick jump")}</span>
            <span className="keycap">⌘K</span>
          </button>

          <LanguageButton className="btn btn--sm topbar__lang" />
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
                {navLink("/", "Console overview", <IconPanels size={17} />)}
                {navLink("/hubs", "Virtual Hubs", <IconHub size={17} />)}
                {navLink("/clients", "Online clients", <IconPulse size={17} />)}
                {navLink("/users", "Users", <IconUsers size={17} />)}
                {navLink("/audit-report", "Audit report", <IconReport size={17} />)}
              </nav>
            </div>
            <div className="side__section">
              <div className="side__gtitle">{t("Server management")}</div>
              <nav className="side__nav">
                {navLink("/servers", "Server list", <IconNode size={17} />)}
                {navLink("/server-settings", "Server config", <IconSettings size={17} />)}
                {navLink("/logs", "Logs", <IconLogs size={17} />)}
              </nav>
            </div>
            <div className="side__section">
              <div className="side__gtitle">{t("Panel navigation")}</div>
              <nav className="side__nav">
                {navLink("/settings", "Panel settings", <IconSettings size={17} />)}
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

      {cmdkOpen && (
        <CommandPalette
          onClose={() => setCmdkOpen(false)}
          hubs={hubs ?? []}
        />
      )}
    </div>
  );
}

function CommandPalette({ onClose, hubs }: { onClose: () => void; hubs: Wire[] }) {
  const { t } = useI18n();
  const { servers, switchServer } = useServer();
  const { push } = useToast();
  const [query, setQuery] = useState("");
  const q = query.trim().toLowerCase();

  useEffect(() => {
    const onKey = (e: KeyboardEvent) => {
      if (e.key === "Escape") onClose();
    };
    window.addEventListener("keydown", onKey);
    return () => window.removeEventListener("keydown", onKey);
  }, [onClose]);

  const pages = [
    { label: t("Console overview"), to: "/", meta: "/" },
    { label: t("Virtual Hubs"), to: "/hubs", meta: "/hubs" },
    { label: t("Online clients"), to: "/clients", meta: "/clients" },
    { label: t("Users"), to: "/users", meta: "/users" },
    { label: t("Audit report"), to: "/audit-report", meta: "/audit-report" },
    { label: t("Server list"), to: "/servers", meta: "/servers" },
    { label: t("Server config"), to: "/server-settings", meta: "/server-settings" },
    { label: t("Logs"), to: "/logs", meta: "/logs" },
    { label: t("Panel settings"), to: "/settings", meta: "/settings" },
  ].filter(item => !q || item.label.toLowerCase().includes(q) || item.meta.includes(q));

  const matchedHubs = hubs
    .map(h => ({
      name: String(h.HubName_str ?? ""),
      sessions: Number(h.NumSessions_u32) || 0,
      online: Boolean(h.Online_bool),
    }))
    .filter(h => h.name && (!q || h.name.toLowerCase().includes(q)));

  const matchedServers = (servers ?? []).filter(
    s => !q || `${s.name} ${s.host}:${s.port}`.toLowerCase().includes(q),
  );

  const go = (to: string) => {
    navigate(to);
    onClose();
  };

  return (
    <div className="cmdk-backdrop" onClick={onClose} role="dialog" aria-modal="true" aria-label={t("Quick jump")}>
      <div className="cmdk-dialog" onClick={e => e.stopPropagation()}>
        <div className="cmdk-head">
          <IconSearch size={15} style={{ color: "var(--text-muted)", flexShrink: 0 }} />
          <input
            className="cmdk-input"
            autoFocus
            value={query}
            onChange={e => setQuery(e.target.value)}
            placeholder={t("Search pages, hubs, servers...")}
            onKeyDown={(e) => {
              if (e.key === "Enter") {
                if (matchedHubs[0]) go(`/hub/${seg(matchedHubs[0].name)}`);
                else if (pages[0]) go(pages[0].to);
              }
            }}
          />
          <button type="button" className="keycap" onClick={onClose}>ESC</button>
        </div>
        <div className="cmdk-list">
          {pages.length > 0 && (
            <>
              <div className="cmdk-group">{t("Pages")}</div>
              {pages.map(item => (
                <button key={item.to} type="button" className="cmdk-item" onClick={() => go(item.to)}>
                  <span>{item.label}</span>
                  <span className="cmdk-item__meta">{item.meta}</span>
                </button>
              ))}
            </>
          )}

          {matchedHubs.length > 0 && (
            <>
              <div className="cmdk-group">{t("Virtual Hubs")}</div>
              {matchedHubs.map(h => (
                <button key={h.name} type="button" className="cmdk-item" onClick={() => go(`/hub/${seg(h.name)}`)}>
                  <span className="mono">{h.name}</span>
                  <span className="cmdk-item__meta">{h.online ? `${h.sessions} ${t("sessions")}` : t("Offline")}</span>
                </button>
              ))}
            </>
          )}

          {matchedServers.length > 0 && (
            <>
              <div className="cmdk-group">{t("Servers")}</div>
              {matchedServers.map(s => (
                <button
                  key={s.id}
                  type="button"
                  className="cmdk-item"
                  disabled={!s.is_enabled}
                  onClick={async () => {
                    onClose();
                    try { await switchServer(s.id); } catch (e) { push("err", e instanceof Error ? e.message : String(e)); }
                  }}
                >
                  <span>{s.name}</span>
                  <span className="cmdk-item__meta">{s.host}:{s.port}</span>
                </button>
              ))}
            </>
          )}

          {!pages.length && !matchedHubs.length && !matchedServers.length && (
            <div className="cmdk-empty">{t("No matching results")}</div>
          )}
        </div>
      </div>
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
      {currentServer?.host && (
        <span className="topbar__switcher-addr">{currentServer.host}:{currentServer.port}</span>
      )}
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
