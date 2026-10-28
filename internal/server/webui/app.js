// Nubilo web app entry: session check, the app shell, routing.

import { h, clear, icon, logo, api, setSignedOutHandler, formatBytes } from "./lib.js";
import { signInView } from "./signin.js";
import { photosView, albumsView, albumView } from "./photos.js";
import { filesView } from "./files.js";
import { notice } from "./ui.js";

const root = document.getElementById("app");
let teardown = () => {};
let shell = null;
let me = null;
let signedOut = false;

// ---- sign-in ----

function showSignIn() {
  if (signedOut) return;
  signedOut = true;
  teardown();
  shell = null;
  document.querySelectorAll("dialog").forEach((d) => d.close());
  document.title = "Sign in · Nubilo";
  teardown = signInView(root, { onApproved: () => { signedOut = false; boot(); } });
}
setSignedOutHandler(showSignIn);

async function signOut(button) {
  button.disabled = true;
  try {
    await api("logout", { method: "POST" });
  } catch {
    // Signed out either way: the cookie is cleared by the server or gone.
  }
  history.replaceState(null, "", "/web/");
  showSignIn();
}

// ---- shell ----

const NAV = [
  { id: "photos", label: "Photos", path: "/web/photos", icon: "photos" },
  { id: "albums", label: "Albums", path: "/web/albums", icon: "albums" },
  { id: "files", label: "Files", path: "/web/files", icon: "files" },
];

function initials(u) {
  const n = (u.display_name || u.username || "?").trim();
  const parts = n.split(/\s+/).filter(Boolean);
  return ((parts[0]?.[0] || "") + (parts.length > 1 ? parts[parts.length - 1][0] : "")).toUpperCase() || "?";
}

function accountCard() {
  const { user, device, storage } = me;
  const used = storage?.bytes || 0;
  const quota = storage?.quota_bytes || user.quota_bytes || 0;
  const pct = quota > 0 ? Math.min(1, used / quota) : 0;
  const out = h("button", { type: "button", class: "btn btn-quiet btn-wide signout" }, icon("signout"), "Sign out");
  out.addEventListener("click", () => signOut(out));
  return h("section", { class: "account", "aria-label": "Account" },
    h("div", { class: "account-who" },
      h("span", { class: "avatar", "aria-hidden": "true" }, initials(user)),
      h("div", { class: "account-names" },
        h("p", { class: "account-name" }, user.display_name || user.username),
        h("p", { class: "account-via" }, device?.name ? `via ${device.name}` : `@${user.username}`))),
    h("div", { class: "storage" },
      h("p", { class: "storage-text" },
        h("span", {}, quota > 0 ? `${formatBytes(used)} of ${formatBytes(quota)}` : `${formatBytes(used)} used`),
        quota > 0 ? h("span", { class: "muted" }, `${Math.round(pct * 100)}%`) : null),
      quota > 0 ? h("meter", { class: ["storage-meter", pct > 0.9 && "is-full"], min: "0", max: String(quota), low: String(quota * 0.75), high: String(quota * 0.9), optimum: "0", value: String(used), "aria-label": "Storage used" }) : null),
    out);
}

function openAccountSheet() {
  const dlg = h("dialog", { class: "dialog sheet", "aria-label": "Account" },
    h("div", { class: "dialog-body" },
      h("div", { class: "sheet-head" }, h("h2", { class: "dialog-title" }, "Account"),
        h("button", { type: "button", class: "icon-btn", "aria-label": "Close", onclick: () => dlg.close() }, icon("close"))),
      accountCard()));
  dlg.addEventListener("close", () => dlg.remove());
  dlg.addEventListener("click", (e) => { if (e.target === dlg) dlg.close(); });
  document.body.append(dlg);
  dlg.showModal();
}

function buildShell() {
  const crumbs = h("nav", { class: "crumbs", "aria-label": "Breadcrumb" });
  const title = h("h1", { class: "page-title" });
  const subtitle = h("p", { class: "page-sub" });
  const actions = h("div", { class: "page-actions" });
  const noticeSlot = h("div", { class: "notice-slot" });
  const content = h("div", { class: "content", id: "content", tabindex: "-1" });
  const navLinks = (cls) => NAV.map((n) => h("a", { class: cls, href: n.path, dataset: { nav: n.id }, onclick: (e) => { if (e.metaKey || e.ctrlKey || e.shiftKey) return; e.preventDefault(); go(n.path); } }, icon(n.icon), h("span", {}, n.label)));
  const accountBtn = h("button", { type: "button", class: "avatar-btn", "aria-label": "Account and sign out", onclick: openAccountSheet }, h("span", { class: "avatar avatar-sm" }, initials(me.user)));
  const main = h("main", { class: "main", "aria-labelledby": "page-title" },
    h("header", { class: "topbar" },
      h("div", { class: "topbar-brand" }, logo("logo logo-sm"), h("span", {}, "Nubilo"), accountBtn),
      crumbs,
      h("div", { class: "title-row" }, h("div", { class: "title-block" }, title, subtitle), actions)),
    noticeSlot, content);
  title.id = "page-title";
  const app = h("div", { class: "app" },
    h("a", { class: "skip", href: "#content" }, "Skip to content"),
    h("aside", { class: "sidebar" },
      h("div", { class: "brand" }, logo("logo"), h("span", { class: "brand-name" }, "Nubilo")),
      h("nav", { class: "side-nav", "aria-label": "Sections" }, navLinks("side-link")),
      h("div", { class: "sidebar-foot" }, accountCard())),
    main,
    h("nav", { class: "tabbar", "aria-label": "Sections" }, navLinks("tab-link")));
  clear(root).append(app);
  let currentTitle = "";
  return {
    main, content, notice: noticeSlot,
    title: () => currentTitle,
    setHeader({ title: t, subtitle: s = "", crumbs: c = [] }) {
      currentTitle = t;
      title.textContent = t;
      subtitle.textContent = s;
      clear(crumbs).append(...c);
      crumbs.hidden = !c.length;
      document.title = `${t} · Nubilo`;
    },
    setActions(nodes) { clear(actions).append(...nodes); },
    setActive(id) {
      for (const a of app.querySelectorAll("[data-nav]")) {
        if (a.dataset.nav === id) a.setAttribute("aria-current", "page");
        else a.removeAttribute("aria-current");
      }
    },
    reset() {
      clear(content);
      clear(noticeSlot);
      // Drop zones attach to main; replace it to drop old listeners.
      const cur = this.main;
      const fresh = cur.cloneNode(false);
      while (cur.firstChild) fresh.append(cur.firstChild);
      cur.replaceWith(fresh);
      this.main = fresh;
    },
  };
}

// ---- routing ----

function go(path, hint) {
  if (location.pathname !== path) history.pushState(null, "", path);
  route(hint);
}

function route(hint) {
  teardown();
  shell.reset();
  const parts = location.pathname.replace(/^\/web\/?/, "").split("/").filter(Boolean).map(decodeURIComponent);
  const [section = "photos", id] = parts;
  shell.setActive(section);
  window.scrollTo(0, 0);
  switch (section) {
    case "albums":
      teardown = id ? albumView(shell, go, id, hint) : albumsView(shell, go);
      break;
    case "files":
      teardown = filesView(shell, go, id);
      break;
    case "photos":
      teardown = photosView(shell);
      break;
    default:
      history.replaceState(null, "", "/web/photos");
      shell.setActive("photos");
      teardown = photosView(shell);
  }
  if (typeof teardown !== "function") teardown = () => {};
}

window.addEventListener("popstate", () => { if (shell) route(); });

// Views fire "nubilo:storage" after uploads and deletes; refresh the
// storage figure without blocking anything.
let storageTimer = 0;
window.addEventListener("nubilo:storage", () => {
  clearTimeout(storageTimer);
  storageTimer = setTimeout(async () => {
    try {
      me = await api("me");
    } catch {
      return;
    }
    for (const el of document.querySelectorAll(".account")) el.replaceWith(accountCard());
  }, 300);
});

// ---- boot ----

async function boot() {
  clear(root).append(h("div", { class: "boot" }, logo("logo logo-lg boot-logo")));
  try {
    me = await api("me");
  } catch (e) {
    if (e.status === 401) return; // the sign-in screen is up
    clear(root).append(h("div", { class: "boot" }, logo("logo logo-lg"), h("div", { class: "boot-error" })));
    notice(root.querySelector(".boot-error"), e.message);
    root.querySelector(".boot-error").append(h("button", { type: "button", class: "btn btn-primary", onclick: boot }, "Try again"));
    return;
  }
  signedOut = false;
  shell = buildShell();
  route();
}

boot();
