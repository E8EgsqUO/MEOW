package main

import (
	"testing"
	"time"
)

func TestConfigParseHelpers(t *testing.T) {
	if !parseBool("true", "x") || parseBool("false", "x") {
		t.Fatal("parseBool")
	}
	if parseInt("42", "x") != 42 {
		t.Fatal("parseInt")
	}
	if parseDuration("1500ms", "x") != 1500*time.Millisecond {
		t.Fatal("parseDuration")
	}
	for _, tc := range []struct {
		v  string
		ok bool
	}{
		{"user:pass", true},
		{"user:", false},
		{":pass", false},
		{"noseparator", false},
	} {
		if got := isUserPasswdValid(tc.v); got != tc.ok {
			t.Errorf("isUserPasswdValid(%q) = %v, want %v", tc.v, got, tc.ok)
		}
	}
}

func TestProxyParserAddsParents(t *testing.T) {
	saved := parentProxy
	defer func() { parentProxy = saved }()
	pool := &backupParentPool{}
	parentProxy = pool

	var p proxyParser
	p.ProxySocks5("127.0.0.1:1080")
	p.ProxyHttp("user:pass@127.0.0.1:8080")
	if len(pool.parent) != 2 {
		t.Fatalf("added %d parents, want 2", len(pool.parent))
	}
	statuses := parentProxyStatuses()
	if len(statuses) != 2 || statuses[0].Server == "" {
		t.Fatalf("unexpected parent statuses: %+v", statuses)
	}
}
