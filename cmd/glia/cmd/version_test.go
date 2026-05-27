package cmd

import (
	"bytes"
	"os"
	"path/filepath"
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
// "glia <version> (schema <range>)\n" (REQ-VER-01).
func TestVersion_OutputFormat(t *testing.T) {
	orig := Version
	Version = "dev"
	t.Cleanup(func() { Version = orig })

	buf := &bytes.Buffer{}
	versionCmd.SetOut(buf)
	versionCmd.SetErr(buf)

	versionCmd.Run(versionCmd, nil)

	want := "glia dev (schema v1)\n"
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

// TestEnforceMinVersion_MissingSchemaIsPermissive verifies that a store with no
// schema.json does not trigger version refusal (REQ-CFG-04).
func TestEnforceMinVersion_MissingSchemaIsPermissive(t *testing.T) {
	dir := t.TempDir()
	if err := enforceMinVersion(dir); err != nil {
		t.Fatalf("expected nil for missing schema.json, got: %v", err)
	}
}

// TestEnforceMinVersion_EmptyMinIsPermissive verifies that a schema.json without
// glia_min_version is permissive (REQ-CFG-04 / config.Refuse).
func TestEnforceMinVersion_EmptyMinIsPermissive(t *testing.T) {
	dir := t.TempDir()
	writeSchema(t, dir, `{"schema_version":1,"created_at":"2026-01-01T00:00:00Z"}`)

	orig := Version
	Version = "v0.1.0"
	t.Cleanup(func() { Version = orig })

	if err := enforceMinVersion(dir); err != nil {
		t.Fatalf("expected nil for empty min_version, got: %v", err)
	}
}

// TestEnforceMinVersion_BinaryTooOldRefuses verifies that glia_min_version
// greater than the binary Version produces a refusal error (REQ-CFG-04).
func TestEnforceMinVersion_BinaryTooOldRefuses(t *testing.T) {
	dir := t.TempDir()
	writeSchema(t, dir, `{"schema_version":1,"created_at":"2026-01-01T00:00:00Z","glia_min_version":"v2.0.0"}`)

	orig := Version
	Version = "v0.5.0"
	t.Cleanup(func() { Version = orig })

	err := enforceMinVersion(dir)
	if err == nil {
		t.Fatal("expected refusal error, got nil")
	}
	if !strings.Contains(err.Error(), "v2.0.0") {
		t.Errorf("expected error to mention required version v2.0.0, got: %v", err)
	}
}

// TestEnforceMinVersion_BinaryAtOrAboveOK verifies that binary >= min_version
// does not refuse (REQ-CFG-04).
func TestEnforceMinVersion_BinaryAtOrAboveOK(t *testing.T) {
	dir := t.TempDir()
	writeSchema(t, dir, `{"schema_version":1,"created_at":"2026-01-01T00:00:00Z","glia_min_version":"v0.1.0"}`)

	orig := Version
	Version = "v0.1.0"
	t.Cleanup(func() { Version = orig })

	if err := enforceMinVersion(dir); err != nil {
		t.Fatalf("expected nil for binary >= min_version, got: %v", err)
	}
}

// TestEnforceMinVersion_DevSatisfiesAnyMin verifies that a "dev" binary is
// treated as infinitely high and satisfies any min_version (config.CompareVersion contract).
func TestEnforceMinVersion_DevSatisfiesAnyMin(t *testing.T) {
	dir := t.TempDir()
	writeSchema(t, dir, `{"schema_version":1,"created_at":"2026-01-01T00:00:00Z","glia_min_version":"v99.0.0"}`)

	orig := Version
	Version = "dev"
	t.Cleanup(func() { Version = orig })

	if err := enforceMinVersion(dir); err != nil {
		t.Fatalf("expected dev to satisfy any min_version, got: %v", err)
	}
}

// TestEnforceMinVersion_CorruptSchemaSurfacesError verifies that an unreadable or
// malformed schema.json surfaces the error rather than being silently permissive.
func TestEnforceMinVersion_CorruptSchemaSurfacesError(t *testing.T) {
	dir := t.TempDir()
	writeSchema(t, dir, `{not valid json`)

	err := enforceMinVersion(dir)
	if err == nil {
		t.Fatal("expected error for corrupt schema.json, got nil")
	}
	if !strings.Contains(err.Error(), "read schema") {
		t.Errorf("expected error to wrap 'read schema', got: %v", err)
	}
}

func writeSchema(t *testing.T, dir, body string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, "schema.json"), []byte(body), 0o644); err != nil {
		t.Fatalf("write schema.json: %v", err)
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
