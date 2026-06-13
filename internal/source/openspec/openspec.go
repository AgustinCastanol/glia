// Package openspec implements adapter.Adapter for the OpenSpec static file
// source (PRD-11). It is read-only: ListNative/ReadNative/ToCanonical ingest
// markdown artifacts from an openspec/ directory tree; FromCanonical and
// WriteNative return ErrUnsupported, closing the pull-leakage gate (D2).
//
// Import direction: internal/source/openspec → internal/adapter → internal/store.
// internal/store is never imported by internal/adapter, so this package must
// import internal/store directly (which is allowed — the adapter layer sits
// above the store layer).
package openspec

import (
	"bufio"
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/agustincastanol/glia/internal/adapter"
	"github.com/agustincastanol/glia/internal/identity"
	"github.com/agustincastanol/glia/internal/store"
)

// Config holds construction-time parameters for Adapter.
// The wiring helper (cmd/glia/cmd/wiring.go) translates *config.Config →
// openspec.Config. Adapters never import internal/config (ADR-D3).
type Config struct {
	// Dir is the absolute path to the openspec root directory (e.g. "/repo/openspec").
	// Defaults to "openspec" relative to the working directory when empty; callers
	// should resolve the absolute path before calling New.
	Dir string
}

// nativeFile is the internal representation of a single OpenSpec artifact file.
// It is the concrete NativeRecord type owned by this adapter.
type nativeFile struct {
	// RelPath is the path relative to the openspec root (e.g. "changes/auth/design.md").
	// It is used as origin.provider_id and as the stable IDMap key.
	RelPath string
	// Content is the full UTF-8 text of the file.
	Content string
	// ModTime is the file's last modification time (used for created_at/updated_at).
	ModTime time.Time
}

// Compile-time assertion: Adapter must satisfy adapter.Adapter.
var _ adapter.Adapter = (*Adapter)(nil)

// Adapter implements adapter.Adapter for the OpenSpec static file source.
// It is safe for concurrent use after construction (no mutable state).
type Adapter struct {
	cfg    Config
	author string
}

// New constructs an Adapter. The Dir field of cfg is used as-is; callers should
// resolve the absolute path before calling New. Author is resolved from
// identity.Resolve if cfg does not supply one.
func New(cfg Config) *Adapter {
	if cfg.Dir == "" {
		cfg.Dir = "openspec"
	}
	return &Adapter{
		cfg:    cfg,
		author: identity.Resolve(""),
	}
}

// Name returns "openspec" — the stable provider identifier stored in
// origin.provider of every canonical record.
func (a *Adapter) Name() string { return "openspec" }

// SupportedKinds returns ["spec_artifact"]. The pull engine uses this to filter
// before calling FromCanonical; however, the real pull-leakage gate is
// ErrUnsupported from FromCanonical itself (D2 — SupportedKinds is not enough).
func (a *Adapter) SupportedKinds() []string { return []string{"spec_artifact"} }

// WriteCapability returns "read-only" — this adapter has no write surface.
func (a *Adapter) WriteCapability() string { return "read-only" }

// Health probes that the configured openspec directory exists and is accessible.
// Returns ErrUnavailable if the directory cannot be stat'd.
func (a *Adapter) Health(ctx context.Context) error {
	if _, err := os.Stat(a.cfg.Dir); err != nil {
		return fmt.Errorf("%w: openspec dir %q: %v", adapter.ErrUnavailable, a.cfg.Dir, err)
	}
	return nil
}

// ListNative walks the openspec directory and returns the relative paths of all
// ingestible artifact files as NativeIDs.
//
// Walk rules:
//   - Accepted paths: changes/<c>/*.md, changes/<c>/specs/**/*.md,
//     specs/<domain>/**/*.md  (any .md under the root, excluding state.yaml).
//   - state.yaml is skipped (§4 open question resolved: skip, not ingest).
//   - Non-.md files are skipped.
//   - Malformed/unreadable directory entries are logged and skipped.
//   - The returned slice is sorted (deterministic order for tests and diffing).
//   - The since filter uses file mtime: only files with mtime ≥ since are returned
//     (zero since = no filter).
func (a *Adapter) ListNative(_ context.Context, _ string, since time.Time) ([]adapter.NativeID, error) {
	var ids []adapter.NativeID
	skippedMalformed := 0

	walkErr := filepath.WalkDir(a.cfg.Dir, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			// Unreadable entry: log, skip (do not abort the walk).
			skippedMalformed++
			return nil
		}
		if d.IsDir() {
			return nil
		}

		// Compute relative path from the openspec root.
		rel, relErr := filepath.Rel(a.cfg.Dir, path)
		if relErr != nil {
			skippedMalformed++
			return nil
		}
		rel = filepath.ToSlash(rel) // normalise to forward slashes on all platforms

		// Skip non-.md files (catches state.yaml and any other non-markdown).
		if !strings.HasSuffix(rel, ".md") {
			return nil
		}

		// Apply mtime-based since filter.
		if !since.IsZero() {
			info, statErr := d.Info()
			if statErr != nil {
				skippedMalformed++
				return nil
			}
			if info.ModTime().Before(since) {
				return nil
			}
		}

		ids = append(ids, adapter.NativeID(rel))
		return nil
	})

	if walkErr != nil {
		// Root directory does not exist or is not accessible — return empty, not error.
		// Health() covers that probe; ListNative is lenient so a disabled openspec
		// does not abort a sync run.
		if os.IsNotExist(walkErr) {
			return nil, nil
		}
		return nil, fmt.Errorf("openspec: walk %q: %w", a.cfg.Dir, walkErr)
	}

	if skippedMalformed > 0 {
		// Log count only — individual entries logged inline above.
		fmt.Fprintf(os.Stderr, "openspec: skipped %d malformed/unreadable entries during walk\n", skippedMalformed)
	}

	sort.Slice(ids, func(i, j int) bool { return ids[i] < ids[j] })
	return ids, nil
}

// ReadNative reads the artifact file identified by id (a relative path from the
// openspec root). Returns ErrNotFound if the file does not exist.
func (a *Adapter) ReadNative(_ context.Context, id adapter.NativeID) (adapter.NativeRecord, error) {
	rel := string(id)
	abs := filepath.Join(a.cfg.Dir, rel)

	info, err := os.Stat(abs)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, adapter.ErrNotFound
		}
		return nil, fmt.Errorf("openspec: stat %q: %w", abs, err)
	}

	data, err := os.ReadFile(abs)
	if err != nil {
		return nil, fmt.Errorf("openspec: read %q: %w", abs, err)
	}

	return nativeFile{
		RelPath: rel,
		Content: string(data),
		ModTime: info.ModTime().UTC(),
	}, nil
}

// ToCanonical converts a nativeFile to a store.CanonicalRecord. Pure: no I/O.
//
// Field mapping (PRD-11 §5):
//   - origin.provider_id  = nativeFile.RelPath (stable per-file key)
//   - origin.provider     = "openspec"
//   - origin.author       = resolved at construction (identity.Resolve)
//   - kind                = "spec_artifact"
//   - type                = derived from path (proposal|spec|design|tasks)
//   - title               = first H1 from content, else "<change> — <artifact>"
//   - topic_key           = sdd/<change>/<artifact> or spec/<domain>
//   - content             = full file text
//   - content_format      = "markdown"
//   - created_at/updated_at = file mtime (RFC3339 UTC)
//   - canonical_id        = from IDMap on provider_id, or "" (store mints ULID)
//   - revision            = 1 (new) or -1 sentinel (known; ADR-12)
//   - tags                = []string{}
func (a *Adapter) ToCanonical(native adapter.NativeRecord, idmap adapter.IDMap) (store.CanonicalRecord, error) {
	f, ok := native.(nativeFile)
	if !ok {
		return store.CanonicalRecord{}, fmt.Errorf("openspec: ToCanonical expected nativeFile, got %T", native)
	}

	nativeID := adapter.NativeID(f.RelPath)
	canonicalID, hasMapping := idmap.CanonicalFromNative(nativeID)

	revision := 1
	if hasMapping {
		revision = -1 // ADR-12 sentinel: known record, store increments
	}

	ts := f.ModTime.Format(time.RFC3339)

	tags := []string{}
	if isArchived(f.RelPath) {
		tags = []string{"archived"}
	}

	return store.CanonicalRecord{
		CanonicalID:   string(canonicalID),
		Kind:          "spec_artifact",
		Type:          artifactType(f.RelPath),
		Revision:      revision,
		Title:         extractTitle(f.Content, f.RelPath),
		Content:       f.Content,
		ContentFormat: "markdown",
		TopicKey:      topicKey(f.RelPath),
		CreatedAt:     ts,
		UpdatedAt:     ts,
		Tags:          tags,
		Origin: store.Origin{
			Provider:   "openspec",
			ProviderID: f.RelPath,
			Author:     a.author,
		},
	}, nil
}

// FromCanonical returns ErrUnsupported. This is the pull-leakage gate (D2):
// the pull engine reaches ErrUnsupported before ever calling WriteNative, so
// the openspec directory is never written to.
func (a *Adapter) FromCanonical(_ store.CanonicalRecord) (adapter.NativeRecord, error) {
	return nil, fmt.Errorf("%w: openspec is read-only; canonical→openspec conversion is not supported", adapter.ErrUnsupported)
}

// WriteNative returns ErrUnsupported. openspec artifacts are never written by glia.
func (a *Adapter) WriteNative(_ context.Context, _ adapter.NativeRecord) (adapter.NativeID, error) {
	return "", fmt.Errorf("%w: openspec is read-only; writing to openspec files is not supported", adapter.ErrUnsupported)
}

// ---------------------------------------------------------------------------
// Internal helpers
// ---------------------------------------------------------------------------

// extractTitle returns the text of the first H1 heading found in content.
// If no H1 is present, it falls back to "<change> — <artifact>" derived from
// the relative path (PRD-11 §5 field mapping).
func extractTitle(content, relPath string) string {
	scanner := bufio.NewScanner(strings.NewReader(content))
	for scanner.Scan() {
		line := scanner.Text()
		if strings.HasPrefix(line, "# ") {
			title := strings.TrimPrefix(line, "# ")
			title = strings.TrimSpace(title)
			if title != "" {
				return title
			}
		}
	}
	// Fallback: "<change> — <artifact>" from path.
	return fallbackTitle(relPath)
}

// fallbackTitle derives "<change> — <artifact>" from a relative path.
// For "changes/auth/design.md" → "auth — design".
// For "changes/auth/specs/req.md" → "auth — req".
// For "specs/auth/spec.md"       → "auth — spec".
func fallbackTitle(relPath string) string {
	parts := strings.Split(relPath, "/")
	switch {
	case len(parts) >= 3 && parts[0] == "changes":
		change := parts[1]
		artifact := strings.TrimSuffix(parts[len(parts)-1], ".md")
		return change + " — " + artifact
	case len(parts) >= 3 && parts[0] == "specs":
		domain := parts[1]
		artifact := strings.TrimSuffix(parts[len(parts)-1], ".md")
		return domain + " — " + artifact
	default:
		return strings.TrimSuffix(filepath.Base(relPath), ".md")
	}
}

// isArchived reports whether relPath is under changes/archive/<change>/.
// Archived changes are relocated by openspec to this subtree (PRD-11 §9.5).
func isArchived(relPath string) bool {
	parts := strings.Split(relPath, "/")
	// Pattern: changes/archive/<change>/...
	return len(parts) >= 4 && parts[0] == "changes" && parts[1] == "archive"
}

// changeAndRest extracts (change, restParts) from a changes/... path.
// For archived paths (changes/archive/<change>/...) it skips the "archive" segment.
// For active paths (changes/<change>/...) it returns directly.
// Returns ("", nil) if the path does not match a changes/... pattern.
func changeAndRest(parts []string) (string, []string) {
	if len(parts) < 3 || parts[0] != "changes" {
		return "", nil
	}
	if parts[1] == "archive" && len(parts) >= 4 {
		// changes/archive/<change>/<rest...>
		return parts[2], parts[3:]
	}
	// changes/<change>/<rest...>
	return parts[1], parts[2:]
}

// artifactType maps a relative artifact path to the canonical type string
// (proposal | spec | design | tasks).
//
// Rules (PRD-11 §5):
//   - changes/<c>/proposal.md              → "proposal"
//   - changes/<c>/design.md                → "design"
//   - changes/<c>/tasks.md                 → "tasks"
//   - changes/<c>/specs/**                 → "spec"
//   - changes/archive/<c>/<same rules>     → same types (archived segment skipped)
//   - specs/**                             → "spec"
func artifactType(relPath string) string {
	parts := strings.Split(relPath, "/")

	if len(parts) >= 2 && parts[0] == "specs" {
		return "spec"
	}

	if len(parts) >= 3 && parts[0] == "changes" {
		_, rest := changeAndRest(parts)
		if rest == nil {
			return "spec"
		}
		// rest[0] may be "specs/..." subtree
		if len(rest) >= 2 && rest[0] == "specs" {
			return "spec"
		}
		base := strings.TrimSuffix(rest[len(rest)-1], ".md")
		switch base {
		case "proposal":
			return "proposal"
		case "design":
			return "design"
		case "tasks":
			return "tasks"
		default:
			return "spec" // any other .md inside a change folder
		}
	}

	return "spec" // safe default
}

// topicKey derives the canonical topic_key from a relative path (PRD-11 §5).
//
// Rules:
//   - changes/<c>/<artifact>.md              → "sdd/<c>/<artifact>"
//   - changes/<c>/specs/<f>.md               → "sdd/<c>/<f>"
//   - changes/archive/<c>/<artifact>.md      → "sdd/<c>/<artifact>"  (archive segment skipped)
//   - changes/archive/<c>/specs/<f>.md       → "sdd/<c>/<f>"         (archive segment skipped)
//   - specs/<domain>/spec.md                 → "spec/<domain>"
//   - specs/<domain>/<f>.md                  → "spec/<domain>"
func topicKey(relPath string) string {
	parts := strings.Split(relPath, "/")

	if len(parts) >= 2 && parts[0] == "specs" {
		domain := parts[1]
		return "spec/" + domain
	}

	if len(parts) >= 3 && parts[0] == "changes" {
		change, rest := changeAndRest(parts)
		if change == "" || rest == nil {
			return strings.TrimSuffix(filepath.Base(relPath), ".md")
		}
		// rest[0] == "specs" subtree → use file base as artifact key
		if len(rest) >= 2 && rest[0] == "specs" {
			artifact := strings.TrimSuffix(rest[len(rest)-1], ".md")
			return "sdd/" + change + "/" + artifact
		}
		artifact := strings.TrimSuffix(rest[len(rest)-1], ".md")
		return "sdd/" + change + "/" + artifact
	}

	// Fallback: use the file stem.
	return strings.TrimSuffix(filepath.Base(relPath), ".md")
}
