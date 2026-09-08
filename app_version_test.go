package main

import (
	"encoding/json"
	"os"
	"testing"
)

// TestAppVersionMatchesManifest — the log line "warpseed <v> starting" is the
// main evidence for which build a tester's bug came from. It used to be a
// hand-kept constant and drifted: every build from 1.1.1 to 1.1.7 logged
// 1.1.1. This pins it to the manifest the release workflow checks against the
// tag, so the two can never disagree again.
func TestAppVersionMatchesManifest(t *testing.T) {
	// Arrange
	raw, err := os.ReadFile("wails.json")
	if err != nil {
		t.Fatalf("read manifest: %v", err)
	}
	var m struct {
		Info struct {
			ProductVersion string `json:"productVersion"`
		} `json:"info"`
	}
	if err := json.Unmarshal(raw, &m); err != nil {
		t.Fatalf("parse manifest: %v", err)
	}

	// Assert
	if m.Info.ProductVersion == "" {
		t.Fatal("wails.json has no info.productVersion")
	}
	if appVersion != m.Info.ProductVersion {
		t.Fatalf("appVersion = %q, wails.json = %q", appVersion, m.Info.ProductVersion)
	}
	if appVersion == "unknown" {
		t.Fatal("appVersion fell back to \"unknown\": the embed or the parse is broken")
	}
}
