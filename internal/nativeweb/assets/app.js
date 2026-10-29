// 本文件管理认证、状态轮询、服务端临时收发记录、网页已读状态及草稿；聊天仅由用户明确提交，失败不自动重发。
(() => {
  "use strict";

  const $ = (id) => document.getElementById(id);
  const encoder = new TextEncoder();
  const phases = { offline: "已离线", starting: "正在上线", online: "已在线", stopping: "正在下线", failed: "连接失败" };
  const drafts = new Map();
  const warnings = new Map();
  const records = [];
  const received = [];
  const unread = new Map();
  const readPending = new Set();
  let readError = "";
  let typingSnapshotAt = 0;
  let typingTimer = null;
  const recordLimit = 128;
  const byteLimit = 1024 * 1024;
  const sessionPending = new Set();
  let historyID = null;
  let receivedSession = null;
  let receivedCursor = 0;
  let authRevision = 0;
  let recordOrder = 0;
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
  let authenticated = false;
  let initialized = false;
  let authBusy = false;
  let keyConfigured = false;

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

  function typingRemaining(userID) {
    const status = state.typing.find((item) => item.user_id === userID);
    return status ? status.remaining_ms - (Date.now() - typingSnapshotAt) : 0;
  }

  function scheduleTypingExpiry() {
    window.clearTimeout(typingTimer);
    const remaining = state.typing.map((status) => typingRemaining(status.user_id)).filter((value) => value > 0);
    if (!remaining.length) return;
    // 越过到期毫秒再刷新，避免同一毫秒内剩余时间仍为正而重复排定定时器。
    typingTimer = window.setTimeout(() => {
      if (!state) return;
      renderDirectory();
      renderTarget();
      scheduleTypingExpiry();
    }, Math.min(...remaining) + 1);
  }

  function renderDirectory() {
    if (!state) return;
    const search = $("directory-search").value.toLocaleLowerCase();
    const signature = JSON.stringify([activeKind, search, selected, reachable, state.phase, state.contacts, state.groups, state.recent_conversations, state.typing.map((status) => [status.user_id, typingRemaining(status.user_id) > 0]), Array.from(unread)]);
    if (signature === directorySignature) return;
    directorySignature = signature;
    const focusedKey = $("target-list").contains(document.activeElement) ? document.activeElement.dataset.key : null;
    $("contact-count").textContent = state.contacts.length;
    $("group-count").textContent = state.groups.length;
    $("recent-count").textContent = state.recent_conversations.length;
    const list = activeKind === "recent" ? state.recent_conversations : activeKind === "user"
      ? state.contacts.map((item) => ({ kind: "user", id: item.user_id, name: item.name }))
      : state.groups.map((item) => ({ kind: "group", id: item.group_id, name: item.name }));
    const visible = list.filter((target) => `${target.name}\n${target.id}`.toLocaleLowerCase().includes(search));
    const fragment = document.createDocumentFragment();
    for (const target of visible) {
      const { kind, id, name } = target;
      const item = targetData(target);
      const group = kind === "group";
      const button = node("button", "target-button");
      button.type = "button";
      button.dataset.key = keyOf(target);
      button.setAttribute("aria-pressed", String(Boolean(selected && keyOf(selected) === keyOf(target))));
      const available = Boolean(item && item.sendable && (group || item.online));
      const typing = reachable && state.phase === "online" && typingRemaining(id) > 0;
      const label = !item ? "暂不可用" : group ? (item.sendable ? "可发送" : "不可发送") : (item.online ? (typing ? "正在输入…" : item.sendable ? "在线" : "不可发送") : "离线");
      const count = unread.get(keyOf(target)) || 0;
      button.setAttribute("aria-label", `${name}，ID ${id}，${label}${count ? `，${count} 条未读` : ""}`);
      const avatar = node("span", `target-avatar${group ? " group" : ""}`, group ? "群" : Array.from(name)[0] || "人");
      avatar.setAttribute("aria-hidden", "true");
      const info = node("span", "target-info");
      info.append(node("strong", "", name), node("small", "mono", activeKind === "recent" ? `${group ? "群聊" : "单聊"} · ${id}` : id));
      button.append(avatar, info, node("span", `target-state${available ? " available" : ""}`, label));
      if (count) button.append(node("span", "unread-count", String(count)));
      button.addEventListener("click", () => selectTarget(target));
      const row = node("li");
      row.append(button);
      fragment.append(row);
    }
    $("target-list").replaceChildren(fragment);
    $("directory-empty").hidden = visible.length > 0;
    $("directory-empty").textContent = search ? "没有匹配的名称或 ID" : activeKind === "recent" ? "还没有收发过消息" : (activeKind === "group" ? "尚无已同步的已加入群" : "联系人列表为空");
    $("directory-note").textContent = activeKind === "recent" ? "按最近收发排序；重新打开页面保留，服务重启清空。" : activeKind === "group" ? "仅列出已同步的已加入群，并非全部群。" : "离线联系人仍可浏览，暂不可发送。";
    if (focusedKey) {
      const button = Array.from($("target-list").querySelectorAll("button")).find((element) => element.dataset.key === focusedKey);
      if (button) button.focus({ preventScroll: true });
    }
  }

  function selectTarget(target) {
    selected = target;
    unread.delete(keyOf(target));
    for (const record of received) if (record.key === keyOf(target)) record.unread = false;
    void saveRead(target);
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
    $("typing-indicator").hidden = !(!group && item && item.online && reachable && state.phase === "online" && typingRemaining(selected.id) > 0);
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
    const pending = activeSend || records.find((record) => record.pending);
    $("byte-count").textContent = `${bytes} / ${limit} 字节`;
    $("byte-count").classList.toggle("over-limit", bytes > limit);
    $("message-text").disabled = !selected;
    $("message-text").readOnly = Boolean(activeSend && selected && activeSend.key === keyOf(selected));
    $("message-text").setAttribute("aria-invalid", String(bytes > limit));
    $("send-button").disabled = Boolean(reason || pending || !canAllocateID() || bytes === 0 || bytes > limit);
    $("send-label").textContent = pending ? "正在提交…" : "发送消息";
    $("composer").setAttribute("aria-busy", String(Boolean(pending)));
    const lastRecord = selected ? records.findLast((record) => record.key === keyOf(selected)) : null;
    let notice = reason || "仅提交一次；传输确认不代表送达或已读。";
    if (!reason && !text && lastRecord && onlineTransmissionConfirmed(lastRecord)) notice = confirmationSummary(lastRecord.result);
    if (!canAllocateID()) notice = "浏览器缺少 crypto.getRandomValues，无法生成安全请求 ID，请使用现代浏览器。";
    else if (pending) notice = "后台仍在处理已受理的发送，请勿重复提交。";
    else if (warning) notice = warning;
    else if (bytes > limit) notice = `正文超过 ${limit} 字节，请缩短后发送。`;
    $("send-notice").textContent = notice;
    $("send-notice").classList.toggle("warning", Boolean(warning && !pending) || bytes > limit || !canAllocateID());
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

  // 后台是收发记录的来源；按会话和请求标识合并，刷新不会重新提交聊天。
  function restoreSentSnapshot() {
    for (const message of state.sent_messages) {
      const session = sessionKey({ ...state, started_at: message.started_at });
      const existing = records.find((record) => record.requestID === message.response.request_id && record.session === session);
      if (existing && !existing.pending) continue;
      const value = { pending: message.pending, result: message.response.result, error: message.response.error };
      if (existing) {
        Object.assign(existing, value);
        continue;
      }
      const target = { kind: message.target_kind, id: message.target_id, name: message.target_name };
      records.push({ ...value, target, text: message.text, requestID: message.response.request_id, session,
        key: keyOf(target), at: new Date(message.sent_at), order: ++recordOrder });
    }
  }

  function renderReceivedNotice() {
    const dropped = state && state.received_dropped ? `后台已丢弃 ${state.received_dropped} 条接收消息，较早消息可能不可见。` : "";
    $("received-notice").textContent = [dropped, readError].filter(Boolean).join(" ");
    $("received-notice").hidden = !dropped && !readError;
  }

  function rebuildUnread() {
    unread.clear();
    for (const record of received) {
      record.unread = !record.read && (!selected || record.key !== keyOf(selected));
      if (record.unread) unread.set(record.key, (unread.get(record.key) || 0) + 1);
    }
  }

  // 只标记已经展示的序号，防止请求途中到达的新消息被误标；这是本地网页状态，不发协议回执。
  async function saveRead(target = selected) {
    if (!target || !state || document.visibilityState === "hidden") return;
    const key = keyOf(target);
    const message = received.findLast((record) => record.key === key && !record.read);
    if (!message) return;
    const session = receivedSession;
    const requestKey = `${session}:${message.sequence}`;
    if (readPending.has(requestKey)) return;
    readPending.add(requestKey);
    const revision = authRevision;
    const startedAt = state.started_at;
    try {
      const response = await api("/api/messages/read", { started_at: startedAt, sequence: String(message.sequence) });
      if (revision !== authRevision || session !== receivedSession) return;
      if (!response.ok) throw new Error();
      for (const record of received) if (record.key === key && record.sequence <= message.sequence) record.read = true;
      readError = "";
      rebuildUnread();
      renderDirectory();
    } catch {
      if (revision !== authRevision || session !== receivedSession) return;
      readError = "已读状态未保存，刷新后仍可能显示未读；请检查后台连接。";
    } finally {
      readPending.delete(requestKey);
      if (revision === authRevision) renderReceivedNotice();
    }
  }

  // 后台序列在进程内递增，只用于接收去重，与发送回执的 message_id 无关。
  function receiveSnapshot() {
    const current = sessionKey(state);
    if (receivedSession !== current) {
      received.length = 0;
      unread.clear();
      receivedCursor = 0;
      receivedSession = current;
    }
    for (const message of state.messages) {
      if (message.sequence <= receivedCursor) {
        const previous = received.find((record) => record.sequence === message.sequence);
        if (previous && message.read) previous.read = true;
        continue;
      }
      receivedCursor = message.sequence;
      const target = message.command === 3011 ? { kind: "group", id: message.group_id } : { kind: "user", id: message.sender_id };
      const key = keyOf(target);
      const isUnread = !message.read && (!selected || keyOf(selected) !== key);
      received.push({ ...message, key, unread: isUnread, order: ++recordOrder });
      if (isUnread) unread.set(key, (unread.get(key) || 0) + 1);
    }
    trimRecords();
    rebuildUnread();
    renderReceivedNotice();
  }

  // 两类记录共享数量和字节预算；淘汰未读消息时同步减少未读数。
  function trimRecords() {
    const size = () => encoder.encode(JSON.stringify([...records, ...received])).length;
    while (records.length + received.length > recordLimit || size() > byteLimit) {
      const firstSend = records.filter((record) => !record.pending).reduce((first, record) => !first || record.at < first.at ? record : first, null);
      const firstReceive = received[0];
      if (!firstSend && !firstReceive) break;
      if (firstReceive && (!firstSend || new Date(firstReceive.received_at) <= firstSend.at)) {
        received.shift();
        if (firstReceive.unread) {
          const count = unread.get(firstReceive.key) - 1;
          if (count) unread.set(firstReceive.key, count);
          else unread.delete(firstReceive.key);
        }
      } else records.splice(records.indexOf(firstSend), 1);
    }
  }

  function renderRecords() {
    const list = $("message-records");
    const scroller = $("records-scroll");
    const nearBottom = scroller.scrollHeight - scroller.scrollTop - scroller.clientHeight < 70;
    const openDetails = new Set(Array.from(list.querySelectorAll("details[open]")).map((detail) => detail.dataset.request));
    const visible = selected ? [...records, ...received].filter((record) => record.key === keyOf(selected)).sort((a, b) => new Date(a.received_at || a.at) - new Date(b.received_at || b.at) || a.order - b.order) : [];
    const fragment = document.createDocumentFragment();
    for (const record of visible) {
      if (record.sequence !== undefined) {
        const item = node("li", "message-record received");
        const heading = node("div", "record-heading");
        const at = new Date(record.received_at);
        const time = node("time", "", at.toLocaleString("zh-CN", { hour12: false }));
        time.dateTime = record.received_at;
        heading.append(node("strong", "", record.sender_name), time);
        const avatar = node("span", "record-avatar", Array.from(record.sender_name)[0] || "人");
        avatar.setAttribute("aria-hidden", "true");
        const content = node("div", "record-content");
        content.append(heading, node("div", "record-text", record.text));
        item.append(avatar, content);
        fragment.append(item);
        continue;
      }
      const item = node("li", "message-record sent");
      const avatar = node("span", "record-avatar own", "我");
      avatar.setAttribute("aria-hidden", "true");
      const content = node("div", "record-content");
      const heading = node("div", "record-heading");
      const time = node("time", "", record.at.toLocaleTimeString("zh-CN", { hour12: false }));
      time.dateTime = record.at.toISOString();
      heading.append(node("strong", "", "我"), time);
      const confirmed = onlineTransmissionConfirmed(record);
      const status = record.pending ? "正在提交 · 尚无确认" : (confirmed ? confirmationSummary(record.result) : "结果未知 · 送达状态未知");
      content.append(heading, node("div", "record-text", record.text), node("p", `record-status ${record.pending ? "pending" : confirmed ? "" : "unknown"}`, status));
      if (!record.pending && !confirmed) content.append(node("p", "record-warning", unknownNotice));
      if (record.error) content.append(node("p", "record-warning", record.error));
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
      content.append(details);
      item.append(avatar, content);
      fragment.append(item);
    }
    list.replaceChildren(fragment);
    $("records-empty").hidden = visible.length > 0;
    if (nearBottom) scroller.scrollTop = scroller.scrollHeight;
  }

  // 禁止跟随重定向，以免 307/308 在另一个地址再次提交同一 POST。
  async function api(path, payload, method = "POST") {
    const options = { credentials: "same-origin", cache: "no-store", redirect: "error", signal: AbortSignal.timeout(25000) };
    if (payload !== undefined) {
      options.method = method;
      options.headers = { "Content-Type": "application/json" };
      options.body = JSON.stringify(payload);
      if (path === "/api/messages/read") options.keepalive = true;
    }
    const revision = authRevision;
    const response = await fetch(path, options);
    if (revision === authRevision && response.status === 401 && (path.startsWith("/api/") || path === "/auth/api-key" || path === "/auth/logout")) {
      showAuth({ initialized, authenticated: false, api_key_configured: keyConfigured });
    }
    const data = response.status === 204 ? {} : await response.json();
    return { ok: response.ok, status: response.status, data };
  }

  function clearKey() {
    $("key-value").textContent = "";
    $("key-result").hidden = true;
  }

  // 认证失效后，旧请求即使随后返回也不能重新写入页面。
  function clearSensitive() {
    authenticated = false;
    authRevision += 1;
    sessionRevision += 1;
    state = null;
    reachable = false;
    selected = null;
    activeSend = null;
    records.length = 0;
    received.length = 0;
    unread.clear();
    drafts.clear();
    warnings.clear();
    sessionPending.clear();
    readPending.clear();
    readError = "";
    window.clearTimeout(typingTimer);
    typingTimer = null;
    $("typing-indicator").hidden = true;
    sessionStateUncertain = false;
    nameEdited = false;
    historyID = null;
    receivedSession = null;
    receivedCursor = 0;
    directorySignature = "";
    eventsSignature = "";
    clearKey();
    for (const id of ["message-text", "directory-search", "node-name", "auth-password", "auth-code"]) $(id).value = "";
    for (const id of ["target-list", "message-records", "stage-list"]) $(id).replaceChildren();
    for (const id of ["target-name", "target-kind", "target-id", "target-avatar", "target-reason", "target-availability", "session-name", "session-id", "session-remote", "session-started", "session-stage", "backend-error", "session-notice", "key-notice", "received-notice"]) $(id).textContent = "";
    $("target-name").textContent = "选择一位联系人";
    $("target-kind").textContent = "新的沟通，从这里开始";
    $("target-id").textContent = "或切换到已加入群";
    $("target-avatar").textContent = "↗";
    $("target-avatar").classList.toggle("group", false);
    $("target-availability").textContent = "未选择目标";
    $("target-reason").textContent = "请从左侧选择目标，再核对名称与 ID。";
    $("contact-count").textContent = "0";
    $("group-count").textContent = "0";
    $("recent-count").textContent = "0";
    $("discovery-label").textContent = "局域网发现 · 0 人在线";
    $("received-notice").hidden = true;
    $("records-empty").hidden = false;
    $("app-shell").hidden = true;
    renderComposer();
  }

  function authControls(busy) {
    authBusy = busy;
    for (const id of ["auth-submit", "auth-retry", "auth-logout", "key-create", "key-revoke"]) $(id).disabled = busy;
    $("key-revoke").disabled = busy || !keyConfigured;
  }

  function showAuth(status) {
    initialized = status.initialized;
    authenticated = status.authenticated;
    $("auth-screen").hidden = authenticated;
    $("app-shell").hidden = !authenticated;
    $("auth-form").hidden = authenticated;
    $("auth-retry").hidden = true;
    $("auth-title").textContent = initialized ? "密码登录" : "首次初始化";
    $("auth-description").textContent = initialized ? "输入密码以访问聊天页面。" : "输入初始化代码并设置登录密码。";
    $("auth-code-field").hidden = initialized;
    $("auth-password-hint").hidden = initialized;
    $("auth-code").required = !initialized;
    $("auth-submit").textContent = initialized ? "登录" : "初始化";
    keyConfigured = status.api_key_configured;
    $("key-status").textContent = status.api_key_configured ? "API Key 已配置。" : "尚未配置 API Key。";
    $("key-revoke").disabled = !status.api_key_configured;
    if (!authenticated) clearSensitive();
  }

  async function refreshAuth() {
    const revision = authRevision;
    try {
      const response = await api("/auth/status");
      if (revision !== authRevision) return;
      if (!response.ok) throw new Error();
      showAuth(response.data);
      $("auth-notice").textContent = "";
      if (authenticated) void refreshState();
    } catch {
      if (revision !== authRevision) return;
      clearSensitive();
      $("app-shell").hidden = true;
      $("auth-screen").hidden = false;
      $("auth-form").hidden = true;
      $("auth-title").textContent = "无法读取认证状态";
      $("auth-description").textContent = "请检查服务后重新读取。";
      $("auth-retry").hidden = false;
    }
  }

  async function submitAuth(event) {
    event.preventDefault();
    if (authBusy) return;
    const password = $("auth-password").value;
    if (!initialized && (Array.from(password).length < 8 || Array.from(password).length > 32 ||
        !/[a-zA-Z]/.test(password) || !/[0-9]/.test(password) ||
        !Array.from(password).some((c) => c >= "!" && c <= "~" && !/[a-zA-Z0-9]/.test(c)))) {
      $("auth-notice").textContent = "设置密码须为 8～32 个字符，包含英文字母、数字和 ASCII 特殊字符。";
      return;
    }
    const payload = initialized ? { password } : { code: $("auth-code").value, password };
    // 请求提交后立即清空输入，错误信息不使用后台内容，避免回显密码或初始化代码。
    $("auth-password").value = "";
    $("auth-code").value = "";
    authControls(true);
    $("auth-notice").textContent = "";
    try {
      const response = await api(initialized ? "/auth/login" : "/auth/setup", payload);
      if (!response.ok) throw new Error();
      await refreshAuth();
    } catch {
      $("auth-notice").textContent = "操作未成功，请检查输入或服务状态后重试。";
    } finally {
      authControls(false);
    }
  }

  async function changeAuth(action) {
    if (authBusy || !authenticated) return;
    authControls(true);
    clearKey();
    $("key-notice").textContent = "";
    if (action === "logout") showAuth({ initialized, authenticated: false, api_key_configured: keyConfigured });
    const revision = authRevision;
    try {
      const response = await api(action === "logout" ? "/auth/logout" : "/auth/api-key", {}, action === "revoke" ? "DELETE" : "POST");
      if (revision !== authRevision) return;
      if (!response.ok) throw new Error();
      if (action === "create") {
        keyConfigured = true;
        $("key-value").textContent = response.data.api_key;
        $("key-result").hidden = false;
        $("key-status").textContent = "API Key 已配置。";
      } else if (action === "revoke") {
        keyConfigured = false;
        $("key-status").textContent = "尚未配置 API Key。";
        $("key-notice").textContent = "API Key 已撤销。";
      }
    } catch {
      if (revision !== authRevision) return;
      if (action === "logout") $("auth-notice").textContent = "退出结果未确认，请重新登录；未自动重试。";
      else $("key-notice").textContent = "操作结果未确认，请刷新页面检查状态；未自动重试。";
    } finally {
      authControls(false);
    }
  }

  function sessionNotice(text, warning = false) {
    $("session-notice").textContent = text;
    $("session-notice").classList.toggle("warning", warning);
  }

  function refreshState() {
    if (!authenticated) return Promise.resolve();
    if (refreshPending) return refreshPending;
    $("refresh-button").disabled = true;
    const revision = sessionRevision;
    refreshPending = (async () => {
      try {
        const requestedAt = Date.now();
        const response = await api("/api/state");
        // 操作前发出的 GET 不得覆盖操作后的状态；操作期间的快照也不能解除发送限制。
        if (revision !== sessionRevision) return;
        if (!response.ok) throw new Error(response.data.error || `HTTP ${response.status}`);
        const recovered = !reachable;
        state = response.data;
        typingSnapshotAt = requestedAt;
        scheduleTypingExpiry();
        if (historyID !== state.history_id) {
          records.length = 0;
          received.length = 0;
          receivedCursor = 0;
          unread.clear();
          historyID = state.history_id;
        }
        restoreSentSnapshot();
        receiveSnapshot();
        // 正文只保存在受预算限制的收发记录中，不重复留在状态快照。
        state.messages = [];
        state.sent_messages = [];
        void saveRead();
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
        renderRecords();
      } catch (error) {
        if (revision !== sessionRevision) return;
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
        if (revision !== sessionRevision && authenticated) void refreshState();
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
    const revision = authRevision;
    sessionNotice(`已提交${label}请求，请等待后台状态更新。`);
    try {
      const response = await api(`/api/session/${action}`, payload);
      if (revision !== authRevision) return;
      if (!response.ok || response.data.error) throw new Error(response.data.error || `HTTP ${response.status}`);
      if (action === "start") {
        state.name = response.data.name;
        nameEdited = false;
      }
      sessionNotice(`${label}请求已返回，以后台当前状态为准。`);
    } catch (error) {
      if (revision !== authRevision) return;
      sessionNotice(`${label}请求未确认：${error.message}。未自动重试，请刷新状态确认。`, true);
    } finally {
      if (revision !== authRevision) return;
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
    const record = { target, text, requestID, key: keyOf(target), session: sessionKey(state), at: new Date(), order: ++recordOrder, pending: true, error: "", result: null };
    const revision = authRevision;
    activeSend = record;
    records.push(record);
    trimRecords();
    warnings.delete(record.key);
    renderComposer();
    renderRecords();
    const payload = { request_id: requestID, text };
    payload[target.kind === "group" ? "group_id" : "user_id"] = target.id;
    try {
      const response = await api(`/api/messages/${target.kind}`, payload);
      if (revision !== authRevision) return;
      record.result = response.data.result || null;
      if (!response.ok || response.data.error) record.error = response.data.error || `HTTP ${response.status}`;
      if (!record.result && !record.error) record.error = "后台未返回发送结果。";
    } catch (error) {
      if (revision !== authRevision) return;
      record.error = `请求未确认：${error.message}。此请求未自动重发。`;
    } finally {
      if (revision !== authRevision) return;
      record.pending = false;
      activeSend = null;
      trimRecords();
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

  $("auth-form").addEventListener("submit", (event) => void submitAuth(event));
  $("auth-retry").addEventListener("click", () => void refreshAuth());
  $("auth-logout").addEventListener("click", () => void changeAuth("logout"));
  $("key-create").addEventListener("click", () => void changeAuth("create"));
  $("key-revoke").addEventListener("click", () => void changeAuth("revoke"));
  $("key-dismiss").addEventListener("click", clearKey);
  $("directory-search").addEventListener("input", renderDirectory);
  for (const tab of document.querySelectorAll('[role="tab"]')) {
    tab.addEventListener("click", () => switchTab(tab.dataset.kind));
    tab.addEventListener("keydown", (event) => {
      if (!["ArrowLeft", "ArrowRight", "Home", "End"].includes(event.key)) return;
      event.preventDefault();
      const kinds = Array.from(document.querySelectorAll('[role="tab"]'), (item) => item.dataset.kind);
      const index = kinds.indexOf(activeKind);
      const next = event.key === "Home" ? 0 : event.key === "End" ? kinds.length - 1 : (index + (event.key === "ArrowRight" ? 1 : -1) + kinds.length) % kinds.length;
      switchTab(kinds[next], true);
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
  void refreshAuth();
  // 定时读取状态，并保存当前已展示消息的已读标记；不会自动上线、下线或重发聊天。
  window.setInterval(() => void refreshState(), 2000);
})();
