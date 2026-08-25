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
	Emails    []ContactValue   `json:"emails"`
	Phones    []ContactValue   `json:"phones"`
	Addresses []ContactAddress `json:"addresses"`
	Birthday  string           `json:"birthday"`
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
			Emails: r.Emails, Phones: r.Phones, Addresses: r.Addresses,
			Birthday: NormalizeBirthday(r.Birthday),
		}
		out = append(out, LocalContact{ID: r.ID, UID: uid, VCard: EncodeContactVCard(spec)})
	}
	return out, nil
}

func (s *cnSource) UpsertContact(localID string, vcf []byte) (string, error) {
	spec := ParseContactVCard(vcf)
	payload, err := json.Marshal(map[string]any{
		"given":     spec.Given,
		"family":    spec.Family,
		"fn":        spec.DisplayName(),
		"emails":    spec.Emails,
		"phones":    spec.Phones,
		"addresses": spec.Addresses,
		"birthday":  NormalizeBirthday(spec.Birthday),
	})
	if err != nil {
		return "", err
	}
	cid := C.CString(localID)
	defer C.free(unsafe.Pointer(cid))
	cjson := C.CString(string(payload))
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
