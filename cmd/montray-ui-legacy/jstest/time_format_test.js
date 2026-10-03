const test = require("node:test");
const assert = require("node:assert/strict");

const {
  formatServerTime,
  formatRelativeTime,
  formatLiveTimestamp,
} = require("../assets/webroot/time_format.js");

const now = new Date(2026, 7, 26, 16, 16, 43);
const ago = (milliseconds) => new Date(now.getTime() - milliseconds);
const fromNow = (milliseconds) => new Date(now.getTime() + milliseconds);

test("formats today's timestamp using 24-hour time", () => {
  assert.equal(formatServerTime(now, now), "16:16:43");
});

test("formats relative time buckets", () => {
  const cases = [
    [0, "<15s ago"],
    [14999, "<15s ago"],
    [15000, "15s ago"],
    [29999, "15s ago"],
    [30000, "30s ago"],
    [44999, "30s ago"],
    [45000, "45s ago"],
    [59999, "45s ago"],
    [60000, "1m ago"],
    [73 * 60000, "1h 13m ago"],
    [(23 * 60 + 59) * 60000, "23h 59m ago"],
    [(24 * 60 + 59) * 60000, "1d ago"],
    [25 * 60 * 60000, "1d 1h ago"],
  ];

  for (const [milliseconds, want] of cases) {
    assert.equal(formatRelativeTime(ago(milliseconds), now), want);
  }
});

test("combines absolute and relative time", () => {
  assert.equal(formatLiveTimestamp(ago(13000), now), "16:16:30 (<15s ago)");
  assert.equal(formatLiveTimestamp(fromNow(73 * 60000), now), "17:29:43 (in 1h 13m)");
});

test("formats a missing timestamp as never", () => {
  assert.equal(formatLiveTimestamp(null, now), "never");
});
