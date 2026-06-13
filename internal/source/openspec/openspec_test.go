package openspec_test

import (
	"context"
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

// ---------------------------------------------------------------------------
// Helpers
// ---------------------------------------------------------------------------

// newAdapter creates an Adapter pointing at a fresh temp directory.
func newAdapter(t *testing.T, dir string) *openspec.Adapter {
	t.Helper()
	return openspec.New(openspec.Config{Dir: dir})
}

// writeFile creates a file and all parent directories in the given root.
func writeFile(t *testing.T, root, rel, content string) {
	t.Helper()
	full := filepath.Join(root, rel)
	require.NoError(t, os.MkdirAll(filepath.Dir(full), 0o755))
	require.NoError(t, os.WriteFile(full, []byte(content), 0o644))
}

// emptyIDMap satisfies adapter.IDMap with no mappings.
type emptyIDMap struct{}

func (emptyIDMap) CanonicalFromNative(adapter.NativeID) (adapter.CanonicalID, bool) {
	return "", false
}
func (emptyIDMap) NativeFromCanonical(adapter.CanonicalID) (adapter.NativeID, bool) {
	return "", false
}

// ---------------------------------------------------------------------------
// Interface contract tests
// ---------------------------------------------------------------------------

func TestName(t *testing.T) {
	a := newAdapter(t, t.TempDir())
	assert.Equal(t, "openspec", a.Name())
}

func TestSupportedKinds(t *testing.T) {
	a := newAdapter(t, t.TempDir())
	assert.Equal(t, []string{"spec_artifact"}, a.SupportedKinds())
}

func TestWriteCapability(t *testing.T) {
	a := newAdapter(t, t.TempDir())
	assert.Equal(t, "read-only", a.WriteCapability())
}

// ---------------------------------------------------------------------------
// Pull-leakage gate: FromCanonical and WriteNative must return ErrUnsupported
// ---------------------------------------------------------------------------

func TestFromCanonical_ReturnsErrUnsupported(t *testing.T) {
	a := newAdapter(t, t.TempDir())
	_, err := a.FromCanonical(store.CanonicalRecord{})
	assert.ErrorIs(t, err, adapter.ErrUnsupported)
}

func TestWriteNative_ReturnsErrUnsupported(t *testing.T) {
	a := newAdapter(t, t.TempDir())
	_, err := a.WriteNative(context.Background(), nil)
	assert.ErrorIs(t, err, adapter.ErrUnsupported)
}

// ---------------------------------------------------------------------------
// Health
// ---------------------------------------------------------------------------

func TestHealth_DirExists_ReturnsNil(t *testing.T) {
	dir := t.TempDir()
	a := newAdapter(t, dir)
	assert.NoError(t, a.Health(context.Background()))
}

func TestHealth_DirMissing_ReturnsErrUnavailable(t *testing.T) {
	a := newAdapter(t, "/definitely/does/not/exist/openspec-xyz")
	err := a.Health(context.Background())
	assert.ErrorIs(t, err, adapter.ErrUnavailable)
}

// ---------------------------------------------------------------------------
// ListNative
// ---------------------------------------------------------------------------

func TestListNative_EmptyDir_ReturnsEmptySlice(t *testing.T) {
	dir := t.TempDir()
	a := newAdapter(t, dir)
	ids, err := a.ListNative(context.Background(), "proj", time.Time{})
	require.NoError(t, err)
	assert.Empty(t, ids)
}

func TestListNative_SingleChangeWithDesignAndTasks(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, dir, "changes/auth/design.md", "# Auth Design\ncontent")
	writeFile(t, dir, "changes/auth/tasks.md", "# Auth Tasks\ncontent")

	a := newAdapter(t, dir)
	ids, err := a.ListNative(context.Background(), "proj", time.Time{})
	require.NoError(t, err)
	assert.Len(t, ids, 2)
}

func TestListNative_StateYamlSkipped(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, dir, "changes/auth/design.md", "# Design\ncontent")
	writeFile(t, dir, "changes/auth/state.yaml", "phase: done")

	a := newAdapter(t, dir)
	ids, err := a.ListNative(context.Background(), "proj", time.Time{})
	require.NoError(t, err)
	// state.yaml must be skipped
	assert.Len(t, ids, 1)
}

func TestListNative_DeterministicOrder(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, dir, "changes/beta/design.md", "content")
	writeFile(t, dir, "changes/alpha/design.md", "content")
	writeFile(t, dir, "changes/alpha/tasks.md", "content")

	a := newAdapter(t, dir)
	ids, err := a.ListNative(context.Background(), "proj", time.Time{})
	require.NoError(t, err)
	require.Len(t, ids, 3)

	// Must be sorted: alpha/design, alpha/tasks, beta/design
	assert.Equal(t, adapter.NativeID("changes/alpha/design.md"), ids[0])
	assert.Equal(t, adapter.NativeID("changes/alpha/tasks.md"), ids[1])
	assert.Equal(t, adapter.NativeID("changes/beta/design.md"), ids[2])
}

func TestListNative_MergedSpecsIngested(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, dir, "specs/auth/spec.md", "# Auth Spec\ncontent")

	a := newAdapter(t, dir)
	ids, err := a.ListNative(context.Background(), "proj", time.Time{})
	require.NoError(t, err)
	assert.Len(t, ids, 1)
	assert.Equal(t, adapter.NativeID("specs/auth/spec.md"), ids[0])
}

// TestListNative_NonExistentRoot_ReturnsNilNil verifies that ListNative on a
// non-existent openspec root returns (nil, nil) — lenient behaviour so a
// disabled openspec does not abort a sync run (PRD-11 §4 open question).
func TestListNative_NonExistentRoot_ReturnsNilNil(t *testing.T) {
	a := newAdapter(t, "/does/not/exist/openspec-xyz-999")
	ids, err := a.ListNative(context.Background(), "proj", time.Time{})
	require.NoError(t, err)
	assert.Nil(t, ids)
}

func TestListNative_SinceMtimeFilter(t *testing.T) {
	dir := t.TempDir()

	// Write an old file.
	oldPath := filepath.Join(dir, "changes/old/design.md")
	require.NoError(t, os.MkdirAll(filepath.Dir(oldPath), 0o755))
	require.NoError(t, os.WriteFile(oldPath, []byte("old content"), 0o644))

	// Set mtime to 1 hour ago.
	oldTime := time.Now().Add(-time.Hour)
	require.NoError(t, os.Chtimes(oldPath, oldTime, oldTime))

	// Write a new file.
	writeFile(t, dir, "changes/new/design.md", "new content")

	since := time.Now().Add(-30 * time.Minute)
	a := newAdapter(t, dir)
	ids, err := a.ListNative(context.Background(), "proj", since)
	require.NoError(t, err)
	// Only the new file should pass the since filter.
	assert.Len(t, ids, 1)
	assert.Equal(t, adapter.NativeID("changes/new/design.md"), ids[0])
}

// ---------------------------------------------------------------------------
// ReadNative
// ---------------------------------------------------------------------------

func TestReadNative_ReturnsFileContent(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, dir, "changes/auth/design.md", "# Auth Design\nsome content")

	a := newAdapter(t, dir)
	native, err := a.ReadNative(context.Background(), "changes/auth/design.md")
	require.NoError(t, err)
	require.NotNil(t, native)
}

func TestReadNative_MissingFile_ReturnsErrNotFound(t *testing.T) {
	a := newAdapter(t, t.TempDir())
	_, err := a.ReadNative(context.Background(), "changes/nope/design.md")
	assert.ErrorIs(t, err, adapter.ErrNotFound)
}

// ---------------------------------------------------------------------------
// ToCanonical — field mapping
// ---------------------------------------------------------------------------

func TestToCanonical_TitleFromFirstH1(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, dir, "changes/auth/design.md", "# My Design Title\ncontent here")

	a := newAdapter(t, dir)
	native, err := a.ReadNative(context.Background(), "changes/auth/design.md")
	require.NoError(t, err)

	rec, err := a.ToCanonical(native, emptyIDMap{})
	require.NoError(t, err)
	assert.Equal(t, "My Design Title", rec.Title)
}

func TestToCanonical_TitleFallback_WhenNoH1(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, dir, "changes/auth/design.md", "no heading here\ncontent")

	a := newAdapter(t, dir)
	native, err := a.ReadNative(context.Background(), "changes/auth/design.md")
	require.NoError(t, err)

	rec, err := a.ToCanonical(native, emptyIDMap{})
	require.NoError(t, err)
	// Fallback: "<change> — <artifact>"
	assert.Equal(t, "auth — design", rec.Title)
}

func TestToCanonical_KindIsSpecArtifact(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, dir, "changes/auth/design.md", "# Design\ncontent")

	a := newAdapter(t, dir)
	native, err := a.ReadNative(context.Background(), "changes/auth/design.md")
	require.NoError(t, err)

	rec, err := a.ToCanonical(native, emptyIDMap{})
	require.NoError(t, err)
	assert.Equal(t, "spec_artifact", rec.Kind)
}

func TestToCanonical_TypeFromArtifactName(t *testing.T) {
	tests := []struct {
		path     string
		wantType string
	}{
		{"changes/auth/proposal.md", "proposal"},
		{"changes/auth/design.md", "design"},
		{"changes/auth/tasks.md", "tasks"},
		{"changes/auth/specs/req.md", "spec"},
		{"specs/auth/spec.md", "spec"},
	}
	for _, tt := range tests {
		t.Run(tt.path, func(t *testing.T) {
			dir := t.TempDir()
			writeFile(t, dir, tt.path, "# Title\ncontent")
			a := newAdapter(t, dir)
			native, err := a.ReadNative(context.Background(), adapter.NativeID(tt.path))
			require.NoError(t, err)
			rec, err := a.ToCanonical(native, emptyIDMap{})
			require.NoError(t, err)
			assert.Equal(t, tt.wantType, rec.Type, "path=%s", tt.path)
		})
	}
}

func TestToCanonical_TopicKey_ChangeArtifact(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, dir, "changes/auth/design.md", "# Design\ncontent")

	a := newAdapter(t, dir)
	native, err := a.ReadNative(context.Background(), "changes/auth/design.md")
	require.NoError(t, err)

	rec, err := a.ToCanonical(native, emptyIDMap{})
	require.NoError(t, err)
	assert.Equal(t, "sdd/auth/design", rec.TopicKey)
}

func TestToCanonical_TopicKey_MergedSpec(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, dir, "specs/auth/spec.md", "# Spec\ncontent")

	a := newAdapter(t, dir)
	native, err := a.ReadNative(context.Background(), "specs/auth/spec.md")
	require.NoError(t, err)

	rec, err := a.ToCanonical(native, emptyIDMap{})
	require.NoError(t, err)
	assert.Equal(t, "spec/auth", rec.TopicKey)
}

func TestToCanonical_ProviderID_IsRelativePath(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, dir, "changes/auth/design.md", "# Design\ncontent")

	a := newAdapter(t, dir)
	native, err := a.ReadNative(context.Background(), "changes/auth/design.md")
	require.NoError(t, err)

	rec, err := a.ToCanonical(native, emptyIDMap{})
	require.NoError(t, err)
	assert.Equal(t, "changes/auth/design.md", rec.Origin.ProviderID)
	assert.Equal(t, "openspec", rec.Origin.Provider)
}

func TestToCanonical_ContentFormat_IsMarkdown(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, dir, "changes/auth/design.md", "# Design\ncontent")

	a := newAdapter(t, dir)
	native, err := a.ReadNative(context.Background(), "changes/auth/design.md")
	require.NoError(t, err)

	rec, err := a.ToCanonical(native, emptyIDMap{})
	require.NoError(t, err)
	assert.Equal(t, "markdown", rec.ContentFormat)
}

func TestToCanonical_ContentIsFullFile(t *testing.T) {
	dir := t.TempDir()
	content := "# Design\nsome content here"
	writeFile(t, dir, "changes/auth/design.md", content)

	a := newAdapter(t, dir)
	native, err := a.ReadNative(context.Background(), "changes/auth/design.md")
	require.NoError(t, err)

	rec, err := a.ToCanonical(native, emptyIDMap{})
	require.NoError(t, err)
	assert.Equal(t, content, rec.Content)
}

func TestToCanonical_Revision_NewRecord(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, dir, "changes/auth/design.md", "# Design\ncontent")

	a := newAdapter(t, dir)
	native, err := a.ReadNative(context.Background(), "changes/auth/design.md")
	require.NoError(t, err)

	// No IDMap mapping → new record → revision 1.
	rec, err := a.ToCanonical(native, emptyIDMap{})
	require.NoError(t, err)
	assert.Equal(t, 1, rec.Revision)
}

// mappedIDMap satisfies adapter.IDMap with a single known mapping.
type mappedIDMap struct {
	nativeID    adapter.NativeID
	canonicalID adapter.CanonicalID
}

func (m mappedIDMap) CanonicalFromNative(id adapter.NativeID) (adapter.CanonicalID, bool) {
	if id == m.nativeID {
		return m.canonicalID, true
	}
	return "", false
}
func (m mappedIDMap) NativeFromCanonical(id adapter.CanonicalID) (adapter.NativeID, bool) {
	if id == m.canonicalID {
		return m.nativeID, true
	}
	return "", false
}

func TestToCanonical_Revision_ExistingRecord_UsesMinusSentinel(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, dir, "changes/auth/design.md", "# Design\ncontent")

	a := newAdapter(t, dir)
	native, err := a.ReadNative(context.Background(), "changes/auth/design.md")
	require.NoError(t, err)

	idmap := mappedIDMap{
		nativeID:    "changes/auth/design.md",
		canonicalID: "01HZZZZZZZZZZZZZZZZZZZAAAA",
	}
	rec, err := a.ToCanonical(native, idmap)
	require.NoError(t, err)
	assert.Equal(t, "01HZZZZZZZZZZZZZZZZZZZAAAA", rec.CanonicalID)
	// Known record → -1 sentinel (ADR-12, same convention as other adapters).
	assert.Equal(t, -1, rec.Revision)
}

// ---------------------------------------------------------------------------
// Archived-change tagging (PRD-11 §9.5)
// ---------------------------------------------------------------------------

// TestToCanonical_ArchivedArtifact_TaggedArchived verifies that an artifact
// under changes/archive/<change>/ carries the "archived" tag and that
// topic_key + type still resolve correctly (the archive/ segment is skipped).
func TestToCanonical_ArchivedArtifact_TaggedArchived(t *testing.T) {
	tests := []struct {
		name          string
		path          string
		wantTopicKey  string
		wantType      string
	}{
		{
			name:         "design under archive",
			path:         "changes/archive/auth/design.md",
			wantTopicKey: "sdd/auth/design",
			wantType:     "design",
		},
		{
			name:         "tasks under archive",
			path:         "changes/archive/auth/tasks.md",
			wantTopicKey: "sdd/auth/tasks",
			wantType:     "tasks",
		},
		{
			name:         "proposal under archive",
			path:         "changes/archive/my-feature/proposal.md",
			wantTopicKey: "sdd/my-feature/proposal",
			wantType:     "proposal",
		},
		{
			name:         "spec nested under archive",
			path:         "changes/archive/my-feature/specs/req.md",
			wantTopicKey: "sdd/my-feature/req",
			wantType:     "spec",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			dir := t.TempDir()
			writeFile(t, dir, tt.path, "# Archived Artifact\ncontent")

			a := newAdapter(t, dir)
			native, err := a.ReadNative(context.Background(), adapter.NativeID(tt.path))
			require.NoError(t, err)

			rec, err := a.ToCanonical(native, emptyIDMap{})
			require.NoError(t, err)

			assert.Equal(t, []string{"archived"}, rec.Tags, "archived path must carry archived tag")
			assert.Equal(t, tt.wantTopicKey, rec.TopicKey, "topic_key must skip archive/ segment")
			assert.Equal(t, tt.wantType, rec.Type, "type must resolve correctly for archived artifact")
		})
	}
}

// TestToCanonical_NonArchivedArtifact_EmptyTags verifies that a regular
// (non-archived) artifact keeps empty tags (no false positives).
func TestToCanonical_NonArchivedArtifact_EmptyTags(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, dir, "changes/auth/design.md", "# Design\ncontent")

	a := newAdapter(t, dir)
	native, err := a.ReadNative(context.Background(), "changes/auth/design.md")
	require.NoError(t, err)

	rec, err := a.ToCanonical(native, emptyIDMap{})
	require.NoError(t, err)

	assert.Empty(t, rec.Tags, "non-archived artifact must have empty tags")
}

// TestListNative_ArchivedDirIngested verifies that ListNative includes .md files
// under changes/archive/ (they are valid artifacts, just tagged differently).
func TestListNative_ArchivedDirIngested(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, dir, "changes/archive/auth/design.md", "# Archived Design")
	writeFile(t, dir, "changes/auth/design.md", "# Active Design")

	a := newAdapter(t, dir)
	ids, err := a.ListNative(context.Background(), "proj", time.Time{})
	require.NoError(t, err)
	assert.Len(t, ids, 2, "archived and active artifacts must both be listed")
}

// ---------------------------------------------------------------------------
// Pull-leakage invariant: openspec/ directory not modified during pull simulation
// ---------------------------------------------------------------------------

func TestPullLeakage_OpenspecDirUnchangedAfterFromCanonical(t *testing.T) {
	dir := t.TempDir()
	relPath := "changes/auth/design.md"
	original := "# Design\noriginal content"
	writeFile(t, dir, relPath, original)

	a := newAdapter(t, dir)

	// Simulate the pull loop calling FromCanonical (the pull-leakage gate).
	rec := store.CanonicalRecord{
		Kind:          "spec_artifact",
		Type:          "design",
		Title:         "Auth Design",
		Content:       "modified content by pull",
		ContentFormat: "markdown",
		Origin: store.Origin{
			Provider:   "openspec",
			ProviderID: relPath,
		},
	}

	native, err := a.FromCanonical(rec)
	assert.ErrorIs(t, err, adapter.ErrUnsupported,
		"FromCanonical must return ErrUnsupported so pull loop skips WriteNative")
	assert.Nil(t, native)

	// Verify the file on disk is untouched.
	got, readErr := os.ReadFile(filepath.Join(dir, relPath))
	require.NoError(t, readErr)
	assert.Equal(t, original, string(got),
		"openspec file must be byte-identical after FromCanonical call")
}
