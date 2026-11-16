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
  const placeholder = () => null;
  const stubs = new Proxy({}, { get: () => placeholder });
  return load(file, {}, {
    react: {
      useState: initial => [states[index++] ?? (typeof initial === "function" ? initial() : initial), () => {}],
      useEffect() {}, useCallback: fn => fn, useRef: value => ({ current: value }),
    },
    "../components/bits": stubs,
    "../../components/bits": stubs,
    "../ui/Sheet": stubs, "../../ui/Sheet": stubs,
    "../ui/Icon": stubs, "../../ui/Icon": stubs,
    "../ui/Status": stubs, "../ui/Switch": stubs,
    "../lib/router": stubs,
    "../lib/i18n": { t: value => value }, "../../lib/i18n": { t: value => value },
    "../lib/toast": { useToast: () => ({ guard: fn => fn() }) },
    "../../lib/toast": { useToast: () => ({ guard: fn => fn() }) },
    ...modules,
  }, `\nexport { ${exports} };`);
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
