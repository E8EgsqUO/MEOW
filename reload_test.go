package main

import (
	"os"
	"path/filepath"
	"testing"
)

func TestFileSignatureTracksChanges(t *testing.T) {
	p := filepath.Join(t.TempDir(), "rc")
	if err := os.WriteFile(p, []byte("listen = http://127.0.0.1:4411\n"), 0644); err != nil {
		t.Fatal(err)
	}
	sig1 := fileSignature(p)
	if sig1 == "" {
		t.Fatal("empty signature for an existing file")
	}
	if fileSignature(p) != sig1 {
		t.Fatal("signature changed without the file changing")
	}
	if err := os.WriteFile(p, []byte("listen = http://127.0.0.1:4412\njudgeByIP = false\n"), 0644); err != nil {
		t.Fatal(err)
	}
	if fileSignature(p) == sig1 {
		t.Fatal("signature did not change after a rewrite")
	}
	if fileSignature(filepath.Join(t.TempDir(), "absent")) != "" {
		t.Fatal("missing file should have an empty signature")
	}
}

func TestReloadArgsStripsCheckConfig(t *testing.T) {
	saved := os.Args
	defer func() { os.Args = saved }()
	os.Args = []string{"meow", "-rc", "/etc/meow/rc", "-check-config", "-debug"}
	got := reloadArgs()
	want := []string{"-rc", "/etc/meow/rc", "-debug"}
	if len(got) != len(want) {
		t.Fatalf("reloadArgs() = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("reloadArgs()[%d] = %q, want %q", i, got[i], want[i])
		}
	}
	if !isConfigCheckRun() {
		t.Fatal("isConfigCheckRun did not see -check-config")
	}
	os.Args = []string{"meow", "-rc", "x"}
	if isConfigCheckRun() {
		t.Fatal("isConfigCheckRun false positive")
	}
}
