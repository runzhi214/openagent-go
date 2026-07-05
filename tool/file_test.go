package tool

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestReadFileWithinWorkspace(t *testing.T) {
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "test.txt"), []byte("hello"), 0644)

	r := NewReadFile(dir)
	out, err := r.Execute(context.Background(), []byte(`{"path":"test.txt"}`))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "hello") {
		t.Errorf("expected 'hello', got: %s", out)
	}
	t.Logf("✅ read file in workspace: %s", out)
}

func TestReadFileResolvesTraversal(t *testing.T) {
	dir := t.TempDir()
	r := NewReadFile(dir)

	// Traversal is resolved to an absolute path outside workspace.
	// The tool allows it; workspace boundary enforcement is the Approver's job.
	_, err := r.Execute(context.Background(), []byte(`{"path":"../etc/passwd"}`))
	if err != nil {
		// May fail if /etc/passwd doesn't exist or is unreadable on this system,
		// but should NOT be rejected by validatePath.
		if strings.Contains(err.Error(), "path outside workspace") {
			t.Errorf("boundary enforcement should be in approver, not validatePath: %v", err)
		} else {
			t.Logf("✅ traversal resolved (non-workspace, approver's job to reject): %v", err)
		}
	} else {
		t.Logf("✅ traversal resolved — file outside workspace was readable (approver would normally block this)")
	}
}

func TestReadFileBlocksAbsoluteOutsideWorkspace(t *testing.T) {
	dir := t.TempDir()
	r := NewReadFile(dir)

	_, err := r.Execute(context.Background(), []byte(`{"path":"/etc/passwd"}`))
	if err == nil {
		t.Error("absolute path outside workspace should be rejected!")
	} else {
		t.Logf("✅ absolute outside blocked: %v", err)
	}
}

func TestReadFileRejectsAbsolutePath(t *testing.T) {
	dir := t.TempDir()
	r := NewReadFile(dir)
	absPath := filepath.Join(dir, "test.txt")

	_, err := r.Execute(context.Background(), []byte(`{"path":"`+absPath+`"}`))
	if err == nil {
		t.Error("absolute path should be rejected")
	} else if !strings.Contains(err.Error(), "use a relative path") {
		t.Errorf("error should tell model to use relative path: %v", err)
	} else {
		t.Logf("✅ absolute rejected with guidance: %v", err)
	}
}

func TestReadFileNotFound(t *testing.T) {
	dir := t.TempDir()
	r := NewReadFile(dir)

	_, err := r.Execute(context.Background(), []byte(`{"path":"nonexistent.txt"}`))
	if err == nil {
		t.Error("missing file should return error")
	} else {
		t.Logf("✅ not found: %v", err)
	}
}

func TestWriteFileWithinWorkspace(t *testing.T) {
	dir := t.TempDir()
	w := NewWriteFile(dir)

	out, err := w.Execute(context.Background(), []byte(`{"path":"out.txt","content":"generated"}`))
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("✅ write: %s", out)

	// Verify file was created.
	data, _ := os.ReadFile(filepath.Join(dir, "out.txt"))
	if string(data) != "generated" {
		t.Errorf("file content mismatch: %q", string(data))
	}
}

func TestWriteFileResolvesTraversal(t *testing.T) {
	dir := t.TempDir()
	w := NewWriteFile(dir)

	_, err := w.Execute(context.Background(), []byte(`{"path":"../outside.txt","content":"evil"}`))
	if err != nil {
		if strings.Contains(err.Error(), "path outside workspace") {
			t.Errorf("boundary enforcement should be in approver, not validatePath: %v", err)
		} else {
			t.Logf("✅ traversal resolved (non-workspace, approver's job to reject): %v", err)
		}
	} else {
		t.Logf("✅ traversal resolved — wrote outside workspace (approver would normally block this)")
	}
}

func TestListDir(t *testing.T) {
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "a.txt"), []byte("a"), 0644)
	os.MkdirAll(filepath.Join(dir, "sub"), 0755)

	l := NewListDir(dir)
	out, err := l.Execute(context.Background(), []byte(`{}`))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "a.txt") || !strings.Contains(out, "sub/") {
		t.Errorf("expected a.txt and sub/ in output: %s", out)
	}
	t.Logf("✅ ls: %s", out)
}

func TestGrep(t *testing.T) {
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "a.go"), []byte("package main\nfunc main() {\n\tfmt.Println(\"hello\")\n}\n"), 0644)
	os.WriteFile(filepath.Join(dir, "b.go"), []byte("package main\nfunc helper() {}\n"), 0644)

	g := NewGrep(dir)

	// Find "main" in all files.
	out, err := g.Execute(context.Background(), []byte(`{"pattern":"main"}`))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "a.go") || !strings.Contains(out, "b.go") {
		t.Errorf("expected matches in a.go and b.go: %s", out)
	}
	t.Logf("✅ grep 'main':\n%s", out)

	// Glob filter — only *.go files.
	out, err = g.Execute(context.Background(), []byte(`{"pattern":"func","glob":"*.go"}`))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "func") {
		t.Errorf("expected func matches: %s", out)
	}
	t.Logf("✅ grep 'func' *.go:\n%s", out)

	// No matches.
	out, err = g.Execute(context.Background(), []byte(`{"pattern":"nonexistent"}`))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "No matches") {
		t.Errorf("expected no matches: %s", out)
	}
	t.Logf("✅ grep no match: %s", out)
}
