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
	"bytes"
	"encoding/json"
	"errors"
	"strings"
	"unsafe"

	"github.com/emersion/go-vcard"
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

func (s *cnSource) ListContacts() ([]LocalContact, error) {
	var err *C.char
	p := C.nubilo_cn_list(&err)
	if p == nil {
		return nil, cnErr(&err)
	}
	defer C.free(unsafe.Pointer(p))
	var rows []struct {
		ID     string `json:"id"`
		UID    string `json:"uid"`
		Given  string `json:"given"`
		Family string `json:"family"`
		FN     string `json:"fn"`
		Email  string `json:"email"`
	}
	if err := json.Unmarshal([]byte(C.GoString(p)), &rows); err != nil {
		return nil, err
	}
	out := make([]LocalContact, 0, len(rows))
	for _, r := range rows {
		uid := r.UID
		if uid == "" {
			uid = r.ID
		}
		vcf := encodeSimpleVCard(uid, r.FN, r.Given, r.Family, r.Email)
		out = append(out, LocalContact{ID: r.ID, UID: uid, VCard: vcf})
	}
	return out, nil
}

func (s *cnSource) UpsertContact(localID string, vcf []byte) (string, error) {
	uid, fn, given, family, email := vcardFields(vcf)
	cid := C.CString(localID)
	defer C.free(unsafe.Pointer(cid))
	cuid := C.CString(uid)
	defer C.free(unsafe.Pointer(cuid))
	cg := C.CString(given)
	defer C.free(unsafe.Pointer(cg))
	cf := C.CString(family)
	defer C.free(unsafe.Pointer(cf))
	cfn := C.CString(fn)
	defer C.free(unsafe.Pointer(cfn))
	cem := C.CString(email)
	defer C.free(unsafe.Pointer(cem))
	var err *C.char
	p := C.nubilo_cn_save(cid, cuid, cg, cf, cfn, cem, &err)
	if p == nil {
		return "", cnErr(&err)
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

func encodeSimpleVCard(uid, fn, given, family, email string) []byte {
	card := make(vcard.Card)
	card.SetValue(vcard.FieldUID, uid)
	if fn != "" {
		card.SetValue(vcard.FieldFormattedName, fn)
	}
	if given != "" || family != "" {
		card.Set(vcard.FieldName, &vcard.Field{Value: family + ";" + given + ";;;"})
	}
	if email != "" {
		card.SetValue(vcard.FieldEmail, email)
	}
	card.SetValue(vcard.FieldVersion, "3.0")
	var buf bytes.Buffer
	_ = vcard.NewEncoder(&buf).Encode(card)
	return buf.Bytes()
}

func vcardFields(vcf []byte) (uid, fn, given, family, email string) {
	card, err := vcard.NewDecoder(bytes.NewReader(vcf)).Decode()
	if err != nil {
		return "", "", "", "", ""
	}
	uid = strings.TrimSpace(card.Value(vcard.FieldUID))
	fn = strings.TrimSpace(card.Value(vcard.FieldFormattedName))
	email = strings.TrimSpace(card.Value(vcard.FieldEmail))
	if n := card.Get(vcard.FieldName); n != nil {
		parts := strings.Split(n.Value, ";")
		if len(parts) > 0 {
			family = parts[0]
		}
		if len(parts) > 1 {
			given = parts[1]
		}
	}
	return uid, fn, given, family, email
}
