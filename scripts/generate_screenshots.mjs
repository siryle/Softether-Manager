import http from "node:http";
import fs from "node:fs";
import { createRequire } from "node:module";
import path from "node:path";
import { fileURLToPath } from "node:url";

const __dirname = path.dirname(fileURLToPath(import.meta.url));
const ROOT = path.resolve(__dirname, "..");
const OUT_DIR = path.resolve(process.env.SEM_SCREENSHOT_WEB_DIR || path.join(ROOT, "app", "web", "out"));
const SCREENSHOTS_DIR = path.join(ROOT, "docs", "screenshots");
const version = fs.readFileSync(path.join(ROOT, "VERSION"), "utf8").trim();

// ── Mock Data Generator ────────────────────────────────────────────────────────
function generateTrafficPoints(hours, totalRecv, totalSend, seed = 42) {
  const stepMs = 5 * 60 * 1000; // 5 min interval
  const count = Math.round((hours * 3600 * 1000) / stepMs);
  const now = Date.now();
  const start = now - hours * 3600 * 1000;

  // Deterministic PRNG for reproducible, natural, organic curves
  let s = seed;
  const rand = () => {
    s = (s * 1664525 + 1013904223) % 4294967296;
    return s / 4294967296;
  };

  const rawRecv = new Float64Array(count + 1);
  const rawSend = new Float64Array(count + 1);

  let currentRecv = 0.25;
  let currentSend = 0.15;

  for (let i = 0; i <= count; i++) {
    const t = start + i * stepMs;
    const hourOfDay = ((new Date(t).getUTCHours() + 8) % 24) + new Date(t).getUTCMinutes() / 60;
    
    // Smooth diurnal daily profile
    let base = 0.15;
    if (hourOfDay >= 6 && hourOfDay < 9.5) {
      const p = (hourOfDay - 6) / 3.5;
      base = 0.15 + 0.65 * (1 - Math.cos(p * Math.PI)) / 2;
    } else if (hourOfDay >= 9.5 && hourOfDay < 12) {
      base = 0.82 + 0.08 * Math.sin((hourOfDay - 9.5) * 1.2);
    } else if (hourOfDay >= 12 && hourOfDay < 13.5) {
      base = 0.68 + 0.06 * Math.cos((hourOfDay - 12) * Math.PI / 1.5);
    } else if (hourOfDay >= 13.5 && hourOfDay < 17.5) {
      base = 0.88 + 0.10 * Math.sin((hourOfDay - 13.5) * 0.8);
    } else if (hourOfDay >= 17.5 && hourOfDay < 20.5) {
      const p = (hourOfDay - 17.5) / 3.0;
      base = 0.80 - 0.40 * (1 - Math.cos(p * Math.PI)) / 2;
    } else if (hourOfDay >= 20.5) {
      const p = (hourOfDay - 20.5) / 3.5;
      base = 0.40 - 0.25 * p;
    }

    // Natural low-frequency random walk momentum
    const driftR = (rand() - 0.49) * 0.035;
    const driftS = (rand() - 0.49) * 0.025;
    currentRecv = currentRecv * 0.90 + base * 0.10 + driftR;
    currentSend = currentSend * 0.90 + (base * 0.62) * 0.10 + driftS;

    rawRecv[i] = Math.max(0.08, currentRecv);
    rawSend[i] = Math.max(0.05, currentSend);
  }

  // 5-point Gaussian smoothing to eliminate jagged comb-teeth
  const weights = [0.1, 0.2, 0.4, 0.2, 0.1];
  const smoothedRecv = new Float64Array(count + 1);
  const smoothedSend = new Float64Array(count + 1);

  for (let i = 0; i <= count; i++) {
    let rSum = 0, sSum = 0, wSum = 0;
    for (let k = -2; k <= 2; k++) {
      const idx = i + k;
      if (idx >= 0 && idx <= count) {
        const w = weights[k + 2];
        rSum += rawRecv[idx] * w;
        sSum += rawSend[idx] * w;
        wSum += w;
      }
    }
    smoothedRecv[i] = rSum / wSum;
    smoothedSend[i] = sSum / wSum;
  }

  // Scale accurately to target totals
  let sumR = 0, sumS = 0;
  for (let i = 0; i <= count; i++) {
    sumR += smoothedRecv[i];
    sumS += smoothedSend[i];
  }

  const points = [];
  for (let i = 0; i <= count; i++) {
    const t = new Date(start + i * stepMs).toISOString();
    const recv = Math.round((smoothedRecv[i] / sumR) * totalRecv);
    const send = Math.round((smoothedSend[i] / sumS) * totalSend);
    points.push({ t, recv, send });
  }

  return {
    hours,
    start: new Date(start).toISOString(),
    end: new Date(now).toISOString(),
    sample_interval_seconds: 300,
    total_recv: totalRecv,
    total_send: totalSend,
    points,
  };
}

const mockServers = [
  {
    id: 1,
    name: "vpnserver",
    host: "127.0.0.1",
    port: 5555,
    auth: "header",
    is_enabled: true,
    is_default: true,
    configured: true,
    sort_order: 1,
    created_date: "2026-08-01T00:00:00Z",
  },
  {
    id: 2,
    name: "Tokyo Node",
    host: "192.168.1.10",
    port: 5555,
    auth: "header",
    is_enabled: true,
    is_default: false,
    configured: true,
    sort_order: 2,
    created_date: "2026-09-01T00:00:00Z",
  },
  {
    id: 3,
    name: "Sydney Node",
    host: "192.168.2.20",
    port: 5555,
    auth: "header",
    is_enabled: true,
    is_default: false,
    configured: true,
    sort_order: 3,
    created_date: "2026-10-01T00:00:00Z",
  },
];

const mockHubs = [
  {
    HubName_str: "DEFAULT",
    Online_bool: true,
    HubType_u32: 0,
    NumUsers_u32: 3,
    NumGroups_u32: 1,
    NumSessions_u32: 2,
    NumMacTables_u32: 4,
    NumIpTables_u32: 4,
    "Recv.BroadcastBytes_u64": 1289000,
    "Recv.UnicastBytes_u64": 34359738368,
    "Send.BroadcastBytes_u64": 2560000,
    "Send.UnicastBytes_u64": 25769803776,
    CreatedTime_dt: "2026-08-01T00:00:00.000Z",
    LastCommTime_dt: "2026-11-17T10:00:00.000Z",
    LastLoginTime_dt: "2026-11-17T09:45:00.000Z",
  },
  {
    HubName_str: "OFFICE",
    Online_bool: true,
    HubType_u32: 0,
    NumUsers_u32: 2,
    NumGroups_u32: 1,
    NumSessions_u32: 1,
    NumMacTables_u32: 4,
    NumIpTables_u32: 4,
    "Recv.BroadcastBytes_u64": 512000,
    "Recv.UnicastBytes_u64": 17179869184,
    "Send.BroadcastBytes_u64": 1024000,
    "Send.UnicastBytes_u64": 27917287424,
    CreatedTime_dt: "2026-09-01T00:00:00.000Z",
    LastCommTime_dt: "2026-11-17T09:50:00.000Z",
    LastLoginTime_dt: "2026-11-17T09:30:00.000Z",
  },
];

const mockUsers = [
  {
    HubName_str: "DEFAULT",
    Name_str: "alice",
    Realname_utf: "Alice Smith",
    GroupName_str: "finance",
    Note_utf: "Finance Department",
    AuthType_u32: 1,
    CreatedTime_dt: "2026-08-10T12:00:00.000Z",
    UpdatedTime_dt: "2026-08-10T12:00:00.000Z",
    LastLoginTime_dt: "2026-11-17T09:45:00.000Z",
    ExpireTime_dt: 0,
    NumLogin_u32: 42,
    Online_bool: true,
    "Recv.BroadcastBytes_u64": 0,
    "Recv.UnicastBytes_u64": 15000000000,
    "Send.BroadcastBytes_u64": 0,
    "Send.UnicastBytes_u64": 11300000000,
    "Ex.Recv.BroadcastBytes_u64": 0,
    "Ex.Recv.UnicastBytes_u64": 15000000000,
    "Ex.Send.BroadcastBytes_u64": 0,
    "Ex.Send.UnicastBytes_u64": 11300000000,
    online: true,
  },
  {
    HubName_str: "DEFAULT",
    Name_str: "bob",
    Realname_utf: "Bob Jones",
    GroupName_str: "dev",
    Note_utf: "Engineering Team",
    AuthType_u32: 2,
    CreatedTime_dt: "2026-08-15T14:30:00.000Z",
    UpdatedTime_dt: "2026-08-15T14:30:00.000Z",
    LastLoginTime_dt: "2026-11-17T09:15:00.000Z",
    ExpireTime_dt: 0,
    NumLogin_u32: 19,
    Online_bool: true,
    "Recv.BroadcastBytes_u64": 0,
    "Recv.UnicastBytes_u64": 7500000000,
    "Send.BroadcastBytes_u64": 0,
    "Send.UnicastBytes_u64": 5500000000,
    "Ex.Recv.BroadcastBytes_u64": 0,
    "Ex.Recv.UnicastBytes_u64": 7500000000,
    "Ex.Send.BroadcastBytes_u64": 0,
    "Ex.Send.UnicastBytes_u64": 5500000000,
    online: true,
  },
  {
    HubName_str: "DEFAULT",
    Name_str: "charlie",
    Realname_utf: "Charlie Brown",
    GroupName_str: "",
    Note_utf: "External Auditor",
    AuthType_u32: 1,
    CreatedTime_dt: "2026-09-01T09:00:00.000Z",
    UpdatedTime_dt: "2026-09-01T09:00:00.000Z",
    LastLoginTime_dt: "2026-11-16T18:20:00.000Z",
    ExpireTime_dt: 0,
    NumLogin_u32: 8,
    Online_bool: false,
    "Recv.BroadcastBytes_u64": 0,
    "Recv.UnicastBytes_u64": 15400000000,
    "Send.BroadcastBytes_u64": 0,
    "Send.UnicastBytes_u64": 12000000000,
    "Ex.Recv.BroadcastBytes_u64": 0,
    "Ex.Recv.UnicastBytes_u64": 15400000000,
    "Ex.Send.BroadcastBytes_u64": 0,
    "Ex.Send.UnicastBytes_u64": 12000000000,
    online: false,
  },
  {
    HubName_str: "OFFICE",
    Name_str: "david",
    Realname_utf: "David Miller",
    GroupName_str: "ops",
    Note_utf: "Operations",
    AuthType_u32: 4,
    CreatedTime_dt: "2026-09-10T11:00:00.000Z",
    UpdatedTime_dt: "2026-09-10T11:00:00.000Z",
    LastLoginTime_dt: "2026-11-17T08:30:00.000Z",
    ExpireTime_dt: 0,
    NumLogin_u32: 65,
    Online_bool: true,
    "Recv.BroadcastBytes_u64": 0,
    "Recv.UnicastBytes_u64": 28000000000,
    "Send.BroadcastBytes_u64": 0,
    "Send.UnicastBytes_u64": 20500000000,
    "Ex.Recv.BroadcastBytes_u64": 0,
    "Ex.Recv.UnicastBytes_u64": 28000000000,
    "Ex.Send.BroadcastBytes_u64": 0,
    "Ex.Send.UnicastBytes_u64": 20500000000,
    online: true,
  },
  {
    HubName_str: "OFFICE",
    Name_str: "eva",
    Realname_utf: "Eva Green",
    GroupName_str: "design",
    Note_utf: "Design Lead",
    AuthType_u32: 1,
    CreatedTime_dt: "2026-10-05T16:00:00.000Z",
    UpdatedTime_dt: "2026-10-05T16:00:00.000Z",
    LastLoginTime_dt: "2026-11-15T14:10:00.000Z",
    ExpireTime_dt: 0,
    NumLogin_u32: 12,
    Online_bool: false,
    "Recv.BroadcastBytes_u64": 0,
    "Recv.UnicastBytes_u64": 5200000000,
    "Send.BroadcastBytes_u64": 0,
    "Send.UnicastBytes_u64": 4300000000,
    "Ex.Recv.BroadcastBytes_u64": 0,
    "Ex.Recv.UnicastBytes_u64": 5200000000,
    "Ex.Send.BroadcastBytes_u64": 0,
    "Ex.Send.UnicastBytes_u64": 4300000000,
    online: false,
  },
];

const mockQuotas = [
  {
    subject: "user",
    hub: "DEFAULT",
    username: "alice",
    has_limit: true,
    limit: 50,
    unit: "GB",
    limit_bytes: 53687091200,
    metric: "total",
    enabled: true,
    upload_bytes: 11300000000,
    download_bytes: 15000000000,
    used_bytes: 26300000000,
    remaining_bytes: 27387091200,
    percent: 49.0,
    base_send_bytes: 0,
    base_recv_bytes: 0,
    exceeded: false,
    blocked: false,
    cycle_start: "2026-11-01T00:00:00.000Z",
    updated_date: "2026-11-17T00:00:00.000Z",
  },
  {
    subject: "user",
    hub: "DEFAULT",
    username: "bob",
    has_limit: true,
    limit: 100,
    unit: "GB",
    limit_bytes: 107374182400,
    metric: "total",
    enabled: true,
    upload_bytes: 5500000000,
    download_bytes: 7500000000,
    used_bytes: 13000000000,
    remaining_bytes: 94374182400,
    percent: 12.1,
    base_send_bytes: 0,
    base_recv_bytes: 0,
    exceeded: false,
    blocked: false,
    cycle_start: "2026-11-01T00:00:00.000Z",
    updated_date: "2026-11-17T00:00:00.000Z",
  },
  {
    subject: "user",
    hub: "OFFICE",
    username: "david",
    has_limit: true,
    limit: 200,
    unit: "GB",
    limit_bytes: 214748364800,
    metric: "total",
    enabled: true,
    upload_bytes: 20500000000,
    download_bytes: 28000000000,
    used_bytes: 48500000000,
    remaining_bytes: 166248364800,
    percent: 22.6,
    base_send_bytes: 0,
    base_recv_bytes: 0,
    exceeded: false,
    blocked: false,
    cycle_start: "2026-11-01T00:00:00.000Z",
    updated_date: "2026-11-17T00:00:00.000Z",
  },
];

const mockSessions = [
  {
    Name_str: "SID-alice-1",
    Username_str: "alice",
    ClientIP_ip: "123.182.152.119",
    VirtualIP_ip: "192.168.30.15",
    Hostname_str: "alice-laptop.local",
    Client_str: "SoftEther VPN Client (Ver 4.44, Build 9807)",
    Cipher_str: "AES256-GCM (TLSv1.3)",
    CurrentNumTcp_u32: 8,
    MaxNumTcp_u32: 32,
    PacketNum_u64: 12543000,
    PacketSize_u64: 26300000000,
    CreatedTime_dt: "2026-11-17T08:15:00.000Z",
    CurrentTime_dt: "2026-11-17T10:30:00.000Z",
    TotalRecv_u64: 15000000000,
    TotalSend_u64: 11300000000,
    VLanMacAddress_bin: "5e:2a:18:4b:9c:01",
    IsLink_bool: false,
    IsSecureNAT_bool: false,
    IsBridge_bool: false,
  },
  {
    Name_str: "SID-bob-1",
    Username_str: "bob",
    ClientIP_ip: "203.0.113.88",
    VirtualIP_ip: "192.168.30.22",
    Hostname_str: "bob-workstation.local",
    Client_str: "OpenVPN Client (v2.6)",
    Cipher_str: "AES-128-CBC (TLSv1.2)",
    CurrentNumTcp_u32: 2,
    MaxNumTcp_u32: 8,
    PacketNum_u64: 5890000,
    PacketSize_u64: 13000000000,
    CreatedTime_dt: "2026-11-17T09:45:00.000Z",
    CurrentTime_dt: "2026-11-17T10:30:00.000Z",
    TotalRecv_u64: 7500000000,
    TotalSend_u64: 5500000000,
    VLanMacAddress_bin: "5e:2a:18:4b:9c:02",
    IsLink_bool: false,
    IsSecureNAT_bool: false,
    IsBridge_bool: false,
  },
];

// ── HTTP Mock Server ──────────────────────────────────────────────────────────
function createMockServer() {
  const mimeTypes = {
    ".html": "text/html; charset=utf-8",
    ".js": "application/javascript; charset=utf-8",
    ".css": "text/css; charset=utf-8",
    ".json": "application/json; charset=utf-8",
    ".png": "image/png",
    ".woff2": "font/woff2",
    ".txt": "text/plain; charset=utf-8",
  };

  const server = http.createServer((req, res) => {
    const url = new URL(req.url, `http://${req.headers.host}`);
    const pathname = decodeURIComponent(url.pathname);

    // API Router
    if (pathname.startsWith("/api/v1/")) {
      res.setHeader("Content-Type", "application/json; charset=utf-8");
      const apiPath = pathname.replace(/^\/api\/v1/, "");

      if (apiPath === "/auth/me") return res.end(JSON.stringify({ username: "admin" }));
      if (apiPath === "/auth/state") return res.end(JSON.stringify({ setup_required: false }));
      if (apiPath === "/system/info") {
        return res.end(JSON.stringify({
          version,
          commit: "66d0c5b",
          arch: "linux-amd64",
          go: "go1.27.3",
          os: "linux",
          repo: "siryle/Softether-Manager",
        }));
      }
      if (apiPath === "/system/domain") {
        return res.end(JSON.stringify({
          configured: false,
          domain: "",
          acme_challenge_type: "dns-01",
          https_port: 443,
          phase: "idle",
          busy: false,
          dns: {},
          http: { listening: false, error: "" },
          https: { listening: false, error: "" },
          certificate: { present: false },
          renewal: { due: false, last_error: "", last_error_type: "", renew_before_days: 30 },
          events: [],
        }));
      }
      if (apiPath === "/system/resources") {
        return res.end(JSON.stringify({
          available: true,
          cpu: {
            cores: [{ usage_percent: 14.2 }, { usage_percent: 18.5 }, { usage_percent: 11.0 }, { usage_percent: 16.8 }],
            overall: { usage_percent: 15.1, steal_percent: 0.0 },
          },
          load: { one: 0.32, five: 0.28, fifteen: 0.25 },
          memory: { total_bytes: 8589934592, used_bytes: 2748779069, available_bytes: 5841155523, usage_percent: 32.0 },
          swap: { total_bytes: 4294967296, used_bytes: 268435456, available_bytes: 4026531840, usage_percent: 6.25 },
          disks: [
            { mount_point: "/", device: "/dev/vda1", fs_type: "ext4", total_bytes: 107374182400, used_bytes: 25769803776, available_bytes: 81604378624, used_percent: 24.0 },
          ],
          network: {
            rx_bytes_per_second: 1258291,
            tx_bytes_per_second: 3984588,
            interfaces: [{ name: "eth0", rx_bytes: 68719476736, tx_bytes: 180388626432, rx_bytes_per_second: 1258291, tx_bytes_per_second: 3984588 }],
          },
        }));
      }

      if (apiPath === "/servers") return res.end(JSON.stringify(mockServers));
      if (apiPath.match(/^\/servers\/\d+$/)) {
        const id = Number(apiPath.split("/")[2]);
        return res.end(JSON.stringify(mockServers.find(s => s.id === id) || mockServers[0]));
      }
      if (apiPath === "/server/connection") {
        return res.end(JSON.stringify({ server_id: 1, name: "vpnserver", host: "127.0.0.1", port: 5555, configured: true }));
      }
      if (apiPath === "/server/probe" || apiPath.match(/^\/servers\/\d+\/probe$/)) {
        const id = apiPath === "/server/probe" ? 1 : Number(apiPath.split("/")[2]);
        const server = mockServers.find((entry) => entry.id === id);
        return res.end(JSON.stringify({ online: true, configured: true, version: "4.44 Build 9807", hostname: server?.name, sessions: id === 1 ? 3 : 2, hubs: id === 1 ? 3 : 1 }));
      }
      if (apiPath === "/server/overview") {
        return res.end(JSON.stringify({
          status: {
            ok: true,
            data: {
              ServerType_u32: 0,
              StartTime_dt: "2026-11-03T10:00:00.000Z",
              CurrentTime_dt: "2026-11-17T10:00:00.000Z",
              NumUsers_u32: 5,
              NumSessionsTotal_u32: 3,
              "Recv.UnicastBytes_u64": 64424509440,
              "Recv.BroadcastBytes_u64": 4294967296,
              "Send.UnicastBytes_u64": 171798691840,
              "Send.BroadcastBytes_u64": 8589934592,
            },
          },
          hubs: {
            ok: true,
            data: { HubList: mockHubs },
          },
          listeners: {
            ok: true,
            data: {
              ListenerList: [
                { Ports_u32: 443, Port_u32: 443, Enables_bool: true, Active_bool: true },
                { Ports_u32: 992, Port_u32: 992, Enables_bool: true, Active_bool: true },
                { Ports_u32: 1194, Port_u32: 1194, Enables_bool: true, Active_bool: true },
                { Ports_u32: 5555, Port_u32: 5555, Enables_bool: true, Active_bool: true },
              ],
            },
          },
          ipsec: {
            ok: true,
            data: { L2TP_Raw_bool: true, L2TP_IPsec_bool: true, IPsec_Secret_str: "vpn", L2TP_DefaultHub_str: "DEFAULT" },
          },
          openvpn: {
            ok: true,
            data: { EnableOpenVPN_bool: true, OpenVPNPortList_str: "1194", EnableSSTP_bool: true },
          },
          azure: {
            ok: true,
            data: { IsEnabled_bool: false },
          },
          ddns: {
            ok: true,
            data: { HostName_str: "vpnserver.softether.net", CurrentHostName_str: "vpnserver", CurrentIPv4_str: "203.0.113.10", IsEnabled_bool: true },
          },
        }));
      }

      if (apiPath.startsWith("/server/traffic")) {
        return res.end(JSON.stringify(generateTrafficPoints(24, 16535879680, 9438965760)));
      }

      if (apiPath === "/hubs") {
        return res.end(JSON.stringify({ HubList: mockHubs }));
      }
      if (apiPath === "/users") {
        return res.end(JSON.stringify({ hubs: ["DEFAULT", "OFFICE"], errors: [], users: mockUsers }));
      }
      if (apiPath === "/clients") {
        return res.end(JSON.stringify({
          hubs: ["DEFAULT", "OFFICE"],
          errors: [],
          clients: mockSessions.map((s) => ({ ...s, HubName_str: "DEFAULT" })),
        }));
      }
      if (apiPath === "/quotas") {
        return res.end(JSON.stringify(mockQuotas));
      }

      // Hub-specific endpoints
      if (apiPath.startsWith("/hubs/DEFAULT/status")) {
        return res.end(JSON.stringify(mockHubs[0]));
      }
      if (apiPath.startsWith("/hubs/DEFAULT/traffic")) {
        return res.end(JSON.stringify(generateTrafficPoints(24, 12500000000, 7800000000)));
      }
      if (apiPath === "/hubs/DEFAULT") {
        return res.end(JSON.stringify({ HubName_str: "DEFAULT", MaxSession_u32: 128, NoEnum_bool: false, Online_bool: true, HubType_u32: 0 }));
      }
      if (apiPath === "/hubs/DEFAULT/users") {
        return res.end(JSON.stringify({ UserList: mockUsers.filter(u => u.HubName_str === "DEFAULT") }));
      }
      if (apiPath === "/hubs/DEFAULT/sessions") {
        return res.end(JSON.stringify({ SessionList: mockSessions }));
      }
      if (apiPath === "/hubs/DEFAULT/users-online") {
        return res.end(JSON.stringify({ usernames: ["alice", "bob"] }));
      }
      if (apiPath === "/hubs/DEFAULT/groups") {
        return res.end(JSON.stringify({ GroupList: [{ Name_str: "finance", Realname_utf: "Finance Group", Note_utf: "Financial staff", NumUsers_u32: 1 }] }));
      }
      if (apiPath === "/hubs/DEFAULT/radius") {
        return res.end(JSON.stringify({ RadiusServerName_str: "", RadiusPort_u32: 1812, RadiusSecret_str: "", RadiusRetryInterval_u32: 5 }));
      }
      if (apiPath === "/hubs/DEFAULT/log-settings") {
        return res.end(JSON.stringify({ SaveSecurityLog_bool: true, SecurityLogSwitchType_u32: 2, SavePacketLog_bool: true, PacketLogConfigList: [] }));
      }
      if (apiPath === "/hubs/DEFAULT/ca") return res.end(JSON.stringify({ CAList: [] }));
      if (apiPath === "/hubs/DEFAULT/crl") return res.end(JSON.stringify({ CRLList: [] }));
      if (apiPath === "/hubs/DEFAULT/access") return res.end(JSON.stringify({ AccessList: [], version: "1" }));
      if (apiPath === "/hubs/DEFAULT/ac-list") return res.end(JSON.stringify({ ACList: [], version: "1" }));
      if (apiPath === "/hubs/DEFAULT/securenat") {
        return res.end(JSON.stringify({ IsEnabled_bool: true, NatOptions: { MacAddress_bin: "5e:2a:18:00:00:01", Ip_ip: "192.168.30.1", Mask_ip: "255.255.255.0" } }));
      }
      if (apiPath === "/hubs/DEFAULT/securenat/nat-table") return res.end(JSON.stringify({ NatTable: [] }));
      if (apiPath === "/hubs/DEFAULT/securenat/dhcp-table") return res.end(JSON.stringify({ DhcpTable: [] }));
      if (apiPath === "/hubs/DEFAULT/links") return res.end(JSON.stringify({ LinkList: [] }));
      if (apiPath === "/hubs/DEFAULT/mac-table") {
        return res.end(JSON.stringify({
          MacTable: [
            { Key_u32: 1, MacAddress_bin: "5e:2a:18:4b:9c:01", SessionName_str: "SID-alice-1", UpdatedTime_dt: "2026-11-17T10:00:00Z" },
            { Key_u32: 2, MacAddress_bin: "5e:2a:18:4b:9c:02", SessionName_str: "SID-bob-1", UpdatedTime_dt: "2026-11-17T10:00:00Z" },
          ],
        }));
      }
      if (apiPath === "/hubs/DEFAULT/ip-table") {
        return res.end(JSON.stringify({
          IpTable: [
            { Key_u32: 1, Ip_ip: "192.168.30.10", SessionName_str: "SID-alice-1", UpdatedTime_dt: "2026-11-17T10:00:00Z" },
            { Key_u32: 2, Ip_ip: "192.168.30.11", SessionName_str: "SID-bob-1", UpdatedTime_dt: "2026-11-17T10:00:00Z" },
          ],
        }));
      }
      if (apiPath === "/hubs/DEFAULT/admin-options") return res.end(JSON.stringify({ AdminOptionList: [] }));
      if (apiPath === "/hubs/DEFAULT/ext-options") return res.end(JSON.stringify({ ExtOptionList: [] }));
      if (apiPath === "/quotas/hub/DEFAULT") {
        return res.end(JSON.stringify({
          subject: "hub",
          hub: "DEFAULT",
          username: "",
          has_limit: false,
          limit: 0,
          unit: "GB",
          limit_bytes: 0,
          metric: "total",
          enabled: false,
          upload_bytes: 25769803776,
          download_bytes: 34359738368,
          used_bytes: 60129542144,
          remaining_bytes: null,
          percent: 0,
          base_send_bytes: 0,
          base_recv_bytes: 0,
          exceeded: false,
          blocked: false,
          cycle_start: "2026-11-01T00:00:00.000Z",
          updated_date: "2026-11-17T00:00:00.000Z",
        }));
      }

      // User details
      if (apiPath === "/hubs/DEFAULT/users/alice") {
        return res.end(JSON.stringify({
          HubName_str: "DEFAULT",
          Name_str: "alice",
          GroupName_str: "finance",
          Realname_utf: "Alice Smith",
          Note_utf: "Finance Department",
          AuthType_u32: 1,
          CreatedTime_dt: "2026-08-10T12:00:00.000Z",
          UpdatedTime_dt: "2026-08-10T12:00:00.000Z",
          ExpireTime_dt: 0,
          NumLogin_u32: 42,
          "Recv.BroadcastBytes_u64": 0,
          "Recv.UnicastBytes_u64": 15000000000,
          "Send.BroadcastBytes_u64": 0,
          "Send.UnicastBytes_u64": 11300000000,
          Recv: { BroadcastBytes_u64: 0, UnicastBytes_u64: 15000000000, BroadcastCount_u64: 0, UnicastCount_u64: 11500000 },
          Send: { BroadcastBytes_u64: 0, UnicastBytes_u64: 11300000000, BroadcastCount_u64: 0, UnicastCount_u64: 8900000 },
          policy: {
            MaxConnection_u32: 32,
            TimeOut_u32: 20,
            MaxMac_u32: 1,
            MaxIP_u32: 1,
            MaxUpload_u32: 0,
            MaxDownload_u32: 0,
            MultiLogins_u32: 1,
            NoQoS_bool: false,
            RSandRAFilter_bool: true,
            RAFilter_bool: true,
            DHCPFilter_bool: true,
            DHCPNoServer_bool: true,
            NoSavePassword_bool: false,
            AutoDisconnect_u32: 0,
            FilterIPv4_bool: false,
            FilterIPv6_bool: false,
            CheckMac_bool: false,
            CheckIP_bool: false,
          },
        }));
      }
      if (apiPath === "/hubs/DEFAULT/users/alice/sessions") {
        return res.end(JSON.stringify({ SessionList: [mockSessions[0]] }));
      }
      if (apiPath === "/hubs/DEFAULT/users/alice/credential-state") {
        return res.end(JSON.stringify({ available: true, source: "saved" }));
      }
      if (apiPath === "/quotas/user/DEFAULT/alice") {
        return res.end(JSON.stringify(mockQuotas[0]));
      }
      if (apiPath.startsWith("/hubs/DEFAULT/users/alice/usage")) {
        return res.end(JSON.stringify(generateTrafficPoints(24, 7500000000, 4200000000)));
      }
      if (apiPath.startsWith("/hubs/DEFAULT/users/alice/session-history")) {
        return res.end(JSON.stringify([
          {
            id: 101,
            client_ip: "192.168.1.50",
            client_hostname: "alice-laptop.local",
            started_date: "2026-11-17T08:15:00.000Z",
            ended_date: null,
            download_bytes: 15000000000,
            upload_bytes: 11300000000,
          },
          {
            id: 98,
            client_ip: "192.168.1.50",
            client_hostname: "alice-laptop.local",
            started_date: "2026-11-16T09:00:00.000Z",
            ended_date: "2026-11-16T18:30:00.000Z",
            download_bytes: 4200000000,
            upload_bytes: 2800000000,
          },
        ]));
      }

      // Server settings cards
      if (apiPath === "/server/listeners") {
        return res.end(JSON.stringify({
          ListenerList: [
            { Ports_u32: 443, Enables_bool: true, Active_bool: true },
            { Ports_u32: 992, Enables_bool: true, Active_bool: true },
            { Ports_u32: 1194, Enables_bool: true, Active_bool: true },
            { Ports_u32: 5555, Enables_bool: true, Active_bool: true },
          ],
        }));
      }
      if (apiPath === "/server/special-listener") return res.end(JSON.stringify({ VpnOverIcmpListener_bool: true, VpnOverDnsListener_bool: true }));
      if (apiPath === "/server/ipsec") {
        return res.end(JSON.stringify({ L2TP_Raw_bool: true, L2TP_IPsec_bool: true, IPsec_Secret_str: "vpn", L2TP_DefaultHub_str: "DEFAULT", EtherIP_IPsec_bool: false }));
      }
      if (apiPath === "/server/openvpn") return res.end(JSON.stringify({ EnableOpenVPN_bool: true, OpenVPNPortList_str: "1194", OpenVPNDefaultHub_str: "DEFAULT" }));
      if (apiPath === "/server/wireguard") {
        return res.end(JSON.stringify({
          supported: true,
          enabled: true,
          default_udp_port: 51820,
          wireguard_port: 51820,
          wireguard_port_listening: true,
          default_endpoint_host: "vpn.example.com",
          peers: [
            {
              hub: "DEFAULT",
              user: "alice",
              label: "MacBook Pro",
              assigned_ip: "10.250.0.2",
              public_key: "mK8x9P2vR4wT7yU1zA3bC5dE6fG8hJ0kL2mN4pQ6rS4=",
            },
          ],
        }));
      }
      if (apiPath === "/server/azure") return res.end(JSON.stringify({ IsEnabled_bool: false, Status_u32: 0 }));
      if (apiPath === "/server/ddns") {
        return res.end(JSON.stringify({ HostName_str: "vpnserver.softether.net", CurrentIPv4_str: "203.0.113.10", CurrentIPv6_str: "", IsEnabled_bool: true, DdnsKey_str: "ABCDEF123456" }));
      }
      if (apiPath === "/server/cipher") return res.end(JSON.stringify({ String_str: "AES256-GCM" }));
      if (apiPath === "/server/cert") {
        return res.end(JSON.stringify({ Cert_str: "CERTDATA...", Issuer_str: "CN=vpnserver", Subject_str: "CN=vpnserver", Expires_dt: "2036-01-01T00:00:00Z" }));
      }
      if (apiPath === "/server/bridges") return res.end(JSON.stringify({ LocalBridgeList: [{ Device_str: "eth0", HubName_str: "DEFAULT", Active_bool: true, Online_bool: true }] }));
      if (apiPath === "/server/bridges/support") return res.end(JSON.stringify({ IsSupported_bool: true }));
      if (apiPath === "/server/bridges/devices") return res.end(JSON.stringify({ DeviceList: [{ DeviceName_str: "eth0" }, { DeviceName_str: "eth1" }] }));
      if (apiPath === "/server/l3") return res.end(JSON.stringify({ L3SwList: [] }));
      if (apiPath === "/server/etherip") return res.end(JSON.stringify({ EtherIPIdList: [] }));
      if (apiPath === "/server/farm") return res.end(JSON.stringify({ ServerType_u32: 0, Weight_u32: 100 }));
      if (apiPath === "/server/farm/status") return res.end(JSON.stringify({ NumFarmMembers_u32: 0 }));
      if (apiPath === "/server/farm/members") return res.end(JSON.stringify({ FarmMemberList: [] }));
      if (apiPath === "/server/keepalive") return res.end(JSON.stringify({ UseKeepAliveLib_bool: true, KeepAliveHost_str: "keepalive.softether.org", KeepAlivePort_u32: 80, KeepAliveInterval_u32: 50 }));
      if (apiPath === "/server/syslog") return res.end(JSON.stringify({ SaveType_u32: 0, HostName_str: "", Port_u32: 514 }));
      if (apiPath === "/server/config") return res.end(JSON.stringify({ filename: "vpn_server.config", config_base64: "ZGVtbyBjb25maWc=" }));
      if (apiPath === "/server/backups") {
        return res.end(JSON.stringify({
          backups: [
            {
              filename: "vpn_server_auto_20261117_030000.config",
              size: 154820,
              created_at: "2026-11-17T03:00:00.000Z",
              is_auto: true,
            },
            {
              filename: "vpn_server_manual_20261116_183000.config",
              size: 153200,
              created_at: "2026-11-16T18:30:00.000Z",
              is_auto: false,
            },
            {
              filename: "vpn_server_auto_20261116_030000.config",
              size: 152900,
              created_at: "2026-11-16T03:00:00.000Z",
              is_auto: true,
            },
          ],
        }));
      }
      if (apiPath === "/server/backup-settings") {
        return res.end(JSON.stringify({
          enabled: true,
          interval_hours: 24,
          backup_time: "03:00",
          backup_dir: "/data/backups",
          configured_dir: "",
          retention_count: 15,
          last_run: "2026-11-17T03:00:00.000Z",
        }));
      }

      // Logs
      if (apiPath === "/server/logs") {
        return res.end(JSON.stringify({
          LogFiles: [
            { FilePath_str: "server_log/vpn_20261117.log", FileSize_u32: 1048576, UpdatedTime_dt: "2026-11-17T10:00:00.000Z" },
            { FilePath_str: "server_log/vpn_20261116.log", FileSize_u32: 2097152, UpdatedTime_dt: "2026-11-16T23:59:59.000Z" },
            { FilePath_str: "security_log/DEFAULT/sec_20261117.log", FileSize_u32: 524288, UpdatedTime_dt: "2026-11-17T09:45:00.000Z" },
            { FilePath_str: "packet_log/DEFAULT/pkt_20261117.log", FileSize_u32: 8388608, UpdatedTime_dt: "2026-11-17T10:15:00.000Z" },
          ],
        }));
      }
      if (apiPath.startsWith("/server/logs/read")) {
        return res.end(JSON.stringify({
          path: "server_log/vpn_20261117.log",
          offset: 0,
          bytes: 2048,
          text: `2026-11-17 08:00:01.120 [SERVER] SoftEther VPN Server 4.44 Build 9807 service started.
2026-11-17 08:00:01.145 [SERVER] TCP Listener started on port 443, 992, 1194, 5555.
2026-11-17 08:00:02.010 [HUB:DEFAULT] Virtual Hub "DEFAULT" is started and online.
2026-11-17 08:00:02.050 [HUB:OFFICE] Virtual Hub "OFFICE" is started and online.
2026-11-17 08:15:22.450 [HUB:DEFAULT] Connection "CID-101" (Client IP: 192.168.1.50): User "alice" logged in.
2026-11-17 08:15:22.480 [HUB:DEFAULT] Session "SID-alice-1" established with cipher AES256-GCM.
2026-11-17 09:45:10.820 [HUB:DEFAULT] Connection "CID-102" (Client IP: 192.168.1.75): User "bob" logged in via OpenVPN.
2026-11-17 09:45:10.850 [HUB:DEFAULT] Session "SID-bob-1" established with cipher AES-128-CBC.
2026-11-17 10:00:00.000 [SERVER] Periodic housekeeping and sampler stats synced successfully.`,
        }));
      }

      // Default fallback for any other api
      return res.end(JSON.stringify({}));
    }

    // Static Files Router
    let filePath = path.join(OUT_DIR, pathname === "/" ? "index.html" : pathname);
    if (!fs.existsSync(filePath) || fs.statSync(filePath).isDirectory()) {
      filePath = path.join(OUT_DIR, "index.html");
    }

    const ext = path.extname(filePath);
    const contentType = mimeTypes[ext] || "application/octet-stream";
    res.setHeader("Content-Type", contentType);
    fs.createReadStream(filePath).pipe(res);
  });

  return new Promise((resolve, reject) => {
    server.once("error", reject);
    server.listen(0, "127.0.0.1", () => resolve(server));
  });
}

// ── Screenshot Runner ──────────────────────────────────────────────────────────
async function run() {
  const require = createRequire(path.join(ROOT, "app", "web", "package.json"));
  const puppeteer = require("puppeteer");
  if (!fs.existsSync(path.join(OUT_DIR, "index.html"))) {
    throw new Error(`Build the frontend first: ${OUT_DIR}/index.html is missing`);
  }
  const server = await createMockServer();
  const baseURL = `http://127.0.0.1:${server.address().port}/`;
  let browser;

  const targets = [
    { name: "dashboard", hash: "#/", isMobile: false, theme: "dark" },
    { name: "users", hash: "#/users", isMobile: false, theme: "dark" },
    { name: "user", hash: "#/hub/DEFAULT/user/alice", isMobile: false, theme: "dark" },
    { name: "hubs", hash: "#/hubs", isMobile: false, theme: "dark" },
    { name: "servers", hash: "#/servers", isMobile: false, theme: "dark" },
    { name: "hub-overview", hash: "#/hub/DEFAULT", isMobile: false, theme: "dark" },
    { name: "hub-overview-light", hash: "#/hub/DEFAULT", isMobile: false, theme: "light" },
    { name: "hub-sessions", hash: "#/hub/DEFAULT/sessions", isMobile: false, theme: "dark" },
    { name: "logs", hash: "#/logs", isMobile: false, theme: "dark" },
    { name: "server-settings", hash: "#/server-settings", isMobile: false, theme: "dark" },
    { name: "mobile-dashboard", hash: "#/", isMobile: true, theme: "dark" },
    { name: "mobile-users", hash: "#/users", isMobile: true, theme: "dark" },
    { name: "mobile-user", hash: "#/hub/DEFAULT/user/alice", isMobile: true, theme: "dark" },
    { name: "mobile-hub", hash: "#/hub/DEFAULT", isMobile: true, theme: "dark" },
  ];

  const languages = ["zh-CN", "en"];

  try {
    browser = await puppeteer.launch({
      executablePath: process.env.CHROME_PATH || undefined,
      args: ["--force-color-profile=srgb", "--hide-scrollbars"],
    });
    console.log(`Mock server running at ${baseURL}`);
    for (const lang of languages) {
      console.log(`\n=== Capturing screenshots for language: ${lang} ===`);
      for (const target of targets) {
        const page = await browser.newPage();
        const pageErrors = [];
        page.on("pageerror", (error) => pageErrors.push(error));
        await page.evaluateOnNewDocument((origin, language, theme) => {
          if (location.origin !== origin) return;
          localStorage.setItem("sem_lang", language);
          localStorage.setItem("sem_theme", theme);
          sessionStorage.setItem("sem_server_id", "1");
        }, new URL(baseURL).origin, lang, target.theme);
        if (target.isMobile) {
          await page.setViewport({ width: 390, height: 844, deviceScaleFactor: 2, isMobile: true, hasTouch: true });
        } else {
          await page.setViewport({ width: 1440, height: 1000, deviceScaleFactor: 1 });
        }

        await page.goto(`${baseURL}${target.hash}`, { waitUntil: "networkidle0" });
        if (pageErrors.length) throw new AggregateError(pageErrors, `${lang}/${target.name}`);
        await page.waitForSelector(`html[lang="${lang}"][data-theme="${target.theme}"] main h1`, { visible: true });
        if (target.name === "logs") {
          const log = await page.waitForSelector(".lograil__item", { visible: true });
          await log.click();
          await page.waitForFunction(() => document.querySelector(".logbench__view")?.textContent?.includes("Periodic housekeeping"));
        }
        await page.evaluate(() => document.fonts.ready);
        if (pageErrors.length) throw new AggregateError(pageErrors, `${lang}/${target.name}`);

        const outPath = path.join(SCREENSHOTS_DIR, lang, `${target.name}.png`);
        fs.mkdirSync(path.dirname(outPath), { recursive: true });

        await page.screenshot({ path: outPath });
        console.log(`Saved: docs/screenshots/${lang}/${target.name}.png (${target.isMobile ? "390x844" : "1440x1000"})`);
        await page.close();
      }
    }
  } finally {
    await browser?.close();
    server.close();
  }
}

export { createMockServer };
// Importing this file for its mock server must not also start a capture run.
if (!process.env.SEM_SCREENSHOTS_LIBRARY) await run();
