package sync

import (
	"testing"

	"github.com/agustincastanol/wrapper-mems/internal/store"
)

func TestRecordsEqualIgnoringMetadata(t *testing.T) {
	base := store.CanonicalRecord{
		Title:         "title",
		Content:       "content",
		ContentFormat: "markdown",
		Type:          "observation",
		TopicKey:      "arch/auth",
		Kind:          "observation",
		Tags:          []string{"a", "b"},
		// Metadata fields that must be ignored:
		Revision:      1,
		Supersedes:    "old-id",
		LineULID:      "01JXYZ",
		SchemaVersion: 1,
		CreatedAt:     "2024-01-01T00:00:00Z",
		UpdatedAt:     "2024-01-01T00:00:00Z",
		Origin: store.Origin{
			Provider:   "engram",
			ProviderID: "prov-1",
			Author:     "alice",
			SessionID:  "sess-1",
		},
	}

	tests := []struct {
		name  string
		mutB  func(b *store.CanonicalRecord)
		equal bool
	}{
		// --- payload fields: each must cause inequality ---
		{
			name:  "Title differs",
			mutB:  func(b *store.CanonicalRecord) { b.Title = "other" },
			equal: false,
		},
		{
			name:  "Content differs",
			mutB:  func(b *store.CanonicalRecord) { b.Content = "other" },
			equal: false,
		},
		{
			name:  "ContentFormat differs",
			mutB:  func(b *store.CanonicalRecord) { b.ContentFormat = "plaintext" },
			equal: false,
		},
		{
			name:  "Type differs",
			mutB:  func(b *store.CanonicalRecord) { b.Type = "session_summary" },
			equal: false,
		},
		{
			name:  "TopicKey differs",
			mutB:  func(b *store.CanonicalRecord) { b.TopicKey = "other/key" },
			equal: false,
		},
		{
			name:  "Kind differs",
			mutB:  func(b *store.CanonicalRecord) { b.Kind = "relation" },
			equal: false,
		},
		{
			name:  "Tags length differs",
			mutB:  func(b *store.CanonicalRecord) { b.Tags = []string{"a"} },
			equal: false,
		},
		{
			name:  "Tags content differs",
			mutB:  func(b *store.CanonicalRecord) { b.Tags = []string{"a", "c"} },
			equal: false,
		},
		// --- metadata fields: each must NOT cause inequality ---
		{
			name:  "Revision differs",
			mutB:  func(b *store.CanonicalRecord) { b.Revision = 99 },
			equal: true,
		},
		{
			name:  "Supersedes differs",
			mutB:  func(b *store.CanonicalRecord) { b.Supersedes = "different" },
			equal: true,
		},
		{
			name:  "LineULID differs",
			mutB:  func(b *store.CanonicalRecord) { b.LineULID = "99ZZZZ" },
			equal: true,
		},
		{
			name:  "SchemaVersion differs",
			mutB:  func(b *store.CanonicalRecord) { b.SchemaVersion = 2 },
			equal: true,
		},
		{
			name:  "CreatedAt differs",
			mutB:  func(b *store.CanonicalRecord) { b.CreatedAt = "2025-01-01T00:00:00Z" },
			equal: true,
		},
		{
			name:  "UpdatedAt differs",
			mutB:  func(b *store.CanonicalRecord) { b.UpdatedAt = "2025-01-01T00:00:00Z" },
			equal: true,
		},
		{
			name:  "Origin.Provider differs",
			mutB:  func(b *store.CanonicalRecord) { b.Origin.Provider = "claude-mem" },
			equal: true,
		},
		{
			name:  "Origin.ProviderID differs",
			mutB:  func(b *store.CanonicalRecord) { b.Origin.ProviderID = "prov-99" },
			equal: true,
		},
		{
			name:  "Origin.Author differs",
			mutB:  func(b *store.CanonicalRecord) { b.Origin.Author = "bob" },
			equal: true,
		},
		{
			name:  "Origin.SessionID differs",
			mutB:  func(b *store.CanonicalRecord) { b.Origin.SessionID = "sess-99" },
			equal: true,
		},
		// --- tags order-insensitivity ---
		{
			name:  "Tags order swapped → equal",
			mutB:  func(b *store.CanonicalRecord) { b.Tags = []string{"b", "a"} },
			equal: true,
		},
		// --- identical records ---
		{
			name:  "All fields identical",
			mutB:  func(b *store.CanonicalRecord) {},
			equal: true,
		},
		// --- nil/empty tags edge cases ---
		{
			name: "Both tags nil → equal",
			mutB: func(b *store.CanonicalRecord) {
				b.Tags = nil
			},
			equal: false, // base has ["a","b"], b has nil — different lengths
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			a := base
			b := base
			a.Tags = cloneStrings(base.Tags)
			b.Tags = cloneStrings(base.Tags)
			tc.mutB(&b)

			got := recordsEqualIgnoringMetadata(a, b)
			if got != tc.equal {
				t.Errorf("recordsEqualIgnoringMetadata() = %v, want %v", got, tc.equal)
			}
		})
	}
}

func TestTagsEqual_BothNil(t *testing.T) {
	if !tagsEqual(nil, nil) {
		t.Fatal("tagsEqual(nil,nil) should return true")
	}
}

func TestTagsEqual_BothEmpty(t *testing.T) {
	if !tagsEqual([]string{}, []string{}) {
		t.Fatal("tagsEqual([],[]) should return true")
	}
}

func TestTagsEqual_NilVsEmpty(t *testing.T) {
	// nil and [] have length 0: both are treated as "no tags" → equal.
	if !tagsEqual(nil, []string{}) {
		t.Fatal("tagsEqual(nil,[]) should return true")
	}
}

func cloneStrings(ss []string) []string {
	if ss == nil {
		return nil
	}
	out := make([]string, len(ss))
	copy(out, ss)
	return out
}
