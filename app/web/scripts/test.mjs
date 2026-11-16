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
    window: { dispatchEvent() {} },
    sessionStorage: { getItem: () => serverId },
    localStorage: { getItem: () => null, removeItem() {} },
    fetch: async (url, options) => {
      requests.push({ url, ...options });
      return { status: 200, ok: true, text };
    },
  });
  return { ...api, requests, switchServer: () => { serverId = "2"; } };
}

function component(file, exports, states, modules) {
  let index = 0;
  let refIndex = 0;
  const refs = [];
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
      useRef: value => refs[refIndex++] ??= { current: value },
    },
    "./bits": stubs,
    "../components/bits": stubs,
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
