/** Pure UI state-transition helpers (browser + Node). */

function snapshotsEqual(a, b) {
  return JSON.stringify(a) === JSON.stringify(b);
}

function isDomainDirty(baseline, draft) {
  if (baseline == null) return false;
  return !snapshotsEqual(baseline, draft);
}

/**
 * Whether an async response should refill editor fields.
 * force=true for explicit user selection / save that should replace the draft.
 */
function shouldFillEditor({
  activeEntityId,
  responseEntityId,
  domainDirty = false,
  force = false,
} = {}) {
  if (force) return true;
  if (activeEntityId == null || responseEntityId == null) return false;
  if (String(activeEntityId) !== String(responseEntityId)) return false;
  return !domainDirty;
}

function isCurrentGeneration(token, current) {
  return token === current;
}

/** True when the open editor entity was among successfully deleted IDs. */
function editorClearedByDeletes(selectedId, successfulIDs) {
  if (selectedId == null) return false;
  const set = new Set((successfulIDs || []).map(String));
  return set.has(String(selectedId));
}

/**
 * Viewer scroll resets only when the time window changes or Now is pressed.
 */
function shouldResetViewerScroll({ windowChanged = false, scrollToNow = false } = {}) {
  return !!(windowChanged || scrollToNow);
}

/**
 * Pack programmes into non-overlapping lanes. laneCount is computed once.
 * @returns {{ programmes: object[], laneCount: number }}
 */
function assignProgrammeLanes(programmes) {
  const sorted = [...programmes].sort((a, b) => a.startMs - b.startMs || a.stopMs - b.stopMs);
  const laneEnds = [];
  const laid = sorted.map((p) => {
    let lane = laneEnds.findIndex((end) => end <= p.startMs);
    if (lane < 0) {
      lane = laneEnds.length;
      laneEnds.push(0);
    }
    laneEnds[lane] = p.stopMs;
    return { ...p, lane };
  });
  return { programmes: laid, laneCount: Math.max(1, laneEnds.length) };
}

/** Pre-group memberships by group_id before rendering. */
function groupMembershipsByGroup(memberships) {
  const map = new Map();
  for (const m of memberships || []) {
    let list = map.get(m.group_id);
    if (!list) {
      list = [];
      map.set(m.group_id, list);
    }
    list.push(m);
  }
  return map;
}

if (typeof module !== "undefined" && module.exports) {
  module.exports = {
    snapshotsEqual,
    isDomainDirty,
    shouldFillEditor,
    isCurrentGeneration,
    editorClearedByDeletes,
    shouldResetViewerScroll,
    assignProgrammeLanes,
    groupMembershipsByGroup,
  };
}
