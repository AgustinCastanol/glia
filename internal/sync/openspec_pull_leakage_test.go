package sync

// TestPullLeakage_OpenspecDirByteIdenticalAfterPull is the engine-level
// integration test for the pull-leakage gate (Design D2, PRD-11).
//
// It seeds a canonical store with a spec_artifact record whose origin is
// "openspec", runs a full pull loop with a real openspec.Adapter, and then
// asserts that every file under the openspec/ fixture tree is byte-identical
// to what it was before the pull. WriteNative must never be reached because
// FromCanonical returns ErrUnsupported, which the pull loop handles via the
// silent-skip path (pull.go:84-86).

import (
	"context"
	"crypto/sha256"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/agustincastanol/glia/internal/adapter"
	"github.com/agustincastanol/glia/internal/source/openspec"
	"github.com/agustincastanol/glia/internal/store"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// dirChecksum computes a map[relPath]sha256hex for every file under root.
// Used to assert byte-identical directory state before and after a pull.
func dirChecksum(t *testing.T, root string) map[string]string {
	t.Helper()
	sums := make(map[string]string)
	err := filepath.WalkDir(root, func(path string, d os.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		rel, relErr := filepath.Rel(root, path)
		if relErr != nil {
			return relErr
		}
		f, openErr := os.Open(path)
		if openErr != nil {
			return openErr
		}
		defer f.Close()
		h := sha256.New()
		if _, copyErr := io.Copy(h, f); copyErr != nil {
			return copyErr
		}
		sums[rel] = fmt.Sprintf("%x", h.Sum(nil))
		return nil
	})
	require.NoError(t, err)
	return sums
}

// writeOpenspecFile creates a file under root with the given relative path and content.
func writeOpenspecFile(t *testing.T, root, rel, content string) {
	t.Helper()
	abs := filepath.Join(root, rel)
	require.NoError(t, os.MkdirAll(filepath.Dir(abs), 0o755))
	require.NoError(t, os.WriteFile(abs, []byte(content), 0o644))
}

func TestPullLeakage_OpenspecDirByteIdenticalAfterPull(t *testing.T) {
	// 1. Set up a temporary openspec directory with fixture files.
	openspecDir := t.TempDir()
	writeOpenspecFile(t, openspecDir, "changes/auth/design.md", "# Auth Design\nsome content here")
	writeOpenspecFile(t, openspecDir, "changes/auth/tasks.md", "# Auth Tasks\ntask list")
	writeOpenspecFile(t, openspecDir, "specs/auth/spec.md", "# Auth Spec\nspec content")

	// 2. Record the byte-level checksum of the directory BEFORE the pull.
	beforeChecksums := dirChecksum(t, openspecDir)
	require.Len(t, beforeChecksums, 3, "fixture should have 3 files")

	// 3. Open a canonical store and seed it with a spec_artifact record
	//    whose origin is "openspec". This simulates a store that already has
	//    an ingested artifact — the pull loop will try to push it back.
	storeDir := filepath.Join(t.TempDir(), ".glia")
	s, err := store.Open(storeDir)
	require.NoError(t, err)
	t.Cleanup(func() { _ = s.Close() })

	specRecord := store.CanonicalRecord{
		Kind:          "spec_artifact",
		Type:          "design",
		Title:         "Auth Design",
		Content:       "# Auth Design\nsome content here",
		ContentFormat: "markdown",
		TopicKey:      "sdd/auth/design",
		CreatedAt:     time.Now().UTC().Format(time.RFC3339),
		UpdatedAt:     time.Now().UTC().Format(time.RFC3339),
		Tags:          []string{},
		Origin: store.Origin{
			Provider:   "openspec",
			ProviderID: "changes/auth/design.md",
		},
	}
	_, appendErr := s.AppendBatch([]store.CanonicalRecord{specRecord})
	require.NoError(t, appendErr, "seeding spec_artifact record into store")

	// 4. Build the engine with the real openspec adapter.
	a := openspec.New(openspec.Config{Dir: openspecDir})
	adapters := map[string]adapter.Adapter{
		"openspec": a,
	}
	engine := New(s, adapters, Default(), Options{Project: "test-project"}, io.Discard)

	// 5. Run a full pull. The pull loop should encounter ErrUnsupported from
	//    FromCanonical and silently skip — never reaching WriteNative.
	_, pullErr := engine.Pull(context.Background())
	require.NoError(t, pullErr, "Pull must not return an error for a read-only adapter")

	// 6. Assert the openspec directory is byte-identical after the pull.
	afterChecksums := dirChecksum(t, openspecDir)
	assert.Equal(t, beforeChecksums, afterChecksums,
		"openspec/ directory must be byte-identical before and after pull (pull-leakage gate)")
}
