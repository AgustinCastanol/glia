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
	SchemaVersion         int    `json:"schema_version"`
	CreatedAt             string `json:"created_at"`
	// WrapperMemsMinVersion is the minimum binary version required to operate on
	// this store. An empty or absent value means any version is accepted
	// (permissive default — D6). Commands that read a store call
	// config.Refuse(Version, WrapperMemsMinVersion) before any I/O.
	WrapperMemsMinVersion string `json:"wrapper_mems_min_version,omitempty" yaml:"wrapper_mems_min_version,omitempty"`
}

// SchemaInfo is the public representation of schema.json fields exposed to
// callers outside the store package (e.g. cmd layer for version-refusal guard).
type SchemaInfo struct {
	SchemaVersion         int    `json:"schema_version"`
	CreatedAt             string `json:"created_at"`
	WrapperMemsMinVersion string `json:"wrapper_mems_min_version,omitempty"`
}

// ReadSchema reads schema.json from storeDir and returns its public fields.
// Returns an error if the file is missing or malformed. Commands use this to
// obtain WrapperMemsMinVersion for the version-refusal guard (REQ-CFG-04).
func ReadSchema(storeDir string) (SchemaInfo, error) {
	data, err := os.ReadFile(filepath.Join(storeDir, "schema.json"))
	if err != nil {
		return SchemaInfo{}, fmt.Errorf("store.ReadSchema: %w", err)
	}
	var sf schemaFile
	if err := json.Unmarshal(data, &sf); err != nil {
		return SchemaInfo{}, fmt.Errorf("store.ReadSchema: parse: %w", err)
	}
	return SchemaInfo{
		SchemaVersion:         sf.SchemaVersion,
		CreatedAt:             sf.CreatedAt,
		WrapperMemsMinVersion: sf.WrapperMemsMinVersion,
	}, nil
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
