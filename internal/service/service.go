package service

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

const (
	LabelAgent  = "dev.nubilo.agent"
	LabelServer = "dev.nubilo.server"
	UnitAgent   = "nubilo-agent.service"
	UnitServer  = "nubilo-server.service"
)

// Kind selects which always-on process to manage.
type Kind int

const (
	KindAgent Kind = iota
	KindServer
)

func (k Kind) Label() string {
	if k == KindAgent {
		return LabelAgent
	}
	return LabelServer
}

func (k Kind) UnitName() string {
	if k == KindAgent {
		return UnitAgent
	}
	return UnitServer
}

func (k Kind) LogFileName() string {
	if k == KindAgent {
		return "agent.log"
	}
	return "server.log"
}

func (k Kind) String() string {
	if k == KindAgent {
		return "agent"
	}
	return "server"
}

// Spec describes a user-level always-on install.
type Spec struct {
	Kind     Kind
	Exe      string // absolute path to nubilo binary
	DataDir  string // absolute data directory
	Insecure bool   // agent only: pass --insecure
}

// Info is a human-readable snapshot of the installed service.
type Info struct {
	Kind      Kind
	Installed bool
	Loaded    bool
	Path      string // plist or unit file path
	Detail    string // platform-specific status text
}

// ResolveExe returns an absolute path to the running executable.
func ResolveExe() (string, error) {
	exe, err := os.Executable()
	if err != nil {
		return "", err
	}
	return filepath.EvalSymlinks(exe)
}

// LogPath is $dataDir/logs/{agent,server}.log.
func LogPath(dataDir string, k Kind) string {
	return filepath.Join(dataDir, "logs", k.LogFileName())
}

// ProgramArgs builds argv for the service (excluding argv0 / Exe).
func ProgramArgs(s Spec) []string {
	args := []string{}
	switch s.Kind {
	case KindAgent:
		args = append(args, "agent", "run", "--data-dir", s.DataDir)
		if s.Insecure {
			args = append(args, "--insecure")
		}
	case KindServer:
		args = append(args, "server", "--data-dir", s.DataDir)
	}
	return args
}

// ValidateSpec checks required absolute paths.
func ValidateSpec(s Spec) error {
	if s.Exe == "" || !filepath.IsAbs(s.Exe) {
		return fmt.Errorf("service: executable must be an absolute path")
	}
	if s.DataDir == "" || !filepath.IsAbs(s.DataDir) {
		return fmt.Errorf("service: data-dir must be an absolute path")
	}
	return nil
}

// RenderPlist builds a macOS LaunchAgent plist for s.
func RenderPlist(s Spec) (string, error) {
	if err := ValidateSpec(s); err != nil {
		return "", err
	}
	log := LogPath(s.DataDir, s.Kind)
	var b strings.Builder
	b.WriteString(`<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0">
<dict>
	<key>Label</key>
	<string>`)
	b.WriteString(escapeXML(s.Kind.Label()))
	b.WriteString(`</string>
	<key>ProgramArguments</key>
	<array>
		<string>`)
	b.WriteString(escapeXML(s.Exe))
	b.WriteString(`</string>
`)
	for _, a := range ProgramArgs(s) {
		b.WriteString("\t\t<string>")
		b.WriteString(escapeXML(a))
		b.WriteString("</string>\n")
	}
	b.WriteString(`	</array>
	<key>RunAtLoad</key>
	<true/>
	<key>KeepAlive</key>
	<true/>
	<key>StandardOutPath</key>
	<string>`)
	b.WriteString(escapeXML(log))
	b.WriteString(`</string>
	<key>StandardErrorPath</key>
	<string>`)
	b.WriteString(escapeXML(log))
	b.WriteString(`</string>
</dict>
</plist>
`)
	return b.String(), nil
}

// RenderSystemdUnit builds a systemd --user unit for s.
func RenderSystemdUnit(s Spec) (string, error) {
	if err := ValidateSpec(s); err != nil {
		return "", err
	}
	log := LogPath(s.DataDir, s.Kind)
	args := append([]string{s.Exe}, ProgramArgs(s)...)
	execStart := shellJoin(args)
	desc := "Nubilo " + s.Kind.String()
	var b strings.Builder
	b.WriteString("[Unit]\n")
	b.WriteString("Description=")
	b.WriteString(desc)
	b.WriteString("\n")
	b.WriteString("After=network-online.target\n")
	b.WriteString("Wants=network-online.target\n\n")
	b.WriteString("[Service]\n")
	b.WriteString("Type=simple\n")
	b.WriteString("ExecStart=")
	b.WriteString(execStart)
	b.WriteString("\n")
	b.WriteString("Restart=on-failure\n")
	b.WriteString("RestartSec=5\n")
	b.WriteString("StandardOutput=append:")
	b.WriteString(log)
	b.WriteString("\n")
	b.WriteString("StandardError=append:")
	b.WriteString(log)
	b.WriteString("\n\n")
	b.WriteString("[Install]\n")
	b.WriteString("WantedBy=default.target\n")
	return b.String(), nil
}

func escapeXML(s string) string {
	r := strings.NewReplacer(
		"&", "&amp;",
		"<", "&lt;",
		">", "&gt;",
		`"`, "&quot;",
		"'", "&apos;",
	)
	return r.Replace(s)
}

// shellJoin quotes args for a systemd ExecStart line.
func shellJoin(args []string) string {
	parts := make([]string, len(args))
	for i, a := range args {
		if a == "" || strings.ContainsAny(a, " \t\n\"'\\$`") {
			parts[i] = `"` + strings.ReplaceAll(a, `"`, `\"`) + `"`
		} else {
			parts[i] = a
		}
	}
	return strings.Join(parts, " ")
}

// EnsureLogDir creates $dataDir/logs with 0700.
func EnsureLogDir(dataDir string) error {
	return os.MkdirAll(filepath.Join(dataDir, "logs"), 0o700)
}
