package paging_test

import (
	"testing"

	"github.com/buildbuddy-io/buildbuddy/server/util/paging"
	"github.com/buildbuddy-io/buildbuddy/server/util/status"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	pgpb "github.com/buildbuddy-io/buildbuddy/proto/pagination"
)

func TestDecodeAndEncodeOffsetLimit(t *testing.T) {
	in := &pgpb.OffsetLimit{Offset: 1000, Limit: 100}

	str, err := paging.EncodeOffsetLimit(in)
	require.NoError(t, err)
	out, err := paging.DecodeOffsetLimit(str)
	require.NoError(t, err)

	assert.Equal(t, in.Offset, out.Offset, "unexpected Offset")
	assert.Equal(t, in.Limit, out.Limit, "unexpected Limit")
}

func TestDecodeEmptyStringToEmptyProto(t *testing.T) {
	page, err := paging.DecodeOffsetLimit("")
	require.NoError(t, err)

	assert.Equal(t, int64(0), page.Offset)
	assert.Equal(t, int64(0), page.Limit)
}

func TestPaginationMalformedEncoding(t *testing.T) {
	for _, token := range []string{"!", "not_base64", "AA==", "/w=="} {
		t.Run(token, func(t *testing.T) {
			_, err := paging.DecodeOffsetLimit(token)
			require.True(t, status.IsInvalidArgumentError(err), "token %q: got %v", token, err)
		})
	}
}
