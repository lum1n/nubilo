// The sign-in screen: shows a short code and a QR code that the Nubilo app
// on a paired phone or Mac approves. No password is ever typed here.

import { h, clear, logo, icon, api } from "./lib.js";

export function signInView(root, { onApproved }) {
  let pollTimer = 0;
  let tickTimer = 0;
  let stopped = false;

  const body = h("div", { class: "signin-body", "aria-live": "polite" });
  const card = h("main", { class: "signin-card" },
    h("div", { class: "signin-brand" }, logo("logo logo-lg"), h("h1", { class: "signin-title" }, "Nubilo")),
    h("p", { class: "signin-lede" }, "Your photos and files, from any browser."),
    body);
  clear(root).append(h("div", { class: "signin" }, h("div", { class: "sky", "aria-hidden": "true" }), card));

  function stop() {
    stopped = true;
    clearTimeout(pollTimer);
    clearInterval(tickTimer);
  }

  function loading() {
    clear(body).append(h("div", { class: "signin-loading" }, h("span", { class: "spinner", "aria-hidden": "true" }), "Getting a sign-in code…"));
  }

  function failed(message) {
    clear(body).append(
      h("div", { class: "notice notice-error", role: "alert" }, icon("alert"), h("span", {}, message)),
      h("button", { type: "button", class: "btn btn-primary btn-wide", onclick: start }, icon("refresh"), "Try again"));
  }

  function expired() {
    clearTimeout(pollTimer);
    clearInterval(tickTimer);
    clear(body).append(
      h("div", { class: "signin-expired" },
        h("p", { class: "signin-expired-title" }, "This code has expired"),
        h("p", { class: "muted" }, "Codes last a few minutes. Get a new one to keep going.")),
      h("button", { type: "button", class: "btn btn-primary btn-wide", autofocus: true, onclick: start }, icon("refresh"), "Get a new code"));
    body.querySelector("button")?.focus();
  }

  function show(login) {
    const [a, b] = String(login.code || "").split("-");
    const countdown = h("span", { class: "signin-countdown" });
    const tick = () => {
      const left = Math.max(0, Math.round((login.expires_at - Date.now()) / 1000));
      countdown.textContent = `Expires in ${Math.floor(left / 60)}:${String(left % 60).padStart(2, "0")}`;
      if (left <= 0) expired();
    };
    clear(body).append(
      h("div", { class: "signin-pair" },
        h("figure", { class: "signin-qr" },
          h("img", { src: login.qr_png, width: "256", height: "256", alt: `QR code for sign-in code ${login.code}` })),
        h("div", { class: "signin-code-wrap" },
          h("p", { class: "signin-code-label", id: "code-label" }, "Sign-in code"),
          h("p", { class: "signin-code", "aria-labelledby": "code-label", "aria-label": `${(a || "").split("").join(" ")}, ${(b || "").split("").join(" ")}` },
            h("span", { class: "code-group" }, a || ""), h("span", { class: "code-dash", "aria-hidden": "true" }, "–"), h("span", { class: "code-group" }, b || "")),
          h("p", { class: "signin-waiting" }, h("span", { class: "pulse", "aria-hidden": "true" }), h("span", {}, "Waiting for approval · ", countdown)))),
      h("ol", { class: "signin-steps" },
        h("li", {}, h("span", {}, "Open the Nubilo app on your phone or Mac.")),
        h("li", {}, h("span", {}, "Scan this code, or go to ", h("strong", {}, "People → Sign In on the Web"), " and type the code.")),
        h("li", {}, h("span", {}, "Approve, and this page opens by itself."))),
      h("p", { class: "signin-note" }, icon("lock", "icon icon-sm"),
        "This browser gets the same access as the device that approves it. Sign out when you're done on a shared computer."));
    tick();
    tickTimer = setInterval(tick, 1000);
    poll();
  }

  async function poll() {
    if (stopped) return;
    try {
      const res = await api("login");
      if (stopped) return;
      if (res.status === "approved") { stop(); onApproved(); return; }
      if (res.status === "expired") { expired(); return; }
    } catch {
      // A missed poll is fine; try again.
    }
    pollTimer = setTimeout(poll, 2000);
  }

  async function start() {
    clearTimeout(pollTimer);
    clearInterval(tickTimer);
    stopped = false;
    loading();
    try {
      const login = await api("login", { method: "POST" });
      if (!stopped) show(login);
    } catch (e) {
      failed(e.message);
    }
  }

  start();
  return stop;
}
