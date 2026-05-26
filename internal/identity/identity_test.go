package identity

import (
	"strings"
	"testing"
)

// TestDefault_Pattern verifies that Default() returns a non-empty string matching
// hostname:username pattern (REQ-IDN-01 step 3).
func TestDefault_Pattern(t *testing.T) {
	got := Default()
	if got == "" {
		t.Fatal("Default() must not be empty")
	}
	if !strings.Contains(got, ":") {
		t.Errorf("Default() should contain ':' separator; got %q", got)
	}
}

// TestResolve_EnvBeatsConfig verifies env var beats user config author.
// REQ-IDN-01: Scenario "Env var beats user config".
func TestResolve_EnvBeatsConfig(t *testing.T) {
	t.Setenv("WRAPPER_MEMS_AUTHOR", "bob")
	got := Resolve("alice")
	if got != "bob" {
		t.Errorf("Resolve: got %q, want %q (env beats user config)", got, "bob")
	}
}

// TestResolve_EnvBeatsEmpty verifies env var beats empty user config.
// REQ-IDN-01.
func TestResolve_EnvBeatsEmpty(t *testing.T) {
	t.Setenv("WRAPPER_MEMS_AUTHOR", "ci-agent")
	got := Resolve("")
	if got != "ci-agent" {
		t.Errorf("Resolve: got %q, want %q", got, "ci-agent")
	}
}

// TestResolve_ExplicitAuthorBeatsDefault verifies explicit config author beats Default().
// REQ-IDN-01: Scenario "Explicit author beats Default".
func TestResolve_ExplicitAuthorBeatsDefault(t *testing.T) {
	t.Setenv("WRAPPER_MEMS_AUTHOR", "") // ensure env is cleared
	got := Resolve("alice")
	if got != "alice" {
		t.Errorf("Resolve: got %q, want %q (user cfg author)", got, "alice")
	}
}

// TestResolve_DefaultWhenNoEnvAndNoConfig verifies Default() is used when both
// env and user config are absent.
func TestResolve_DefaultWhenNoEnvAndNoConfig(t *testing.T) {
	t.Setenv("WRAPPER_MEMS_AUTHOR", "")
	got := Resolve("")
	if got == "" {
		t.Error("Resolve(\"\") must not return empty string")
	}
	if !strings.Contains(got, ":") {
		t.Errorf("Resolve(\"\") should return hostname:username format; got %q", got)
	}
}
