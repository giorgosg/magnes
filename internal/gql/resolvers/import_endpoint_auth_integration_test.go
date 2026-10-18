package resolvers_test

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/bitmagnet-io/bitmagnet/internal/auth/authconfig"
	"github.com/bitmagnet-io/bitmagnet/internal/database/dbtest"
	"github.com/bitmagnet-io/bitmagnet/internal/dev/fixtureserver"
	"github.com/bitmagnet-io/bitmagnet/internal/importer"
	importhttpserver "github.com/bitmagnet-io/bitmagnet/internal/importer/httpserver"
	"github.com/bitmagnet-io/bitmagnet/internal/lazy"
	"github.com/stretchr/testify/require"
	"go.uber.org/zap"
)

// POST /import writes the catalogue, so the read-only anonymous baseline does not reach
// it: an anonymous request is refused before anything is read, and an administrator's is
// imported. The endpoint is mounted with the guard production uses, over the real
// authenticator and permission model.
func TestImportEndpointRefusesAnonymousUnderTheReadOnlyBaseline(t *testing.T) {
	t.Parallel()

	db := dbtest.New(t)
	stack, err := fixtureserver.Build(t.Context(), fixtureserver.Options{
		Config:                    authconfig.NewDefaultConfig(),
		Provider:                  daoProvider{db.Query},
		Logger:                    zap.NewNop().Sugar(),
		GrantAnonymousReadSurface: true,
	})
	require.NoError(t, err)

	require.NoError(t, importhttpserver.New(importhttpserver.Params{
		Importer: lazy.New(func() (*importer.ExchangeImporter, error) {
			return importer.NewExchangeImporter(
				db.Gorm,
				nil,
				importer.ExchangeConfig{FilesThreshold: 100},
			), nil
		}),
		Guard:  stack.Guard,
		Logger: zap.NewNop().Sugar(),
	}).Option.Apply(stack.Engine))

	server := httptest.NewServer(stack.Engine)
	t.Cleanup(server.Close)

	invitation, err := stack.UserService.CreateInitialInvitation(t.Context())
	require.NoError(t, err)

	admin := loginAsAdmin(t, server, invitation.Code)

	importAs := func(token string) int {
		req, err := http.NewRequestWithContext(t.Context(), http.MethodPost, server.URL+"/import?origin=alice",
			strings.NewReader(`{"format":{"name":"torrent-exchange","version":{"major":1,"minor":0}}}`+"\n"+
				`{"infoHash":"0000000000000000000000000000000000000001","name":"Imported"}`+"\n"))
		require.NoError(t, err)

		req.Header.Set("Content-Type", "application/x-ndjson")

		if token != "" {
			req.Header.Set("Authorization", "Bearer "+token)
		}

		res, err := server.Client().Do(req)
		require.NoError(t, err)

		defer func() { _ = res.Body.Close() }()

		return res.StatusCode
	}

	torrents := func() int64 {
		var n int64
		require.NoError(t, db.Gorm.Raw(`SELECT count(*) FROM torrents`).Scan(&n).Error)

		return n
	}

	require.Equal(t, http.StatusUnauthorized, importAs(""))
	require.Zero(t, torrents(), "an anonymous import must not write")

	require.Equal(t, http.StatusOK, importAs(admin))
	require.EqualValues(t, 1, torrents())
}
