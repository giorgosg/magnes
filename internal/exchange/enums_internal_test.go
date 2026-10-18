package exchange

import (
	"testing"

	"github.com/bitmagnet-io/bitmagnet/internal/model"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Walks every internal enum value, so that adding a value to an internal enum without a
// format spelling fails here rather than in an export.
func TestEveryInternalEnumValueHasAFormatSpelling(t *testing.T) {
	t.Parallel()

	walkEnum(t, contentTypes, model.ContentTypeValues())
	walkEnum(t, videoResolutions, model.VideoResolutionValues())
	walkEnum(t, videoSources, model.VideoSourceValues())
	walkEnum(t, videoCodecs, model.VideoCodecValues())
	walkEnum(t, video3Ds, model.Video3DValues())
	walkEnum(t, videoModifiers, model.VideoModifierValues())
}

func walkEnum[T ~string](t *testing.T, table enumTable[T], values []T) {
	t.Helper()

	require.NotEmpty(t, values)

	for _, v := range values {
		spelling, err := table.format(v)
		require.NoError(t, err, "%s %q", table.field, v)

		back, ok := table.internal(spelling)
		assert.True(t, ok, "%s %q", table.field, spelling)
		assert.Equal(t, v, back, "%s %q", table.field, spelling)
	}

	assert.Len(t, table.toInternal, len(values), "%s has spellings for values that no longer exist", table.field)
}

func TestUnmappedInternalValueFailsToEncode(t *testing.T) {
	t.Parallel()

	_, err := contentTypes.format(model.ContentType("podcast"))
	require.ErrorIs(t, err, ErrUnmappedValue)
}
