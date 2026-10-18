package enhancement

import (
	"strings"
	"testing"
	"time"

	"github.com/bitmagnet-io/bitmagnet/internal/model"
	"github.com/bitmagnet-io/bitmagnet/internal/protocol"
	"github.com/stretchr/testify/require"
)

func TestRecipeFingerprintExcludesSecretsAndOperationalSettings(t *testing.T) {
	t.Parallel()

	c := NewDefaultConfig()
	c.BaseURL = "https://name:secret@example.invalid/v1?api_key=hidden#fragment"
	c.APIKey = "credential"
	c.Model = "test"
	before, canonical, err := Recipe(c, "rules-v1")
	require.NoError(t, err)

	for _, secret := range []string{"secret", "hidden", "credential", "name:"} {
		require.NotContains(t, canonical, secret)
	}

	c.APIKey = "rotated"
	c.Concurrency = 19
	c.Timeout = time.Second
	c.MaxRunCost = 100
	c.MaxRunRequests = 7
	after, _, err := Recipe(c, "rules-v1")
	require.NoError(t, err)
	require.Equal(t, before, after)

	c.Model = "semantic-change"
	after, _, err = Recipe(c, "rules-v1")
	require.NoError(t, err)
	require.NotEqual(t, before, after)

	changed, _, err := Recipe(c, "rules-v2")
	require.NoError(t, err)
	require.NotEqual(t, after, changed)
	require.True(t, strings.HasPrefix(canonical, "{"))
}

func TestInputFingerprintTracksEvidenceWithoutIncidentalMetadata(t *testing.T) {
	t.Parallel()

	torrent := model.Torrent{
		InfoHash:    protocol.ID{1},
		Name:        "Show.S02E05",
		FilesStatus: model.FilesStatusSingle,
		FilesCount:  model.NewNullUint(1),
	}
	ordinary := model.TorrentContent{ContentType: model.NewNullContentType(model.ContentTypeTvShow)}
	_, before, err := inputEvidence(torrent, ordinary)
	require.NoError(t, err)

	torrent.UpdatedAt = time.Now()
	ordinary.UpdatedAt = time.Now()
	ordinary.Seeders = model.NewNullUint(100)
	ordinary.Content.Popularity = model.NewNullFloat32(99)
	_, after, err := inputEvidence(torrent, ordinary)
	require.NoError(t, err)
	require.Equal(t, before, after)

	torrent.Name = "Changed.Show.S02E05"
	_, after, err = inputEvidence(torrent, ordinary)
	require.NoError(t, err)
	require.NotEqual(t, before, after)
}

func TestReasoningAndRoutingSettingsEnterRecipeOnlyWhenSet(t *testing.T) {
	t.Parallel()

	c := NewDefaultConfig()
	c.BaseURL = "https://example.invalid/v1"
	c.Model = "test"
	base, canonical, err := Recipe(c, "rules-v1")
	require.NoError(t, err)

	for _, key := range []string{"reasoningEffort", "reasoningParameter", "providerOrder"} {
		require.NotContains(t, canonical, key, "defaults keep existing fingerprints")
	}

	unset := c
	unset.ReasoningParameter = ""
	same, _, err := Recipe(unset, "rules-v1")
	require.NoError(t, err)
	require.Equal(t, base, same, "unset and explicit native parameters are one recipe")

	for name, change := range map[string]func(*Config){
		"effort":    func(c *Config) { c.ReasoningEffort = "low" },
		"parameter": func(c *Config) { c.ReasoningParameter = "reasoning" },
		"provider":  func(c *Config) { c.ProviderOrder = []string{"deepseek"} },
	} {
		changed := c
		change(&changed)
		after, _, err := Recipe(changed, "rules-v1")
		require.NoError(t, err)
		require.NotEqual(t, base, after, name)
	}
}
