/* Shared UI state and helpers */
const state = {
  channels: [],
  epgs: [],
  proxies: [],
  relays: [],
  channelStatuses: [],
  baseURL: "",
  currentRelay: null,
  selectedChannelId: null,
  selectedEPGId: null,
  selectedProxyId: null,
  selectedRelayId: null,
  creatingChannel: false,
  creatingEPG: false,
  creatingProxy: false,
  dragMemberId: null,
  dragGroupId: null,
  collapsedGroups: new Set(),
  filter: { channels: "", epgs: "", proxies: "", relays: "" },
  selected: {
    channels: new Set(),
    epgs: new Set(),
    proxies: new Set(),
    relays: new Set(),
    members: new Set(),
  },
  channelTest: {}, // id -> "ok" | "fail" | "testing"
  testingAllChannels: false,
  gens: {
    channels: 0,
    epgs: 0,
    proxies: 0,
    relays: 0,
    relayOpen: 0,
    relayLineup: 0,
    memberRelay: 0,
    tvg: 0,
  },
  editors: {
    channel: { baseline: null },
    epg: { baseline: null },
    proxy: { baseline: null },
    relayMeta: { baseline: null },
  },
  viewer: {
    sourceId: null,
    q: "",
    offset: 0,
    limit: 50,
    from: 0,
    to: 0,
    data: null,
    loading: false,
    error: "",
    abort: null,
    searchTimer: null,
    lastKey: "",
    scrollLeft: 0,
    resetScroll: false,
  },
};

function currentTab() {
  return document.querySelector(".nav-item.active")?.dataset.tab || "channels";
}

function publicBaseURL() {
  return (state.baseURL || location.origin || "").replace(/\/$/, "");
}

function relayURLs(slug) {
  const base = publicBaseURL();
  return {
    playlist: `${base}/r/${slug}/playlist.m3u`,
    epg: `${base}/r/${slug}/epg.xml`,
  };
}

function newEntityID(prefix) {
  if (typeof crypto !== "undefined" && crypto.randomUUID) return crypto.randomUUID();
  return (prefix || "id") + "-" + Math.random().toString(16).slice(2) + Date.now().toString(16);
}

function channelStreamURL(channelID) {
  if (!channelID) return "";
  return `${publicBaseURL()}/stream/${channelID}`;
}

async function copyText(label, text) {
  if (!text) return;
  try {
    await navigator.clipboard.writeText(text);
    toast.success("Copied", `${label}: ${text}`);
  } catch {
    const ta = document.createElement("textarea");
    ta.value = text;
    ta.setAttribute("readonly", "");
    ta.style.cssText = "position:fixed;opacity:0";
    document.body.appendChild(ta);
    ta.select();
    try {
      document.execCommand("copy");
      toast.success("Copied", `${label}: ${text}`);
    } catch (err) {
      toast.error("Copy failed", err.message || text);
    } finally {
      ta.remove();
    }
  }
}

function upsertById(list, item) {
  const idx = list.findIndex((x) => x.id === item.id);
  if (idx >= 0) list[idx] = { ...list[idx], ...item };
  else list.push(item);
  return list;
}

function removeByIds(list, ids) {
  const set = new Set(ids.map(String));
  return list.filter((x) => !set.has(String(x.id)));
}

function syncRelayListItem(detail) {
  const urls = relayURLs(detail.slug);
  upsertById(state.relays, {
    id: detail.id,
    name: detail.name,
    slug: detail.slug,
    playlist_url: urls.playlist,
    epg_url: urls.epg,
  });
}

function readRelayMetaDraft() {
  return {
    name: document.getElementById("relay-name").value,
    slug: document.getElementById("relay-slug").value,
  };
}

function isRelayMetaDirty() {
  return isDomainDirty(state.editors.relayMeta.baseline, readRelayMetaDraft());
}

function fillRelayMetaFields(detail) {
  document.getElementById("relay-editor-title").textContent = detail.name;
  document.getElementById("relay-name").value = detail.name;
  document.getElementById("relay-slug").value = detail.slug;
  const urls = relayURLs(detail.slug);
  document.getElementById("btn-copy-playlist").dataset.url = urls.playlist;
  document.getElementById("btn-copy-epg").dataset.url = urls.epg;
  state.editors.relayMeta.baseline = readRelayMetaDraft();
}

function showRelayEditorChrome(detail) {
  document.getElementById("relay-detail-empty").classList.add("hidden");
  document.getElementById("relay-editor").classList.remove("hidden");
  setDetailOpen("relays", true);
  const urls = relayURLs(detail.slug);
  document.getElementById("btn-copy-playlist").dataset.url = urls.playlist;
  document.getElementById("btn-copy-epg").dataset.url = urls.epg;
}

/** Full editor apply (user open / create / metadata save). */
function applyRelayEditor(detail, { clearMemberSelection = false, forceFill = false } = {}) {
  state.selectedRelayId = detail.id;
  state.currentRelay = detail;
  if (clearMemberSelection) state.selected.members.clear();
  showRelayEditorChrome(detail);
  syncRelayListItem(detail);
  renderRelayList();
  if (shouldFillEditor({
    activeEntityId: state.selectedRelayId,
    responseEntityId: detail.id,
    domainDirty: isRelayMetaDirty(),
    force: forceFill,
  })) {
    fillRelayMetaFields(detail);
  }
  renderLineup();
}

/** Apply lineup/detail payload without refilling relay meta fields. */
function applyRelayLineup(detail) {
  if (!state.currentRelay || state.currentRelay.id !== detail.id) {
    state.currentRelay = detail;
  } else {
    state.currentRelay = {
      ...state.currentRelay,
      name: detail.name,
      slug: detail.slug,
      groups: detail.groups,
      memberships: detail.memberships,
    };
  }
  state.selectedRelayId = detail.id;
  syncRelayListItem(detail);
  renderRelayList();
  renderLineup();
}

async function refreshRelayLineup() {
  if (!state.currentRelay) return;
  const id = state.currentRelay.id;
  const gen = ++state.gens.relayLineup;
  const detail = await api(`/api/relays/${id}`);
  if (!isCurrentGeneration(gen, state.gens.relayLineup)) return;
  if (state.selectedRelayId !== detail.id) return;
  if (!state.currentRelay || state.currentRelay.id !== detail.id) return;
  applyRelayLineup(detail);
}

const PAGE_META = {
  channels: { title: "Channels", desc: "Manage stream sources" },
  proxies: { title: "Proxies", desc: "HTTP prefixes for multicast links" },
  epgs: { title: "EPG Sources", desc: "Configure EPG feeds" },
  relays: { title: "Relays", desc: "Publish playlists and EPG feeds" },
  "epg-viewer": { title: "EPG Viewer", desc: "Review EPG data" },
  settings: { title: "Settings", desc: "System settings and configurations" },
};

async function api(path, options = {}) {
  const res = await fetch(path, {
    headers: { "Content-Type": "application/json", ...(options.headers || {}) },
    ...options,
  });
  if (res.status === 204) return null;
  const text = await res.text();
  let data = null;
  try { data = text ? JSON.parse(text) : null; } catch { data = { error: text }; }
  if (!res.ok) throw new Error((data && data.error) || res.statusText || "request failed");
  return data;
}

function onFilePick(pickEl, fileEl, handler) {
  pickEl.addEventListener("click", () => fileEl.click());
  fileEl.addEventListener("change", async () => {
    const file = fileEl.files && fileEl.files[0];
    fileEl.value = "";
    if (!file) return;
    await handler(file);
  });
}

function esc(s) {
  return String(s ?? "").replace(/[&<>"']/g, (c) => ({
    "&": "&amp;", "<": "&lt;", ">": "&gt;", '"': "&quot;", "'": "&#39;"
  }[c]));
}

let toastSeq = 0;
const TOAST_MS = 10000;

function notify(type, title, body = "", { timeout = TOAST_MS, id = null } = {}) {
  const root = document.getElementById("toasts");
  if (!root) return null;
  const toastId = id || `toast-${++toastSeq}`;
  timeout = timeout > 0 ? timeout : TOAST_MS;
  let el = document.getElementById(toastId);
  if (!el) {
    el = document.createElement("div");
    el.id = toastId;
    el.className = `toast ${type}`;
    root.prepend(el);
  } else {
    el.className = `toast ${type}`;
  }
  el.innerHTML = `
    <div>
      <p class="toast-title">${esc(title)}</p>
      ${body ? `<p class="toast-body">${esc(body)}</p>` : ""}
    </div>
    <button type="button" class="toast-close" aria-label="Dismiss">×</button>
  `;
  const close = () => {
    if (!el.isConnected) return;
    el.classList.add("hiding");
    setTimeout(() => el.remove(), 140);
  };
  el.querySelector(".toast-close").onclick = close;
  clearTimeout(el._timer);
  el._timer = setTimeout(close, timeout);
  return {
    id: toastId,
    update(nextType, nextTitle, nextBody = "", opts) {
      return notify(nextType, nextTitle, nextBody, { timeout, ...opts, id: toastId });
    },
    close,
  };
}

const toast = {
  success: (title, body, opts) => notify("success", title, body, opts),
  error: (title, body, opts) => notify("error", title, body, opts),
  info: (title, body, opts) => notify("info", title, body, opts),
  loading: (title, body, opts) => notify("loading", title, body, opts),
};

function showTab(name) {
  document.querySelectorAll(".nav-item").forEach((b) => b.classList.toggle("active", b.dataset.tab === name));
  document.querySelectorAll(".tab-panel").forEach((p) => p.classList.toggle("active", p.id === `tab-${name}`));
  const meta = PAGE_META[name] || PAGE_META.channels;
  document.getElementById("page-title").textContent = meta.title;
  document.getElementById("page-desc").textContent = meta.desc;
  closeSidebarMobile();
  if (name === "epg-viewer") {
    ensureViewerWindow();
    fillViewerSources();
    loadViewerGuide({ force: false });
  } else if (name === "settings") {
    loadSettings().catch(() => {});
  }
  if (name !== "epg-viewer") {
    // Leaving viewer: abort in-flight fetch and always clear loading ownership.
    if (state.viewer.abort) {
      state.viewer.abort.abort();
      state.viewer.abort = null;
    }
    state.viewer.loading = false;
  }
}

function setDetailOpen(tab, open) {
  const panel = document.querySelector(`#tab-${tab} .md`);
  if (panel) panel.classList.toggle("show-detail", !!open);
}

function showDetail(cfg, { forceFill = false } = {}) {
  const creating = !!state[cfg.creating];
  const selectedId = state[cfg.selected];
  const entity = creating
    ? null
    : (state[cfg.list] || []).find((x) => String(x.id) === String(selectedId)) || null;
  const empty = document.getElementById(cfg.empty);
  const body = document.getElementById(cfg.body);
  if (!creating && !entity) {
    empty.classList.remove("hidden");
    body.classList.add("hidden");
    setDetailOpen(cfg.tab, false);
    state.editors[cfg.editor].baseline = null;
    return;
  }
  empty.classList.add("hidden");
  body.classList.remove("hidden");
  setDetailOpen(cfg.tab, true);
  cfg.updateMeta(entity);
  const fill = shouldFillEditor({
    activeEntityId: creating ? "new" : selectedId,
    responseEntityId: creating ? "new" : entity?.id,
    domainDirty: cfg.isDirty(),
    force: forceFill || state.editors[cfg.editor].baseline == null,
  });
  if (fill) cfg.fill(entity);
  else if (!creating && entity && cfg.title) {
    document.getElementById(cfg.title).textContent = entity.name;
  }
}

function dropStaleSelection(cfg) {
  const id = state[cfg.selected];
  if (id == null) return;
  if ((state[cfg.list] || []).some((x) => String(x.id) === String(id))) return;
  state[cfg.selected] = null;
  state.editors[cfg.editor].baseline = null;
}

function applyDetailDeletes(cfg, ids, onEach) {
  const cleared = editorClearedByDeletes(state[cfg.selected], ids);
  state[cfg.list] = removeByIds(state[cfg.list], ids);
  for (const id of ids) onEach?.(id);
  if (cleared) {
    state[cfg.selected] = null;
    state.editors[cfg.editor].baseline = null;
  }
  cfg.render();
  if (cleared) showDetail(cfg, { forceFill: true });
}

function selectDetail(cfg, id) {
  state[cfg.creating] = false;
  state[cfg.selected] = id;
  cfg.render();
  showDetail(cfg, { forceFill: true });
}

function closeSidebarMobile() {
  document.getElementById("sidebar").classList.remove("open");
  document.body.classList.remove("sidebar-open");
}



function pruneSelection(key, validIds) {
  const set = state.selected[key];
  for (const id of [...set]) {
    if (!validIds.has(id)) set.delete(id);
  }
}

function updateBulkBar(key, barId, countId) {
  const n = state.selected[key].size;
  document.getElementById(barId).classList.toggle("hidden", n === 0);
  document.getElementById(countId).textContent = `${n} selected`;
}

function askConfirm(title, ok = "Delete") {
  const dlg = document.getElementById("confirm-dialog");
  if (dlg.open) return Promise.resolve(false);
  document.getElementById("confirm-title").textContent = title;
  document.getElementById("confirm-ok").textContent = ok;
  return new Promise((resolve) => {
    const onClose = () => {
      dlg.removeEventListener("close", onClose);
      resolve(dlg.returnValue === "ok");
    };
    dlg.addEventListener("close", onClose);
    dlg.showModal();
  });
}

async function bulkDelete(label, ids, deleteOne) {
  if (!ids.length) return null;
  if (!await askConfirm(`Delete ${ids.length} ${label}?`)) return null;
  const loading = toast.loading(`Deleting ${ids.length} ${label}…`);
  const successfulIDs = [];
  const failures = [];
  for (const id of ids) {
    try {
      await deleteOne(id);
      successfulIDs.push(id);
    } catch (err) {
      failures.push({ id, error: err.message });
    }
  }
  if (!failures.length) {
    loading.update("success", `Deleted ${successfulIDs.length} ${label}`);
  } else {
    const body = failures.slice(0, 6).map((f) => `${f.id}: ${f.error}`).join("\n");
    loading.update("error", `Deleted ${successfulIDs.length}, failed ${failures.length}`, body);
  }
  return { successfulIDs, failures };
}

function matches(q, ...parts) {
  if (!q) return true;
  const needle = q.toLowerCase();
  return parts.some((p) => String(p || "").toLowerCase().includes(needle));
}

function shortHost(url) {
  try {
    const u = new URL(url);
    const path = u.pathname.length > 18 ? `${u.pathname.slice(0, 16)}…` : u.pathname;
    return `${u.host}${path === "/" ? "" : path}`;
  } catch {
    return String(url || "").slice(0, 40);
  }
}

function logoHTML(url, { size = "" } = {}) {
  const cls = size ? `thumb ${size}` : "thumb";
  if (!url) return `<span class="${cls} placeholder" aria-hidden="true"></span>`;
  return `<img class="${cls}" src="${esc(url)}" alt="" loading="lazy" referrerpolicy="no-referrer" onerror="this.classList.add('broken');this.removeAttribute('src')" />`;
}

function setChannelDetailLogo(url) {
  const img = document.getElementById("channel-detail-logo");
  const fallback = document.getElementById("channel-detail-logo-fallback");
  const trimmed = (url || "").trim();
  if (!trimmed) {
    img.classList.add("hidden");
    img.removeAttribute("src");
    fallback.classList.remove("hidden");
    return;
  }
  fallback.classList.add("hidden");
  img.classList.remove("hidden", "broken");
  img.onload = () => img.classList.remove("broken");
  img.onerror = () => {
    img.classList.add("hidden");
    fallback.classList.remove("hidden");
  };
  img.src = trimmed;
}

function parseTime(iso) {
  if (!iso) return null;
  const d = new Date(iso);
  return Number.isNaN(d.getTime()) ? null : d;
}

const UI_LOCALE = "en";

function formatAbsolute(d) {
  return new Intl.DateTimeFormat(UI_LOCALE, {
    year: "numeric",
    month: "short",
    day: "numeric",
    hour: "numeric",
    minute: "2-digit",
  }).format(d);
}

function formatRelative(d, now = Date.now()) {
  const sec = Math.round((d.getTime() - now) / 1000);
  const rtf = new Intl.RelativeTimeFormat(UI_LOCALE, { numeric: "auto" });
  const abs = Math.abs(sec);
  if (abs < 45) return rtf.format(sec, "second");
  const min = Math.round(sec / 60);
  if (Math.abs(min) < 60) return rtf.format(min, "minute");
  const hr = Math.round(min / 60);
  if (Math.abs(hr) < 24) return rtf.format(hr, "hour");
  const day = Math.round(hr / 24);
  if (Math.abs(day) < 7) return rtf.format(day, "day");
  return formatAbsolute(d);
}

/** English relative label + absolute title (local timezone). */
function formatWhen(iso) {
  const d = parseTime(iso);
  if (!d) return null;
  return {
    iso: d.toISOString(),
    relative: formatRelative(d),
    absolute: formatAbsolute(d),
  };
}

function whenHTML(iso, { withAbsolute = false } = {}) {
  const w = formatWhen(iso);
  if (!w) return "";
  const abs = withAbsolute
    ? `<span class="when-abs">${esc(w.absolute)}</span>`
    : "";
  return `<time class="when" datetime="${esc(w.iso)}" title="${esc(w.absolute)}">${esc(w.relative)}</time>${abs}`;
}

function wireCore() {
  document.querySelector(".sidebar-nav").addEventListener("click", (e) => {
    const btn = e.target.closest(".nav-item");
    if (!btn) return;
    showTab(btn.dataset.tab);
  });

  document.getElementById("sidebar-toggle").addEventListener("click", () => {
    const sb = document.getElementById("sidebar");
    const open = !sb.classList.contains("open");
    sb.classList.toggle("open", open);
    document.body.classList.toggle("sidebar-open", open);
  });
  document.querySelectorAll(".back-btn").forEach((btn) => {
    btn.addEventListener("click", () => {
      const tab = btn.dataset.back;
      if (tab === "channels") {
        state.creatingChannel = false;
        state.selectedChannelId = null;
        state.editors.channel.baseline = null;
        renderChannelList();
        showChannelDetail({ forceFill: true });
      } else if (tab === "proxies") {
        state.creatingProxy = false;
        state.selectedProxyId = null;
        state.editors.proxy.baseline = null;
        renderProxyList();
        showProxyDetail({ forceFill: true });
      } else if (tab === "epgs") {
        state.creatingEPG = false;
        state.selectedEPGId = null;
        state.editors.epg.baseline = null;
        renderEPGList();
        showEPGDetail({ forceFill: true });
      } else if (tab === "relays") {
        state.selectedRelayId = null;
        showRelayEmpty();
        renderRelayList();
      }
    });
  });

}
