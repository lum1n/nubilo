//go:build darwin

package service

import (
	"fmt"
	"os"
	"os/exec"
	"os/user"
	"path/filepath"
	"strconv"
	"strings"
)

func plistPath(k Kind) (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, "Library", "LaunchAgents", k.Label()+".plist"), nil
}

func guiDomain() (string, error) {
	u, err := user.Current()
	if err != nil {
		return "", err
	}
	return "gui/" + u.Uid, nil
}

// Install writes a LaunchAgent and loads it.
func Install(s Spec) (string, error) {
	if err := ValidateSpec(s); err != nil {
		return "", err
	}
	if err := EnsureLogDir(s.DataDir); err != nil {
		return "", err
	}
	body, err := RenderPlist(s)
	if err != nil {
		return "", err
	}
	path, err := plistPath(s.Kind)
	if err != nil {
		return "", err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return "", err
	}
	// Unload existing before overwrite so launchd picks up the new file.
	_ = bootout(s.Kind)
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		return "", err
	}
	if err := bootstrap(path); err != nil {
		return path, err
	}
	return path, nil
}

// Uninstall unloads and removes the LaunchAgent plist.
func Uninstall(k Kind) error {
	path, err := plistPath(k)
	if err != nil {
		return err
	}
	_ = bootout(k)
	if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
		return err
	}
	return nil
}

// Status reports whether the LaunchAgent plist exists and is loaded.
func Status(k Kind) (Info, error) {
	path, err := plistPath(k)
	if err != nil {
		return Info{}, err
	}
	st := Info{Kind: k, Path: path}
	if _, err := os.Stat(path); err == nil {
		st.Installed = true
	} else if !os.IsNotExist(err) {
		return st, err
	}
	domain, err := guiDomain()
	if err != nil {
		return st, err
	}
	out, err := exec.Command("launchctl", "print", domain+"/"+k.Label()).CombinedOutput()
	detail := strings.TrimSpace(string(out))
	if err == nil {
		st.Loaded = true
		st.Detail = summarizeLaunchctl(detail)
	} else if st.Installed {
		st.Detail = "plist present but not loaded"
		if detail != "" {
			st.Detail += ": " + firstLine(detail)
		}
	} else {
		st.Detail = "not installed"
	}
	return st, nil
}

func bootout(k Kind) error {
	domain, err := guiDomain()
	if err != nil {
		return err
	}
	cmd := exec.Command("launchctl", "bootout", domain+"/"+k.Label())
	_ = cmd.Run() // ignore "no such service"
	return nil
}

func bootstrap(plist string) error {
	domain, err := guiDomain()
	if err != nil {
		return err
	}
	out, err := exec.Command("launchctl", "bootstrap", domain, plist).CombinedOutput()
	if err != nil {
		return fmt.Errorf("launchctl bootstrap: %w (%s)", err, strings.TrimSpace(string(out)))
	}
	return nil
}

func summarizeLaunchctl(detail string) string {
	pid := ""
	state := ""
	for _, line := range strings.Split(detail, "\n") {
		line = strings.TrimSpace(line)
		if strings.HasPrefix(line, "pid = ") {
			pid = strings.TrimPrefix(line, "pid = ")
		}
		if strings.HasPrefix(line, "state = ") {
			state = strings.TrimPrefix(line, "state = ")
		}
	}
	parts := []string{}
	if state != "" {
		parts = append(parts, "state="+state)
	}
	if pid != "" && pid != "0" {
		if _, err := strconv.Atoi(pid); err == nil {
			parts = append(parts, "pid="+pid)
		}
	}
	if len(parts) == 0 {
		return "loaded"
	}
	return strings.Join(parts, " ")
}

func firstLine(s string) string {
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		return s[:i]
	}
	return s
}
