package sync

import (
	"sort"

	"github.com/agustincastanol/wrapper-mems/internal/store"
)

// recordsEqualIgnoringMetadata reports whether a and b carry identical payload
// content, intentionally ignoring all store-managed metadata fields.
//
// Compared fields (D5 / REQ-SE-56):
//   Title, Content, ContentFormat, Type, TopicKey, Kind, Tags (set equality).
//
// Ignored fields:
//   Revision, Supersedes, LineULID, SchemaVersion, CreatedAt, UpdatedAt, Origin.*.
func recordsEqualIgnoringMetadata(a, b store.CanonicalRecord) bool {
	if a.Title != b.Title {
		return false
	}
	if a.Content != b.Content {
		return false
	}
	if a.ContentFormat != b.ContentFormat {
		return false
	}
	if a.Type != b.Type {
		return false
	}
	if a.TopicKey != b.TopicKey {
		return false
	}
	if a.Kind != b.Kind {
		return false
	}
	return tagsEqual(a.Tags, b.Tags)
}

// tagsEqual returns true when the two slices contain the same set of strings,
// regardless of order (REQ-SE-56 "Tags order-insensitive").
func tagsEqual(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	if len(a) == 0 {
		return true
	}

	// Copy then sort to avoid mutating the caller's slices.
	sa := make([]string, len(a))
	sb := make([]string, len(b))
	copy(sa, a)
	copy(sb, b)
	sort.Strings(sa)
	sort.Strings(sb)

	for i := range sa {
		if sa[i] != sb[i] {
			return false
		}
	}
	return true
}
