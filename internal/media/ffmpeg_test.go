package media

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestNeedsRebuild(t *testing.T) {
	dir := t.TempDir()
	src := filepath.Join(dir, "src.wav")
	dst := filepath.Join(dir, "out.mp3")

	if err := os.WriteFile(src, []byte("source"), 0o644); err != nil {
		t.Fatal(err)
	}

	// dst missing -> rebuild
	if ok, err := NeedsRebuild(src, dst, false); err != nil || !ok {
		t.Fatalf("missing dst: got (%v, %v), want (true, <nil>)", ok, err)
	}

	// dst present and newer than src -> no rebuild
	if err := os.WriteFile(dst, []byte("output"), 0o644); err != nil {
		t.Fatal(err)
	}
	past := time.Now().Add(-time.Hour)
	if err := os.Chtimes(src, past, past); err != nil {
		t.Fatal(err)
	}
	if ok, err := NeedsRebuild(src, dst, false); err != nil || ok {
		t.Fatalf("fresh dst: got (%v, %v), want (false, <nil>)", ok, err)
	}

	// force -> rebuild even when up to date
	if ok, err := NeedsRebuild(src, dst, true); err != nil || !ok {
		t.Fatalf("force: got (%v, %v), want (true, <nil>)", ok, err)
	}

	// src newer than dst -> rebuild
	future := time.Now().Add(time.Hour)
	if err := os.Chtimes(src, future, future); err != nil {
		t.Fatal(err)
	}
	if ok, err := NeedsRebuild(src, dst, false); err != nil || !ok {
		t.Fatalf("stale dst: got (%v, %v), want (true, <nil>)", ok, err)
	}
}

func TestNeedsRebuildMissingSource(t *testing.T) {
	dir := t.TempDir()
	dst := filepath.Join(dir, "out.mp3")
	if err := os.WriteFile(dst, []byte("output"), 0o644); err != nil {
		t.Fatal(err)
	}
	// dst exists but src does not: stat(src) fails -> error, not a silent "up to date".
	if _, err := NeedsRebuild(filepath.Join(dir, "missing.wav"), dst, false); err == nil {
		t.Fatal("expected an error when the source file is missing")
	}
}

func TestRequireToolMissing(t *testing.T) {
	err := requireTool("techgo-definitely-not-a-real-binary-zzz")
	if err == nil {
		t.Fatal("expected an error for a non-existent tool")
	}
	if !strings.Contains(err.Error(), "techgo-definitely-not-a-real-binary-zzz") {
		t.Errorf("error %q should name the missing tool", err)
	}
	if !strings.Contains(err.Error(), "PATH") {
		t.Errorf("error %q should mention PATH", err)
	}
}
