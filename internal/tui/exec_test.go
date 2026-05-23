package tui

import (
	"errors"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
)

// TestSyncCmd_SuccessEmitsSyncDoneMsg verifies that syncCmd returns a tea.Cmd
// that emits syncDoneMsg with nil error when the injected runner succeeds.
func TestSyncCmd_SuccessEmitsSyncDoneMsg(t *testing.T) {
	fakeRunner := func(name string, args []string) ([]byte, error) {
		return []byte("line1\nline2\n"), nil
	}

	cmd := syncCmd("/testdir", fakeRunner, "sync")
	msg := cmd() // execute the Cmd synchronously

	done, ok := msg.(syncDoneMsg)
	if !ok {
		t.Fatalf("expected syncDoneMsg, got %T", msg)
	}
	if done.err != nil {
		t.Errorf("expected nil error, got: %v", done.err)
	}
	if !strings.Contains(done.output, "line1") {
		t.Errorf("expected output to contain 'line1', got: %q", done.output)
	}
}

// TestSyncCmd_FailureEmitsSyncDoneMsgWithErr verifies error propagation.
func TestSyncCmd_FailureEmitsSyncDoneMsgWithErr(t *testing.T) {
	fakeErr := errors.New("exit status 1")
	fakeRunner := func(name string, args []string) ([]byte, error) {
		return []byte("error output"), fakeErr
	}

	cmd := syncCmd("/testdir", fakeRunner, "sync")
	msg := cmd()

	done, ok := msg.(syncDoneMsg)
	if !ok {
		t.Fatalf("expected syncDoneMsg, got %T", msg)
	}
	if done.err == nil {
		t.Error("expected non-nil error")
	}
	if !strings.Contains(done.output, "error output") {
		t.Errorf("expected output to contain 'error output', got: %q", done.output)
	}
}

// TestSyncCmd_ArgvConstructionForSync verifies args for `sync` command.
func TestSyncCmd_ArgvConstructionForSync(t *testing.T) {
	var capturedName string
	var capturedArgs []string

	fakeRunner := func(name string, args []string) ([]byte, error) {
		capturedName = name
		capturedArgs = make([]string, len(args))
		copy(capturedArgs, args)
		return []byte("ok"), nil
	}

	cmd := syncCmd("/myproject", fakeRunner, "sync")
	cmd()

	if capturedName == "" {
		t.Fatal("runner was not called")
	}
	// Expected: wrapper-mems --dir /myproject sync
	wantArgs := []string{"--dir", "/myproject", "sync"}
	if len(capturedArgs) != len(wantArgs) {
		t.Fatalf("expected args %v, got %v", wantArgs, capturedArgs)
	}
	for i, w := range wantArgs {
		if capturedArgs[i] != w {
			t.Errorf("arg[%d]: expected %q, got %q", i, w, capturedArgs[i])
		}
	}
}

// TestSyncCmd_ArgvConstructionForPull verifies args for `sync pull` command.
func TestSyncCmd_ArgvConstructionForPull(t *testing.T) {
	var capturedArgs []string

	fakeRunner := func(name string, args []string) ([]byte, error) {
		capturedArgs = make([]string, len(args))
		copy(capturedArgs, args)
		return []byte("ok"), nil
	}

	cmd := syncCmd("/myproject", fakeRunner, "sync", "pull")
	cmd()

	wantArgs := []string{"--dir", "/myproject", "sync", "pull"}
	if len(capturedArgs) != len(wantArgs) {
		t.Fatalf("expected args %v, got %v", wantArgs, capturedArgs)
	}
	for i, w := range wantArgs {
		if capturedArgs[i] != w {
			t.Errorf("arg[%d]: expected %q, got %q", i, w, capturedArgs[i])
		}
	}
}

// TestSyncCmd_ArgvConstructionForPush verifies args for `sync push` command.
func TestSyncCmd_ArgvConstructionForPush(t *testing.T) {
	var capturedArgs []string

	fakeRunner := func(name string, args []string) ([]byte, error) {
		capturedArgs = make([]string, len(args))
		copy(capturedArgs, args)
		return []byte("ok"), nil
	}

	cmd := syncCmd("/myproject", fakeRunner, "sync", "push")
	cmd()

	wantArgs := []string{"--dir", "/myproject", "sync", "push"}
	for i, w := range wantArgs {
		if i >= len(capturedArgs) {
			t.Errorf("arg[%d]: expected %q, got nothing", i, w)
			continue
		}
		if capturedArgs[i] != w {
			t.Errorf("arg[%d]: expected %q, got %q", i, w, capturedArgs[i])
		}
	}
}

// TestSyncCmd_ArgvConstructionForResolve verifies args for resolve command.
func TestSyncCmd_ArgvConstructionForResolve(t *testing.T) {
	var capturedArgs []string

	fakeRunner := func(name string, args []string) ([]byte, error) {
		capturedArgs = make([]string, len(args))
		copy(capturedArgs, args)
		return []byte("ok"), nil
	}

	cmd := syncCmd("/dir", fakeRunner, "sync", "resolve", "abc123", "--keep", "1")
	cmd()

	wantArgs := []string{"--dir", "/dir", "sync", "resolve", "abc123", "--keep", "1"}
	if len(capturedArgs) != len(wantArgs) {
		t.Fatalf("expected args %v, got %v", wantArgs, capturedArgs)
	}
	for i, w := range wantArgs {
		if capturedArgs[i] != w {
			t.Errorf("arg[%d]: expected %q, got %q", i, w, capturedArgs[i])
		}
	}
}

// TestSyncCmd_ReturnsTeeMsg asserts the return type is a tea.Cmd (callable).
func TestSyncCmd_ReturnsTeeMsg(t *testing.T) {
	fakeRunner := func(name string, args []string) ([]byte, error) {
		return []byte("done"), nil
	}
	var cmd tea.Cmd = syncCmd("/d", fakeRunner, "sync")
	if cmd == nil {
		t.Fatal("syncCmd returned nil tea.Cmd")
	}
}
