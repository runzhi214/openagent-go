package native

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	openagent "github.com/yusheng-g/openagent-go"
)

func TestSandboxWorkspaceAccess(t *testing.T) {
	dir := t.TempDir()
	sb, err := New(dir)
	if err != nil {
		t.Fatal(err)
	}

	// Write a file inside the workspace.
	if err := os.WriteFile(filepath.Join(dir, "hello.txt"), []byte("hello sandbox"), 0644); err != nil {
		t.Fatal(err)
	}

	// Shell command should be able to read files inside workspace.
	result, err := sb.Run(context.Background(), openagent.Command{
		Program: "/bin/bash",
		Args:    []string{"-c", "cat hello.txt"},
		WorkDir: dir,
	})
	if err != nil {
		t.Fatalf("failed to run: %v", err)
	}
	if !strings.Contains(result.Stdout, "hello sandbox") {
		t.Errorf("expected 'hello sandbox' in stdout, got: %s", result.Stdout)
	}
	t.Logf("✅ workspace read works: stdout=%q exit=%d", result.Stdout, result.ExitCode)
}

func TestSandboxBlocksExternalAccess(t *testing.T) {
	dir := t.TempDir()
	sb, err := New(dir)
	if err != nil {
		t.Fatal(err)
	}

	// Trying to read /etc/passwd should fail in the sandbox.
	result, _ := sb.Run(context.Background(), openagent.Command{
		Program: "/bin/bash",
		Args:    []string{"-c", "cat /etc/passwd 2>&1 || true"},
		WorkDir: dir,
	})
	// The sandbox should prevent accessing /etc/passwd.
	// Either the command fails (exit != 0) or returns empty/permission-denied output.
	if result.ExitCode == 0 && strings.Contains(result.Stdout, "root:") {
		t.Errorf("sandbox did NOT block access to /etc/passwd! stdout: %s", result.Stdout)
	} else {
		t.Logf("✅ external access blocked: exit=%d stderr=%q", result.ExitCode, result.Stderr)
	}
}

func TestSandboxNoNetwork(t *testing.T) {
	dir := t.TempDir()
	sb, err := New(dir)
	if err != nil {
		t.Fatal(err)
	}

	// Network access should be denied.
	result, _ := sb.Run(context.Background(), openagent.Command{
		Program: "/bin/bash",
		Args:    []string{"-c", "curl -s --connect-timeout 2 https://example.com 2>&1 || ping -c 1 -W 2 8.8.8.8 2>&1 || true"},
		WorkDir: dir,
	})
	// Network should be blocked — either by sandbox policy or by missing tools.
	if strings.Contains(result.Stdout, "Example Domain") || strings.Contains(result.Stdout, "1 packets transmitted") {
		t.Errorf("sandbox did NOT block network access! stdout: %s", result.Stdout)
	} else {
		t.Logf("✅ network blocked: exit=%d", result.ExitCode)
	}
}

func TestSandboxStreaming(t *testing.T) {
	dir := t.TempDir()
	sb, err := New(dir)
	if err != nil {
		t.Fatal(err)
	}

	ch := sb.RunStream(context.Background(), openagent.Command{
		Program: "/bin/bash",
		Args:    []string{"-c", "echo line1; sleep 0.1; echo line2"},
		WorkDir: dir,
	})

	var lines []string
	for chunk := range ch {
		if chunk.Error != nil {
			t.Fatalf("stream error: %v", chunk.Error)
		}
		lines = append(lines, strings.TrimSpace(chunk.Content))
	}
	if len(lines) < 2 {
		t.Errorf("expected at least 2 lines from streaming, got %d: %v", len(lines), lines)
	} else {
		t.Logf("✅ streaming works: %d lines", len(lines))
	}
}
