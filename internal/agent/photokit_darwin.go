//go:build darwin

package agent

/*
#cgo CFLAGS: -fobjc-arc
#cgo LDFLAGS: -framework Photos -framework Foundation
#include "photokit_darwin.h"
#include <stdlib.h>
*/
import "C"

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"unsafe"
)

type pkSource struct{}

func openPhotoKit() (*pkSource, error) {
	return &pkSource{}, nil
}

func pkErr(err **C.char) error {
	if err == nil || *err == nil {
		return errors.New("photokit error")
	}
	defer C.free(unsafe.Pointer(*err))
	return errors.New(C.GoString(*err))
}

func pkStr(p *C.char) string {
	if p == nil {
		return ""
	}
	defer C.free(unsafe.Pointer(p))
	return C.GoString(p)
}

func (p *pkSource) ListAlbums() ([]PhotoInfo, error) {
	list, err := listPhotoCollections()
	if err != nil {
		return nil, err
	}
	return list.Albums, nil
}

type photoCollectionList struct {
	LibraryCount int
	Albums       []PhotoInfo
}

func listPhotoCollections() (photoCollectionList, error) {
	var err *C.char
	raw := C.nubilo_pk_list_albums(&err)
	if raw == nil {
		return photoCollectionList{}, pkErr(&err)
	}
	s := pkStr(raw)
	var payload struct {
		LibraryCount int `json:"library_count"`
		Albums       []struct {
			ID    string `json:"id"`
			Title string `json:"title"`
			Kind  string `json:"kind"`
			Count int    `json:"count"`
		} `json:"albums"`
	}
	if err := json.Unmarshal([]byte(s), &payload); err != nil {
		return photoCollectionList{}, err
	}
	out := make([]PhotoInfo, 0, len(payload.Albums))
	for _, r := range payload.Albums {
		kind := r.Kind
		if kind == "" {
			kind = "user"
		}
		out = append(out, PhotoInfo{ID: r.ID, Title: r.Title, Kind: kind, Count: r.Count})
	}
	return photoCollectionList{LibraryCount: payload.LibraryCount, Albums: out}, nil
}

// PlatformAlbumList returns albums/people/pets plus library-wide asset count.
func PlatformAlbumList() (libraryCount int, albums []PhotoInfo, err error) {
	pk, err := openPhotoKit()
	if err != nil {
		return 0, nil, err
	}
	_ = pk
	list, err := listPhotoCollections()
	if err != nil {
		return 0, nil, err
	}
	return list.LibraryCount, list.Albums, nil
}

func PhotosAuthStatus() string {
	raw := C.nubilo_pk_auth_status()
	return pkStr(raw)
}

func RequestPhotosAccess() (status string, err error) {
	var cerr *C.char
	ok := C.nubilo_pk_request_access(&cerr)
	status = PhotosAuthStatus()
	if ok == 0 {
		return status, pkErr(&cerr)
	}
	return status, nil
}

func (p *pkSource) ListPhotos(filter PhotoFilter) ([]LocalPhoto, error) {
	src := C.CString(filter.Source)
	defer C.free(unsafe.Pointer(src))
	albums, _ := json.Marshal(filter.Albums)
	aj := C.CString(string(albums))
	defer C.free(unsafe.Pointer(aj))
	after, before := 0.0, 0.0
	if filter.AfterMS > 0 {
		after = float64(filter.AfterMS) / 1000
	}
	if filter.BeforeMS > 0 {
		before = float64(filter.BeforeMS) / 1000
	}
	var err *C.char
	raw := C.nubilo_pk_list_assets(src, aj, C.double(after), C.double(before), &err)
	if raw == nil {
		return nil, pkErr(&err)
	}
	s := pkStr(raw)
	var rows []struct {
		ID       string   `json:"id"`
		Filename string   `json:"filename"`
		Taken    float64  `json:"taken"`
		Mod      float64  `json:"mod"`
		Albums   []string `json:"albums"`
		Kind     string   `json:"kind"`
		Duration float64  `json:"duration"`
	}
	if err := json.Unmarshal([]byte(s), &rows); err != nil {
		return nil, err
	}
	out := make([]LocalPhoto, 0, len(rows))
	for _, r := range rows {
		kind := r.Kind
		if kind == "" {
			kind = "image"
		}
		out = append(out, LocalPhoto{
			ID: r.ID, Filename: r.Filename, Kind: kind,
			TakenAtMS: int64(r.Taken * 1000), ModMS: int64(r.Mod * 1000),
			DurationMS: int64(r.Duration * 1000), Albums: r.Albums,
		})
	}
	return out, nil
}

func (p *pkSource) ReadOriginal(id string) ([]byte, error) {
	tmp, err := os.CreateTemp("", "nubilo-pk-*.bin")
	if err != nil {
		return nil, err
	}
	path := tmp.Name()
	tmp.Close()
	defer os.Remove(path)
	cid := C.CString(id)
	defer C.free(unsafe.Pointer(cid))
	cpath := C.CString(path)
	defer C.free(unsafe.Pointer(cpath))
	var cerr *C.char
	if C.nubilo_pk_export_original(cid, cpath, &cerr) == 0 {
		return nil, pkErr(&cerr)
	}
	return os.ReadFile(path)
}

func (p *pkSource) ReadLiveMovie(id string) ([]byte, error) {
	tmp, err := os.CreateTemp("", "nubilo-pk-live-*.mov")
	if err != nil {
		return nil, err
	}
	path := tmp.Name()
	tmp.Close()
	defer os.Remove(path)
	cid := C.CString(id)
	defer C.free(unsafe.Pointer(cid))
	cpath := C.CString(path)
	defer C.free(unsafe.Pointer(cpath))
	var cerr *C.char
	if C.nubilo_pk_export_live_movie(cid, cpath, &cerr) == 0 {
		if cerr != nil {
			return nil, pkErr(&cerr)
		}
		return nil, nil
	}
	return os.ReadFile(path)
}

func (p *pkSource) ImportOriginal(data []byte, filename string, albumID string) (string, error) {
	ext := filepath.Ext(filename)
	if ext == "" {
		ext = ".bin"
	}
	tmp, err := os.CreateTemp("", "nubilo-pk-import-*"+ext)
	if err != nil {
		return "", err
	}
	path := tmp.Name()
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		os.Remove(path)
		return "", err
	}
	tmp.Close()
	defer os.Remove(path)

	cpath := C.CString(path)
	defer C.free(unsafe.Pointer(cpath))
	calbum := C.CString(albumID)
	defer C.free(unsafe.Pointer(calbum))
	cname := C.CString(filename)
	defer C.free(unsafe.Pointer(cname))
	var outID *C.char
	var cerr *C.char
	if C.nubilo_pk_import(cpath, calbum, cname, &outID, &cerr) == 0 {
		return "", pkErr(&cerr)
	}
	id := pkStr(outID)
	if strings.TrimSpace(id) == "" {
		return "", errors.New("import returned empty id")
	}
	return id, nil
}
