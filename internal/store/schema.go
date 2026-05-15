package store

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"time"
)

// StoreSupportedVersion is the maximum schema_version this build can read/write.
// A schema.json with a higher value triggers ErrSchemaTooNew on Open.
const StoreSupportedVersion = 1

type schemaFile struct {
	SchemaVersion int    `json:"schema_version"`
	CreatedAt     string `json:"created_at"`
}

// loadOrBootstrapSchema reads schema.json at schemaPath. If the file does not
// exist it writes a fresh one with version=StoreSupportedVersion. If the file
// exists and its version exceeds StoreSupportedVersion it returns ErrSchemaTooNew.
func loadOrBootstrapSchema(schemaPath string) error {
	data, err := os.ReadFile(schemaPath)
	if os.IsNotExist(err) {
		sf := schemaFile{
			SchemaVersion: StoreSupportedVersion,
			CreatedAt:     time.Now().UTC().Format(time.RFC3339Nano),
		}
		return atomicWriteJSON(schemaPath, sf)
	}
	if err != nil {
		return fmt.Errorf("schema: read: %w", err)
	}
	var sf schemaFile
	if err := json.Unmarshal(data, &sf); err != nil {
		return fmt.Errorf("schema: parse: %w", err)
	}
	if sf.SchemaVersion > StoreSupportedVersion {
		return fmt.Errorf("schema: version %d > supported %d: %w", sf.SchemaVersion, StoreSupportedVersion, ErrSchemaTooNew)
	}
	return nil
}

// atomicWriteJSON marshals v to JSON and writes it atomically to path using
// a temp file + rename. Used by schema.go and index.go.
func atomicWriteJSON(path string, v any) error {
	data, err := json.Marshal(v)
	if err != nil {
		return fmt.Errorf("atomicWriteJSON marshal: %w", err)
	}
	dir := filepath.Dir(path)
	tmp, err := os.CreateTemp(dir, ".tmp-")
	if err != nil {
		return fmt.Errorf("atomicWriteJSON create temp: %w", err)
	}
	tmpName := tmp.Name()
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		os.Remove(tmpName)
		return fmt.Errorf("atomicWriteJSON write: %w", err)
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		os.Remove(tmpName)
		return fmt.Errorf("atomicWriteJSON sync: %w", err)
	}
	if err := tmp.Close(); err != nil {
		os.Remove(tmpName)
		return fmt.Errorf("atomicWriteJSON close: %w", err)
	}
	if err := os.Rename(tmpName, path); err != nil {
		os.Remove(tmpName)
		return fmt.Errorf("atomicWriteJSON rename: %w", err)
	}
	return nil
}
