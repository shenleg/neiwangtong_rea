// 本文件验证nwtweb页面：--synthetic-only不使用socket；真实Chromium仅允许回环网页，控制操作限合成loopback子网，聊天请求由合成接口接管。
// 用法：node tools/nwtweb-browser-check.mjs --synthetic-only；或 PLAYWRIGHT_MODULE=/path/to/playwright node tools/nwtweb-browser-check.mjs http://127.0.0.1:18788 /tmp/screenshots
import assert from "node:assert/strict";
import { createRequire } from "node:module";
import { isIPv4 } from "node:net";
import { mkdir, readFile } from "node:fs/promises";
import path from "node:path";
import { runInNewContext } from "node:vm";
import { webcrypto } from "node:crypto";

// 无浏览器或socket的合成DOM模式，便于在不能启动sandbox浏览器的环境运行结果回归。
if (process.argv[2] === "--synthetic-only") {
  await checkSyntheticGroupResults();
  await checkSyntheticReceivedMessages();
  await checkSwaggerAuth();
  process.exit(0);
}

const { chromium } = createRequire(import.meta.url)(process.env.PLAYWRIGHT_MODULE || "playwright");
const base = new URL(process.argv[2] || "http://127.0.0.1:18788");
const output = path.resolve(process.argv[3] || "/tmp/nwtweb-browser-check");
assert.equal(base.hostname, "127.0.0.1", "浏览器检查只允许显式IPv4回环网页");
await mkdir(output, { recursive: true });
// 默认启用浏览器sandbox；仅在操作者明确批准的root本地验证中设置此环境变量。
const browser = await chromium.launch({ channel: "chrome", headless: true, chromiumSandbox: process.env.NWT_BROWSER_NO_SANDBOX !== "1" });
const checks = [];
try {
  const page = await browser.newPage({ viewport: { width: 1440, height: 1000 } });
  const scriptErrors = [];
  page.on("pageerror", (error) => scriptErrors.push(error.message));
  // 当前后端尚未提供认证接口；浏览器聊天回归使用合成的已登录状态。
  await page.route("**/auth/status", (route) => route.fulfill({ json: { initialized: true, authenticated: true, api_key_configured: false } }));
  await page.goto(base.href);
  await page.getByText("已离线", { exact: true }).waitFor();
  const liveState = await page.request.get(new URL("/api/state", base).href).then((response) => response.json());
  assert.equal(liveState.discovery_mode, "subnet", "后台必须仅使用子网发现");
  const [subnetAddress, prefix] = liveState.remote.split("/");
  assert.ok(isIPv4(subnetAddress) && subnetAddress.startsWith("127."), "真实控制测试不得面向非loopback子网");
  assert.match(prefix, /^(?:[89]|[12]\d|30)$/, "发现子网必须完全位于127.0.0.0/8内");
  await page.getByRole("button", { name: "上线", exact: true }).click();
  await page.getByRole("button", { name: "下线", exact: true }).waitFor({ state: "visible" });
  await page.waitForFunction(() => !document.getElementById("stop-button").disabled);
  await page.getByRole("button", { name: "下线", exact: true }).click();
  await page.getByText("已离线", { exact: true }).waitFor();
  checks.push("实际HTTP：页面、明确子网上线、正常下线及回到离线");

  const state = {
    phase: "online", name: "Synthetic-Web", own_user_id: "synthetic-own", remote: "127.0.0.0/8", discovery_mode: "subnet",
    started_at: "2026-10-25T00:00:00Z", error: "", stage: "protocol_ready", max_text_bytes: 4096,
    contacts: [
      { user_id: "synthetic-peer", name: "合成联系人", discovery: "127.0.0.2:19011", online: true, sendable: true, reason: "" },
      { user_id: "third-peer", name: "合成群成员", discovery: "127.0.0.5:19011", online: true, sendable: true, reason: "" },
      { user_id: "offline-peer", name: "离线联系人", discovery: "127.0.0.3:19011", online: false, sendable: false, reason: "该联系人当前未完成连接" },
      { user_id: "html-name", name: "<img src=x onerror=alert(1)>", discovery: "127.0.0.4:19011", online: false, sendable: false, reason: "该联系人当前未完成连接" },
    ],
    groups: [
      { group_id: "synthetic-group", name: "合成双成员群", members: ["synthetic-own", "synthetic-peer"], sendable: true, reason: "" },
      { group_id: "multi-group", name: "合成多成员群", members: ["synthetic-own", "synthetic-peer", "third-peer"], sendable: true, reason: "" },
      { group_id: "mixed-group", name: "合成在线离线群", members: ["synthetic-own", "synthetic-peer", "offline-peer"], sendable: true, reason: "" },
      { group_id: "offline-group", name: "合成全离线群", members: ["synthetic-own", "offline-peer", "missing-peer"], sendable: false, reason: "没有可用的READY群成员，不投递、不自动补发" },
    ],
    events: [{ stage: "protocol_ready", elapsed_ms: 50 }], messages: [], received_total: 0, received_dropped: 0,
  };
  const posts = [];
  let stateGets = 0;
  let abortNext = false;
  let partialNext = false;
  await page.route("**/api/**", async (route) => {
    const request = route.request();
    if (request.method() === "GET") {
      stateGets += 1;
      return route.fulfill({ json: state });
    }
    const payload = request.postDataJSON();
    posts.push({ url: request.url(), payload, headers: request.headers() });
    if (abortNext) {
      abortNext = false;
      return route.abort("failed");
    }
    const partial = partialNext;
    partialNext = false;
    const mixed = payload.group_id === "mixed-group";
    const result = { command: request.url().endsWith("/group") ? 3011 : 1004, message_id: String(posts.length),
      submitted: true, transport_acknowledged: !partial && !mixed, business_acknowledged: false, receipt_received: false, delivery: "unknown" };
    if (result.command === 3011) result.recipients = groupRecipients(payload.group_id === "multi-group" || mixed, partial, mixed);
    return route.fulfill({ status: partial ? 502 : 200, json: { request_id: payload.request_id,
      result, error: partial ? "部分成员路由失败，勿重复发送" : "" } });
  });
  await page.reload();
  await page.getByText("已在线", { exact: true }).waitFor();
  assert.equal(await page.locator("#discovery-label").textContent(), "局域网发现 · 2 人在线");
  assert.equal(await page.locator("#session-remote-label").textContent(), "发现子网");
  assert.equal(await page.locator("#session-remote").textContent(), "127.0.0.0/8");
  await page.getByRole("button", { name: "合成联系人，ID synthetic-peer，在线", exact: true }).click();
  const editor = page.getByRole("textbox", { name: "消息正文", exact: true });
  await editor.fill("第一行\n第二行 🙂");
  await page.getByRole("button", { name: "发送消息", exact: true }).click();
  await page.getByText("传输已确认 · 送达状态未知", { exact: true }).waitFor();
  assert.equal(posts.length, 1);
  assert.equal(posts[0].payload.text, "第一行\n第二行 🙂");
  assert.equal(posts[0].payload.user_id, "synthetic-peer");
  assert.equal(posts[0].headers.authorization, undefined);
  assert.equal(posts[0].headers["x-nwt-request"], undefined);
  checks.push("单聊：精确目标、多行emoji、一次POST、未知送达状态");

  await editor.fill("用户草稿");
  await page.getByRole("tab", { name: /已加入群/ }).click();
  await page.getByRole("button", { name: "合成全离线群，ID offline-group，不可发送", exact: true }).click();
  await editor.fill("全部成员离线时不得提交");
  assert.equal(await page.getByRole("button", { name: "发送消息", exact: true }).isDisabled(), true);
  await page.locator("#composer").dispatchEvent("submit");
  assert.equal(posts.length, 1, "全部离线不发送POST");
  await page.getByRole("button", { name: "合成双成员群，ID synthetic-group，可发送", exact: true }).click();
  assert.equal(await editor.inputValue(), "");
  await editor.fill("群消息\n保留换行");
  await page.getByRole("button", { name: "发送消息", exact: true }).click();
  await page.getByText("传输已确认 · 送达状态未知", { exact: true }).waitFor();
  assert.equal(posts.length, 2);
  assert.equal(posts[1].payload.group_id, "synthetic-group");
  assert.equal(posts[1].payload.text, "群消息\n保留换行");
  await page.getByRole("tab", { name: /联系人/ }).click();
  await page.getByRole("button", { name: "合成联系人，ID synthetic-peer，在线", exact: true }).click();
  assert.equal(await editor.inputValue(), "用户草稿");
  checks.push("群聊：完整目标、全部离线时禁发且无POST、每目标草稿隔离");

  await page.getByRole("tab", { name: /已加入群/ }).click();
  await page.getByRole("button", { name: "合成多成员群，ID multi-group，可发送", exact: true }).click();
  await editor.fill("多成员群全部传输确认");
  await page.getByRole("button", { name: "发送消息", exact: true }).click();
  await page.getByText("传输已确认 · 送达状态未知", { exact: true }).waitFor();
  await page.locator("#message-records details summary").click();
  assert.equal(await page.locator(".recipient-result").count(), 2);
  assert.equal(await page.locator(".recipient-result").filter({ hasText: "传输 ACK：有" }).count(), 2);
  await page.getByRole("button", { name: "合成在线离线群，ID mixed-group，可发送", exact: true }).click();
  await editor.fill("向在线成员发送，离线成员不补发");
  await page.getByRole("button", { name: "发送消息", exact: true }).click();
  await page.locator("#send-notice").filter({ hasText: "在线成员传输已确认，另有1名成员未投递" }).waitFor();
  assert.equal(await editor.inputValue(), "");
  await page.locator("#message-records details summary").click();
  assert.match(await page.locator("#message-records").textContent(), /成员 offline-peer.*连接 0.*skipped：true（未投递）.*连接未就绪/s);
  assert.match(await page.locator("#message-records").textContent(), /传输确认：未确认/);
  assert.doesNotMatch(await page.locator("#message-records").textContent(), /结果未知，请先确认/);
  const afterMixed = posts.length;
  state.events = [{ stage: "transport_acknowledged", command: 3011, message_id: String(afterMixed), peer_index: 1, elapsed_ms: 500 }];
  await page.getByRole("button", { name: /刷新状态/ }).click();
  await page.waitForFunction(() => !document.getElementById("refresh-button").disabled);
  assert.match(await page.locator("#message-records").textContent(), /传输确认：未确认/);
  assert.equal(posts.length, afterMixed, "跳过成员不自动补发");
  checks.push("在线加离线群：HTTP200且整群ACK为false仍清稿，显示未投递原因，事件不改聚合结果或补发");
  await page.getByRole("button", { name: "合成多成员群，ID multi-group，可发送", exact: true }).click();
  partialNext = true;
  await editor.fill("部分提交后不得重发整群");
  await page.getByRole("button", { name: "发送消息", exact: true }).click();
  await page.locator("#send-notice").filter({ hasText: "勿重复发送" }).waitFor();
  await page.locator("#message-records details summary").last().click();
  const partialRecord = page.locator("#message-records .message-record").last();
  assert.match(await partialRecord.textContent(), /成员 third-peer.*已提交：否.*连接已断开/s);
  assert.equal(await editor.inputValue(), "部分提交后不得重发整群");
  const afterPartial = posts.length;
  state.events = ["transport_acknowledged", "business_acknowledged", "receipt_received"].flatMap((stage) => [
    { stage, command: 3011, message_id: String(afterPartial), peer_index: 1, elapsed_ms: 500 },
    { stage, command: 1004, message_id: String(afterPartial), peer_index: 1, elapsed_ms: 501 },
  ]);
  await page.getByRole("button", { name: /刷新状态/ }).click();
  await page.waitForFunction(() => !document.getElementById("refresh-button").disabled);
  assert.match(await partialRecord.textContent(), /传输确认：未确认/);
  assert.match(await partialRecord.textContent(), /业务 ACK：未确认 · 协议回执：未确认/);
  assert.equal(posts.length, afterPartial);
  checks.push("多成员群合成结果：全部ACK、HTTP502逐成员失败、单peer/同号单聊事件不误改聚合结果、刷新不重发");
  await page.getByRole("tab", { name: /联系人/ }).click();
  await page.getByRole("button", { name: "合成联系人，ID synthetic-peer，在线", exact: true }).click();

  await editor.fill("汉".repeat(1366));
  assert.equal(await page.getByRole("button", { name: "发送消息", exact: true }).isDisabled(), true);
  assert.equal(await page.locator("#byte-count").textContent(), "4098 / 4096 字节");
  await editor.fill(" ");
  assert.equal(await page.getByRole("button", { name: "发送消息", exact: true }).isEnabled(), true);
  checks.push("UTF-8字节上限、非空空格正文不被trim");

  await page.getByRole("searchbox", { name: "搜索名称或 ID", exact: true }).fill("html-name");
  assert.equal(await page.locator("#target-list button").count(), 1);
  assert.equal(await page.locator("#target-list img").count(), 0);
  await page.getByRole("searchbox", { name: "搜索名称或 ID", exact: true }).fill("");
  await editor.fill("提交结果未知时保留草稿");
  abortNext = true;
  await page.getByRole("button", { name: "发送消息", exact: true }).click();
  await page.locator("#send-notice").filter({ hasText: "结果未知，请先确认，勿重复发送" }).waitFor();
  assert.equal(await editor.inputValue(), "提交结果未知时保留草稿");
  const afterFailure = posts.length;
  const beforeGets = stateGets;
  await page.getByRole("button", { name: /刷新状态/ }).click();
  await page.waitForFunction(() => !document.getElementById("refresh-button").disabled);
  assert.ok(stateGets > beforeGets);
  assert.equal(posts.length, afterFailure);
  checks.push("搜索与XSS文本展示、网络失败保留草稿、刷新只GET不补发POST");

  const firstContact = state.contacts[0];
  state.contacts = state.contacts.map((contact) => ({ ...contact, online: false, sendable: false, reason: "该联系人当前未完成连接" }));
  await page.getByRole("button", { name: /刷新状态/ }).click();
  await page.waitForFunction(() => !document.getElementById("refresh-button").disabled);
  assert.equal(await page.locator("#phase-label").textContent(), "等待客户端");
  assert.equal(await page.locator("#discovery-label").textContent(), "局域网发现 · 0 人在线");
  assert.equal(await page.getByRole("button", { name: "发送消息", exact: true }).isDisabled(), true);
  assert.equal(posts.length, afterFailure);
  checks.push("Host已上线但无就绪客户端时显示等待客户端，禁止发送");

  state.contacts = [firstContact,
    { user_id: "second-online", name: "第二位在线联系人", discovery: "127.0.0.3:19011", online: true, sendable: true, reason: "" },
    { user_id: "third-online", name: "第三位在线联系人", discovery: "127.0.0.4:19011", online: true, sendable: true, reason: "" }];
  await page.getByRole("button", { name: /刷新状态/ }).click();
  await page.waitForFunction(() => !document.getElementById("refresh-button").disabled);
  assert.equal(await page.locator("#target-list button").count(), 3);
  assert.equal(await page.locator("#discovery-label").textContent(), "局域网发现 · 3 人在线");
  await page.getByRole("button", { name: "第三位在线联系人，ID third-online，在线", exact: true }).click();
  await editor.fill("第三位联系人可独立选择，不发送");
  assert.equal(await page.getByRole("button", { name: "发送消息", exact: true }).isEnabled(), true);
  assert.equal(posts.length, afterFailure);
  checks.push("三个在线联系人完整展示、分别可选，未因唯一peer字段缺失误标离线");

  assert.equal(await page.locator("#directory-panel").evaluate((element) => element.scrollWidth <= element.clientWidth), true);
  await page.screenshot({ path: path.join(output, "desktop.png"), fullPage: true });
  await page.setViewportSize({ width: 390, height: 844 });
  assert.equal(await page.locator("#discovery-label").isVisible(), true);
  assert.equal(await page.locator("#discovery-label").textContent(), "局域网发现 · 3 人在线");
  assert.equal(await page.evaluate(() => document.documentElement.scrollWidth <= window.innerWidth), true);
  await page.screenshot({ path: path.join(output, "mobile.png"), fullPage: true });
  assert.deepEqual(scriptErrors, []);
  checks.push("1440px/390px真实渲染，无横向溢出，无脚本异常");
  console.log(JSON.stringify({ checks, screenshots: output, messagePosts: posts.length }, null, 2));
} finally {
  await browser.close();
}

/** 验证文档声明 Bearer 认证且只向同源请求发送 Key；读取或断言失败时抛错。 */
async function checkSwaggerAuth() {
  const spec = JSON.parse(await readFile(new URL("../internal/nativeweb/assets/docs/openapi.json", import.meta.url), "utf8"));
  assert.equal(spec.components.securitySchemes.bearerAuth.scheme, "bearer");
  assert.deepEqual(spec.security, [{ bearerAuth: [] }]);
  const source = await readFile(new URL("../internal/nativeweb/assets/docs/init.js", import.meta.url), "utf8");
  let config;
  const window = { location: { href: "http://example.test/docs", origin: "http://example.test" }, addEventListener: (_, callback) => callback() };
  runInNewContext(source, { window, URL, SwaggerUIBundle: (value) => { config = value; } });
  assert.equal(config.persistAuthorization, false);
  const request = config.requestInterceptor({ url: "/contacts", headers: { Authorization: "Bearer test-key" } });
  assert.equal(request.headers.Authorization, "Bearer test-key");
  assert.equal(request.redirect, "error");
  assert.throws(() => config.requestInterceptor({ url: "http://other.test/contacts", headers: { Authorization: "Bearer test-key" } }));
}

/** 生成逐成员结果；multi增加第二人，partial模拟实际路由失败，skipped模拟离线跳过；不执行I/O，无异常。 */
function groupRecipients(multi, partial = false, skipped = false) {
  const recipients = [{ user_id: "synthetic-peer", peer_index: 1, skipped: false, submitted: true, transport_acknowledged: true, error: "" }];
  if (multi) recipients.push({ user_id: skipped ? "offline-peer" : "third-peer", peer_index: skipped ? 0 : 2, skipped,
    submitted: !partial && !skipped, transport_acknowledged: !partial && !skipped, error: skipped ? "连接未就绪" : partial ? "连接已断开" : "" });
  return recipients;
}

/** 在最小合成DOM中执行真实app.js发送及事件回填，验证聚合事实；断言或源码读取失败时抛错，无socket。 */
async function checkSyntheticGroupResults() {
  class Element {
    constructor(tag = "div") {
      this.tagName = tag;
      this.children = [];
      this.dataset = {};
      this.classList = { toggle() {} };
      this.value = "";
      this.disabled = false;
      this.scrollHeight = this.scrollTop = this.clientHeight = 0;
      this.text = "";
    }
    set textContent(value) { this.text = String(value); this.children = []; }
    get textContent() { return this.text + this.children.map((child) => child.textContent).join("\n"); }
    append(...children) { this.children.push(...children); }
    replaceChildren(...children) { this.text = ""; this.children = children; }
    setAttribute() {}
    querySelectorAll(selector) {
      const descendants = this.children.flatMap((child) => [child, ...child.querySelectorAll(selector)]);
      return descendants.filter((child) => selector === "details[open]" && child.tagName === "details" && child.open);
    }
  }
  const elements = new Map();
  const document = {
    getElementById(id) {
      if (!elements.has(id)) elements.set(id, new Element());
      return elements.get(id);
    },
    createElement: (tag) => new Element(tag),
    createDocumentFragment: () => new Element("fragment"),
  };
  const posts = [];
  let response;
  let authStatus = { initialized: false, authenticated: false, api_key_configured: false };
  const authRequests = [];
  const context = { document, TextEncoder, AbortSignal, window: { crypto: webcrypto },
    fetch: async (url, options) => {
      if (url.startsWith("/auth/")) {
        assert.equal(options.redirect, "error");
        if (url === "/auth/status") return { ok: true, status: 200, json: async () => structuredClone(authStatus) };
        authRequests.push({ url, method: options.method, payload: JSON.parse(options.body) });
        if (url === "/auth/setup" || url === "/auth/login") authStatus = { ...authStatus, initialized: true, authenticated: true };
        if (url === "/auth/logout") authStatus.authenticated = false;
        if (url === "/auth/api-key") authStatus.api_key_configured = options.method !== "DELETE";
        return { ok: true, status: 200, json: async () => url === "/auth/api-key" && options.method === "POST" ? { api_key: "one-time-key" } : {} };
      }
      assert.equal(options.method, "POST");
      assert.equal(options.redirect, "error");
      posts.push({ url, payload: JSON.parse(options.body) });
      return { ok: response.ok, status: response.status, json: async () => structuredClone(response.data) };
    } };
  const source = await readFile(new URL("../internal/nativeweb/assets/app.js", import.meta.url), "utf8");
  const boundary = source.indexOf('  $("auth-form").addEventListener');
  assert.ok(boundary > 0, "必须执行真实页面逻辑，不能误取空源码");
  runInNewContext(source.slice(0, boundary) + `
    refreshState = () => Promise.resolve();
    globalThis.fixture = {
      records,
      refreshAuth, submitAuth, changeAuth, clearKey,
      prepare(mode = "ready") {
        authenticated = true;
        state = { phase: "online", started_at: "synthetic", own_user_id: "synthetic-own", remote: "127.0.0.0/8", max_text_bytes: 4096,
          contacts: [{ user_id: "synthetic-peer", name: "合成联系人", online: mode !== "offline", sendable: mode !== "offline" },
            { user_id: mode === "ready" ? "third-peer" : "offline-peer", name: "合成群成员", online: mode === "ready", sendable: mode === "ready" }],
          groups: [{ group_id: "multi-group", name: "合成多成员群", members: ["synthetic-own", "synthetic-peer", mode === "ready" ? "third-peer" : "offline-peer"],
            sendable: mode !== "offline", reason: mode === "offline" ? "没有可用的READY群成员" : "" }], events: [], messages: [], received_total: 0, received_dropped: 0 };
        reachable = true;
        selected = { kind: "group", id: "multi-group", name: "合成多成员群" };
        records.length = 0;
        warnings.clear();
        $("message-text").value = "完整群正文";
        drafts.set(keyOf(selected), "完整群正文");
      },
      selectUser() { selected = { kind: "user", id: "synthetic-peer", name: "合成联系人" }; },
      send: () => sendMessage({ preventDefault() {} }),
      apply(events) { state.events = events; applyAcknowledgements(); }
    };
  })();`, context);
  const fixture = context.fixture;
  await fixture.refreshAuth();
  assert.equal(document.getElementById("auth-title").textContent, "首次初始化");
  assert.equal(document.getElementById("app-shell").hidden, true);
  document.getElementById("auth-code").value = "setup-code";
  document.getElementById("auth-password").value = "setup-password";
  await fixture.submitAuth({ preventDefault() {} });
  assert.deepEqual(authRequests[0], { url: "/auth/setup", method: "POST", payload: { code: "setup-code", password: "setup-password" } });
  assert.equal(document.getElementById("auth-password").value, "");
  assert.equal(document.getElementById("auth-code").value, "");
  assert.equal(document.getElementById("app-shell").hidden, false);
  await fixture.changeAuth("create");
  assert.equal(document.getElementById("key-value").textContent, "one-time-key");
  assert.equal(document.getElementById("key-result").hidden, false);
  fixture.clearKey();
  assert.equal(document.getElementById("key-value").textContent, "");
  await fixture.changeAuth("revoke");
  assert.equal(authRequests[2].method, "DELETE");
  assert.equal(document.getElementById("key-revoke").disabled, true);
  await fixture.changeAuth("logout");
  assert.equal(document.getElementById("auth-title").textContent, "密码登录");
  assert.equal(document.getElementById("app-shell").hidden, true);
  document.getElementById("auth-password").value = "login-password";
  await fixture.submitAuth({ preventDefault() {} });
  assert.deepEqual(authRequests[4], { url: "/auth/login", method: "POST", payload: { password: "login-password" } });
  assert.equal(document.getElementById("auth-password").value, "");
  assert.equal(document.getElementById("app-shell").hidden, false);
  const result = (partial) => ({ command: 3011, message_id: "7", submitted: true, transport_acknowledged: !partial,
    business_acknowledged: false, receipt_received: false, delivery: "unknown", recipients: groupRecipients(true, partial) });
  fixture.prepare();
  response = { ok: true, status: 200, data: { result: result(false), error: "" } };
  await fixture.send();
  assert.equal(posts.length, 1);
  assert.equal(posts[0].payload.group_id, "multi-group");
  assert.equal(fixture.records[0].result.transport_acknowledged, true);
  assert.match(document.getElementById("message-records").textContent, /成员 third-peer.*传输 ACK：有/);
  assert.equal(document.getElementById("message-text").value, "");

  fixture.prepare();
  response = { ok: false, status: 502, data: { result: result(true), error: "部分成员路由失败" } };
  await fixture.send();
  assert.equal(posts.length, 2);
  assert.equal(fixture.records[0].result.submitted, true);
  assert.equal(fixture.records[0].result.recipients[1].submitted, false);
  assert.equal(document.getElementById("message-text").value, "完整群正文");
  assert.match(document.getElementById("message-records").textContent, /成员 third-peer.*已提交：否.*连接已断开/);
  assert.match(document.getElementById("message-records").textContent, /勿向整群重复发送/);
  fixture.apply([3011, 1004, 1005, 1006].flatMap((command) => ["transport_acknowledged", "business_acknowledged", "receipt_received"].map((stage) => ({ command, stage, message_id: "7", peer_index: 1 }))));
  assert.equal(fixture.records[0].result.transport_acknowledged, false);
  assert.equal(fixture.records[0].result.business_acknowledged, false);
  assert.equal(fixture.records[0].result.receipt_received, false);
  assert.equal(posts.length, 2, "事件刷新不得补发POST");

  fixture.prepare("mixed");
  response = { ok: true, status: 200, data: { result: { ...result(false), transport_acknowledged: false, recipients: groupRecipients(true, false, true) }, error: "" } };
  await fixture.send();
  assert.equal(document.getElementById("message-text").value, "", "可用路由全部确认后清稿，不依赖整群ACK");
  assert.equal(fixture.records[0].result.transport_acknowledged, false);
  assert.match(document.getElementById("message-records").textContent, /在线成员传输已确认，另有1名成员未投递/);
  assert.match(document.getElementById("send-notice").textContent, /不自动补发/);
  assert.match(document.getElementById("message-records").textContent, /成员 offline-peer.*连接 0.*skipped：true（未投递）.*连接未就绪/);
  assert.doesNotMatch(document.getElementById("message-records").textContent, /结果未知，请先确认/);
  const afterMixed = posts.length;
  fixture.apply([{ command: 3011, stage: "transport_acknowledged", message_id: "7", peer_index: 1 }]);
  assert.equal(fixture.records[0].result.transport_acknowledged, false);
  assert.equal(posts.length, afterMixed);

  fixture.prepare("offline");
  await fixture.send();
  assert.equal(document.getElementById("send-button").disabled, true);
  assert.equal(posts.length, afterMixed, "全部离线时禁用且不得发送");
  assert.equal(fixture.records.length, 0);
  assert.equal(document.getElementById("message-text").value, "完整群正文");

  fixture.prepare("mixed");
  response = { ...response, ok: false, status: 502 };
  await fixture.send();
  assert.equal(document.getElementById("message-text").value, "完整群正文", "HTTP错误即使路由均确认也保留草稿");
  assert.match(document.getElementById("message-records").textContent, /HTTP 502/);

  fixture.prepare();
  fixture.selectUser();
  response = { ok: true, status: 200, data: { result: { ...result(false), command: 1004, recipients: undefined }, error: "" } };
  await fixture.send();
  fixture.apply([3011, 1004, 1006].map((command) => ({ command, stage: "business_acknowledged", message_id: "7", peer_index: 1 })));
  assert.equal(fixture.records[0].result.business_acknowledged, false);
  fixture.apply([{ command: 1005, stage: "business_acknowledged", message_id: "7", peer_index: 1 }]);
  assert.equal(fixture.records[0].result.business_acknowledged, true);
  fixture.apply([1004, 1005, 3011].map((command) => ({ command, stage: "receipt_received", message_id: "7", peer_index: 1 })));
  assert.equal(fixture.records[0].result.receipt_received, false);
  fixture.apply([{ command: 1006, stage: "receipt_received", message_id: "7", peer_index: 1 }]);
  assert.equal(fixture.records[0].result.receipt_received, true);
  fixture.records[0].result.transport_acknowledged = false;
  fixture.apply([1005, 1006, 3011].map((command) => ({ command, stage: "transport_acknowledged", message_id: "7", peer_index: 1 })));
  assert.equal(fixture.records[0].result.transport_acknowledged, false);
  fixture.apply([{ command: 1004, stage: "transport_acknowledged", message_id: "7", peer_index: 1 }]);
  assert.equal(fixture.records[0].result.transport_acknowledged, true);
  console.log(JSON.stringify({ checks: ["首次初始化、登录、退出与一次性 Key 生成/撤销", "多成员群全部传输确认", "在线加离线群HTTP200清稿并显示skipped及原因，整群ACK仍false", "全部离线禁用且无POST", "HTTP502及实际路由失败保留逐成员结果与草稿", "单peer及同号单聊事件不修改群聚合结果", "1004传输/1005业务ACK/1006回执按阶段回填单聊", "事件刷新不重发POST"], messagePosts: posts.length, network: "无socket，仅合成DOM/fetch" }, null, 2));
}

/** 用合成 DOM 和 GET 快照驱动完整页面，验证接收消息、会话及认证边界；不打开 socket，断言失败时抛错。 */
async function checkSyntheticReceivedMessages() {
  class Element {
    constructor(tag = "div") {
      this.tagName = tag.toLowerCase();
      this.children = [];
      this.dataset = {};
      this.attributes = {};
      this.listeners = {};
      this.value = "";
      this.className = "";
      this.text = "";
      this.scrollHeight = this.scrollTop = this.clientHeight = 0;
      this.classList = {
        toggle: (name, enabled) => {
          const classes = new Set(this.className.split(/\s+/).filter(Boolean));
          if (enabled ?? !classes.has(name)) classes.add(name); else classes.delete(name);
          this.className = [...classes].join(" ");
        },
        contains: (name) => this.className.split(/\s+/).includes(name),
      };
    }
    set textContent(value) { this.text = String(value); this.children = []; }
    get textContent() { return this.text + this.children.map((child) => child.textContent).join(""); }
    append(...children) {
      for (const child of children) {
        if (child.tagName === "fragment") this.append(...child.children);
        else this.children.push(child);
      }
    }
    replaceChildren(...children) { this.text = ""; this.children = []; this.append(...children); }
    setAttribute(name, value) { this.attributes[name] = String(value); }
    getAttribute(name) { return this.attributes[name] ?? null; }
    addEventListener(type, listener) { (this.listeners[type] ??= []).push(listener); }
    dispatch(type, properties = {}) {
      for (const listener of this.listeners[type] ?? []) listener({ preventDefault() {}, target: this, ...properties });
    }
    contains(element) { return element === this || this.children.some((child) => child.contains(element)); }
    focus() { document.activeElement = this; }
    querySelectorAll(selector) {
      const descendants = this.children.flatMap((child) => [child, ...child.querySelectorAll(selector)]);
      return descendants.filter((child) => {
        if (selector === "details[open]") return child.tagName === "details" && child.open;
        const [tag, ...classes] = selector.split(".");
        return (!tag || child.tagName === tag) && classes.every((name) => child.classList.contains(name));
      });
    }
  }
  const elements = new Map();
  const tabs = ["user", "group"].map((kind) => {
    const tab = new Element("button");
    tab.dataset.kind = kind;
    tab.id = `tab-${kind}`;
    return tab;
  });
  const document = {
    activeElement: null,
    getElementById(id) {
      if (!elements.has(id)) elements.set(id, new Element());
      return elements.get(id);
    },
    createElement: (tag) => new Element(tag),
    createDocumentFragment: () => new Element("fragment"),
    querySelectorAll: (selector) => selector === '[role="tab"]' ? tabs : [],
  };
  const $ = (id) => document.getElementById(id);
  let auth = { initialized: true, authenticated: true, api_key_configured: false };
  let snapshot = {
    phase: "online", name: "合成本机", started_at: "2026-10-25T00:00:00Z", own_user_id: "own", remote: "127.0.0.0/8",
    stage: "protocol_ready", error: "", max_text_bytes: 4096,
    contacts: [
      { user_id: "alice", name: "Alice", discovery: "synthetic", online: true, sendable: true, reason: "" },
      { user_id: "bob", name: "Bob", discovery: "synthetic", online: true, sendable: true, reason: "" },
    ],
    groups: [{ group_id: "group", name: "测试群", members: ["own", "alice", "bob"], sendable: true, reason: "" }],
    events: [], messages: [], received_total: 0, received_dropped: 0,
  };
  let stateStatus = 200;
  let pendingState;
  let getCount = 0;
  const posts = [];
  const reply = (data, status = 200) => ({ ok: status >= 200 && status < 300, status, json: async () => structuredClone(data) });
  const context = {
    document, TextEncoder, AbortSignal,
    window: { crypto: webcrypto, setInterval() {} },
    fetch: async (url, options) => {
      assert.equal(options.redirect, "error");
      if (url === "/auth/status") return reply(auth);
      if (url === "/auth/logout") { auth.authenticated = false; return reply({}); }
      if (url === "/auth/login") { auth.authenticated = true; return reply({}); }
      if (url === "/api/state") {
        assert.equal(options.method, undefined, "状态只能通过 GET 读取");
        getCount++;
        if (pendingState) return pendingState.promise;
        return reply(snapshot, stateStatus);
      }
      assert.equal(url, "/api/messages/user", "不允许其他网络请求");
      assert.equal(options.method, "POST");
      posts.push(JSON.parse(options.body));
      return reply({ result: { command: 1004, message_id: "7", submitted: true, transport_acknowledged: false,
        business_acknowledged: false, receipt_received: false, delivery: "unknown" }, error: "" });
    },
  };
  const source = await readFile(new URL("../internal/nativeweb/assets/app.js", import.meta.url), "utf8");
  const css = await readFile(new URL("../internal/nativeweb/assets/app.css", import.meta.url), "utf8");
  assert.match(css, /\.record-text\s*\{[^}]*white-space:\s*pre-wrap\s*;/, "消息样式必须保留换行");
  const boundary = source.indexOf('  $("auth-form").addEventListener');
  assert.ok(boundary > 0, "必须运行真实页面的事件绑定和初始化");
  runInNewContext(source.slice(0, boundary) + "  globalThis.fixture = { refreshState, refreshAuth, changeAuth, submitAuth };\n" + source.slice(boundary), context);
  const { fixture } = context;
  await fixture.refreshAuth();
  await fixture.refreshState();
  const select = (kind, id) => {
    tabs.find((tab) => tab.dataset.kind === kind).dispatch("click");
    const button = $("target-list").querySelectorAll("button").find((item) => item.dataset.key === `${kind}:${id}`);
    assert.ok(button, `目标 ${kind}:${id} 应存在`);
    button.dispatch("click");
  };
  const target = (id) => $("target-list").querySelectorAll("button").find((item) => item.dataset.key === id);
  const received = () => $("message-records").querySelectorAll(".message-record.received");
  const push = (sequence, command, sender, group, text, messageID = "7") => {
    snapshot.messages.push({ sequence, received_at: `2026-10-25T00:00:${String(sequence).padStart(2, "0")}Z`, command,
      message_id: messageID, sender_id: sender, sender_name: sender === "alice" ? "Alice" : "Bob", group_id: group, text });
    snapshot.received_total++;
  };
  const refresh = async () => { const before = getCount; await fixture.refreshState(); assert.ok(getCount > before, "必须执行状态 GET"); };
  const body = (item) => item.querySelectorAll(".record-text")[0].textContent;

  select("user", "alice");
  $("message-text").value = "发送待确认";
  $("message-text").dispatch("input");
  $("composer").dispatch("submit");
  // 等待真实发送处理中的自动刷新完成。
  await new Promise((resolve) => setImmediate(resolve));
  assert.equal(posts.length, 1);
  assert.match($("message-records").textContent, /传输确认：未确认/);
  $("message-text").value = "Alice 草稿";
  $("message-text").dispatch("input");
  push(1, 1004, "alice", "", "单聊第一行\n<img src=x onerror=alert(1)>");
  push(2, 1004, "bob", "", "Bob 单聊同编号");
  push(3, 3011, "alice", "group", "Alice 群消息同编号");
  push(4, 3011, "bob", "group", "Bob 群消息同编号");
  await refresh();
  assert.equal($("target-name").textContent, "Alice", "入站消息不能切换目标");
  assert.equal($("message-text").value, "Alice 草稿", "入站消息不能覆盖草稿");
  assert.equal(received().length, 1);
  assert.equal(body(received()[0]), "单聊第一行\n<img src=x onerror=alert(1)>");
  assert.equal(received()[0].querySelectorAll("img").length, 0, "正文必须作为文本而非 HTML");
  assert.match($("message-records").textContent, /传输确认：未确认/);
  assert.match($("message-records").textContent, /业务 ACK：未确认 · 协议回执：未确认/, "同编号入站消息不得改变发送 ACK");
  assert.equal(target("user:bob").querySelectorAll(".unread-count")[0].textContent, "1");
  assert.match(target("user:bob").getAttribute("aria-label"), /1 条未读/);
  await refresh();
  assert.equal(received().length, 1, "重复快照不得重复显示");
  assert.equal(target("user:bob").querySelectorAll(".unread-count")[0].textContent, "1", "重复快照不得重复计未读");

  select("user", "bob");
  assert.equal(received().length, 1);
  assert.equal(body(received()[0]), "Bob 单聊同编号");
  assert.equal(target("user:bob").querySelectorAll(".unread-count").length, 0, "选择会话清除未读");
  tabs.find((tab) => tab.dataset.kind === "group").dispatch("click");
  assert.equal(target("group:group").querySelectorAll(".unread-count")[0].textContent, "2", "群消息按条累计未读");
  select("group", "group");
  assert.equal(received().length, 2, "不同发送者的同编号群消息均应显示");
  assert.deepEqual(received().map(body), ["Alice 群消息同编号", "Bob 群消息同编号"]);
  assert.match(received()[0].textContent, /Alice/);
  assert.match(received()[1].textContent, /Bob/);
  assert.equal(target("group:group").querySelectorAll(".unread-count").length, 0);
  select("user", "alice");
  assert.equal($("message-text").value, "Alice 草稿");
  snapshot.received_dropped = 2;
  await refresh();
  assert.match($("received-notice").textContent, /2/, "丢弃数量必须显示");

  snapshot = { ...snapshot, started_at: "2026-10-25T01:00:00Z", messages: [], received_total: 0, received_dropped: 0 };
  push(1, 1004, "alice", "", "新会话同序号");
  await refresh();
  assert.equal(received().length, 1, "新后台会话清除旧接收正文并接受同序号");
  assert.equal(body(received()[0]), "新会话同序号");

  // 退出时即使此前 GET 晚到，也不能把旧正文写回未认证页面。
  let resolvePending;
  pendingState = { promise: new Promise((resolve) => { resolvePending = resolve; }) };
  const stale = fixture.refreshState();
  await fixture.changeAuth("logout");
  assert.equal($("app-shell").hidden, true);
  assert.doesNotMatch($("message-records").textContent, /新会话同序号|单聊第一行|发送待确认/, "退出必须清除收发正文");
  assert.equal($("message-text").value, "");
  const oldSnapshot = structuredClone(snapshot);
  pendingState = null;
  resolvePending(reply(oldSnapshot));
  await stale;
  assert.doesNotMatch($("message-records").textContent, /新会话同序号|单聊第一行/, "退出后的旧 GET 不得恢复正文");
  snapshot = { ...snapshot, messages: [], received_total: 0, received_dropped: 0 };
  $("auth-password").value = "synthetic-password";
  await fixture.submitAuth({ preventDefault() {} });
  await fixture.refreshState();
  select("user", "alice");
  assert.doesNotMatch($("message-records").textContent, /新会话同序号|单聊第一行/, "重新登录空快照不得恢复本地旧正文");
  snapshot = structuredClone(oldSnapshot);
  await refresh();
  assert.match($("message-records").textContent, /新会话同序号/, "有效认证后允许读取后台当前缓冲");
  push(2, 1004, "alice", "", "401 前消息");
  await refresh();
  assert.match($("message-records").textContent, /401 前消息/);
  $("message-text").value = "401 前草稿";
  $("message-text").dispatch("input");
  auth.authenticated = false;
  stateStatus = 401;
  await refresh();
  await new Promise((resolve) => setImmediate(resolve));
  assert.equal($("app-shell").hidden, true);
  assert.doesNotMatch($("message-records").textContent, /401 前消息/, "401 必须清空正文 DOM");
  assert.equal($("message-text").value, "");
  snapshot = { ...snapshot, messages: [], received_total: 0, received_dropped: 0 };
  stateStatus = 200;
  $("auth-password").value = "synthetic-password";
  await fixture.submitAuth({ preventDefault() {} });
  await fixture.refreshState();
  select("user", "alice");
  assert.doesNotMatch($("message-records").textContent, /401 前消息/, "重新登录空快照不得恢复 401 前正文");
  assert.equal(posts.length, 1, "状态刷新和接收不得触发发送");
  console.log(JSON.stringify({ checks: ["GET 接收单聊及多发送者群聊，同编号隔离与重复轮询去重", "未读与清除、目标和草稿保持、换行及 HTML 文本安全", "丢弃提示、新后台会话、退出/401 清理及旧 GET", "入站消息不更改发送 ACK、不自动发送"], stateGets: getCount, network: "无socket，仅合成DOM/fetch" }, null, 2));
}
