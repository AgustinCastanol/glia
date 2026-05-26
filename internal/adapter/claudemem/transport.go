package claudemem

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"time"

	"github.com/agustincastanol/glia/internal/adapter"
)

// Transport abstracts the HTTP calls to the claude-mem daemon so that unit
// tests can inject a fake without requiring a real daemon to be running.
//
// A nil Transport causes Health/ListNative/ReadNative to return
// adapter.ErrUnavailable without panicking.
type Transport interface {
	// Health probes GET /health. Returns nil on 2xx.
	// Returns adapter.ErrUnavailable on connection failure or non-2xx.
	// Context cancellation is propagated raw.
	Health(ctx context.Context) error

	// ListPage fetches GET /api/observations?limit=<limit>&offset=<offset> and
	// returns the raw JSON body for one page.
	// Returns adapter.ErrUnavailable on connection failure or non-2xx.
	ListPage(ctx context.Context, limit, offset int) ([]byte, error)

	// GetByID fetches GET /api/observations/<id>.
	// Returns (body, true, nil) on 2xx.
	// Returns ("", false, adapter.ErrNotFound) on 404 (clean not-found).
	// Returns ("", false, adapter.ErrUnavailable) on connection failure, missing
	// endpoint, or non-2xx/non-404 — signals ReadNative to degrade to scan.
	GetByID(ctx context.Context, id string) (body []byte, found bool, err error)
}

// supervisorConfig is a minimal subset of ~/.claude-mem/supervisor.json.
// Additional fields are silently ignored (tolerant parse per ADR-3).
type supervisorConfig struct {
	Port int `json:"port,omitempty"` // PROVISIONAL: supervisor.json schema unverified
}

// readSupervisorPort reads ~/.claude-mem/supervisor.json and returns the port
// field. Returns 0 on ANY error (missing file, bad JSON, port==0).
// Never returns an error; never panics. Construction must not fail on this
// peripheral discovery file (ADR-3).
func readSupervisorPort() int {
	home, err := os.UserHomeDir()
	if err != nil {
		return 0
	}
	data, err := os.ReadFile(home + "/.claude-mem/supervisor.json")
	if err != nil {
		return 0
	}
	var cfg supervisorConfig
	if err := json.Unmarshal(data, &cfg); err != nil {
		return 0
	}
	return cfg.Port
}

// resolveBaseURL resolves the HTTP base URL for the claude-mem daemon using the
// priority order mandated by REQ-CM-05 and ADR-3:
//  1. explicit arg (non-empty) — wins immediately
//  2. port from ~/.claude-mem/supervisor.json → http://localhost:<port>
//  3. hardcoded fallback: http://localhost:37701
//
// The resolved URL is fixed at construction time; never re-resolved at call time.
func resolveBaseURL(explicit string) string {
	if explicit != "" {
		return explicit
	}
	if p := readSupervisorPort(); p != 0 {
		return fmt.Sprintf("http://localhost:%d", p)
	}
	return "http://localhost:37701"
}

// httpTransport is the production Transport that hits the claude-mem HTTP daemon.
type httpTransport struct {
	client  *http.Client // carries default 10s timeout; context deadline also authoritative
	baseURL string
}

// NewHTTPTransport returns a Transport targeting the claude-mem HTTP daemon.
// baseURL="" triggers automatic resolution via resolveBaseURL (supervisor.json →
// fallback 37701). A non-empty baseURL is used verbatim.
// The http.Client carries a defensive 10 s timeout; the caller's context deadline
// is also forwarded — whichever fires first wins.
func NewHTTPTransport(baseURL string) Transport {
	return &httpTransport{
		client:  &http.Client{Timeout: 10 * time.Second},
		baseURL: resolveBaseURL(baseURL),
	}
}

// Health performs GET /health. Returns nil on 2xx.
// Maps connection errors and non-2xx to adapter.ErrUnavailable.
// Context cancellation is propagated raw.
func (t *httpTransport) Health(ctx context.Context) error {
	url := t.baseURL + "/health"
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return fmt.Errorf("%w: build health request: %v", adapter.ErrUnavailable, err)
	}

	resp, err := t.client.Do(req)
	if err != nil {
		if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
			return err
		}
		return fmt.Errorf("%w: http get /health: %v", adapter.ErrUnavailable, err)
	}
	defer resp.Body.Close()

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("%w: /health unexpected status %d", adapter.ErrUnavailable, resp.StatusCode)
	}
	return nil
}

// ListPage performs GET /api/observations?limit=<limit>&offset=<offset> and
// returns the raw JSON body. Maps connection errors and non-2xx to
// adapter.ErrUnavailable.
func (t *httpTransport) ListPage(ctx context.Context, limit, offset int) ([]byte, error) {
	url := fmt.Sprintf("%s/api/observations?limit=%d&offset=%d", t.baseURL, limit, offset)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, fmt.Errorf("%w: build list-page request: %v", adapter.ErrUnavailable, err)
	}

	resp, err := t.client.Do(req)
	if err != nil {
		if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
			return nil, err
		}
		return nil, fmt.Errorf("%w: http get /api/observations: %v", adapter.ErrUnavailable, err)
	}
	defer resp.Body.Close()

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, fmt.Errorf("%w: /api/observations unexpected status %d", adapter.ErrUnavailable, resp.StatusCode)
	}

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("%w: read /api/observations body: %v", adapter.ErrUnavailable, err)
	}
	return body, nil
}

// GetByID performs GET /api/observations?id=<id>.
//
// The claude-mem worker does NOT expose a path-style per-ID endpoint
// (`/api/observations/:id` returns Express's "Cannot GET" 404 — verified
// 2026-05-20). Instead, the id filter is a query parameter on the list
// endpoint, which returns the standard envelope. We unwrap the envelope here
// so callers receive the bare record body.
//
// Returns (recordBody, true, nil) when the envelope contains exactly one item.
// Returns (nil, false, adapter.ErrNotFound) when the envelope is empty (definitive not-found).
// Returns (nil, false, adapter.ErrUnavailable) on connection failure, non-2xx,
// envelope decode error, or unexpected multi-item response — signalling
// ReadNative to degrade to paginate+scan (ADR-5).
func (t *httpTransport) GetByID(ctx context.Context, id string) ([]byte, bool, error) {
	url := fmt.Sprintf("%s/api/observations?id=%s", t.baseURL, id)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, false, fmt.Errorf("%w: build get-by-id request: %v", adapter.ErrUnavailable, err)
	}

	resp, err := t.client.Do(req)
	if err != nil {
		if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
			return nil, false, err
		}
		return nil, false, fmt.Errorf("%w: http get /api/observations?id=%s: %v", adapter.ErrUnavailable, id, err)
	}
	defer resp.Body.Close()

	// The query endpoint returns 2xx with an empty items[] when the id doesn't
	// exist — there is no 404 path. A 404 here would indicate the route itself
	// is missing (degrade to scan via ErrUnavailable).
	if resp.StatusCode == http.StatusNotFound {
		return nil, false, fmt.Errorf("%w: /api/observations route missing", adapter.ErrUnavailable)
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, false, fmt.Errorf("%w: /api/observations?id=%s unexpected status %d", adapter.ErrUnavailable, id, resp.StatusCode)
	}

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, false, fmt.Errorf("%w: read /api/observations?id=%s body: %v", adapter.ErrUnavailable, id, err)
	}

	var envelope struct {
		Items []json.RawMessage `json:"items"`
	}
	if err := json.Unmarshal(body, &envelope); err != nil {
		return nil, false, fmt.Errorf("%w: decode envelope for id=%s: %v", adapter.ErrUnavailable, id, err)
	}
	switch len(envelope.Items) {
	case 0:
		return nil, false, adapter.ErrNotFound
	case 1:
		return []byte(envelope.Items[0]), true, nil
	default:
		return nil, false, fmt.Errorf("%w: /api/observations?id=%s returned %d items, expected ≤1",
			adapter.ErrUnavailable, id, len(envelope.Items))
	}
}
