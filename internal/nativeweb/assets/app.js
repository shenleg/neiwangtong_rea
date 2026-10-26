// 本文件管理同源状态轮询、每目标草稿及单次发送记录；POST 只由用户明确操作触发，失败不自动重发。
(() => {
  "use strict";

  const $ = (id) => document.getElementById(id);
  const encoder = new TextEncoder();
  const phases = { offline: "已离线", starting: "正在上线", online: "已在线", stopping: "正在下线", failed: "连接失败" };
  const drafts = new Map();
  const warnings = new Map();
  const records = [];
  const sessionPending = new Set();
  const unknownNotice = "结果未知，请先确认，勿重复发送";
  let state = null;
  let reachable = false;
  let selected = null;
  let activeKind = "user";
  let activeSend = null;
  let refreshPending = null;
  let directorySignature = "";
  let eventsSignature = "";
  let sessionRevision = 0;
  let sessionStateUncertain = false;
  let nameEdited = false;

  const keyOf = (target) => `${target.kind}:${target.id}`;
  // message_id 只在所属后台会话内关联，避免下次上线后相同编号误改旧记录。
  const sessionKey = (value) => JSON.stringify([value.started_at, value.own_user_id, value.remote]);
  const canAllocateID = () => typeof window.crypto === "object" && typeof window.crypto.getRandomValues === "function";
  // 局域网HTTP不提供randomUUID；getRandomValues仍提供安全随机数，不退回Math.random。
  const allocateID = () => Array.from(window.crypto.getRandomValues(new Uint8Array(16)), (value) => value.toString(16).padStart(2, "0")).join("");

  // 跳过成员不算路由失败，也不能当作整群确认；仅已选路由全部确认时允许清除草稿。
  function onlineTransmissionConfirmed(record) {
    if (record.pending || record.error || !record.result) return false;
    if (record.result.command !== 3011) return record.result.transport_acknowledged;
    const recipients = record.result.recipients.filter((recipient) => !recipient.skipped);
    return recipients.length > 0 && recipients.every((recipient) => recipient.submitted && recipient.transport_acknowledged && !recipient.error);
  }

  function confirmationSummary(result) {
    const skipped = result.command === 3011 ? result.recipients.filter((recipient) => recipient.skipped).length : 0;
    return skipped ? `在线成员传输已确认，另有${skipped}名成员未投递；不自动补发，送达状态未知。` : "传输已确认 · 送达状态未知";
  }

  function node(tag, className = "", text = "") {
    const element = document.createElement(tag);
    element.className = className;
    element.textContent = text;
    return element;
  }

  function targetData(target = selected) {
    if (!target || !state) return null;
    return target.kind === "user"
      ? state.contacts.find((item) => item.user_id === target.id)
      : state.groups.find((item) => item.group_id === target.id);
  }

  function blockedReason(target = selected) {
    if (!target) return "请先选择目标。";
    const item = targetData(target);
    if (!item) return "当前后台列表中已无此目标，请重新选择。";
    if (!reachable) return "无法确认后台状态，已暂停发送。请先刷新状态。";
    if (sessionStateUncertain) return "上线或下线操作后正在重新确认后台状态，请稍候。";
    if (state.phase !== "online") return "后台尚未在线，暂不可发送。";
    if (sessionPending.size) return "后台正在处理上线或下线请求，请稍候。";
    if (target.kind === "user" && !item.online) return item.reason || "该联系人离线，仅可浏览。";
    if (!item.sendable) return item.reason || "后台检查未通过，该目标暂不可发送。";
    return "";
  }

  function textLimit() {
    return state ? Math.min(4096, state.max_text_bytes) : 4096;
  }

  function renderDirectory() {
    if (!state) return;
    const search = $("directory-search").value.toLocaleLowerCase();
    const signature = JSON.stringify([activeKind, search, selected, state.contacts, state.groups]);
    if (signature === directorySignature) return;
    directorySignature = signature;
    const focusedKey = $("target-list").contains(document.activeElement) ? document.activeElement.dataset.key : null;
    $("contact-count").textContent = state.contacts.length;
    $("group-count").textContent = state.groups.length;
    const list = activeKind === "user" ? state.contacts : state.groups;
    const visible = list.filter((item) => `${item.name}\n${activeKind === "user" ? item.user_id : item.group_id}`.toLocaleLowerCase().includes(search));
    const fragment = document.createDocumentFragment();
    for (const item of visible) {
      const id = activeKind === "user" ? item.user_id : item.group_id;
      const target = { kind: activeKind, id, name: item.name };
      const button = node("button", "target-button");
      button.type = "button";
      button.dataset.key = keyOf(target);
      button.setAttribute("aria-pressed", String(Boolean(selected && keyOf(selected) === keyOf(target))));
      const available = item.sendable && (activeKind === "group" || item.online);
      const label = activeKind === "user" ? (item.online ? (item.sendable ? "在线" : "不可发送") : "离线") : (item.sendable ? "可发送" : "不可发送");
      button.setAttribute("aria-label", `${item.name}，ID ${id}，${label}`);
      const avatar = node("span", `target-avatar${activeKind === "group" ? " group" : ""}`, activeKind === "group" ? "群" : Array.from(item.name)[0] || "人");
      avatar.setAttribute("aria-hidden", "true");
      const info = node("span", "target-info");
      info.append(node("strong", "", item.name), node("small", "mono", id));
      button.append(avatar, info, node("span", `target-state${available ? " available" : ""}`, label));
      button.addEventListener("click", () => selectTarget(target));
      const row = node("li");
      row.append(button);
      fragment.append(row);
    }
    $("target-list").replaceChildren(fragment);
    $("directory-empty").hidden = visible.length > 0;
    $("directory-empty").textContent = search ? "没有匹配的名称或 ID" : (activeKind === "group" ? "尚无已同步的已加入群" : "联系人列表为空");
    $("directory-note").textContent = activeKind === "group" ? "仅列出已同步的已加入群，并非全部群。" : "离线联系人仍可浏览，暂不可发送。";
    if (focusedKey) {
      const button = Array.from($("target-list").querySelectorAll("button")).find((element) => element.dataset.key === focusedKey);
      if (button) button.focus({ preventScroll: true });
    }
  }

  function selectTarget(target) {
    selected = target;
    $("message-text").value = drafts.get(keyOf(target)) || "";
    renderDirectory();
    renderTarget();
    renderComposer();
    renderRecords();
  }

  function renderTarget() {
    if (!selected) return;
    const item = targetData();
    const name = item ? item.name : selected.name;
    const group = selected.kind === "group";
    $("target-name").textContent = name;
    $("target-kind").textContent = group ? "已加入群 / GROUP" : "联系人 / CONTACT";
    $("target-id").textContent = `${group ? "Group ID" : "User ID"} · ${selected.id}`;
    $("target-avatar").textContent = group ? "群" : Array.from(name)[0] || "人";
    $("target-avatar").classList.toggle("group", group);
    const reason = blockedReason();
    $("target-availability").textContent = reason ? "暂不可发送" : "可发送";
    $("target-availability").classList.toggle("available", !reason);
    if (!item) {
      $("target-reason").textContent = reason;
    } else if (group) {
      $("target-reason").textContent = `成员条目 ${item.members.length} · ${item.reason || reason || "向在线就绪成员投递；离线或未就绪成员列为未投递，不自动补发。"} 成员完整性以后台检查为准。`;
    } else {
      $("target-reason").textContent = `${item.discovery} · ${item.reason || reason || "当前联系人在线，可向该联系人发送。"}`;
    }
  }

  function renderComposer() {
    const text = $("message-text").value;
    const bytes = encoder.encode(text).length;
    const limit = textLimit();
    const warning = selected ? warnings.get(keyOf(selected)) : "";
    const reason = blockedReason();
    $("byte-count").textContent = `${bytes} / ${limit} 字节`;
    $("byte-count").classList.toggle("over-limit", bytes > limit);
    $("message-text").disabled = !selected;
    $("message-text").readOnly = Boolean(activeSend && selected && activeSend.key === keyOf(selected));
    $("message-text").setAttribute("aria-invalid", String(bytes > limit));
    $("send-button").disabled = Boolean(reason || activeSend || !canAllocateID() || bytes === 0 || bytes > limit);
    $("send-label").textContent = activeSend ? "正在提交…" : "发送消息";
    $("composer").setAttribute("aria-busy", String(Boolean(activeSend)));
    const lastRecord = selected ? records.findLast((record) => record.key === keyOf(selected)) : null;
    let notice = reason || "仅提交一次；传输确认不代表送达或已读。";
    if (!reason && !text && lastRecord && onlineTransmissionConfirmed(lastRecord)) notice = confirmationSummary(lastRecord.result);
    if (!canAllocateID()) notice = "浏览器缺少 crypto.getRandomValues，无法生成安全请求 ID，请使用现代浏览器。";
    else if (activeSend) notice = "正在提交固定目标与正文，请勿重复操作。";
    else if (warning) notice = warning;
    else if (bytes > limit) notice = `正文超过 ${limit} 字节，请缩短后发送。`;
    $("send-notice").textContent = notice;
    $("send-notice").classList.toggle("warning", Boolean(warning && !activeSend) || bytes > limit || !canAllocateID());
  }

  function renderSession() {
    if (!state) return;
    const onlineCount = state.contacts.filter((contact) => contact.online).length;
    const discovering = state.phase === "online" && onlineCount === 0;
    $("phase-badge").dataset.phase = reachable ? (discovering ? "starting" : state.phase) : "failed";
    $("phase-label").textContent = reachable ? (discovering ? "等待客户端" : phases[state.phase]) : "后台不可达";
    $("discovery-label").textContent = `局域网发现 · ${onlineCount} 人在线`;
    $("session-name").textContent = state.name || "—";
    $("session-id").textContent = state.own_user_id || "—";
    $("session-remote").textContent = state.remote || "—";
    $("session-started").textContent = state.started_at ? new Date(state.started_at).toLocaleString("zh-CN", { hour12: false }) : "未启动";
    $("session-stage").textContent = state.stage || "—";
    const idle = ["offline", "failed"].includes(state.phase);
    const editingAllowed = reachable && idle && !sessionStateUncertain && sessionPending.size === 0;
    const nameInput = $("node-name");
    if (!nameEdited || !idle) {
      nameInput.value = state.name;
      nameEdited = false;
    }
    nameInput.disabled = !editingAllowed;
    const invalidName = nameInput.value.trim().length === 0 || /[\p{Cc}]/u.test(nameInput.value);
    nameInput.setAttribute("aria-invalid", String(invalidName));
    $("start-button").disabled = !editingAllowed || invalidName;
    // starting 期间也允许显式停止；不因尚未返回的 start 请求锁住停止按钮。
    $("stop-button").disabled = sessionPending.has("stop") || !["starting", "online"].includes(state.phase);
    $("start-button").textContent = sessionPending.has("start") ? "请求上线中…" : "上线";
    $("stop-button").textContent = sessionPending.has("stop") ? "请求下线中…" : "下线";
    $("backend-error").textContent = state.error;
    $("backend-error").hidden = !state.error;
  }

  function renderEvents() {
    const signature = JSON.stringify([sessionKey(state), state.events]);
    if (signature === eventsSignature) return;
    eventsSignature = signature;
    const fragment = document.createDocumentFragment();
    for (const event of state.events.slice().reverse()) {
      const item = node("li");
      item.append(node("span", "stage-name mono", event.stage));
      const parts = [`+${(event.elapsed_ms / 1000).toFixed(2)}s`];
      if (event.command) parts.push(`命令 ${event.command}`);
      if (event.message_id) parts.push(`消息 ${event.message_id}`);
      if (event.peer_index) parts.push(`连接 ${event.peer_index}`);
      if (event.error) parts.push(event.error);
      item.append(node("span", "stage-meta mono", parts.join(" · ")));
      fragment.append(item);
    }
    $("stage-list").replaceChildren(fragment);
    $("events-empty").hidden = state.events.length > 0;
  }

  function applyAcknowledgements() {
    let changed = false;
    const commands = { transport_acknowledged: 1004, business_acknowledged: 1005, receipt_received: 1006 };
    for (const record of records) {
      // 群结果是所有路由的聚合事实，单个peer的3011事件不能确认整群。
      if (!record.result || record.result.command !== 1004 || record.session !== sessionKey(state) || !record.result.message_id) continue;
      for (const event of state.events) {
        const command = commands[event.stage];
        if (!command || event.command !== command || event.message_id !== record.result.message_id) continue;
        if (!record.result[event.stage]) {
          record.result[event.stage] = true;
          changed = true;
        }
      }
    }
    if (changed) renderRecords();
  }

  function renderRecords() {
    const list = $("message-records");
    const scroller = $("records-scroll");
    const nearBottom = scroller.scrollHeight - scroller.scrollTop - scroller.clientHeight < 70;
    const openDetails = new Set(Array.from(list.querySelectorAll("details[open]")).map((detail) => detail.dataset.request));
    const visible = selected ? records.filter((record) => record.key === keyOf(selected)) : [];
    const fragment = document.createDocumentFragment();
    for (const record of visible) {
      const item = node("li", "message-record");
      const heading = node("div", "record-heading");
      const time = node("time", "", record.at.toLocaleTimeString("zh-CN", { hour12: false }));
      time.dateTime = record.at.toISOString();
      heading.append(node("strong", "", `发给 ${record.target.name}`), time);
      const confirmed = onlineTransmissionConfirmed(record);
      const status = record.pending ? "正在提交 · 尚无确认" : (confirmed ? confirmationSummary(record.result) : "结果未知 · 送达状态未知");
      item.append(heading, node("div", "record-text", record.text), node("p", `record-status ${record.pending ? "pending" : confirmed ? "" : "unknown"}`, status));
      if (!record.pending && !confirmed) item.append(node("p", "record-warning", unknownNotice));
      if (record.error) item.append(node("p", "record-warning", record.error));
      const details = node("details", "record-details");
      details.dataset.request = record.requestID;
      details.open = openDetails.has(record.requestID);
      details.append(node("summary", "", "请求与确认详情"));
      const lines = node("div", "record-detail-lines");
      lines.append(node("p", "mono", `Request ID · ${record.requestID}`), node("p", "mono", `${record.target.kind === "group" ? "Group" : "User"} ID · ${record.target.id}`));
      if (record.result) {
        const result = record.result;
        lines.append(node("p", "mono", `命令 ${result.command} · Message ID ${result.message_id || "未返回"}`));
        lines.append(node("p", "", `已提交：${result.submitted ? "是" : "未确认"} · 传输确认：${result.transport_acknowledged ? "有" : "未确认"}`));
        lines.append(node("p", "", `业务 ACK：${result.business_acknowledged ? "有" : "未确认"} · 协议回执：${result.receipt_received ? "有" : "未确认"}`));
        if (result.recipients) {
          lines.append(node("p", "", "逐成员结果（跳过成员未投递，不离线存储、不自动补发；全部成员确认才表示整群传输已确认）："));
          for (const recipient of result.recipients) {
            lines.append(node("p", "mono recipient-result", `成员 ${recipient.user_id} · 连接 ${recipient.peer_index} · skipped：${recipient.skipped ? "true（未投递）" : "false"} · 已提交：${recipient.submitted ? "是" : "否"} · 传输 ACK：${recipient.transport_acknowledged ? "有" : "未确认"} · 原因/错误：${recipient.error || "无"}`));
          }
          if (!confirmed && result.recipients.some((recipient) => recipient.submitted)) lines.append(node("p", "record-warning", "已有成员提交，请先确认逐成员结果，勿向整群重复发送。"));
        }
      } else {
        lines.append(node("p", "", "后台尚未返回结果，不能判断是否提交。"));
      }
      lines.append(node("p", "", "送达状态：未知。业务 ACK 与协议回执均不作为送达或已读证明。"));
      details.append(lines);
      item.append(details);
      fragment.append(item);
    }
    list.replaceChildren(fragment);
    $("records-empty").hidden = visible.length > 0;
    if (nearBottom) scroller.scrollTop = scroller.scrollHeight;
  }

  // 禁止跟随重定向，以免 307/308 在另一个地址再次提交同一 POST。
  async function api(path, payload) {
    const options = { credentials: "same-origin", cache: "no-store", redirect: "error", signal: AbortSignal.timeout(25000) };
    if (payload !== undefined) {
      options.method = "POST";
      options.headers = { "Content-Type": "application/json" };
      options.body = JSON.stringify(payload);
    }
    const response = await fetch(path, options);
    const data = await response.json();
    return { ok: response.ok, status: response.status, data };
  }

  function sessionNotice(text, warning = false) {
    $("session-notice").textContent = text;
    $("session-notice").classList.toggle("warning", warning);
  }

  function refreshState() {
    if (refreshPending) return refreshPending;
    $("refresh-button").disabled = true;
    const revision = sessionRevision;
    refreshPending = (async () => {
      try {
        const response = await api("/api/state");
        // 操作前发出的 GET 不得覆盖操作后的状态；操作期间的快照也不能解除发送限制。
        if (revision !== sessionRevision) return;
        if (!response.ok) throw new Error(response.data.error || `HTTP ${response.status}`);
        const recovered = !reachable;
        state = response.data;
        reachable = true;
        if (!sessionPending.size) sessionStateUncertain = false;
        $("last-refresh").textContent = `${new Date().toLocaleTimeString("zh-CN", { hour12: false })} 更新`;
        if (recovered) sessionNotice("状态已读取，每 2 秒更新。页面关闭不自动下线。");
        renderDirectory();
        renderSession();
        renderTarget();
        renderComposer();
        renderEvents();
        applyAcknowledgements();
      } catch (error) {
        reachable = false;
        sessionNotice(`无法读取后台状态：${error.message}。仅自动重试状态读取，不重发任何操作。`, true);
        $("phase-badge").dataset.phase = "failed";
        $("phase-label").textContent = "后台不可达";
        $("last-refresh").textContent = "读取失败";
        if (!state) $("directory-empty").textContent = "后台不可达，请刷新状态";
        renderSession();
        renderTarget();
        renderComposer();
      } finally {
        refreshPending = null;
        $("refresh-button").disabled = false;
      }
    })();
    return refreshPending;
  }

  async function changeSession(action) {
    if ($(action === "start" ? "start-button" : "stop-button").disabled) return;
    const payload = action === "start" ? { name: $("node-name").value } : {};
    sessionPending.add(action);
    sessionStateUncertain = true;
    sessionRevision += 1;
    renderSession();
    renderComposer();
    const label = action === "start" ? "上线" : "下线";
    sessionNotice(`已提交${label}请求，请等待后台状态更新。`);
    try {
      const response = await api(`/api/session/${action}`, payload);
      if (!response.ok || response.data.error) throw new Error(response.data.error || `HTTP ${response.status}`);
      if (action === "start") {
        state.name = response.data.name;
        nameEdited = false;
      }
      sessionNotice(`${label}请求已返回，以后台当前状态为准。`);
    } catch (error) {
      sessionNotice(`${label}请求未确认：${error.message}。未自动重试，请刷新状态确认。`, true);
    } finally {
      sessionPending.delete(action);
      sessionStateUncertain = true;
      sessionRevision += 1;
      renderSession();
      renderComposer();
      void refreshState();
    }
  }

  async function sendMessage(event) {
    event.preventDefault();
    renderComposer();
    if ($("send-button").disabled) return;
    // 在第一个 await 之前固定目标、正文和 ID，并锁住全页发送，避免切换目标或重复点击改变请求。
    const target = { ...selected, name: targetData().name };
    const text = $("message-text").value;
    const requestID = allocateID();
    const record = { target, text, requestID, key: keyOf(target), session: sessionKey(state), at: new Date(), pending: true, error: "", result: null };
    activeSend = record;
    records.push(record);
    warnings.delete(record.key);
    renderComposer();
    renderRecords();
    const payload = { request_id: requestID, text };
    payload[target.kind === "group" ? "group_id" : "user_id"] = target.id;
    try {
      const response = await api(`/api/messages/${target.kind}`, payload);
      record.result = response.data.result || null;
      if (!response.ok || response.data.error) record.error = response.data.error || `HTTP ${response.status}`;
      if (!record.result && !record.error) record.error = "后台未返回发送结果。";
    } catch (error) {
      record.error = `请求未确认：${error.message}。此请求未自动重发。`;
    } finally {
      record.pending = false;
      activeSend = null;
      const confirmed = onlineTransmissionConfirmed(record);
      if (confirmed) {
        // 只清除本次已确认的原始草稿，不碰其他目标或随后编辑的新正文。
        if (drafts.get(record.key) === text) drafts.delete(record.key);
        if (selected && keyOf(selected) === record.key && $("message-text").value === text) $("message-text").value = "";
      } else {
        warnings.set(record.key, unknownNotice);
      }
      if (state) applyAcknowledgements();
      renderRecords();
      renderComposer();
      void refreshState();
    }
  }

  function switchTab(kind, focus = false) {
    activeKind = kind;
    for (const tab of document.querySelectorAll('[role="tab"]')) {
      const active = tab.dataset.kind === kind;
      tab.setAttribute("aria-selected", String(active));
      tab.tabIndex = active ? 0 : -1;
      if (active) {
        $("directory-panel").setAttribute("aria-labelledby", tab.id);
        if (focus) tab.focus();
      }
    }
    renderDirectory();
  }

  $("directory-search").addEventListener("input", renderDirectory);
  for (const tab of document.querySelectorAll('[role="tab"]')) {
    tab.addEventListener("click", () => switchTab(tab.dataset.kind));
    tab.addEventListener("keydown", (event) => {
      if (!["ArrowLeft", "ArrowRight", "Home", "End"].includes(event.key)) return;
      event.preventDefault();
      const kind = event.key === "Home" ? "user" : event.key === "End" ? "group" : activeKind === "user" ? "group" : "user";
      switchTab(kind, true);
    });
  }
  $("message-text").addEventListener("input", () => {
    if (selected) drafts.set(keyOf(selected), $("message-text").value);
    renderComposer();
  });
  $("composer").addEventListener("submit", sendMessage);
  $("node-name").addEventListener("input", () => {
    nameEdited = true;
    renderSession();
  });
  $("start-button").addEventListener("click", () => void changeSession("start"));
  $("stop-button").addEventListener("click", () => void changeSession("stop"));
  $("refresh-button").addEventListener("click", () => void refreshState());
  renderComposer();
  void refreshState();
  // 只有 GET 定时刷新。没有 visibilitychange、pagehide、beforeunload 或自动下线逻辑。
  window.setInterval(() => void refreshState(), 2000);
})();
