package cmd

import (
	"bytes"
	"strings"
	"testing"
)

// TestVersion_DefaultOutput verifies that the version command prints the
// default "dev" string and the schema version range (REQ-VER-01 dev scenario).
func TestVersion_DefaultOutput(t *testing.T) {
	// Ensure Version is the default "dev" for this test.
	orig := Version
	Version = "dev"
	t.Cleanup(func() { Version = orig })

	buf := &bytes.Buffer{}
	versionCmd.SetOut(buf)
	versionCmd.SetErr(buf)

	versionCmd.Run(versionCmd, nil)

	got := buf.String()
	if !strings.Contains(got, "dev") {
		t.Errorf("expected output to contain %q, got: %q", "dev", got)
	}
	if !strings.Contains(got, SchemaVersionRange) {
		t.Errorf("expected output to contain schema range %q, got: %q", SchemaVersionRange, got)
	}
}

// TestVersion_OutputFormat verifies the exact output format:
// "wrapper-mems <version> (schema <range>)\n" (REQ-VER-01).
func TestVersion_OutputFormat(t *testing.T) {
	orig := Version
	Version = "dev"
	t.Cleanup(func() { Version = orig })

	buf := &bytes.Buffer{}
	versionCmd.SetOut(buf)
	versionCmd.SetErr(buf)

	versionCmd.Run(versionCmd, nil)

	want := "wrapper-mems dev (schema v1)\n"
	if got := buf.String(); got != want {
		t.Errorf("version output:\n  got:  %q\n  want: %q", got, want)
	}
}

// TestVersion_LdflajsInjected verifies the output when Version is overridden
// (simulates what -ldflags does at release build time — REQ-VER-01 release scenario).
func TestVersion_LdflagsInjected(t *testing.T) {
	orig := Version
	Version = "v0.1.0"
	t.Cleanup(func() { Version = orig })

	buf := &bytes.Buffer{}
	versionCmd.SetOut(buf)
	versionCmd.SetErr(buf)

	versionCmd.Run(versionCmd, nil)

	got := buf.String()
	if !strings.Contains(got, "v0.1.0") {
		t.Errorf("expected output to contain %q, got: %q", "v0.1.0", got)
	}
}

// TestVersion_SchemaVersionRange verifies the SchemaVersionRange constant value.
func TestVersion_SchemaVersionRange(t *testing.T) {
	if SchemaVersionRange != "v1" {
		t.Errorf("SchemaVersionRange = %q, want %q", SchemaVersionRange, "v1")
	}
}

// TestVersion_HelpTextNoTelemetry verifies that the root command's Long description
// contains an explicit "no telemetry" statement (REQ-TEL-01).
func TestVersion_HelpTextNoTelemetry(t *testing.T) {
	long := rootCmd.Long
	lowerLong := strings.ToLower(long)
	if !strings.Contains(lowerLong, "telemetry") {
		t.Error("root command Long description does not mention 'telemetry' (REQ-TEL-01)")
	}
	if !strings.Contains(lowerLong, "no telemetry") {
		t.Errorf("root command Long description does not say 'no telemetry'; got: %q", long)
	}
}
