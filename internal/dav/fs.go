package dav

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/emersion/go-webdav"

	"nubilo/internal/authz"
	ncrypto "nubilo/internal/crypto"
	"nubilo/internal/identity"
	"nubilo/internal/ids"
	"nubilo/internal/store"
	"nubilo/internal/syncengine"
)

type ctxKey int

const deviceCtx ctxKey = 1

func WithDevice(ctx context.Context, d *identity.Device) context.Context {
	return context.WithValue(ctx, deviceCtx, d)
}

func DeviceFrom(ctx context.Context) *identity.Device {
	d, _ := ctx.Value(deviceCtx).(*identity.Device)
	return d
}

type FS struct {
	Engine *syncengine.Engine
	Store  *store.Store
	Prefix string
}

func NewFS(eng *syncengine.Engine, st *store.Store) *FS {
	return &FS{Engine: eng, Store: st, Prefix: Prefix}
}

type nodeKind int

const (
	nodeRoot nodeKind = iota
	nodeFilesHome
	nodeCollection
	nodeFile
)

type node struct {
	kind nodeKind
	path string
	col  *syncengine.Collection
	obj  *syncengine.Object
}

func (f *FS) resolve(ctx context.Context, name string) (*node, error) {
	p, err := Normalize(name)
	if err != nil {
		return nil, webdav.NewHTTPError(http.StatusBadRequest, err)
	}
	if p != "/" && !HasPrefix(p, f.Prefix) {
		return nil, webdav.NewHTTPError(http.StatusNotFound, os.ErrNotExist)
	}
	rel := strings.TrimPrefix(p, f.Prefix)
	if rel == "" {
		rel = "/"
	}
	segs, err := Split(rel)
	if err != nil {
		return nil, webdav.NewHTTPError(http.StatusBadRequest, err)
	}
	if len(segs) == 0 {
		return &node{kind: nodeRoot, path: f.Prefix + "/"}, nil
	}
	if segs[0] != "files" {
		return nil, webdav.NewHTTPError(http.StatusNotFound, os.ErrNotExist)
	}
	if len(segs) == 1 {
		return &node{kind: nodeFilesHome, path: Join(f.Prefix, "files")}, nil
	}
	parentID := ""
	var col *syncengine.Collection
	for i := 1; i < len(segs); i++ {
		name := segs[i]
		child, err := f.Engine.FindChildCollection(ctx, "files", parentID, name)
		if err == nil {
			col = child
			parentID = child.ID
			if i == len(segs)-1 {
				return &node{kind: nodeCollection, path: p, col: col}, nil
			}
			continue
		}
		if !errors.Is(err, syncengine.ErrNotFound) {
			return nil, err
		}
		if col == nil {
			return nil, webdav.NewHTTPError(http.StatusNotFound, os.ErrNotExist)
		}
		if i != len(segs)-1 {
			return nil, webdav.NewHTTPError(http.StatusNotFound, os.ErrNotExist)
		}
		obj, err := f.Engine.FindObjectByName(ctx, col.ID, name)
		if err != nil {
			if errors.Is(err, syncengine.ErrNotFound) {
				return nil, webdav.NewHTTPError(http.StatusNotFound, os.ErrNotExist)
			}
			return nil, err
		}
		return &node{kind: nodeFile, path: p, col: col, obj: obj}, nil
	}
	return nil, webdav.NewHTTPError(http.StatusNotFound, os.ErrNotExist)
}

func (f *FS) allow(ctx context.Context, write bool, collectionID string) error {
	dev := DeviceFrom(ctx)
	act := authz.DAVRead
	if write {
		act = authz.DAVWrite
	}
	if err := authz.Allow(dev, act, collectionID); err != nil {
		return webdav.NewHTTPError(http.StatusForbidden, err)
	}
	return nil
}

func (f *FS) Stat(ctx context.Context, name string) (*webdav.FileInfo, error) {
	n, err := f.resolve(ctx, name)
	if err != nil {
		return nil, err
	}
	if err := f.allow(ctx, false, collectionID(n)); err != nil {
		return nil, err
	}
	return f.info(n), nil
}

func (f *FS) ReadDir(ctx context.Context, name string, recursive bool) ([]webdav.FileInfo, error) {
	n, err := f.resolve(ctx, name)
	if err != nil {
		return nil, err
	}
	if err := f.allow(ctx, false, collectionID(n)); err != nil {
		return nil, err
	}
	switch n.kind {
	case nodeRoot:
		return []webdav.FileInfo{*f.info(&node{kind: nodeFilesHome, path: Join(f.Prefix, "files")})}, nil
	case nodeFilesHome:
		cols, err := f.Engine.ChildCollections(ctx, "files", "")
		if err != nil {
			return nil, err
		}
		var out []webdav.FileInfo
		for i := range cols {
			c := cols[i]
			child := &node{kind: nodeCollection, path: Join(f.Prefix, "files", c.Name), col: &c}
			if err := f.allow(ctx, false, c.ID); err != nil {
				continue
			}
			out = append(out, *f.info(child))
			if recursive {
				more, err := f.ReadDir(ctx, child.path, true)
				if err != nil {
					return nil, err
				}
				out = append(out, more...)
			}
		}
		return out, nil
	case nodeCollection:
		return f.readCollection(ctx, n, recursive)
	default:
		return nil, webdav.NewHTTPError(http.StatusMethodNotAllowed, errors.New("not a collection"))
	}
}

func (f *FS) readCollection(ctx context.Context, n *node, recursive bool) ([]webdav.FileInfo, error) {
	var out []webdav.FileInfo
	children, err := f.Engine.ChildCollections(ctx, "files", n.col.ID)
	if err != nil {
		return nil, err
	}
	for i := range children {
		c := children[i]
		child := &node{kind: nodeCollection, path: Join(n.path, c.Name), col: &c}
		out = append(out, *f.info(child))
		if recursive {
			more, err := f.readCollection(ctx, child, true)
			if err != nil {
				return nil, err
			}
			out = append(out, more...)
		}
	}
	objs, err := f.Engine.ListObjects(ctx, n.col.ID)
	if err != nil {
		return nil, err
	}
	for i := range objs {
		o := objs[i]
		meta := ParseFileMeta(o.Metadata)
		name := meta.Name
		if name == "" {
			name = o.ID
		}
		out = append(out, *f.info(&node{kind: nodeFile, path: Join(n.path, name), col: n.col, obj: &o}))
	}
	return out, nil
}

func (f *FS) Open(ctx context.Context, name string) (io.ReadCloser, error) {
	n, err := f.resolve(ctx, name)
	if err != nil {
		return nil, err
	}
	if n.kind != nodeFile || n.obj == nil {
		return nil, webdav.NewHTTPError(http.StatusMethodNotAllowed, errors.New("not a file"))
	}
	if err := f.allow(ctx, false, n.col.ID); err != nil {
		return nil, err
	}
	if n.obj.BlobID == "" {
		return io.NopCloser(bytes.NewReader(nil)), nil
	}
	pt, err := f.Store.GetBlobPlaintext(n.obj.BlobID)
	if err != nil {
		return nil, err
	}
	return &seekCloser{Reader: bytes.NewReader(pt)}, nil
}

type seekCloser struct {
	*bytes.Reader
}

func (s *seekCloser) Close() error { return nil }

func (f *FS) Create(ctx context.Context, name string, body io.ReadCloser, opts *webdav.CreateOptions) (*webdav.FileInfo, bool, error) {
	defer body.Close()
	p, err := Normalize(name)
	if err != nil {
		return nil, false, webdav.NewHTTPError(http.StatusBadRequest, err)
	}
	parentPath, fileName, err := splitParent(p)
	if err != nil {
		return nil, false, webdav.NewHTTPError(http.StatusBadRequest, err)
	}
	if !ValidDisplayName(fileName) {
		return nil, false, webdav.NewHTTPError(http.StatusBadRequest, errors.New("invalid name"))
	}
	parent, err := f.resolve(ctx, parentPath)
	if err != nil {
		return nil, false, err
	}
	if parent.kind != nodeCollection {
		return nil, false, webdav.NewHTTPError(http.StatusConflict, errors.New("parent is not a collection"))
	}
	if err := f.allow(ctx, true, parent.col.ID); err != nil {
		return nil, false, err
	}
	dev := DeviceFrom(ctx)
	if _, err := f.Engine.FindChildCollection(ctx, "files", parent.col.ID, fileName); err == nil {
		return nil, false, webdav.NewHTTPError(http.StatusConflict, errors.New("name exists as collection"))
	}

	payload, err := io.ReadAll(body)
	if err != nil {
		return nil, false, err
	}
	sum := ncrypto.SHA256Hex(payload)
	blobID, size, err := f.Store.PutBlob(ctx, bytes.NewReader(payload), sum)
	if err != nil {
		return nil, false, webdav.NewHTTPError(http.StatusRequestEntityTooLarge, err)
	}

	existing, err := f.Engine.FindObjectByName(ctx, parent.col.ID, fileName)
	created := errors.Is(err, syncengine.ErrNotFound)
	if err != nil && !created {
		return nil, false, err
	}
	if !created {
		if opts != nil {
			if opts.IfNoneMatch.IsSet() {
				ok, _ := opts.IfNoneMatch.MatchETag(existing.ContentHash)
				if opts.IfNoneMatch.IsWildcard() || ok {
					return nil, false, webdav.NewHTTPError(http.StatusPreconditionFailed, errors.New("exists"))
				}
			}
			if opts.IfMatch.IsSet() && !opts.IfMatch.IsWildcard() {
				ok, _ := opts.IfMatch.MatchETag(existing.ContentHash)
				if !ok {
					return nil, false, webdav.NewHTTPError(http.StatusPreconditionFailed, errors.New("etag"))
				}
			}
		}
	}
	meta := EncodeFileMeta(FileMeta{Name: fileName, MIME: http.DetectContentType(payload)})
	in := syncengine.ChangeInput{
		CollectionID: parent.col.ID,
		Kind:         "file",
		ContentHash:  blobID,
		BlobID:       blobID,
		Size:         size,
		Metadata:     meta,
	}
	if created {
		in.ObjectID = ids.New()
		in.Op = syncengine.OpCreate
	} else {
		in.ObjectID = existing.ID
		in.Op = syncengine.OpUpdate
		in.BaseRevision = existing.Revision
		in.Force = opts == nil || !opts.IfMatch.IsSet()
	}
	res, err := f.Engine.Push(ctx, dev, ids.New(), []syncengine.ChangeInput{in})
	if err != nil {
		return nil, false, err
	}
	if res[0].Status != "ok" {
		if res[0].Status == "conflict" {
			return nil, false, webdav.NewHTTPError(http.StatusPreconditionFailed, errors.New("conflict"))
		}
		return nil, false, webdav.NewHTTPError(http.StatusConflict, errors.New(res[0].Error))
	}
	obj, err := f.Engine.GetObject(ctx, in.ObjectID)
	if err != nil {
		return nil, created, err
	}
	fi := f.info(&node{kind: nodeFile, path: p, col: parent.col, obj: obj})
	return fi, created, nil
}

func (f *FS) Mkdir(ctx context.Context, name string) error {
	p, err := Normalize(name)
	if err != nil {
		return webdav.NewHTTPError(http.StatusBadRequest, err)
	}
	parentPath, dirName, err := splitParent(p)
	if err != nil {
		return webdav.NewHTTPError(http.StatusBadRequest, err)
	}
	if !ValidDisplayName(dirName) {
		return webdav.NewHTTPError(http.StatusBadRequest, errors.New("invalid name"))
	}
	parent, err := f.resolve(ctx, parentPath)
	if err != nil {
		return err
	}
	var parentID string
	switch parent.kind {
	case nodeFilesHome:
		parentID = ""
	case nodeCollection:
		parentID = parent.col.ID
	default:
		return webdav.NewHTTPError(http.StatusConflict, errors.New("cannot mkdir here"))
	}
	if err := f.allow(ctx, true, parentID); err != nil {
		return err
	}
	if _, err := f.Engine.FindChildCollection(ctx, "files", parentID, dirName); err == nil {
		return webdav.NewHTTPError(http.StatusMethodNotAllowed, os.ErrExist)
	}
	if parent.kind == nodeCollection {
		if _, err := f.Engine.FindObjectByName(ctx, parent.col.ID, dirName); err == nil {
			return webdav.NewHTTPError(http.StatusMethodNotAllowed, os.ErrExist)
		}
	}
	_, err = f.Engine.CreateCollection(ctx, "files", dirName, parentID, nil)
	return err
}

func (f *FS) RemoveAll(ctx context.Context, name string, opts *webdav.RemoveAllOptions) error {
	n, err := f.resolve(ctx, name)
	if err != nil {
		return err
	}
	switch n.kind {
	case nodeRoot, nodeFilesHome:
		return webdav.NewHTTPError(http.StatusForbidden, errors.New("cannot delete dav root"))
	case nodeFile:
		if err := f.allow(ctx, true, n.col.ID); err != nil {
			return err
		}
		if opts != nil && opts.IfMatch.IsSet() && !opts.IfMatch.IsWildcard() {
			ok, _ := opts.IfMatch.MatchETag(n.obj.ContentHash)
			if !ok {
				return webdav.NewHTTPError(http.StatusPreconditionFailed, errors.New("etag"))
			}
		}
		dev := DeviceFrom(ctx)
		res, err := f.Engine.Push(ctx, dev, ids.New(), []syncengine.ChangeInput{{
			ObjectID: n.obj.ID, CollectionID: n.col.ID, Op: syncengine.OpDelete,
			BaseRevision: n.obj.Revision, Force: opts == nil || !opts.IfMatch.IsSet(),
		}})
		if err != nil {
			return err
		}
		if res[0].Status != "ok" {
			return webdav.NewHTTPError(http.StatusConflict, errors.New(res[0].Status))
		}
		return nil
	case nodeCollection:
		if err := f.allow(ctx, true, n.col.ID); err != nil {
			return err
		}
		return f.deleteCollection(ctx, n.col)
	}
	return webdav.NewHTTPError(http.StatusNotFound, os.ErrNotExist)
}

func (f *FS) deleteCollection(ctx context.Context, col *syncengine.Collection) error {
	dev := DeviceFrom(ctx)
	children, err := f.Engine.ChildCollections(ctx, "files", col.ID)
	if err != nil {
		return err
	}
	for i := range children {
		if err := f.deleteCollection(ctx, &children[i]); err != nil {
			return err
		}
	}
	objs, err := f.Engine.ListObjects(ctx, col.ID)
	if err != nil {
		return err
	}
	for i := range objs {
		o := objs[i]
		res, err := f.Engine.Push(ctx, dev, ids.New(), []syncengine.ChangeInput{{
			ObjectID: o.ID, CollectionID: col.ID, Op: syncengine.OpDelete, BaseRevision: o.Revision, Force: true,
		}})
		if err != nil {
			return err
		}
		if res[0].Status != "ok" {
			return webdav.NewHTTPError(http.StatusConflict, errors.New(res[0].Status))
		}
	}
	return f.Engine.TombstoneCollection(ctx, col.ID)
}

func (f *FS) Copy(ctx context.Context, name, dest string, options *webdav.CopyOptions) (bool, error) {
	src, err := f.resolve(ctx, name)
	if err != nil {
		return false, err
	}
	if src.kind != nodeFile {
		return false, webdav.NewHTTPError(http.StatusForbidden, errors.New("copy collections is not implemented"))
	}
	if err := f.allow(ctx, false, src.col.ID); err != nil {
		return false, err
	}
	if options != nil && options.NoOverwrite {
		if _, err := f.resolve(ctx, dest); err == nil {
			return false, os.ErrExist
		}
	}
	body, err := f.Open(ctx, name)
	if err != nil {
		return false, err
	}
	_, created, err := f.Create(ctx, dest, body, nil)
	return created, err
}

func (f *FS) Move(ctx context.Context, name, dest string, options *webdav.MoveOptions) (bool, error) {
	src, err := f.resolve(ctx, name)
	if err != nil {
		return false, err
	}
	dp, err := Normalize(dest)
	if err != nil {
		return false, webdav.NewHTTPError(http.StatusBadRequest, err)
	}
	parentPath, newName, err := splitParent(dp)
	if err != nil || !ValidDisplayName(newName) {
		return false, webdav.NewHTTPError(http.StatusBadRequest, err)
	}
	parent, err := f.resolve(ctx, parentPath)
	if err != nil {
		return false, err
	}
	noOverwrite := options != nil && options.NoOverwrite
	switch src.kind {
	case nodeFile:
		if parent.kind != nodeCollection {
			return false, webdav.NewHTTPError(http.StatusConflict, errors.New("dest parent"))
		}
		if err := f.allow(ctx, true, src.col.ID); err != nil {
			return false, err
		}
		if err := f.allow(ctx, true, parent.col.ID); err != nil {
			return false, err
		}
		destObj, destErr := f.Engine.FindObjectByName(ctx, parent.col.ID, newName)
		created := errors.Is(destErr, syncengine.ErrNotFound)
		if destErr == nil && destObj.ID != src.obj.ID {
			if noOverwrite {
				return false, os.ErrExist
			}
			_, _ = f.Engine.Push(ctx, DeviceFrom(ctx), ids.New(), []syncengine.ChangeInput{{
				ObjectID: destObj.ID, CollectionID: parent.col.ID, Op: syncengine.OpDelete, BaseRevision: destObj.Revision, Force: true,
			}})
		}
		meta := ParseFileMeta(src.obj.Metadata)
		meta.Name = newName
		res, err := f.Engine.Push(ctx, DeviceFrom(ctx), ids.New(), []syncengine.ChangeInput{{
			ObjectID:     src.obj.ID,
			CollectionID: parent.col.ID,
			Kind:         "file",
			Op:           syncengine.OpUpdate,
			BaseRevision: src.obj.Revision,
			Force:        true,
			ContentHash:  src.obj.ContentHash,
			BlobID:       src.obj.BlobID,
			Size:         src.obj.Size,
			Metadata:     EncodeFileMeta(meta),
		}})
		if err != nil {
			return false, err
		}
		if res[0].Status != "ok" {
			return false, webdav.NewHTTPError(http.StatusConflict, errors.New(res[0].Status))
		}
		return created, nil
	case nodeCollection:
		if parent.kind != nodeFilesHome && parent.kind != nodeCollection {
			return false, webdav.NewHTTPError(http.StatusConflict, errors.New("dest parent"))
		}
		if err := f.allow(ctx, true, src.col.ID); err != nil {
			return false, err
		}
		parentID := ""
		if parent.kind == nodeCollection {
			parentID = parent.col.ID
			if parentID == src.col.ID || isAncestor(ctx, f.Engine, src.col.ID, parentID) {
				return false, webdav.NewHTTPError(http.StatusConflict, errors.New("cannot move into self"))
			}
		}
		if _, err := f.Engine.FindChildCollection(ctx, "files", parentID, newName); err == nil {
			if noOverwrite {
				return false, os.ErrExist
			}
		}
		if err := f.Engine.RenameCollection(ctx, src.col.ID, newName); err != nil {
			return false, err
		}
		return false, f.Engine.SetCollectionParent(ctx, src.col.ID, parentID)
	default:
		return false, webdav.NewHTTPError(http.StatusForbidden, errors.New("cannot move"))
	}
}

func isAncestor(ctx context.Context, eng *syncengine.Engine, id, maybeChild string) bool {
	cur := maybeChild
	for i := 0; i < 32 && cur != ""; i++ {
		if cur == id {
			return true
		}
		c, err := eng.GetCollection(ctx, cur)
		if err != nil {
			return false
		}
		cur = c.ParentID
	}
	return false
}

func (f *FS) info(n *node) *webdav.FileInfo {
	fi := &webdav.FileInfo{Path: n.path}
	switch n.kind {
	case nodeRoot, nodeFilesHome, nodeCollection:
		fi.IsDir = true
		if n.col != nil {
			fi.ModTime = syncengine.ModTime(n.col.UpdatedAt)
			fi.ETag = n.col.MetadataHash
		} else {
			fi.ModTime = time.Unix(0, 0).UTC()
		}
	case nodeFile:
		fi.Size = n.obj.Size
		fi.ModTime = syncengine.ModTime(n.obj.UpdatedAt)
		fi.ETag = n.obj.ContentHash
		fi.MIMEType = ParseFileMeta(n.obj.Metadata).MIME
	}
	if !strings.HasPrefix(fi.Path, "/") {
		fi.Path = "/" + fi.Path
	}
	return fi
}

func collectionID(n *node) string {
	if n == nil || n.col == nil {
		return ""
	}
	return n.col.ID
}

func splitParent(p string) (parent, base string, err error) {
	p, err = Normalize(p)
	if err != nil {
		return "", "", err
	}
	if p == "/" {
		return "", "", ErrBadPath
	}
	i := strings.LastIndex(p, "/")
	if i <= 0 {
		return "/", strings.TrimPrefix(p, "/"), nil
	}
	return p[:i], p[i+1:], nil
}

var _ webdav.FileSystem = (*FS)(nil)
