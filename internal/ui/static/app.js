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
    r.render();
  }

  async function renderHome() {
    content.innerHTML = '<div class="empty">loading</div>';
    try {
      const o = await api("/overview");
      content.innerHTML = `
        <div class="section">
          <p class="section-title">counts</p>
          <table class="kv">
            ${kvRow("photos", o.counts.photos || 0)}
            ${kvRow("events", o.counts.calendar || 0)}
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
        <p class="hint">pair / verify / gc → <a href="#/ops">ops</a></p>`;
    } catch (e) {
      content.innerHTML = `<div class="empty">${esc(e.message)}</div>`;
    }
  }

  async function renderPhotos() {
    content.innerHTML = '<div class="empty">loading</div>';
    try {
      const data = await api("/photos");
      const photos = data.photos || [];
      if (!photos.length) {
        content.innerHTML = '<div class="empty">no photos — enable photokit sync on mac agent</div>';
        return;
      }
      content.innerHTML = `<div class="photo-grid">${photos
        .map(
          (p) => `<div class="photo-card" data-id="${esc(p.id)}" title="${esc(p.name)}">
            <img loading="lazy" src="/api/photos/${encodeURIComponent(p.id)}/thumb" alt="">
            <span>${esc(p.name)}</span>
          </div>`
        )
        .join("")}</div>`;
      content.querySelectorAll(".photo-card").forEach((card) => {
        card.addEventListener("click", () => openLightbox(card.dataset.id));
      });
    } catch (e) {
      content.innerHTML = `<div class="empty">${esc(e.message)}</div>`;
    }
  }

  function openLightbox(id) {
    const lb = $("#lightbox");
    const img = $("#lightbox-img");
    img.src = `/api/photos/${encodeURIComponent(id)}/preview`;
    lb.classList.remove("hidden");
    const close = () => lb.classList.add("hidden");
    $(".lightbox-close", lb).onclick = close;
    lb.onclick = (ev) => {
      if (ev.target === lb) close();
    };
    const onKey = (ev) => {
      if (ev.key === "Escape") {
        close();
        document.removeEventListener("keydown", onKey);
      }
    };
    document.addEventListener("keydown", onKey);
  }

  async function renderBrowse(kind, emptyMsg, createLabel) {
    content.innerHTML = '<div class="empty">loading</div>';
    try {
      const data = await api("/collections?kind=" + encodeURIComponent(kind));
      const cols = data.collections || [];
      topbarActions.innerHTML = `<button type="button" class="btn sm" id="create-col">${esc(createLabel)}</button>`;
      $("#create-col").onclick = async () => {
        const name = prompt("name:");
        if (!name) return;
        try {
          await api("/collections", { method: "POST", body: JSON.stringify({ kind, name }) });
          toast("created " + name);
          renderBrowse(kind, emptyMsg, createLabel);
        } catch (e) {
          toast(e.message);
        }
      };
      if (!cols.length) {
        content.innerHTML = `<div class="empty">${emptyMsg}</div>`;
        return;
      }
      let active = cols[0].id;
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
      </div>`;
      content.innerHTML = tabs + '<div id="browse-list"></div>';
      const listEl = $("#browse-list");

      async function loadCol(id) {
        listEl.innerHTML = '<div class="empty">loading</div>';
        const res = await api("/collections/" + encodeURIComponent(id) + "/objects");
        const objs = res.objects || [];
        if (!objs.length) {
          listEl.innerHTML = '<div class="empty">empty</div>';
          return;
        }
        listEl.innerHTML = `<div class="block"><ul class="list">${objs
          .map((o) => itemRow(kind, o))
          .join("")}</ul></div>`;
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
              loadCol(active);
            } catch (e) {
              toast(e.message);
            }
          });
        });
      }

      function bindColActions() {
        $("#rename-col").onclick = async () => {
          const cur = cols.find((c) => c.id === active);
          const name = prompt("new name:", cur?.name || "");
          if (!name) return;
          try {
            await api("/collections/" + encodeURIComponent(active) + "/rename", {
              method: "POST",
              body: JSON.stringify({ name }),
            });
            toast("renamed");
            renderBrowse(kind, emptyMsg, createLabel);
          } catch (e) {
            toast(e.message);
          }
        };
        $("#delete-col").onclick = async () => {
          const cur = cols.find((c) => c.id === active);
          if (!confirm(`delete collection "${cur?.name}" and all items?`)) return;
          try {
            await api("/collections/" + encodeURIComponent(active), { method: "DELETE" });
            toast("deleted collection");
            renderBrowse(kind, emptyMsg, createLabel);
          } catch (e) {
            toast(e.message);
          }
        };
      }

      content.querySelectorAll(".tab").forEach((btn) => {
        btn.addEventListener("click", () => {
          active = btn.dataset.id;
          content.querySelectorAll(".tab").forEach((c) => c.classList.toggle("active", c.dataset.id === active));
          loadCol(active);
        });
      });
      bindColActions();
      await loadCol(active);
    } catch (e) {
      content.innerHTML = `<div class="empty">${esc(e.message)}</div>`;
    }
  }

  function itemRow(kind, o) {
    let title = o.summary || o.display_name || o.name || o.id;
    let sub = "";
    if (kind === "calendar") sub = fmtDate(o.start_ms);
    else if (kind === "addressbook") sub = o.uid || "";
    else if (kind === "files") sub = (o.mime || "file") + " · " + fmtSize(o.size);
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
      if (kind === "calendar") renderCalendar();
      else if (kind === "addressbook") renderContacts();
      else renderFiles();
    };
  }

  function renderCalendar() {
    return renderBrowse("calendar", "no calendars — create one", "new calendar");
  }
  function renderContacts() {
    return renderBrowse("addressbook", "no address books — create one", "new address book");
  }
  function renderFiles() {
    return renderBrowse("files", "no file collections — create one", "new folder");
  }

  let pairPoll = null;

  async function renderOps() {
    if (pairPoll) {
      clearInterval(pairPoll);
      pairPoll = null;
    }
    content.innerHTML = `
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
          <pre class="detail" id="verify-out">// not run</pre>
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
      <p class="hint">backup / restore stay on the CLI (passphrase files).</p>`;

    $("#pair-start").onclick = async () => {
      try {
        const r = await api("/pair", {
          method: "POST",
          body: JSON.stringify({ role: $("#pair-role").value }),
        });
        const out = $("#pair-out");
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
            const st = await api("/pair/" + encodeURIComponent(r.id));
            const el = $("#pair-status");
            if (!el) {
              clearInterval(pairPoll);
              return;
            }
            if (st.completed) {
              el.textContent = "paired " + (st.device_id || "");
              clearInterval(pairPoll);
              pairPoll = null;
              toast("device paired");
            } else if (st.expired) {
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
    };

    const runVerify = async (repair) => {
      $("#verify-out").textContent = "running…";
      try {
        const r = await api("/verify", { method: "POST", body: JSON.stringify({ repair }) });
        if (r.ok) {
          $("#verify-out").textContent = "ok" + (repair ? ` (orphans=${r.orphans_removed || 0} refs=${r.refcounts_repaired || 0})` : "");
        } else {
          const lines = (r.issues || []).map((i) => (i.kind || "") + ": " + (i.message || "") + (i.ref ? " (" + i.ref + ")" : ""));
          $("#verify-out").textContent = lines.join("\n") || "issues found";
        }
      } catch (e) {
        $("#verify-out").textContent = e.message;
      }
    };
    $("#verify-run").onclick = () => runVerify(false);
    $("#verify-repair").onclick = () => {
      if (!confirm("repair orphan blobs and refcounts?")) return;
      runVerify(true);
    };

    const runGC = async (apply) => {
      if (apply && !confirm("delete unreferenced blobs and compact tombstones?")) return;
      $("#gc-out").textContent = "running…";
      try {
        const r = await api("/gc", { method: "POST", body: JSON.stringify({ apply }) });
        $("#gc-out").textContent = JSON.stringify(r, null, 2);
      } catch (e) {
        $("#gc-out").textContent = e.message;
      }
    };
    $("#gc-dry").onclick = () => runGC(false);
    $("#gc-apply").onclick = () => runGC(true);
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
              <p class="hint">restart nubilo server after changing listen/tls/pairing rates</p>
            </div>
          </div>
        </div>
        <div class="section">
          <p class="section-title">devices</p>
          <div class="block">
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
            }),
          });
          toast(r.restart || "saved");
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
