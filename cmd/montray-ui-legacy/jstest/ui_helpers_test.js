const test = require("node:test");
const assert = require("node:assert/strict");

const {
  snoozeDurationOptions,
  parseVisibilityPreference,
  visibilityPreference,
  heartbeatStatusClass,
  formatIncidentCount,
  formatSnoozedIncidentCount,
  summarizeServers,
  isIncidentStale,
  formatIncidentState,
  retainedSnoozeMenuKey,
  iconURL,
} = require("../assets/webroot/ui_helpers.js");

test("lists the supported snooze durations", () => {
  assert.deepEqual(snoozeDurationOptions, [
    "15m",
    "30m",
    "1h",
    "4h",
    "6h",
    "12h",
    "1d",
    "2d",
    "7d",
  ]);
});

test("parses saved visibility preferences", () => {
  assert.equal(parseVisibilityPreference("true", false), true);
  assert.equal(parseVisibilityPreference("false", true), false);
  assert.equal(parseVisibilityPreference(null, true), true);
  assert.equal(parseVisibilityPreference(null, false), false);
  assert.equal(parseVisibilityPreference("unexpected", true), true);
});

test("reads visibility preferences and falls back when storage fails", () => {
  const stored = new Map([
    ["visible", "true"],
    ["hidden", "false"],
  ]);
  const storage = () => ({getItem: (key) => stored.get(key) ?? null});

  assert.equal(visibilityPreference("visible", false, storage), true);
  assert.equal(visibilityPreference("hidden", true, storage), false);
  assert.equal(visibilityPreference("missing", true, storage), true);
  assert.equal(visibilityPreference("missing", false, storage), false);
  assert.equal(visibilityPreference("visible", true, () => {
    throw new Error("storage unavailable");
  }), true);
});

test("classifies heartbeat status at its boundaries", () => {
  const now = new Date("2026-08-26T16:00:00Z");
  const ago = (milliseconds) => new Date(now.getTime() - milliseconds);

  assert.equal(heartbeatStatusClass(null, now), "");
  assert.equal(heartbeatStatusClass("invalid", now), "");
  assert.equal(heartbeatStatusClass(new Date(now.getTime() + 1000), now), "");
  assert.equal(heartbeatStatusClass(ago(14999), now), "");
  assert.equal(heartbeatStatusClass(ago(15000), now), "status-warning");
  assert.equal(heartbeatStatusClass(ago(29999), now), "status-warning");
  assert.equal(heartbeatStatusClass(ago(30000), now), "status-disconnected");
});

test("formats incident counts", () => {
  assert.equal(formatIncidentCount(0), "0 incidents");
  assert.equal(formatIncidentCount(1), "1 incident");
  assert.equal(formatIncidentCount(2), "2 incidents");
  assert.equal(formatSnoozedIncidentCount(0), "0 snoozed incidents");
  assert.equal(formatSnoozedIncidentCount(1), "1 snoozed incident");
  assert.equal(formatSnoozedIncidentCount(2), "2 snoozed incidents");
});

test("summarizes server connectivity", () => {
  assert.deepEqual(summarizeServers([{connected: true}, {connected: true}]), {
    text: "2/2 servers are online",
    statusClass: "status-connected",
  });
  assert.deepEqual(summarizeServers([{connected: true}, {connected: false}]), {
    text: "1/2 servers are online",
    statusClass: "status-warning",
  });
  assert.deepEqual(summarizeServers([{connected: false}, {connected: false}]), {
    text: "0/2 servers are online",
    statusClass: "status-disconnected",
  });
});

test("reads stale state from incidents", () => {
  assert.equal(isIncidentStale({stale: true}), true);
  assert.equal(isIncidentStale({stale: false}), false);
  assert.equal(isIncidentStale({}), false);
});

test("marks stale and snoozed incident states", () => {
  assert.equal(formatIncidentState("error"), "error");
  assert.equal(formatIncidentState("error", {snoozed: true}), "error (SNOOZED)");
  assert.equal(formatIncidentState("error", {stale: true}), "error (STALE)");
  assert.equal(
    formatIncidentState("error", {stale: true, snoozed: true}),
    "error (STALE, SNOOZED)",
  );
});

test("retains an open snooze menu while its incident still exists", () => {
  const incidents = [{key: "disk"}, {key: "systemd"}];

  assert.equal(retainedSnoozeMenuKey("disk", incidents), "disk");
  assert.equal(retainedSnoozeMenuKey("missing", incidents), null);
  assert.equal(retainedSnoozeMenuKey(null, incidents), null);
});

test("selects incident icons", () => {
  assert.equal(iconURL({key: "disk", state: "ok"}), "/icons/status_green.png");
  assert.equal(iconURL({key: "internal.client", state: "error"}), "/icons/status_magenta.png");
  assert.equal(iconURL({key: "disk", state: "warning"}), "/icons/status_yellow.png");
  assert.equal(iconURL({key: "disk", state: "error"}), "/icons/status_red.png");
});
