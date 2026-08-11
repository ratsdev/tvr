/** Pure timeline geometry helpers (browser + Node). */

const TIMELINE_PPM = 4; // pixels per minute
const TIMELINE_WINDOW_MS = 6 * 60 * 60 * 1000;

function timelinePixelsPerMinute() {
  return TIMELINE_PPM;
}

function timelineWindowMs() {
  return TIMELINE_WINDOW_MS;
}

/** Snap a "Now" window centered around `now`, default 6h with now at 25% from left. */
function timelineNowWindow(now = Date.now(), windowMs = TIMELINE_WINDOW_MS) {
  const lead = Math.floor(windowMs * 0.25);
  const from = now - lead;
  return { from, to: from + windowMs };
}

function timelineShiftWindow(fromMs, toMs, dir) {
  const span = toMs - fromMs;
  const delta = dir * span;
  return { from: fromMs + delta, to: toMs + delta };
}

/**
 * Clip a programme into the visible window.
 * Returns null when there is no overlap (half-open [from, to)).
 */
function programmeLayout(startMs, stopMs, windowFromMs, windowToMs, ppm = TIMELINE_PPM) {
  if (!(stopMs > startMs) || !(windowToMs > windowFromMs)) return null;
  const clipStart = Math.max(startMs, windowFromMs);
  const clipStop = Math.min(stopMs, windowToMs);
  if (!(clipStop > clipStart)) return null;
  const left = ((clipStart - windowFromMs) / 60000) * ppm;
  const width = Math.max(((clipStop - clipStart) / 60000) * ppm, 2);
  return { left, width, clippedStart: clipStart !== startMs, clippedStop: clipStop !== stopMs };
}

function timelineWidthPx(windowMs = TIMELINE_WINDOW_MS, ppm = TIMELINE_PPM) {
  return (windowMs / 60000) * ppm;
}

function hourMarks(windowFromMs, windowToMs) {
  const marks = [];
  const start = new Date(windowFromMs);
  start.setMinutes(0, 0, 0);
  if (start.getTime() < windowFromMs) start.setHours(start.getHours() + 1);
  for (let t = start.getTime(); t < windowToMs; t += 60 * 60 * 1000) {
    marks.push({
      at: t,
      left: ((t - windowFromMs) / 60000) * TIMELINE_PPM,
    });
  }
  return marks;
}

if (typeof module !== "undefined" && module.exports) {
  module.exports = {
    timelinePixelsPerMinute,
    timelineWindowMs,
    timelineNowWindow,
    timelineShiftWindow,
    programmeLayout,
    timelineWidthPx,
    hourMarks,
  };
}
