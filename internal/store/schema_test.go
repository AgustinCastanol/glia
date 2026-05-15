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
