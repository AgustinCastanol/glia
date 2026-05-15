package store

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func openRW(t *testing.T, path string) *os.File {
	t.Helper()
	f, err := os.OpenFile(path, os.O_RDWR|os.O_CREATE, 0644)
	require.NoError(t, err)
	t.Cleanup(func() { f.Close() })
	return f
}

func TestRecoverPartialLine_CleanFile_NoTruncation(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "memory.jsonl")
	// Three complete lines.
	content := "line1\nline2\nline3\n"
	require.NoError(t, os.WriteFile(path, []byte(content), 0644))

	f := openRW(t, path)
	discarded, err := recoverPartialLine(f)
	require.NoError(t, err)
	assert.Equal(t, int64(0), discarded)

	// File should be unchanged.
	data, _ := os.ReadFile(path)
	assert.Equal(t, content, string(data))
}

func TestRecoverPartialLine_PartialTrailingLine_ScenarioG(t *testing.T) {
	// Scenario G: fixture has 3 complete lines + partial 4th line (no trailing \n).
	dir := t.TempDir()
	path := filepath.Join(dir, "memory.jsonl")

	fixture, err := os.ReadFile(filepath.Join("testdata", "partial_trailing_line.jsonl"))
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(path, fixture, 0644))

	f := openRW(t, path)
	discarded, err := recoverPartialLine(f)
	require.NoError(t, err)
	assert.Greater(t, discarded, int64(0))

	// File must now end with \n.
	data, _ := os.ReadFile(path)
	require.NotEmpty(t, data)
	assert.Equal(t, byte('\n'), data[len(data)-1])
}

func TestRecoverPartialLine_NoNewlineAnywhere_ScenarioH(t *testing.T) {
	// Scenario H: file has arbitrary bytes, no \n — truncate to 0.
	dir := t.TempDir()
	path := filepath.Join(dir, "memory.jsonl")

	fixture, err := os.ReadFile(filepath.Join("testdata", "no_newline_anywhere.jsonl"))
	require.NoError(t, err)
	require.NotEmpty(t, fixture)
	require.NoError(t, os.WriteFile(path, fixture, 0644))

	f := openRW(t, path)
	discarded, err := recoverPartialLine(f)
	require.NoError(t, err)
	assert.Equal(t, int64(len(fixture)), discarded)

	info, _ := os.Stat(path)
	assert.Equal(t, int64(0), info.Size())
}

func TestRecoverPartialLine_EmptyFile_Noop(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "memory.jsonl")
	require.NoError(t, os.WriteFile(path, []byte{}, 0644))

	f := openRW(t, path)
	discarded, err := recoverPartialLine(f)
	require.NoError(t, err)
	assert.Equal(t, int64(0), discarded)
}

func TestRecoverPartialLine_SingleByteLF_Noop(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "memory.jsonl")
	require.NoError(t, os.WriteFile(path, []byte{'\n'}, 0644))

	f := openRW(t, path)
	discarded, err := recoverPartialLine(f)
	require.NoError(t, err)
	assert.Equal(t, int64(0), discarded)
}

func TestRecoverPartialLine_LargeFile_TruncatesToLastNewline(t *testing.T) {
	// Write a file larger than 4096 bytes with a partial trailing line.
	dir := t.TempDir()
	path := filepath.Join(dir, "memory.jsonl")

	// 10 complete lines of 500 bytes each, then partial line.
	line := make([]byte, 499)
	for i := range line {
		line[i] = 'x'
	}
	line = append(line, '\n')

	var buf []byte
	for i := 0; i < 10; i++ {
		buf = append(buf, line...)
	}
	// Append partial line (no \n).
	buf = append(buf, []byte("partial-data")...)
	require.NoError(t, os.WriteFile(path, buf, 0644))

	sizeBefore := int64(len(buf))
	f := openRW(t, path)
	discarded, err := recoverPartialLine(f)
	require.NoError(t, err)
	assert.Greater(t, discarded, int64(0))
	assert.Less(t, discarded, sizeBefore)

	data, _ := os.ReadFile(path)
	assert.Equal(t, byte('\n'), data[len(data)-1])
}
