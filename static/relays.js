/* —— Relays —— */
function filteredRelays() {
  return state.relays.filter((r) => matches(state.filter.relays, r.name, r.slug));
}

function syncRelaySelectionDOM() {
  document.querySelectorAll("#relay-list input[data-select-relay]").forEach((cb) => {
    cb.checked = state.selected.relays.has(Number(cb.dataset.selectRelay));
  });
  updateBulkBar("relays", "relay-bulk", "relay-bulk-count");
}

function renderRelayList() {
  pruneSelection("relays", new Set(state.relays.map((r) => r.id)));
  const items = filteredRelays();
  document.getElementById("relay-count").textContent = `Relays (${state.relays.length})`;
  const list = document.getElementById("relay-list");
  if (!items.length) {
    list.innerHTML = `<div class="empty-list">${state.relays.length ? "No matches" : "No relays yet"}</div>`;
  } else {
    list.innerHTML = items.map((r) => {
      const active = state.selectedRelayId === r.id ? "active" : "";
      const checked = state.selected.relays.has(r.id) ? "checked" : "";
      return `<div class="master-item ${active}" data-open-relay="${r.id}">
        <input type="checkbox" data-select-relay="${r.id}" ${checked} />
        <div class="min">
          <div class="title">${esc(r.name)}</div>
          <div class="sub">${esc(r.slug)}</div>
        </div>
      </div>`;
    }).join("");
  }
  updateBulkBar("relays", "relay-bulk", "relay-bulk-count");
}

function showRelayEmpty() {
  document.getElementById("relay-detail-empty").classList.remove("hidden");
  document.getElementById("relay-editor").classList.add("hidden");
  state.currentRelay = null;
  state.editors.relayMeta.baseline = null;
  state.editors.relayEpg.baseline = null;
  setDetailOpen("relays", false);
}

async function openRelayEditor(id) {
  const rid = Number(id);
  const gen = ++state.gens.relayOpen;
  state.selectedRelayId = rid;
  renderRelayList();
  const detail = await api(`/api/relays/${rid}`);
  if (!isCurrentGeneration(gen, state.gens.relayOpen)) return;
  if (state.selectedRelayId !== detail.id) return;
  applyRelayEditor(detail, { clearMemberSelection: true, forceFill: true });
}

async function loadRelays() {
  const gen = ++state.gens.relays;
  const relays = await api("/api/relays");
  if (!isCurrentGeneration(gen, state.gens.relays)) return;
  state.relays = relays;
  if (state.selectedRelayId && !state.relays.some((r) => r.id === state.selectedRelayId)) {
    state.selectedRelayId = null;
    state.currentRelay = null;
  }
  renderRelayList();
  if (!state.selectedRelayId) showRelayEmpty();
}

function renderRelayEPGChecks() {
  const box = document.getElementById("relay-epg-checks");
  if (!state.currentRelay) return;
  const selected = new Set(state.currentRelay.epg_source_ids || []);
  if (!state.epgs.length) {
    box.innerHTML = `<div class="empty-list">No EPG sources available</div>`;
    return;
  }
  box.innerHTML = state.epgs.map((src) => `
    <label><input type="checkbox" data-epg-id="${src.id}" ${selected.has(src.id) ? "checked" : ""}/> ${esc(src.name)}</label>
  `).join("");
}

function syncMemberSelectionDOM() {
  document.querySelectorAll("#relay-lineup input[data-select-member]").forEach((cb) => {
    const id = Number(cb.dataset.selectMember);
    const checked = state.selected.members.has(id);
    cb.checked = checked;
    cb.closest(".member")?.classList.toggle("selected", checked);
  });
  updateBulkBar("members", "member-bulk", "member-bulk-count");
}

function renderLineup() {
  const root = document.getElementById("relay-lineup");
  if (!state.currentRelay) return;
  const groups = [...(state.currentRelay.groups || [])].sort((a, b) => a.sort_order - b.sort_order);
  const members = state.currentRelay.memberships || [];
  pruneSelection("members", new Set(members.map((m) => m.id)));
  if (!groups.length) {
    root.innerHTML = `<div class="empty-list">No groups. Add a group first.</div>`;
    updateBulkBar("members", "member-bulk", "member-bulk-count");
    return;
  }
  const byGroup = groupMembershipsByGroup(members);
  root.innerHTML = groups.map((g) => {
    const gm = [...(byGroup.get(g.id) || [])].sort((a, b) => a.sort_order - b.sort_order);
    return `<section class="group" data-group-id="${g.id}" draggable="true">
      <div class="group-head">
        <div><span class="handle">☰</span> <strong>${esc(g.name)}</strong> <span class="muted">(${gm.length})</span></div>
        <div class="row">
          <button type="button" class="small" data-rename-group="${g.id}">Rename</button>
          <button type="button" class="small danger" data-del-group="${g.id}">Delete</button>
        </div>
      </div>
      <div class="group-members" data-group-id="${g.id}">
        ${gm.map((m) => {
          const checked = state.selected.members.has(m.id) ? "checked" : "";
          const selected = checked ? "selected" : "";
          return `<div class="member ${selected}" draggable="true" data-member-id="${m.id}" data-group-id="${g.id}">
            <input type="checkbox" data-select-member="${m.id}" ${checked} />
            <span class="handle">⋮⋮</span>
            <div>
              <div class="member-title">#${esc(m.number)} · ${esc(m.channel_name)}</div>
              <div class="sub">tvg-id=${esc(m.tvg_id || "—")}</div>
            </div>
            <div class="row">
              <button type="button" class="small" data-copy-stream="${esc(m.channel_id)}" title="Copy stream URL">Stream</button>
              <button type="button" class="small" data-edit-member="${m.id}">Edit</button>
              <button type="button" class="small danger" data-del-member="${m.id}">Remove</button>
            </div>
          </div>`;
        }).join("") || `<div class="empty-list">Drop channels here</div>`}
      </div>
    </section>`;
  }).join("");
  updateBulkBar("members", "member-bulk", "member-bulk-count");
  bindDragDrop();
}

function bindDragDrop() {
  const root = document.getElementById("relay-lineup");
  root.querySelectorAll(".member").forEach((el) => {
    el.addEventListener("dragstart", (e) => {
      if (e.target.closest("input,button")) {
        e.preventDefault();
        return;
      }
      state.dragMemberId = Number(el.dataset.memberId);
      state.dragGroupId = null;
      el.classList.add("dragging");
      e.dataTransfer.setData("text/plain", `member:${state.dragMemberId}`);
    });
    el.addEventListener("dragend", () => el.classList.remove("dragging"));
  });
  root.querySelectorAll(".group").forEach((el) => {
    el.addEventListener("dragstart", (e) => {
      if (e.target.closest(".member")) return;
      state.dragGroupId = Number(el.dataset.groupId);
      state.dragMemberId = null;
      e.dataTransfer.setData("text/plain", `group:${state.dragGroupId}`);
    });
    el.addEventListener("dragover", (e) => {
      e.preventDefault();
      el.classList.add("drag-over");
    });
    el.addEventListener("dragleave", () => el.classList.remove("drag-over"));
    el.addEventListener("drop", async (e) => {
      e.preventDefault();
      el.classList.remove("drag-over");
      const targetGroupId = Number(el.dataset.groupId);
      if (state.dragMemberId) {
        await dropMember(state.dragMemberId, targetGroupId, e.target.closest(".member")?.dataset.memberId);
      } else if (state.dragGroupId && state.dragGroupId !== targetGroupId) {
        await dropGroup(state.dragGroupId, targetGroupId);
      }
      state.dragMemberId = null;
      state.dragGroupId = null;
    });
  });
}

async function persistLayout(groups) {
  try {
    const detail = await api(`/api/relays/${state.currentRelay.id}/layout`, {
      method: "PUT",
      body: JSON.stringify({ groups }),
    });
    applyRelayLineup(detail);
  } catch (err) {
    toast.error("Layout save failed", err.message);
    await refreshRelayLineup();
  }
}

async function dropMember(memberId, targetGroupId, beforeMemberId) {
  const groups = [...state.currentRelay.groups].sort((a, b) => a.sort_order - b.sort_order);
  const byGroup = {};
  for (const g of groups) byGroup[g.id] = [];
  for (const m of [...state.currentRelay.memberships].sort((a, b) => a.sort_order - b.sort_order)) {
    byGroup[m.group_id].push(m.id);
  }
  for (const g of groups) byGroup[g.id] = byGroup[g.id].filter((id) => id !== memberId);
  const target = byGroup[targetGroupId] || [];
  if (beforeMemberId) {
    const idx = target.indexOf(Number(beforeMemberId));
    if (idx >= 0) target.splice(idx, 0, memberId);
    else target.push(memberId);
  } else target.push(memberId);
  byGroup[targetGroupId] = target;
  await persistLayout(groups.map((g) => ({
    id: g.id, name: g.name, membership_ids: byGroup[g.id] || [],
  })));
}

async function dropGroup(dragId, beforeId) {
  const groups = [...state.currentRelay.groups].sort((a, b) => a.sort_order - b.sort_order);
  const ordered = groups.map((g) => g.id).filter((id) => id !== dragId);
  const idx = ordered.indexOf(beforeId);
  if (idx >= 0) ordered.splice(idx, 0, dragId);
  else ordered.push(dragId);
  const byGroup = {};
  for (const m of [...state.currentRelay.memberships].sort((a, b) => a.sort_order - b.sort_order)) {
    (byGroup[m.group_id] ||= []).push(m.id);
  }
  const nameById = Object.fromEntries(groups.map((g) => [g.id, g.name]));
  await persistLayout(ordered.map((id) => ({
    id, name: nameById[id], membership_ids: byGroup[id] || [],
  })));
}

const MEMBER_NEW_GROUP = "__new__";

function showNewGroupField(on) {
  document.getElementById("member-new-group-wrap").classList.toggle("hidden", !on);
  if (!on) document.getElementById("member-new-group").value = "";
}

async function loadMemberTargets(relayId, member) {
  const gen = ++state.gens.memberRelay;
  const detail = state.currentRelay?.id === relayId ? state.currentRelay : await api(`/api/relays/${relayId}`);
  if (!isCurrentGeneration(gen, state.gens.memberRelay)) return false;
  const groupSel = document.getElementById("member-group");
  const epgSel = document.getElementById("member-epg-source");
  const groups = detail.groups || [];
  groupSel.innerHTML = groups.map((g) => `<option value="${g.id}">${esc(g.name)}</option>`).join("")
    + `<option value="${MEMBER_NEW_GROUP}">(Create New Group)</option>`;
  const epgs = state.epgs.filter((e) => (detail.epg_source_ids || []).includes(e.id));
  epgSel.innerHTML = `<option value="">(none)</option>` + epgs.map((e) => `<option value="${e.id}">${esc(e.name)}</option>`).join("");
  groupSel.value = member?.group_id ?? groups[0]?.id ?? MEMBER_NEW_GROUP;
  epgSel.value = member?.epg_source_id ?? "";
  showNewGroupField(groupSel.value === MEMBER_NEW_GROUP);
  return true;
}

async function openMemberDialog(member, channelId) {
  const relayWrap = document.getElementById("member-relay-wrap");
  const relaySel = document.getElementById("member-relay");
  const channelWrap = document.getElementById("member-channel-wrap");
  const channelSel = document.getElementById("member-channel");

  if (channelId) {
    const ch = state.channels.find((c) => c.id === channelId);
    if (!ch) return;
    const relays = state.relays.filter((r) => !(ch.relay_slugs || []).includes(r.slug));
    if (!state.relays.length) { toast.info("Create a relay first"); return; }
    if (!relays.length) { toast.info("Already in every relay"); return; }
    relayWrap.classList.remove("hidden");
    channelWrap.classList.add("hidden");
    relaySel.innerHTML = relays.map((r) => `<option value="${r.id}">${esc(r.name)}</option>`).join("");
    channelSel.innerHTML = `<option value="${ch.id}">${esc(ch.name)}</option>`;
    document.getElementById("member-dialog-title").textContent = "Add to Relay";
  } else if (!state.currentRelay) {
    return;
  } else {
    relayWrap.classList.add("hidden");
    channelWrap.classList.remove("hidden");
    relaySel.innerHTML = `<option value="${state.currentRelay.id}">${esc(state.currentRelay.name)}</option>`;
    channelSel.innerHTML = state.channels.map((c) => `<option value="${c.id}">${esc(c.name)}</option>`).join("");
    document.getElementById("member-dialog-title").textContent = member ? "Edit membership" : "Add Channel";
  }

  document.getElementById("member-id").value = member?.id || "";
  document.getElementById("member-channel").value = channelId || member?.channel_id || state.channels[0]?.id || "";
  document.getElementById("member-tvg-id").value = member?.tvg_id || "";
  document.getElementById("member-error").textContent = "";
  try {
    if (!await loadMemberTargets(Number(relaySel.value), member)) return;
  } catch (err) {
    toast.error("Failed to load relay", err.message);
    return;
  }
  document.getElementById("member-dialog").showModal();
  await refreshTvgSuggestions();
}

async function refreshTvgSuggestions() {
  const sourceID = document.getElementById("member-epg-source").value;
  const q = document.getElementById("member-tvg-id").value;
  const list = document.getElementById("tvg-suggestions");
  const gen = ++state.gens.tvg;
  if (!sourceID) {
    list.innerHTML = "";
    return;
  }
  try {
    const items = await api(`/api/epg/sources/${sourceID}/channels?q=${encodeURIComponent(q)}&limit=30`);
    if (!isCurrentGeneration(gen, state.gens.tvg)) return;
    list.innerHTML = items.map((c) => {
      const label = c.display_names?.[0] ? `${c.id} — ${c.display_names[0]}` : c.id;
      return `<option value="${esc(c.id)}">${esc(label)}</option>`;
    }).join("");
  } catch {
    if (!isCurrentGeneration(gen, state.gens.tvg)) return;
    list.innerHTML = "";
  }
}

function wireRelays() {
  document.getElementById("relay-search").addEventListener("input", (e) => {
    state.filter.relays = e.target.value;
    renderRelayList();
  });

  document.getElementById("btn-new-relay").addEventListener("click", () => {
    document.getElementById("new-relay-name").value = "";
    document.getElementById("new-relay-slug").value = "";
    document.getElementById("relay-error").textContent = "";
    document.getElementById("relay-dialog").showModal();
  });
  document.getElementById("relay-cancel").addEventListener("click", () => document.getElementById("relay-dialog").close());
  document.getElementById("relay-create").addEventListener("click", async (e) => {
    e.preventDefault();
    const errEl = document.getElementById("relay-error");
    try {
      const detail = await api("/api/relays", {
        method: "POST",
        body: JSON.stringify({
          name: document.getElementById("new-relay-name").value,
          slug: document.getElementById("new-relay-slug").value,
        }),
      });
      document.getElementById("relay-dialog").close();
      applyRelayEditor(detail, { clearMemberSelection: true, forceFill: true });
      showTab("relays");
      toast.success("Relay created", detail.slug);
    } catch (err) { errEl.textContent = err.message; toast.error("Create failed", err.message); }
  });
  document.getElementById("relay-list").addEventListener("click", async (e) => {
    const t = e.target;
    if (!(t instanceof HTMLElement)) return;
    if (t.dataset.selectRelay) return;
    const item = t.closest("[data-open-relay]");
    if (item) await openRelayEditor(item.dataset.openRelay);
  });
  document.getElementById("relay-list").addEventListener("change", (e) => {
    const t = e.target;
    if (!(t instanceof HTMLInputElement) || !t.dataset.selectRelay) return;
    const id = Number(t.dataset.selectRelay);
    if (t.checked) state.selected.relays.add(id);
    else state.selected.relays.delete(id);
    syncRelaySelectionDOM();
  });
  document.getElementById("btn-relay-select-all").addEventListener("click", () => {
    filteredRelays().forEach((r) => state.selected.relays.add(r.id));
    syncRelaySelectionDOM();
  });
  document.getElementById("btn-clear-relays").addEventListener("click", () => {
    state.selected.relays.clear();
    syncRelaySelectionDOM();
  });
  document.getElementById("btn-del-relays").addEventListener("click", async () => {
    const ids = [...state.selected.relays];
    const res = await bulkDelete("relay(s)", ids, (id) => api(`/api/relays/${id}`, { method: "DELETE" }));
    if (!res) return;
    state.relays = removeByIds(state.relays, res.successfulIDs);
    for (const id of res.successfulIDs) state.selected.relays.delete(Number(id));
    if (editorClearedByDeletes(state.selectedRelayId, res.successfulIDs)) {
      state.selectedRelayId = null;
      state.currentRelay = null;
      showRelayEmpty();
    }
    renderRelayList();
    await loadChannels();
  });
  document.getElementById("btn-copy-playlist").addEventListener("click", () => {
    const url = document.getElementById("btn-copy-playlist").dataset.url;
    copyText("M3U", url);
  });
  document.getElementById("btn-copy-epg").addEventListener("click", () => {
    const url = document.getElementById("btn-copy-epg").dataset.url;
    copyText("EPG", url);
  });
  document.getElementById("btn-del-relay").addEventListener("click", async () => {
    if (!state.currentRelay || !confirm("Delete this relay?")) return;
    try {
      const id = state.currentRelay.id;
      await api(`/api/relays/${id}`, { method: "DELETE" });
      state.relays = removeByIds(state.relays, [id]);
      state.selected.relays.delete(id);
      state.selectedRelayId = null;
      state.currentRelay = null;
      showRelayEmpty();
      renderRelayList();
      await loadChannels();
      toast.success("Relay deleted");
    } catch (err) { toast.error("Delete failed", err.message); }
  });
  document.getElementById("btn-save-relay").addEventListener("click", async () => {
    if (!state.currentRelay) return;
    try {
      const previousSlug = state.currentRelay.slug;
      const detail = await api(`/api/relays/${state.currentRelay.id}`, {
        method: "PUT",
        body: JSON.stringify({
          name: document.getElementById("relay-name").value,
          slug: document.getElementById("relay-slug").value,
        }),
      });
      applyRelayEditor(detail, { forceFill: true });
      if (previousSlug !== detail.slug) await loadChannels();
      toast.success("Relay saved");
    } catch (err) { toast.error("Save failed", err.message); }
  });
  document.getElementById("btn-add-group").addEventListener("click", async () => {
    const name = prompt("Group name", "New group");
    if (!name) return;
    try {
      await api(`/api/relays/${state.currentRelay.id}/groups`, { method: "POST", body: JSON.stringify({ name }) });
      await refreshRelayLineup();
      toast.success("Group added", name);
    } catch (err) { toast.error("Add group failed", err.message); }
  });
  document.getElementById("btn-add-member").addEventListener("click", async () => {
    if (!state.channels.length) { toast.info("Add channels first"); return; }
    await openMemberDialog(null);
  });
  document.getElementById("btn-clear-members").addEventListener("click", () => {
    state.selected.members.clear();
    syncMemberSelectionDOM();
  });
  document.getElementById("btn-del-members").addEventListener("click", async () => {
    if (!state.currentRelay) return;
    const ids = [...state.selected.members];
    const res = await bulkDelete("group channel(s)", ids, (id) =>
      api(`/api/relays/${state.currentRelay.id}/memberships/${id}`, { method: "DELETE" }));
    if (!res) return;
    for (const id of res.successfulIDs) state.selected.members.delete(Number(id));
    await refreshRelayLineup();
    await loadChannels();
  });
  document.getElementById("member-cancel").addEventListener("click", () => document.getElementById("member-dialog").close());
  document.querySelector("#member-dialog form").addEventListener("submit", (e) => {
    e.preventDefault();
    document.getElementById("member-save").click();
  });
  document.getElementById("member-relay").addEventListener("change", async () => {
    document.getElementById("member-tvg-id").value = "";
    document.getElementById("member-error").textContent = "";
    try {
      if (!await loadMemberTargets(Number(document.getElementById("member-relay").value), null)) return;
      await refreshTvgSuggestions();
    } catch (err) {
      document.getElementById("member-error").textContent = err.message;
    }
  });
  document.getElementById("member-group").addEventListener("change", () => {
    const creating = document.getElementById("member-group").value === MEMBER_NEW_GROUP;
    showNewGroupField(creating);
    if (creating) document.getElementById("member-new-group").focus();
  });
  document.getElementById("member-tvg-id").addEventListener("input", () => {
    clearTimeout(window.__tvgTimer);
    window.__tvgTimer = setTimeout(refreshTvgSuggestions, 200);
  });
  document.getElementById("member-save").addEventListener("click", async (e) => {
    e.preventDefault();
    const errEl = document.getElementById("member-error");
    const saveBtn = document.getElementById("member-save");
    errEl.textContent = "";
    const relayId = Number(document.getElementById("member-relay").value);
    if (!relayId) { errEl.textContent = "Select a relay"; return; }
    const epgRaw = document.getElementById("member-epg-source").value;
    const channelId = document.getElementById("member-channel").value;
    if (!channelId) { errEl.textContent = "Select a channel"; return; }
    const id = document.getElementById("member-id").value;
    const relayName = document.getElementById("member-relay").selectedOptions[0]?.textContent || "";
    const groupRaw = document.getElementById("member-group").value;
    const newGroupName = document.getElementById("member-new-group").value.trim();
    if (groupRaw === MEMBER_NEW_GROUP && !newGroupName) { errEl.textContent = "Enter a group name"; return; }
    saveBtn.disabled = true;
    try {
      let groupId = Number(groupRaw);
      if (groupRaw === MEMBER_NEW_GROUP) {
        const g = await api(`/api/relays/${relayId}/groups`, { method: "POST", body: JSON.stringify({ name: newGroupName }) });
        groupId = g.id;
        const sel = document.getElementById("member-group");
        sel.insertBefore(new Option(g.name, g.id), sel.lastElementChild);
        sel.value = String(g.id);
        showNewGroupField(false);
      }
      const body = {
        channel_id: channelId,
        group_id: groupId,
        epg_source_id: epgRaw ? Number(epgRaw) : null,
        tvg_id: document.getElementById("member-tvg-id").value,
      };
      if (id) await api(`/api/relays/${relayId}/memberships/${id}`, { method: "PUT", body: JSON.stringify(body) });
      else await api(`/api/relays/${relayId}/memberships`, { method: "POST", body: JSON.stringify(body) });
      document.getElementById("member-dialog").close();
      if (state.currentRelay?.id === relayId) await refreshRelayLineup();
      await loadChannels();
      toast.success(id ? "Membership updated" : "Channel added to Relay", id ? "" : relayName);
    } catch (err) { errEl.textContent = err.message; toast.error("Save failed", err.message); }
    finally { saveBtn.disabled = false; }
  });
  document.getElementById("relay-lineup").addEventListener("change", (e) => {
    const t = e.target;
    if (!(t instanceof HTMLInputElement) || !t.dataset.selectMember) return;
    const id = Number(t.dataset.selectMember);
    if (t.checked) state.selected.members.add(id);
    else state.selected.members.delete(id);
    syncMemberSelectionDOM();
  });
  document.getElementById("relay-lineup").addEventListener("click", async (e) => {
    const t = e.target;
    if (!(t instanceof HTMLElement) || !state.currentRelay) return;
    if (t.dataset.renameGroup) {
      const g = state.currentRelay.groups.find((x) => String(x.id) === t.dataset.renameGroup);
      const name = prompt("Group name", g?.name || "");
      if (!name) return;
      try {
        await api(`/api/relays/${state.currentRelay.id}/groups/${t.dataset.renameGroup}`, {
          method: "PUT", body: JSON.stringify({ name }),
        });
        await refreshRelayLineup();
      } catch (err) { toast.error("Rename group failed", err.message); }
    } else if (t.dataset.delGroup) {
      if (!confirm("Delete group?")) return;
      try {
        const groupId = t.dataset.delGroup;
        await api(`/api/relays/${state.currentRelay.id}/groups/${groupId}`, { method: "DELETE" });
        for (const m of state.currentRelay.memberships) {
          if (String(m.group_id) === String(groupId)) state.selected.members.delete(Number(m.id));
        }
        await refreshRelayLineup();
        await loadChannels();
      } catch (err) { toast.error("Delete group failed", err.message); }
    } else if (t.dataset.copyStream) {
      copyText("Stream", channelStreamURL(t.dataset.copyStream));
    } else if (t.dataset.editMember) {
      const m = state.currentRelay.memberships.find((x) => String(x.id) === t.dataset.editMember);
      await openMemberDialog(m);
    } else if (t.dataset.delMember) {
      if (!confirm("Remove channel from relay?")) return;
      try {
        await api(`/api/relays/${state.currentRelay.id}/memberships/${t.dataset.delMember}`, { method: "DELETE" });
        state.selected.members.delete(Number(t.dataset.delMember));
        await refreshRelayLineup();
        await loadChannels();
        toast.success("Removed from group");
      } catch (err) { toast.error("Remove failed", err.message); }
    }
  });

  document.getElementById("btn-import-m3u").addEventListener("click", () => {
    document.getElementById("import-relay-name").value = "";
    document.getElementById("import-relay-slug").value = "";
    document.getElementById("import-url").value = "";
    document.getElementById("import-content").value = "";
    document.getElementById("import-file").value = "";
    document.getElementById("import-ignore-groups").checked = false;
    document.getElementById("import-epg").checked = true;
    document.getElementById("import-error").textContent = "";
    document.getElementById("import-dialog").showModal();
  });
  document.getElementById("import-cancel").addEventListener("click", () => document.getElementById("import-dialog").close());
  document.getElementById("import-file").addEventListener("change", async (e) => {
    const file = e.target.files && e.target.files[0];
    if (!file) return;
    document.getElementById("import-content").value = await file.text();
  });
  document.getElementById("import-save").addEventListener("click", async (e) => {
    e.preventDefault();
    const errEl = document.getElementById("import-error");
    const saveBtn = document.getElementById("import-save");
    errEl.textContent = "";
    saveBtn.disabled = true;
    const loading = toast.loading("Importing M3U…");
    try {
      const res = await api("/api/relays/import", {
        method: "POST",
        body: JSON.stringify({
          relay_name: document.getElementById("import-relay-name").value,
          relay_slug: document.getElementById("import-relay-slug").value,
          url: document.getElementById("import-url").value.trim(),
          content: document.getElementById("import-content").value,
          ignore_groups: document.getElementById("import-ignore-groups").checked,
          import_epg: document.getElementById("import-epg").checked,
        }),
      });
      document.getElementById("import-dialog").close();
      await Promise.all([
        loadChannels(),
        loadEPG({ withStatus: true }),
        loadRelays(),
      ]);
      showTab("relays");
      await openRelayEditor(res.relay_id);
      loading.update("success", `Imported "${res.slug}"`,
        `created ${res.channels_created}, reused ${res.channels_reused}, memberships ${res.memberships_created}`, 7000);
    } catch (err) {
      loading.update("error", "Import failed", err.message);
      errEl.textContent = err.message;
    } finally {
      saveBtn.disabled = false;
    }
  });

  document.getElementById("btn-save-relay-epg").addEventListener("click", async () => {
    if (!state.currentRelay) return;
    try {
      const ids = [...document.querySelectorAll("#relay-epg-checks input:checked")].map((el) => Number(el.dataset.epgId));
      const detail = await api(`/api/relays/${state.currentRelay.id}/epg-sources`, {
        method: "PUT",
        body: JSON.stringify({ epg_source_ids: ids }),
      });
      state.currentRelay = {
        ...state.currentRelay,
        epg_source_ids: detail.epg_source_ids,
      };
      renderRelayEPGChecks();
      state.editors.relayEpg.baseline = readRelayEpgDraft();
      toast.success("EPG selection saved");
    } catch (err) { toast.error("Save failed", err.message); }
  });
  document.getElementById("member-epg-source").addEventListener("change", refreshTvgSuggestions);
}
