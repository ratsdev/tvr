/* Settings tab */
const DEFAULT_BRAND_ICON = "/assets/brand.svg";
const DEFAULT_BRAND_TITLE = "IPTV Relay";

let savedBrandIcon = "";
let pendingBrandUpload = null; // null = keep saved, "" = reset, data URL = new upload

function settingsBrandError() {
  return document.getElementById("settings-brand-error");
}

function settingsFormError() {
  return document.getElementById("settings-error");
}

function clearSettingsErrors() {
  settingsBrandError().textContent = "";
  settingsFormError().textContent = "";
}

function isBrandSettingsError(message) {
  return /brand icon|brand title|icon must be|could not read brand icon/i.test(String(message || ""));
}

function displaySettingsError(message) {
  return String(message || "").replace(/^validation error:\s*/i, "");
}

async function loadSettings() {
  clearSettingsErrors();
  try {
    const data = await api("/api/settings");
    fillSettingsForm(data);
  } catch (err) {
    settingsFormError().textContent = err.message;
    toast.error("Failed to load settings", err.message);
  }
}

function brandIconSrc(brand) {
  return ((brand || {}).icon || "").trim() || DEFAULT_BRAND_ICON;
}

function brandTitleText(brand) {
  return ((brand || {}).title || "").trim() || DEFAULT_BRAND_TITLE;
}

function setBrandPreview(img, src) {
  if (!img) return;
  img.onerror = () => {
    img.onerror = null;
    img.src = DEFAULT_BRAND_ICON;
  };
  img.src = src;
}

function previewBrand(brand) {
  setBrandPreview(document.getElementById("settings-brand-preview"), brandIconSrc(brand));
}

function applyBrand(brand) {
  previewBrand(brand);
  setBrandPreview(document.getElementById("brand-icon"), brandIconSrc(brand));
  document.getElementById("brand-title").textContent = brandTitleText(brand);
}

function isRemoteBrandIcon(icon) {
  return /^https?:\/\//i.test(icon || "");
}

function fillSettingsForm(data) {
  const b = data.brand || {};
  const icon = (b.icon || "").trim();
  savedBrandIcon = icon;
  pendingBrandUpload = null;
  document.getElementById("settings-brand-icon").value = isRemoteBrandIcon(icon) ? icon : "";
  document.getElementById("settings-brand-title").value = b.title || DEFAULT_BRAND_TITLE;
  applyBrand({ icon, title: b.title });
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

function brandIconToSave(iconEl) {
  if (pendingBrandUpload !== null) return pendingBrandUpload;
  const typed = iconEl.value.trim();
  if (typed) return typed;
  if ((savedBrandIcon || "").startsWith("/brand-icon")) return savedBrandIcon;
  return "";
}

function wireSettings() {
  const iconEl = document.getElementById("settings-brand-icon");
  const titleEl = document.getElementById("settings-brand-title");
  const fileEl = document.getElementById("settings-brand-icon-file");
  const preview = () => previewBrand({
    icon: pendingBrandUpload !== null ? pendingBrandUpload : (iconEl.value || savedBrandIcon),
  });
  iconEl.addEventListener("input", () => {
    pendingBrandUpload = null;
    settingsBrandError().textContent = "";
    preview();
  });
  document.getElementById("settings-brand-icon-reset").addEventListener("click", () => {
    pendingBrandUpload = "";
    iconEl.value = "";
    settingsBrandError().textContent = "";
    preview();
  });
  onFilePick(document.getElementById("settings-brand-icon-pick"), fileEl, async (file) => {
    if (file.type && !file.type.startsWith("image/")) {
      settingsBrandError().textContent = "Icon must be an image";
      return;
    }
    if (file.size > 8 * 1024 * 1024) {
      settingsBrandError().textContent = "Icon is too large";
      return;
    }
    try {
      settingsBrandError().textContent = "";
      pendingBrandUpload = await fileToSmallIconDataURL(file);
      iconEl.value = "";
      preview();
    } catch (err) {
      settingsBrandError().textContent = err.message;
    }
  });
  document.getElementById("settings-form").addEventListener("submit", async (e) => {
    e.preventDefault();
    clearSettingsErrors();
    const body = {
      brand: {
        icon: brandIconToSave(iconEl),
        title: titleEl.value,
      },
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
      const dest = isBrandSettingsError(err.message) ? settingsBrandError() : settingsFormError();
      dest.textContent = displaySettingsError(err.message);
      toast.error("Save failed", err.message);
    }
  });
}

function readFileAsDataURL(file) {
  return new Promise((resolve, reject) => {
    const reader = new FileReader();
    reader.onload = () => resolve(String(reader.result || ""));
    reader.onerror = () => reject(new Error("failed to read file"));
    reader.readAsDataURL(file);
  });
}

function loadImage(src) {
  return new Promise((resolve, reject) => {
    const img = new Image();
    img.onload = () => resolve(img);
    img.onerror = () => reject(new Error("browser could not load this image"));
    img.src = src;
  });
}

async function fileToSmallIconDataURL(file) {
  const dataURL = await readFileAsDataURL(file);
  const img = await loadImage(dataURL);
  if (!img.width || !img.height) {
    throw new Error("could not read image size");
  }
  if (Math.abs(img.width - img.height) / Math.max(img.width, img.height) > 0.02) {
    throw new Error("icon must be square");
  }
  const size = Math.min(128, img.width);
  const canvas = document.createElement("canvas");
  canvas.width = size;
  canvas.height = size;
  const ctx = canvas.getContext("2d");
  if (!ctx) {
    throw new Error("could not resize icon");
  }
  ctx.drawImage(img, 0, 0, size, size);
  return canvas.toDataURL("image/png");
}
