// Files: the same folders the Files app and WebDAV show. Folders are
// collections of kind "files"; files are objects in them. Uploads write a
// blob, then push a "file" object with {name, mime} metadata exactly like a
// WebDAV PUT does.

import { h, clear, icon, api, send, storageChanged, ulid, sha256Hex, formatBytes, formatRelative, validName } from "./lib.js";
import { notice, promptName, confirmAction, trackUpload, dropZone, pickFiles, runQueue } from "./ui.js";

const KIND = "files";
const collator = new Intl.Collator(undefined, { numeric: true, sensitivity: "base" });
const canWrite = (access) => access === "owner" || access === "rw";

async function loadFolders(signal) {
  const { collections = [] } = await api("collections", { signal });
  const folders = collections.filter((c) => c.kind === KIND && !c.deleted_at);
  const byID = new Map(folders.map((c) => [c.id, c]));
  // Subfolders inherit access from their root when a listing leaves it out.
  for (const c of folders) {
    if (c.access) continue;
    let r = c;
    while (r && !r.access && r.parent_id) r = byID.get(r.parent_id);
    c.access = r?.access || "ro";
  }
  return { folders, byID };
}

function fileName(o) {
  return o.metadata?.name || o.id;
}

function fileIcon(name) {
  const ext = (name.split(".").pop() || "").toLowerCase();
  const type = /^(jpe?g|png|gif|heic|webp|tiff?|bmp|svg|dng|raw)$/.test(ext) ? "img"
    : /^(mov|mp4|m4v|avi|mkv|webm)$/.test(ext) ? "vid"
    : /^(mp3|m4a|wav|aac|flac|ogg)$/.test(ext) ? "aud"
    : /^(pdf)$/.test(ext) ? "pdf"
    : /^(zip|gz|tgz|bz2|xz|7z|rar|tar)$/.test(ext) ? "zip"
    : /^(docx?|pages|odt|rtf|txt|md)$/.test(ext) ? "doc"
    : /^(xlsx?|numbers|ods|csv)$/.test(ext) ? "sheet"
    : /^(pptx?|key|odp)$/.test(ext) ? "slide"
    : "";
  return h("span", { class: ["file-icon", type && `file-icon-${type}`], "aria-hidden": "true" },
    icon("file"), ext && ext.length <= 4 ? h("span", { class: "file-ext" }, ext) : null);
}

export function filesView(shell, nav, folderID) {
  const ctl = new AbortController();
  let state = { folders: [], byID: new Map(), objects: [] };
  const content = h("div", { class: "files" }, h("p", { class: "muted loading-line" }, h("span", { class: "spinner", "aria-hidden": "true" }), "Loading…"));
  shell.content.append(content);
  shell.setActions([]);
  shell.setHeader({ title: "Files", subtitle: "" });

  const current = () => (folderID ? state.byID.get(folderID) : null);
  const writable = () => (folderID ? canWrite(current()?.access) : true);
  const children = (parent) => state.folders.filter((c) => (c.parent_id || "") === parent).sort((a, b) => collator.compare(a.name, b.name));
  const link = (path, label, cls = "crumb") => h("a", { class: cls, href: path, onclick: (e) => { e.preventDefault(); nav(path); } }, label);

  // Folders the user may rename or delete: own top-level folders, and
  // subfolders of anything writable.
  const canAdmin = (c) => (c.parent_id ? canWrite(c.access) : c.access === "owner");

  async function refresh() {
    const s = await loadFolders(ctl.signal);
    let objects = [];
    if (folderID) {
      if (!s.byID.has(folderID)) throw Object.assign(new Error("This folder no longer exists, or it's no longer shared with you."), { gone: true });
      let after = "";
      for (;;) {
        const res = await api("objects", { method: "POST", json: { collection_id: folderID, after_id: after, limit: 1000 }, signal: ctl.signal });
        objects.push(...(res.objects || []));
        if (!res.next_after_id) break;
        after = res.next_after_id;
      }
      objects = objects.filter((o) => !o.deleted_at && o.kind === "file")
        .sort((a, b) => collator.compare(fileName(a), fileName(b)));
    }
    state = { ...s, objects };
    render();
  }

  function header() {
    const c = current();
    if (!c) {
      shell.setHeader({ title: "Files", subtitle: "" });
    } else {
      const crumbs = [link("/web/files", "Files")];
      const chain = [];
      for (let p = state.byID.get(c.parent_id); p; p = state.byID.get(p.parent_id)) chain.unshift(p);
      for (const p of chain) crumbs.push(icon("chevron", "icon icon-xs crumb-sep"), link(`/web/files/${p.id}`, p.name));
      const shared = c.access !== "owner" ? (c.access === "ro" ? "View only" : "Shared with you") : "";
      shell.setHeader({ title: c.name, subtitle: shared, crumbs });
    }
    const actions = [];
    if (writable()) {
      actions.push(h("button", { type: "button", class: "btn btn-quiet", onclick: newFolder }, icon("folderPlus"), h("span", { class: "btn-label" }, "New folder")));
      if (folderID) actions.push(h("button", { type: "button", class: "btn btn-primary", onclick: async () => upload(await pickFiles()) }, icon("upload"), h("span", { class: "btn-label" }, "Upload")));
    }
    shell.setActions(actions);
  }

  function folderRow(c) {
    const actions = [];
    if (canAdmin(c)) {
      actions.push(h("button", { type: "button", class: "icon-btn", "aria-label": `Rename ${c.name}`, title: "Rename", onclick: () => renameFolder(c) }, icon("pencil")));
      actions.push(h("button", { type: "button", class: "icon-btn icon-btn-danger", "aria-label": `Delete ${c.name}`, title: "Delete", onclick: () => deleteFolder(c) }, icon("trash")));
    }
    return h("tr", { class: "row row-folder" },
      h("td", { class: "cell-name" }, link(`/web/files/${c.id}`, [h("span", { class: "folder-icon", "aria-hidden": "true" }, icon("folder")), h("span", { class: "name-text" }, c.name),
        !folderID && c.access === "ro" ? h("span", { class: "badge" }, "View only") : null,
        !folderID && c.access === "rw" ? h("span", { class: "badge badge-rw" }, "Can edit") : null], "row-link")),
      h("td", { class: "cell-date" }, formatRelative(c.updated_at)),
      h("td", { class: "cell-size" }, "—"),
      h("td", { class: "cell-actions" }, actions));
  }

  function fileRow(o) {
    const name = fileName(o);
    const href = `/web/api/files/${encodeURIComponent(o.id)}`;
    const actions = [h("a", { class: "icon-btn", href, download: name, "aria-label": `Download ${name}`, title: "Download" }, icon("download"))];
    if (writable()) {
      actions.push(h("button", { type: "button", class: "icon-btn", "aria-label": `Rename ${name}`, title: "Rename", onclick: () => renameFile(o) }, icon("pencil")));
      actions.push(h("button", { type: "button", class: "icon-btn icon-btn-danger", "aria-label": `Delete ${name}`, title: "Delete", onclick: () => deleteFile(o) }, icon("trash")));
    }
    return h("tr", { class: "row" },
      h("td", { class: "cell-name" }, h("a", { class: "row-link", href, download: name, title: `Download ${name}` }, fileIcon(name), h("span", { class: "name-text" }, name))),
      h("td", { class: "cell-date" }, formatRelative(o.updated_at)),
      h("td", { class: "cell-size" }, formatBytes(o.size)),
      h("td", { class: "cell-actions" }, actions));
  }

  function table(rows, caption) {
    return h("table", { class: "file-table" },
      h("caption", { class: "visually-hidden" }, caption),
      h("thead", {}, h("tr", {},
        h("th", { scope: "col", class: "cell-name" }, "Name"),
        h("th", { scope: "col", class: "cell-date" }, "Modified"),
        h("th", { scope: "col", class: "cell-size" }, "Size"),
        h("th", { scope: "col", class: "cell-actions" }, h("span", { class: "visually-hidden" }, "Actions")))),
      h("tbody", {}, rows));
  }

  function render() {
    header();
    clear(content);
    if (!folderID) {
      const roots = state.folders.filter((c) => !c.parent_id || !state.byID.has(c.parent_id));
      const own = roots.filter((c) => c.access === "owner").sort((a, b) => collator.compare(a.name, b.name));
      const shared = roots.filter((c) => c.access !== "owner");
      if (!roots.length) {
        content.append(h("div", { class: "empty" },
          h("div", { class: "empty-art", "aria-hidden": "true" }, icon("files", "icon icon-xl")),
          h("h2", { class: "empty-title" }, "No folders yet"),
          h("p", { class: "muted" }, "Folders you keep in Nubilo, from the Files app or WebDAV, show up here."),
          h("button", { type: "button", class: "btn btn-primary", onclick: newFolder }, icon("folderPlus"), "New folder")));
        return;
      }
      if (own.length) content.append(h("h2", { class: "section-title" }, "My folders"), table(own.map(folderRow), "My folders"));
      const owners = new Map();
      for (const c of shared) {
        const k = c.owner || "Someone";
        if (!owners.has(k)) owners.set(k, []);
        owners.get(k).push(c);
      }
      for (const [owner, list] of [...owners].sort((a, b) => collator.compare(a[0], b[0]))) {
        content.append(h("h2", { class: "section-title" }, icon("people", "icon icon-sm"), `Shared by ${owner}`),
          table(list.sort((a, b) => collator.compare(a.name, b.name)).map(folderRow), `Shared by ${owner}`));
      }
      return;
    }
    const subs = children(folderID);
    if (!subs.length && !state.objects.length) {
      content.append(h("div", { class: "empty" },
        h("div", { class: "empty-art", "aria-hidden": "true" }, icon("folder", "icon icon-xl")),
        h("h2", { class: "empty-title" }, "This folder is empty"),
        writable() ? h("p", { class: "muted" }, "Drop files here, or use Upload.") : h("p", { class: "muted" }, "Nothing has been added yet.")));
      return;
    }
    content.append(table([...subs.map(folderRow), ...state.objects.map(fileRow)], current()?.name || "Folder"));
    const total = state.objects.reduce((n, o) => n + (o.size || 0), 0);
    const parts = [];
    if (subs.length) parts.push(`${subs.length} ${subs.length === 1 ? "folder" : "folders"}`);
    if (state.objects.length) parts.push(`${state.objects.length} ${state.objects.length === 1 ? "file" : "files"}, ${formatBytes(total)}`);
    content.append(h("p", { class: "files-footer muted" }, parts.join(" · ")));
  }

  // Names in a folder are shared between subfolders and files.
  function nameTaken(name, except) {
    const lower = name.toLowerCase();
    const parent = folderID || "";
    if (children(parent).some((c) => c.id !== except && c.name.toLowerCase() === lower)) return "A folder with that name is already here.";
    if (folderID && state.objects.some((o) => o.id !== except && fileName(o).toLowerCase() === lower)) return "A file with that name is already here.";
    return "";
  }

  const fail = (e) => { if (e.name !== "AbortError") notice(shell.notice, e.message); };

  async function newFolder() {
    await promptName({
      title: "New folder", label: "Name", value: "", action: "Create",
      validate: (n) => validName(n) || nameTaken(n),
      submit: (name) => api("collection", { method: "POST", json: { kind: KIND, name, parent_id: folderID || "" } }),
    });
    refresh().catch(fail);
  }

  async function renameFolder(c) {
    const done = await promptName({
      title: "Rename folder", label: "Name", value: c.name, action: "Rename",
      validate: (n) => validName(n) || (n === c.name ? "" : nameTaken(n, c.id)),
      submit: (name) => (name === c.name ? null : api("collection/update", { method: "POST", json: { id: c.id, name } })),
    });
    if (done) refresh().catch(fail);
  }

  async function deleteFolder(c) {
    const done = await confirmAction({
      title: `Delete “${c.name}”?`,
      message: c.access === "owner" && !c.parent_id
        ? "The folder and everything in it is deleted on all your devices, and for anyone it's shared with."
        : "The folder and everything in it is deleted on all devices.",
      action: "Delete folder",
      run: () => api("collection/delete", { method: "POST", json: { id: c.id } }),
    });
    if (done) { storageChanged(); refresh().catch(fail); }
  }

  async function push(change) {
    const { results = [] } = await api("push", { method: "POST", json: { idempotency_key: ulid(), changes: [change] } });
    const r = results[0] || {};
    if (r.status === "ok") return r;
    if (r.status === "conflict") throw new Error("This item was changed somewhere else. Refresh and try again.");
    if (/quota/i.test(r.error || "")) throw new Error("Your storage is full. Free up space or ask the admin for a bigger quota.");
    if (/unauthorized/i.test(r.error || "")) throw new Error("You don't have permission to change this folder.");
    if (/read.?only|mirror/i.test(r.error || "")) throw new Error("This folder is read-only.");
    throw new Error(r.error ? `Couldn't save: ${r.error}.` : "Couldn't save the change.");
  }

  async function renameFile(o) {
    const old = fileName(o);
    const done = await promptName({
      title: "Rename file", label: "Name", value: old, action: "Rename",
      validate: (n) => validName(n) || (n === old ? "" : nameTaken(n, o.id)),
      submit: (name) => (name === old ? null : push({
        object_id: o.id, collection_id: o.collection_id, kind: o.kind || "file", op: "update", base_revision: o.revision,
        content_hash: o.content_hash, blob_id: o.blob_id, size: o.size,
        metadata: { ...(o.metadata || {}), name },
      })),
    });
    if (done) refresh().catch(fail);
  }

  async function deleteFile(o) {
    const done = await confirmAction({
      title: `Delete “${fileName(o)}”?`,
      message: "The file is deleted on all devices.",
      action: "Delete",
      run: () => push({ object_id: o.id, collection_id: o.collection_id, op: "delete", base_revision: o.revision }),
    });
    if (done) { storageChanged(); refresh().catch(fail); }
  }

  async function upload(files) {
    if (!files.length || !folderID || !writable()) return;
    let changed = false;
    await runQueue(files, 2, async (file) => {
      const t = trackUpload(file.name);
      try {
        const name = file.name.trim();
        const bad = validName(name);
        if (bad) throw new Error(bad);
        if (children(folderID).some((c) => c.name.toLowerCase() === name.toLowerCase())) throw new Error("A folder with that name is already here.");
        t.stage("Preparing…");
        const hash = await sha256Hex(file);
        await send("PUT", `blob/${hash}`, file, { headers: { "Content-Type": "application/octet-stream" }, onProgress: (f) => t.progress(f * 0.97) });
        const existing = state.objects.find((o) => fileName(o) === name);
        const meta = { name, mime: file.type || "application/octet-stream" };
        const change = { collection_id: folderID, kind: "file", content_hash: hash, blob_id: hash, size: file.size, metadata: meta };
        if (existing) {
          // Same as WebDAV: uploading a name that exists replaces the file.
          await push({ ...change, object_id: existing.id, op: "update", base_revision: existing.revision });
          t.ok();
          t.stage("Replaced");
        } else {
          await push({ ...change, object_id: ulid(), op: "create" });
          t.ok();
        }
        changed = true;
      } catch (e) {
        t.fail(e.message);
        if (/storage is full/i.test(e.message)) notice(shell.notice, "Your storage is full. Some uploads didn't finish.");
      }
    });
    if (changed) {
      storageChanged();
      refresh().catch(fail);
    }
  }

  dropZone(shell.main, {
    label: "Drop to upload to this folder",
    onFiles: upload,
    enabled: () => !!folderID && writable(),
  });

  refresh().catch((e) => {
    if (e.name === "AbortError") return;
    clear(content);
    if (e.gone) {
      content.append(h("div", { class: "empty" },
        h("div", { class: "empty-art", "aria-hidden": "true" }, icon("folder", "icon icon-xl")),
        h("h2", { class: "empty-title" }, "Folder not found"),
        h("p", { class: "muted" }, e.message),
        link("/web/files", "Back to Files", "btn btn-quiet")));
      return;
    }
    fail(e);
  });

  return () => ctl.abort();
}
