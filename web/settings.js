/* Settings tab */
async function loadSettings() {
  const errEl = document.getElementById("settings-error");
  errEl.textContent = "";
  try {
    const data = await api("/api/settings");
    fillSettingsForm(data);
  } catch (err) {
    errEl.textContent = err.message;
    toast.error("Failed to load settings", err.message);
  }
}

function fillSettingsForm(data) {
  const t = data.transcode || {};
  document.getElementById("settings-crf").value = t.video_crf ?? 23;
  document.getElementById("settings-preset").value = t.video_preset || "veryfast";
  document.getElementById("settings-audio-bitrate").value = t.audio_bitrate_kbps ?? 128;
  document.getElementById("settings-max-height").value = t.max_height ?? 0;
  document.getElementById("settings-startup-timeout").value = t.startup_timeout_seconds ?? 30;
  const status = document.getElementById("settings-ffmpeg-status");
  const path = data.ffmpeg_path || "ffmpeg";
  status.textContent = data.ffmpeg_available
    ? `ffmpeg: ${path}`
    : `ffmpeg missing: ${path}`;
  status.classList.toggle("warn", !data.ffmpeg_available);

  const sys = data.system || {};
  const rows = [
    ["Listen address", sys.listen_addr],
    ["Base URL", sys.base_url || "(auto from request)"],
    ["Trust proxy", String(!!sys.trust_proxy)],
    ["Data directory", sys.data_dir],
    ["Database", sys.database_path],
    ["Log level", sys.log_level],
    ["ffmpeg path", sys.ffmpeg_path],
    ["Relay buffer size", sys.relay_buffer_size],
    ["Relay idle timeout", sys.relay_idle_timeout],
    ["Relay connect timeout", sys.relay_conn_timeout],
    ["EPG max bytes", sys.epg_max_bytes],
    ["EPG default interval", sys.epg_default_interval],
  ];
  document.getElementById("settings-system").innerHTML = rows.map(([k, v]) => (
    `<div><dt>${esc(String(k))}</dt><dd>${esc(v == null || v === "" ? "—" : String(v))}</dd></div>`
  )).join("");
}

function wireSettings() {
  document.getElementById("settings-form").addEventListener("submit", async (e) => {
    e.preventDefault();
    const errEl = document.getElementById("settings-error");
    errEl.textContent = "";
    const body = {
      transcode: {
        video_crf: Number(document.getElementById("settings-crf").value),
        video_preset: document.getElementById("settings-preset").value,
        audio_bitrate_kbps: Number(document.getElementById("settings-audio-bitrate").value),
        max_height: Number(document.getElementById("settings-max-height").value),
        startup_timeout_seconds: Number(document.getElementById("settings-startup-timeout").value),
      },
    };
    try {
      const saved = await api("/api/settings", { method: "PUT", body: JSON.stringify(body) });
      fillSettingsForm(saved);
      toast.success("Settings saved");
    } catch (err) {
      errEl.textContent = err.message;
      toast.error("Save failed", err.message);
    }
  });
}
