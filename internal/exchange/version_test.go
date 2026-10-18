package exchange_test

import (
	"testing"

	"github.com/bitmagnet-io/bitmagnet/internal/exchange"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestHeaderVersionRules(t *testing.T) {
	t.Parallel()

	record := `{"infoHash":"` + hashA + `","name":"one"}`

	for _, tc := range []struct {
		name   string
		header string
		err    error
	}{
		{
			name:   "unknown major is refused",
			header: `{"format":{"name":"torrent-exchange","version":{"major":2,"minor":0}}}`,
			err:    exchange.ErrUnsupportedVersion,
		},
		{
			name:   "major 0 is refused",
			header: `{"format":{"name":"torrent-exchange","version":{"major":0,"minor":9}}}`,
			err:    exchange.ErrUnsupportedVersion,
		},
		{
			name:   "another format name is refused",
			header: `{"format":{"name":"other-exchange","version":{"major":1,"minor":0}}}`,
			err:    exchange.ErrUnsupportedFormat,
		},
		{
			name:   "a header without a version is refused",
			header: `{"format":{"name":"torrent-exchange"}}`,
			err:    exchange.ErrInvalidHeader,
		},
		{
			name:   "unknown minor is accepted",
			header: `{"format":{"name":"torrent-exchange","version":{"major":1,"minor":7}}}`,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			lines, _, _, err := readAll(t, tc.header+"\n"+record+"\n")
			if tc.err != nil {
				require.ErrorIs(t, err, tc.err)
				assert.Empty(t, lines, "nothing after a refused header is read")

				return
			}

			require.NoError(t, err)
			require.Len(t, lines, 2)
			assert.Equal(t, exchange.Version{Major: 1, Minor: 7}, lines[0].Header.Version)
			assert.NotNil(t, lines[1].Record)
		})
	}
}
