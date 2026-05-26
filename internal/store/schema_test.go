package store

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestLoadOrBootstrapSchema_MissingCreatesWithVersion1(t *testing.T) {
	dir := t.TempDir()
	schemaPath := filepath.Join(dir, "schema.json")

	err := loadOrBootstrapSchema(schemaPath)
	require.NoError(t, err)

	data, err := os.ReadFile(schemaPath)
	require.NoError(t, err)

	var sf schemaFile
	require.NoError(t, json.Unmarshal(data, &sf))
	assert.Equal(t, 1, sf.SchemaVersion)
	assert.NotEmpty(t, sf.CreatedAt)
}

func TestLoadOrBootstrapSchema_ExistingVersion1Proceeds(t *testing.T) {
	dir := t.TempDir()
	schemaPath := filepath.Join(dir, "schema.json")

	// Use the fixture file content.
	src := filepath.Join("testdata", "schema", "schema_v1.json")
	data, err := os.ReadFile(src)
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(schemaPath, data, 0644))

	err = loadOrBootstrapSchema(schemaPath)
	assert.NoError(t, err)
}

func TestLoadOrBootstrapSchema_TooNewReturnsErrSchemaTooNew(t *testing.T) {
	dir := t.TempDir()
	schemaPath := filepath.Join(dir, "schema.json")

	src := filepath.Join("testdata", "schema", "schema_too_new.json")
	data, err := os.ReadFile(src)
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(schemaPath, data, 0644))

	err = loadOrBootstrapSchema(schemaPath)
	require.Error(t, err)
	assert.True(t, errors.Is(err, ErrSchemaTooNew))
}

func TestLoadOrBootstrapSchema_TooNewErrorsIs(t *testing.T) {
	dir := t.TempDir()
	schemaPath := filepath.Join(dir, "schema.json")

	sf := schemaFile{SchemaVersion: 9999, CreatedAt: "2024-01-01T00:00:00Z"}
	data, err := json.Marshal(sf)
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(schemaPath, data, 0644))

	err = loadOrBootstrapSchema(schemaPath)
	require.Error(t, err)
	assert.True(t, errors.Is(err, ErrSchemaTooNew), "expected errors.Is(err, ErrSchemaTooNew) == true")
}

// TestSchemaFile_WrapperMemsMinVersion_RoundTrip verifies the new field
// round-trips through JSON marshal/unmarshal (T-1.7).
func TestSchemaFile_WrapperMemsMinVersion_RoundTrip(t *testing.T) {
	sf := schemaFile{
		SchemaVersion:         1,
		CreatedAt:             "2026-05-25T00:00:00Z",
		WrapperMemsMinVersion: "v0.2.0",
	}
	data, err := json.Marshal(sf)
	require.NoError(t, err)

	var out schemaFile
	require.NoError(t, json.Unmarshal(data, &out))
	assert.Equal(t, "v0.2.0", out.WrapperMemsMinVersion)
}

// TestSchemaFile_WrapperMemsMinVersion_OmittedWhenEmpty verifies that an empty
// WrapperMemsMinVersion is omitted from the JSON output (omitempty — T-1.7).
func TestSchemaFile_WrapperMemsMinVersion_OmittedWhenEmpty(t *testing.T) {
	sf := schemaFile{SchemaVersion: 1, CreatedAt: "2026-05-25T00:00:00Z"}
	data, err := json.Marshal(sf)
	require.NoError(t, err)

	// The JSON must NOT contain the key when the field is empty.
	var raw map[string]any
	require.NoError(t, json.Unmarshal(data, &raw))
	_, present := raw["wrapper_mems_min_version"]
	assert.False(t, present, "wrapper_mems_min_version must be absent when empty (omitempty)")
}

// TestReadSchema_RoundTrip verifies that ReadSchema reads the field correctly.
func TestReadSchema_RoundTrip(t *testing.T) {
	dir := t.TempDir()
	sf := schemaFile{
		SchemaVersion:         1,
		CreatedAt:             "2026-05-25T00:00:00Z",
		WrapperMemsMinVersion: "v1.0.0",
	}
	data, err := json.Marshal(sf)
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(filepath.Join(dir, "schema.json"), data, 0o644))

	info, err := ReadSchema(dir)
	require.NoError(t, err)
	assert.Equal(t, "v1.0.0", info.WrapperMemsMinVersion)
	assert.Equal(t, 1, info.SchemaVersion)
}

func TestAtomicWriteJSON_RoundTrip(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "out.json")

	type payload struct {
		Key string `json:"key"`
		Val int    `json:"val"`
	}
	in := payload{Key: "hello", Val: 42}
	require.NoError(t, atomicWriteJSON(path, in))

	data, err := os.ReadFile(path)
	require.NoError(t, err)

	var out payload
	require.NoError(t, json.Unmarshal(data, &out))
	assert.Equal(t, in, out)
}
