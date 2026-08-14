/* —— EPG Viewer —— */
function ensureViewerWindow() {
  if (state.viewer.from && state.viewer.to) return;
  const w = timelineNowWindow(Date.now(), timelineWindowMs());
  state.viewer.from = w.from;
  state.viewer.to = w.to;
}

function fillViewerSources() {
  const sel = document.getElementById("viewer-source");
  const prev = state.viewer.sourceId || sel.value;
  const opts = state.epgs.map((s) =>
    `<option value="${s.id}">${esc(s.name)}${s.enabled ? "" : " (disabled)"}</option>`).join("");
  sel.innerHTML = opts || `<option value="">No EPG Sources</option>`;
  if (prev && state.epgs.some((s) => String(s.id) === String(prev))) {
    sel.value = String(prev);
  }
  state.viewer.sourceId = sel.value ? Number(sel.value) : null;
}

function viewerRangeLabel() {
  const fmt = new Intl.DateTimeFormat(UI_LOCALE, {
    month: "short", day: "numeric", hour: "numeric", minute: "2-digit",
  });
  return `${fmt.format(new Date(state.viewer.from))} – ${fmt.format(new Date(state.viewer.to))}`;
}

function renderViewer() {
  const body = document.getElementById("viewer-body");
  const status = document.getElementById("viewer-status");
  const pageLabel = document.getElementById("viewer-page-label");
  const v = state.viewer;
  const prevScroll = document.getElementById("viewer-scroll");
  const hadTimeline = !!prevScroll;
  if (prevScroll) v.scrollLeft = prevScroll.scrollLeft;

  document.getElementById("viewer-page-prev").disabled = !v.data || v.offset <= 0 || v.loading;
  document.getElementById("viewer-page-next").disabled = !v.data || v.offset + v.limit >= (v.data?.total || 0) || v.loading;

  if (!v.sourceId) {
    status.textContent = "";
    pageLabel.textContent = "—";
    body.innerHTML = `<div class="empty-list">Select an EPG Source</div>`;
    return;
  }
  if (v.loading && !v.data) {
    status.textContent = "Loading guide…";
    body.innerHTML = `<div class="empty-list">Loading…</div>`;
    return;
  }
  if (v.error) {
    status.innerHTML = `<span class="status-chip err">${esc(v.error)}</span>`;
    pageLabel.textContent = "—";
    body.innerHTML = `<div class="empty-list">${esc(v.error)}</div>`;
    return;
  }
  if (!v.data) {
    status.textContent = "";
    body.innerHTML = `<div class="empty-list">No data</div>`;
    return;
  }

  const total = v.data.total || 0;
  const fromIdx = total ? v.offset + 1 : 0;
  const toIdx = Math.min(v.offset + v.limit, total);
  pageLabel.textContent = total ? `${fromIdx}–${toIdx} / ${total}` : "0 channels";

  const chips = [`<span class="status-chip">${esc(viewerRangeLabel())}</span>`];
  if (v.data.fetched_at) {
    chips.push(`<span class="status-chip">Cached ${whenHTML(v.data.fetched_at)}</span>`);
  }
  if (v.data.stale) {
    chips.push(`<span class="status-chip err">Stale · last refresh failed</span>`);
  }
  if (v.loading) chips.push(`<span class="status-refreshing">Updating…</span>`);
  status.innerHTML = chips.join(`<span class="sep" aria-hidden="true">·</span>`);

  if (!v.data.channels?.length) {
    body.innerHTML = `<div class="empty-list">${v.q ? "No matching Channels" : "No Channels in this source"}</div>`;
    return;
  }

  const ppm = timelinePixelsPerMinute();
  const width = timelineWidthPx(v.to - v.from, ppm);
  const marks = hourMarks(v.from, v.to);
  const now = Date.now();
  const nowLeft = now >= v.from && now < v.to ? ((now - v.from) / 60000) * ppm : null;
  const fmtHour = new Intl.DateTimeFormat(UI_LOCALE, { hour: "numeric", minute: "2-digit" });

  const rulerMarks = marks.map((m) =>
    `<span class="tl-mark" style="left:${m.left}px">${esc(fmtHour.format(new Date(m.at)))}</span>`
  ).join("");

  const rows = v.data.channels.map((ch) => {
    const prepared = (ch.programmes || []).map((p) => ({
      ...p,
      startMs: Date.parse(p.start),
      stopMs: Date.parse(p.stop),
    })).filter((p) => Number.isFinite(p.startMs) && Number.isFinite(p.stopMs));
    const { programmes: lanes, laneCount } = assignProgrammeLanes(prepared);
    const laid = lanes.map((p) => {
      const layout = programmeLayout(p.startMs, p.stopMs, v.from, v.to, ppm);
      return layout ? { ...p, ...layout } : null;
    }).filter(Boolean);
    const rowH = 28 + (laneCount - 1) * 22;
    const blocks = laid.map((p) => {
      const top = 4 + p.lane * 22;
      const cls = `tl-prog${p.clippedStart || p.clippedStop ? " clipped" : ""}`;
      const title = p.title || "(no title)";
      return `<button type="button" class="${cls}" style="left:${p.left}px;width:${p.width}px;top:${top}px"
        data-prog-title="${esc(title)}"
        data-prog-start="${esc(p.start)}"
        data-prog-stop="${esc(p.stop)}"
        data-prog-desc="${esc(p.description || "")}"
        data-prog-cat="${esc(p.category || "")}"
        data-prog-channel="${esc(ch.display_name || ch.id)}"
        title="${esc(title)}">
        <span>${esc(title)}</span>
      </button>`;
    }).join("");
    const nowMark = nowLeft != null ? `<span class="tl-now" style="left:${nowLeft}px"></span>` : "";
    const name = ch.display_name || ch.id;
    return `<div class="tl-row" style="height:${rowH}px">
      <div class="tl-id" title="${esc(ch.id)}">${esc(ch.id)}</div>
      <div class="tl-ch" title="${esc(name)}">
        <div class="title">${esc(name)}</div>
      </div>
      <div class="tl-track" style="width:${width}px;height:${rowH}px">${nowMark}${blocks}</div>
    </div>`;
  }).join("");

  const headNow = nowLeft != null ? `<span class="tl-now" style="left:${nowLeft}px"></span>` : "";
  const head = `<div class="tl-row tl-head-row">
      <div class="tl-id tl-ch-head">ID</div>
      <div class="tl-ch tl-ch-head">Channel</div>
      <div class="tl-track tl-ruler" style="width:${width}px">${rulerMarks}${headNow}</div>
    </div>`;

  body.innerHTML = `<div class="tl" id="viewer-timeline">
    <div class="tl-scroll" id="viewer-scroll">${head}${rows}</div>
  </div>`;

  const scroll = document.getElementById("viewer-scroll");
  if (!scroll) return;
  const jumpNow = shouldResetViewerScroll({
    windowChanged: v.resetScroll,
    scrollToNow: v.resetScroll,
  }) || !hadTimeline;
  if (jumpNow && nowLeft != null) {
    scroll.scrollLeft = Math.max(0, nowLeft - 120);
  } else {
    scroll.scrollLeft = v.scrollLeft || 0;
  }
  v.resetScroll = false;
  v.scrollLeft = scroll.scrollLeft;
}

function viewerRequestKey() {
  const v = state.viewer;
  return [v.sourceId, v.from, v.to, v.offset, v.limit, v.q].join("|");
}

async function loadViewerGuide({ force = true, resetScroll = false } = {}) {
  const v = state.viewer;
  ensureViewerWindow();
  if (resetScroll) v.resetScroll = true;
  if (!v.sourceId) {
    v.data = null;
    v.error = "";
    v.lastKey = "";
    v.loading = false;
    renderViewer();
    return;
  }
  const key = viewerRequestKey();
  if (!force && v.data && v.lastKey === key && !v.error) {
    v.loading = false;
    renderViewer();
    return;
  }
  if (v.abort) v.abort.abort();
  const ac = new AbortController();
  v.abort = ac;
  v.loading = true;
  v.error = "";
  renderViewer();
  try {
    const params = new URLSearchParams({
      from: new Date(v.from).toISOString(),
      to: new Date(v.to).toISOString(),
      offset: String(v.offset),
      limit: String(v.limit),
    });
    if (v.q) params.set("q", v.q);
    const res = await fetch(`/api/epg/sources/${v.sourceId}/guide?${params}`, {
      headers: { "Content-Type": "application/json" },
      signal: ac.signal,
    });
    const text = await res.text();
    let data = null;
    try { data = text ? JSON.parse(text) : null; } catch { data = { error: text }; }
    if (!res.ok) {
      const msg = (data && data.error) || res.statusText || "request failed";
      if (res.status === 409) throw new Error("Refresh required — open EPG Sources and refresh first");
      throw new Error(msg);
    }
    if (ac.signal.aborted || v.abort !== ac) return;
    v.data = data;
    v.error = "";
    v.lastKey = key;
  } catch (err) {
    if (err.name === "AbortError" || v.abort !== ac) return;
    v.error = err.message || String(err);
    v.data = null;
    v.lastKey = "";
  } finally {
    if (v.abort === ac) {
      v.loading = false;
      v.abort = null;
      renderViewer();
    }
  }
}

function openViewerDetail(btn) {
  const panel = document.getElementById("viewer-detail");
  document.getElementById("viewer-detail-title").textContent = btn.dataset.progTitle || "Programme";
  const start = formatAbsolute(new Date(btn.dataset.progStart));
  const stop = formatAbsolute(new Date(btn.dataset.progStop));
  const cat = btn.dataset.progCat ? ` · ${btn.dataset.progCat}` : "";
  document.getElementById("viewer-detail-meta").textContent =
    `${btn.dataset.progChannel || ""} · ${start} – ${stop}${cat}`;
  document.getElementById("viewer-detail-desc").textContent = btn.dataset.progDesc || "No description";
  panel.classList.remove("hidden");
  panel.focus();
}

function wireViewer() {
  document.getElementById("viewer-source").addEventListener("change", () => {
    state.viewer.sourceId = Number(document.getElementById("viewer-source").value) || null;
    state.viewer.offset = 0;
    loadViewerGuide();
  });
  document.getElementById("viewer-search").addEventListener("input", () => {
    clearTimeout(state.viewer.searchTimer);
    state.viewer.searchTimer = setTimeout(() => {
      state.viewer.q = document.getElementById("viewer-search").value.trim();
      state.viewer.offset = 0;
      loadViewerGuide();
    }, 250);
  });
  document.getElementById("viewer-prev").addEventListener("click", () => {
    const w = timelineShiftWindow(state.viewer.from, state.viewer.to, -1);
    state.viewer.from = w.from;
    state.viewer.to = w.to;
    loadViewerGuide({ force: true, resetScroll: true });
  });
  document.getElementById("viewer-next").addEventListener("click", () => {
    const w = timelineShiftWindow(state.viewer.from, state.viewer.to, 1);
    state.viewer.from = w.from;
    state.viewer.to = w.to;
    loadViewerGuide({ force: true, resetScroll: true });
  });
  document.getElementById("viewer-now").addEventListener("click", () => {
    const w = timelineNowWindow(Date.now(), timelineWindowMs());
    state.viewer.from = w.from;
    state.viewer.to = w.to;
    loadViewerGuide({ force: true, resetScroll: true });
  });
  document.getElementById("viewer-page-prev").addEventListener("click", () => {
    state.viewer.offset = Math.max(0, state.viewer.offset - state.viewer.limit);
    loadViewerGuide({ force: true, resetScroll: false });
  });
  document.getElementById("viewer-page-next").addEventListener("click", () => {
    state.viewer.offset += state.viewer.limit;
    loadViewerGuide({ force: true, resetScroll: false });
  });
  document.getElementById("viewer-body").addEventListener("click", (e) => {
    const btn = e.target.closest(".tl-prog");
    if (!(btn instanceof HTMLElement)) return;
    openViewerDetail(btn);
  });
  document.getElementById("viewer-detail-close").addEventListener("click", () => {
    document.getElementById("viewer-detail").classList.add("hidden");
  });

}
