package sync

import (
	"time"

	"github.com/agustincastanol/wrapper-mems/internal/store"
)

// readWatermark returns the last-pushed-at watermark for provider.
// Returns (zero time, false) when no watermark exists yet (first run).
func readWatermark(s *store.Store, provider string) (time.Time, bool) {
	st, ok := s.SyncState(provider)
	if !ok {
		return time.Time{}, false
	}

	// Use LastPushedAt as the push-side watermark; callers decide which field to use.
	if st.LastPushedAt == "" {
		return time.Time{}, false
	}

	t, err := time.Parse(time.RFC3339, st.LastPushedAt)
	if err != nil {
		return time.Time{}, false
	}
	return t, true
}

// readPullWatermark returns the last-pulled-at watermark for provider.
// Returns (zero time, false) when no watermark exists yet.
func readPullWatermark(s *store.Store, provider string) (time.Time, bool) {
	st, ok := s.SyncState(provider)
	if !ok {
		return time.Time{}, false
	}
	if st.LastPulledAt == "" {
		return time.Time{}, false
	}
	t, err := time.Parse(time.RFC3339, st.LastPulledAt)
	if err != nil {
		return time.Time{}, false
	}
	return t, true
}

// writeWatermark persists the push watermark and counters for provider.
// pushedAt is typically time.Now().UTC() after a successful push loop.
//
// The SyncState read + UpdateSyncState write is not atomic; callers MUST
// serialize watermark writes per provider (the engine processes adapters
// sequentially in deterministic order, so this holds in v1).
func writeWatermark(s *store.Store, provider string, pushedAt time.Time, result ProviderResult) error {
	st, _ := s.SyncState(provider)
	st.LastPushedAt = pushedAt.UTC().Format(time.RFC3339)
	st.RecordsPushed += result.Pushed
	return s.UpdateSyncState(provider, st)
}

// writePullWatermark persists the pull watermark and counters for provider.
// Same non-atomic read-modify-write caveat as writeWatermark: callers MUST
// serialize watermark writes per provider.
func writePullWatermark(s *store.Store, provider string, pulledAt time.Time, result ProviderResult) error {
	st, _ := s.SyncState(provider)
	st.LastPulledAt = pulledAt.UTC().Format(time.RFC3339)
	st.RecordsPulled += result.Pulled
	return s.UpdateSyncState(provider, st)
}
