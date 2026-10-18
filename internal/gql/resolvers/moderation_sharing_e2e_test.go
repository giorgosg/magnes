package resolvers_test

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/bitmagnet-io/bitmagnet/internal/auth/authconfig"
	"github.com/bitmagnet-io/bitmagnet/internal/database/dbtest"
	"github.com/bitmagnet-io/bitmagnet/internal/dev/fixtureserver"
	"github.com/bitmagnet-io/bitmagnet/internal/importer"
	"github.com/bitmagnet-io/bitmagnet/internal/model"
	"github.com/bitmagnet-io/bitmagnet/internal/moderation"
	"github.com/bitmagnet-io/bitmagnet/internal/protocol"
	"github.com/stretchr/testify/require"
	"go.uber.org/zap"
)

// Both instances and all identifiers are disposable. Source quarantine and allow
// classification use the real offline classifier; target writes use the real exchange importer.
func TestModerationQuarantineAllowBanExportImportAcrossInstances(t *testing.T) {
	t.Parallel()
	sourceDB, targetDB := dbtest.New(t), dbtest.New(t)
	start := func(db *dbtest.DB, seed bool) (*fixtureserver.Stack, *httptest.Server, string) {
		stack, err := fixtureserver.Build(
			t.Context(),
			fixtureserver.Options{
				Config:                    authconfig.NewDefaultConfig(),
				Provider:                  daoProvider{db.Query},
				Logger:                    zap.NewNop().Sugar(),
				GrantAnonymousReadSurface: true,
				SeedModerationData:        seed,
			},
		)
		require.NoError(t, err)
		invitation, err := stack.UserService.CreateInitialInvitation(t.Context())
		require.NoError(t, err)

		server := httptest.NewServer(stack.Engine)
		t.Cleanup(server.Close)

		return stack, server, loginAsAdmin(t, server, invitation.Code)
	}
	source, sourceServer, sourceToken := start(sourceDB, true)
	target, targetServer, targetToken := start(targetDB, true)
	hash := protocol.ID{0xfa, 0xb7, 69}
	id := moderation.IdentifierForInfoHash(hash)
	private := func(server *httptest.Server, token string, id moderation.Identifier) []byte {
		response := query(
			t,
			server,
			token,
			fmt.Sprintf(
				`{moderation{record(identifier:%q){
 status origin importedOrigin source revision evidence{name files infoHash}}}}`,
				id.String(),
			),
		)
		requireNoGqlErrors(t, response)

		return response.Data
	}
	ordinary := func(server *httptest.Server, hash protocol.ID) []byte {
		response := query(
			t,
			server,
			"",
			fmt.Sprintf(
				`{torrentContent{search(input:{infoHashes:[%q],cached:true}){items{infoHash torrent{name}}}}}`,
				hash.String(),
			),
		)
		requireNoGqlErrors(t, response)

		return response.Data
	}
	empty := `{"torrentContent":{"search":{"items":[]}}}`
	factory := func(db *dbtest.DB) *importer.ExchangeImporter {
		return importer.NewExchangeImporter(db.Gorm, nil, importer.ExchangeConfig{FilesThreshold: 100})
	}
	sourceImporter, targetImporter := factory(sourceDB), factory(targetDB)
	ingest := func(imp *importer.ExchangeImporter, server *httptest.Server, token, name string, hashes ...protocol.ID) {
		file := `{"format":{"name":"torrent-exchange","version":{"major":1,"minor":0}}}` + "\n"
		for _, hash := range hashes {
			record, err := json.Marshal(map[string]any{
				"infoHash": hash.String(),
				"name":     name,
				"size":     42,
				"sources":  []map[string]any{{"source": "harmless-sharing"}},
				"magnes":   map[string]any{"classification": map[string]any{"contentType": "ebook"}},
			})
			require.NoError(t, err)

			file += string(record) + "\n"
		}

		_, err := imp.Import(t.Context(), strings.NewReader(file), importer.ExchangeOptions{Origin: "peer"})
		require.NoError(t, err)

		for _, hash := range hashes {
			response := query(
				t,
				server,
				token,
				fmt.Sprintf(
					`mutation{torrent{reprocess(input:{infoHashes:[%q],apisDisabled:true,
 localSearchDisabled:true,classifierRematch:true})}}`,
					hash.String(),
				),
			)
			requireNoGqlErrors(t, response)
		}
	}
	ingest(sourceImporter, sourceServer, sourceToken, "Harmless synthetic review marker sharing handbook.pdf", hash)
	record := private(sourceServer, sourceToken, id)
	require.Contains(t, string(record), `"status":"pending"`)
	require.Contains(t, string(record), "Harmless synthetic review marker sharing handbook.pdf")
	require.JSONEq(t, empty, string(ordinary(sourceServer, hash)))

	decide := func(action string) {
		var found sharingModerationResponse
		require.NoError(t, json.Unmarshal(private(sourceServer, sourceToken, id), &found))

		selected := "status"
		if action == "allow" {
			selected = "record{status} classificationQueued"
		}

		response := query(
			t,
			sourceServer,
			sourceToken,
			fmt.Sprintf(
				`mutation{moderation{%s(input:{identifier:%q,expectedRevision:%q}){%s}}}`,
				action,
				id.String(),
				found.Moderation.Record.Revision,
				selected,
			),
		)
		requireNoGqlErrors(t, response)
	}
	decide("allow")
	require.Contains(t, string(private(sourceServer, sourceToken, id)), `"status":"allowed"`)
	response := query(
		t,
		sourceServer,
		sourceToken,
		fmt.Sprintf(
			`mutation{torrent{reprocess(input:{infoHashes:[%q],apisDisabled:true,
 localSearchDisabled:true,classifierRematch:true})}}`,
			hash.String(),
		),
	)
	requireNoGqlErrors(t, response)
	require.Contains(
		t,
		string(ordinary(sourceServer, hash)),
		"Harmless synthetic review marker sharing handbook.pdf",
	)
	decide("ban")
	require.JSONEq(t, empty, string(ordinary(sourceServer, hash)))

	unseen := protocol.ID{0xfa, 0xb7, 98}
	sourceLedger := moderation.NewService(sourceDB.Gorm)
	require.NoError(
		t,
		sourceLedger.Ban(t.Context(), moderation.IdentifierForInfoHash(unseen), "synthetic reviewer"),
	)

	allowed := protocol.ID{0xfa, 0xb7, 90} // source has a reviewed digest-only ban.
	targetLedger := moderation.NewService(targetDB.Gorm)
	require.NoError(t, targetLedger.Allow(t.Context(), moderation.IdentifierForInfoHash(allowed), "local reviewer"))

	ingest(targetImporter, targetServer, targetToken, "Harmless destination handbook", hash)
	require.Contains(t, string(ordinary(targetServer, hash)), "Harmless destination handbook")

	request := func(stack *fixtureserver.Stack, token, method, path string, data []byte) *httptest.ResponseRecorder {
		req := httptest.NewRequestWithContext(t.Context(), method, path, bytes.NewReader(data))
		req.Header.Set("Authorization", "Bearer "+token)
		req.Header.Set("Content-Type", "application/json")

		result := httptest.NewRecorder()
		stack.Engine.ServeHTTP(result, req)

		return result
	}
	exported := request(source, sourceToken, http.MethodGet, "/graphql/moderation/export", nil)
	require.Equal(t, http.StatusOK, exported.Code)

	var file moderation.BanFile
	require.NoError(t, json.Unmarshal(exported.Body.Bytes(), &file))
	require.Len(t, file.Entries, 3)

	for range 2 {
		uploaded := request(
			target,
			targetToken,
			http.MethodPost,
			"/graphql/moderation/import",
			exported.Body.Bytes(),
		)
		require.Equal(t, http.StatusOK, uploaded.Code)
	}

	require.JSONEq(t, empty, string(ordinary(targetServer, hash)))
	retained := private(targetServer, targetToken, id)
	require.Contains(t, string(retained), `"origin":"import"`)
	require.Contains(t, string(retained), `"importedOrigin":"moderator"`)
	require.Contains(t, string(retained), "Harmless destination handbook")
	ingest(targetImporter, targetServer, targetToken, "Harmless destination handbook", unseen, allowed)
	require.JSONEq(t, empty, string(ordinary(targetServer, unseen)))
	require.Contains(t, string(ordinary(targetServer, allowed)), "Harmless destination handbook")
	require.Contains(
		t,
		string(private(targetServer, targetToken, moderation.IdentifierForInfoHash(unseen))),
		"Harmless destination handbook",
	)
	require.NoError(t, targetDB.Gorm.Where("info_hash = ?", hash).Delete(&model.Torrent{}).Error)
	require.Contains(t, string(private(targetServer, targetToken, id)), "Harmless destination handbook")
	reexported := request(target, targetToken, http.MethodGet, "/graphql/moderation/export", nil)
	require.Equal(t, http.StatusOK, reexported.Code)
	require.JSONEq(t, `{"version":1,"identifierFormat":"sha256-btih-v1","entries":[]}`, reexported.Body.String())
}

type sharingRevision struct {
	Revision string `json:"revision"`
}
type sharingRecordResponse struct {
	Record sharingRevision `json:"record"`
}
type sharingModerationResponse struct {
	Moderation sharingRecordResponse `json:"moderation"`
}
