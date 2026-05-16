// Package engram implements the adapter.Adapter interface for the engram memory
// provider. It communicates with engram via CLI (primary) and HTTP (fallback for
// ReadNative only).
package engram

import (
	"bytes"
	"context"
	"os/exec"
)

// Commander abstracts engram CLI subprocess invocation so that unit tests can
// inject a fake without running a real engram binary.
type Commander interface {
	// Run executes the engram binary with the given args under ctx.
	// Returns raw stdout/stderr bytes and any process error.
	Run(ctx context.Context, args ...string) (stdout, stderr []byte, err error)
}

// execCommander is the production Commander backed by exec.CommandContext.
type execCommander struct{}

// NewExecCommander returns a Commander that shells out to the engram binary on PATH.
func NewExecCommander() Commander {
	return &execCommander{}
}

// Run executes "engram <args>" under ctx and captures stdout/stderr.
func (e *execCommander) Run(ctx context.Context, args ...string) ([]byte, []byte, error) {
	cmd := exec.CommandContext(ctx, "engram", args...)
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	err := cmd.Run()
	return stdout.Bytes(), stderr.Bytes(), err
}
