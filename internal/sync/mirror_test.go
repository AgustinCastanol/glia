package sync

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/agustincastanol/glia/internal/adapter"
	"github.com/agustincastanol/glia/internal/store"
)

// writeFakeEngram writes a shell script named "engram" to dir and prepends dir
// to PATH, returning a restore function.
func writeFakeEngram(t *testing.T, script string) func() {
	t.Helper()
	dir := t.TempDir()
	engPath := filepath.Join(dir, "engram")
	if err := os.WriteFile(engPath, []byte("#!/bin/sh\n"+script+"\n"), 0755); err != nil {
		t.Fatalf("write fake engram: %v", err)
	}
	orig := os.Getenv("PATH")
	os.Setenv("PATH", dir+":"+orig)
	return func() { os.Setenv("PATH", orig) }
}

// openMirrorStore opens a temp store for mirror tests.
func openMirrorStore(t *testing.T) *store.Store {
	t.Helper()
	dir := t.TempDir()
	s, err := store.Open(filepath.Join(dir, ".glia"))
	if err != nil {
		t.Fatalf("store.Open: %v", err)
	}
	t.Cleanup(func() { _ = s.Close() })
	return s
}

func TestMirrorEngram_Success(t *testing.T) {
	restore := writeFakeEngram(t, "exit 0")
	defer restore()

	s := openMirrorStore(t)
	var buf bytes.Buffer
	e := New(s, nil, Default(), Options{MirrorEngram: true}, &buf)

	e.mirrorEngram("testproject")

	if buf.Len() != 0 {
		t.Errorf("unexpected output on success: %q", buf.String())
	}
}

func TestMirrorEngram_NonZeroExitWarnsOnly(t *testing.T) {
	restore := writeFakeEngram(t, "echo 'some error' >&2; exit 1")
	defer restore()

	s := openMirrorStore(t)
	var buf bytes.Buffer
	e := New(s, nil, Default(), Options{MirrorEngram: true}, &buf)

	e.mirrorEngram("testproject")

	out := buf.String()
	if len(out) == 0 {
		t.Error("expected WARN output for non-zero exit, got none")
	}
	if out[:4] != "WARN" {
		t.Errorf("expected output to start with WARN, got %q", out)
	}
}

func TestMirrorEngram_NotOnPath(t *testing.T) {
	// Ensure engram is NOT on PATH by pointing to an empty dir.
	emptyDir := t.TempDir()
	orig := os.Getenv("PATH")
	os.Setenv("PATH", emptyDir)
	defer os.Setenv("PATH", orig)

	s := openMirrorStore(t)
	var buf bytes.Buffer
	e := New(s, nil, Default(), Options{MirrorEngram: true}, &buf)

	e.mirrorEngram("testproject")

	out := buf.String()
	if len(out) == 0 {
		t.Error("expected WARN output for missing binary, got none")
	}
}

func TestMirrorEngram_Timeout(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping timeout test in short mode")
	}

	// Script loops tightly so that SIGKILL to the process group kills it fast.
	restore := writeFakeEngram(t, "while true; do :; done")
	defer restore()

	s := openMirrorStore(t)
	var buf bytes.Buffer

	cfg := Default()
	cfg.MirrorTimeoutSeconds = 1 // very short timeout for the test
	e := New(s, nil, cfg, Options{MirrorEngram: true}, &buf)

	start := time.Now()
	e.mirrorEngram("testproject")
	elapsed := time.Since(start)

	if elapsed > 10*time.Second {
		t.Errorf("mirror did not time out quickly: elapsed=%v", elapsed)
	}

	out := buf.String()
	if len(out) == 0 {
		t.Error("expected WARN output for timeout, got none")
	}
}

func TestMirrorEngramEnabled(t *testing.T) {
	s := openMirrorStore(t)

	cases := []struct {
		cfgMirror  bool
		optsMirror bool
		want       bool
	}{
		{false, false, false},
		{true, false, true},
		{false, true, true},
		{true, true, true},
	}

	for _, tc := range cases {
		cfg := Default()
		cfg.MirrorEngram = tc.cfgMirror
		e := New(s, map[string]adapter.Adapter{}, cfg, Options{MirrorEngram: tc.optsMirror}, nil)
		if got := e.mirrorEngramEnabled(); got != tc.want {
			t.Errorf("cfg.Mirror=%v opts.Mirror=%v: enabled=%v, want %v",
				tc.cfgMirror, tc.optsMirror, got, tc.want)
		}
	}
}

// minimalAdapter satisfies adapter.Adapter for map construction in non-push/pull tests.
type minimalAdapter struct{ name string }

func (a *minimalAdapter) Name() string                         { return a.name }
func (a *minimalAdapter) Health(_ context.Context) error       { return nil }
func (a *minimalAdapter) SupportedKinds() []string             { return nil }
func (a *minimalAdapter) ListNative(_ context.Context, _ string, _ time.Time) ([]adapter.NativeID, error) {
	return nil, nil
}
func (a *minimalAdapter) ReadNative(_ context.Context, _ adapter.NativeID) (adapter.NativeRecord, error) {
	return nil, nil
}
func (a *minimalAdapter) ToCanonical(_ adapter.NativeRecord, _ adapter.IDMap) (store.CanonicalRecord, error) {
	return store.CanonicalRecord{}, adapter.ErrUnsupported
}
func (a *minimalAdapter) FromCanonical(_ store.CanonicalRecord) (adapter.NativeRecord, error) {
	return nil, adapter.ErrUnsupported
}
func (a *minimalAdapter) WriteNative(_ context.Context, _ adapter.NativeRecord) (adapter.NativeID, error) {
	return "", adapter.ErrUnsupported
}

func (a *minimalAdapter) WriteCapability() string { return "read+write" }
