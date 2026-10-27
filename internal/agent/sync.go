package agent

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	ncrypto "nubilo/internal/crypto"
	"nubilo/internal/dav"
	"nubilo/internal/ids"
	"nubilo/internal/protocol"
	"nubilo/internal/syncengine"
)

const (
	kindFile  = "file"
	kindFiles = "files"
)

type Agent struct {
	Client  *protocol.Client
	Map     *Map
	Sel     Selection
	SelPath string // agent.json; reloaded at the start of each SyncOnce
	Files   FileSource
	Log     *slog.Logger

	routes map[string]route

	// Collection list cache, valid while the server's collections_rev is unchanged.
	colRev      string
	colCache    []syncengine.Collection
	colCacheRev string
}

type route struct {
	localID  string
	kind     string
	rootPath string // selected folder absolute path (files)
	relDir   string // relative dir of this collection within root (files)
}

func (a *Agent) reloadSelection() error {
	if a.SelPath == "" {
		return nil
	}
	sel, err := LoadSelection(a.SelPath)
	if err != nil {
		return err
	}
	a.Sel = sel
	return nil
}

func (a *Agent) SyncOnce(ctx context.Context) error {
	if a.Log == nil {
		a.Log = slog.Default()
	}
	if err := a.reloadSelection(); err != nil {
		return err
	}
	cursor := a.Map.Cursor()
	hello, err := a.Client.Hello(cursor, false)
	if err != nil {
		return err
	}
	a.colRev = hello.CollectionsRev
	if err := a.bindRoutes(); err != nil {
		return err
	}
	if hello.NeedReconcile {
		if err := a.reconcile(ctx); err != nil {
			return err
		}
	}
	if err := a.pull(ctx, cursor); err != nil {
		return err
	}
	return a.pushLocal(ctx)
}

func (a *Agent) pull(ctx context.Context, since int64) error {
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		res, err := a.Client.Changes(since, 200, "")
		if err != nil {
			return err
		}
		if res.NeedReconcile {
			if err := a.reconcile(ctx); err != nil {
				return err
			}
			// The cursor is past the server's journal (a restored server).
			// Continue from its head; replaying older entries could write
			// restored, older files over newer local ones.
			a.Log.Warn("journal_reset", "cursor", since, "head", res.NextSeq)
			if err := a.Map.SetCursor(res.NextSeq); err != nil {
				return err
			}
			since = res.NextSeq
			continue
		}
		failed := false
		for i := range res.Changes {
			if err := a.applyChange(ctx, res.Changes[i]); err != nil {
				a.Log.Warn("apply_change", "err", err.Error(), "object", res.Changes[i].ObjectID)
				failed = true
			}
		}
		if failed {
			return errors.New("apply_change failed")
		}
		if res.NextSeq > since {
			if err := a.Client.Ack(res.NextSeq); err != nil {
				return err
			}
			if err := a.Map.SetCursor(res.NextSeq); err != nil {
				return err
			}
			since = res.NextSeq
		}
		if !res.HasMore {
			return nil
		}
	}
}

func (a *Agent) applyChange(ctx context.Context, ch syncengine.Change) error {
	rt, ok := a.routes[ch.CollectionID]
	if !ok {
		return nil
	}
	existing, mapErr := a.Map.ByObject(ch.ObjectID)
	if ch.Op == syncengine.OpDelete || ch.Deleted {
		if mapErr == nil {
			if rt.kind == kindFile && a.Files != nil {
				_ = a.Files.DeleteFile(existing.LocalID)
			}
			return a.Map.DeleteObject(ch.ObjectID)
		}
		return nil
	}
	if mapErr == nil && existing.Revision >= ch.Revision && existing.ContentHash == ch.ContentHash {
		return nil
	}
	if rt.kind != kindFile || a.Files == nil || rt.rootPath == "" {
		return nil
	}
	var payload []byte
	if ch.BlobID != "" {
		var err error
		payload, err = a.Client.GetBlob(ch.BlobID)
		if err != nil {
			return err
		}
	}
	fname := dav.ParseFileMeta(ch.Metadata).Name
	if skipFileName(fname) {
		return nil
	}
	abs := filepath.Join(rt.rootPath, filepath.FromSlash(rt.relDir), fname)
	if err := a.Files.WriteFile(abs, payload); err != nil {
		return err
	}
	return a.Map.Put(Mapping{
		LocalID: abs, Kind: rt.kind, ObjectID: ch.ObjectID, CollectionID: ch.CollectionID,
		ContentHash: ch.ContentHash, Revision: ch.Revision,
	})
}

func (a *Agent) pushLocal(ctx context.Context) error {
	return a.pushFiles(ctx)
}

func (a *Agent) pushFiles(ctx context.Context) error {
	if a.Files == nil || !a.Sel.Files.Enabled {
		return nil
	}
	for _, folder := range a.Sel.Files.Folders {
		if err := ctx.Err(); err != nil {
			return err
		}
		if err := a.pushFileFolder(ctx, folder); err != nil {
			a.Log.Warn("push_files", "path", folder.Path, "err", err.Error())
		}
	}
	return nil
}

func (a *Agent) pushFileFolder(ctx context.Context, folder FileFolderSel) error {
	root, err := filepath.Abs(folder.Path)
	if err != nil {
		return err
	}
	name := folder.Name
	if name == "" {
		name = filepath.Base(root)
	}
	rootCol, err := a.ensureCollection(kindFiles, name)
	if err != nil {
		return err
	}
	a.routes[rootCol.ID] = route{kind: kindFile, rootPath: root, relDir: ""}
	list, err := a.Files.ListFiles(root)
	if err != nil {
		a.Log.Warn("list_files_failed", "path", root, "err", err.Error())
		return nil
	}
	a.Log.Info("files_list", "path", root, "n", len(list))
	seen := map[string]bool{}
	colCache := map[string]string{"": rootCol.ID}
	ok, fail := 0, 0
	for _, f := range list {
		if err := ctx.Err(); err != nil {
			return err
		}
		dir := slashRelDir(f.RelPath)
		colID, err := a.ensureFileDirCollection(root, rootCol.ID, dir, colCache)
		if err != nil {
			fail++
			a.Log.Warn("ensure_file_dir", "dir", dir, "err", err.Error())
			continue
		}
		seen[f.AbsPath] = true
		if err := a.pushFile(colID, f); err != nil {
			fail++
			a.Log.Warn("push_file", "err", err.Error(), "local", f.AbsPath)
		} else {
			ok++
		}
	}
	a.Log.Info("files_push", "path", root, "ok", ok, "fail", fail)
	for colID, rt := range a.routes {
		if rt.kind != kindFile || rt.rootPath != root {
			continue
		}
		mapped, err := a.Map.ForCollection(colID)
		if err != nil {
			return err
		}
		for _, row := range mapped {
			if seen[row.LocalID] {
				continue
			}
			if !strings.HasPrefix(row.LocalID, root+string(os.PathSeparator)) && row.LocalID != root {
				continue
			}
			if err := a.pushDelete(row); err != nil {
				a.Log.Warn("push_delete", "err", err.Error(), "object", row.ObjectID)
			}
		}
	}
	return nil
}

func (a *Agent) ensureFileDirCollection(root, rootColID, relDir string, cache map[string]string) (string, error) {
	if id, ok := cache[relDir]; ok {
		return id, nil
	}
	parts := strings.Split(relDir, "/")
	parentID := rootColID
	cur := ""
	for _, p := range parts {
		if p == "" || p == "." {
			continue
		}
		if cur == "" {
			cur = p
		} else {
			cur = cur + "/" + p
		}
		if id, ok := cache[cur]; ok {
			parentID = id
			continue
		}
		col, err := a.ensureChildFilesCollection(parentID, p)
		if err != nil {
			return "", err
		}
		a.routes[col.ID] = route{kind: kindFile, rootPath: root, relDir: cur}
		cache[cur] = col.ID
		parentID = col.ID
	}
	cache[relDir] = parentID
	return parentID, nil
}

// slashRelDir returns the parent directory of a slash-separated relative path.
func slashRelDir(rel string) string {
	rel = filepath.ToSlash(rel)
	i := strings.LastIndex(rel, "/")
	if i <= 0 {
		return ""
	}
	return rel[:i]
}

func (a *Agent) pushFile(collectionID string, f LocalFile) error {
	if skipFileName(f.Name) {
		return nil
	}
	hash := ncrypto.SHA256Hex(f.Data)
	row, err := a.Map.ByLocal(kindFile, f.AbsPath)
	if err == nil && row.ContentHash == hash && row.CollectionID == collectionID {
		return nil
	}
	if err := a.putBlob(hash, f.Data); err != nil {
		return err
	}
	mime := http.DetectContentType(f.Data)
	in := syncengine.ChangeInput{
		CollectionID: collectionID,
		Kind:         kindFile,
		ContentHash:  hash,
		BlobID:       hash,
		Size:         f.Size,
		Metadata:     dav.EncodeFileMeta(dav.FileMeta{Name: f.Name, MIME: mime}),
		Force:        true,
	}
	if errors.Is(err, sql.ErrNoRows) {
		in.ObjectID = ids.New()
		in.Op = syncengine.OpCreate
	} else if err != nil {
		return err
	} else {
		in.ObjectID = row.ObjectID
		in.Op = syncengine.OpUpdate
		in.BaseRevision = row.Revision
	}
	res, err := a.Client.Push(ids.New(), []syncengine.ChangeInput{in})
	if err != nil {
		return err
	}
	if len(res) == 0 || res[0].Status != "ok" {
		return errors.New("push file rejected")
	}
	return a.Map.Put(Mapping{
		LocalID: f.AbsPath, Kind: kindFile, ObjectID: in.ObjectID, CollectionID: collectionID,
		ContentHash: hash, Revision: res[0].Revision,
	})
}

func (a *Agent) putBlob(hash string, payload []byte) error {
	if err := a.Client.PutBlob(hash, payload); err != nil {
		return fmt.Errorf("%w (bytes=%d)", err, len(payload))
	}
	return nil
}

func (a *Agent) pushDelete(row Mapping) error {
	res, err := a.Client.Push(ids.New(), []syncengine.ChangeInput{{
		ObjectID: row.ObjectID, CollectionID: row.CollectionID, Op: syncengine.OpDelete,
		BaseRevision: row.Revision, Force: true,
	}})
	if err != nil {
		return err
	}
	if len(res) == 0 || res[0].Status != "ok" {
		return errors.New("push delete rejected")
	}
	return a.Map.DeleteObject(row.ObjectID)
}

// reconcile handles a server request to rebuild sync state. Folder sync has
// never reconciled: each pushLocal rescans the selected folders and pushes
// what changed, so there is nothing to do here.
func (a *Agent) reconcile(ctx context.Context) error {
	return nil
}

func (a *Agent) bindRoutes() error {
	a.routes = map[string]route{}
	if a.Sel.Files.Enabled && a.Files != nil {
		for _, folder := range a.Sel.Files.Folders {
			if err := a.bindFileFolder(folder); err != nil {
				return err
			}
		}
	}
	return nil
}

func (a *Agent) bindFileFolder(folder FileFolderSel) error {
	root, err := filepath.Abs(folder.Path)
	if err != nil {
		return err
	}
	name := folder.Name
	if name == "" {
		name = filepath.Base(root)
	}
	col, err := a.ensureCollection(kindFiles, name)
	if err != nil {
		return err
	}
	a.routes[col.ID] = route{kind: kindFile, rootPath: root, relDir: ""}
	return a.bindFileSubdirs(root, col.ID, "")
}

func (a *Agent) bindFileSubdirs(root, parentColID, relDir string) error {
	dir := filepath.Join(root, filepath.FromSlash(relDir))
	entries, err := os.ReadDir(dir)
	if err != nil {
		return err
	}
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		name := e.Name()
		if name == ".git" || name == "node_modules" || strings.HasPrefix(name, ".") {
			continue
		}
		if !dav.ValidDisplayName(name) {
			continue
		}
		childRel := name
		if relDir != "" {
			childRel = relDir + "/" + name
		}
		col, err := a.ensureChildFilesCollection(parentColID, name)
		if err != nil {
			return err
		}
		a.routes[col.ID] = route{kind: kindFile, rootPath: root, relDir: childRel}
		if err := a.bindFileSubdirs(root, col.ID, childRel); err != nil {
			return err
		}
	}
	return nil
}

func (a *Agent) ensureChildFilesCollection(parentID, name string) (*syncengine.Collection, error) {
	name = dav.DAVResourceName(name, "")
	cols, err := a.collections()
	if err != nil {
		return nil, err
	}
	for i := range cols {
		c := &cols[i]
		if c.Kind == kindFiles && c.Name == name && c.ParentID == parentID && c.DeletedAt == nil && ownCollection(c) {
			return c, nil
		}
	}
	a.colCache = nil
	return a.Client.EnsureChildCollection(kindFiles, parentID, name, nil)
}

// collections returns the server's collection list, refetching only when
// Hello reported a different collections_rev (or the server sends none).
func (a *Agent) collections() ([]syncengine.Collection, error) {
	if a.colCache != nil && a.colRev != "" && a.colCacheRev == a.colRev {
		return a.colCache, nil
	}
	cols, err := a.Client.Collections()
	if err != nil {
		return nil, err
	}
	a.colCache, a.colCacheRev = cols, a.colRev
	return cols, nil
}

// ownCollection reports whether c belongs to this device's user. The agent
// mirrors local data only into its own collections, never into calendars or
// folders other users shared with it (older servers send no access field).
func ownCollection(c *syncengine.Collection) bool {
	return c.Access == "" || c.Access == "owner"
}

func (a *Agent) ensureCollection(kind, name string) (*syncengine.Collection, error) {
	if name == "" {
		name = "Untitled"
	}
	name = dav.DAVResourceName(name, "")
	cols, err := a.collections()
	if err != nil {
		return nil, err
	}
	for i := range cols {
		if cols[i].Kind == kind && cols[i].Name == name && cols[i].DeletedAt == nil && ownCollection(&cols[i]) {
			return &cols[i], nil
		}
	}
	a.colCache = nil
	return a.Client.EnsureCollection(kind, name, nil)
}

func (a *Agent) runSync(ctx context.Context) {
	if err := a.SyncOnce(ctx); err != nil {
		a.Log.Error("sync", "err", err.Error())
		return
	}
	a.Log.Info("sync_ok", "files", a.Sel.Files.Enabled)
}

func (a *Agent) Run(ctx context.Context) error {
	a.runSync(ctx)
	d := time.Duration(a.Sel.IntervalSeconds) * time.Second
	if d < 15*time.Second {
		d = 15 * time.Second
	}
	t := time.NewTicker(d)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-t.C:
			a.runSync(ctx)
		}
	}
}
