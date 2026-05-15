package store

import (
	"github.com/oklog/ulid/v2"
)

// ulidSource is the injectable ULID generator interface.
// Tests may replace it with a deterministic implementation.
type ulidSource interface {
	Make() string
}

// defaultULIDSource uses oklog/ulid to generate monotonic ULIDs.
type defaultULIDSource struct{}

func (defaultULIDSource) Make() string {
	return ulid.Make().String()
}

func newULID(src ulidSource) string {
	return src.Make()
}
