const assert = require("node:assert/strict");
const test = require("node:test");
const {
  snapshotsEqual,
  isDomainDirty,
  shouldFillEditor,
  isCurrentGeneration,
  editorClearedByDeletes,
  shouldResetViewerScroll,
  assignProgrammeLanes,
  epgChannelLabel,
  membershipEPGLine,
  epgChannelHint,
  groupMembershipsByGroup,
} = require("./state.js");

test("snapshotsEqual and isDomainDirty", () => {
  assert.equal(snapshotsEqual({ a: 1 }, { a: 1 }), true);
  assert.equal(snapshotsEqual({ a: 1 }, { a: 2 }), false);
  assert.equal(isDomainDirty(null, { a: 1 }), false);
  assert.equal(isDomainDirty({ a: 1 }, { a: 1 }), false);
  assert.equal(isDomainDirty({ a: 1 }, { a: 2 }), true);
});

test("shouldFillEditor respects active entity and dirty domain", () => {
  assert.equal(shouldFillEditor({
    activeEntityId: 1, responseEntityId: 1, domainDirty: false,
  }), true);
  assert.equal(shouldFillEditor({
    activeEntityId: "1", responseEntityId: 1, domainDirty: false,
  }), true);
  assert.equal(shouldFillEditor({
    activeEntityId: "a1b2c3d4-e5f6-7890-abcd-ef1234567890",
    responseEntityId: "a1b2c3d4-e5f6-7890-abcd-ef1234567890",
    domainDirty: false,
  }), true);
  assert.equal(shouldFillEditor({
    activeEntityId: 1, responseEntityId: 1, domainDirty: true,
  }), false);
  assert.equal(shouldFillEditor({
    activeEntityId: 1, responseEntityId: 2, domainDirty: false,
  }), false);
  assert.equal(shouldFillEditor({
    activeEntityId: null, responseEntityId: 1, domainDirty: false,
  }), false);
  assert.equal(shouldFillEditor({
    activeEntityId: 1, responseEntityId: 2, domainDirty: true, force: true,
  }), true);
});

test("generation tokens discard stale responses", () => {
  assert.equal(isCurrentGeneration(3, 3), true);
  assert.equal(isCurrentGeneration(2, 3), false);
});

test("editorClearedByDeletes only when selected id succeeded", () => {
  assert.equal(editorClearedByDeletes(5, [1, 5, 9]), true);
  assert.equal(editorClearedByDeletes("5", [1, 5, 9]), true);
  assert.equal(editorClearedByDeletes("uuid-1", ["uuid-1", "uuid-2"]), true);
  assert.equal(editorClearedByDeletes(5, [1, 9]), false);
  assert.equal(editorClearedByDeletes(null, [1]), false);
});

test("viewer scroll resets only on window change or Now", () => {
  assert.equal(shouldResetViewerScroll({}), false);
  assert.equal(shouldResetViewerScroll({ windowChanged: true }), true);
  assert.equal(shouldResetViewerScroll({ scrollToNow: true }), true);
  assert.equal(shouldResetViewerScroll({ windowChanged: false, scrollToNow: false }), false);
});

test("assignProgrammeLanes computes laneCount once", () => {
  const { programmes, laneCount } = assignProgrammeLanes([
    { id: "a", startMs: 0, stopMs: 100 },
    { id: "b", startMs: 50, stopMs: 150 },
    { id: "c", startMs: 100, stopMs: 200 },
  ]);
  assert.equal(laneCount, 2);
  assert.equal(programmes.length, 3);
  assert.equal(programmes.find((p) => p.id === "a").lane, 0);
  assert.equal(programmes.find((p) => p.id === "b").lane, 1);
  assert.equal(programmes.find((p) => p.id === "c").lane, 0);
  assert.equal(programmes.every((p) => p.laneCount === undefined), true);
});

test("epgChannelLabel", () => {
  assert.equal(epgChannelLabel({ id: "cnn", display_names: ["CNN"] }), "CNN (cnn)");
  assert.equal(epgChannelLabel({ id: "news" }), "news");
  assert.equal(epgChannelLabel({ id: "" }), "");
});

test("membershipEPGLine includes source and tvg-id", () => {
  assert.equal(membershipEPGLine("Guide", "cnn"), "EPG:Guide ID:cnn");
  assert.equal(membershipEPGLine("", "cnn"), "EPG:— ID:cnn");
  assert.equal(membershipEPGLine("Guide", ""), "EPG:Guide ID:—");
  assert.equal(membershipEPGLine("", ""), "EPG:— ID:—");
});

test("epgChannelHint covers search paging", () => {
  assert.equal(epgChannelHint(30, 30), "");
  assert.equal(epgChannelHint(50, 1500), "Showing 50 of 1500");
  assert.equal(epgChannelHint(0, 0), "No matching Channels");
  assert.equal(epgChannelHint(50, 180), "Showing 50 of 180");
});

test("groupMembershipsByGroup pre-groups with Map", () => {
  const map = groupMembershipsByGroup([
    { id: 1, group_id: 10 },
    { id: 2, group_id: 20 },
    { id: 3, group_id: 10 },
  ]);
  assert.equal(map.get(10).length, 2);
  assert.equal(map.get(20).length, 1);
  assert.equal(map.get(10)[0].id, 1);
  assert.equal(map.get(10)[1].id, 3);
});
