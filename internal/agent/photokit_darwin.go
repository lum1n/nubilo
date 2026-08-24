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
	var err *C.char
	raw := C.nubilo_pk_list_albums(&err)
	if raw == nil {
		return nil, pkErr(&err)
	}
	s := pkStr(raw)
	var rows []struct {
		ID    string `json:"id"`
		Title string `json:"title"`
	}
	if err := json.Unmarshal([]byte(s), &rows); err != nil {
		return nil, err
	}
	out := make([]PhotoInfo, 0, len(rows))
	for _, r := range rows {
		out = append(out, PhotoInfo{ID: r.ID, Title: r.Title})
	}
	return out, nil
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
	}
	if err := json.Unmarshal([]byte(s), &rows); err != nil {
		return nil, err
	}
	out := make([]LocalPhoto, 0, len(rows))
	for _, r := range rows {
		out = append(out, LocalPhoto{
			ID: r.ID, Filename: r.Filename,
			TakenAtMS: int64(r.Taken * 1000), ModMS: int64(r.Mod * 1000),
			Albums: r.Albums,
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
