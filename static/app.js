/* tvr admin UI entry */
function wireAll() {
  wireCore();
  wireChannels();
  wireEPGs();
  wireRelays();
  wireViewer();
  wireSettings();
}

async function pollStatus() {
  if (document.hidden) return;
  switch (currentTab()) {
    case "epgs":
      setEPGStatusHTML(await api("/api/epg/status"));
      break;
    case "channels":
      await refreshChannelStatuses();
      break;
  }
}

async function boot() {
  const healthP = api("/api/health").then((health) => {
    state.baseURL = (health.base_url || location.origin || "").replace(/\/$/, "");
    document.getElementById("sidebar-version").textContent = health.version ? `tvr ${health.version}` : "tvr";
  }).catch(() => {
    state.baseURL = location.origin;
  });
  await Promise.all([healthP, loadChannels(), loadEPG(), loadRelays()]);
  pollStatus().catch(() => {});
  setInterval(() => { pollStatus().catch(() => {}); }, 5000);
  document.addEventListener("visibilitychange", () => {
    if (!document.hidden) pollStatus().catch(() => {});
  });
}

wireAll();
boot().catch((err) => toast.error("Failed to load UI", err.message));
