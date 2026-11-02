// Shared UI pieces: modal dialogs, inline notices, the upload tray and the
// drop zone.

import { h, clear, icon } from "./lib.js";

// ---- dialogs ----

// openDialog shows a modal <dialog> and resolves with the value passed to
// close(), or null when dismissed.
function openDialog(build) {
  return new Promise((resolve) => {
    const dlg = h("dialog", { class: "dialog" });
    let result = null;
    const close = (value) => { result = value; dlg.close(); };
    dlg.addEventListener("close", () => { dlg.remove(); resolve(result); });
    dlg.addEventListener("click", (e) => { if (e.target === dlg) dlg.close(); });
    dlg.append(build(close));
    document.body.append(dlg);
    dlg.showModal();
    const focus = dlg.querySelector("[autofocus]") || dlg.querySelector("input, button");
    focus?.focus();
    if (focus?.select) focus.select();
  });
}

// promptName asks for a name. validate(name) returns an error or "", and
// submit(name) runs the change; its error is shown in the dialog.
export function promptName({ title, label, value = "", action, validate, submit }) {
  return openDialog((close) => {
    const input = h("input", { type: "text", class: "field-input", value, autofocus: true, autocomplete: "off", spellcheck: "false", "aria-describedby": "dlg-err" });
    const err = h("p", { class: "field-error", id: "dlg-err", role: "alert" });
    const ok = h("button", { type: "submit", class: "btn btn-primary" }, action);
    const form = h("form", { class: "dialog-body", method: "dialog" },
      h("h2", { class: "dialog-title" }, title),
      h("label", { class: "field" }, h("span", { class: "field-label" }, label), input),
      err,
      h("div", { class: "dialog-actions" },
        h("button", { type: "button", class: "btn btn-quiet", onclick: () => close(null) }, "Cancel"),
        ok));
    // Selecting the name before an extension, like Finder does.
    if (value) {
      const dot = value.lastIndexOf(".");
      queueMicrotask(() => input.setSelectionRange(0, dot > 0 ? dot : value.length));
    }
    form.addEventListener("submit", async (e) => {
      e.preventDefault();
      const name = input.value.trim();
      const problem = validate ? validate(name) : "";
      if (problem) { err.textContent = problem; input.focus(); return; }
      ok.disabled = true;
      err.textContent = "";
      try {
        await submit(name);
        close(name);
      } catch (ex) {
        err.textContent = ex.message;
        ok.disabled = false;
        input.focus();
      }
    });
    return form;
  });
}

// confirmAction asks before something destructive; run() does it and its
// error stays in the dialog.
export function confirmAction({ title, message, action, run }) {
  return openDialog((close) => {
    const err = h("p", { class: "field-error", role: "alert" });
    const ok = h("button", { type: "button", class: "btn btn-danger" }, action);
    ok.addEventListener("click", async () => {
      ok.disabled = true;
      err.textContent = "";
      try {
        await run();
        close(true);
      } catch (ex) {
        err.textContent = ex.message;
        ok.disabled = false;
      }
    });
    return h("div", { class: "dialog-body" },
      h("h2", { class: "dialog-title" }, title),
      h("p", { class: "dialog-text" }, message),
      err,
      h("div", { class: "dialog-actions" },
        h("button", { type: "button", class: "btn btn-quiet", autofocus: true, onclick: () => close(false) }, "Cancel"),
        ok));
  });
}

// ---- notices ----

// notice renders an inline message (kind: "error" | "info") into a slot.
export function notice(slot, message, kind = "error") {
  clear(slot);
  if (!message) return;
  slot.append(h("div", { class: ["notice", `notice-${kind}`], role: kind === "error" ? "alert" : "status" },
    icon(kind === "error" ? "alert" : "info"),
    h("span", {}, message),
    h("button", { type: "button", class: "icon-btn icon-btn-sm", "aria-label": "Dismiss", onclick: () => clear(slot) }, icon("close"))));
}

// ---- uploads ----

const tray = { el: null, list: null, title: null, active: 0 };

function ensureTray() {
  if (tray.el) return;
  tray.title = h("h2", { class: "tray-title" }, "Uploads");
  tray.list = h("ul", { class: "tray-list" });
  tray.el = h("section", { class: "tray", "aria-label": "Uploads", "aria-live": "polite" },
    h("header", { class: "tray-head" }, tray.title,
      h("button", { type: "button", class: "icon-btn icon-btn-sm", "aria-label": "Close uploads", onclick: () => { tray.el.remove(); tray.el = null; } }, icon("close"))),
    tray.list);
  document.body.append(tray.el);
}

function updateTrayTitle() {
  if (!tray.title) return;
  tray.title.textContent = tray.active > 0 ? `Uploading ${tray.active} ${tray.active === 1 ? "item" : "items"}…` : "Uploads done";
}

// trackUpload adds a row to the tray and returns callbacks to update it.
export function trackUpload(name) {
  ensureTray();
  tray.active++;
  updateTrayTitle();
  const bar = h("progress", { class: "tray-progress", max: "1", value: "0", "aria-label": `Uploading ${name}` });
  const status = h("span", { class: "tray-status" }, "Waiting…");
  const row = h("li", { class: "tray-item" },
    h("span", { class: "tray-name", title: name }, name), status, bar);
  tray.list.prepend(row);
  let done = false;
  const finish = () => { if (!done) { done = true; tray.active--; updateTrayTitle(); } };
  return {
    stage(text) { status.textContent = text; },
    progress(f) { bar.value = f; status.textContent = `${Math.round(f * 100)}%`; },
    ok() { bar.value = 1; status.textContent = "Done"; row.classList.add("is-done"); finish(); },
    fail(message) { row.classList.add("is-error"); status.textContent = message; bar.remove(); finish(); },
  };
}

// dropZone makes target accept dragged files and calls onFiles with them.
// The overlay is shown while files hover. enabled() can veto drops.
export function dropZone(target, { label, onFiles, enabled = () => true }) {
  const overlay = h("div", { class: "drop-overlay", "aria-hidden": "true" },
    h("div", { class: "drop-card" }, icon("upload", "icon icon-lg"), h("span", {}, label)));
  let depth = 0;
  const hasFiles = (e) => Array.from(e.dataTransfer?.types || []).includes("Files");
  const show = (on) => {
    if (on && !overlay.isConnected) target.append(overlay);
    overlay.classList.toggle("is-visible", on);
  };
  target.addEventListener("dragenter", (e) => {
    if (!hasFiles(e) || !enabled()) return;
    e.preventDefault();
    depth++;
    show(true);
  });
  target.addEventListener("dragover", (e) => {
    if (!hasFiles(e) || !enabled()) return;
    e.preventDefault();
    e.dataTransfer.dropEffect = "copy";
  });
  target.addEventListener("dragleave", () => {
    depth = Math.max(0, depth - 1);
    if (depth === 0) show(false);
  });
  target.addEventListener("drop", (e) => {
    if (!hasFiles(e) || !enabled()) return;
    e.preventDefault();
    depth = 0;
    show(false);
    const files = Array.from(e.dataTransfer.files || []).filter((f) => f.size > 0 || f.type);
    if (files.length) onFiles(files);
  });
}

// pickFiles opens the file chooser.
export function pickFiles({ accept = "", multiple = true } = {}) {
  return new Promise((resolve) => {
    const input = h("input", { type: "file", accept: accept || null, multiple, class: "visually-hidden" });
    input.addEventListener("change", () => { resolve(Array.from(input.files || [])); input.remove(); });
    input.addEventListener("cancel", () => { resolve([]); input.remove(); });
    document.body.append(input);
    input.click();
  });
}

// runQueue runs async jobs with a small concurrency limit.
export async function runQueue(items, limit, job) {
  let i = 0;
  const workers = Array.from({ length: Math.min(limit, items.length) }, async () => {
    while (i < items.length) {
      const item = items[i++];
      await job(item);
    }
  });
  await Promise.all(workers);
}
