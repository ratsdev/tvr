/* —— Channels —— */
const POLICY_HINT = {
  fixed: "Use the selected URL. Reconnect that one.",
  random: "Pick one when a session starts. Stay on it while anyone is watching.",
  failover: "Try in order. On failure, continue to the next.",
};

function channelSearchHaystack(ch) {
  const urls = (ch.upstreams || []).map((u) => u.url);
  return [ch.name, ch.upstream_url, ...urls];
}

function filteredChannels() {
  return state.channels.filter((c) => matches(state.filter.channels, ...channelSearchHaystack(c)));
}

function channelListSub(ch) {
  const n = (ch.upstreams || []).length;
  const live = state.channelStatuses.find((s) => s.channel_id === ch.id);
  const liveHost = live && live.state && live.state !== "idle" ? live.upstream_host : "";
  const host = liveHost || shortHost(ch.upstream_url);
  return n > 1 ? `${host} · ${n}` : host;
}

function channelTestTitle(st) {
  return st === "ok" ? "Test OK" : st === "fail" ? "Test failed" : "Testing…";
}

function channelTestDot(id) {
  const st = state.channelTest[id];
  if (!st) return "";
  const title = channelTestTitle(st);
  return `<span class="status-dot ${st}" title="${title}" aria-label="${title}"></span>`;
}

function channelStatusBadge(channelID) {
  const st = state.channelStatuses.find((s) => s.channel_id === channelID)?.state;
  if (!st || st === "idle") return "";
  return `<span class="badge ${esc(st)}">${esc(st)}</span>`;
}

function paintChannelStatuses() {
  document.querySelectorAll("#channel-list [data-channel-status]").forEach((el) => {
    el.innerHTML = channelStatusBadge(el.dataset.channelStatus);
  });
  document.querySelectorAll("#channel-list [data-channel-sub]").forEach((el) => {
    const ch = state.channels.find((c) => c.id === el.dataset.channelSub);
    if (ch) el.textContent = channelListSub(ch);
  });
}

async function refreshChannelStatuses() {
  state.channelStatuses = await api("/api/relay/status");
  paintChannelStatuses();
}

function updateTestAllButton() {
  const testAllBtn = document.getElementById("btn-test-all-channels");
  if (!testAllBtn) return;
  testAllBtn.disabled = state.testingAllChannels || !state.channels.length;
  testAllBtn.textContent = state.testingAllChannels ? "Testing…" : "Test All";
}

function updateChannelTestIndicator(id) {
  const trail = document.querySelector(`#channel-list [data-open-channel="${id}"] .item-trail`);
  if (!trail) return;
  const st = state.channelTest[id];
  let dot = trail.querySelector(".status-dot");
  if (!st) {
    dot?.remove();
    return;
  }
  const title = channelTestTitle(st);
  if (!dot) {
    trail.insertAdjacentHTML("afterbegin", channelTestDot(id));
    return;
  }
  dot.className = `status-dot ${st}`;
  dot.title = title;
  dot.setAttribute("aria-label", title);
}

function syncChannelSelectionDOM() {
  document.querySelectorAll("#channel-list input[data-select-channel]").forEach((cb) => {
    cb.checked = state.selected.channels.has(cb.dataset.selectChannel);
  });
  updateBulkBar("channels", "channel-bulk", "channel-bulk-count");
}

function renderChannelList() {
  pruneSelection("channels", new Set(state.channels.map((c) => c.id)));
  const items = filteredChannels();
  document.getElementById("channel-count").textContent = `Channels (${state.channels.length})`;
  const list = document.getElementById("channel-list");
  updateTestAllButton();
  if (!items.length) {
    list.innerHTML = `<div class="empty-list">${state.channels.length ? "No matches" : "No Channels yet"}</div>`;
  } else {
    list.innerHTML = items.map((ch) => {
      const active = !state.creatingChannel && state.selectedChannelId === ch.id ? "active" : "";
      const checked = state.selected.channels.has(ch.id) ? "checked" : "";
      return `<div class="master-item ${active}" data-open-channel="${ch.id}">
        <input type="checkbox" data-select-channel="${ch.id}" ${checked} />
        <div class="item-main">
          ${logoHTML(ch.logo_url)}
          <div class="min">
            <div class="title">${esc(ch.name)}</div>
            <div class="sub" data-channel-sub="${ch.id}">${esc(channelListSub(ch))}</div>
          </div>
        </div>
        <div class="item-trail">
          <span data-channel-status="${ch.id}">${channelStatusBadge(ch.id)}</span>
          ${channelTestDot(ch.id)}
          <span class="meta-badge">${esc(ch.relay_count || 0)}</span>
        </div>
      </div>`;
    }).join("");
  }
  updateBulkBar("channels", "channel-bulk", "channel-bulk-count");
}

async function testChannelById(id) {
  state.channelTest[id] = "testing";
  updateChannelTestIndicator(id);
  try {
    const res = await api(`/api/channels/${id}/test`, { method: "POST", body: "{}" });
    state.channelTest[id] = res.ok ? "ok" : "fail";
    return res;
  } catch (err) {
    state.channelTest[id] = "fail";
    throw err;
  } finally {
    updateChannelTestIndicator(id);
  }
}

async function runPool(items, concurrency, fn) {
  let i = 0;
  const workers = Array.from({ length: Math.min(concurrency, items.length) }, async () => {
    while (i < items.length) {
      const item = items[i++];
      await fn(item);
    }
  });
  await Promise.all(workers);
}

function normalizeOverlay(raw) {
  if (!raw || typeof raw !== "object" || Array.isArray(raw)) return {};
  const out = {};
  for (const [k, v] of Object.entries(raw)) {
    if (String(k).trim()) out[k] = v;
  }
  return out;
}

function parseHeadersField(raw, errEl) {
  const text = (raw || "").trim();
  if (!text) return {};
  try {
    const parsed = JSON.parse(text);
    if (!parsed || typeof parsed !== "object" || Array.isArray(parsed)) {
      if (errEl) errEl.textContent = "Headers must be a JSON object";
      return null;
    }
    return parsed;
  } catch {
    if (errEl) errEl.textContent = "Headers must be valid JSON";
    return null;
  }
}

function updatePolicyHint() {
  const policy = document.getElementById("channel-policy").value;
  document.getElementById("channel-policy-hint").textContent = POLICY_HINT[policy] || "";
  document.querySelectorAll(".upstream-row").forEach((row) => {
    row.classList.toggle("fixed-off", policy !== "fixed");
  });
}

function newUpstreamID() {
  if (typeof crypto !== "undefined" && crypto.randomUUID) return crypto.randomUUID();
  return "u-" + Math.random().toString(16).slice(2) + Date.now().toString(16);
}

function ensureUpstreamRow(u = {}) {
  return {
    id: String(u.id || "").trim() || newUpstreamID(),
    url: u.url || "",
    proxy_id: String(u.proxy_id || "").trim(),
    headers: u.headers || {},
  };
}

function proxySelectHTML(selectedId) {
  const id = String(selectedId || "");
  const opts = [`<option value="">Direct</option>`];
  const seen = new Set();
  for (const p of state.proxies || []) {
    seen.add(p.id);
    opts.push(`<option value="${esc(p.id)}" ${p.id === id ? "selected" : ""}>${esc(p.name)}</option>`);
  }
  if (id && !seen.has(id)) {
    opts.push(`<option value="${esc(id)}" selected>${esc(id)}</option>`);
  }
  return `<select class="upstream-proxy" data-upstream-proxy>${opts.join("")}</select>`;
}

function upstreamPlaceholder(proxyId) {
  return proxyId ? "host:port" : "http(s)://…mpegts or .m3u8";
}

function persistedUpstreamIDs() {
  if (state.creatingChannel) return new Set();
  const id = document.getElementById("channel-id").value;
  const ch = state.channels.find((c) => c.id === id);
  return new Set((ch?.upstreams || []).map((u) => u.id).filter(Boolean));
}

function renderChannelUpstreams(rows, { policy = "fixed", fixedId = "", openHeaders = {} } = {}) {
  const list = document.getElementById("channel-upstreams");
  const persisted = persistedUpstreamIDs();
  rows = (rows && rows.length) ? rows.map(ensureUpstreamRow) : [ensureUpstreamRow({})];
  let selected = rows.findIndex((u) => u.id === fixedId);
  if (selected < 0) selected = 0;
  list.innerHTML = rows.map((u, i) => {
    const id = u.id || "";
    const overlay = normalizeOverlay(u.headers);
    const headersOpen = !!openHeaders[i] || !!openHeaders[id];
    const checked = policy === "fixed" && i === selected;
    const canRemove = rows.length > 1;
    return `<div class="upstream-row ${policy === "fixed" ? "" : "fixed-off"}" data-upstream-idx="${i}">
      <div class="upstream-main">
        <input class="upstream-pick" type="radio" name="channel-fixed-up" value="${i}" ${checked ? "checked" : ""} title="Use this URL" />
        ${proxySelectHTML(u.proxy_id)}
        <input type="text" data-upstream-url required placeholder="${esc(upstreamPlaceholder(u.proxy_id))}" value="${esc(u.url || "")}" />
        <input type="hidden" data-upstream-id value="${esc(id)}" />
        <div class="upstream-actions">
          <button type="button" class="icon-btn ghost" data-up-move="-1" title="Move up" aria-label="Move up" ${i === 0 ? "disabled" : ""}>↑</button>
          <button type="button" class="icon-btn ghost" data-up-move="1" title="Move down" aria-label="Move down" ${i === rows.length - 1 ? "disabled" : ""}>↓</button>
          <button type="button" class="small ghost${headersOpen ? " active" : ""}" data-up-headers title="Per-URL headers">Headers</button>
          ${persisted.has(id) ? `<button type="button" class="small ghost" data-up-test="${esc(id)}">Test</button>` : ""}
          <button type="button" class="icon-btn ghost danger" data-up-remove title="Remove" aria-label="Remove" ${canRemove ? "" : "disabled"}>×</button>
        </div>
      </div>
      <textarea class="${headersOpen ? "" : "hidden"}" data-upstream-headers rows="2" placeholder='{"Cookie":"…"}'>${esc(JSON.stringify(overlay))}</textarea>
    </div>`;
  }).join("");
}

function readUpstreamRows() {
  return [...document.querySelectorAll("#channel-upstreams .upstream-row")].map((row) => {
    let headers = {};
    const raw = row.querySelector("[data-upstream-headers]")?.value || "";
    try {
      const parsed = raw.trim() ? JSON.parse(raw) : {};
      if (parsed && typeof parsed === "object" && !Array.isArray(parsed)) headers = normalizeOverlay(parsed);
    } catch { /* draft may be invalid until save */ }
    return {
      id: row.querySelector("[data-upstream-id]")?.value || "",
      url: row.querySelector("[data-upstream-url]")?.value || "",
      proxy_id: row.querySelector("[data-upstream-proxy]")?.value || "",
      headers,
    };
  });
}

function readOpenHeaders() {
  const open = {};
  document.querySelectorAll("#channel-upstreams .upstream-row").forEach((row, i) => {
    const ta = row.querySelector("[data-upstream-headers]");
    if (ta && !ta.classList.contains("hidden")) open[i] = true;
  });
  return open;
}

function refreshUpstreamList(mutate) {
  const policy = document.getElementById("channel-policy").value;
  const rows = readUpstreamRows();
  const open = readOpenHeaders();
  const radio = document.querySelector("input[name='channel-fixed-up']:checked");
  const fixedId = radio ? (rows[Number(radio.value)]?.id || "") : "";
  mutate(rows);
  renderChannelUpstreams(rows, { policy, fixedId, openHeaders: open });
  updatePolicyHint();
}

function readChannelDraft() {
  const rows = readUpstreamRows();
  const radio = document.querySelector("input[name='channel-fixed-up']:checked");
  const fixedId = radio ? (rows[Number(radio.value)]?.id || "") : (rows[0]?.id || "");
  return {
    name: document.getElementById("channel-name").value,
    logo_url: document.getElementById("channel-logo").value,
    upstream_policy: document.getElementById("channel-policy").value,
    fixed_upstream_id: fixedId,
    upstreams: rows.map((u) => ({ id: u.id, url: u.url, proxy_id: u.proxy_id || "", headers: normalizeOverlay(u.headers) })),
    headers: document.getElementById("channel-headers").value,
    transcode_enabled: document.getElementById("channel-transcode").checked,
  };
}

function isChannelDirty() {
  return isDomainDirty(state.editors.channel.baseline, readChannelDraft());
}

function fillChannelForm(ch) {
  document.getElementById("channel-detail-title").textContent = state.creatingChannel ? "New Channel" : ch.name;
  document.getElementById("channel-id").value = ch?.id || "";
  document.getElementById("channel-name").value = ch?.name || "";
  document.getElementById("channel-logo").value = ch?.logo_url || "";
  document.getElementById("channel-policy").value = ch?.upstream_policy || "fixed";
  document.getElementById("channel-transcode").checked = !!ch?.transcode_enabled;
  document.getElementById("channel-headers").value = JSON.stringify(ch?.headers || {}, null, 2);
  const rows = (ch?.upstreams && ch.upstreams.length)
    ? ch.upstreams.map(ensureUpstreamRow)
    : [ensureUpstreamRow({ url: ch?.upstream_url || "" })];
  renderChannelUpstreams(rows, { policy: ch?.upstream_policy || "fixed", fixedId: ch?.fixed_upstream_id || "" });
  updatePolicyHint();
  setChannelDetailLogo(ch?.logo_url || "");
  document.getElementById("channel-error").textContent = "";
  state.editors.channel.baseline = readChannelDraft();
}

function updateChannelMeta(ch) {
  const meta = document.getElementById("channel-meta");
  if (!ch) {
    meta.textContent = "";
  } else if (ch.relay_slugs?.length) {
    meta.textContent = `Used by: ${ch.relay_slugs.join(", ")}`;
  } else {
    meta.textContent = "Used by: none";
  }
  const creating = state.creatingChannel;
  document.getElementById("btn-test-channel").classList.toggle("hidden", creating);
  document.getElementById("btn-add-channel-to-relay").classList.toggle("hidden", creating);
  document.getElementById("btn-del-channel").classList.toggle("hidden", creating);
  const linkRow = document.getElementById("channel-link-row");
  const copyBtn = document.getElementById("btn-copy-channel-stream");
  const url = ch?.id ? channelStreamURL(ch.id) : "";
  linkRow.classList.toggle("hidden", !url);
  copyBtn.dataset.url = url;
}

function showChannelDetail({ forceFill = false } = {}) {
  const empty = document.getElementById("channel-detail-empty");
  const body = document.getElementById("channel-detail-body");
  const ch = state.creatingChannel
    ? null
    : state.channels.find((c) => c.id === state.selectedChannelId) || null;
  if (!state.creatingChannel && !ch) {
    empty.classList.remove("hidden");
    body.classList.add("hidden");
    setDetailOpen("channels", false);
    state.editors.channel.baseline = null;
    return;
  }
  empty.classList.add("hidden");
  body.classList.remove("hidden");
  setDetailOpen("channels", true);
  updateChannelMeta(ch);
  const fill = shouldFillEditor({
    activeEntityId: state.creatingChannel ? "new" : state.selectedChannelId,
    responseEntityId: state.creatingChannel ? "new" : ch?.id,
    domainDirty: isChannelDirty(),
    force: forceFill || state.editors.channel.baseline == null,
  });
  if (fill) fillChannelForm(ch);
  else if (!state.creatingChannel && ch) {
    document.getElementById("channel-detail-title").textContent = ch.name;
  }
}

async function loadChannels() {
  const gen = ++state.gens.channels;
  const channels = await api("/api/channels");
  if (!isCurrentGeneration(gen, state.gens.channels)) return;
  state.channels = channels;
  if (state.selectedChannelId && !state.channels.some((c) => c.id === state.selectedChannelId)) {
    state.selectedChannelId = null;
    state.editors.channel.baseline = null;
  }
  renderChannelList();
  showChannelDetail({ forceFill: false });
}

async function applyChannelSave(saved) {
  state.creatingChannel = false;
  state.selectedChannelId = saved.id;
  await loadChannels();
  showChannelDetail({ forceFill: true });
}

function applyChannelDeletes(ids) {
  const cleared = editorClearedByDeletes(state.selectedChannelId, ids);
  state.channels = removeByIds(state.channels, ids);
  for (const id of ids) {
    state.selected.channels.delete(String(id));
    delete state.channelTest[id];
  }
  if (cleared) {
    state.selectedChannelId = null;
    state.editors.channel.baseline = null;
  }
  renderChannelList();
  if (cleared) showChannelDetail({ forceFill: true });
}

function selectChannel(id) {
  state.creatingChannel = false;
  state.selectedChannelId = id;
  renderChannelList();
  showChannelDetail({ forceFill: true });
}

function wireChannels() {
  document.getElementById("channel-search").addEventListener("input", (e) => {
    state.filter.channels = e.target.value;
    renderChannelList();
  });
  document.getElementById("channel-logo").addEventListener("input", (e) => {
    setChannelDetailLogo(e.target.value);
  });

  document.getElementById("btn-new-channel").addEventListener("click", () => {
    state.creatingChannel = true;
    state.selectedChannelId = null;
    renderChannelList();
    showChannelDetail({ forceFill: true });
  });
  document.getElementById("channel-list").addEventListener("click", (e) => {
    const t = e.target;
    if (!(t instanceof HTMLElement)) return;
    if (t.dataset.selectChannel) return;
    const item = t.closest("[data-open-channel]");
    if (item) selectChannel(item.dataset.openChannel);
  });
  document.getElementById("channel-list").addEventListener("change", (e) => {
    const t = e.target;
    if (!(t instanceof HTMLInputElement) || !t.dataset.selectChannel) return;
    const id = t.dataset.selectChannel;
    if (t.checked) state.selected.channels.add(id);
    else state.selected.channels.delete(id);
    syncChannelSelectionDOM();
  });
  document.getElementById("btn-channel-select-all").addEventListener("click", () => {
    filteredChannels().forEach((c) => state.selected.channels.add(c.id));
    syncChannelSelectionDOM();
  });
  document.getElementById("btn-clear-channels").addEventListener("click", () => {
    state.selected.channels.clear();
    syncChannelSelectionDOM();
  });
  document.getElementById("btn-del-channels").addEventListener("click", async () => {
    const ids = [...state.selected.channels];
    const res = await bulkDelete("channel(s)", ids, (id) => api(`/api/channels/${id}`, { method: "DELETE" }));
    if (!res) return;
    applyChannelDeletes(res.successfulIDs);
  });
  document.getElementById("channel-policy").addEventListener("change", updatePolicyHint);
  document.getElementById("btn-add-upstream").addEventListener("click", () => {
    refreshUpstreamList((rows) => { rows.push(ensureUpstreamRow({})); });
  });
  document.getElementById("channel-upstreams").addEventListener("change", (e) => {
    const t = e.target;
    if (!(t instanceof HTMLSelectElement) || !t.dataset.upstreamProxy) return;
    const input = t.closest(".upstream-row")?.querySelector("[data-upstream-url]");
    if (input) input.placeholder = upstreamPlaceholder(t.value);
  });
  document.getElementById("channel-upstreams").addEventListener("click", async (e) => {
    const t = e.target;
    if (!(t instanceof HTMLElement)) return;
    const row = t.closest(".upstream-row");
    if (!row) return;
    const idx = Number(row.dataset.upstreamIdx);
    if (t.dataset.upMove) {
      const dir = Number(t.dataset.upMove);
      refreshUpstreamList((rows) => {
        const j = idx + dir;
        if (j < 0 || j >= rows.length) return;
        [rows[idx], rows[j]] = [rows[j], rows[idx]];
      });
      return;
    }
    if (t.dataset.upRemove !== undefined) {
      refreshUpstreamList((rows) => {
        if (rows.length < 2) return;
        rows.splice(idx, 1);
      });
      return;
    }
    if (t.dataset.upHeaders !== undefined) {
      const ta = row.querySelector("[data-upstream-headers]");
      ta.classList.toggle("hidden");
      t.classList.toggle("active", !ta.classList.contains("hidden"));
      return;
    }
    if (t.dataset.upTest) {
      const id = document.getElementById("channel-id").value;
      if (!id) return;
      const loading = toast.loading("Testing upstream…");
      try {
        const res = await api(`/api/channels/${id}/test`, {
          method: "POST",
          body: JSON.stringify({ upstream_id: t.dataset.upTest }),
        });
        if (res.ok) {
          loading.update("success", "Upstream test OK", `HTTP ${res.status_code}, ${res.bytes_read} bytes, sync=${res.has_sync}, hls=${res.looks_hls}${res.upstream_host ? `, ${res.upstream_host}` : ""}`);
        } else {
          loading.update("error", "Upstream test failed", String(res.error || res.status_code));
        }
      } catch (err) { loading.update("error", "Upstream test failed", err.message); }
    }
  });
  document.getElementById("channel-form").addEventListener("submit", async (e) => {
    e.preventDefault();
    const errEl = document.getElementById("channel-error");
    errEl.textContent = "";
    const headers = parseHeadersField(document.getElementById("channel-headers").value, errEl);
    if (headers == null) return;
    const rows = [];
    for (const row of document.querySelectorAll("#channel-upstreams .upstream-row")) {
      const overlay = parseHeadersField(row.querySelector("[data-upstream-headers]")?.value || "", errEl);
      if (overlay == null) return;
      rows.push({
        id: row.querySelector("[data-upstream-id]")?.value || "",
        url: row.querySelector("[data-upstream-url]")?.value.trim() || "",
        proxy_id: row.querySelector("[data-upstream-proxy]")?.value || "",
        headers: normalizeOverlay(overlay),
      });
    }
    if (!rows.length) {
      errEl.textContent = "At least one upstream is required";
      return;
    }
    const radio = document.querySelector("input[name='channel-fixed-up']:checked");
    const fixedId = radio ? (rows[Number(radio.value)]?.id || "") : (rows[0]?.id || "");
    const body = {
      name: document.getElementById("channel-name").value,
      logo_url: document.getElementById("channel-logo").value,
      upstream_policy: document.getElementById("channel-policy").value,
      fixed_upstream_id: fixedId,
      upstreams: rows.map((u) => {
        const item = { url: u.url, headers: u.headers };
        if (u.id) item.id = u.id;
        if (u.proxy_id) item.proxy_id = u.proxy_id;
        return item;
      }),
      headers,
      transcode_enabled: document.getElementById("channel-transcode").checked,
    };
    const id = document.getElementById("channel-id").value;
    try {
      const saved = id
        ? await api(`/api/channels/${id}`, { method: "PUT", body: JSON.stringify(body) })
        : await api("/api/channels", { method: "POST", body: JSON.stringify(body) });
      await applyChannelSave(saved);
      toast.success(id ? "Channel updated" : "Channel created");
    } catch (err) { errEl.textContent = err.message; toast.error("Save failed", err.message); }
  });
  document.getElementById("btn-del-channel").addEventListener("click", async () => {
    const id = state.selectedChannelId;
    if (!id || !confirm("Delete this Channel?")) return;
    try {
      await api(`/api/channels/${id}`, { method: "DELETE" });
      applyChannelDeletes([id]);
      toast.success("Channel deleted");
    } catch (err) { toast.error("Delete failed", err.message); }
  });
  document.getElementById("btn-add-channel-to-relay").addEventListener("click", async () => {
    if (!state.selectedChannelId) return;
    await openMemberDialog(null, state.selectedChannelId);
  });
  document.getElementById("btn-test-channel").addEventListener("click", async () => {
    const id = state.selectedChannelId;
    if (!id) return;
    const loading = toast.loading("Testing channel…");
    try {
      const res = await testChannelById(id);
      if (res.ok) {
        loading.update("success", "Channel test OK", `HTTP ${res.status_code}, ${res.bytes_read} bytes, sync=${res.has_sync}, hls=${res.looks_hls}${res.upstream_host ? `, ${res.upstream_host}` : ""}`);
      } else {
        loading.update("error", "Channel test failed", String(res.error || res.status_code));
      }
    } catch (err) { loading.update("error", "Channel test failed", err.message); }
  });

  document.getElementById("btn-copy-channel-stream").addEventListener("click", () => {
    const url = document.getElementById("btn-copy-channel-stream").dataset.url;
    copyText("Stream", url);
  });

  document.getElementById("btn-test-all-channels").addEventListener("click", async () => {
    if (state.testingAllChannels || !state.channels.length) return;
    state.testingAllChannels = true;
    const ids = state.channels.map((c) => c.id);
    for (const id of ids) state.channelTest[id] = "testing";
    updateTestAllButton();
    for (const id of ids) updateChannelTestIndicator(id);
    const loading = toast.loading("Testing all channels…", `0 / ${ids.length}`);
    let done = 0;
    let ok = 0;
    let fail = 0;
    try {
      await runPool(ids, 4, async (id) => {
        try {
          const res = await testChannelById(id);
          if (res.ok) ok++;
          else fail++;
        } catch {
          fail++;
        }
        done++;
        loading.update("loading", "Testing all channels…", `${done} / ${ids.length} · ${ok} ok · ${fail} failed`, 0);
      });
      loading.update(
        fail ? "error" : "success",
        fail ? "Channel tests finished" : "All channels OK",
        `${ok} ok · ${fail} failed`,
        7000,
      );
    } finally {
      state.testingAllChannels = false;
      updateTestAllButton();
    }
  });

  document.getElementById("btn-import-channels").addEventListener("click", () => {
    document.getElementById("import-channels-content").value = "";
    document.getElementById("import-channels-file").value = "";
    document.getElementById("import-channels-filename").textContent = "No file selected";
    document.getElementById("import-channels-error").textContent = "";
    document.getElementById("import-channels-dialog").showModal();
  });
  document.getElementById("import-channels-cancel").addEventListener("click", () => {
    document.getElementById("import-channels-dialog").close();
  });
  onFilePick(
    document.getElementById("import-channels-pick"),
    document.getElementById("import-channels-file"),
    async (file) => {
      const nameEl = document.getElementById("import-channels-filename");
      const errEl = document.getElementById("import-channels-error");
      try {
        const text = await file.text();
        nameEl.textContent = file.name;
        document.getElementById("import-channels-content").value = text;
        errEl.textContent = "";
      } catch (err) {
        nameEl.textContent = "No file selected";
        errEl.textContent = err.message || "failed to read file";
      }
    },
  );
  document.getElementById("import-channels-save").addEventListener("click", async (e) => {
    e.preventDefault();
    const errEl = document.getElementById("import-channels-error");
    const saveBtn = document.getElementById("import-channels-save");
    errEl.textContent = "";
    saveBtn.disabled = true;
    const loading = toast.loading("Importing Channels…");
    try {
      const res = await api("/api/channels/import", {
        method: "POST",
        body: JSON.stringify({ content: document.getElementById("import-channels-content").value }),
      });
      document.getElementById("import-channels-dialog").close();
      await loadChannels();
      const parts = [
        `${res.channels_created} created`,
        `${res.channels_reused} reused`,
        `${res.upstreams_added} added`,
      ];
      loading.update("success", "Channels imported", parts.join(", "));
    } catch (err) {
      loading.update("error", "Import failed", err.message);
      errEl.textContent = err.message;
    } finally {
      saveBtn.disabled = false;
    }
  });

}
