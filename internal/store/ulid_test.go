package store

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestDefaultULIDSource_MakeReturnsNonEmpty(t *testing.T) {
	src := defaultULIDSource{}
	id := src.Make()
	assert.NotEmpty(t, id)
	assert.Len(t, id, 26, "ULID string representation is always 26 chars")
}

func TestDefaultULIDSource_MakeReturnsDifferentValues(t *testing.T) {
	src := defaultULIDSource{}
	a := src.Make()
	b := src.Make()
	assert.NotEqual(t, a, b, "two sequential ULIDs must differ")
}

func TestNewULID_DelegatesToSource(t *testing.T) {
	src := defaultULIDSource{}
	id := newULID(src)
	assert.NotEmpty(t, id)
}

// staticULIDSource is a test helper that returns a fixed sequence of IDs.
type staticULIDSource struct {
	ids []string
	idx int
}

func (s *staticULIDSource) Make() string {
	id := s.ids[s.idx]
	s.idx++
	return id
}

func TestStaticULIDSource_Injectability(t *testing.T) {
	src := &staticULIDSource{ids: []string{"AAAA", "BBBB"}}
	assert.Equal(t, "AAAA", newULID(src))
	assert.Equal(t, "BBBB", newULID(src))
}
