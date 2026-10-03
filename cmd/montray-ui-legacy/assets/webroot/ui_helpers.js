// snoozeDurationOptions lists the durations offered by the incident controls.
const snoozeDurationOptions = Object.freeze([
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

// parseVisibilityPreference converts a stored string to a boolean, falling back
// to the section's default for missing or unrecognized values.
function parseVisibilityPreference(value, defaultVisible = false) {
  if (value === "true") {
    return true;
  }
  if (value === "false") {
    return false;
  }
  return defaultVisible;
}

// visibilityPreference reads a section preference from browser storage. The
// storage callback keeps the logic testable and lets storage failures fall back
// to the section's default.
function visibilityPreference(key, defaultVisible = false, storage = () => globalThis.localStorage) {
  try {
    return parseVisibilityPreference(storage().getItem(key), defaultVisible);
  } catch (_) {
    return defaultVisible;
  }
}

// heartbeatStatusClass returns "" for missing, invalid, future, or less-than-15s-old
// timestamps; "status-warning" from 15s; and "status-disconnected" from 30s.
function heartbeatStatusClass(value, now = new Date()) {
  if (!value) {
    return "";
  }
  const date = new Date(value);
  const age = now.getTime() - date.getTime();
  if (Number.isNaN(age) || age < 15000) {
    return "";
  }
  if (age < 30000) {
    return "status-warning";
  }
  return "status-disconnected";
}

// formatIncidentCount formats an active incident count with correct plurality.
function formatIncidentCount(count) {
  return `${count} ${count === 1 ? "incident" : "incidents"}`;
}

// formatSnoozedIncidentCount formats a snoozed incident count with correct plurality.
function formatSnoozedIncidentCount(count) {
  return `${count} snoozed ${count === 1 ? "incident" : "incidents"}`;
}

// summarizeServers returns the display text and severity class for connectivity.
function summarizeServers(items) {
  const online = items.filter((item) => item.connected).length;
  let statusClass = "status-warning";
  if (online === items.length) {
    statusClass = "status-connected";
  } else if (online === 0) {
    statusClass = "status-disconnected";
  }

  return {
    text: `${online}/${items.length} servers are online`,
    statusClass,
  };
}

// isIncidentStale reports the freshness recorded on the Watch incident.
function isIncidentStale(item) {
  return item.stale === true;
}

// formatIncidentState appends stale and snoozed markers in a stable order
// without changing the source state.
function formatIncidentState(state, {stale = false, snoozed = false} = {}) {
  const markers = [];
  if (stale) {
    markers.push("STALE");
  }
  if (snoozed) {
    markers.push("SNOOZED");
  }
  return markers.length === 0 ? state : `${state} (${markers.join(", ")})`;
}

// retainedSnoozeMenuKey keeps the open menu key while its alerting incident
// still exists, and clears it once that menu can no longer be rendered.
function retainedSnoozeMenuKey(openKey, items) {
  return items.some((item) => item.key === openKey) ? openKey : null;
}

// iconURL selects the incident icon, giving internal errors their distinct icon.
function iconURL(item) {
  if (item.state === "ok") {
    return "/icons/status_green.png";
  }
  if (item.key.startsWith("internal.")) {
    return "/icons/status_magenta.png";
  }
  if (item.state === "warning") {
    return "/icons/status_yellow.png";
  }
  return "/icons/status_red.png";
}

// Export the pure helpers when the file is loaded by Node's test runner.
if (typeof module !== "undefined") {
  module.exports = {
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
  };
}
