package server

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

func TestSaveAndLoadFeishuAppFile(t *testing.T) {
	// Use a temp dir to avoid touching real credentials.
	dir := t.TempDir()
	orig := feishuAppPath
	defer func() { feishuAppPath = orig }()
	feishuAppPath = func() string {
		return filepath.Join(dir, "feishu_app.json")
	}

	// Initial load should return false.
	_, ok := loadFeishuAppFile()
	if ok {
		t.Fatal("expected no credentials before save")
	}

	// Save credentials.
	creds := FeishuCredentials{AppID: "cli_test123", AppSecret: "secret456"}
	saveFeishuAppFile(creds)

	// Verify file exists and is valid JSON.
	data, err := os.ReadFile(feishuAppPath())
	if err != nil {
		t.Fatalf("failed to read saved file: %v", err)
	}
	var decoded FeishuCredentials
	if err := json.Unmarshal(data, &decoded); err != nil {
		t.Fatalf("saved file is not valid JSON: %v", err)
	}
	if decoded.AppID != "cli_test123" || decoded.AppSecret != "secret456" {
		t.Errorf("decoded = %+v", decoded)
	}

	// Load should succeed.
	loaded, ok := loadFeishuAppFile()
	if !ok {
		t.Fatal("expected credentials after save")
	}
	if loaded.AppID != "cli_test123" || loaded.AppSecret != "secret456" {
		t.Errorf("loaded = %+v", loaded)
	}
}

func TestLoadFeishuAppFileEmptyFields(t *testing.T) {
	dir := t.TempDir()
	orig := feishuAppPath
	defer func() { feishuAppPath = orig }()
	feishuAppPath = func() string {
		return filepath.Join(dir, "feishu_app.json")
	}

	// Save empty credentials — load should return false.
	_ = os.WriteFile(feishuAppPath(), []byte(`{"app_id":"","app_secret":""}`), 0600)
	_, ok := loadFeishuAppFile()
	if ok {
		t.Fatal("empty credentials should not be considered valid")
	}
}

func TestLoadFeishuAppFileMissing(t *testing.T) {
	dir := t.TempDir()
	orig := feishuAppPath
	defer func() { feishuAppPath = orig }()
	feishuAppPath = func() string {
		return filepath.Join(dir, "nonexistent", "feishu_app.json")
	}

	_, ok := loadFeishuAppFile()
	if ok {
		t.Fatal("missing file should return false")
	}
}
