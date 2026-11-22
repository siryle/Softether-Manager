import assert from "node:assert/strict";
import { readFileSync } from "node:fs";
import { createRequire } from "node:module";
import { test } from "node:test";
import vm from "node:vm";
import ts from "typescript";

const require = createRequire(import.meta.url);
const deferred = () => {
  let resolve;
  const promise = new Promise(r => { resolve = r; });
  return { promise, resolve };
};

function load(file, globals = {}, modules = {}, extra = "") {
  const source = readFileSync(new URL(file, import.meta.url), "utf8") + extra;
  const js = ts.transpileModule(source, { compilerOptions: { target: ts.ScriptTarget.ES2022, module: ts.ModuleKind.CommonJS, jsx: ts.JsxEmit.ReactJSX } }).outputText;
  const exports = {};
  vm.runInNewContext(js, {
    exports, process: { env: {} }, DOMException,
    require: name => modules[name] ?? require(name),
    ...globals,
  }, { filename: file });
  return exports;
}

function backend(text = async () => "{}") {
  let serverId = "1";
  const requests = [];
  const api = load("../src/lib/api.ts", {
    URLSearchParams,
    window: { dispatchEvent() {} },
    sessionStorage: { getItem: () => serverId },
    localStorage: { getItem: () => null, removeItem() {} },
    fetch: async (url, options) => {
      requests.push({ url, ...options });
      return { status: 200, ok: true, text, blob: text };
    },
  });
  return { ...api, requests, switchServer: () => { serverId = "2"; } };
}

function component(file, exports, states, modules) {
  let index = 0;
  let refIndex = 0;
  let memoIndex = 0;
  const refs = [];
  const memos = [];
  const placeholder = () => null;
  const stubs = new Proxy({}, { get: () => placeholder });
  const loaded = load(file, {}, {
    react: {
      useState: initial => {
        const slot = index++;
        if (!(slot in states)) states[slot] = typeof initial === "function" ? initial() : initial;
        return [states[slot], value => { states[slot] = typeof value === "function" ? value(states[slot]) : value; }];
      },
      useEffect() {}, useCallback: fn => fn,
      useMemo: (fn, deps) => {
        const slot = memoIndex++;
        const previous = memos[slot];
        if (!previous || !deps || previous.deps.length !== deps.length || deps.some((value, i) => !Object.is(value, previous.deps[i]))) {
          memos[slot] = { deps: deps ?? [], value: fn() };
        }
        return memos[slot].value;
      },
      useRef: value => refs[refIndex++] ??= { current: value },
    },
    "./bits": stubs,
    "../components/bits": stubs,
    "../components/WireGuardPeerSheet": stubs,
    "../../components/bits": stubs,
    "../ui/Sheet": stubs, "../../ui/Sheet": stubs,
    "../ui/Icon": stubs, "../../ui/Icon": stubs,
    "../ui/Status": stubs, "../ui/Switch": stubs,
    "../lib/router": stubs,
    "../lib/i18n": { t: value => value, useI18n: () => ({ t: value => value }) }, "../../lib/i18n": { t: value => value, useI18n: () => ({ t: value => value }) },
    "../lib/toast": { useToast: () => ({ guard: fn => fn(), push() {} }) },
    "../../lib/toast": { useToast: () => ({ guard: fn => fn() }) },
    ...modules,
  }, `\nexport { ${exports} };`);
  const render = loaded[exports];
  loaded[exports] = (...args) => {
    index = 0;
    refIndex = 0;
    memoIndex = 0;
    return render(...args);
  };
  return loaded;
}

function find(element, predicate) {
  if (!element || typeof element !== "object") return null;
  if (predicate(element)) return element;
  for (const child of [element.props?.children].flat(Infinity)) {
    const match = find(child, predicate);
    if (match) return match;
  }
  return null;
}

test("a server switch during response download rejects the old response", async () => {
  const body = deferred();
  const client = backend(() => body.promise);
  const pending = client.api.createUser("VPN", { Name_str: "Alice" });
  await new Promise(resolve => setImmediate(resolve));
  client.switchServer();
  body.resolve('{"ok":true}');
  await assert.rejects(pending, { name: "AbortError" });
});

test("backup downloads use the panel prefix and keep the server scope", async () => {
  const body = deferred();
  const client = backend(() => body.promise);
  const download = client.createServerApi(1).downloadBackup("vpn_server_test.config");
  await new Promise(resolve => setImmediate(resolve));
  assert.equal(client.requests[0].url, "api/v1/server/backups/vpn_server_test.config/download");
  assert.equal(client.requests[0].headers["X-Server-ID"], "1");
  client.switchServer();
  body.resolve("backup");
  await assert.rejects(download, { name: "AbortError" });
});

test("configuration restore cannot follow a switch while reading the local file", async () => {
  const file = deferred();
  const client = backend();
  const bound = client.createServerApi(1);
  const { ConfigCard } = component("../src/screens/ServerSettings.tsx", "ConfigCard", [{ name: "backup.config" }], {
    "../lib/api": { api: client.api, useServerApi: () => bound },
    "../lib/util": { fileToBase64: () => file.promise },
  });
  const confirmation = find(ConfigCard(), node => typeof node.props?.onConfirm === "function");
  assert.ok(confirmation);
  const pending = confirmation.props.onConfirm();
  client.switchServer();
  file.resolve("Y29uZmln");
  await assert.rejects(pending, { name: "AbortError" });
  assert.equal(client.requests.length, 0);
});

test("RADIUS address edits retain the existing shared secret", async () => {
  const client = backend();
  const config = { RadiusServerName_str: "changed.example", RadiusPort_u32: 1812, RadiusRetryInterval_u32: 500, RadiusSecret_str: "existing shared secret" };
  const { RadiusSection } = component("../src/screens/hub/HubSecurity.tsx", "RadiusSection", [config, "", true], {
    "../../lib/api": { api: client.api },
    "../../lib/util": {},
  });
  const save = find(RadiusSection({ hub: "VPN" }), node => node.type === "button" && node.props.children === "Save RADIUS settings");
  assert.ok(save);
  await save.props.onClick();
  assert.equal(JSON.parse(client.requests[0].body).RadiusSecret_str, "existing shared secret");
});

function userEditor(client, draft) {
  const quota = component("../src/components/QuotaCard.tsx", "saveUserQuotaDraft", [], {
    "../lib/api": { api: client.api }, "../lib/util": {},
  });
  const states = [];
  states[15] = draft;
  states[16] = true;
  const { UserSheet } = component("../src/components/UserSheet.tsx", "UserSheet", states, {
    "../lib/api": { ...client, useServerApi: () => client.createServerApi(1) },
    "../lib/se": load("../src/lib/se.ts"), "../lib/util": {}, "./QuotaCard": quota,
  });
  const props = { hub: "DEFAULT", existing: { Name_str: "admin", Realname_utf: "Admin", AuthType_u32: 1 }, onClose() {}, onSaved() {} };
  return () => UserSheet(props);
}

const resetDraft = { on: false, limit: "", unit: "GB", metric: "total", enforce: true, reset: true };

test("resetting transfer alone never rewrites the VPN user profile", async () => {
  const client = backend();
  const render = userEditor(client, { ...resetDraft });
  const sheet = render();
  const save = find(sheet.props.footer, node => node.type === "button" && node.props.className.includes("btn--primary"));
  await save.props.onClick();
  assert.deepEqual(client.requests.map(r => [r.method, r.url]), [["POST", "api/v1/quotas/user/DEFAULT/admin/reset"]]);
});

test("an actual profile edit still saves alongside a transfer reset", async () => {
  const client = backend();
  const render = userEditor(client, { ...resetDraft });
  const name = find(render(), node => node.type === "input" && node.props.value === "Admin");
  name.props.onChange({ target: { value: "Renamed admin" } });
  const save = find(render().props.footer, node => node.type === "button" && node.props.className.includes("btn--primary"));
  await save.props.onClick();
  assert.deepEqual(client.requests.map(r => r.method), ["PUT", "POST"]);
  assert.equal(JSON.parse(client.requests[0].body).Realname_utf, "Renamed admin");
});

test("a limit and transfer reset are sent as one operation", async () => {
  const client = backend();
  const render = userEditor(client, { ...resetDraft, on: true, limit: "1" });
  const save = find(render().props.footer, node => node.type === "button" && node.props.className.includes("btn--primary"));
  await save.props.onClick();
  assert.deepEqual(client.requests.map(r => [r.method, r.url]), [["PUT", "api/v1/quotas/user/DEFAULT/admin"]]);
  assert.equal(JSON.parse(client.requests[0].body).reset, true);
});

test("a removed limit still shows pending VPN access restoration", () => {
  const client = backend();
  const { QuotaSummary } = component("../src/components/QuotaCard.tsx", "QuotaSummary", [], {
    "../lib/api": { api: client.api }, "../lib/util": load("../src/lib/util.ts", {}, { "./i18n": { t: value => value } }),
  });
  const quota = { has_limit: false, enabled: false, blocked: true, limit_bytes: 0, used_bytes: 1000, upload_bytes: 600, download_bytes: 400, metric: "total" };
  const summary = QuotaSummary({ quota, subject: "user" });
  assert.ok(find(summary, node => node.props?.label === "access restoration pending"));
  assert.ok(find(summary, node => node.type === "div" && node.props.className === "alert alert--warn"));
});

test("SoftEther success codes are not colored as server errors", () => {
  const { classify } = load("../src/components/Terminal.tsx", {}, { "../lib/i18n": { t: value => value } }, "\nexport { classify };");
  for (const [line, level] of [
    ['Method: "EnumHub", Returned error code: 0 (0 = success), Returned error message: "No error."', "ok"],
    ['Returned error message: "No error."', "ok"],
    ['Method: "EnumSession", Returned error code: 13, Returned error message: "Timeout."', "err"],
    ['Returned error code: -32603', "err"],
    ['Cannot connect to SoftEther', "err"],
  ]) assert.equal(classify(line), level, line);
});

function chart(usage) {
  const { TrafficChart } = component("../src/components/TrafficChart.tsx", "TrafficChart", [], {
    "../lib/util": load("../src/lib/util.ts", {}, { "./i18n": { t: value => value } }),
  });
  return TrafficChart({ usage });
}

const chartEnd = Date.parse("2026-11-16T22:45:00Z");
const point = (minutes, send = 10, recv = 5) => ({ t: new Date(chartEnd + minutes * 60_000).toISOString(), send, recv });
const chartUsage = (hours, points) => ({ hours, start: new Date(chartEnd - hours * 3600_000).toISOString(), end: new Date(chartEnd).toISOString(), sample_interval_seconds: 300, points, total_send: 100, total_recv: 50, samples: points.length });

test("empty and missing history never generate a chart", () => {
  for (const points of [[], [point(-5, null, null)]]) {
    const tree = chart(chartUsage(720, points));
    assert.equal(find(tree, node => node.type === "svg"), null);
    assert.ok(find(tree, node => node.props?.className?.includes("chart--empty")));
  }
});

test("short recorded history is positioned inside the requested time window", () => {
  for (const hours of [6, 24, 168, 720]) {
    const tree = chart(chartUsage(hours, [point(-5), point(0)]));
    const path = find(tree, node => node.type === "path" && node.props.stroke === "var(--chart-send)");
    const firstX = Number(/^M([\d.]+)/.exec(path.props.d)[1]);
    assert.ok(firstX > 700, `${hours}h stretched five minutes across the whole chart: ${firstX}`);
  }
});

test("missing observations split both chart lines and area fills", () => {
  const tree = chart(chartUsage(24, [point(-20), point(-15), point(-10, null, null), point(-5), point(0)]));
  const line = find(tree, node => node.type === "path" && node.props.stroke === "var(--chart-send)");
  const area = find(tree, node => node.type === "path" && node.props.fill === "var(--chart-send)");
  assert.equal((line.props.d.match(/M/g) ?? []).length, 2);
  assert.equal((area.props.d.match(/Z/g) ?? []).length, 2);
});

test("weekly history labels include calendar dates", () => {
  const usage = chartUsage(168, [point(-5), point(0)]);
  const tree = chart(usage);
  const label = find(tree, node => node.type === "text" && node.props.y === 183);
  assert.equal(label.props.children, new Date(usage.start).toLocaleDateString(undefined, { month: "numeric", day: "numeric" }));
});

test("a stale history response cannot replace the newly selected range", async () => {
  let poll;
  const { useTrafficUsage } = component("../src/components/TrafficChart.tsx", "useTrafficUsage", [], {
    "./bits": { usePoll: fn => { poll = fn; } }, "../lib/util": {},
  });
  const old = deferred();
  const next = deferred();
  assert.equal(useTrafficUsage(() => old.promise, "list", [6]), null);
  const oldRequest = poll();
  assert.equal(useTrafficUsage(() => next.promise, "list", [24]), null);
  const nextRequest = poll();
  next.resolve(chartUsage(24, [point(-5), point(0)]));
  await nextRequest;
  assert.equal(useTrafficUsage(() => next.promise, "list", [24]).hours, 24);
  old.resolve(chartUsage(6, [point(-5), point(0)]));
  await oldRequest;
  assert.equal(useTrafficUsage(() => next.promise, "list", [24]).hours, 24);
  assert.equal(useTrafficUsage(() => Promise.resolve(null), "list", [720]), null);
});

test("IP location drops the placeholder ISP", () => {
  const { formatIPLocation } = load("../src/lib/util.ts", {}, { "./i18n": { t: value => value } });
  assert.equal(formatIPLocation("局域网/内网", "—"), "局域网/内网");
  assert.equal(formatIPLocation("局域网/内网", ""), "局域网/内网");
  assert.equal(formatIPLocation("局域网/内网", undefined), "局域网/内网");
  assert.equal(formatIPLocation("江苏省 苏州", "China Telecom"), "江苏省 苏州 China Telecom");
  assert.equal(formatIPLocation("", "—"), "");
  assert.equal(formatIPLocation(undefined, undefined), "");
  const { hasIPLocation } = load("../src/lib/util.ts", {}, { "./i18n": { t: value => value } });
  assert.equal(hasIPLocation("—", "—"), false);
  assert.equal(hasIPLocation("", ""), false);
  assert.equal(hasIPLocation(undefined, undefined), false);
  assert.equal(hasIPLocation("—", "China Telecom"), true);
  assert.equal(hasIPLocation("局域网/内网", "—"), true);
});

test("IP location localizes a known ISP and leaves unknown ones alone", () => {
  const { formatIPLocation } = load("../src/lib/util.ts", {}, {
    "./i18n": { t: value => (value === "China Telecom" ? "中国电信" : value) },
  });
  assert.equal(formatIPLocation("江苏省 苏州", "China Telecom"), "江苏省 苏州 中国电信");
  assert.equal(formatIPLocation("Tokyo", "Some Local ISP"), "Tokyo Some Local ISP");
});

test("groupWireGuardPeers and config helpers handle keys and filenames safely", () => {
  const placeholder = () => null;
  const stubs = new Proxy({}, { get: () => placeholder });
  const { groupWireGuardPeers, wireguardPeerKey, peerConfigFilename, hasPlaceholderPrivateKey } = load("../src/components/WireGuardPeerSheet.tsx", {}, {
    "../lib/api": stubs,
    "../lib/i18n": { useI18n: () => ({ t: v => v }) },
    "../lib/qrcode": stubs,
    "../lib/toast": { useToast: () => ({ guard: fn => fn(), push() {} }) },
    "../lib/util": stubs,
    "../ui/Icon": stubs,
    "../ui/Sheet": stubs,
    "./bits": stubs,
  });
  assert.equal(groupWireGuardPeers({ supported: false, peers: [{ public_key: "k1", hub: "DEFAULT", user: "sean", label: "", assigned_ip: "" }] }).size, 0);
  const grouped = groupWireGuardPeers({
    supported: true,
    peers: [
      { public_key: "k1", hub: "DEFAULT", user: "Sean", label: "iPhone", assigned_ip: "192.168.30.2" },
      { public_key: "k2", hub: "DEFAULT", user: "sean", label: "Mac", assigned_ip: "192.168.30.3" },
      { public_key: "k3", hub: "OTHER", user: "alice", label: "", assigned_ip: "192.168.30.4" },
    ],
  });
  assert.equal(grouped.get(wireguardPeerKey("DEFAULT", "sean"))?.length, 2);
  assert.equal(grouped.get(wireguardPeerKey("OTHER", "ALICE"))?.length, 1);
  assert.equal(grouped.get(wireguardPeerKey("DEFAULT", "bob")), undefined);
  assert.equal(peerConfigFilename({ hub: "DEFAULT", user: "sean", label: "My Mac" }), "wg-DEFAULT-sean-My-Mac.conf");
  assert.equal(hasPlaceholderPrivateKey("[Interface]\nPrivateKey = <CLIENT_PRIVATE_KEY>\n"), true);
  assert.equal(hasPlaceholderPrivateKey("[Interface]\nPrivateKey = abc=\n"), false);
});

test("encodeQrCode builds valid finder patterns and SVG paths for WireGuard configs", () => {
  const { encodeQrCode, qrSvgPath } = load("../src/lib/qrcode.ts", { TextEncoder, Uint8Array });
  const wgConf = "[Interface]\nPrivateKey = oN9z0iF8j9fJ1B5X3h4z8a5y6x9w0v1u2t3s4r5q6p8=\nAddress = 192.168.30.2/32\n\n[Peer]\nPublicKey = xwO6hiy1V2bu8cD7eF9gH0iJ1kL2mN3oP4qR5sT6uV8=\nEndpoint = vpn.example.com:51820\nAllowedIPs = 0.0.0.0/0, ::/0\n";
  const m = encodeQrCode(wgConf);
  assert.ok(m.version >= 7);
  assert.equal(m.size, m.version * 4 + 17);
  // Top-left finder corners and center must be dark, dark module at (size - 8, 8) must be dark.
  assert.equal(m.modules[0], 1);
  assert.equal(m.modules[3 * m.size + 3], 1);
  assert.equal(m.modules[(m.size - 8) * m.size + 8], 1);
  const path = qrSvgPath(m, 4);
  assert.ok(path.startsWith("M4 4h"));
});

test("sessionReport and exportSessionReport encode filters and use server scope", async () => {
  const client = backend(() => Promise.resolve('{"items":[],"total":0,"summary":{},"hubs":[]}'));
  await client.api.sessionReport({
    from: "2026-11-01T00:00:00.000Z",
    to: "2026-11-19T23:59:59.000Z",
    hub: "DEFAULT",
    q: "alice",
    status: "active",
    limit: 50,
    offset: 100,
  });
  assert.equal(
    client.requests[0].url,
    "api/v1/server/session-history?from=2026-11-01T00%3A00%3A00.000Z&to=2026-11-19T23%3A59%3A59.000Z&hub=DEFAULT&q=alice&status=active&limit=50&offset=100",
  );
  assert.equal(client.requests[0].headers["X-Server-ID"], "1");

  await client.api.exportSessionReport({ hub: "OFFICE", status: "all", lang: "zh-CN", tz_offset: -480 });
  assert.equal(
    client.requests[1].url,
    "api/v1/server/session-history/export?hub=OFFICE&lang=zh-CN&tz_offset=-480",
  );
});

test("SESSION_TYPE and isSystemSession identify system vs client sessions", () => {
  const { SESSION_TYPE, isSystemSession } = load("../src/lib/se.ts");

  // Client sessions
  assert.equal(SESSION_TYPE({ Username_str: "sean", Name_str: "SID-SEAN-1" }), "client");
  assert.equal(isSystemSession({ Username_str: "sean", Name_str: "SID-SEAN-1" }), false);
  assert.equal(isSystemSession({ username: "alice" }), false);

  // SecureNAT
  assert.equal(SESSION_TYPE({ SecureNATMode_bool: true }), "SecureNAT");
  assert.equal(isSystemSession({ SecureNATMode_bool: true }), true);
  assert.equal(SESSION_TYPE({ username: "SecureNAT" }), "SecureNAT");
  assert.equal(isSystemSession({ username: "SecureNAT" }), true);
  assert.equal(isSystemSession({ Username_str: "securenat" }), true);
  assert.equal(isSystemSession({ Name_str: "SID-SECURENAT-1" }), true);

  // Local Bridge
  assert.equal(SESSION_TYPE({ BridgeMode_bool: true }), "bridge");
  assert.equal(isSystemSession({ BridgeMode_bool: true }), true);
  assert.equal(isSystemSession({ username: "Local Bridge" }), true);
  assert.equal(isSystemSession({ username: "sid-local-bridge-1" }), true);

  // Cascade
  assert.equal(SESSION_TYPE({ LinkMode_bool: true }), "cascade");
  assert.equal(isSystemSession({ LinkMode_bool: true }), true);
  assert.equal(isSystemSession({ username: "link_office" }), true);

  // L3 switch
  assert.equal(SESSION_TYPE({ Layer3Mode_bool: true }), "L3 switch");
  assert.equal(isSystemSession({ Layer3Mode_bool: true }), true);
  assert.equal(isSystemSession({ username: "l3_switch" }), true);
});

test("TrafficChart supports rate mode and concurrent session overlay", () => {
  const { TrafficChart } = component("../src/components/TrafficChart.tsx", "TrafficChart", [], {
    "../lib/util": load("../src/lib/util.ts", {}, { "./i18n": { t: value => value } }),
  });
  const usage = chartUsage(24, [
    { ...point(-10, 1_500_000, 3_000_000), sessions: 5 },
    { ...point(-5, 2_000_000, 4_000_000), sessions: 12 },
    { ...point(0, 1_000_000, 2_000_000), sessions: 8 },
  ]);
  const tree = TrafficChart({ usage, defaultMode: "rate" });
  assert.ok(find(tree, node => node.type === "path" && node.props.stroke === "var(--warn)"));
  assert.ok(find(tree, node => node.type === "text" && typeof node.props.children === "string" && node.props.children.includes("bps")));

  // Low bitrate steady traffic: Peak must be >= Avg (both measure combined RX+TX) and Y-axis must not jump to 1.0 Kbps.
  const lowUsage = chartUsage(24, [
    { ...point(-10, 2175, 9675), sessions: 1 }, // ~58 bps send + ~258 bps recv = ~316 bps
    { ...point(-5, 2175, 9675), sessions: 1 },
    { ...point(0, 2175, 9675), sessions: 1 },
  ]);
  const lowTree = TrafficChart({ usage: lowUsage, defaultMode: "rate" });
  const statNode = find(lowTree, node => node.props?.className?.includes("chart__stat"));
  const statNums = [];
  (function collect(n) {
    if (!n) return;
    if (n.type === "b" && typeof n.props?.children === "string") statNums.push(n.props.children);
    for (const c of [].concat(n.props?.children ?? [])) if (c && typeof c === "object") collect(c);
  })(statNode);
  assert.equal(statNums.length, 2);
  assert.equal(statNums[0], "316 bps");
  assert.equal(statNums[1], "316 bps");
  const kbpsTick = find(lowTree, node => node.type === "text" && node.props.children === "1.0 Kbps");
  assert.equal(kbpsTick, null);
});

test("serverAnalytics and sessionReportAnalytics encode filters and use server scope", async () => {
  const client = backend(() => Promise.resolve("{}"));
  await client.api.serverAnalytics(168, "OFFICE");
  assert.equal(client.requests[0].url, "api/v1/server/analytics?hours=168&hub=OFFICE");
  assert.equal(client.requests[0].headers["X-Server-ID"], "1");

  await client.api.sessionReportAnalytics({
    hub: "DEFAULT",
    protocol: "WireGuard",
    sort: "bytes",
    tz_offset: -480,
  });
  assert.equal(
    client.requests[1].url,
    "api/v1/server/session-history/analytics?hub=DEFAULT&protocol=WireGuard&sort=bytes&tz_offset=-480",
  );
  assert.equal(client.requests[1].headers["X-Server-ID"], "1");
});

test("formatDate, timeAgo, formatIPLocation and formatDisconnectReason localize cleanly", () => {
  const zhMap = {
    never: "从未",
    "WireGuard peer inactive timeout": "WireGuard 对端超时断开",
    "China Unicom": "中国联通",
  };
  const enMap = {
    never: "Never",
    "局域网/内网": "LAN / Private",
    "中国联通": "China Unicom",
  };
  const zhUtil = load("../src/lib/util.ts", {}, { "./i18n": { t: k => zhMap[k] ?? k } });
  assert.equal(zhUtil.formatDate(""), "从未");
  assert.equal(zhUtil.timeAgo("1970-01-01T00:00:00Z"), "从未");
  assert.equal(zhUtil.formatDisconnectReason("WireGuard peer inactive timeout"), "WireGuard 对端超时断开");

  const enUtil = load("../src/lib/util.ts", {}, { "./i18n": { t: k => enMap[k] ?? k } });
  assert.equal(enUtil.formatDate(null), "Never");
  assert.equal(enUtil.formatIPLocation("局域网/内网", "—"), "LAN / Private");
  assert.equal(enUtil.formatIPLocation("Jiangsu", "中国联通"), "Jiangsu China Unicom");
});

test("CheckRow distinguishes single-line and multi-line hint layout", () => {
  const { CheckRow } = component("../src/components/bits.tsx", "CheckRow", [], {
    "../ui/Sheet": {},
    "../ui/Icon": {},
    "../lib/toast": {},
    "../lib/rates": {},
  });
  const single = CheckRow({ checked: true, onChange() {}, label: "Encrypt with SSL" });
  assert.equal(single.props.className, "checkrow");
  const multi = CheckRow({ checked: false, onChange() {}, label: "Encrypt with SSL", hint: "Details" });
  assert.equal(multi.props.className, "checkrow checkrow--multiline");
});
