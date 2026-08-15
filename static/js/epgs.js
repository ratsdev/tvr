/* —— EPGs —— */
function filteredEPGs() {
  return state.epgs.filter((e) => matches(state.filter.epgs, e.name, e.url));
}

function setEPGStatusHTML(status) {
  const el = document.getElementById("epg-status");
  if (status?.refreshing) {
    el.classList.remove("hidden");
    el.innerHTML = `<span class="status-refreshing">Refreshing…</span>`;
    return;
  }
  if (status?.last_error) {
    el.classList.remove("hidden");
    el.innerHTML = `<span class="status-chip err">Error · ${esc(status.last_error)}</span>`;
    return;
  }
  el.textContent = "";
  el.classList.add("hidden");
}

function syncEPGSelectionDOM() {
  document.querySelectorAll("#epg-list input[data-select-epg]").forEach((cb) => {
    cb.checked = state.selected.epgs.has(Number(cb.dataset.selectEpg));
  });
  updateBulkBar("epgs", "epg-bulk", "epg-bulk-count");
}

function renderEPGList() {
  pruneSelection("epgs", new Set(state.epgs.map((e) => e.id)));
  const items = filteredEPGs();
  document.getElementById("epg-count").textContent = `EPG Sources (${state.epgs.length})`;
  const list = document.getElementById("epg-list");
  if (!items.length) {
    list.innerHTML = `<div class="empty-list">${state.epgs.length ? "No matches" : "No EPG Sources yet"}</div>`;
  } else {
    list.innerHTML = items.map((src) => {
      const active = !state.creatingEPG && state.selectedEPGId === src.id ? "active" : "";
      const checked = state.selected.epgs.has(src.id) ? "checked" : "";
      return `<div class="master-item ${active}" data-open-epg="${src.id}">
        <input type="checkbox" data-select-epg="${src.id}" ${checked} />
        <div class="min">
          <div class="title">${esc(src.name)}</div>
          <div class="sub">${esc(shortHost(src.url))}</div>
        </div>
        <span class="badge ${src.enabled ? "enabled" : ""}">${src.enabled ? "on" : "off"}</span>
      </div>`;
    }).join("");
  }
  updateBulkBar("epgs", "epg-bulk", "epg-bulk-count");
}

function readEPGDraft() {
  return {
    name: document.getElementById("epg-name").value,
    url: document.getElementById("epg-url").value,
    refresh_interval: document.getElementById("epg-interval").value,
    enabled: document.getElementById("epg-enabled").checked,
  };
}

function isEPGDirty() {
  return isDomainDirty(state.editors.epg.baseline, readEPGDraft());
}

function fillEPGForm(src) {
  document.getElementById("epg-detail-title").textContent = state.creatingEPG ? "New EPG Source" : src.name;
  document.getElementById("epg-id").value = src?.id || "";
  document.getElementById("epg-name").value = src?.name || "";
  document.getElementById("epg-url").value = src?.url || "";
  document.getElementById("epg-interval").value = src?.refresh_interval || "1h";
  document.getElementById("epg-enabled").checked = src?.enabled ?? true;
  document.getElementById("epg-error").textContent = "";
  state.editors.epg.baseline = readEPGDraft();
}

function updateEPGMeta(src) {
  const meta = document.getElementById("epg-meta");
  if (src?.last_refresh_at) {
    meta.innerHTML = `
      <span class="meta-label">Last refresh</span>
      ${whenHTML(src.last_refresh_at, { withAbsolute: true })}
      ${src.last_error ? `<span class="meta-error">${esc(src.last_error)}</span>` : ""}
    `;
  } else if (src?.last_error) {
    meta.innerHTML = `<span class="meta-error">${esc(src.last_error)}</span>`;
  } else {
    meta.innerHTML = state.creatingEPG ? "" : `<span class="meta-label">Never refreshed</span>`;
  }
  document.getElementById("btn-del-epg").classList.toggle("hidden", state.creatingEPG);
  document.getElementById("btn-refresh-epg-one").classList.toggle("hidden", state.creatingEPG);
}

function epgDetail() {
  return {
    tab: "epgs",
    empty: "epg-detail-empty",
    body: "epg-detail-body",
    title: "epg-detail-title",
    list: "epgs",
    selected: "selectedEPGId",
    creating: "creatingEPG",
    editor: "epg",
    isDirty: isEPGDirty,
    updateMeta: updateEPGMeta,
    fill: fillEPGForm,
    render: renderEPGList,
  };
}

function showEPGDetail(opts) {
  showDetail(epgDetail(), opts);
}

async function loadEPG({ withStatus = true } = {}) {
  const gen = ++state.gens.epgs;
  let sources;
  if (withStatus) {
    const [srcList, status] = await Promise.all([api("/api/epg/sources"), api("/api/epg/status")]);
    if (!isCurrentGeneration(gen, state.gens.epgs)) return;
    sources = srcList;
    setEPGStatusHTML(status);
  } else {
    sources = await api("/api/epg/sources");
    if (!isCurrentGeneration(gen, state.gens.epgs)) return;
  }
  state.epgs = sources;
  dropStaleSelection(epgDetail());
  renderEPGList();
  showEPGDetail({ forceFill: false });
  const channelSel = document.getElementById("channel-epg-source");
  if (channelSel && typeof fillChannelEPGSources === "function") {
    fillChannelEPGSources(channelSel.value);
  }
  if (typeof renderLineup === "function") renderLineup();
}

async function applyEPGSave(saved) {
  state.creatingEPG = false;
  state.selectedEPGId = saved.id;
  await loadEPG({ withStatus: false });
  showEPGDetail({ forceFill: true });
}

function applyEPGDeletes(ids) {
  applyDetailDeletes(epgDetail(), ids, (id) => {
    state.selected.epgs.delete(Number(id));
  });
}

function selectEPG(id) {
  selectDetail(epgDetail(), id);
}

function wireEPGs() {
  document.getElementById("epg-search").addEventListener("input", (e) => {
    state.filter.epgs = e.target.value;
    renderEPGList();
  });
  document.getElementById("btn-new-epg").addEventListener("click", () => {
    state.creatingEPG = true;
    state.selectedEPGId = null;
    renderEPGList();
    showEPGDetail({ forceFill: true });
  });
  document.getElementById("epg-list").addEventListener("click", (e) => {
    const t = e.target;
    if (!(t instanceof HTMLElement)) return;
    if (t.dataset.selectEpg) return;
    const item = t.closest("[data-open-epg]");
    if (item) selectEPG(Number(item.dataset.openEpg));
  });
  document.getElementById("epg-list").addEventListener("change", (e) => {
    const t = e.target;
    if (!(t instanceof HTMLInputElement) || !t.dataset.selectEpg) return;
    const id = Number(t.dataset.selectEpg);
    if (t.checked) state.selected.epgs.add(id);
    else state.selected.epgs.delete(id);
    syncEPGSelectionDOM();
  });
  document.getElementById("btn-epg-select-all").addEventListener("click", () => {
    filteredEPGs().forEach((e) => state.selected.epgs.add(e.id));
    syncEPGSelectionDOM();
  });
  document.getElementById("btn-clear-epgs").addEventListener("click", () => {
    state.selected.epgs.clear();
    syncEPGSelectionDOM();
  });
  document.getElementById("btn-del-epgs").addEventListener("click", async () => {
    const ids = [...state.selected.epgs];
    const res = await bulkDelete("EPG Source(s)", ids, (id) => api(`/api/epg/sources/${id}`, { method: "DELETE" }));
    if (!res) return;
    applyEPGDeletes(res.successfulIDs);
  });
  document.getElementById("epg-form").addEventListener("submit", async (e) => {
    e.preventDefault();
    const errEl = document.getElementById("epg-error");
    errEl.textContent = "";
    const body = {
      name: document.getElementById("epg-name").value,
      url: document.getElementById("epg-url").value,
      refresh_interval: document.getElementById("epg-interval").value,
      enabled: document.getElementById("epg-enabled").checked,
    };
    const id = document.getElementById("epg-id").value;
    try {
      const saved = id
        ? await api(`/api/epg/sources/${id}`, { method: "PUT", body: JSON.stringify(body) })
        : await api("/api/epg/sources", { method: "POST", body: JSON.stringify(body) });
      await applyEPGSave(saved);
      toast.success(id ? "EPG Source updated" : "EPG Source created");
    } catch (err) { errEl.textContent = err.message; toast.error("Save failed", err.message); }
  });
  document.getElementById("btn-del-epg").addEventListener("click", async () => {
    const id = state.selectedEPGId;
    if (!id || !await askConfirm("Delete this EPG Source?")) return;
    try {
      await api(`/api/epg/sources/${id}`, { method: "DELETE" });
      applyEPGDeletes([id]);
      toast.success("EPG Source deleted");
    } catch (err) { toast.error("Delete failed", err.message); }
  });
  document.getElementById("btn-refresh-epg").addEventListener("click", async () => {
    await api("/api/epg/refresh", { method: "POST", body: "{}" });
    toast.info("Refreshing all EPG Sources…");
    try { setEPGStatusHTML(await api("/api/epg/status")); } catch { /* ignore */ }
    setTimeout(() => { loadEPG({ withStatus: true }).catch(() => {}); }, 2000);
  });
  document.getElementById("btn-refresh-epg-one").addEventListener("click", async () => {
    const id = state.selectedEPGId;
    if (!id) return;
    const name = state.epgs.find((e) => e.id === id)?.name || "source";
    try {
      await api(`/api/epg/sources/${id}/refresh`, { method: "POST", body: "{}" });
      toast.info(`Refreshing “${name}”…`);
      try { setEPGStatusHTML(await api("/api/epg/status")); } catch { /* ignore */ }
      setTimeout(() => { loadEPG({ withStatus: true }).catch(() => {}); }, 2000);
    } catch (err) { toast.error("Refresh failed", err.message); }
  });

}
