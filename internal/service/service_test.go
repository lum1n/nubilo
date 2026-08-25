package service

import (
	"strings"
	"testing"
)

func TestRenderPlistAgent(t *testing.T) {
	s := Spec{
		Kind:     KindAgent,
		Exe:      "/Applications/Nubilo.app/Contents/MacOS/nubilo",
		DataDir:  "/Users/me/.nubilo-agent",
		Insecure: true,
	}
	got, err := RenderPlist(s)
	if err != nil {
		t.Fatal(err)
	}
	want := []string{
		"<string>dev.nubilo.agent</string>",
		"<string>/Applications/Nubilo.app/Contents/MacOS/nubilo</string>",
		"<string>agent</string>",
		"<string>run</string>",
		"<string>--data-dir</string>",
		"<string>/Users/me/.nubilo-agent</string>",
		"<string>--insecure</string>",
		"<string>/Users/me/.nubilo-agent/logs/agent.log</string>",
		"<key>KeepAlive</key>",
		"<true/>",
	}
	for _, w := range want {
		if !strings.Contains(got, w) {
			t.Fatalf("missing %q in:\n%s", w, got)
		}
	}
}

func TestRenderPlistServer(t *testing.T) {
	s := Spec{Kind: KindServer, Exe: "/usr/local/bin/nubilo", DataDir: "/var/nubilo"}
	got, err := RenderPlist(s)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(got, "--insecure") || strings.Contains(got, "<string>agent</string>") {
		t.Fatalf("server plist should not mention agent/insecure:\n%s", got)
	}
	want := []string{
		"dev.nubilo.server",
		"<string>server</string>",
		"<string>--data-dir</string>",
		"/var/nubilo/logs/server.log",
	}
	for _, w := range want {
		if !strings.Contains(got, w) {
			t.Fatalf("missing %q in:\n%s", w, got)
		}
	}
}

func TestRenderSystemdUnit(t *testing.T) {
	s := Spec{Kind: KindServer, Exe: "/home/u/bin/nubilo", DataDir: "/home/u/.nubilo"}
	got, err := RenderSystemdUnit(s)
	if err != nil {
		t.Fatal(err)
	}
	want := []string{
		"Description=Nubilo server",
		`ExecStart=/home/u/bin/nubilo server --data-dir /home/u/.nubilo`,
		"Restart=on-failure",
		"StandardOutput=append:/home/u/.nubilo/logs/server.log",
		"WantedBy=default.target",
	}
	for _, w := range want {
		if !strings.Contains(got, w) {
			t.Fatalf("missing %q in:\n%s", w, got)
		}
	}
}

func TestRenderSystemdUnitQuotesSpaces(t *testing.T) {
	s := Spec{Kind: KindServer, Exe: "/opt/my bin/nubilo", DataDir: "/home/u/My Data"}
	got, err := RenderSystemdUnit(s)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(got, `"/opt/my bin/nubilo"`) || !strings.Contains(got, `"/home/u/My Data"`) {
		t.Fatalf("expected quoted paths:\n%s", got)
	}
}

func TestValidateSpec(t *testing.T) {
	if err := ValidateSpec(Spec{Kind: KindServer, Exe: "rel", DataDir: "/abs"}); err == nil {
		t.Fatal("expected error for relative exe")
	}
	if err := ValidateSpec(Spec{Kind: KindServer, Exe: "/abs", DataDir: "rel"}); err == nil {
		t.Fatal("expected error for relative data-dir")
	}
}

func TestProgramArgs(t *testing.T) {
	a := ProgramArgs(Spec{Kind: KindAgent, DataDir: "/d", Insecure: false})
	if len(a) != 4 || a[0] != "agent" || a[1] != "run" {
		t.Fatalf("%v", a)
	}
	s := ProgramArgs(Spec{Kind: KindServer, DataDir: "/d"})
	if len(s) != 3 || s[0] != "server" {
		t.Fatalf("%v", s)
	}
}

func TestEscapeXML(t *testing.T) {
	got, err := RenderPlist(Spec{Kind: KindServer, Exe: "/bin/a&b", DataDir: "/tmp/<x>"})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(got, "/bin/a&amp;b") || !strings.Contains(got, "/tmp/&lt;x&gt;") {
		t.Fatalf("xml escape failed:\n%s", got)
	}
}
