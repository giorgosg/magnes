package importer

import (
	"fmt"
	"testing"

	"github.com/bitmagnet-io/bitmagnet/internal/exchange"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// The limit bounds what the import holds, not only what it reports: a record's drops past
// the limit are never kept, so planning one huge record cannot grow the drop set before
// the batch is stopped. Nothing outside the package can see the set, hence a test inside.
func TestDropsKeepNoMoreThanTheLimitAllows(t *testing.T) {
	t.Parallel()

	d := newDrops(exchange.Limits{MaxReported: 3})
	d.reported = 1

	for i := range 100 {
		d.add(ReasonInvalidTag, "magnes.tags[]", fmt.Sprintf("Bad %d", i))
	}

	assert.Len(t, d.counts, 2)
	require.ErrorIs(t, d.check(), exchange.ErrTooMuchReported)

	d.add(ReasonInvalidTag, "magnes.tags[]", "Bad 0")
	assert.Equal(t, 2, d.counts[dropKey{reason: ReasonInvalidTag, path: "magnes.tags[]", value: "Bad 0"}],
		"an entry already kept is still counted")
}
