//go:build darwin

package agent

/*
#cgo CFLAGS: -fobjc-arc
#cgo LDFLAGS: -framework Contacts -framework Foundation
#include "contacts_darwin.h"
#include <stdlib.h>
*/
import "C"

import (
	"encoding/base64"
	"encoding/json"
	"errors"
	"unsafe"
)

type cnSource struct{}

func openContacts() (*cnSource, error) {
	return &cnSource{}, nil
}

func cnErr(err **C.char) error {
	if err == nil || *err == nil {
		return errors.New("contacts error")
	}
	defer C.free(unsafe.Pointer(*err))
	return errors.New(C.GoString(*err))
}

type cnListRow struct {
	ID        string           `json:"id"`
	UID       string           `json:"uid"`
	Given     string           `json:"given"`
	Family    string           `json:"family"`
	FN        string           `json:"fn"`
	Org       string           `json:"org"`
	Nickname  string           `json:"nickname"`
	Note      string           `json:"note"`
	Emails    []ContactValue   `json:"emails"`
	Phones    []ContactValue   `json:"phones"`
	Addresses []ContactAddress `json:"addresses"`
	URLs      []ContactValue   `json:"urls"`
	Birthday  string           `json:"birthday"`
	PhotoB64  string           `json:"photo_b64"`
}

func (s *cnSource) ListContacts() ([]LocalContact, error) {
	var err *C.char
	p := C.nubilo_cn_list(&err)
	if p == nil {
		return nil, cnErr(&err)
	}
	defer C.free(unsafe.Pointer(p))
	var rows []cnListRow
	if err := json.Unmarshal([]byte(C.GoString(p)), &rows); err != nil {
		return nil, err
	}
	out := make([]LocalContact, 0, len(rows))
	for _, r := range rows {
		uid := r.UID
		if uid == "" {
			uid = r.ID
		}
		spec := ContactSpec{
			UID: uid, FN: r.FN, Given: r.Given, Family: r.Family,
			Org: r.Org, Nickname: r.Nickname, Note: r.Note,
			Emails: r.Emails, Phones: r.Phones, Addresses: r.Addresses, URLs: r.URLs,
			Birthday: NormalizeBirthday(r.Birthday),
		}
		if r.PhotoB64 != "" {
			if raw, err := base64.StdEncoding.DecodeString(r.PhotoB64); err == nil {
				spec.Photo = raw
			}
		}
		out = append(out, LocalContact{ID: r.ID, UID: uid, VCard: EncodeContactVCard(spec)})
	}
	return out, nil
}

func (s *cnSource) UpsertContact(localID string, vcf []byte) (string, error) {
	spec := ParseContactVCard(vcf)
	payload := map[string]any{
		"given":     spec.Given,
		"family":    spec.Family,
		"fn":        spec.DisplayName(),
		"org":       spec.Org,
		"nickname":  spec.Nickname,
		"note":      spec.Note,
		"emails":    spec.Emails,
		"phones":    spec.Phones,
		"addresses": spec.Addresses,
		"urls":      spec.URLs,
		"birthday":  NormalizeBirthday(spec.Birthday),
	}
	if len(spec.Photo) > 0 {
		payload["photo_b64"] = base64.StdEncoding.EncodeToString(spec.Photo)
	}
	raw, err := json.Marshal(payload)
	if err != nil {
		return "", err
	}
	cid := C.CString(localID)
	defer C.free(unsafe.Pointer(cid))
	cjson := C.CString(string(raw))
	defer C.free(unsafe.Pointer(cjson))
	var cerr *C.char
	p := C.nubilo_cn_save(cid, cjson, &cerr)
	if p == nil {
		return "", cnErr(&cerr)
	}
	defer C.free(unsafe.Pointer(p))
	return C.GoString(p), nil
}

func (s *cnSource) DeleteContact(localID string) error {
	cid := C.CString(localID)
	defer C.free(unsafe.Pointer(cid))
	var err *C.char
	if C.nubilo_cn_delete(cid, &err) == 0 {
		return cnErr(&err)
	}
	return nil
}
