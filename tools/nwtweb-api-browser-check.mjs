// 本文件通过真实Chrome验收本地合成后台的Swagger渲染、无鉴权及Try it out；只由Go测试启动，不访问真实客户端。
// 用法：PLAYWRIGHT_MODULE=/absolute/playwright NWT_BROWSER_NO_SANDBOX=1 node tools/nwtweb-api-browser-check.mjs http://127.0.0.1:port /tmp/screenshots
import assert from "node:assert/strict";
import { createRequire } from "node:module";
import { mkdir } from "node:fs/promises";
import path from "node:path";

const { chromium } = createRequire(import.meta.url)(process.env.PLAYWRIGHT_MODULE || "playwright");
const base = new URL(process.argv[2]);
assert.equal(base.hostname, "127.0.0.1", "仅允许Go测试的回环合成后台");
const output = path.resolve(process.argv[3]);
await mkdir(output, { recursive: true });
const browser = await chromium.launch({ channel: "chrome", headless: true, chromiumSandbox: process.env.NWT_BROWSER_NO_SANDBOX !== "1" });
try {
  const page = await browser.newPage({ viewport: { width: 1440, height: 1000 } });
  const errors = [];
  const requests = [];
  const external = [];
  page.on("pageerror", (error) => errors.push(error.message));
  page.on("request", (request) => requests.push({ method: request.method(), url: request.url(), headers: request.headers() }));
  await page.route("**/*", async (route) => {
    if (new URL(route.request().url()).origin !== base.origin) {
      external.push(route.request().url());
      return route.abort();
    }
    return route.continue();
  });
  await page.goto(new URL("/docs", base).href);
  await page.getByRole("heading", { name: "内网通独立原生消息网关" }).waitFor();
  assert.equal(await page.getByRole("button", { name: "Authorize", exact: true }).count(), 0);
  assert.equal(requests.filter((request) => request.method === "POST").length, 0);
  await page.locator(".opblock-tag").filter({ hasText: "公开接口" }).click();

  // operation展开指定path的Swagger操作，返回其卡片；路径缺失或控件不可交互时Playwright抛错。
  async function operation(apiPath) {
    const card = page.locator(".opblock").filter({ has: page.getByText(apiPath, { exact: true }) });
    await card.locator(".opblock-summary-control").click();
    await card.getByRole("button", { name: "Try it out", exact: true }).click();
    return card;
  }

  const initialStateResponse = await page.request.get(new URL("/api/state", base).href);
  const initialState = await initialStateResponse.json();
  assert.equal(initialState.own_user_id, "synthetic-own", "此脚本只允许Go测试的合成后台");
  assert.match(initialState.remote, /^127\./, "禁止对真实网络后台执行浏览器测试");

  const health = await operation("/health");
  const healthResponse = page.waitForResponse((response) => response.url() === new URL("/health", base).href);
  await health.getByRole("button", { name: "Execute", exact: true }).click();
  const healthResult = await healthResponse;
  assert.equal(healthResult.status(), 200);
  assert.equal((await healthResult.json()).status, "ready");

  const user = await operation("/send/user");
  await user.locator("textarea").fill(JSON.stringify({ name: "合成联系人", text: "Swagger 合成单聊\n第二行🙂" }));
  const userResponse = page.waitForResponse((response) => response.url() === new URL("/send/user", base).href && response.request().method() === "POST");
  await user.getByRole("button", { name: "Execute", exact: true }).click();
  const userResult = await userResponse;
  assert.equal(userResult.status(), 200);
  const userBody = await userResult.json();
  assert.equal(userBody.command, 1004);
  assert.equal(userBody.submitted, true);
  assert.equal(userBody.delivery, "unknown");
  assert.equal(userBody.result, undefined);
  assert.equal(typeof userBody.request_id, "string");
  assert.equal(userResult.request().headers().authorization, undefined);
  assert.equal(userResult.request().headers()["x-nwt-request"], undefined);

  const group = await operation("/send/group");
  await group.locator("textarea").fill(JSON.stringify({ group_id: "group-two", text: "Swagger 合成群聊\r\n第二行" }));
  const groupResponse = page.waitForResponse((response) => response.url() === new URL("/send/group", base).href && response.request().method() === "POST");
  await group.getByRole("button", { name: "Execute", exact: true }).click();
  const groupResult = await groupResponse;
  assert.equal(groupResult.status(), 200);
  assert.equal((await groupResult.json()).command, 3011);
  assert.equal(requests.filter((request) => request.method === "POST").length, 2);

  const contacts = await page.request.get(new URL("/contacts?name=" + encodeURIComponent("合成联系人"), base).href);
  assert.equal(contacts.status(), 200);
  assert.equal((await contacts.json()).status, "unique");
  const missing = await page.request.get(new URL("/contacts?name=" + encodeURIComponent("不存在"), base).href);
  assert.equal(missing.status(), 404);
  await page.screenshot({ path: path.join(output, "swagger-desktop.png"), fullPage: true });

  await page.goto(new URL("/swagger/index.html", base).href);
  await page.getByRole("heading", { name: "内网通独立原生消息网关" }).waitFor();
  await page.setViewportSize({ width: 390, height: 844 });
  assert.equal(await page.evaluate(() => document.documentElement.scrollWidth <= window.innerWidth), true);
  await page.screenshot({ path: path.join(output, "swagger-mobile.png"), fullPage: true });
  await page.getByRole("link", { name: "返回聊天页面", exact: true }).click();
  await page.getByRole("heading", { name: /内网通/ }).waitFor();
  await page.getByRole("link", { name: "Swagger API 文档", exact: true }).waitFor();
  const nameInput = page.getByRole("textbox", { name: "上线名称", exact: true });
  await page.waitForFunction(() => document.getElementById("phase-label").textContent === "已在线");
  assert.equal(await nameInput.isDisabled(), true);
  await page.getByRole("button", { name: "下线", exact: true }).click();
  await page.waitForFunction(() => !document.getElementById("node-name").disabled);
  const chosenName = "  浏览器自定义网关 <&>🙂  ";
  await nameInput.fill(chosenName);
  await page.getByRole("button", { name: /刷新状态/ }).click();
  await page.waitForFunction(() => !document.getElementById("refresh-button").disabled);
  assert.equal(await nameInput.inputValue(), chosenName);
  await nameInput.fill("   ");
  assert.equal(await page.getByRole("button", { name: "上线", exact: true }).isDisabled(), true);
  await nameInput.fill(chosenName);
  const startResponse = page.waitForResponse((response) => response.url() === new URL("/api/session/start", base).href && response.request().method() === "POST");
  await page.getByRole("button", { name: "上线", exact: true }).click();
  const started = await startResponse;
  assert.equal(started.status(), 202);
  assert.equal(started.request().postDataJSON().name, chosenName);
  await page.waitForFunction((name) => document.getElementById("session-name").textContent === name && document.getElementById("node-name").disabled, chosenName);
  assert.equal(await page.locator("#session-id").textContent(), "synthetic-own");
  await page.reload();
  await page.waitForFunction((name) => document.getElementById("node-name").value === name, chosenName);
  assert.equal(await nameInput.isDisabled(), true);
  assert.equal(await page.evaluate(() => document.documentElement.scrollWidth <= window.innerWidth), true);
  await page.screenshot({ path: path.join(output, "custom-name-mobile.png"), fullPage: true });
  assert.deepEqual(errors, []);
  assert.deepEqual(external, []);
  assert.equal(requests.filter((request) => request.method === "POST").length, 4);
  console.log(JSON.stringify({ verified: ["真实Swagger UI渲染", "无鉴权及无Authorize按钮", "页面加载不提交POST", "Try it out健康检查", "合成单聊及群聊各一次", "原生扁平响应及unknown", "名称查询", "离线同源资源", "手机布局与聊天导航", "离线自定义名称及刷新不丢草稿", "纯空白名称禁用上线", "完整名称提交、身份不变及在线锁定", "页面重载显示已用名称"], screenshots: output }, null, 2));
} finally {
  await browser.close();
}
