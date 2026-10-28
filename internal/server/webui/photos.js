// Photos: the library grid grouped by month, albums, the lightbox and
// uploads.

import { h, clear, icon, api, send, storageChanged, ulidTime, formatMonth, formatDateTime, formatBytes, formatDuration, APIError } from "./lib.js";
import { notice, trackUpload, dropZone, pickFiles, runQueue } from "./ui.js";

const PAGE = 200; // metadata page size
const CHUNK = 150; // tiles added per scroll step

// Album items hold their own copy of the photo's blobs, so URLs use the
// item's id; that also works for albums someone else shared.
const photoURL = (id, rendition) => `/web/api/photos/${encodeURIComponent(id)}/${rendition}`;
const takenAt = (p) => p.taken_at_ms || ulidTime(p.id) || 0;
const isVideo = (p) => p.kind === "video" || /^video\//.test(p.mime || "");

function describe(p) {
  const when = formatDateTime(takenAt(p));
  const what = isVideo(p) ? "Video" : p.kind === "live" ? "Live photo" : "Photo";
  return when ? `${what}, ${when}` : (p.name || what);
}

// pageAll loads every page of a listing, calling onPage as pages arrive.
async function pageAll(path, key, onPage, signal) {
  let after = "";
  const sep = path.includes("?") ? "&" : "?";
  for (;;) {
    const res = await api(`${path}${sep}limit=${PAGE}&after=${encodeURIComponent(after)}`, { signal });
    onPage(res[key] || []);
    if (!res.next) return;
    after = res.next;
  }
}

// ---- grid ----

// photoGrid renders photos newest first, grouped by month, adding tiles as
// the user scrolls. open(index) opens the lightbox.
function photoGrid(container, items, open) {
  clear(container);
  const sentinel = h("div", { class: "grid-sentinel", "aria-hidden": "true" });
  let rendered = 0;
  let section = null;
  let month = "";
  const vids = new IntersectionObserver((entries) => {
    for (const e of entries) {
      if (!e.isIntersecting) continue;
      const v = e.target;
      v.src = v.dataset.src + "#t=0.1";
      vids.unobserve(v);
    }
  }, { rootMargin: "400px" });

  function tile(p, i) {
    const id = p.id;
    const media = isVideo(p) && !p.thumb_hash
      ? h("video", { class: "tile-media", muted: true, playsinline: true, preload: "metadata", "aria-hidden": "true", tabindex: "-1", dataset: { src: photoURL(id, "original") } })
      : h("img", { class: "tile-media", src: photoURL(id, "thumb"), loading: "lazy", decoding: "async", alt: describe(p) });
    if (media.tagName === "VIDEO") vids.observe(media);
    media.addEventListener("error", () => { media.replaceWith(h("span", { class: "tile-missing" }, icon(isVideo(p) ? "play" : "photos", "icon icon-lg"))); });
    const badges = [];
    if (isVideo(p)) badges.push(h("span", { class: "tile-badge" }, icon("play", "icon icon-xs"), p.duration_ms ? formatDuration(p.duration_ms) : ""));
    else if (p.kind === "live") badges.push(h("span", { class: "tile-badge" }, "LIVE"));
    return h("button", { type: "button", class: "tile", "aria-label": media.tagName === "VIDEO" ? describe(p) : null, onclick: () => open(i) }, media, badges);
  }

  function more() {
    const end = Math.min(items.length, rendered + CHUNK);
    for (; rendered < end; rendered++) {
      const p = items[rendered];
      const m = formatMonth(takenAt(p) || Date.now());
      if (m !== month) {
        month = m;
        const grid = h("div", { class: "grid", role: "list" });
        section = { grid };
        container.insertBefore(h("section", { class: "month" }, h("h2", { class: "month-title" }, m), grid), sentinel);
      }
      section.grid.append(h("div", { role: "listitem", class: "cell" }, tile(p, rendered)));
    }
  }

  container.append(sentinel);
  const io = new IntersectionObserver((entries) => {
    if (entries.some((e) => e.isIntersecting) && rendered < items.length) more();
  }, { rootMargin: "1200px" });
  more();
  io.observe(sentinel);
  return {
    // ensure renders up to index i (for lightbox navigation).
    ensure(i) { while (rendered <= i && rendered < items.length) more(); },
    destroy() { io.disconnect(); vids.disconnect(); },
  };
}

// ---- lightbox ----

export function openLightbox(items, start, { onChange } = {}) {
  let index = start;
  const stage = h("div", { class: "lb-stage" });
  const title = h("p", { class: "lb-title" });
  const sub = h("p", { class: "lb-sub" });
  const counter = h("span", { class: "lb-counter" });
  const download = h("a", { class: "icon-btn lb-btn", "aria-label": "Download original", title: "Download original" }, icon("download"));
  const infoPanel = h("aside", { class: "lb-info", "aria-label": "Details" });
  const infoBtn = h("button", { type: "button", class: "icon-btn lb-btn", "aria-label": "Show details", "aria-pressed": "false", title: "Details" }, icon("info"));
  const prev = h("button", { type: "button", class: "lb-nav lb-prev", "aria-label": "Previous" }, icon("left", "icon icon-lg"));
  const next = h("button", { type: "button", class: "lb-nav lb-next", "aria-label": "Next" }, icon("right", "icon icon-lg"));
  const closeBtn = h("button", { type: "button", class: "icon-btn lb-btn", "aria-label": "Close", title: "Close (Esc)" }, icon("close"));
  const dlg = h("dialog", { class: "lightbox", "aria-label": "Photo viewer" },
    h("header", { class: "lb-bar" },
      closeBtn,
      h("div", { class: "lb-heading" }, title, sub),
      counter, infoBtn, download),
    h("div", { class: "lb-main" }, stage, prev, next, infoPanel));

  function detail(label, value) {
    return value ? [h("dt", {}, label), h("dd", {}, value)] : [];
  }

  function render() {
    const p = items[index];
    const id = p.id;
    title.textContent = p.name || (isVideo(p) ? "Video" : "Photo");
    sub.textContent = formatDateTime(takenAt(p));
    counter.textContent = `${index + 1} of ${items.length}`;
    download.href = photoURL(id, "original");
    download.setAttribute("download", p.name || id);
    prev.disabled = index === 0;
    next.disabled = index === items.length - 1;
    clear(stage);
    let media;
    if (isVideo(p)) {
      media = h("video", { class: "lb-media", controls: true, autoplay: true, playsinline: true, src: photoURL(id, "original"), poster: p.thumb_hash ? photoURL(id, "preview") : null });
    } else {
      media = h("img", { class: "lb-media is-loading", src: photoURL(id, "preview"), alt: describe(p) });
      media.addEventListener("load", () => media.classList.remove("is-loading"));
    }
    media.addEventListener("error", () => {
      media.replaceWith(h("div", { class: "lb-missing" }, icon("photos", "icon icon-xl"),
        h("p", {}, "This item can't be shown in the browser."),
        h("a", { class: "btn btn-primary", href: photoURL(id, "original"), download: p.name || id }, icon("download"), "Download original")));
    });
    stage.append(media);
    const dims = p.width && p.height ? `${p.width} × ${p.height}` : "";
    const camera = [p.camera_make, p.camera_model].filter(Boolean).join(" ");
    clear(infoPanel).append(h("dl", { class: "lb-details" },
      detail("Name", p.name), detail("Taken", formatDateTime(takenAt(p))), detail("Size", p.size ? formatBytes(p.size) : ""),
      detail("Dimensions", dims), detail("Camera", camera), detail("Duration", p.duration_ms ? formatDuration(p.duration_ms) : ""),
      detail("Type", p.mime)));
    // Warm the neighbours.
    for (const j of [index - 1, index + 1]) {
      const q = items[j];
      if (q && !isVideo(q)) new Image().src = photoURL(q.id, "preview");
    }
    onChange?.(index);
  }

  const go = (d) => {
    const j = index + d;
    if (j >= 0 && j < items.length) { index = j; render(); }
  };
  prev.addEventListener("click", () => go(-1));
  next.addEventListener("click", () => go(1));
  closeBtn.addEventListener("click", () => dlg.close());
  infoBtn.addEventListener("click", () => {
    const on = dlg.classList.toggle("show-info");
    infoBtn.setAttribute("aria-pressed", String(on));
  });
  dlg.addEventListener("keydown", (e) => {
    if (e.key === "ArrowLeft") { e.preventDefault(); go(-1); }
    else if (e.key === "ArrowRight") { e.preventDefault(); go(1); }
  });
  // Swipe on touch screens.
  let touchX = null;
  stage.addEventListener("touchstart", (e) => { touchX = e.touches.length === 1 ? e.touches[0].clientX : null; }, { passive: true });
  stage.addEventListener("touchend", (e) => {
    if (touchX == null) return;
    const dx = e.changedTouches[0].clientX - touchX;
    if (Math.abs(dx) > 50) go(dx < 0 ? 1 : -1);
    touchX = null;
  });
  stage.addEventListener("click", (e) => { if (e.target === stage) dlg.close(); });
  dlg.addEventListener("close", () => { dlg.querySelectorAll("video").forEach((v) => v.pause()); dlg.remove(); });
  document.body.append(dlg);
  render();
  dlg.showModal();
  closeBtn.focus();
}

// ---- library view ----

export function photosView(shell) {
  const ctl = new AbortController();
  let items = [];
  let grid = null;
  let loaded = false;
  const content = h("div", { class: "photos" });
  const status = h("p", { class: "muted loading-line" }, h("span", { class: "spinner", "aria-hidden": "true" }), "Loading photos…");

  shell.setHeader({ title: "Photos", subtitle: "" });
  const uploadBtn = h("button", { type: "button", class: "btn btn-primary", onclick: async () => upload(await pickFiles({ accept: "image/*,video/*" })) }, icon("upload"), h("span", { class: "btn-label" }, "Upload"));
  shell.setActions([uploadBtn]);
  shell.content.append(content);
  content.append(status);

  function sortItems() {
    items.sort((a, b) => takenAt(b) - takenAt(a) || (a.id < b.id ? 1 : -1));
  }

  function subtitle() {
    const photos = items.filter((p) => !isVideo(p)).length;
    const videos = items.length - photos;
    const parts = [];
    if (photos) parts.push(`${photos.toLocaleString()} ${photos === 1 ? "photo" : "photos"}`);
    if (videos) parts.push(`${videos.toLocaleString()} ${videos === 1 ? "video" : "videos"}`);
    shell.setHeader({ title: "Photos", subtitle: parts.join(" · ") + (loaded ? "" : "…") });
  }

  function render() {
    grid?.destroy();
    clear(content);
    if (!items.length) {
      content.append(h("div", { class: "empty" },
        h("div", { class: "empty-art", "aria-hidden": "true" }, icon("photos", "icon icon-xl")),
        h("h2", { class: "empty-title" }, "No photos yet"),
        h("p", { class: "muted" }, "Photos backed up from your phone or Mac appear here. You can also drop pictures and videos onto this page."),
        h("button", { type: "button", class: "btn btn-primary", onclick: async () => upload(await pickFiles({ accept: "image/*,video/*" })) }, icon("upload"), "Upload photos")));
      return;
    }
    const wrap = h("div", { class: "months" });
    content.append(wrap);
    grid = photoGrid(wrap, items, (i) => openLightbox(items, i, { onChange: (j) => grid.ensure(j) }));
  }

  async function upload(files) {
    if (!files.length) return;
    const added = [];
    await runQueue(files, 3, async (file) => {
      const t = trackUpload(file.name);
      try {
        const p = await send("POST", `photos?name=${encodeURIComponent(file.name)}`, file, {
          headers: { "Content-Type": file.type || "application/octet-stream" },
          onProgress: (f) => t.progress(f * 0.95),
        });
        t.ok();
        if (p && !items.some((q) => q.id === p.id)) added.push(p);
      } catch (e) {
        t.fail(e instanceof APIError && e.status === 400 ? "Not a photo or video Nubilo can read." : e.message);
        if (e.status === 507) notice(shell.notice, "Your storage is full. Some uploads didn't finish.");
      }
    });
    if (added.length) {
      storageChanged();
      items.push(...added);
      sortItems();
      subtitle();
      render();
    }
  }

  dropZone(shell.main, { label: "Drop to add to Photos", onFiles: upload });

  (async () => {
    try {
      await pageAll("photos", "photos", (page) => {
        items.push(...page);
        status.lastChild.textContent = `Loading photos… ${items.length.toLocaleString()}`;
        subtitle();
      }, ctl.signal);
      loaded = true;
      sortItems();
      subtitle();
      render();
    } catch (e) {
      if (e.name === "AbortError") return;
      clear(content);
      notice(shell.notice, e.message);
    }
  })();

  return () => { ctl.abort(); grid?.destroy(); };
}

// ---- albums ----

export function albumsView(shell, nav) {
  const ctl = new AbortController();
  shell.setHeader({ title: "Albums", subtitle: "" });
  shell.setActions([]);
  const content = h("div", { class: "albums" }, h("p", { class: "muted loading-line" }, h("span", { class: "spinner", "aria-hidden": "true" }), "Loading albums…"));
  shell.content.append(content);

  (async () => {
    try {
      const { albums = [] } = await api("albums", { signal: ctl.signal });
      clear(content);
      if (!albums.length) {
        content.append(h("div", { class: "empty" },
          h("div", { class: "empty-art", "aria-hidden": "true" }, icon("albums", "icon icon-xl")),
          h("h2", { class: "empty-title" }, "No albums yet"),
          h("p", { class: "muted" }, "Albums you make in the Nubilo app, and albums shared with you, show up here.")));
        return;
      }
      shell.setHeader({ title: "Albums", subtitle: `${albums.length} ${albums.length === 1 ? "album" : "albums"}` });
      const own = albums.filter((a) => a.access === "owner");
      const shared = albums.filter((a) => a.access !== "owner");
      const card = (a) => {
        const cover = h("span", { class: "album-cover" }, icon("albums", "icon icon-lg"));
        // Cover: the first item, fetched lazily.
        api(`albums/${encodeURIComponent(a.id)}/items?limit=1`, { signal: ctl.signal }).then((res) => {
          const first = res.items?.[0];
          if (!first) return;
          const img = h("img", { src: photoURL(first.id, "thumb"), alt: "", loading: "lazy" });
          img.addEventListener("error", () => img.remove());
          cover.append(img);
          cover.classList.add("has-image");
        }).catch(() => {});
        return h("li", {}, h("a", { class: "album-card", href: `/web/albums/${encodeURIComponent(a.id)}`, onclick: (e) => { e.preventDefault(); nav(`/web/albums/${a.id}`, { name: a.name }); } },
          cover,
          h("span", { class: "album-name" }, a.name),
          h("span", { class: "album-meta" }, a.access === "owner" ? "" : `Shared by ${a.owner || "someone"}${a.access === "ro" ? " · view only" : ""}`)));
      };
      if (own.length) content.append(h("ul", { class: "album-grid" }, own.map(card)));
      if (shared.length) content.append(h("h2", { class: "section-title" }, "Shared with you"), h("ul", { class: "album-grid" }, shared.map(card)));
    } catch (e) {
      if (e.name === "AbortError") return;
      clear(content);
      notice(shell.notice, e.message);
    }
  })();
  return () => ctl.abort();
}

export function albumView(shell, nav, albumID, hint = {}) {
  const ctl = new AbortController();
  let grid = null;
  const items = [];
  const back = h("a", { class: "crumb", href: "/web/albums", onclick: (e) => { e.preventDefault(); nav("/web/albums"); } }, icon("left", "icon icon-sm"), "Albums");
  shell.setHeader({ title: hint.name || "Album", subtitle: "", crumbs: [back] });
  shell.setActions([]);
  const content = h("div", { class: "photos" }, h("p", { class: "muted loading-line" }, h("span", { class: "spinner", "aria-hidden": "true" }), "Loading album…"));
  shell.content.append(content);

  (async () => {
    try {
      if (!hint.name) {
        api("albums", { signal: ctl.signal }).then(({ albums = [] }) => {
          const a = albums.find((x) => x.id === albumID);
          if (a) shell.setHeader({ title: a.name, subtitle: `${items.length} ${items.length === 1 ? "item" : "items"}`, crumbs: [back] });
        }).catch(() => {});
      }
      await pageAll(`albums/${encodeURIComponent(albumID)}/items`, "items", (page) => items.push(...page), ctl.signal);
      items.sort((a, b) => takenAt(b) - takenAt(a));
      clear(content);
      shell.setHeader({ title: shell.title(), subtitle: `${items.length} ${items.length === 1 ? "item" : "items"}`, crumbs: [back] });
      if (!items.length) {
        content.append(h("div", { class: "empty" },
          h("div", { class: "empty-art", "aria-hidden": "true" }, icon("albums", "icon icon-xl")),
          h("h2", { class: "empty-title" }, "This album is empty")));
        return;
      }
      const wrap = h("div", { class: "months" });
      content.append(wrap);
      grid = photoGrid(wrap, items, (i) => openLightbox(items, i, { onChange: (j) => grid.ensure(j) }));
    } catch (e) {
      if (e.name === "AbortError") return;
      clear(content);
      notice(shell.notice, e.message);
    }
  })();
  return () => { ctl.abort(); grid?.destroy(); };
}
