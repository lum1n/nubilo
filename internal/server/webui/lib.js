// Shared helpers for the Nubilo web app: DOM building, the API client,
// ULIDs, hashing and formatting. No dependencies, no build step.

// ---- DOM ----

// h builds an element. attrs: class, text, on<Event> handlers (functions,
// attached with addEventListener, never inline), boolean and string
// attributes. Children may be nodes, strings, arrays, null or false.
export function h(tag, attrs = {}, ...children) {
  const svg = tag.startsWith("svg:");
  const el = svg ? document.createElementNS("http://www.w3.org/2000/svg", tag.slice(4)) : document.createElement(tag);
  for (const [k, v] of Object.entries(attrs || {})) {
    if (v == null || v === false) continue;
    if (k === "class") el.setAttribute("class", Array.isArray(v) ? v.filter(Boolean).join(" ") : v);
    else if (k === "text") el.textContent = v;
    else if (k.startsWith("on") && typeof v === "function") el.addEventListener(k.slice(2).toLowerCase(), v);
    else if (k === "dataset") Object.assign(el.dataset, v);
    else if (k === "value" && !svg) el.value = v;
    else el.setAttribute(k, v === true ? "" : String(v));
  }
  append(el, children);
  return el;
}

function append(el, children) {
  for (const c of children) {
    if (c == null || c === false) continue;
    if (Array.isArray(c)) append(el, c);
    else el.append(c instanceof Node ? c : document.createTextNode(String(c)));
  }
}

export function clear(el) {
  while (el.firstChild) el.firstChild.remove();
  return el;
}

// Icons are simple 24px line drawings, built as SVG nodes.
const ICONS = {
  photos: "M4 6.5A2.5 2.5 0 0 1 6.5 4h11A2.5 2.5 0 0 1 20 6.5v11a2.5 2.5 0 0 1-2.5 2.5h-11A2.5 2.5 0 0 1 4 17.5zM4 15l4.2-4.2a1.5 1.5 0 0 1 2.1 0L16 16.5M14 14l1.7-1.7a1.5 1.5 0 0 1 2.1 0L20 14.5M15.5 8.5h.01",
  albums: "M7 4h10M5.5 7h13M6 10h12a2 2 0 0 1 2 2v6a2 2 0 0 1-2 2H6a2 2 0 0 1-2-2v-6a2 2 0 0 1 2-2z",
  files: "M3.5 7.5A2.5 2.5 0 0 1 6 5h3.6l2 2H18a2.5 2.5 0 0 1 2.5 2.5v7A2.5 2.5 0 0 1 18 19H6a2.5 2.5 0 0 1-2.5-2.5z",
  folder: "M3.5 7.5A2.5 2.5 0 0 1 6 5h3.6l2 2H18a2.5 2.5 0 0 1 2.5 2.5v7A2.5 2.5 0 0 1 18 19H6a2.5 2.5 0 0 1-2.5-2.5z",
  file: "M7 3.5h6.5L18 8v10.5A2 2 0 0 1 16 20.5H7a2 2 0 0 1-2-2v-13a2 2 0 0 1 2-2zM13 3.5V8h5",
  upload: "M12 15.5V4.5M7.5 9 12 4.5 16.5 9M5 15v2.5A2 2 0 0 0 7 19.5h10a2 2 0 0 0 2-2V15",
  download: "M12 4.5v11M7.5 11 12 15.5 16.5 11M5 15v2.5A2 2 0 0 0 7 19.5h10a2 2 0 0 0 2-2V15",
  plus: "M12 5v14M5 12h14",
  folderPlus: "M3.5 7.5A2.5 2.5 0 0 1 6 5h3.6l2 2H18a2.5 2.5 0 0 1 2.5 2.5v7A2.5 2.5 0 0 1 18 19H6a2.5 2.5 0 0 1-2.5-2.5zM12 10.5v5M9.5 13h5",
  pencil: "M14.5 5.5l4 4M4.5 19.5l1-4.5L15.3 5.2a1.4 1.4 0 0 1 2 0l1.5 1.5a1.4 1.4 0 0 1 0 2L9 18.5z",
  trash: "M4.5 7h15M10 11v6M14 11v6M6 7l1 11.5A2 2 0 0 0 9 20.5h6a2 2 0 0 0 2-2L18 7M9 7V4.5h6V7",
  close: "M6 6l12 12M18 6 6 18",
  left: "M14.5 5.5 8 12l6.5 6.5",
  right: "M9.5 5.5 16 12l-6.5 6.5",
  chevron: "M9.5 6.5 15 12l-5.5 5.5",
  signout: "M14 4.5h3.5a2 2 0 0 1 2 2v11a2 2 0 0 1-2 2H14M10 8l-4 4 4 4M6 12h10",
  play: "M8 5.5v13l10.5-6.5z",
  live: "M12 12m-2.5 0a2.5 2.5 0 1 0 5 0a2.5 2.5 0 1 0-5 0M12 12m-5.5 0a5.5 5.5 0 1 0 11 0a5.5 5.5 0 1 0-11 0",
  info: "M12 11v5.5M12 7.5h.01M12 3.5a8.5 8.5 0 1 0 0 17 8.5 8.5 0 0 0 0-17z",
  lock: "M7 11V8a5 5 0 0 1 10 0v3M6.5 11h11a1.5 1.5 0 0 1 1.5 1.5v6a1.5 1.5 0 0 1-1.5 1.5h-11A1.5 1.5 0 0 1 5 18.5v-6A1.5 1.5 0 0 1 6.5 11z",
  people: "M9 11a3.5 3.5 0 1 0 0-7 3.5 3.5 0 0 0 0 7zM2.5 19.5a6.5 6.5 0 0 1 13 0M16 4.3a3.5 3.5 0 0 1 0 6.4M18.5 14a6.5 6.5 0 0 1 3 5.5",
  refresh: "M19.5 12a7.5 7.5 0 1 1-2.2-5.3M19.5 4.5v4h-4",
  more: "M6 12h.01M12 12h.01M18 12h.01",
  check: "M5 12.5l4.5 4.5L19 7.5",
  alert: "M12 9v4.5M12 17h.01M10.3 4.6 2.9 17.5A2 2 0 0 0 4.6 20.5h14.8a2 2 0 0 0 1.7-3L13.7 4.6a2 2 0 0 0-3.4 0z",
};

export function icon(name, cls = "icon") {
  const svg = h("svg:svg", { class: cls, viewBox: "0 0 24 24", "aria-hidden": "true", focusable: "false" });
  svg.append(h("svg:path", { d: ICONS[name] || ICONS.file, fill: "none", stroke: "currentColor", "stroke-width": "1.7", "stroke-linecap": "round", "stroke-linejoin": "round" }));
  return svg;
}

// The app icon: a white cloud on the blue gradient.
export function logo(cls = "logo") {
  const svg = h("svg:svg", { class: cls, viewBox: "0 0 64 64", "aria-hidden": "true", focusable: "false" });
  const id = "lg" + Math.random().toString(36).slice(2, 8);
  const grad = h("svg:linearGradient", { id, x1: "0", y1: "0", x2: "0", y2: "1" },
    h("svg:stop", { offset: "0", "stop-color": "#5fb2ff" }),
    h("svg:stop", { offset: "1", "stop-color": "#1f63e0" }));
  svg.append(
    h("svg:defs", {}, grad),
    h("svg:rect", { width: "64", height: "64", rx: "15", fill: `url(#${id})` }),
    h("svg:path", { fill: "#fff", d: "M20.5 44.5h24a9 9 0 0 0 1.4-17.9A12.5 12.5 0 0 0 22 24.3 10.1 10.1 0 0 0 20.5 44.5z" }),
  );
  return svg;
}

// ---- API ----

export class APIError extends Error {
  constructor(status, message) {
    super(message || `Request failed (${status})`);
    this.status = status;
  }
}

let onSignedOut = () => {};
export function setSignedOutHandler(fn) { onSignedOut = fn; }

// api calls a /web/api route. Non-GET requests carry the X-Nubilo-Web
// header the server requires. A 401 means the session ended.
export async function api(path, { method = "GET", json, body, headers = {}, raw = false, signal } = {}) {
  const init = { method, headers: { ...headers }, credentials: "same-origin", signal };
  if (method !== "GET" && method !== "HEAD") init.headers["X-Nubilo-Web"] = "1";
  if (json !== undefined) {
    init.headers["Content-Type"] = "application/json";
    init.body = JSON.stringify(json);
  } else if (body !== undefined) {
    init.body = body;
  }
  let res;
  try {
    res = await fetch("/web/api/" + path, init);
  } catch (e) {
    if (e.name === "AbortError") throw e;
    throw new APIError(0, "Can't reach your Nubilo server. Check your connection.");
  }
  if (res.status === 401 && path !== "login") {
    onSignedOut();
    throw new APIError(401, "You were signed out.");
  }
  if (!res.ok) {
    const text = (await res.text().catch(() => "")).trim();
    throw new APIError(res.status, friendlyError(res.status, text));
  }
  if (raw) return res;
  if (res.status === 204) return null;
  const type = res.headers.get("Content-Type") || "";
  return type.includes("json") ? res.json() : res.text();
}

export function friendlyError(status, text) {
  if (status === 507 || /quota exceeded/i.test(text)) return "Your storage is full. Free up space or ask the admin for a bigger quota.";
  if (status === 413) return "This file is larger than the server accepts.";
  if (status === 403) return "You don't have permission to do that.";
  if (status === 404) return "That item no longer exists.";
  if (status === 409) return text ? capitalize(text) + "." : "That conflicts with a change made elsewhere.";
  if (status === 429) return text ? capitalize(text) + "." : "Too many attempts. Try again later.";
  if (status >= 500) return "The server ran into a problem. Try again in a moment.";
  return text ? capitalize(text) : `Request failed (${status}).`;
}

function capitalize(s) { return s.charAt(0).toUpperCase() + s.slice(1); }

// send uploads a body with XMLHttpRequest so progress can be shown.
export function send(method, path, body, { headers = {}, onProgress } = {}) {
  return new Promise((resolve, reject) => {
    const xhr = new XMLHttpRequest();
    xhr.open(method, "/web/api/" + path);
    xhr.withCredentials = true;
    xhr.setRequestHeader("X-Nubilo-Web", "1");
    for (const [k, v] of Object.entries(headers)) xhr.setRequestHeader(k, v);
    if (onProgress) xhr.upload.addEventListener("progress", (e) => { if (e.lengthComputable) onProgress(e.loaded / e.total); });
    xhr.addEventListener("load", () => {
      if (xhr.status === 401) { onSignedOut(); reject(new APIError(401, "You were signed out.")); return; }
      if (xhr.status < 200 || xhr.status >= 300) { reject(new APIError(xhr.status, friendlyError(xhr.status, (xhr.responseText || "").trim()))); return; }
      try { resolve(xhr.responseText ? JSON.parse(xhr.responseText) : null); } catch { resolve(null); }
    });
    xhr.addEventListener("error", () => reject(new APIError(0, "Upload failed. Check your connection.")));
    xhr.addEventListener("abort", () => reject(new APIError(0, "Upload cancelled.")));
    xhr.send(body);
  });
}

// ---- IDs ----

const B32 = "0123456789ABCDEFGHJKMNPQRSTVWXYZ";
let lastTime = -1;
let lastRand = null;

// ulid returns a monotonic ULID: 10 chars of millisecond time and 16 chars
// of randomness, Crockford base32. Within one millisecond the random part
// is incremented so IDs stay ordered.
export function ulid() {
  let now = Date.now();
  if (now <= lastTime && lastRand) {
    now = lastTime;
    let i = 15;
    while (i >= 0 && lastRand[i] === 31) { lastRand[i] = 0; i--; }
    if (i < 0) throw new Error("ulid: random overflow");
    lastRand[i]++;
  } else {
    const bytes = crypto.getRandomValues(new Uint8Array(16));
    lastRand = Array.from(bytes, (b) => b & 31);
    lastTime = now;
  }
  let t = "";
  let ms = now;
  for (let i = 0; i < 10; i++) {
    t = B32[ms % 32] + t;
    ms = Math.floor(ms / 32);
  }
  return t + lastRand.map((d) => B32[d]).join("");
}

// ulidTime returns the millisecond timestamp of a ULID, or 0.
export function ulidTime(id) {
  if (typeof id !== "string" || id.length !== 26) return 0;
  let ms = 0;
  for (let i = 0; i < 10; i++) {
    const v = B32.indexOf(id[i].toUpperCase());
    if (v < 0) return 0;
    ms = ms * 32 + v;
  }
  return ms;
}

export async function sha256Hex(blob) {
  const buf = await blob.arrayBuffer();
  const digest = await crypto.subtle.digest("SHA-256", buf);
  return Array.from(new Uint8Array(digest), (b) => b.toString(16).padStart(2, "0")).join("");
}

// storageChanged tells the shell to refresh the storage figure.
export function storageChanged() {
  window.dispatchEvent(new Event("nubilo:storage"));
}

// ---- formatting ----

export function formatBytes(n) {
  if (!Number.isFinite(n) || n <= 0) return "0 bytes";
  if (n < 1000) return `${n} bytes`;
  const units = ["KB", "MB", "GB", "TB", "PB"];
  let v = n;
  let u = -1;
  do { v /= 1000; u++; } while (v >= 1000 && u < units.length - 1);
  return `${v >= 100 ? Math.round(v) : v.toFixed(1).replace(/\.0$/, "")} ${units[u]}`;
}

const monthFmt = new Intl.DateTimeFormat(undefined, { month: "long", year: "numeric" });
const dayFmt = new Intl.DateTimeFormat(undefined, { dateStyle: "medium" });
const dateTimeFmt = new Intl.DateTimeFormat(undefined, { dateStyle: "long", timeStyle: "short" });

export function formatMonth(ms) { return monthFmt.format(new Date(ms)); }
export function formatDate(ms) { return ms ? dayFmt.format(new Date(ms)) : ""; }
export function formatDateTime(ms) { return ms ? dateTimeFmt.format(new Date(ms)) : ""; }

export function formatRelative(ms) {
  if (!ms) return "";
  const diff = Date.now() - ms;
  if (diff < 60_000) return "Just now";
  if (diff < 3_600_000) return `${Math.floor(diff / 60_000)} min ago`;
  if (diff < 86_400_000 && new Date(ms).getDate() === new Date().getDate()) return "Today";
  return formatDate(ms);
}

export function formatDuration(ms) {
  const s = Math.round(ms / 1000);
  return `${Math.floor(s / 60)}:${String(s % 60).padStart(2, "0")}`;
}

// validName mirrors the server's rule for file and folder names.
export function validName(name) {
  const n = name.trim();
  if (!n || n === "." || n === "..") return "Enter a name.";
  if (new TextEncoder().encode(n).length > 255) return "That name is too long.";
  if (/[\/\\]/.test(n)) return "Names can't contain / or \\.";
  // eslint-disable-next-line no-control-regex
  if (/[\x00-\x1f]/.test(n)) return "Names can't contain control characters.";
  return "";
}
