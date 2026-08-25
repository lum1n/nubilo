(function () {
  "use strict";

  const $ = (sel, el = document) => el.querySelector(sel);
  const content = $("#content");
  const pageTitle = $("#page-title");
  const topbarActions = $("#topbar-actions");
  const toastEl = $("#toast");
  const statusText = $("#status-text");

  const routes = {
    home: { title: "overview", render: renderHome },
    calendars: { title: "calendars", render: renderCalendars },
    reminders: { title: "reminders", render: renderReminders },
    contacts: { title: "contacts", render: renderContacts },
    photos: { title: "photos", render: renderPhotos },
    files: { title: "files", render: renderFiles },
    setup: { title: "setup", render: renderSetup },
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
      statusText.textContent = "unauthorized — reopen from CLI session URL";
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

  function esc(s) {
    const d = document.createElement("div");
    d.textContent = s == null ? "" : String(s);
    return d.innerHTML;
  }

  function kvRow(label, value) {
    return `<tr><td>${esc(label)}</td><td>${value}</td></tr>`;
  }

  function msToDateInput(ms) {
    if (!ms) return "";
    const d = new Date(ms);
    const y = d.getFullYear();
    const m = String(d.getMonth() + 1).padStart(2, "0");
    const day = String(d.getDate()).padStart(2, "0");
    return `${y}-${m}-${day}`;
  }

  function dateInputToMS(v, endOfDay) {
    if (!v) return 0;
    const d = new Date(v + (endOfDay ? "T23:59:59" : "T00:00:00"));
    const t = d.getTime();
    return Number.isFinite(t) ? t : 0;
  }

  function route() {
    const hash = (location.hash || "#/").replace(/^#\/?/, "") || "home";
    const name = hash.split("/")[0] || "home";
    const r = routes[name] || routes.home;
    pageTitle.textContent = r.title;
    topbarActions.innerHTML = "";
    document.querySelectorAll(".nav-link").forEach((a) => {
      a.classList.toggle("active", a.dataset.route === name);
    });
    return r.render();
  }

  async function renderHome() {
    const o = await api("/overview");
    const p = o.pairing || {};
    content.innerHTML = `
      <table class="kv">
        ${kvRow("version", esc(o.version))}
        ${kvRow("data dir", esc(o.data_dir))}
        ${kvRow("paired", p.paired ? "yes" : "no")}
        ${kvRow("server", p.paired ? esc(p.server) : "—")}
        ${kvRow("device", p.paired ? esc(p.name || p.device_id) : "—")}
        ${kvRow("photos access", esc(o.photos_auth))}
        ${kvRow("interval", esc(o.interval_seconds) + "s")}
        ${kvRow("window", esc(o.window_days) + "d")}
        ${kvRow("calendars", esc(o.counts.calendars))}
        ${kvRow("reminders", esc(o.counts.reminders))}
        ${kvRow("contacts", o.sync_contacts ? "on" : "off")}
        ${kvRow("photos", o.photos_enabled ? "on (" + o.counts.albums + " albums)" : "off")}
        ${kvRow("files", o.files_enabled ? "on (" + o.counts.folders + " folders)" : "off")}
      </table>
      <p class="muted">Selection changes write agent.json. A running agent picks them up on the next sync tick (or restart).</p>
    `;
    statusText.textContent = p.paired ? "paired" : "unpaired";
  }

  function checklistTable(rows, idKey, labelFn, onToggle) {
    const q = `<input type="search" id="filter" placeholder="filter…" style="max-width:16rem;margin-bottom:0.75rem">`;
    const body = rows
      .map((row) => {
        const id = row[idKey];
        const checked = row.selected ? "checked" : "";
        return `<tr data-q="${esc((labelFn(row) + " " + id).toLowerCase())}">
          <td><input type="checkbox" data-id="${esc(id)}" ${checked}></td>
          <td>${labelFn(row)}</td>
        </tr>`;
      })
      .join("");
    content.innerHTML = `${q}<div class="block"><table class="data"><thead><tr><th></th><th>name</th></tr></thead><tbody>${body}</tbody></table></div>`;
    const filter = $("#filter");
    filter.addEventListener("input", () => {
      const v = filter.value.trim().toLowerCase();
      content.querySelectorAll("tbody tr").forEach((tr) => {
        tr.style.display = !v || tr.dataset.q.includes(v) ? "" : "none";
      });
    });
    content.querySelectorAll("input[type=checkbox][data-id]").forEach((cb) => {
      cb.addEventListener("change", async () => {
        try {
          await onToggle(cb.dataset.id, cb.checked);
          toast(cb.checked ? "selected" : "unselected");
        } catch (e) {
          cb.checked = !cb.checked;
          toast(String(e.message || e));
        }
      });
    });
  }

  async function renderCalendars() {
    const data = await api("/calendars");
    checklistTable(data.calendars || [], "id", (r) => esc(r.title || r.id), async (id, on) => {
      await api(on ? "/calendars/select" : "/calendars/unselect", {
        method: "POST",
        body: JSON.stringify({ id }),
      });
    });
  }

  async function renderReminders() {
    const data = await api("/reminders");
    checklistTable(data.reminders || [], "id", (r) => esc(r.title || r.id), async (id, on) => {
      await api(on ? "/reminders/select" : "/reminders/unselect", {
        method: "POST",
        body: JSON.stringify({ id }),
      });
    });
  }

  async function renderContacts() {
    const sel = await api("/selection");
    content.innerHTML = `
      <div class="section">
        <h3 class="section-title">contacts</h3>
        <label><input type="checkbox" id="sync-contacts" ${sel.sync_contacts ? "checked" : ""}> sync Address Book contacts</label>
        <p style="margin-top:1rem"><button type="button" class="btn primary" id="save-contacts" style="width:auto">save</button></p>
      </div>`;
    $("#save-contacts").onclick = async () => {
      sel.sync_contacts = $("#sync-contacts").checked;
      await api("/selection", { method: "PUT", body: JSON.stringify(sel) });
      toast("saved");
    };
  }

  async function renderPhotos() {
    const [sel, albums] = await Promise.all([api("/selection"), api("/albums")]);
    const p = sel.photos || {};
    const src = p.source || "all";
    const albumRows = (albums.albums || [])
      .map((a) => {
        const kind = a.kind || "user";
        const checked = a.selected ? "checked" : "";
        return `<tr data-q="${esc((a.title + " " + kind + " " + a.id).toLowerCase())}">
          <td><input type="checkbox" data-id="${esc(a.id)}" ${checked}></td>
          <td>[${esc(kind)}]</td>
          <td>${esc(a.title)}</td>
          <td>${esc(a.count)}</td>
        </tr>`;
      })
      .join("");
    content.innerHTML = `
      <div class="section">
        <h3 class="section-title">sync</h3>
        <label><input type="checkbox" id="photos-on" ${p.enabled ? "checked" : ""}> enable photos sync</label>
        <div style="margin-top:0.75rem">
          <label><input type="radio" name="src" value="all" ${src === "all" ? "checked" : ""}> all library</label>
          <label style="margin-left:1rem"><input type="radio" name="src" value="albums" ${src === "albums" ? "checked" : ""}> selected albums / people</label>
          <label style="margin-left:1rem"><input type="radio" name="src" value="dates" ${src === "dates" ? "checked" : ""}> date range</label>
        </div>
        <div id="dates-row" style="margin-top:0.75rem;${src === "dates" ? "" : "display:none"}">
          <label>after <input type="date" id="after" value="${esc(msToDateInput(p.after_ms))}" style="width:auto;display:inline-block;margin:0 0.5rem"></label>
          <label>before <input type="date" id="before" value="${esc(msToDateInput(p.before_ms))}" style="width:auto;display:inline-block;margin:0 0.5rem"></label>
        </div>
        <p style="margin-top:0.75rem">
          <button type="button" class="btn primary" id="save-photos" style="width:auto">save</button>
          <button type="button" class="btn" id="auth-photos" style="width:auto;margin-left:0.5rem">authorize photos</button>
        </p>
        <p class="muted">photos access: ${esc(albums.photos_auth)} · library assets: ${esc(albums.library_count)}</p>
        <p class="muted">People &amp; Pets use kind=person|pet and ids person:… — select those for full sets.</p>
      </div>
      <div class="section">
        <h3 class="section-title">albums / people</h3>
        <input type="search" id="filter" placeholder="filter…" style="max-width:16rem;margin-bottom:0.75rem">
        <div class="block"><table class="data">
          <thead><tr><th></th><th>kind</th><th>title</th><th>count</th></tr></thead>
          <tbody>${albumRows || '<tr><td colspan="4">no albums (authorize Photos)</td></tr>'}</tbody>
        </table></div>
      </div>`;
    const syncDates = () => {
      const v = content.querySelector('input[name=src]:checked')?.value;
      $("#dates-row").style.display = v === "dates" ? "" : "none";
    };
    content.querySelectorAll('input[name=src]').forEach((el) => el.addEventListener("change", syncDates));
    $("#filter")?.addEventListener("input", () => {
      const v = $("#filter").value.trim().toLowerCase();
      content.querySelectorAll("tbody tr[data-q]").forEach((tr) => {
        tr.style.display = !v || tr.dataset.q.includes(v) ? "" : "none";
      });
    });
    content.querySelectorAll("input[type=checkbox][data-id]").forEach((cb) => {
      cb.addEventListener("change", async () => {
        try {
          await api(cb.checked ? "/albums/select" : "/albums/unselect", {
            method: "POST",
            body: JSON.stringify({ id: cb.dataset.id }),
          });
          toast(cb.checked ? "selected" : "unselected");
        } catch (e) {
          cb.checked = !cb.checked;
          toast(String(e.message || e));
        }
      });
    });
    $("#save-photos").onclick = async () => {
      const next = await api("/selection");
      next.photos = next.photos || {};
      next.photos.enabled = $("#photos-on").checked;
      next.photos.source = content.querySelector('input[name=src]:checked')?.value || "all";
      next.photos.after_ms = dateInputToMS($("#after")?.value, false);
      next.photos.before_ms = dateInputToMS($("#before")?.value, true);
      await api("/selection", { method: "PUT", body: JSON.stringify(next) });
      toast("saved");
    };
    $("#auth-photos").onclick = async () => {
      try {
        const r = await api("/photos/authorize", { method: "POST", body: "{}" });
        toast("photos access: " + r.status);
        renderPhotos();
      } catch (e) {
        toast(String(e.message || e));
      }
    };
  }

  async function renderFiles() {
    const sel = await api("/selection");
    const f = sel.files || { folders: [] };
    const rows = (f.folders || [])
      .map(
        (x) => `<tr>
          <td>${esc(x.name)}</td>
          <td>${esc(x.path)}</td>
          <td><button type="button" class="btn sm danger" data-path="${esc(x.path)}">remove</button></td>
        </tr>`
      )
      .join("");
    content.innerHTML = `
      <div class="section">
        <h3 class="section-title">sync</h3>
        <label><input type="checkbox" id="files-on" ${f.enabled ? "checked" : ""}> enable files sync</label>
        <p style="margin-top:0.75rem"><button type="button" class="btn primary" id="save-files" style="width:auto">save</button></p>
      </div>
      <div class="section">
        <h3 class="section-title">folders</h3>
        <div class="block"><table class="data">
          <thead><tr><th>name</th><th>path</th><th></th></tr></thead>
          <tbody>${rows || "<tr><td colspan=3>none</td></tr>"}</tbody>
        </table></div>
        <div style="margin-top:0.75rem;display:flex;gap:0.5rem;flex-wrap:wrap;align-items:center">
          <input type="text" id="folder-path" placeholder="/Users/you/Documents/Nubilo" style="flex:1;min-width:12rem;margin:0">
          <input type="text" id="folder-name" placeholder="name (optional)" style="width:10rem;margin:0">
          <button type="button" class="btn" id="add-folder">add</button>
        </div>
      </div>`;
    $("#save-files").onclick = async () => {
      const next = await api("/selection");
      next.files = next.files || {};
      next.files.enabled = $("#files-on").checked;
      await api("/selection", { method: "PUT", body: JSON.stringify(next) });
      toast("saved");
    };
    $("#add-folder").onclick = async () => {
      try {
        await api("/files/add", {
          method: "POST",
          body: JSON.stringify({
            path: $("#folder-path").value.trim(),
            name: $("#folder-name").value.trim(),
          }),
        });
        toast("added");
        renderFiles();
      } catch (e) {
        toast(String(e.message || e));
      }
    };
    content.querySelectorAll("button[data-path]").forEach((btn) => {
      btn.onclick = async () => {
        try {
          await api("/files/remove", {
            method: "POST",
            body: JSON.stringify({ path: btn.dataset.path }),
          });
          toast("removed");
          renderFiles();
        } catch (e) {
          toast(String(e.message || e));
        }
      };
    });
  }

  async function renderSetup() {
    const [o, sel] = await Promise.all([api("/overview"), api("/selection")]);
    const p = o.pairing || {};
    content.innerHTML = `
      <div class="section">
        <h3 class="section-title">sync timing</h3>
        <label>interval (seconds)<input type="number" id="interval" value="${esc(sel.interval_seconds)}" min="30"></label>
        <label>window (days)<input type="number" id="window" value="${esc(sel.window_days)}" min="1"></label>
        <p><button type="button" class="btn primary" id="save-setup" style="width:auto">save</button></p>
        <p class="muted">data dir: ${esc(o.data_dir)}</p>
      </div>
      <div class="section">
        <h3 class="section-title">pairing</h3>
        ${
          p.paired
            ? `<table class="kv">${kvRow("server", esc(p.server))}${kvRow("device", esc(p.name))} ${kvRow("id", esc(p.device_id))}</table>`
            : `<p class="muted">On the server: <code>nubilo pair --role agent</code>. Then enter the code here.</p>
               <label>server URL<input id="pair-server" placeholder="https://home.example:8443"></label>
               <label>code<input id="pair-code" placeholder="XXXXX-XXXXX" autocomplete="off"></label>
               <label>device name<input id="pair-name" placeholder="this Mac"></label>
               <label><input type="checkbox" id="pair-insecure"> insecure TLS (self-signed)</label>
               <p><button type="button" class="btn primary" id="pair-btn" style="width:auto">pair</button></p>`
        }
      </div>`;
    $("#save-setup").onclick = async () => {
      const next = await api("/selection");
      next.interval_seconds = parseInt($("#interval").value, 10) || 120;
      next.window_days = parseInt($("#window").value, 10) || 730;
      await api("/selection", { method: "PUT", body: JSON.stringify(next) });
      toast("saved");
    };
    const pairBtn = $("#pair-btn");
    if (pairBtn) {
      pairBtn.onclick = async () => {
        try {
          const r = await api("/pair", {
            method: "POST",
            body: JSON.stringify({
              server: $("#pair-server").value.trim(),
              code: $("#pair-code").value.trim(),
              name: $("#pair-name").value.trim(),
              insecure: $("#pair-insecure").checked,
            }),
          });
          toast("paired as " + r.device_id);
          renderSetup();
        } catch (e) {
          toast(String(e.message || e));
        }
      };
    }
  }

  window.addEventListener("hashchange", () => {
    route().catch((e) => toast(String(e.message || e)));
  });
  api("/session")
    .then(() => route())
    .catch(() => {
      statusText.textContent = "unauthorized";
      content.innerHTML = `<p class="muted">Open the session URL printed by <code>nubilo agent ui</code>.</p>`;
    });
})();
