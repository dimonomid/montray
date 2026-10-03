function formatServerTime(value, now = new Date()) {
  if (!value) {
    return "never";
  }
  const date = new Date(value);
  if (Number.isNaN(date.getTime())) {
    return value;
  }

  const pad = (number) => String(number).padStart(2, "0");
  const time = `${pad(date.getHours())}:${pad(date.getMinutes())}:${pad(date.getSeconds())}`;
  const isToday = date.getFullYear() === now.getFullYear()
    && date.getMonth() === now.getMonth()
    && date.getDate() === now.getDate();
  if (isToday) {
    return time;
  }

  const dateOptions = {month: "short", day: "numeric"};
  if (date.getFullYear() !== now.getFullYear()) {
    dateOptions.year = "numeric";
  }
  return `${date.toLocaleDateString(undefined, dateOptions)}, ${time}`;
}

function formatRelativeTime(value, now = new Date()) {
  if (!value) {
    return "";
  }
  const date = new Date(value);
  if (Number.isNaN(date.getTime())) {
    return "";
  }

  const elapsedMilliseconds = now.getTime() - date.getTime();
  const future = elapsedMilliseconds < 0;
  const seconds = future
    ? Math.ceil(-elapsedMilliseconds / 1000)
    : Math.floor(elapsedMilliseconds / 1000);
  let duration;
  if (seconds < 15) {
    duration = "<15s";
  } else if (seconds < 60) {
    duration = `${Math.floor(seconds / 15) * 15}s`;
  } else if (seconds < 86400) {
    const totalMinutes = Math.floor(seconds / 60);
    const hours = Math.floor(totalMinutes / 60);
    const minutes = totalMinutes % 60;
    if (hours === 0) {
      duration = `${minutes}m`;
    } else {
      duration = minutes === 0 ? `${hours}h` : `${hours}h ${minutes}m`;
    }
  } else {
    const totalHours = Math.floor(seconds / 3600);
    const days = Math.floor(totalHours / 24);
    const hours = totalHours % 24;
    duration = hours === 0 ? `${days}d` : `${days}d ${hours}h`;
  }
  return future ? `in ${duration}` : `${duration} ago`;
}

function formatLiveTimestamp(value, now = new Date()) {
  const absolute = formatServerTime(value, now);
  const relative = formatRelativeTime(value, now);
  return relative === "" ? absolute : `${absolute} (${relative})`;
}

if (typeof module !== "undefined") {
  module.exports = {formatServerTime, formatRelativeTime, formatLiveTimestamp};
}
