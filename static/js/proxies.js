/* —— Proxies —— */
const PROXY_POLICY_HINT = {
  fixed: "Use the selected server. Reconnect that one.",
  random: "Pick one when a session starts. Stay on it while anyone is watching.",
  failover: "Try in order. On failure, continue to the next.",
};

function filteredProxies() {
  return state.proxies.filter((p) => matches(state.filter.proxies, p.name, ...(p.servers || []).map((s) => s.url)));
}

function syncProxySelectionDOM() {
  document.querySelectorAll("#proxy-list input[data-select-proxy]").forEach((cb) => {
    cb.checked = state.selected.proxies.has(cb.dataset.selectProxy);
  });
  updateBulkBar("proxies", "proxy-bulk", "proxy-bulk-count");
}

function renderProxyList() {
  pruneSelection("proxies", new Set(state.proxies.map((p) => p.id)));
  const items = filteredProxies();
  document.getElementById("proxy-count").textContent = `Proxies (${state.proxies.length})`;
  const list = document.getElementById("proxy-list");
  if (!items.length) {
    list.innerHTML = `<div class="empty-list">${state.proxies.length ? "No matches" : "No Proxies yet"}</div>`;
  } else {
    list.innerHTML = items.map((p) => {
      const active = !state.creatingProxy && state.selectedProxyId === p.id ? "active" : "";
      const checked = state.selected.proxies.has(p.id) ? "checked" : "";
      const n = (p.servers || []).length;
      const host = shortHost((p.servers && p.servers[0] && p.servers[0].url) || "");
      const sub = n > 1 ? `${host} · ${n}` : host;
      return `<div class="master-item ${active}" data-open-proxy="${esc(p.id)}">
        <input type="checkbox" data-select-proxy="${esc(p.id)}" ${checked} />
        <div class="min">
          <div class="title">${esc(p.name)}</div>
          <div class="sub">${esc(sub)}</div>
        </div>
      </div>`;
    }).join("");
  }
  updateBulkBar("proxies", "proxy-bulk", "proxy-bulk-count");
}

function newServerID() {
  return newEntityID("s");
}

function ensureServerRow(s = {}) {
  return { id: String(s.id || "").trim() || newServerID(), url: s.url || "" };
}

function renderProxyServers(rows, { policy = "fixed", fixedId = "" } = {}) {
  const list = document.getElementById("proxy-servers");
  rows = (rows && rows.length) ? rows.map(ensureServerRow) : [ensureServerRow({})];
  let selected = rows.findIndex((s) => s.id === fixedId);
  if (selected < 0) selected = 0;
  list.innerHTML = rows.map((s, i) => {
    const checked = policy === "fixed" && i === selected;
    const canRemove = rows.length > 1;
    return `<div class="upstream-row ${policy === "fixed" ? "" : "fixed-off"}" data-server-idx="${i}">
      <div class="upstream-main">
        <input class="upstream-pick" type="radio" name="proxy-fixed-server" value="${i}" ${checked ? "checked" : ""} title="Use this server" />
        <input type="url" data-server-url required placeholder="http://host:port/udp/" value="${esc(s.url || "")}" />
        <input type="hidden" data-server-id value="${esc(s.id || "")}" />
        <div class="upstream-actions">
          <button type="button" class="icon-btn ghost" data-srv-move="-1" title="Move up" aria-label="Move up" ${i === 0 ? "disabled" : ""}>↑</button>
          <button type="button" class="icon-btn ghost" data-srv-move="1" title="Move down" aria-label="Move down" ${i === rows.length - 1 ? "disabled" : ""}>↓</button>
          <button type="button" class="icon-btn ghost danger" data-srv-remove title="Remove" aria-label="Remove" ${canRemove ? "" : "disabled"}>×</button>
        </div>
      </div>
    </div>`;
  }).join("");
}

function readProxyServerRows() {
  return [...document.querySelectorAll("#proxy-servers .upstream-row")].map((row) => ({
    id: row.querySelector("[data-server-id]")?.value || "",
    url: row.querySelector("[data-server-url]")?.value || "",
  }));
}

function refreshProxyServerList(mutate) {
  const policy = document.getElementById("proxy-policy").value;
  const rows = readProxyServerRows();
  const radio = document.querySelector("input[name='proxy-fixed-server']:checked");
  const fixedId = radio ? (rows[Number(radio.value)]?.id || "") : "";
  mutate(rows);
  renderProxyServers(rows, { policy, fixedId });
  updateProxyPolicyHint();
}

function readProxyDraft() {
  const rows = readProxyServerRows();
  const radio = document.querySelector("input[name='proxy-fixed-server']:checked");
  const fixedId = radio ? (rows[Number(radio.value)]?.id || "") : (rows[0]?.id || "");
  return {
    name: document.getElementById("proxy-name").value,
    policy: document.getElementById("proxy-policy").value,
    fixed_server_id: fixedId,
    servers: rows.map((s) => ({ id: s.id, url: s.url })),
  };
}

function isProxyDirty() {
  return isDomainDirty(state.editors.proxy.baseline, readProxyDraft());
}

function updateProxyPolicyHint() {
  const policy = document.getElementById("proxy-policy").value;
  document.getElementById("proxy-policy-hint").textContent = PROXY_POLICY_HINT[policy] || "";
  document.querySelectorAll("#proxy-servers .upstream-row").forEach((row) => {
    row.classList.toggle("fixed-off", policy !== "fixed");
  });
}

function fillProxyForm(p) {
  document.getElementById("proxy-detail-title").textContent = state.creatingProxy ? "New Proxy" : p.name;
  document.getElementById("proxy-id").value = p?.id || "";
  document.getElementById("proxy-name").value = p?.name || "";
  document.getElementById("proxy-policy").value = p?.policy || "fixed";
  const rows = (p?.servers && p.servers.length) ? p.servers.map(ensureServerRow) : [ensureServerRow({})];
  renderProxyServers(rows, { policy: p?.policy || "fixed", fixedId: p?.fixed_server_id || "" });
  updateProxyPolicyHint();
  document.getElementById("proxy-error").textContent = "";
  state.editors.proxy.baseline = readProxyDraft();
}

function updateProxyMeta(p) {
  const meta = document.getElementById("proxy-meta");
  if (!p) meta.textContent = "";
  else if (p.channel_count) meta.textContent = `Used by ${p.channel_count} channel(s)`;
  else meta.textContent = "Used by: none";
  document.getElementById("btn-del-proxy").classList.toggle("hidden", state.creatingProxy);
}

function proxyDetail() {
  return {
    tab: "proxies",
    empty: "proxy-detail-empty",
    body: "proxy-detail-body",
    title: "proxy-detail-title",
    list: "proxies",
    selected: "selectedProxyId",
    creating: "creatingProxy",
    editor: "proxy",
    isDirty: isProxyDirty,
    updateMeta: updateProxyMeta,
    fill: fillProxyForm,
    render: renderProxyList,
  };
}

function showProxyDetail(opts) {
  showDetail(proxyDetail(), opts);
}

async function loadProxies() {
  const gen = ++state.gens.proxies;
  const list = await api("/api/proxies");
  if (!isCurrentGeneration(gen, state.gens.proxies)) return;
  state.proxies = list;
  dropStaleSelection(proxyDetail());
  renderProxyList();
  showProxyDetail({ forceFill: false });
  if (typeof renderChannelUpstreams === "function" && document.getElementById("channel-upstreams")?.children.length) {
    refreshUpstreamList((rows) => rows);
  }
}

async function applyProxySave(saved) {
  state.creatingProxy = false;
  state.selectedProxyId = saved.id;
  await loadProxies();
  if (typeof loadChannels === "function") {
    await loadChannels();
  }
  showProxyDetail({ forceFill: true });
}

function applyProxyDeletes(ids) {
  applyDetailDeletes(proxyDetail(), ids, (id) => {
    state.selected.proxies.delete(String(id));
  });
}

function selectProxy(id) {
  selectDetail(proxyDetail(), id);
}

function wireProxies() {
  document.getElementById("proxy-search").addEventListener("input", (e) => {
    state.filter.proxies = e.target.value;
    renderProxyList();
  });
  document.getElementById("btn-new-proxy").addEventListener("click", () => {
    state.creatingProxy = true;
    state.selectedProxyId = null;
    renderProxyList();
    showProxyDetail({ forceFill: true });
  });
  document.getElementById("proxy-list").addEventListener("click", (e) => {
    const t = e.target;
    if (!(t instanceof HTMLElement)) return;
    if (t.dataset.selectProxy) return;
    const item = t.closest("[data-open-proxy]");
    if (item) selectProxy(item.dataset.openProxy);
  });
  document.getElementById("proxy-list").addEventListener("change", (e) => {
    const t = e.target;
    if (!(t instanceof HTMLInputElement) || !t.dataset.selectProxy) return;
    if (t.checked) state.selected.proxies.add(t.dataset.selectProxy);
    else state.selected.proxies.delete(t.dataset.selectProxy);
    syncProxySelectionDOM();
  });
  document.getElementById("btn-proxy-select-all").addEventListener("click", () => {
    filteredProxies().forEach((p) => state.selected.proxies.add(p.id));
    syncProxySelectionDOM();
  });
  document.getElementById("btn-clear-proxies").addEventListener("click", () => {
    state.selected.proxies.clear();
    syncProxySelectionDOM();
  });
  document.getElementById("btn-del-proxies").addEventListener("click", async () => {
    const ids = [...state.selected.proxies];
    const res = await bulkDelete("Proxy(s)", ids, (id) => api(`/api/proxies/${id}`, { method: "DELETE" }));
    if (!res) return;
    applyProxyDeletes(res.successfulIDs);
  });
  document.getElementById("proxy-policy").addEventListener("change", updateProxyPolicyHint);
  document.getElementById("btn-add-proxy-server").addEventListener("click", () => {
    refreshProxyServerList((rows) => rows.push(ensureServerRow({})));
  });
  document.getElementById("proxy-servers").addEventListener("click", (e) => {
    const t = e.target;
    if (!(t instanceof HTMLElement)) return;
    const row = t.closest(".upstream-row");
    if (!row) return;
    if (t.dataset.srvRemove != null) {
      refreshProxyServerList((rows) => {
        const i = Number(row.dataset.serverIdx);
        if (rows.length > 1) rows.splice(i, 1);
      });
    } else if (t.dataset.srvMove) {
      const dir = Number(t.dataset.srvMove);
      refreshProxyServerList((rows) => {
        const i = Number(row.dataset.serverIdx);
        const j = i + dir;
        if (j < 0 || j >= rows.length) return;
        const tmp = rows[i];
        rows[i] = rows[j];
        rows[j] = tmp;
      });
    }
  });
  document.getElementById("proxy-form").addEventListener("submit", async (e) => {
    e.preventDefault();
    const errEl = document.getElementById("proxy-error");
    errEl.textContent = "";
    const draft = readProxyDraft();
    const servers = draft.servers.filter((s) => s.url.trim());
    if (!servers.length) {
      errEl.textContent = "At least one server is required";
      return;
    }
    const body = { name: draft.name, policy: draft.policy, fixed_server_id: draft.fixed_server_id, servers };
    const id = document.getElementById("proxy-id").value;
    try {
      const saved = id
        ? await api(`/api/proxies/${id}`, { method: "PUT", body: JSON.stringify(body) })
        : await api("/api/proxies", { method: "POST", body: JSON.stringify(body) });
      await applyProxySave(saved);
      toast.success(id ? "Proxy updated" : "Proxy created");
    } catch (err) { errEl.textContent = err.message; toast.error("Save failed", err.message); }
  });
  document.getElementById("btn-del-proxy").addEventListener("click", async () => {
    const id = state.selectedProxyId;
    if (!id || !await askConfirm("Delete this Proxy?")) return;
    try {
      await api(`/api/proxies/${id}`, { method: "DELETE" });
      applyProxyDeletes([id]);
      toast.success("Proxy deleted");
    } catch (err) { toast.error("Delete failed", err.message); }
  });
}
