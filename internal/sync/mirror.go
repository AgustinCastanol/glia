package sync

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os/exec"
	"time"
)

// mirrorEngram shells out to `engram sync --project <project>` to push the
// canonical store's records into the .engram/ chunk files (D11 / REQ-SE-40..43).
//
// A 30-second timeout is applied (REQ-SE-41). Non-zero exit codes and timeout
// are WARNING-only: sync still exits 0 unless other failures exist (REQ-SE-42).
// If the engram binary is not on PATH the call is skipped with a warning (REQ-SE-43).
func (e *Engine) mirrorEngram(project string) {
	timeoutSecs := e.cfg.MirrorTimeoutSeconds
	if timeoutSecs <= 0 {
		timeoutSecs = 30
	}

	ctx, cancel := context.WithTimeout(context.Background(), time.Duration(timeoutSecs)*time.Second)
	defer cancel()

	args := []string{"sync"}
	if project != "" {
		args = append(args, "--project", project)
	}

	cmd := exec.CommandContext(ctx, "engram", args...)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr

	err := cmd.Run()
	if err == nil {
		return
	}

	// Timeout case.
	if errors.Is(ctx.Err(), context.DeadlineExceeded) {
		fmt.Fprintf(e.w, "WARN mirror-engram timed out after %ds\n", timeoutSecs)
		return
	}

	// Binary not found.
	var exitErr *exec.ExitError
	if !errors.As(err, &exitErr) {
		// exec.ErrNotFound or path error — binary missing.
		fmt.Fprintf(e.w, "WARN mirror-engram: engram not found on PATH, skipping\n")
		return
	}

	// Non-zero exit.
	stderrStr := stderr.String()
	if stderrStr == "" {
		stderrStr = err.Error()
	}
	fmt.Fprintf(e.w, "WARN mirror-engram failed: %s\n", stderrStr)
}

// mirrorEngramEnabled reports whether the mirror-engram shell-out should run,
// combining config.yaml setting and opts.MirrorEngram flag (REQ-SE-10).
func (e *Engine) mirrorEngramEnabled() bool {
	return e.cfg.MirrorEngram || e.opts.MirrorEngram
}
