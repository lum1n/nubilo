//go:build darwin

package main

/*
#cgo LDFLAGS: -Wl,-sectcreate,__TEXT,__info_plist,${SRCDIR}/Info.plist
*/
import "C"
