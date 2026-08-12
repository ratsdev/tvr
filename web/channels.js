/* —— Channels —— */
function filteredChannels() {
  // Preserve API/natural-sort order from the server.
  return state.channels.filter((c) => matches(state.filter.channels, c.name, c.upstream_url));
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
    list.innerHTML = `<div class="empty-list">${state.channels.length ? "No matches" : "No channels yet"}</div>`;
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
            <div class="sub">${esc(shortHost(ch.upstream_url))}</div>
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

function readChannelDraft() {
  return {
    name: document.getElementById("channel-name").value,
    logo_url: document.getElementById("channel-logo").value,
    upstream_url: document.getElementById("channel-url").value,
    headers: document.getElementById("channel-headers").value,
    transcode_enabled: document.getElementById("channel-transcode").checked,
  };
}

function isChannelDirty() {
  return isDomainDirty(state.editors.channel.baseline, readChannelDraft());
}

function fillChannelForm(ch) {
  document.getElementById("channel-detail-title").textContent = state.creatingChannel ? "New channel" : ch.name;
  document.getElementById("channel-id").value = ch?.id || "";
  document.getElementById("channel-name").value = ch?.name || "";
  document.getElementById("channel-logo").value = ch?.logo_url || "";
  document.getElementById("channel-url").value = ch?.upstream_url || "";
  document.getElementById("channel-transcode").checked = !!ch?.transcode_enabled;
  document.getElementById("channel-headers").value = JSON.stringify(ch?.headers || {}, null, 2);
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
  document.getElementById("btn-test-channel").classList.toggle("hidden", state.creatingChannel);
  document.getElementById("btn-del-channel").classList.toggle("hidden", state.creatingChannel);
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
  document.getElementById("channel-form").addEventListener("submit", async (e) => {
    e.preventDefault();
    const errEl = document.getElementById("channel-error");
    errEl.textContent = "";
    let headers = {};
    const raw = document.getElementById("channel-headers").value.trim();
    if (raw) {
      try { headers = JSON.parse(raw); }
      catch { errEl.textContent = "Headers must be valid JSON"; return; }
    }
    const body = {
      name: document.getElementById("channel-name").value,
      logo_url: document.getElementById("channel-logo").value,
      upstream_url: document.getElementById("channel-url").value,
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
    if (!id || !confirm("Delete this channel?")) return;
    try {
      await api(`/api/channels/${id}`, { method: "DELETE" });
      applyChannelDeletes([id]);
      toast.success("Channel deleted");
    } catch (err) { toast.error("Delete failed", err.message); }
  });
  document.getElementById("btn-test-channel").addEventListener("click", async () => {
    const id = state.selectedChannelId;
    if (!id) return;
    const loading = toast.loading("Testing channel…");
    try {
      const res = await testChannelById(id);
      if (res.ok) {
        loading.update("success", "Channel test OK", `HTTP ${res.status_code}, ${res.bytes_read} bytes, sync=${res.has_sync}, hls=${res.looks_hls}`);
      } else {
        loading.update("error", "Channel test failed", String(res.error || res.status_code));
      }
    } catch (err) { loading.update("error", "Channel test failed", err.message); }
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

}
