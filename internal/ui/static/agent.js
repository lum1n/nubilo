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
    const [o, health] = await Promise.all([api("/overview"), api("/health")]);
    const p = o.pairing || {};
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
      ? `<p class="hint">health: ok</p>`
      : `<p class="hint">health: needs attention (${health.fail || 0} fail, ${health.warn || 0} warn)</p>`;
    content.innerHTML = `
      <div class="section">
        <p class="section-title">health</p>
        ${banner}
        <div class="block"><table class="data health-table"><tbody>${checks}</tbody></table></div>
      </div>
      <table class="kv">
        ${kvRow("version", esc(o.version))}
        ${kvRow("data dir", esc(o.data_dir))}
        ${kvRow("paired", p.paired ? "yes" : "no")}
        ${kvRow("server", p.paired ? esc(p.server) : "—")}
        ${kvRow("device", p.paired ? esc(p.name || p.device_id) : "—")}
        ${kvRow("interval", esc(o.interval_seconds) + "s")}
        ${kvRow("files", o.files_enabled ? "on (" + o.counts.folders + " folders)" : "off")}
      </table>
      <p class="muted">CLI: <code>nubilo agent setup</code> · <code>nubilo doctor --agent</code></p>
    `;
    statusText.textContent = p.paired ? "paired" : "unpaired";
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
        <p style="margin-top:0.5rem"><button type="button" class="btn primary" id="save-files" style="width:auto">save</button></p>
      </div>
      <div class="section">
        <h3 class="section-title">folders</h3>
        <div class="block"><table class="data">
          <thead><tr><th>name</th><th>path</th><th></th></tr></thead>
          <tbody>${rows || "<tr><td colspan=3>none</td></tr>"}</tbody>
        </table></div>
        <div class="choice-row" style="margin-top:0.5rem">
          <input type="text" id="folder-path" placeholder="/Users/you/Documents/Nubilo" style="flex:1;min-width:12rem;max-width:28rem;margin:0">
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
