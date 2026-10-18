package importer

import (
	"strings"
	"testing"

	"github.com/bitmagnet-io/bitmagnet/internal/exchange"
	"github.com/bitmagnet-io/bitmagnet/internal/protocol"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// A batch is cut by the bytes of its lines as well as by its record count. A decoded record
// can take several times its line in memory, so 100 lines of a megabyte each held the best
// part of a gigabyte; the byte bound keeps a batch of large lines small.
func TestBatchesAreCutByBytesAsWellAsByCount(t *testing.T) {
	t.Parallel()

	padding := strings.Repeat("x", 1000)
	input := `{"format":{"name":"torrent-exchange","version":{"major":1,"minor":0}}}` + "\n"

	for i := range 10 {
		hash := protocol.ID{byte(i + 1)}.String()
		input += `{"infoHash":"` + hash + `","name":"x","padding":"` + padding + `"}` + "\n"
	}

	out := make(chan readBatch)
	done := make(chan struct{})

	defer close(done)

	go readBatches(exchange.NewReader(strings.NewReader(input)), 100, 2500, out, done)

	var sizes []int

	for batch := range out {
		require.NoError(t, batch.err)

		sizes = append(sizes, len(batch.records))

		if batch.last {
			break
		}
	}

	// Each line is a little over 1000 bytes, so a batch is cut once it holds three.
	assert.Equal(t, []int{3, 3, 3, 1}, sizes)
}
