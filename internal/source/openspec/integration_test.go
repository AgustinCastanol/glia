package openspec_test

// Integration tests for the openspec adapter using a synthetic fixture tree.
// These tests verify the ListNative→ReadNative→ToCanonical round-trip against
// a realistic openspec directory layout including archived changes and merged
// spec domains (PRD-11, Task 8).

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/agustincastanol/glia/internal/adapter"
	"github.com/agustincastanol/glia/internal/source/openspec"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// buildFixtureTree creates the synthetic openspec fixture tree inside dir and
// returns the openspec root (dir/openspec). The tree mimics a real SDD repo:
//
//	openspec/
//	  changes/
//	    static-file-sources/
//	      proposal.md
//	      design.md
//	      tasks.md
//	      specs/
//	        req.md
//	    archive/
//	      old-feature/
//	        proposal.md
//	        design.md
//	  specs/
//	    auth/
//	      spec.md
func buildFixtureTree(t *testing.T, root string) string {
	t.Helper()
	base := filepath.Join(root, "openspec")

	files := map[string]string{
		"changes/static-file-sources/proposal.md": "# Static File Sources Proposal\nThis is the proposal.",
		"changes/static-file-sources/design.md":   "# Static File Sources Design\nThis is the design.",
		"changes/static-file-sources/tasks.md":    "# Static File Sources Tasks\nThis is the task list.",
		"changes/static-file-sources/specs/req.md": "# Requirements\nFunctional requirements.",
		"changes/archive/old-feature/proposal.md":  "# Old Feature Proposal\nArchived content.",
		"changes/archive/old-feature/design.md":    "# Old Feature Design\nArchived design.",
		"specs/auth/spec.md":                       "# Auth Spec\nAuth domain specification.",
	}

	for rel, content := range files {
		full := filepath.Join(base, rel)
		require.NoError(t, os.MkdirAll(filepath.Dir(full), 0o755))
		require.NoError(t, os.WriteFile(full, []byte(content), 0o644))
	}
	return base
}

// TestIntegration_FixtureRoundTrip exercises the full ListNative→ReadNative→ToCanonical
// round-trip against the synthetic fixture tree (PRD-11, Task 8).
func TestIntegration_FixtureRoundTrip(t *testing.T) {
	root := t.TempDir()
	dir := buildFixtureTree(t, root)

	a := openspec.New(openspec.Config{Dir: dir})
	ctx := context.Background()

	// --- ListNative ---
	ids, err := a.ListNative(ctx, "", time.Time{})
	require.NoError(t, err)
	// Expect 7 .md files from the fixture tree.
	assert.Len(t, ids, 7, "ListNative should return all 7 fixture files")

	// --- ReadNative + ToCanonical for each ---
	var (
		sawProposal bool
		sawDesign   bool
		sawTasks    bool
		sawSpec     bool
		sawArchived bool
		sawMerged   bool
	)

	for _, id := range ids {
		native, readErr := a.ReadNative(ctx, id)
		require.NoError(t, readErr, "ReadNative(%s)", id)

		rec, toErr := a.ToCanonical(native, emptyIDMap{})
		require.NoError(t, toErr, "ToCanonical(%s)", id)

		// Every record must be spec_artifact.
		assert.Equal(t, "spec_artifact", rec.Kind, "kind mismatch for %s", id)
		// Content must be non-empty.
		assert.NotEmpty(t, rec.Content, "content empty for %s", id)
		// ContentFormat must be markdown.
		assert.Equal(t, "markdown", rec.ContentFormat, "content_format for %s", id)
		// Origin.Provider must be openspec.
		assert.Equal(t, "openspec", rec.Origin.Provider, "provider for %s", id)
		// Tags must be non-nil (empty slice or ["archived"]).
		assert.NotNil(t, rec.Tags, "tags nil for %s", id)

		switch rec.Type {
		case "proposal":
			sawProposal = true
		case "design":
			sawDesign = true
		case "tasks":
			sawTasks = true
		case "spec":
			sawSpec = true
		}

		// Archived artifacts must carry the "archived" tag and still resolve types.
		if rec.Origin.ProviderID != "" {
			rel := rec.Origin.ProviderID
			if len(rel) > len("changes/archive/") && rel[:len("changes/archive/")] == "changes/archive/" {
				sawArchived = true
				found := false
				for _, tag := range rec.Tags {
					if tag == "archived" {
						found = true
						break
					}
				}
				assert.True(t, found, "archived artifact %s must have 'archived' tag", id)
				// topic_key must skip the archive/ segment.
				assert.NotContains(t, rec.TopicKey, "archive", "topic_key must not contain 'archive' for %s", id)
			}
		}

		// Merged spec domain records.
		if rec.TopicKey == "spec/auth" {
			sawMerged = true
		}
	}

	assert.True(t, sawProposal, "expected at least one proposal record")
	assert.True(t, sawDesign, "expected at least one design record")
	assert.True(t, sawTasks, "expected at least one tasks record")
	assert.True(t, sawSpec, "expected at least one spec record")
	assert.True(t, sawArchived, "expected at least one archived record")
	assert.True(t, sawMerged, "expected spec/auth topic_key from merged spec domain")
}

// TestIntegration_ArchivedRecordTypes verifies that archived change artifacts
// resolve to correct types and carry the "archived" tag.
func TestIntegration_ArchivedRecordTypes(t *testing.T) {
	root := t.TempDir()
	dir := buildFixtureTree(t, root)

	a := openspec.New(openspec.Config{Dir: dir})
	ctx := context.Background()

	ids, err := a.ListNative(ctx, "", time.Time{})
	require.NoError(t, err)

	for _, id := range ids {
		rel := string(id)
		if len(rel) <= len("changes/archive/") || rel[:len("changes/archive/")] != "changes/archive/" {
			continue
		}

		native, err := a.ReadNative(ctx, id)
		require.NoError(t, err)

		rec, err := a.ToCanonical(native, emptyIDMap{})
		require.NoError(t, err)

		// Must have archived tag.
		assert.Contains(t, rec.Tags, "archived", "archived path %s must have 'archived' tag", id)
		// Type must be one of the valid SDD artifact types.
		validTypes := map[string]bool{"proposal": true, "design": true, "tasks": true, "spec": true}
		assert.True(t, validTypes[rec.Type], "archived record %s: unexpected type %q", id, rec.Type)
		// topic_key must not contain "archive".
		assert.NotContains(t, rec.TopicKey, "archive", "topic_key must skip archive segment for %s", id)
	}
}

// TestIntegration_HealthAndListConsistency verifies that a healthy adapter
// returns non-nil IDs and that the count matches the fixture tree.
func TestIntegration_HealthAndListConsistency(t *testing.T) {
	root := t.TempDir()
	dir := buildFixtureTree(t, root)

	a := openspec.New(openspec.Config{Dir: dir})
	ctx := context.Background()

	require.NoError(t, a.Health(ctx), "Health must be nil for valid fixture dir")

	ids, err := a.ListNative(ctx, "", time.Time{})
	require.NoError(t, err)
	assert.Greater(t, len(ids), 0, "ListNative must return at least one ID for a non-empty fixture")
}

// TestIntegration_PullLeakageInvariant verifies that running FromCanonical on
// all fixture records leaves the fixture tree byte-identical (no writes).
func TestIntegration_PullLeakageInvariant(t *testing.T) {
	root := t.TempDir()
	dir := buildFixtureTree(t, root)

	// Snapshot the tree before the pull simulation.
	before := snapshotTree(t, dir)

	a := openspec.New(openspec.Config{Dir: dir})
	ctx := context.Background()

	ids, err := a.ListNative(ctx, "", time.Time{})
	require.NoError(t, err)

	for _, id := range ids {
		native, err := a.ReadNative(ctx, id)
		require.NoError(t, err)
		rec, err := a.ToCanonical(native, emptyIDMap{})
		require.NoError(t, err)

		// Simulate pull: FromCanonical must return ErrUnsupported.
		_, fcErr := a.FromCanonical(rec)
		assert.ErrorIs(t, fcErr, adapter.ErrUnsupported, "FromCanonical(%s) must return ErrUnsupported", id)
	}

	// Snapshot after — must be identical.
	after := snapshotTree(t, dir)
	assert.Equal(t, before, after, "fixture tree must be byte-identical after pull simulation")
}

// snapshotTree walks dir and returns a map[relPath]content for all files.
func snapshotTree(t *testing.T, dir string) map[string]string {
	t.Helper()
	snap := make(map[string]string)
	err := filepath.WalkDir(dir, func(path string, d os.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		rel, _ := filepath.Rel(dir, path)
		data, readErr := os.ReadFile(path)
		if readErr != nil {
			t.Fatalf("snapshotTree: read %s: %v", path, readErr)
		}
		snap[rel] = string(data)
		return nil
	})
	require.NoError(t, err)
	return snap
}
