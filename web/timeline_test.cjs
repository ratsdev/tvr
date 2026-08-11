const assert = require("node:assert/strict");
const test = require("node:test");
const {
  programmeLayout,
  timelineNowWindow,
  timelineShiftWindow,
  timelineWidthPx,
} = require("./timeline.js");

test("programmeLayout clips to window edges", () => {
  const from = Date.parse("2026-01-01T12:00:00Z");
  const to = from + 6 * 3600 * 1000;
  const ppm = 4;
  const mid = programmeLayout(from + 3600e3, from + 2 * 3600e3, from, to, ppm);
  assert.equal(mid.left, 60 * ppm);
  assert.equal(mid.width, 60 * ppm);

  const leftClip = programmeLayout(from - 1800e3, from + 1800e3, from, to, ppm);
  assert.equal(leftClip.left, 0);
  assert.equal(leftClip.width, 30 * ppm);
  assert.equal(leftClip.clippedStart, true);

  const rightClip = programmeLayout(to - 1800e3, to + 1800e3, from, to, ppm);
  assert.equal(rightClip.left, (6 * 60 - 30) * ppm);
  assert.equal(rightClip.width, 30 * ppm);
  assert.equal(rightClip.clippedStop, true);

  assert.equal(programmeLayout(to, to + 3600e3, from, to, ppm), null);
  assert.equal(programmeLayout(from - 3600e3, from, from, to, ppm), null);
});

test("now and shift windows", () => {
  const now = Date.parse("2026-01-01T12:00:00Z");
  const w = timelineNowWindow(now, 6 * 3600e3);
  assert.equal(w.to - w.from, 6 * 3600e3);
  assert.ok(now >= w.from && now < w.to);
  const next = timelineShiftWindow(w.from, w.to, 1);
  assert.equal(next.from, w.to);
  assert.equal(timelineWidthPx(6 * 3600e3, 4), 6 * 60 * 4);
});
