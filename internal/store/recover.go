package store

import (
	"fmt"
	"io"
	"os"
)

// RecoverPartialLine inspects f for a partial trailing line (no terminating '\n').
// If found, it truncates the file to the position after the last '\n'.
// If no '\n' exists anywhere in the file, it truncates to 0.
// Returns the number of bytes discarded (0 means the file was already clean).
// The file must be opened O_RDWR. After return the file offset is undefined;
// callers should close and re-open for append.
//
// Exported so that doctor --fix can invoke partial-line recovery on memory.jsonl
// without opening the store (which acquires the advisory lock). REQ-DOC-03.
func RecoverPartialLine(f *os.File) (int64, error) {
	return recoverPartialLine(f)
}

func recoverPartialLine(f *os.File) (int64, error) {
	info, err := f.Stat()
	if err != nil {
		return 0, fmt.Errorf("recover: stat: %w", err)
	}
	size := info.Size()
	if size == 0 {
		return 0, nil // empty file is always clean
	}

	// Check the last byte.
	lastByte := make([]byte, 1)
	if _, err := f.ReadAt(lastByte, size-1); err != nil {
		return 0, fmt.Errorf("recover: read last byte: %w", err)
	}
	if lastByte[0] == '\n' {
		return 0, nil // file ends cleanly
	}

	// Tail is partial — scan backwards for last '\n'.
	const bufSize = 4096
	pos := size
	truncateTo := int64(-1)

outer:
	for pos > 0 {
		readSize := int64(bufSize)
		if readSize > pos {
			readSize = pos
		}
		pos -= readSize
		buf := make([]byte, readSize)
		if _, err := f.ReadAt(buf, pos); err != nil && err != io.EOF {
			return 0, fmt.Errorf("recover: read at %d: %w", pos, err)
		}
		for i := len(buf) - 1; i >= 0; i-- {
			if buf[i] == '\n' {
				truncateTo = pos + int64(i) + 1 // byte AFTER the newline
				break outer
			}
		}
	}

	if truncateTo < 0 {
		// No '\n' anywhere — discard everything.
		if err := f.Truncate(0); err != nil {
			return 0, fmt.Errorf("recover: truncate to 0: %w", err)
		}
		if err := f.Sync(); err != nil {
			return 0, fmt.Errorf("recover: sync after truncate to 0: %w", err)
		}
		return size, nil
	}

	discarded := size - truncateTo
	if err := f.Truncate(truncateTo); err != nil {
		return 0, fmt.Errorf("recover: truncate to %d: %w", truncateTo, err)
	}
	if err := f.Sync(); err != nil {
		return 0, fmt.Errorf("recover: sync after truncate: %w", err)
	}
	return discarded, nil
}
