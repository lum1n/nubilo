//go:build !darwin && !linux

package service

import (
	"fmt"
	"runtime"
)

// Install is unsupported on this OS.
func Install(s Spec) (string, error) {
	return "", fmt.Errorf("service: always-on install is not supported on %s", runtime.GOOS)
}

// Uninstall is unsupported on this OS.
func Uninstall(k Kind) error {
	return fmt.Errorf("service: always-on uninstall is not supported on %s", runtime.GOOS)
}

// Status is unsupported on this OS.
func Status(k Kind) (Info, error) {
	return Info{Kind: k, Detail: "unsupported on " + runtime.GOOS}, fmt.Errorf("service: always-on status is not supported on %s", runtime.GOOS)
}
