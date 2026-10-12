package config

import "testing"

func TestCodexFilesPrototypeConfigOptIn(t *testing.T) {
	cfg, err := ParseConfigBytes([]byte("multimedia:\n  codex-files-prototype:\n    enabled: true\n    max-bytes: 1234\n    max-files: 3\n    ttl-seconds: 30\n"))
	if err != nil {
		t.Fatal(err)
	}
	if !cfg.CodexFilesPrototype.Enabled {
		t.Fatal("v8 opt-in not parsed")
	}
	size, count, ttl := cfg.CodexFilesPrototype.Limits()
	if size != 1234 || count != 3 || ttl != 30 {
		t.Fatalf("limits=%d %d %d", size, count, ttl)
	}
	var defaults CodexFilesPrototypeConfig
	if defaults.Enabled {
		t.Fatal("enabled by default")
	}
	size, count, ttl = defaults.Limits()
	if size != 5*1024*1024 || count != 128 || ttl != 3600 {
		t.Fatal("defaults")
	}
}
