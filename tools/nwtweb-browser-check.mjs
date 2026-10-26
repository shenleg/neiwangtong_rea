// 本文件用真实Chromium验证nwtweb页面：只允许回环网页，真实控制操作仅向合成loopback对端；聊天请求由浏览器内合成接口接管。
// 用法：PLAYWRIGHT_MODULE=/path/to/playwright node tools/nwtweb-browser-check.mjs http://127.0.0.1:18788 /tmp/screenshots
import assert from "node:assert/strict";
import { createRequire } from "node:module";
import { mkdir } from "node:fs/promises";
import path from "node:path";

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
  assert.match(liveState.remote, /^127\./, "真实控制测试不得面向非loopback对端");
  await page.getByRole("button", { name: "上线", exact: true }).click();
  await page.getByRole("button", { name: "下线", exact: true }).waitFor({ state: "visible" });
  await page.waitForFunction(() => !document.getElementById("stop-button").disabled);
  await page.getByRole("button", { name: "下线", exact: true }).click();
  await page.getByText("已离线", { exact: true }).waitFor();
  checks.push("实际HTTP：页面、明确上线、starting期间停止及正常回到离线");

  const state = {
    phase: "online", name: "Synthetic-Web", own_user_id: "synthetic-own", remote: "127.0.0.2:19011",
    started_at: "2026-10-25T00:00:00Z", error: "", stage: "protocol_ready", max_text_bytes: 4096,
    contacts: [
      { user_id: "synthetic-peer", name: "合成联系人", discovery: "127.0.0.2:19011", online: true, sendable: true, reason: "" },
      { user_id: "offline-peer", name: "离线联系人", discovery: "127.0.0.3:19011", online: false, sendable: false, reason: "当前仅连接一个对端" },
      { user_id: "html-name", name: "<img src=x onerror=alert(1)>", discovery: "127.0.0.4:19011", online: false, sendable: false, reason: "当前仅连接一个对端" },
    ],
    groups: [
      { group_id: "synthetic-group", name: "合成双成员群", members: ["synthetic-own", "synthetic-peer"], sendable: true, reason: "" },
      { group_id: "multi-group", name: "合成多成员群", members: ["synthetic-own", "synthetic-peer", "third-peer"], sendable: false, reason: "群包含当前连接以外的成员，尚不支持多对端群投递" },
    ],
    events: [{ stage: "protocol_ready", elapsed_ms: 50 }],
  };
  const posts = [];
  let stateGets = 0;
  let abortNext = false;
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
    return route.fulfill({ json: { request_id: payload.request_id,
      result: { command: request.url().endsWith("/group") ? 3011 : 1004, message_id: String(posts.length),
        submitted: true, transport_acknowledged: true, business_acknowledged: false, receipt_received: false, delivery: "unknown" }, error: "" } });
  });
  await page.reload();
  await page.getByText("已在线", { exact: true }).waitFor();
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
  await page.getByRole("button", { name: "合成多成员群，ID multi-group，不可发送", exact: true }).click();
  await editor.fill("多成员群不得提交");
  assert.equal(await page.getByRole("button", { name: "发送消息", exact: true }).isDisabled(), true);
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
  checks.push("群聊：完整目标、无法覆盖成员时禁发、每目标草稿隔离");

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

  state.discovery_mode = "subnet";
  state.remote = "127.0.0.0/8";
  state.contacts = [state.contacts[0],
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
  assert.equal(await page.evaluate(() => document.documentElement.scrollWidth <= window.innerWidth), true);
  await page.screenshot({ path: path.join(output, "mobile.png"), fullPage: true });
  assert.deepEqual(scriptErrors, []);
  checks.push("1440px/390px真实渲染，无横向溢出，无脚本异常");
  console.log(JSON.stringify({ checks, screenshots: output, messagePosts: posts.length }, null, 2));
} finally {
  await browser.close();
}
