package imapbackend_test

import (
	"testing"

	"github.com/godwit-email/godwit/backend/imapbackend"
	"github.com/stretchr/testify/assert"
)

// The encoding is the one the schema migration writes (core/store).
func TestEncodeDecode(t *testing.T) {
	assert.Equal(t, "0000000007:0000000042", imapbackend.Encode(7, 42))
	assert.Equal(t, "4294967295:4294967295", imapbackend.Encode(4294967295, 4294967295))
	v, n := imapbackend.Decode("0000000007:0000000042")
	assert.Equal(t, [2]uint32{7, 42}, [2]uint32{v, n})
	for _, bad := range []string{"", "7", "x:1", "1:99999999999"} {
		v, n := imapbackend.Decode(bad)
		assert.Equal(t, [2]uint32{0, 0}, [2]uint32{v, n}, bad)
	}
	assert.Less(t, imapbackend.Encode(7, 9), imapbackend.Encode(7, 10), "a validity's refs sort by UID")
}
