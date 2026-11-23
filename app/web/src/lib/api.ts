/**
 * The one door to the backend.
 *
 * Paths are *relative* ("api/v1/..."): the exported page lives at the install
 * prefix's root, so the browser resolves them under whatever prefix this panel
 * happens to be served from -- no build-time base, no runtime discovery.
 * `NEXT_PUBLIC_API_BASE` overrides for the dev server, where the UI runs on
 * :3000 and the API on :8000.
 */

import { useMemo, useState } from "react";

const BASE = process.env.NEXT_PUBLIC_API_BASE ?? "";
const SERVER_ID_KEY = "sem_server_id";

export function clearLegacyToken() {
  localStorage.removeItem("sem_token");
}

export function getStoredServerId(): number | null {
  if (typeof window === "undefined") return null;
  const v = sessionStorage.getItem(SERVER_ID_KEY) ?? localStorage.getItem(SERVER_ID_KEY);
  const id = Number(v);
  return Number.isSafeInteger(id) && id > 0 ? id : null;
}
export function setStoredServerId(id: number) {
  if (typeof window === "undefined") return;
  sessionStorage.setItem(SERVER_ID_KEY, String(id));
  localStorage.removeItem(SERVER_ID_KEY);
}
export function clearStoredServerId() {
  if (typeof window === "undefined") return;
  sessionStorage.removeItem(SERVER_ID_KEY);
  localStorage.removeItem(SERVER_ID_KEY);
}

export interface ManagedServer {
  id: number;
  name: string;
  host: string;
  port: number;
  auth: string;
  is_enabled: boolean;
  is_default: boolean;
  configured: boolean;
  sort_order?: number;
  created_date?: string;
  updated_date?: string;
}

export interface BackupInfo {
  filename: string;
  size: number;
  created_at: string;
  is_auto: boolean;
}

export interface BackupSettings {
  enabled: boolean;
  interval_hours: number;
  backup_time: string;
  backup_dir: string;
  configured_dir: string;
  retention_count: number;
  last_run: string;
  last_error: string;
}

export interface WireGuardPeer {
  public_key: string;
  hub: string;
  user: string;
  label: string;
  assigned_ip: string;
  created_date?: string;
  has_private_key?: boolean;
}

export interface WireGuardHubNetwork {
  hub: string;
  gateway: string;
  subnet: string;
}

export interface WireGuardState {
  supported: boolean;
  enabled?: boolean;
  public_key?: string;
  preshared_key?: string;
  has_preshared_key?: boolean;
  udp_ports?: number[];
  default_udp_port?: number;
  wireguard_port?: number;
  wireguard_port_listening?: boolean;
  default_endpoint_host?: string;
  peers?: WireGuardPeer[];
  hub_networks?: WireGuardHubNetwork[];
}

export class ApiError extends Error {
  status: number;
  seError: string;
  constructor(status: number, message: string, seError = "") {
    super(message);
    this.status = status;
    this.seError = seError;
  }
}

export type Wire = Record<string, any>;

async function request<T = Wire>(method: string, path: string, body?: unknown, binary = false, serverId = getStoredServerId()): Promise<T> {
  const scoped = path === "/system/domain/push-cert" || path === "/system/domain/test-ssh" || (!path.startsWith("/auth/") && !path.startsWith("/system/") && !path.startsWith("/servers"));
  const assertServer = () => {
    if (scoped && (serverId === null || serverId !== getStoredServerId())) throw new DOMException("Server changed", "AbortError");
  };
  assertServer();
  const headers: Record<string, string> = {};
  if (serverId) headers["X-Server-ID"] = String(serverId);
  if (body !== undefined) headers["Content-Type"] = "application/json";

  const res = await fetch(`${BASE}api/v1${path}`, {
    method,
    headers,
    credentials: "include",
    body: body !== undefined ? JSON.stringify(body) : undefined,
  });

  if (binary && res.ok) {
    const blob = await res.blob();
    assertServer();
    return blob as T;
  }
  const text = await res.text();
  assertServer();

  if (res.status === 401) {
    clearLegacyToken();
    if (!path.includes("/auth/")) {
      window.dispatchEvent(new CustomEvent("sem-unauthorized"));
    }
  }

  if (!res.ok) {
    let detail = `Request failed (${res.status})`;
    let seError = "";
    try {
      const data = JSON.parse(text);
      if (typeof data?.detail === "string") detail = data.detail;
      else if (data?.detail?.message) {
        detail = data.detail.message;
        seError = data.detail.se_error ?? "";
      } else if (data?.detail) detail = JSON.stringify(data.detail);
    } catch {
      /* not json */
    }
    throw new ApiError(res.status, detail, seError);
  }

  if (res.status === 204) return undefined as T;
  return text ? (JSON.parse(text) as T) : (undefined as T);
}

export interface ConnectionTest {
  ok: boolean;
  auth: string;
  version?: string;
  hostname?: string;
  sessions?: number;
  rtt_ms: number;
}

export interface Probe {
  online: boolean;
  configured: boolean;
  error?: string;
  version?: string;
  hostname?: string;
  sessions?: number;
  users?: number;
  hubs?: number;
  send_bytes?: number;
  recv_bytes?: number;
  started?: string | null;
  current_time?: string | null;
}

export interface UsagePoint {
  t: string;
  send: number | null;
  recv: number | null;
  sessions?: number;
}

export interface Usage {
  hours: number;
  start?: string;
  end?: string;
  sample_interval_seconds?: number;
  points: UsagePoint[];
  total_send: number;
  total_recv: number;
  samples: number;
}

/** What a traffic quota counts. `total` is the two directions added up. */
export type QuotaMetric = "total" | "download" | "upload";
export type QuotaUnit = "MB" | "GB" | "TB";

/** A byte ceiling on a hub or on one user's config, and how far into it the
 *  subject has got this cycle. */
export interface Quota {
  subject: "hub" | "user";
  hub: string;
  username: string;
  /** False for a record that carries only a reset baseline, with no ceiling. */
  has_limit: boolean;
  limit_bytes: number;
  /** The same limit as the operator typed it: a number and a unit. */
  limit: number;
  unit: QuotaUnit;
  metric: QuotaMetric;
  enabled: boolean;
  upload_bytes: number;
  download_bytes: number;
  /** Where the last reset put the zero. Subtracting these from the counters a
   *  list already has is what keeps its Transfer column and its meter equal. */
  base_send_bytes: number;
  base_recv_bytes: number;
  /** Only what `metric` counts — this is what the limit is compared against. */
  used_bytes: number;
  remaining_bytes: number | null;
  percent: number;
  exceeded: boolean;
  exceeded_date: string | null;
  /** Whether the panel has actually cut the subject off. */
  blocked: boolean;
  blocked_date: string | null;
  cycle_start: string;
  updated_date: string;
}

export interface QuotaIn {
  limit: number;
  unit: QuotaUnit;
  metric: QuotaMetric;
  enabled: boolean;
  reset?: boolean;
}

export interface SessionReportQuery {
  from?: string;
  to?: string;
  hub?: string;
  q?: string;
  status?: "all" | "active" | "ended";
  protocol?: string;
  sort?: "started" | "bytes" | "duration";
  limit?: number;
  offset?: number;
}

export interface SessionReportSummary {
  total_sessions: number;
  active_sessions: number;
  unique_users: number;
  total_bytes: number;
  download_bytes: number;
  upload_bytes: number;
}

export interface SessionReportResponse {
  items: Wire[];
  total: number;
  summary: SessionReportSummary;
  hubs: string[];
}

export interface ServerAnalytics {
  hours: number;
  hub: string;
  top_users: {
    hub: string;
    username: string;
    real_name: string;
    send_bytes: number;
    recv_bytes: number;
    total_bytes: number;
  }[];
  hub_totals: {
    hub: string;
    send_bytes: number;
    recv_bytes: number;
    total_bytes: number;
    max_sessions: number;
  }[];
  protocol_counts: {
    protocol: string;
    count: number;
  }[];
}

export interface SessionAnalytics {
  granularity: "hour" | "day";
  buckets: {
    key: string;
    label: string;
    sessions: number;
    unique_users: number;
    total_bytes: number;
    download_bytes: number;
    upload_bytes: number;
  }[];
  top_users: {
    hub: string;
    username: string;
    real_name: string;
    sessions: number;
    total_bytes: number;
    download_bytes: number;
    upload_bytes: number;
  }[];
  top_locations: {
    location: string;
    sessions: number;
    total_bytes: number;
  }[];
  protocols: {
    protocol: string;
    sessions: number;
    total_bytes: number;
  }[];
  anomalies: {
    abnormal_disconnects: number;
    short_sessions: number;
    concurrent_multi_ip: number;
  };
}

function sessionReportQuery(params: SessionReportQuery & { lang?: string; tz?: string; tz_offset?: number }): string {
  const sp = new URLSearchParams();
  if (params.from) sp.set("from", params.from);
  if (params.to) sp.set("to", params.to);
  if (params.hub) sp.set("hub", params.hub);
  if (params.q) sp.set("q", params.q);
  if (params.status && params.status !== "all") sp.set("status", params.status);
  if (params.protocol) sp.set("protocol", params.protocol);
  if (params.sort && params.sort !== "started") sp.set("sort", params.sort);
  if (params.limit !== undefined) sp.set("limit", String(params.limit));
  if (params.offset !== undefined && params.offset > 0) sp.set("offset", String(params.offset));
  if (params.lang) sp.set("lang", params.lang);
  if (params.tz) sp.set("tz", params.tz);
  if (params.tz_offset !== undefined) sp.set("tz_offset", String(params.tz_offset));
  const s = sp.toString();
  return s ? `?${s}` : "";
}

/** The panel's own domain and certificate, every stage reported on its own. */
export interface DomainStatus {
  configured: boolean;
  domain: string;
  https_port: number;
  acme_email: string;
  acme_staging: boolean;
  domain_only: boolean;
  config_error: string;
  /** idle · waiting (no certificate yet) · issuing · renewing · ok · failed */
  phase: string;
  busy: boolean;
  /** What the issuance is doing right now, while busy. */
  step: string;
  dns: {
    checked_at?: string;
    domain?: string;
    addresses?: string[];
    local_addresses?: string[];
    /** True when the name points at one of this machine's addresses; null when it could not be resolved. */
    match?: boolean | null;
    error?: string;
  };
  http: { port: number; listening: boolean; error: string; redirecting: boolean };
  https: { port: number; listening: boolean; error: string; since: string };
  certificate: {
    present: boolean;
    domain: string;
    issued_at: string;
    not_before: string;
    expires_at: string;
    days_left: number | null;
    renew_at: string;
    issuer: string;
    serial: string;
    staging: boolean;
    expired: boolean;
    /** The certificate on disk does not fit the settings: another name, the
     *  other environment, or expired -- a new one is due. */
    stale: boolean;
    matches_domain: boolean;
  };
  renewal: {
    automatic: boolean;
    renew_before_days: number;
    renew_at: string;
    due: boolean;
    last_attempt_at: string;
    last_attempt_kind: string;
    last_success_at: string;
    last_error: string;
    last_error_type: string;
    last_error_at: string;
    next_attempt_at: string;
    consecutive_failures: number;
  };
  url: string;
  origin: string;
  events: { at: string; kind: string; message: string }[];
  /** The Host this very request came in on, and whether that is the domain. */
  request_host: string;
  via_domain: boolean;
  via_loopback: boolean;
  bind_port: number;
  web_path: string;
  changed?: string[];
  acme_challenge_type?: "dns-01" | "http-01";
  cf_dns_token?: string;
  has_cf_dns_token?: boolean;
  cert_key_type?: "rsa2048" | "ecdsa";
  cert_auto_push?: boolean;
  cert_push_server_id?: number;
  deployment_error?: string;
  cert_renew_hook?: string;
  cert_ssh_sync?: boolean;
  cert_ssh_host?: string;
  cert_ssh_port?: number;
  cert_ssh_user?: string;
  cert_ssh_auth_type?: "key" | "password";
  cert_ssh_key?: string;
  has_cert_ssh_key?: boolean;
  cert_ssh_public_key?: string;
  cert_ssh_password?: string;
  has_cert_ssh_password?: boolean;
  cert_ssh_remote_dir?: string;
  cert_ssh_host_key?: string;
  vpn_public_host?: string;
  vpn_public_port?: number;
  ssl_dir?: string;
  ssl_files?: { pem: boolean; key: boolean };
}

export interface DomainIn {
  domain?: string;
  https_port?: number;
  acme_email?: string;
  acme_staging?: boolean;
  domain_only?: boolean;
  acme_challenge_type?: "dns-01" | "http-01";
  cf_dns_token?: string;
  has_cf_dns_token?: boolean;
  cert_key_type?: "rsa2048" | "ecdsa";
  cert_auto_push?: boolean;
  cert_push_server_id?: number;
  cert_renew_hook?: string;
  cert_ssh_sync?: boolean;
  cert_ssh_host?: string;
  cert_ssh_port?: number;
  cert_ssh_user?: string;
  cert_ssh_auth_type?: "key" | "password";
  cert_ssh_key?: string;
  cert_ssh_password?: string;
  cert_ssh_remote_dir?: string;
  reset_cert_ssh_host_key?: boolean;
}

const hubPath = (hub: string) => `/hubs/${encodeURIComponent(hub)}`;

export function createServerApi(serverId: number | null) {
  const request = <T = Wire>(method: string, path: string, body?: unknown, binary = false) =>
    sendRequest<T>(method, path, body, binary, serverId);
  return apiMethods(request);
}

const sendRequest = request;

// Screens remount on a server switch; their pending operations keep this original scope.
export function useServerApi() {
  const [serverId] = useState(getStoredServerId);
  return useMemo(() => createServerApi(serverId), [serverId]);
}

function apiMethods(request: typeof sendRequest) {
  return {
  // -- auth ------------------------------------------------------------------
  authState: () => request<{ setup_required: boolean }>("GET", "/auth/state"),
  setup: (username: string, password: string) =>
    request<{ username: string }>("POST", "/auth/setup", { username, password }),
  login: (username: string, password: string) =>
    request<{ username: string }>("POST", "/auth/login", { username, password }),
  logout: () => request("POST", "/auth/logout"),
  me: () => request<{ username: string }>("GET", "/auth/me"),
  changePassword: (current_password: string, new_password: string) =>
    request("PUT", "/auth/password", { current_password, new_password }),

  // -- the panel itself --------------------------------------------------------
  systemInfo: () => request("GET", "/system/info"),
  resources: () => request("GET", "/system/resources"),
  vpnTemplate: () => request<Wire>("GET", "/system/vpn-template"),
  saveVpnTemplate: (body: Wire) => request<Wire>("PUT", "/system/vpn-template", body),
  settings: () => request("GET", "/system/settings"),
  saveSettings: (body: Wire) => request("PUT", "/system/settings", body),
  updateStatus: () => request("GET", "/system/update"),
  updateCheck: () => request("POST", "/system/update/check"),
  updateApply: (version = "") => request("POST", "/system/update/apply", { version }),
  updateState: () => request("GET", "/system/update/state"),
  restartPanel: () => request("POST", "/system/restart"),
  domain: () => request<DomainStatus>("GET", "/system/domain"),
  saveDomain: (body: DomainIn) => request<DomainStatus>("PUT", "/system/domain", body),
  removeDomain: () => request<DomainStatus>("DELETE", "/system/domain"),
  issueCertificate: () => request<DomainStatus>("POST", "/system/domain/issue"),
  checkDomain: () => request<DomainStatus>("POST", "/system/domain/check"),
  pushDomainCert: () => request<{ ok: boolean; message: string }>("POST", "/system/domain/push-cert"),
  generateDomainSshKey: () => request<{ ok: boolean; public_key: string; has_cert_ssh_key: boolean }>("POST", "/system/domain/ssh-keygen"),
  testDomainSsh: (body?: {
    cert_ssh_host?: string;
    cert_ssh_port?: number;
    cert_ssh_user?: string;
    cert_ssh_auth_type?: "key" | "password";
    cert_ssh_key?: string;
    cert_ssh_password?: string;
    cert_ssh_remote_dir?: string;
    reset_host_key?: boolean;
  }) => request<{ ok: boolean; remote_dir: string; fingerprint: string; host_key: string }>("POST", "/system/domain/test-ssh", body ?? {}),
  audit: (beforeId = 0) =>
    request<Wire[]>("GET", `/system/audit?limit=100${beforeId ? `&before_id=${beforeId}` : ""}`),

  // -- the connection to SoftEther ----------------------------------------------
  connection: () => request<{ host: string; port: number; configured: boolean; name?: string; server_id?: number; public_host?: string; public_port?: number }>("GET", "/server/connection"),
  saveConnection: (body: Wire) => request("PUT", "/server/connection", body),
  testConnection: (body: Wire) => request("POST", "/server/connection/test", body),
  probe: () => request<Probe>("POST", "/server/probe"),

  // -- multi-server cluster management -----------------------------------------
  servers: () => request<ManagedServer[]>("GET", "/servers"),
  server: (id: number) => request<ManagedServer>("GET", `/servers/${id}`),
  createServer: (body: Wire) => request<ManagedServer>("POST", "/servers", body),
  updateServer: (id: number, body: Wire) => request<ManagedServer>("PUT", `/servers/${id}`, body),
  deleteServer: (id: number) => request<{ ok: boolean }>("DELETE", `/servers/${id}`),
  testServerConnection: (body: Wire) => request<ConnectionTest>("POST", "/servers/test", body),
  testServerId: (id: number, body?: Wire) => request<ConnectionTest>("POST", `/servers/${id}/test`, body),
  serverCertificate: (id: number) => request<{ fingerprint: string; pinned: string }>("GET", `/servers/${id}/certificate`),
  trustServerCertificate: (id: number, fingerprint: string) => request("PUT", `/servers/${id}/certificate`, { fingerprint }),
  setDefaultServer: (id: number) => request<{ ok: boolean; default_id: number }>("POST", `/servers/${id}/set-default`),
  probeServer: (id: number) => request<Probe>("POST", `/servers/${id}/probe`),

  // -- server-level SoftEther -----------------------------------------------------
  overview: () => {
    const sp = new URLSearchParams();
    try {
      const tz = Intl.DateTimeFormat().resolvedOptions().timeZone;
      if (tz) sp.set("tz", tz);
    } catch {}
    sp.set("tz_offset", String(new Date().getTimezoneOffset()));
    return request("GET", `/server/overview?${sp.toString()}`);
  },
  serverTraffic: (hours = 24) => request<Usage>("GET", `/server/traffic?hours=${hours}`),
  serverAnalytics: (hours = 24, hub = "", range?: "today") => {
    if (range === "today") {
      const sp = new URLSearchParams({ range: "today" });
      if (hub) sp.set("hub", hub);
      try {
        const tz = Intl.DateTimeFormat().resolvedOptions().timeZone;
        if (tz) sp.set("tz", tz);
      } catch {}
      sp.set("tz_offset", String(new Date().getTimezoneOffset()));
      return request<ServerAnalytics>("GET", `/server/analytics?${sp.toString()}`);
    }
    return request<ServerAnalytics>(
      "GET",
      `/server/analytics?hours=${hours}${hub ? `&hub=${encodeURIComponent(hub)}` : ""}`,
    );
  },
  serverInfo: () => request("GET", "/server/info"),
  serverStatus: () => request("GET", "/server/status"),
  caps: () => request("GET", "/server/caps"),
  listeners: () => request("GET", "/server/listeners"),
  createListener: (port: number, enable = true) =>
    request("POST", "/server/listeners", { port, enable }),
  toggleListener: (port: number, enable: boolean) =>
    request("PUT", `/server/listeners/${port}`, { port, enable }),
  deleteListener: (port: number) => request("DELETE", `/server/listeners/${port}`),
  specialListener: () => request("GET", "/server/special-listener"),
  setSpecialListener: (body: Wire) => request("PUT", "/server/special-listener", body),
  ipsec: () => request("GET", "/server/ipsec"),
  setIpsec: (body: Wire) => request("PUT", "/server/ipsec", body),
  openvpn: () => request("GET", "/server/openvpn"),
  setOpenvpn: (body: Wire) => request("PUT", "/server/openvpn", body),
  openvpnSample: (body?: { host?: string; port?: number }) =>
    request<{ filename: string; config: string }>("POST", "/server/openvpn/sample-config", body),
  wireguard: () => request<WireGuardState>("GET", "/server/wireguard"),
  setWireguard: (body: Wire) => request<{ restarting: boolean }>("PUT", "/server/wireguard", body),
  setWireguardHubNetwork: (hub: string, body: { gateway: string; subnet: string; confirm_restart: boolean }) =>
    request<{ restarting: boolean }>("PUT", `/hubs/${encodeURIComponent(hub)}/wireguard/network`, body),
  addWireguardPeer: (hub: string, body: Wire) =>
    request<{ restarting: boolean; peer: WireGuardPeer; client_private_key?: string; client_config: string }>(
      "POST",
      `/hubs/${encodeURIComponent(hub)}/wireguard/peers`,
      body,
    ),
  deleteWireguardPeer: (hub: string, body: { public_key: string; confirm_restart: boolean }) =>
    request<{ restarting: boolean }>("POST", `/hubs/${encodeURIComponent(hub)}/wireguard/peers/delete`, body),
  rotateWireguardPeer: (hub: string, body: { public_key: string; confirm_restart: boolean }) =>
    request<{ restarting: boolean; peer: WireGuardPeer; client_private_key: string; client_config: string }>(
      "POST",
      `/hubs/${encodeURIComponent(hub)}/wireguard/peers/rotate`,
      body,
    ),
  updateWireguardPeerMeta: (
    hub: string,
    body: { public_key: string; label: string; assigned_ip: string; client_private_key?: string },
  ) => request<WireGuardPeer>("PUT", `/hubs/${encodeURIComponent(hub)}/wireguard/peers/meta`, body),
  wireguardClientConfig: (hub: string, body: Wire) =>
    request<{ filename: string; config: string; assigned_ip: string; has_private_key?: boolean }>(
      "POST",
      `/hubs/${encodeURIComponent(hub)}/wireguard/config`,
      body,
    ),
  azure: () => request("GET", "/server/azure"),
  setAzure: (enabled: boolean) => request("PUT", "/server/azure", { IsEnabled_bool: enabled }),
  ddns: () => request("GET", "/server/ddns"),
  setDdnsEnabled: (enabled: boolean) => request("PUT", "/server/ddns", { enabled, confirm_restart: true }),
  setDdnsHostname: (hostname: string) => request("PUT", "/server/ddns/hostname", { hostname }),
  ddnsProxy: () => request("GET", "/server/ddns/proxy"),
  setDdnsProxy: (body: Wire) => request("PUT", "/server/ddns/proxy", body),
  cipher: () => request("GET", "/server/cipher"),
  setCipher: (cipher: string) => request("PUT", "/server/cipher", { cipher }),
  cert: () => request("GET", "/server/cert"),
  setCert: (cert_base64: string, key_base64: string) =>
    request("PUT", "/server/cert", { cert_base64, key_base64 }),
  regenerateCert: (common_name: string) =>
    request("POST", "/server/cert/regenerate", { common_name }),
  setAdminPassword: (password: string) => request("PUT", "/server/admin-password", { password }),
  bridges: () => request("GET", "/server/bridges"),
  bridgeSupport: () => request("GET", "/server/bridges/support"),
  bridgeDevices: () => request("GET", "/server/bridges/devices"),
  addBridge: (device: string, hub: string) => request("POST", "/server/bridges", { device, hub }),
  deleteBridge: (device: string, hub: string) =>
    request("POST", "/server/bridges/delete", { device, hub }),
  l3: () => request("GET", "/server/l3"),
  addL3: (name: string) => request("POST", "/server/l3", { name }),
  delL3: (name: string) => request("DELETE", `/server/l3/${encodeURIComponent(name)}`),
  startL3: (name: string) => request("POST", `/server/l3/${encodeURIComponent(name)}/start`),
  stopL3: (name: string) => request("POST", `/server/l3/${encodeURIComponent(name)}/stop`),
  l3Ifs: (name: string) => request("GET", `/server/l3/${encodeURIComponent(name)}/interfaces`),
  addL3If: (name: string, body: Wire) =>
    request("POST", `/server/l3/${encodeURIComponent(name)}/interfaces`, body),
  delL3If: (name: string, body: Wire) =>
    request("POST", `/server/l3/${encodeURIComponent(name)}/interfaces/delete`, body),
  l3Routes: (name: string) => request("GET", `/server/l3/${encodeURIComponent(name)}/routes`),
  addL3Route: (name: string, body: Wire) =>
    request("POST", `/server/l3/${encodeURIComponent(name)}/routes`, body),
  delL3Route: (name: string, body: Wire) =>
    request("POST", `/server/l3/${encodeURIComponent(name)}/routes/delete`, body),
  farm: () => request("GET", "/server/farm"),
  setFarm: (body: Wire) => request("PUT", "/server/farm", body),
  farmStatus: () => request("GET", "/server/farm/status"),
  farmMembers: () => request("GET", "/server/farm/members"),
  keepalive: () => request("GET", "/server/keepalive"),
  setKeepalive: (body: Wire) => request("PUT", "/server/keepalive", body),
  syslog: () => request("GET", "/server/syslog"),
  setSyslog: (body: Wire) => request("PUT", "/server/syslog", body),
  etherip: () => request("GET", "/server/etherip"),
  addEtherip: (body: Wire) => request("POST", "/server/etherip", body),
  deleteEtherip: (key: string) => request("DELETE", `/server/etherip/${encodeURIComponent(key)}`),
  logFiles: () => request("GET", "/server/logs"),
  readLog: (path: string, offset = 0, server = "") =>
    request<{ path: string; offset: number; text: string; bytes: number }>(
      "GET",
      `/server/logs/read?path=${encodeURIComponent(path)}&offset=${offset}${server ? `&server=${encodeURIComponent(server)}` : ""}`,
    ),
  getConfig: () => request<{ filename: string; config_base64: string }>("GET", "/server/config"),
  setConfig: (config_base64: string) => request("PUT", "/server/config", { config_base64 }),
  getBackups: () => request<{ backups: BackupInfo[] }>("GET", "/server/backups"),
  createBackup: () => request<BackupInfo>("POST", "/server/backups"),
  downloadBackup: (filename: string) => request<Blob>("GET", `/server/backups/${encodeURIComponent(filename)}/download`, undefined, true),
  restoreBackup: (filename: string) => request("POST", `/server/backups/${encodeURIComponent(filename)}/restore`),
  deleteBackup: (filename: string) => request("DELETE", `/server/backups/${encodeURIComponent(filename)}`),
  getBackupSettings: () => request<BackupSettings>("GET", "/server/backup-settings"),
  saveBackupSettings: (body: Partial<BackupSettings>) => request<BackupSettings>("PUT", "/server/backup-settings", body),
  flush: () => request("POST", "/server/flush"),
  reboot: () => request("POST", "/server/reboot"),
  crashServer: () => request("POST", "/server/crash"),

  // -- every user on the server ---------------------------------------------------
  allUsers: () =>
    request<{ hubs: string[]; users: Wire[]; errors: { hub: string; error: string }[] }>(
      "GET",
      "/users",
    ),

  // -- every online client across all hubs -----------------------------------------
  allClients: async (): Promise<{ hubs: string[]; clients: Wire[]; errors: { hub: string; error: string }[] }> => {
    try {
      const res = await request<{ hubs: string[]; clients: Wire[]; errors: { hub: string; error: string }[] }>("GET", "/clients");
      if (res && Array.isArray(res.clients)) return res;
    } catch (e) {
      if (!(e instanceof ApiError && e.status === 404)) throw e;
    }

    const hubListRes = await api.hubs().catch(() => null);
    const hubList = (hubListRes?.HubList as Wire[]) ?? [];
    const hubs = hubList.map((h) => String(h.HubName_str)).filter(Boolean);
    const clients: Wire[] = [];
    const errors: { hub: string; error: string }[] = [];

    await Promise.all(
      hubs.map(async (hub) => {
        try {
          const r = await api.sessions(hub).catch(() => null);
          const list = (r?.SessionList as Wire[]) ?? [];
          for (const s of list) {
            const isSystem = Boolean(s.LinkMode_bool || s.SecureNATMode_bool || s.BridgeMode_bool || s.Layer3Mode_bool);
            if (!isSystem) {
              s.HubName_str = hub;
              clients.push(s);
            }
          }
        } catch (e) {
          errors.push({ hub, error: String(e) });
        }
      }),
    );
    return { hubs, clients, errors };
  },

  // -- hubs -------------------------------------------------------------------------
  hubs: () => request("GET", "/hubs"),
  createHub: (body: Wire) => request("POST", "/hubs", body),
  hub: (hub: string) => request("GET", hubPath(hub)),
  setHub: (hub: string, body: Wire) => request("PUT", hubPath(hub), body),
  deleteHub: (hub: string) => request("DELETE", hubPath(hub)),
  hubOnline: (hub: string, online: boolean) => request("PUT", `${hubPath(hub)}/online`, { online }),
  hubStatus: (hub: string) => request("GET", `${hubPath(hub)}/status`),
  hubTraffic: (hub: string, hours: number) =>
    request<Usage>("GET", `${hubPath(hub)}/traffic?hours=${hours}`),
  hubLog: (hub: string) => request("GET", `${hubPath(hub)}/log-settings`),
  setHubLog: (hub: string, body: Wire) => request("PUT", `${hubPath(hub)}/log-settings`, body),
  radius: (hub: string) => request("GET", `${hubPath(hub)}/radius`),
  setRadius: (hub: string, body: Wire) => request("PUT", `${hubPath(hub)}/radius`, body),
  hubMsg: (hub: string) => request<{ message: string }>("GET", `${hubPath(hub)}/message`),
  setHubMsg: (hub: string, message: string) =>
    request("PUT", `${hubPath(hub)}/message`, { message }),
  adminOptions: (hub: string) => request("GET", `${hubPath(hub)}/admin-options`),
  setAdminOptions: (hub: string, body: Wire) =>
    request("PUT", `${hubPath(hub)}/admin-options`, body),
  extOptions: (hub: string) => request("GET", `${hubPath(hub)}/ext-options`),
  setExtOptions: (hub: string, body: Wire) => request("PUT", `${hubPath(hub)}/ext-options`, body),

  // -- users / groups -----------------------------------------------------------------
  users: (hub: string) => request("GET", `${hubPath(hub)}/users`),
  createUser: (hub: string, body: Wire) => request("POST", `${hubPath(hub)}/users`, body),
  exportUsers: (hub: string) => request<{ filename: string; csv: string; count: number }>("GET", `${hubPath(hub)}/users/export`),
  importUsers: (hub: string, csv: string) =>
    request<{ created: string[]; skipped: Wire[]; failed: Wire[] }>("POST", `${hubPath(hub)}/users/import`, { csv }),
  user: (hub: string, name: string) =>
    request("GET", `${hubPath(hub)}/users/${encodeURIComponent(name)}`),
  setUser: (hub: string, name: string, body: Wire) =>
    request("PUT", `${hubPath(hub)}/users/${encodeURIComponent(name)}`, body),
  deleteUser: (hub: string, name: string) =>
    request("DELETE", `${hubPath(hub)}/users/${encodeURIComponent(name)}`),
  hubUsersOnline: (hub: string) =>
    request<{ usernames: string[] }>("GET", `${hubPath(hub)}/users-online`),
  userSessions: (hub: string, name: string) =>
    request("GET", `${hubPath(hub)}/users/${encodeURIComponent(name)}/sessions`),
  userCredentialState: (hub: string, name: string) =>
    request<{ available: boolean; source: string | null }>(
      "GET",
      `${hubPath(hub)}/users/${encodeURIComponent(name)}/credential-state`,
    ),
  userVpnFile: (
    hub: string,
    name: string,
    body: { host: string; port: number; embed_password: boolean; password?: string; account_name?: string; filename?: string },
  ) =>
    request<{ filename: string; content: string }>(
      "POST",
      `${hubPath(hub)}/users/${encodeURIComponent(name)}/vpn-file`,
      body,
    ),
  userUsage: (hub: string, name: string, hours: number) =>
    request<Usage>("GET", `${hubPath(hub)}/users/${encodeURIComponent(name)}/usage?hours=${hours}`),
  sessionUsage: (hub: string, id: number) =>
    request<Usage & { session: Wire }>("GET", `${hubPath(hub)}/session-history/${id}/usage`),
  userSessionHistory: (hub: string, name: string, beforeId = 0) =>
    request<Wire[]>(
      "GET",
      `${hubPath(hub)}/users/${encodeURIComponent(name)}/session-history?limit=100${beforeId ? `&before_id=${beforeId}` : ""}`,
    ),
  sessionReport: (params: SessionReportQuery = {}) =>
    request<SessionReportResponse>("GET", `/server/session-history${sessionReportQuery(params)}`),
  sessionReportAnalytics: (params: SessionReportQuery & { tz?: string; tz_offset?: number } = {}) =>
    request<SessionAnalytics>("GET", `/server/session-history/analytics${sessionReportQuery(params)}`),
  exportSessionReport: (params: SessionReportQuery & { lang?: string; tz?: string; tz_offset?: number } = {}) =>
    request<Blob>("GET", `/server/session-history/export${sessionReportQuery(params)}`, undefined, true),
  groups: (hub: string) => request("GET", `${hubPath(hub)}/groups`),
  createGroup: (hub: string, body: Wire) => request("POST", `${hubPath(hub)}/groups`, body),
  group: (hub: string, name: string) =>
    request("GET", `${hubPath(hub)}/groups/${encodeURIComponent(name)}`),
  setGroup: (hub: string, name: string, body: Wire) =>
    request("PUT", `${hubPath(hub)}/groups/${encodeURIComponent(name)}`, body),
  deleteGroup: (hub: string, name: string) =>
    request("DELETE", `${hubPath(hub)}/groups/${encodeURIComponent(name)}`),

  // -- sessions ---------------------------------------------------------------------------
  sessions: (hub: string) => request("GET", `${hubPath(hub)}/sessions`),
  sessionStatus: (hub: string, name: string) =>
    request("GET", `${hubPath(hub)}/sessions/${encodeURIComponent(name)}`),
  killSession: (hub: string, name: string) =>
    request("DELETE", `${hubPath(hub)}/sessions/${encodeURIComponent(name)}`),

  // -- access control -----------------------------------------------------------------------
  accessList: (hub: string) => request("GET", `${hubPath(hub)}/access`),
  addAccess: (hub: string, rule: Wire) => request("POST", `${hubPath(hub)}/access`, rule),
  setAccessList: (hub: string, rules: Wire[], version: string) =>
    request("PUT", `${hubPath(hub)}/access`, { AccessList: rules, expected_version: version }),
  deleteAccess: (hub: string, ruleId: number, version: string) =>
    request("DELETE", `${hubPath(hub)}/access/${ruleId}`, { expected_version: version }),
  acList: (hub: string) => request("GET", `${hubPath(hub)}/ac-list`),
  setAcList: (hub: string, rules: Wire[], version: string) =>
    request("PUT", `${hubPath(hub)}/ac-list`, { ACList: rules, expected_version: version }),

  // -- CA / CRL ---------------------------------------------------------------------------------
  cas: (hub: string) => request("GET", `${hubPath(hub)}/ca`),
  addCa: (hub: string, cert_base64: string) => request("POST", `${hubPath(hub)}/ca`, { cert_base64 }),
  getCa: (hub: string, key: number) => request("GET", `${hubPath(hub)}/ca/${key}`),
  deleteCa: (hub: string, key: number) => request("DELETE", `${hubPath(hub)}/ca/${key}`),
  crls: (hub: string) => request("GET", `${hubPath(hub)}/crl`),
  addCrl: (hub: string, body: Wire) => request("POST", `${hubPath(hub)}/crl`, body),
  deleteCrl: (hub: string, key: number) => request("DELETE", `${hubPath(hub)}/crl/${key}`),

  // -- SecureNAT ----------------------------------------------------------------------------------
  securenat: (hub: string) => request("GET", `${hubPath(hub)}/securenat`),
  securenatEnable: (hub: string, on: boolean) =>
    request("POST", `${hubPath(hub)}/securenat/${on ? "enable" : "disable"}`),
  setSecurenatOptions: (hub: string, body: Wire) =>
    request("PUT", `${hubPath(hub)}/securenat/options`, body),
  natTable: (hub: string) => request("GET", `${hubPath(hub)}/securenat/nat-table`),
  dhcpTable: (hub: string) => request("GET", `${hubPath(hub)}/securenat/dhcp-table`),

  // -- cascade links --------------------------------------------------------------------------------
  links: (hub: string) => request("GET", `${hubPath(hub)}/links`),
  createLink: (hub: string, body: Wire) => request("POST", `${hubPath(hub)}/links`, body),
  link: (hub: string, name: string) =>
    request("GET", `${hubPath(hub)}/links/${encodeURIComponent(name)}`),
  setLink: (hub: string, name: string, body: Wire) =>
    request("PUT", `${hubPath(hub)}/links/${encodeURIComponent(name)}`, body),
  deleteLink: (hub: string, name: string) =>
    request("DELETE", `${hubPath(hub)}/links/${encodeURIComponent(name)}`),
  linkStatus: (hub: string, name: string) =>
    request("GET", `${hubPath(hub)}/links/${encodeURIComponent(name)}/status`),
  linkOnline: (hub: string, name: string, online: boolean) =>
    request("PUT", `${hubPath(hub)}/links/${encodeURIComponent(name)}/online`, { online }),
  renameLink: (hub: string, name: string, newName: string) =>
    request("POST", `${hubPath(hub)}/links/${encodeURIComponent(name)}/rename`, { new_name: newName }),

  // -- address tables ----------------------------------------------------------------------------------
  macTable: (hub: string) => request("GET", `${hubPath(hub)}/mac-table`),
  deleteMac: (hub: string, key: number) => request("DELETE", `${hubPath(hub)}/mac-table/${key}`),
  ipTable: (hub: string) => request("GET", `${hubPath(hub)}/ip-table`),
  deleteIp: (hub: string, key: number) => request("DELETE", `${hubPath(hub)}/ip-table/${key}`),

  // -- traffic quotas ----------------------------------------------------------------------
  quotas: () => request<Quota[]>("GET", "/quotas"),
  hubQuota: (hub: string) => request<Quota>("GET", `/quotas/hub/${encodeURIComponent(hub)}`),
  setHubQuota: (hub: string, body: QuotaIn) =>
    request<Quota>("PUT", `/quotas/hub/${encodeURIComponent(hub)}`, body),
  setHubQuotaEnabled: (hub: string, enabled: boolean) =>
    request<Quota>("PUT", `/quotas/hub/${encodeURIComponent(hub)}/enabled`, { enabled }),
  deleteHubQuota: (hub: string) => request("DELETE", `/quotas/hub/${encodeURIComponent(hub)}`),
  resetHubTransfer: (hub: string) =>
    request<Quota>("POST", `/quotas/hub/${encodeURIComponent(hub)}/reset`),
  userQuota: (hub: string, name: string) =>
    request<Quota>("GET", `/quotas/user/${encodeURIComponent(hub)}/${encodeURIComponent(name)}`),
  setUserQuota: (hub: string, name: string, body: QuotaIn) =>
    request<Quota>("PUT", `/quotas/user/${encodeURIComponent(hub)}/${encodeURIComponent(name)}`, body),
  setUserQuotaEnabled: (hub: string, name: string, enabled: boolean) =>
    request<Quota>("PUT", `/quotas/user/${encodeURIComponent(hub)}/${encodeURIComponent(name)}/enabled`, { enabled }),
  deleteUserQuota: (hub: string, name: string) =>
    request("DELETE", `/quotas/user/${encodeURIComponent(hub)}/${encodeURIComponent(name)}`),
  resetUserTransfer: (hub: string, name: string) =>
    request<Quota>("POST", `/quotas/user/${encodeURIComponent(hub)}/${encodeURIComponent(name)}/reset`),
  };
}

export const api = apiMethods(sendRequest);
export type ServerApi = ReturnType<typeof createServerApi>;
