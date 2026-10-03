const connectionStatus = document.getElementById("connection-status");
const themeToggle = document.getElementById("theme-toggle");
const statusSummary = document.getElementById("status-summary");
const serverSummary = document.getElementById("server-summary");
const servers = document.getElementById("servers");
const incidents = document.getElementById("incidents");
const incidentSummary = document.getElementById("incident-summary");
const snoozedSummary = document.getElementById("snoozed-summary");
const snoozedIncidents = document.getElementById("snoozed-incidents");
const serverDetailsVisibilityKey = "montray.serverDetailsVisible";
const incidentDetailsVisibilityKey = "montray.incidentDetailsVisible";
const snoozedVisibilityKey = "montray.snoozedVisible";
const themeKey = "montray.theme";

function themePreference() {
  try {
    return localStorage.getItem(themeKey) === "light" ? "light" : "dark";
  } catch (_) {
    return "dark";
  }
}

function applyTheme(theme) {
  document.documentElement.dataset.theme = theme === "light" ? "light" : "dark";
  const nextTheme = theme === "dark" ? "light" : "dark";
  themeToggle.textContent = nextTheme === "light" ? "☀" : "☾";
  themeToggle.setAttribute("aria-label", `Switch to ${nextTheme} scheme`);
  themeToggle.title = `Switch to ${nextTheme} scheme`;
}

let theme = themePreference();
applyTheme(theme);

themeToggle.addEventListener("click", () => {
  theme = theme === "dark" ? "light" : "dark";
  applyTheme(theme);
  try {
    localStorage.setItem(themeKey, theme);
  } catch (_) {
    // Keep the current-page setting working when browser storage is unavailable.
  }
});

function saveVisibilityPreference(key, visible) {
  try {
    localStorage.setItem(key, String(visible));
  } catch (_) {
    // Keep the current-page setting working when browser storage is unavailable.
  }
}

let serverDetailsVisible = visibilityPreference(serverDetailsVisibilityKey, true);
let incidentDetailsVisible = visibilityPreference(incidentDetailsVisibilityKey, true);

function setSectionSummary(summary, text, visible) {
  summary.replaceChildren(document.createTextNode(`▪ ${text} ${visible ? "▼" : "▲"}`));
}

function updateLiveTimestamp(element, now = new Date()) {
  const value = element.dataset.timestamp;
  element.textContent = formatLiveTimestamp(value, now);

  if (element.classList.contains("server-heartbeat")) {
    const statusClass = heartbeatStatusClass(value, now);
    element.classList.toggle("status-warning", statusClass === "status-warning");
    element.classList.toggle("status-disconnected", statusClass === "status-disconnected");
  }
}

function createLiveTimestamp(value, className = "") {
  const element = document.createElement("time");
  element.className = "live-timestamp";
  if (className) {
    element.classList.add(className);
  }
  element.dataset.timestamp = value || "";
  if (value && !Number.isNaN(new Date(value).getTime())) {
    element.dateTime = value;
  }
  updateLiveTimestamp(element);
  return element;
}

function updateLiveTimestamps() {
  const now = new Date();
  for (const element of document.querySelectorAll(".live-timestamp")) {
    updateLiveTimestamp(element, now);
  }
}

window.setInterval(updateLiveTimestamps, 1000);

function renderServers(items) {
  items = items || [];
  lastServerItems = items;
  const summary = summarizeServers(items);
  setSectionSummary(serverSummary, summary.text, serverDetailsVisible);
  serverSummary.className = `section-summary ${summary.statusClass}`;
  serverSummary.hidden = items.length === 0;
  servers.hidden = !serverDetailsVisible || items.length === 0;
  servers.replaceChildren();

  const table = document.createElement("table");
  table.className = "server-status-table";
  const header = table.insertRow();
  for (const column of ["server", "connection", "last heartbeat"]) {
    const cell = document.createElement("th");
    cell.textContent = column;
    header.appendChild(cell);
  }
  for (const item of items) {
    const row = table.insertRow();
    for (const [columnIndex, value] of [
      item.id,
      item.connected ? "online" : "offline",
      item.lastHeartbeatTime,
    ].entries()) {
      const cell = row.insertCell();
      if (columnIndex === 2) {
        cell.appendChild(createLiveTimestamp(value, "server-heartbeat"));
      } else {
        cell.textContent = value;
      }
      if (columnIndex === 1) {
        cell.className = item.connected ? "status-connected" : "status-disconnected";
      }
    }
  }
  servers.appendChild(table);
  servers.appendChild(document.createElement("hr"));
}

serverSummary.addEventListener("click", () => {
  serverDetailsVisible = !serverDetailsVisible;
  saveVisibilityPreference(serverDetailsVisibilityKey, serverDetailsVisible);
  renderServers(lastServerItems);
});

let snoozedVisible = visibilityPreference(snoozedVisibilityKey);
let lastServerItems = [];
let lastAlertingItems = [];
let lastSnoozedItems = [];
// openSnoozeMenuKey is the incident key whose snooze menu is expanded, such as
// "my-server.disk-space", or null when no snooze menu is open. Keeping it here
// preserves the expanded menu across incident-list rerenders.
let openSnoozeMenuKey = null;

function renderIncidentList(items, container, isSnoozed) {
  for (const item of items) {
    const stale = isIncidentStale(item);
    const table = document.createElement("table");
    table.className = isSnoozed ? "incident snoozed-incident" : "incident";
    if (stale) {
      table.classList.add("stale-incident");
    }

    const fields = [
      ["key", item.key],
      ["state", formatIncidentState(item.state, {stale, snoozed: isSnoozed})],
      ["details", item.details],
      ["started at", createLiveTimestamp(item.incidentStartedAt)],
    ];
    if (isSnoozed) {
      fields.push(["snoozed until", createLiveTimestamp(item.snoozedUntil)]);
    }

    for (const [fieldIndex, [label, value]] of fields.entries()) {
      const row = table.insertRow();

      if (fieldIndex === 0) {
        const iconCell = row.insertCell();
        iconCell.className = "incident-icon";
        iconCell.rowSpan = fields.length;

        const icon = document.createElement("img");
        icon.src = iconURL(item);
        icon.alt = item.state;
        iconCell.appendChild(icon);
      }

      const labelCell = document.createElement("th");
      labelCell.textContent = `${label}:`;
      labelCell.className = "incident-label";
      row.appendChild(labelCell);

      const valueCell = row.insertCell();
      if (value instanceof Node) {
        valueCell.appendChild(value);
      } else {
        valueCell.textContent = value;
      }
    }

    container.appendChild(table);

    const controls = document.createElement("div");
    controls.className = "incident-controls";
    if (isSnoozed) {
      const button = document.createElement("button");
      button.type = "button";
      button.textContent = "Unsnooze";
      button.addEventListener("click", () => unsnoozeIncident(item.key));
      controls.appendChild(button);
    } else {
      const menu = document.createElement("div");
      menu.className = "snooze-menu";

      const menuButton = document.createElement("button");
      menuButton.type = "button";
      menuButton.textContent = "Snooze";
      menuButton.setAttribute("aria-haspopup", "menu");
      menuButton.setAttribute("aria-expanded", String(openSnoozeMenuKey === item.key));
      menu.appendChild(menuButton);

      const options = document.createElement("div");
      options.className = "snooze-menu-options";
      options.hidden = openSnoozeMenuKey !== item.key;
      options.setAttribute("role", "menu");
      menuButton.addEventListener("click", () => {
        options.hidden = !options.hidden;
        menuButton.setAttribute("aria-expanded", String(!options.hidden));
        openSnoozeMenuKey = options.hidden ? null : item.key;
      });
      for (const duration of snoozeDurationOptions) {
        const button = document.createElement("button");
        button.type = "button";
        button.textContent = duration;
        button.setAttribute("role", "menuitem");
        button.addEventListener("click", () => {
          openSnoozeMenuKey = null;
          options.hidden = true;
          menuButton.setAttribute("aria-expanded", "false");
          snoozeIncident(item.key, duration);
        });
        options.appendChild(button);
      }
      menu.appendChild(options);
      menu.addEventListener("focusout", (event) => {
        if (menu.isConnected && !menu.contains(event.relatedTarget)) {
          if (openSnoozeMenuKey === item.key) {
            openSnoozeMenuKey = null;
          }
          options.hidden = true;
          menuButton.setAttribute("aria-expanded", "false");
        }
      });
      controls.appendChild(menu);
    }
    if (stale) {
      const button = document.createElement("button");
      button.type = "button";
      button.textContent = "Forget stale";
      button.addEventListener("click", () => forgetIncident(item.key));
      controls.appendChild(button);
    }
    container.appendChild(controls);
    container.appendChild(document.createElement("hr"));
  }
}

function render(alertingItems, snoozedItems) {
  alertingItems = alertingItems || [];
  snoozedItems = snoozedItems || [];
  lastAlertingItems = alertingItems;
  lastSnoozedItems = snoozedItems;
  const snoozeMenuKeyToRestore = retainedSnoozeMenuKey(openSnoozeMenuKey, alertingItems);

  setSectionSummary(incidentSummary, formatIncidentCount(alertingItems.length), incidentDetailsVisible);
  if (alertingItems.length > 0) {
    incidentSummary.className = "section-summary status-disconnected";
  } else {
    incidentSummary.className = "section-summary status-connected";
  }

  setSectionSummary(snoozedSummary, formatSnoozedIncidentCount(snoozedItems.length), snoozedVisible);
  snoozedSummary.className = snoozedItems.length > 0
    ? "section-summary status-warning"
    : "section-summary status-connected";

  incidents.replaceChildren();
  openSnoozeMenuKey = snoozeMenuKeyToRestore;

  if (alertingItems.length === 0) {
    const emptyMessage = document.createElement("div");
    emptyMessage.className = "incident-empty";
    emptyMessage.textContent = "No ongoing incidents.";
    incidents.appendChild(emptyMessage);
  } else {
    renderIncidentList(alertingItems, incidents, false);
  }

  snoozedIncidents.replaceChildren();
  snoozedIncidents.className = "";
  if (snoozedItems.length === 0) {
    const emptyMessage = document.createElement("div");
    emptyMessage.className = "incident-empty";
    emptyMessage.textContent = "No snoozed incidents.";
    snoozedIncidents.appendChild(emptyMessage);
  } else {
    snoozedIncidents.className = "snoozed-list";
    renderIncidentList(snoozedItems, snoozedIncidents, true);
  }

  incidents.hidden = !incidentDetailsVisible;
  snoozedIncidents.hidden = !snoozedVisible;
}

incidentSummary.addEventListener("click", () => {
  incidentDetailsVisible = !incidentDetailsVisible;
  saveVisibilityPreference(incidentDetailsVisibilityKey, incidentDetailsVisible);
  render(lastAlertingItems, lastSnoozedItems);
});

snoozedSummary.addEventListener("click", () => {
  snoozedVisible = !snoozedVisible;
  saveVisibilityPreference(snoozedVisibilityKey, snoozedVisible);
  render(lastAlertingItems, lastSnoozedItems);
});

async function snoozeIncident(key, duration) {
  const response = await fetch("/api/v1/snooze", {
    method: "POST",
    headers: {"Content-Type": "application/json"},
    body: JSON.stringify({key, duration}),
  });

  if (!response.ok) {
    connectionStatus.textContent = "Failed to snooze incident";
  }
}

async function unsnoozeIncident(key) {
  const response = await fetch("/api/v1/unsnooze", {
    method: "POST",
    headers: {"Content-Type": "application/json"},
    body: JSON.stringify({key}),
  });

  if (!response.ok) {
    connectionStatus.textContent = "Failed to unsnooze incident";
  }
}

async function forgetIncident(key) {
  const response = await fetch("/api/v1/forget", {
    method: "POST",
    headers: {"Content-Type": "application/json"},
    body: JSON.stringify({key}),
  });

  if (!response.ok) {
    connectionStatus.textContent = "Failed to forget stale incident";
  }
}

function connect() {
  const protocol = window.location.protocol === "https:" ? "wss:" : "ws:";
  const socket = new WebSocket(`${protocol}//${window.location.host}/api/v1/wsconnect`);

  socket.onopen = () => {
    connectionStatus.textContent = "▪ UI online";
    connectionStatus.className = "status-connected";
  };

  socket.onmessage = (event) => {
    const message = JSON.parse(event.data);
    renderServers(message.servers);
    render(message.ongoingIncidents.alerting, message.ongoingIncidents.snoozed);
    statusSummary.hidden = false;
  };

  socket.onclose = () => {
    connectionStatus.textContent = "× UI offline; reconnecting...";
    connectionStatus.className = "status-disconnected";
    serverSummary.replaceChildren();
    statusSummary.hidden = true;
    servers.hidden = true;
    window.setTimeout(connect, 1000);
  };

  socket.onerror = () => socket.close();
}

connect();
