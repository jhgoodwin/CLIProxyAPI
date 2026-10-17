package config

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"
)

func TestSharedFilesV8ConfigRoundTrip(t *testing.T) {
	cfg, err := ParseConfigBytes([]byte("multimedia:\n  files:\n    enabled: true\n    storage-dir: persistent-files\n    max-files: 17\n    max-storage-bytes: 123456\n    cleanup-interval-seconds: 60\n"))
	if err != nil {
		t.Fatal(err)
	}
	if !cfg.Files.Enabled || cfg.Files.StorageDir != "persistent-files" || cfg.Files.MaxFiles != 17 || cfg.Files.MaxStorageBytes != 123456 || cfg.Files.CleanupIntervalSeconds != 60 {
		t.Fatalf("Files config not projected: %+v", cfg.Files)
	}
	path := filepath.Join(t.TempDir(), "config.yaml")
	if err = os.WriteFile(path, []byte("multimedia:\n  files:\n    enabled: true\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if err = SaveConfigPreserveComments(path, cfg); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(data, []byte("multimedia:")) || !bytes.Contains(data, []byte("files:")) || bytes.Contains(data, []byte("anthropic:")) {
		t.Fatalf("unexpected layout: %s", data)
	}
	again, err := ParseConfigBytes(data)
	if err != nil || again.Files != cfg.Files {
		t.Fatalf("round trip: %+v %v", again, err)
	}
	disabled, err := ParseConfigBytes([]byte("server:\n  port: 0\n"))
	if err != nil || disabled.Files.Enabled {
		t.Fatalf("default enabled: %+v %v", disabled, err)
	}
}
