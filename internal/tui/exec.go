package tui

import (
	"bytes"
	"fmt"
	"os"
	"os/exec"
	"strings"

	tea "github.com/charmbracelet/bubbletea"
)

// syncDoneMsg is sent to the root model when a sync subprocess completes.
type syncDoneMsg struct {
	// output is the combined stdout+stderr from the subprocess.
	output string
	// err is non-nil if the subprocess exited non-zero.
	err error
}

// execRunner is a function type that matches exec.Command's run behavior.
// Injected in tests to avoid spawning real subprocesses.
type execRunner func(name string, args []string) ([]byte, error)

// runCommand executes name with args and returns combined stdout+stderr.
// It is the real implementation used in production.
func runCommand(name string, args ...string) ([]byte, error) {
	cmd := exec.Command(name, args...)
	var buf bytes.Buffer
	cmd.Stdout = &buf
	cmd.Stderr = &buf
	err := cmd.Run()
	return buf.Bytes(), err
}

// runCommandExec is the production execRunner: adapts runCommand (variadic) to
// the execRunner signature (slice). Pass this to syncCmd in production code.
func runCommandExec(name string, args []string) ([]byte, error) {
	return runCommand(name, args...)
}

// syncCmd returns a tea.Cmd that runs `glia --dir <dir> <args...>` as a
// subprocess, captures its combined stdout/stderr, and emits syncDoneMsg when
// the subprocess exits. The spinner ticks while this Cmd runs on Bubble Tea's
// off-UI goroutine. (REQ-TUI-10)
//
// runner must not be nil. In production pass runCommandExec; in tests inject a
// fake runner so no real subprocess is spawned.
func syncCmd(dir string, runner execRunner, args ...string) tea.Cmd {
	if runner == nil {
		panic("syncCmd: runner must not be nil — pass runCommandExec in production")
	}
	return func() tea.Msg {
		// Build argument list: --dir <dir> <args...>
		fullArgs := append([]string{"--dir", dir}, args...)
		out, err := runner(os.Args[0], fullArgs)

		// Trim trailing whitespace for clean overlay display.
		output := strings.TrimRight(string(out), "\n")

		if err != nil {
			return syncDoneMsg{
				output: fmt.Sprintf("%s\n\nerror: %v", output, err),
				err:    err,
			}
		}
		return syncDoneMsg{output: output}
	}
}
