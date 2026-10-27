package main

import (
	"os"
	"path/filepath"
	"testing"
)

func TestLoadHostnamePolicy(t *testing.T) {
	if _, err := loadHostnamePolicy(""); err == nil {
		t.Fatal("missing policy path accepted")
	}
	path := filepath.Join(t.TempDir(), "policy.json")
	if _, err := loadHostnamePolicy(path); err == nil {
		t.Fatal("missing policy file accepted")
	}
	for _, input := range []string{`{}`, `{"cutover":"invalid"}`} {
		if err := os.WriteFile(path, []byte(input), 0600); err != nil {
			t.Fatal(err)
		}
		if _, err := loadHostnamePolicy(path); err == nil {
			t.Fatalf("invalid startup policy accepted: %s", input)
		}
	}
	if err := os.WriteFile(path, []byte(`{"cutover":"2026-10-26T00:00:00Z","entries":[]}`), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := loadHostnamePolicy(path); err != nil {
		t.Fatalf("valid startup policy rejected: %v", err)
	}
}
