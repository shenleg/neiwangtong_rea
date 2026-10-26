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
    events: [{ stage: "protocol_ready", elapsed_ms: 50 }],
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
  const context = { document, TextEncoder, AbortSignal, window: { crypto: webcrypto },
    fetch: async (url, options) => {
      assert.equal(options.method, "POST");
      assert.equal(options.redirect, "error");
      posts.push({ url, payload: JSON.parse(options.body) });
      return { ok: response.ok, status: response.status, json: async () => structuredClone(response.data) };
    } };
  const source = await readFile(new URL("../internal/nativeweb/assets/app.js", import.meta.url), "utf8");
  const boundary = source.indexOf('  $("directory-search").addEventListener');
  assert.ok(boundary > 0, "必须执行真实页面逻辑，不能误取空源码");
  runInNewContext(source.slice(0, boundary) + `
    refreshState = () => Promise.resolve();
    globalThis.fixture = {
      records,
      prepare(mode = "ready") {
        state = { phase: "online", started_at: "synthetic", own_user_id: "synthetic-own", remote: "127.0.0.0/8", max_text_bytes: 4096,
          contacts: [{ user_id: "synthetic-peer", name: "合成联系人", online: mode !== "offline", sendable: mode !== "offline" },
            { user_id: mode === "ready" ? "third-peer" : "offline-peer", name: "合成群成员", online: mode === "ready", sendable: mode === "ready" }],
          groups: [{ group_id: "multi-group", name: "合成多成员群", members: ["synthetic-own", "synthetic-peer", mode === "ready" ? "third-peer" : "offline-peer"],
            sendable: mode !== "offline", reason: mode === "offline" ? "没有可用的READY群成员" : "" }], events: [] };
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
  console.log(JSON.stringify({ checks: ["多成员群全部传输确认", "在线加离线群HTTP200清稿并显示skipped及原因，整群ACK仍false", "全部离线禁用且无POST", "HTTP502及实际路由失败保留逐成员结果与草稿", "单peer及同号单聊事件不修改群聚合结果", "1004传输/1005业务ACK/1006回执按阶段回填单聊", "事件刷新不重发POST"], messagePosts: posts.length, network: "无socket，仅合成DOM/fetch" }, null, 2));
}
