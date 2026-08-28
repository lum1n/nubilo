(function () {
  "use strict";

  const $ = (sel, el = document) => el.querySelector(sel);
  const content = $("#content");
  const pageTitle = $("#page-title");
  const topbarActions = $("#topbar-actions");
  const loginEl = $("#login");
  const appEl = $("#app");
  const toastEl = $("#toast");

  const routes = {
    home: { title: "overview", render: renderHome },
    photos: { title: "photos", render: renderPhotos },
    calendar: { title: "calendar", render: renderCalendar },
    reminders: { title: "reminders", render: renderReminders },
    contacts: { title: "contacts", render: renderContacts },
    files: { title: "files", render: renderFiles },
    ops: { title: "ops", render: renderOps },
    settings: { title: "settings", render: renderSettings },
  };

  async function api(path, opts = {}) {
    const headers = { ...(opts.headers || {}) };
    if (opts.body && !headers["Content-Type"]) {
      headers["Content-Type"] = "application/json";
    }
    const res = await fetch("/api" + path, {
      credentials: "same-origin",
      headers,
      ...opts,
    });
    if (res.status === 401) {
      showLogin();
      throw new Error("unauthorized");
    }
    if (!res.ok) {
      const t = await res.text();
      throw new Error(t || res.statusText);
    }
    const ct = res.headers.get("content-type") || "";
    if (ct.includes("application/json")) return res.json();
    return res;
  }

  function toast(msg) {
    toastEl.textContent = msg;
    toastEl.classList.remove("hidden");
    setTimeout(() => toastEl.classList.add("hidden"), 4000);
  }

  function fmtDate(ms) {
    if (!ms) return "—";
    return new Date(ms).toLocaleString(undefined, {
      dateStyle: "medium",
      timeStyle: "short",
    });
  }

  function fmtSize(n) {
    if (!n) return "0 B";
    const u = ["B", "KB", "MB", "GB"];
    let i = 0;
    while (n >= 1024 && i < u.length - 1) {
      n /= 1024;
      i++;
    }
    return n.toFixed(i ? 1 : 0) + " " + u[i];
  }

  function esc(s) {
    const d = document.createElement("div");
    d.textContent = s == null ? "" : String(s);
    return d.innerHTML;
  }

  function kvRow(label, value) {
    return `<tr><td>${esc(label)}</td><td>${value}</td></tr>`;
  }

  function route() {
    const hash = location.hash.slice(1) || "/";
    const name = hash.replace(/^\//, "").split("/")[0] || "home";
    const r = routes[name] || routes.home;
    document.querySelectorAll(".nav-link").forEach((a) => {
      a.classList.toggle("active", a.dataset.route === name);
    });
    pageTitle.textContent = r.title;
    topbarActions.innerHTML = "";
    Promise.resolve(r.render()).catch((e) => {
      content.innerHTML = `<div class="empty">${esc(e.message || String(e))}</div>`;
    });
  }

  async function renderHome() {
    content.innerHTML = '<div class="empty">loading</div>';
    try {
      const [o, health] = await Promise.all([api("/overview"), api("/health")]);
      const checks = (health.checks || [])
        .map((c) => {
          const mark = c.status === "ok" ? "ok" : c.status === "warn" ? "!!" : c.status === "fail" ? "XX" : "--";
          let row = `<tr><td class="health-${esc(c.status)}">${mark}</td><td>${esc(c.title)}</td><td>${esc(c.detail || "")}</td></tr>`;
          if (c.fix && c.status !== "ok" && c.status !== "info") {
            row += `<tr class="health-fix"><td></td><td colspan="2">fix: ${esc(c.fix)}</td></tr>`;
          }
          return row;
        })
        .join("");
      const banner = health.healthy
        ? `<p class="hint">health: ok — ${health.ok || 0} checks passed</p>`
        : `<p class="hint accent">health: needs attention — ${health.fail || 0} fail, ${health.warn || 0} warn</p>`;
      let backupBtn = "";
      const bak = (health.checks || []).find((c) => c.id === "backup" && c.status === "fail");
      if (bak) {
        backupBtn = ` <button type="button" class="btn sm primary" id="enable-bak">enable auto-backup</button>`;
      }
      content.innerHTML = `
        <div class="section">
          <p class="section-title">health</p>
          ${banner}${backupBtn}
          <div class="block"><table class="data health-table"><tbody>${checks}</tbody></table></div>
        </div>
        <div class="section">
          <p class="section-title">counts</p>
          <table class="kv">
            ${kvRow("photos", o.counts.photos || 0)}
            ${kvRow("events", o.counts.calendar || 0)}
            ${kvRow("reminders", o.counts.reminders || 0)}
            ${kvRow("contacts", o.counts.addressbook || 0)}
            ${kvRow("files", o.counts.files || 0)}
            ${kvRow("devices", `${o.devices_active || 0} active / ${o.devices || 0}`)}
          </table>
        </div>
        <div class="section">
          <p class="section-title">server</p>
          <table class="kv">
            ${kvRow("version", esc(o.version))}
            ${kvRow("data_dir", esc(o.data_dir))}
            ${kvRow("listen", esc(o.listen))}
            ${kvRow("head_seq", o.head_seq)}
            ${kvRow("tls_cert", esc(o.tls_cert || "—"))}
          </table>
        </div>
        <p class="hint">pair / verify / gc → <a href="#/ops">ops</a> · CLI: <code>nubilo doctor</code> / <code>nubilo setup</code></p>`;
      const btn = $("#enable-bak");
      if (btn) {
        btn.onclick = async () => {
          try {
            const r = await api("/setup/backup", { method: "POST", body: "{}" });
            if (r.passphrase) {
              alert("Auto-backup enabled.\n\nSave this passphrase offline (shown once):\n\n" + r.passphrase + "\n\nFile: " + r.passphrase_file);
            } else {
              toast("auto-backup enabled");
            }
            renderHome();
          } catch (e) {
            toast(e.message);
          }
        };
      }
    } catch (e) {
      content.innerHTML = `<div class="empty">${esc(e.message)}</div>`;
    }
  }

  async function renderPhotos() {
    content.innerHTML = '<div class="empty">loading</div>';
    try {
      const data = await api("/photos");
      const photos = data.photos || [];
      const bar = `<div class="toolbar">
        <label class="btn sm">upload / camera
          <input type="file" accept="image/*,video/*" capture="environment" multiple hidden id="photo-upload">
        </label>
        <span class="hint">iPhone: Files → WebDAV folder “Camera Upload”, or Shortcuts POST /api/v1/photos</span>
      </div>`;
      if (!photos.length) {
        content.innerHTML = bar + '<div class="empty">no photos yet — upload here, Camera Upload folder, or enable photokit on mac</div>';
      } else {
        content.innerHTML =
          bar +
          `<div class="photo-grid">${photos
            .map((p) => {
              const kind = p.kind || "image";
              const cap = [p.name || "", kind !== "image" ? kind : "", fmtSize(p.size), fmtTaken(p.taken_at_ms)]
                .filter(Boolean)
                .join(" · ");
              const thumb =
                kind === "video" && !p.thumb_hash
                  ? `<div class="photo-placeholder">▶ video</div>`
                  : `<img loading="lazy" src="/api/photos/${encodeURIComponent(p.id)}/thumb" alt="">`;
              return `<div class="photo-card" data-id="${esc(p.id)}" title="${esc(cap)}">
            ${thumb}
            <span>${esc(cap)}</span>
          </div>`;
            })
            .join("")}</div>`;
        content.querySelectorAll(".photo-card").forEach((card) => {
          card.addEventListener("click", () => openLightbox(card.dataset.id, photos));
        });
      }
      const input = $("#photo-upload");
      if (input) {
        input.onchange = async () => {
          const files = [...(input.files || [])];
          input.value = "";
          for (const f of files) {
            try {
              const res = await fetch("/api/photos?name=" + encodeURIComponent(f.name), {
                method: "POST",
                credentials: "same-origin",
                headers: { "Content-Type": f.type || "application/octet-stream" },
                body: f,
              });
              if (!res.ok) throw new Error(await res.text());
              toast("uploaded " + f.name);
            } catch (e) {
              toast(e.message || "upload failed");
            }
          }
          renderPhotos();
        };
      }
    } catch (e) {
      content.innerHTML = `<div class="empty">${esc(e.message)}</div>`;
    }
  }

  function fmtTaken(ms) {
    if (!ms) return "";
    try {
      return new Date(ms).toISOString().slice(0, 10);
    } catch (_) {
      return "";
    }
  }

  function openLightbox(id, photos) {
    const p = (photos || []).find((x) => x.id === id) || { id };
    const kind = p.kind || "image";
    const lb = $("#lightbox");
    const img = $("#lightbox-img");
    const vid = $("#lightbox-video");
    const meta = $("#lightbox-meta");
    const actions = $("#lightbox-actions");
    img.classList.add("hidden");
    img.removeAttribute("src");
    vid.classList.add("hidden");
    vid.pause();
    vid.removeAttribute("src");
    vid.load();
    if (kind === "video") {
      const url = `/api/photos/${encodeURIComponent(id)}/original`;
      vid.src = url;
      if (p.mime) vid.setAttribute("type", p.mime);
      else vid.removeAttribute("type");
      vid.classList.remove("hidden");
      vid.load();
      vid.play().catch(() => {});
    } else {
      img.src = `/api/photos/${encodeURIComponent(id)}/preview`;
      img.classList.remove("hidden");
    }
    meta.textContent = [p.name, kind, fmtSize(p.size), fmtTaken(p.taken_at_ms)].filter(Boolean).join(" · ");
    let acts = `<a class="btn sm" href="/api/photos/${encodeURIComponent(id)}/original?download=1">download original</a>`;
    if (p.live_pair_hash) {
      acts += ` <a class="btn sm" href="/api/photos/${encodeURIComponent(id)}/live?download=1">download live movie</a>`;
    }
    if (kind === "raw") {
      acts += ` <span class="hint">raw · no develop</span>`;
    }
    acts += ` <button type="button" class="btn sm danger" id="lb-delete">delete</button>`;
    actions.innerHTML = acts;
    lb.classList.remove("hidden");
    const close = () => {
      lb.classList.add("hidden");
      vid.pause();
      vid.removeAttribute("src");
      vid.load();
    };
    vid.onerror = () => {
      const hint = " · browser cannot decode this codec (download to play)";
      if (!meta.textContent.includes("cannot decode")) meta.textContent += hint;
    };
    $(".lightbox-close", lb).onclick = close;
    lb.onclick = (ev) => {
      if (ev.target === lb) close();
    };
    $("#lb-delete").onclick = async () => {
      if (!confirm("delete this photo from the server?")) return;
      try {
        await api("/objects/" + encodeURIComponent(id), { method: "DELETE" });
        toast("deleted");
        close();
        renderPhotos();
      } catch (e) {
        toast(e.message);
      }
    };
    const onKey = (ev) => {
      if (ev.key === "Escape") {
        close();
        document.removeEventListener("keydown", onKey);
      }
    };
    document.addEventListener("keydown", onKey);
  }

  async function renderBrowse(kind, emptyMsg, createLabel, opts = {}) {
    const query = opts.query || "";
    const createBody = opts.createBody || {};
    content.innerHTML = '<div class="empty">loading</div>';
    try {
      const data = await api("/collections?kind=" + encodeURIComponent(kind) + query);
      const cols = data.collections || [];
      topbarActions.innerHTML = `<button type="button" class="btn sm" id="create-col">${esc(createLabel)}</button>`;
      $("#create-col").onclick = async () => {
        const name = prompt("name:");
        if (!name) return;
        try {
          await api("/collections", {
            method: "POST",
            body: JSON.stringify({ kind, name, ...createBody }),
          });
          toast("created " + name);
          renderBrowse(kind, emptyMsg, createLabel, opts);
        } catch (e) {
          toast(e.message);
        }
      };
      if (!cols.length) {
        content.innerHTML = `<div class="empty">${emptyMsg}</div>`;
        return;
      }
      let active = cols[0].id;
      let rootActive = cols[0].id;
      const tabs = `<div class="tabs-row">
        <div class="tabs">${cols
          .map(
            (c) =>
              `<button type="button" class="tab${c.id === active ? " active" : ""}" data-id="${esc(c.id)}">${esc(c.name)}</button>`
          )
          .join("")}</div>
        <div class="tab-actions">
          <button type="button" class="btn sm" id="rename-col">rename</button>
          <button type="button" class="btn sm danger" id="delete-col">delete</button>
        </div>
      </div>
      <div id="folder-trail" class="folder-trail hidden"></div>`;
      content.innerHTML = tabs + '<div id="browse-list"></div>';
      const listEl = $("#browse-list");
      if (kind === "files") {
        topbarActions.innerHTML += ` <label class="btn sm" style="cursor:pointer">upload<input type="file" id="upload-file" hidden></label>`;
        const input = $("#upload-file");
        if (input) {
          input.onchange = async () => {
            const file = input.files && input.files[0];
            if (!file) return;
            try {
              const res = await fetch("/api/collections/" + encodeURIComponent(active) + "/upload?name=" + encodeURIComponent(file.name), {
                method: "POST",
                credentials: "same-origin",
                headers: { "Content-Type": "application/octet-stream", "X-Filename": file.name },
                body: file,
              });
              if (!res.ok) throw new Error(await res.text());
              toast("uploaded " + file.name);
              loadCol(active);
            } catch (e) {
              toast(e.message);
            }
            input.value = "";
          };
        }
      }

      async function loadCol(id) {
        listEl.innerHTML = '<div class="empty">loading</div>';
        const res = await api("/collections/" + encodeURIComponent(id) + "/objects");
        const objs = res.objects || [];
        const children = res.children || [];
        if (!objs.length && !children.length) {
          listEl.innerHTML = '<div class="empty">empty</div>';
          return;
        }
        const folderRows = children
          .map(
            (c) => `<li class="list-item folder-item" data-folder-id="${esc(c.id)}" data-folder-name="${esc(c.name)}">
              <div class="list-item-main"><p class="list-item-title">${esc(c.name)}/</p><p class="list-item-sub">folder</p></div>
            </li>`
          )
          .join("");
        const fileRows = objs.map((o) => itemRow(kind, o)).join("");
        listEl.innerHTML = `<div class="block"><ul class="list">${folderRows}${fileRows}</ul></div>`;
        listEl.querySelectorAll(".folder-item").forEach((el) => {
          el.addEventListener("click", () => {
            const fid = el.dataset.folderId;
            const fname = el.dataset.folderName || "folder";
            if (kind === "files" && typeof pushFolder === "function") {
              pushFolder(fid, fname);
            } else {
              loadCol(fid);
            }
          });
        });
        listEl.querySelectorAll(".list-item[data-blob]").forEach((el) => {
          el.querySelector(".open-item")?.addEventListener("click", (ev) => {
            ev.stopPropagation();
            showBlobDetail(kind, el.dataset);
          });
          el.querySelector(".del-item")?.addEventListener("click", async (ev) => {
            ev.stopPropagation();
            if (!confirm("delete this item?")) return;
            try {
              await api("/objects/" + encodeURIComponent(el.dataset.id), { method: "DELETE" });
              toast("deleted");
              loadCol(id);
            } catch (e) {
              toast(e.message);
            }
          });
        });
      }

      let folderStack = [];
      function renderTrail() {
        const trail = $("#folder-trail");
        if (!trail) return;
        if (kind !== "files" || folderStack.length <= 1) {
          trail.innerHTML = "";
          trail.classList.add("hidden");
          return;
        }
        trail.classList.remove("hidden");
        trail.innerHTML = folderStack
          .map((s, i) => {
            if (i === folderStack.length - 1) {
              return `<span class="trail-here">${esc(s.name)}</span>`;
            }
            return `<button type="button" class="tab trail-seg" data-idx="${i}">${esc(s.name)}</button>`;
          })
          .join(` <span class="muted">/</span> `);
        trail.querySelectorAll(".trail-seg").forEach((btn) => {
          btn.addEventListener("click", () => {
            const idx = parseInt(btn.dataset.idx, 10);
            folderStack = folderStack.slice(0, idx + 1);
            active = folderStack[folderStack.length - 1].id;
            renderTrail();
            loadCol(active);
          });
        });
      }
      function pushFolder(fid, fname) {
        folderStack.push({ id: fid, name: fname });
        active = fid;
        renderTrail();
        loadCol(active);
      }
      function resetFolderStack(rootId, rootName) {
        folderStack = [{ id: rootId, name: rootName || "root" }];
        active = rootId;
        renderTrail();
      }

      function bindColActions() {
        $("#rename-col").onclick = async () => {
          const cur = cols.find((c) => c.id === rootActive);
          const name = prompt("new name:", cur?.name || "");
          if (!name) return;
          try {
            await api("/collections/" + encodeURIComponent(rootActive) + "/rename", {
              method: "POST",
              body: JSON.stringify({ name }),
            });
            toast("renamed");
            renderBrowse(kind, emptyMsg, createLabel, opts);
          } catch (e) {
            toast(e.message);
          }
        };
        $("#delete-col").onclick = async () => {
          const cur = cols.find((c) => c.id === rootActive);
          if (!confirm(`delete collection "${cur?.name}" and all items?`)) return;
          try {
            await api("/collections/" + encodeURIComponent(rootActive), { method: "DELETE" });
            toast("deleted collection");
            renderBrowse(kind, emptyMsg, createLabel, opts);
          } catch (e) {
            toast(e.message);
          }
        };
      }

      content.querySelectorAll(".tabs .tab").forEach((btn) => {
        btn.addEventListener("click", () => {
          rootActive = btn.dataset.id;
          content.querySelectorAll(".tabs .tab").forEach((c) => c.classList.toggle("active", c.dataset.id === rootActive));
          const cur = cols.find((c) => c.id === rootActive);
          resetFolderStack(rootActive, cur?.name || "root");
          loadCol(active);
        });
      });
      bindColActions();
      {
        const cur = cols.find((c) => c.id === rootActive);
        resetFolderStack(rootActive, cur?.name || "root");
      }
      await loadCol(active);
    } catch (e) {
      content.innerHTML = `<div class="empty">${esc(e.message)}</div>`;
    }
  }

  function itemRow(kind, o) {
    let title = o.summary || o.display_name || o.name || o.id;
    let sub = "";
    if (kind === "calendar") {
      if ((o.comp || "").toUpperCase() === "VTODO") {
        const bits = [];
        if (o.status) bits.push(String(o.status).toLowerCase());
        if (o.due_ms) bits.push("due " + fmtDate(o.due_ms));
        else if (o.start_ms) bits.push(fmtDate(o.start_ms));
        sub = bits.join(" · ");
      } else {
        sub = fmtDate(o.start_ms);
      }
    } else if (kind === "addressbook") {
      const bits = [o.email, o.phone, o.birthday, o.uid].filter(Boolean);
      sub = bits.join(" · ");
    } else if (kind === "files") sub = (o.mime || "file") + " · " + fmtSize(o.size);
    return `<li class="list-item" data-blob="${o.blob_id || ""}" data-id="${esc(o.id)}" data-name="${esc(o.name)}" data-mime="${esc(o.mime || "")}">
      <div class="list-item-main open-item" style="cursor:pointer">
        <p class="list-item-title">${esc(title)}</p>
        ${sub ? `<p class="list-item-sub">${esc(sub)}</p>` : ""}
      </div>
      <span class="list-item-meta">${fmtSize(o.size)}</span>
      <button type="button" class="btn sm danger del-item">del</button>
    </li>`;
  }

  async function showBlobDetail(kind, ds) {
    if (!ds.blob) return;
    content.innerHTML = '<div class="empty">loading</div>';
    const mime =
      kind === "calendar"
        ? "text/calendar"
        : kind === "addressbook"
          ? "text/vcard"
          : ds.mime || "application/octet-stream";
    const dl =
      `/api/blobs/${ds.blob}?mime=${encodeURIComponent(mime)}&name=${encodeURIComponent(ds.name || "blob")}&download=1`;
    const res = await fetch(`/api/blobs/${ds.blob}?mime=${encodeURIComponent(mime)}`, { credentials: "same-origin" });
    const text = await res.text();
    content.innerHTML = `
      <div class="block">
        <div class="block-head">
          <h3>${esc(ds.name)}</h3>
          <div>
            <a class="btn sm" href="${dl}">download</a>
            <button type="button" class="btn sm" id="back-btn">back</button>
          </div>
        </div>
        <pre class="detail">${esc(text)}</pre>
      </div>`;
    $("#back-btn").onclick = () => {
      if (kind === "calendar") {
        if (location.hash.includes("reminders")) renderReminders();
        else renderCalendar();
      } else if (kind === "addressbook") renderContacts();
      else renderFiles();
    };
  }

  function renderCalendar() {
    return renderBrowse("calendar", "no calendars — create one", "new calendar", {
      query: "&comp=VEVENT",
    });
  }
  function renderReminders() {
    return renderBrowse("calendar", "no reminder lists — create one", "new reminder list", {
      query: "&comp=VTODO",
      createBody: { comp: "VTODO" },
    });
  }
  function renderContacts() {
    return renderBrowse("addressbook", "no address books — create one", "new address book");
  }
  function renderFiles() {
    return renderBrowse("files", "no file collections — create one", "new folder");
  }

  let pairPoll = null;
  let opsGen = 0;

  async function renderOps() {
    const gen = ++opsGen;
    if (pairPoll) {
      clearInterval(pairPoll);
      pairPoll = null;
    }
    content.innerHTML = '<div class="empty">loading</div>';
    let st = {};
    try {
      st = await api("/status");
    } catch (_) {}
    if (gen !== opsGen) return;
    const lastBackup = st.last_backup_unix_ms
      ? new Date(st.last_backup_unix_ms).toISOString()
      : "—";
    content.innerHTML = `
      <div class="section">
        <p class="section-title">status</p>
        <div class="block">
          <table class="kv">
            ${kvRow("version", esc(st.version || ""))}
            ${kvRow("listen", esc(st.listen || ""))}
            ${kvRow("head_seq", st.head_seq ?? "—")}
            ${kvRow("devices", `${st.devices || 0} active / ${st.devices_revoked || 0} revoked`)}
            ${kvRow("blobs", `${st.blob_count || 0} · ${fmtSize(st.blob_bytes || 0)}`)}
            ${kvRow("last_backup", esc(lastBackup))}
            ${kvRow("backup_error", esc(st.last_backup_error || "—"))}
          </table>
        </div>
      </div>
      <div class="section">
        <p class="section-title">pair</p>
        <div class="block">
          <div class="block-head">
            <h3>pairing code</h3>
            <div>
              <select id="pair-role">
                <option value="agent">agent</option>
                <option value="client">client</option>
              </select>
              <button type="button" class="btn sm primary" id="pair-start">start</button>
            </div>
          </div>
          <div class="form-body" id="pair-out"><p class="hint">generates a code for nubilo pair --server …</p></div>
        </div>
      </div>
      <div class="section">
        <p class="section-title">verify</p>
        <div class="block">
          <div class="block-head">
            <h3>integrity</h3>
            <div>
              <button type="button" class="btn sm" id="verify-run">check</button>
              <button type="button" class="btn sm" id="verify-repair">repair</button>
            </div>
          </div>
          <pre class="detail" id="verify-out">// not run — use check (walks the store)</pre>
        </div>
      </div>
      <div class="section">
        <p class="section-title">gc</p>
        <div class="block">
          <div class="block-head">
            <h3>garbage collect</h3>
            <div>
              <button type="button" class="btn sm" id="gc-dry">dry-run</button>
              <button type="button" class="btn sm danger" id="gc-apply">apply</button>
            </div>
          </div>
          <pre class="detail" id="gc-out">// not run</pre>
        </div>
      </div>
      <div class="section">
        <p class="section-title">backup</p>
        <div class="block form-grid">
          <div class="block-head"><h3>create encrypted backup</h3></div>
          <div class="form-body">
            <div class="form-row"><label>passphrase</label><input type="password" id="bak-pass" autocomplete="new-password"></div>
            <button type="button" class="btn sm primary" id="bak-create">create + download</button>
            <p class="hint" id="bak-out"></p>
          </div>
        </div>
        <div class="block form-grid" style="margin-top:1rem">
          <div class="block-head"><h3>restore to empty dest</h3></div>
          <div class="form-body">
            <div class="form-row"><label>archive</label><input type="file" id="restore-file"></div>
            <div class="form-row"><label>passphrase</label><input type="password" id="restore-pass" autocomplete="off"></div>
            <div class="form-row"><label>dest path</label><input id="restore-dest" placeholder="/path/to/empty/dir"></div>
            <div class="form-row"><label>confirm</label><input id="restore-confirm" placeholder="RESTORE"></div>
            <button type="button" class="btn sm danger" id="restore-run">restore</button>
            <p class="hint">refuses live data_dir; for that stop the server and use CLI</p>
          </div>
        </div>
      </div>
      <div class="section">
        <p class="section-title">tls</p>
        <div class="block form-grid">
          <div class="block-head"><h3>regenerate certificate</h3></div>
          <div class="form-body">
            <div class="form-row"><label>extra hosts</label><input id="tls-hosts" placeholder="host1,host2"></div>
            <button type="button" class="btn sm" id="tls-regen">regen</button>
            <p class="hint" id="tls-out">cert ${esc(st.tls_cert || "—")}</p>
          </div>
        </div>
      </div>`;

    const on = (id, fn) => {
      const el = $("#" + id);
      if (el) el.onclick = fn;
    };

    on("pair-start", async () => {
      try {
        const r = await api("/pair", {
          method: "POST",
          body: JSON.stringify({ role: $("#pair-role").value }),
        });
        const out = $("#pair-out");
        if (!out) return;
        out.innerHTML = `
          <table class="kv">
            ${kvRow("code", `<strong class="accent">${esc(r.code)}</strong>`)}
            ${kvRow("role", esc(r.role))}
            ${kvRow("expires", esc(r.expires))}
            ${kvRow("status", '<span id="pair-status">waiting…</span>')}
          </table>
          <p class="hint">on the agent: nubilo pair --data-dir ~/.nubilo-agent --server https://HOST:8443 --code ${esc(r.code)} --name NAME</p>`;
        if (pairPoll) clearInterval(pairPoll);
        pairPoll = setInterval(async () => {
          try {
            const st2 = await api("/pair/" + encodeURIComponent(r.id));
            const el = $("#pair-status");
            if (!el) {
              clearInterval(pairPoll);
              return;
            }
            if (st2.completed) {
              el.textContent = "paired " + (st2.device_id || "");
              clearInterval(pairPoll);
              pairPoll = null;
              toast("device paired");
            } else if (st2.expired) {
              el.textContent = "expired";
              clearInterval(pairPoll);
              pairPoll = null;
            }
          } catch (_) {
            clearInterval(pairPoll);
            pairPoll = null;
          }
        }, 800);
      } catch (e) {
        toast(e.message);
      }
    });

    const runVerify = async (repair) => {
      const out = $("#verify-out");
      if (!out) return;
      out.textContent = "running…";
      try {
        const r = await api("/verify", { method: "POST", body: JSON.stringify({ repair }) });
        if (r.ok) {
          out.textContent = "ok" + (repair ? ` (orphans=${r.orphans_removed || 0} refs=${r.refcounts_repaired || 0})` : "");
        } else {
          const lines = (r.issues || []).map((i) => (i.kind || "") + ": " + (i.message || "") + (i.ref ? " (" + i.ref + ")" : ""));
          out.textContent = lines.join("\n") || "issues found";
        }
      } catch (e) {
        out.textContent = e.message;
      }
    };
    on("verify-run", () => runVerify(false));
    on("verify-repair", () => {
      if (!confirm("repair orphan blobs and refcounts?")) return;
      runVerify(true);
    });

    const runGC = async (apply) => {
      if (apply && !confirm("delete unreferenced blobs and compact tombstones?")) return;
      const out = $("#gc-out");
      if (!out) return;
      out.textContent = "running…";
      try {
        const r = await api("/gc", { method: "POST", body: JSON.stringify({ apply }) });
        out.textContent = JSON.stringify(r, null, 2);
      } catch (e) {
        out.textContent = e.message;
      }
    };
    on("gc-dry", () => runGC(false));
    on("gc-apply", () => runGC(true));

    on("bak-create", async () => {
      const passphrase = $("#bak-pass")?.value;
      if (!passphrase) {
        toast("passphrase required");
        return;
      }
      const hint = $("#bak-out");
      if (hint) hint.textContent = "creating…";
      try {
        const r = await api("/backup", { method: "POST", body: JSON.stringify({ passphrase }) });
        if (hint) hint.textContent = "ready — downloading…";
        window.location.href = r.download;
        toast("backup ready");
      } catch (e) {
        if (hint) hint.textContent = e.message;
        toast(e.message);
      }
    });

    on("restore-run", async () => {
      const file = $("#restore-file")?.files?.[0];
      if (!file) {
        toast("choose archive");
        return;
      }
      const fd = new FormData();
      fd.append("archive", file);
      fd.append("passphrase", $("#restore-pass")?.value || "");
      fd.append("dest", $("#restore-dest")?.value || "");
      fd.append("confirm", $("#restore-confirm")?.value || "");
      try {
        const res = await fetch("/api/restore", { method: "POST", body: fd, credentials: "same-origin" });
        const t = await res.text();
        if (!res.ok) throw new Error(t || res.statusText);
        toast("restored");
      } catch (e) {
        toast(e.message);
      }
    });

    on("tls-regen", async () => {
      try {
        const r = await api("/tls", { method: "POST", body: JSON.stringify({ hosts: $("#tls-hosts")?.value || "" }) });
        const out = $("#tls-out");
        if (out) out.textContent = r.cert + " — " + (r.restart || "");
        toast("tls regenerated");
      } catch (e) {
        toast(e.message);
      }
    });
  }

  async function renderSettings() {
    content.innerHTML = '<div class="empty">loading</div>';
    try {
      const [cfg, devs] = await Promise.all([api("/config"), api("/devices")]);
      const devices = devs.devices || [];
      content.innerHTML = `
        <div class="section">
          <p class="section-title">config</p>
          <div class="block form-grid">
            <div class="block-head">
              <h3>config.json</h3>
              <button type="button" class="btn sm primary" id="save-cfg">save</button>
            </div>
            <div class="form-body">
              <div class="form-row"><label>listen</label><input id="cfg-listen" value="${esc(cfg.listen)}"></div>
              <div class="form-row"><label>log level</label><input id="cfg-log" value="${esc(cfg.log?.level || "info")}"></div>
              <div class="form-row"><label>max blob (bytes)</label><input type="number" id="cfg-blob" value="${cfg.sync?.max_blob_bytes || 0}"></div>
              <div class="form-row"><label>max batch</label><input type="number" id="cfg-batch" value="${cfg.sync?.max_batch || 0}"></div>
              <div class="form-row"><label>pairing ttl (seconds)</label><input type="number" id="cfg-ttl" value="${cfg.pairing?.ttl_seconds || 0}"></div>
              <div class="form-row"><label>thumb max px</label><input type="number" id="cfg-thumb" value="${cfg.photos?.thumb_max_px || 0}"></div>
              <div class="form-row"><label>preview max px</label><input type="number" id="cfg-preview" value="${cfg.photos?.preview_max_px || 0}"></div>
              <div class="form-row"><label><input type="checkbox" id="cfg-gps" ${cfg.photos?.strip_gps_from_derivatives ? "checked" : ""}> strip gps from previews</label></div>
              <div class="form-row"><label><input type="checkbox" id="cfg-phash" ${cfg.photos?.perceptual_hash ? "checked" : ""}> perceptual hash</label></div>
              <div class="form-row"><label><input type="checkbox" id="cfg-tls-auto" ${cfg.tls?.auto ? "checked" : ""}> tls auto</label></div>
              <div class="form-row"><label><input type="checkbox" id="cfg-tls-insecure" ${cfg.tls?.allow_insecure_loopback ? "checked" : ""}> allow insecure loopback</label></div>
              <div class="form-row"><label><input type="checkbox" id="cfg-bak-on" ${cfg.backup?.enabled ? "checked" : ""}> auto-backup (server)</label></div>
              <div class="form-row"><label>backup interval (hours)</label><input type="number" id="cfg-bak-h" value="${cfg.backup?.interval_hours || 24}"></div>
              <div class="form-row"><label>backup keep</label><input type="number" id="cfg-bak-keep" value="${cfg.backup?.keep || 7}"></div>
              <div class="form-row"><label>passphrase file</label><input id="cfg-bak-pf" value="${esc(cfg.backup?.passphrase_file || "")}" placeholder="path on server disk"></div>
              <p class="hint">restart nubilo server after changing listen/tls/pairing rates; auto-backup runs in nubilo server</p>
            </div>
          </div>
        </div>
        <div class="section">
          <p class="section-title">devices</p>
          <div class="block form-grid">
            <div class="block-head"><h3>enroll pubkey</h3></div>
            <div class="form-body">
              <div class="form-row"><label>name</label><input id="enroll-name"></div>
              <div class="form-row"><label>role</label>
                <select id="enroll-role"><option value="client">client</option><option value="agent">agent</option></select>
              </div>
              <div class="form-row"><label>pubkey</label><textarea id="enroll-pub" rows="2" placeholder="hex or base64 (32 bytes)"></textarea></div>
              <button type="button" class="btn sm primary" id="enroll-run">enroll</button>
            </div>
          </div>
          <div class="block" style="margin-top:1rem">
            <div class="block-head">
              <h3>paired</h3>
              <button type="button" class="btn sm" id="new-pass">new password</button>
            </div>
            <ul class="list" id="dev-list">${devices
              .map(
                (d) => `<li class="list-item" style="cursor:default">
                <div class="list-item-main">
                  <p class="list-item-title">${esc(d.name)}${d.revoked ? " (revoked)" : ""}</p>
                  <p class="list-item-sub">${esc(d.id)} · ${esc(d.role)}${d.protocols ? " · " + esc((d.protocols || []).join(",")) : ""}</p>
                </div>
                ${
                  d.revoked
                    ? ""
                    : `<button type="button" class="btn sm rename" data-id="${esc(d.id)}" data-name="${esc(d.name)}">rename</button>
                       <button type="button" class="btn sm rotate" data-id="${esc(d.id)}">rotate</button>
                       <button type="button" class="btn sm danger revoke" data-id="${esc(d.id)}">revoke</button>`
                }
              </li>`
              )
              .join("")}</ul>
          </div>
        </div>`;
      $("#save-cfg").onclick = async () => {
        try {
          const r = await api("/config", {
            method: "PUT",
            body: JSON.stringify({
              listen: $("#cfg-listen").value,
              log: { level: $("#cfg-log").value },
              sync: {
                max_blob_bytes: parseInt($("#cfg-blob").value, 10) || undefined,
                max_batch: parseInt($("#cfg-batch").value, 10) || undefined,
              },
              pairing: { ttl_seconds: parseInt($("#cfg-ttl").value, 10) || undefined },
              photos: {
                strip_gps_from_derivatives: $("#cfg-gps").checked,
                perceptual_hash: $("#cfg-phash").checked,
                thumb_max_px: parseInt($("#cfg-thumb").value, 10) || undefined,
                preview_max_px: parseInt($("#cfg-preview").value, 10) || undefined,
              },
              tls: {
                auto: $("#cfg-tls-auto").checked,
                allow_insecure_loopback: $("#cfg-tls-insecure").checked,
              },
              backup: {
                enabled: $("#cfg-bak-on").checked,
                interval_hours: parseInt($("#cfg-bak-h").value, 10) || 24,
                keep: parseInt($("#cfg-bak-keep").value, 10) || 7,
                passphrase_file: $("#cfg-bak-pf").value,
              },
            }),
          });
          toast(r.restart || "saved");
        } catch (e) {
          toast(e.message);
        }
      };
      $("#enroll-run").onclick = async () => {
        try {
          const r = await api("/devices/enroll", {
            method: "POST",
            body: JSON.stringify({
              name: $("#enroll-name").value,
              role: $("#enroll-role").value,
              pubkey: $("#enroll-pub").value.trim(),
            }),
          });
          toast("enrolled " + r.id);
          renderSettings();
        } catch (e) {
          toast(e.message);
        }
      };
      $("#new-pass").onclick = async () => {
        const name = prompt("device name:");
        if (!name) return;
        const scope = prompt("scope: webdav, caldav, carddav, photos, all", "all");
        try {
          const r = await api("/devices/password", {
            method: "POST",
            body: JSON.stringify({ name, scope: scope || "all" }),
          });
          const urls = Object.entries(r.urls || {})
            .map(([k, v]) => `${k}: ${v}`)
            .join("\n");
          alert(`save this password now\n\nusername: ${r.device_id}\npassword: ${r.password}\n\n${urls}`);
          renderSettings();
        } catch (e) {
          toast(e.message);
        }
      };
      content.querySelectorAll(".rename").forEach((btn) => {
        btn.onclick = async () => {
          const name = prompt("new name:", btn.dataset.name);
          if (!name) return;
          try {
            await api("/devices/rename", { method: "POST", body: JSON.stringify({ id: btn.dataset.id, name }) });
            renderSettings();
          } catch (e) {
            toast(e.message);
          }
        };
      });
      content.querySelectorAll(".rotate").forEach((btn) => {
        btn.onclick = async () => {
          const pubkey = prompt("new pubkey (hex or base64):");
          if (!pubkey) return;
          try {
            await api("/devices/rotate", {
              method: "POST",
              body: JSON.stringify({ id: btn.dataset.id, pubkey: pubkey.trim() }),
            });
            toast("rotated");
            renderSettings();
          } catch (e) {
            toast(e.message);
          }
        };
      });
      content.querySelectorAll(".revoke").forEach((btn) => {
        btn.onclick = async () => {
          if (!confirm("revoke this device?")) return;
          await api("/devices/revoke", { method: "POST", body: JSON.stringify({ id: btn.dataset.id }) });
          renderSettings();
        };
      });
    } catch (e) {
      content.innerHTML = `<div class="empty">${esc(e.message)}</div>`;
    }
  }

  function showLogin() {
    loginEl.classList.remove("hidden");
    appEl.classList.add("hidden");
  }

  function showApp() {
    loginEl.classList.add("hidden");
    appEl.classList.remove("hidden");
    route();
  }

  async function init() {
    $("#login-btn").onclick = async () => {
      const token = $("#login-token").value.trim();
      try {
        await fetch("/api/login", {
          method: "POST",
          credentials: "same-origin",
          headers: { "Content-Type": "application/json" },
          body: JSON.stringify({ token }),
        }).then((r) => {
          if (!r.ok) throw new Error("invalid token");
        });
        showApp();
      } catch {
        toast("invalid admin token");
      }
    };
    try {
      const s = await api("/session");
      if (s.ok) {
        showApp();
        return;
      }
    } catch (_) {}
    showLogin();
    try {
      const info = await fetch("/api/info").then((r) => r.json());
      if (info.admin_token) $("#token-path").textContent = info.admin_token;
    } catch (_) {}
  }

  window.addEventListener("hashchange", () => {
    if (!appEl.classList.contains("hidden")) route();
  });
  init();
})();
