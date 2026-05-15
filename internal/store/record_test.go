package store

import (
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func validObservation() CanonicalRecord {
	return CanonicalRecord{
		CanonicalID:   "01HZZZZZZZZZZZZZZZZZZZAAAA",
		LineULID:      "01HZZZZZZZZZZZZZZZZZZZBBBB",
		SchemaVersion: 1,
		Kind:          "observation",
		Revision:      1,
		ContentFormat: "text/plain",
		Content:       "hello",
		Title:         "test",
	}
}

func TestValidateRecord_ValidObservation(t *testing.T) {
	err := validateRecord(validObservation())
	assert.NoError(t, err)
}

func TestValidateRecord_ValidSessionSummary(t *testing.T) {
	r := validObservation()
	r.Kind = "session_summary"
	err := validateRecord(r)
	assert.NoError(t, err)
}

func TestValidateRecord_ValidRelation(t *testing.T) {
	r := validObservation()
	r.Kind = "relation"
	err := validateRecord(r)
	assert.NoError(t, err)
}

func TestValidateRecord_EmptyKind(t *testing.T) {
	r := validObservation()
	r.Kind = ""
	err := validateRecord(r)
	require.Error(t, err)
	assert.True(t, errors.Is(err, ErrInvalidRecord))
}

func TestValidateRecord_UnknownKind(t *testing.T) {
	r := validObservation()
	r.Kind = "bogus"
	err := validateRecord(r)
	require.Error(t, err)
	assert.True(t, errors.Is(err, ErrInvalidRecord))
}

func TestValidateRecord_EmptyContentFormatOnNonTombstone(t *testing.T) {
	r := validObservation()
	r.ContentFormat = ""
	err := validateRecord(r)
	require.Error(t, err)
	assert.True(t, errors.Is(err, ErrInvalidRecord))
}

func TestValidateRecord_ValidTombstone(t *testing.T) {
	r := CanonicalRecord{
		CanonicalID:   "01HZZZZZZZZZZZZZZZZZZZAAAA",
		LineULID:      "01HZZZZZZZZZZZZZZZZZZZBBBB",
		SchemaVersion: 1,
		Kind:          "observation",
		Revision:      2,
		Supersedes:    "01HZZZZZZZZZZZZZZZZZZZAAAA",
		Deleted:       true,
	}
	err := validateRecord(r)
	assert.NoError(t, err)
}

func TestValidateRecord_TombstoneSupersedes_MustMatchCanonicalID(t *testing.T) {
	r := CanonicalRecord{
		CanonicalID:   "01HZZZZZZZZZZZZZZZZZZZAAAA",
		LineULID:      "01HZZZZZZZZZZZZZZZZZZZBBBB",
		SchemaVersion: 1,
		Kind:          "observation",
		Revision:      2,
		Supersedes:    "01HZZZZZZZZZZZZZZZZZZZDIFFERENT",
		Deleted:       true,
	}
	err := validateRecord(r)
	require.Error(t, err)
	assert.True(t, errors.Is(err, ErrInvalidRecord))
}

func TestDecodeLineSkipsUnknownSchemaVersion(t *testing.T) {
	line := []byte(`{"canonical_id":"01HZZZZZZZZZZZZZZZZZZZAAAA","line_ulid":"01HZZZZZZZZZZZZZZZZZZZBBBB","schema_version":9999,"kind":"observation","revision":1,"supersedes":"","deleted":false,"title":"future","content":"x","content_format":"text/plain","origin":{"provider":"","provider_id":"","author":"","session_id":""},"created_at":"","updated_at":"","tags":[],"topic_key":"","type":""}`)
	_, ok, err := decodeLine(line)
	require.NoError(t, err)
	assert.False(t, ok, "line with schema_version > 1 should be skipped")
}

func TestDecodeLineAcceptsKnownSchemaVersion(t *testing.T) {
	line := []byte(`{"canonical_id":"01HZZZZZZZZZZZZZZZZZZZAAAA","line_ulid":"01HZZZZZZZZZZZZZZZZZZZBBBB","schema_version":1,"kind":"observation","revision":1,"supersedes":"","deleted":false,"title":"test","content":"hello","content_format":"text/plain","origin":{"provider":"","provider_id":"","author":"","session_id":""},"created_at":"","updated_at":"","tags":[],"topic_key":"","type":""}`)
	r, ok, err := decodeLine(line)
	require.NoError(t, err)
	assert.True(t, ok)
	assert.Equal(t, "01HZZZZZZZZZZZZZZZZZZZAAAA", r.CanonicalID)
	assert.Equal(t, "01HZZZZZZZZZZZZZZZZZZZBBBB", r.LineULID)
}

func TestDecodeLineReturnsErrorOnInvalidJSON(t *testing.T) {
	_, _, err := decodeLine([]byte(`not json`))
	require.Error(t, err)
}
