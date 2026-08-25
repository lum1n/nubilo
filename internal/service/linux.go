//go:build linux

package service

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

func unitPath(k Kind) (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, ".config", "systemd", "user", k.UnitName()), nil
}

// Install writes a systemd --user unit and enables it.
func Install(s Spec) (string, error) {
	if s.Kind == KindAgent {
		return "", fmt.Errorf("service: agent install requires macOS")
	}
	if err := ValidateSpec(s); err != nil {
		return "", err
	}
	if err := EnsureLogDir(s.DataDir); err != nil {
		return "", err
	}
	body, err := RenderSystemdUnit(s)
	if err != nil {
		return "", err
	}
	path, err := unitPath(s.Kind)
	if err != nil {
		return "", err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return "", err
	}
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		return "", err
	}
	if out, err := exec.Command("systemctl", "--user", "daemon-reload").CombinedOutput(); err != nil {
		return path, fmt.Errorf("systemctl daemon-reload: %w (%s)", err, strings.TrimSpace(string(out)))
	}
	if out, err := exec.Command("systemctl", "--user", "enable", "--now", s.Kind.UnitName()).CombinedOutput(); err != nil {
		return path, fmt.Errorf("systemctl enable: %w (%s)", err, strings.TrimSpace(string(out)))
	}
	return path, nil
}

// Uninstall disables and removes the systemd --user unit.
func Uninstall(k Kind) error {
	if k == KindAgent {
		path, err := unitPath(k)
		if err != nil {
			return err
		}
		_ = os.Remove(path)
		return nil
	}
	_ = exec.Command("systemctl", "--user", "disable", "--now", k.UnitName()).Run()
	path, err := unitPath(k)
	if err != nil {
		return err
	}
	if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
		return err
	}
	_ = exec.Command("systemctl", "--user", "daemon-reload").Run()
	return nil
}

// Status reports whether the user unit exists and is active.
func Status(k Kind) (Info, error) {
	path, err := unitPath(k)
	if err != nil {
		return Info{}, err
	}
	st := Info{Kind: k, Path: path}
	if _, err := os.Stat(path); err == nil {
		st.Installed = true
	} else if !os.IsNotExist(err) {
		return st, err
	}
	if k == KindAgent {
		if st.Installed {
			st.Detail = "unit file present (agent requires macOS)"
		} else {
			st.Detail = "not installed"
		}
		return st, nil
	}
	out, err := exec.Command("systemctl", "--user", "is-active", k.UnitName()).CombinedOutput()
	active := strings.TrimSpace(string(out))
	if err == nil && active == "active" {
		st.Loaded = true
		st.Detail = "active"
	} else if st.Installed {
		st.Detail = active
		if st.Detail == "" {
			st.Detail = "inactive"
		}
	} else {
		st.Detail = "not installed"
	}
	return st, nil
}
