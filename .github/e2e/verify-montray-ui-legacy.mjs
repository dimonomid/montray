const websocketURL = process.argv[2] ?? "ws://127.0.0.1:41991/api/v1/wsconnect";
const expectedKey = "local.e2e.exec_result";
const expectedDetails = "expected-e2e-incident";
const timeoutMilliseconds = 30_000;

let finished = false;
let lastMessage = "no WebSocket message received";
let lastError = "none";

function finish(exitCode, message) {
  if (finished) {
    return;
  }
  finished = true;
  clearTimeout(timeout);
  if (exitCode === 0) {
    console.log(message);
  } else {
    console.error(message);
  }
  process.exit(exitCode);
}

function connect() {
  if (finished) {
    return;
  }

  const socket = new WebSocket(websocketURL);
  socket.addEventListener("message", (event) => {
    lastMessage = String(event.data);
    let message;
    try {
      message = JSON.parse(lastMessage);
    } catch (error) {
      lastError = `invalid JSON: ${error}`;
      return;
    }

    const incidents = message?.ongoingIncidents?.alerting ?? [];
    const incident = incidents.find((candidate) => candidate.key === expectedKey);
    if (!incident) {
      return;
    }
    if (incident.state !== "error") {
      finish(1, `incident ${expectedKey} has state ${JSON.stringify(incident.state)}, want "error"`);
      return;
    }
    if (!String(incident.details).includes(expectedDetails)) {
      finish(1, `incident ${expectedKey} details ${JSON.stringify(incident.details)} do not contain ${JSON.stringify(expectedDetails)}`);
      return;
    }

    finish(0, `received expected incident: ${JSON.stringify(incident)}`);
  });
  socket.addEventListener("error", (event) => {
    lastError = event.message || "WebSocket connection error";
    socket.close();
  });
  socket.addEventListener("close", () => {
    if (!finished) {
      setTimeout(connect, 250);
    }
  });
}

const timeout = setTimeout(() => {
  finish(1, `timed out waiting for ${expectedKey}; last error: ${lastError}; last message: ${lastMessage}`);
}, timeoutMilliseconds);

connect();
