package httpserver_test

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"

	"github.com/bitmagnet-io/bitmagnet/internal/database/dbtest"
	"github.com/bitmagnet-io/bitmagnet/internal/importer"
	"github.com/bitmagnet-io/bitmagnet/internal/importer/httpserver"
	"github.com/bitmagnet-io/bitmagnet/internal/lazy"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/zap"
)

// fixturePath is the frozen exchange fixture from ticket 01.
const fixturePath = "../../exchange/testdata/exchange-v1.jsonl"

const exchangeHeader = `{"format":{"name":"torrent-exchange","version":{"major":1,"minor":0}}}`

// newDBEngine serves POST /import against a test database, with a guard that allows
// everything so these tests exercise the handler rather than the permission model.
func newDBEngine(t *testing.T, db *dbtest.DB) *gin.Engine {
	t.Helper()

	result := httpserver.New(httpserver.Params{
		Importer: lazy.New(func() (*importer.ExchangeImporter, error) {
			return importer.NewExchangeImporter(
				db.Gorm,
				nil,
				importer.ExchangeConfig{FilesThreshold: 100},
			), nil
		}),
		Guard:  allowGuard{},
		Logger: zap.NewNop().Sugar(),
	})

	engine := gin.New()
	require.NoError(t, result.Option.Apply(engine))

	return engine
}

// post sends body to target with the given media type.
func post(t *testing.T, engine *gin.Engine, target, contentType string, body io.Reader) *httptest.ResponseRecorder {
	t.Helper()

	req := httptest.NewRequestWithContext(t.Context(), http.MethodPost, target, body)
	if contentType != "" {
		req.Header.Set("Content-Type", contentType)
	}

	rec := httptest.NewRecorder()
	engine.ServeHTTP(rec, req)

	return rec
}

func count(t *testing.T, db *dbtest.DB, sql string, args ...any) int {
	t.Helper()

	var n int
	require.NoError(t, db.Pool.QueryRow(t.Context(), sql, args...).Scan(&n))

	return n
}

// A legacy flat-format body is now rejected. Its line has no header before it, which the
// exchange format treats as fatal, so nothing is written.
func TestImportRejectsTheLegacyFlatFormat(t *testing.T) {
	t.Parallel()

	db := dbtest.New(t)
	engine := newDBEngine(t, db)

	rec := post(t, engine, "/import?origin=alice", "application/json", strings.NewReader(
		`{"source":"legacy","infoHash":"0000000000000000000000000000000000000001","name":"Legacy","size":1}`+"\n",
	))

	assert.Equal(t, http.StatusUnprocessableEntity, rec.Code)
	assert.Contains(t, rec.Body.String(), "record before the first header")
	assert.Zero(t, count(t, db, `SELECT count(*) FROM torrents`))
}

// origin is required and must be one lowercase kebab-case label. Anything else is refused
// with 400 before the body is read, and nothing is written.
func TestImportRefusesAMissingOrInvalidOriginBeforeReading(t *testing.T) {
	t.Parallel()

	db := dbtest.New(t)
	engine := newDBEngine(t, db)

	for _, target := range []string{
		"/import",
		"/import?origin=",
		"/import?origin=Alice",
		"/import?origin=alice:bob",
		"/import?origin=alice&origin=bob",
		"/import?origin=alice&overwriteHints=yes",
		"/import?origin=alice&overwriteHints=true&overwriteHints=false",
		"/import?origin=alice&overwrite-hints=true",
	} {
		t.Run(target, func(t *testing.T) {
			t.Parallel()

			rec := post(t, engine, target, "application/x-ndjson", unreadBody{t})

			assert.Equal(t, http.StatusBadRequest, rec.Code)

			var body importReport
			require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &body), rec.Body.String())
			assert.NotEmpty(t, body.Error)
			assert.Zero(t, count(t, db, `SELECT count(*) FROM torrents`))
		})
	}
}

// importReport is the JSON report POST /import answers with, decoded independently of the
// handler's own types so that a renamed field fails here.
type importReport struct {
	Error     string            `json:"error"`
	Headers   []reportHeader    `json:"headers"`
	Records   int               `json:"records"`
	Malformed []reportMalformed `json:"malformed"`
	Torrents  reportTorrents    `json:"torrents"`
	Hints     reportHints       `json:"hints"`
	TagsAdded int               `json:"tagsAdded"`
	Dropped   []reportDropped   `json:"dropped"`
}

type reportHeader struct {
	Line       int            `json:"line"`
	Version    reportVersion  `json:"version"`
	Producer   reportProducer `json:"producer"`
	ExportedAt string         `json:"exportedAt"`
	Since      string         `json:"since"`
}

type reportVersion struct {
	Major int `json:"major"`
	Minor int `json:"minor"`
}

type reportProducer struct {
	Program string `json:"program"`
	Version string `json:"version"`
}

type reportMalformed struct {
	Line  int    `json:"line"`
	Error string `json:"error"`
}

type reportTorrents struct {
	New       int `json:"new"`
	Updated   int `json:"updated"`
	Unchanged int `json:"unchanged"`
	Banned    int `json:"banned"`
	Skipped   int `json:"skipped"`
}

type reportHints struct {
	Written int `json:"written"`
	Kept    int `json:"kept"`
}

type reportDropped struct {
	Scope  string `json:"scope"`
	Reason string `json:"reason"`
	Path   string `json:"path"`
	Value  string `json:"value"`
	Count  int    `json:"count"`
}

func decodeReport(t *testing.T, rec *httptest.ResponseRecorder) importReport {
	t.Helper()

	assert.Equal(t, "application/json; charset=utf-8", rec.Header().Get("Content-Type"))

	var report importReport
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &report), rec.Body.String())

	return report
}

// Both NDJSON media types are accepted, and the import answers with its report as JSON.
func TestImportAnswersWithTheReportAsJSON(t *testing.T) {
	t.Parallel()

	for _, contentType := range []string{
		"application/x-ndjson",
		"application/x-ndjson; charset=utf-8",
		"application/json",
		"application/JSON; charset=utf-8",
	} {
		t.Run(contentType, func(t *testing.T) {
			t.Parallel()

			db := dbtest.New(t)
			fixture, err := os.Open(fixturePath)
			require.NoError(t, err)

			defer fixture.Close()

			rec := post(t, newDBEngine(t, db), "/import?origin=alice", contentType, fixture)
			require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())

			report := decodeReport(t, rec)
			assert.Empty(t, report.Error)
			require.Len(t, report.Headers, 2)
			assert.Equal(t, 1, report.Headers[0].Line)
			assert.Equal(t, 1, report.Headers[0].Version.Major)
			assert.Equal(t, "magnes", report.Headers[0].Producer.Program)
			assert.Equal(t, "0.14.0", report.Headers[0].Producer.Version)
			assert.Equal(t, "2026-10-18T12:00:00Z", report.Headers[0].ExportedAt)
			assert.Equal(t, "torrents-csv-converter", report.Headers[1].Producer.Program)
			assert.Equal(t, "2026-10-11T00:00:00Z", report.Headers[1].Since)
			assert.Equal(t, 4, report.Records)
			assert.Empty(t, report.Malformed)
			assert.Equal(t, 4, report.Torrents.New)
			assert.Equal(t, 3, report.Hints.Written)
			assert.Equal(t, 2, report.TagsAdded)
			assert.Empty(t, report.Dropped)

			assert.Equal(t, 4, count(t, db, `SELECT count(*) FROM torrents`))
			assert.Zero(t, count(t, db,
				`SELECT count(*) FROM torrents_torrent_sources WHERE source NOT LIKE 'alice:%'`),
				"every source is stored under the origin")
		})
	}
}

// Malformed lines and dropped values reach the JSON report.
func TestImportReportsMalformedLinesAndDrops(t *testing.T) {
	t.Parallel()

	db := dbtest.New(t)
	rec := post(t, newDBEngine(t, db), "/import?origin=alice", "application/x-ndjson", strings.NewReader(
		exchangeHeader+"\n"+
			`{"infoHash":"0000000000000000000000000000000000000001","name":"x","surprise":1}`+"\n"+
			"not json\n"))
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())

	report := decodeReport(t, rec)
	assert.Equal(t, 1, report.Records)
	require.Len(t, report.Malformed, 1)
	assert.Equal(t, 3, report.Malformed[0].Line)
	assert.NotEmpty(t, report.Malformed[0].Error)
	require.Len(t, report.Dropped, 1)
	assert.Equal(t, "record", report.Dropped[0].Scope)
	assert.Equal(t, "unknown-field", report.Dropped[0].Reason)
	assert.Equal(t, "surprise", report.Dropped[0].Path)
	assert.Equal(t, 1, report.Dropped[0].Count)
}

// overwriteHints=true replaces an existing hint; without it the existing hint wins.
func TestImportOverwritesHintsOnlyWhenAsked(t *testing.T) {
	t.Parallel()

	db := dbtest.New(t)
	engine := newDBEngine(t, db)

	record := func(contentType string) io.Reader {
		return strings.NewReader(exchangeHeader + "\n" +
			`{"infoHash":"0000000000000000000000000000000000000001","name":"x",` +
			`"magnes":{"classification":{"contentType":"` + contentType + `"}}}` + "\n")
	}
	hint := func() string {
		var contentType string
		require.NoError(
			t,
			db.Pool.QueryRow(t.Context(), `SELECT content_type FROM torrent_hints`).Scan(&contentType),
		)

		return contentType
	}

	require.Equal(t, http.StatusOK, post(t, engine, "/import?origin=alice", "application/x-ndjson",
		record("movie")).Code)
	require.Equal(t, "movie", hint())

	kept := decodeReport(t, post(t, engine, "/import?origin=alice&overwriteHints=false", "application/x-ndjson",
		record("music")))
	assert.Equal(t, 1, kept.Hints.Kept)
	require.Equal(t, "movie", hint())

	written := decodeReport(t, post(t, engine, "/import?origin=alice&overwriteHints=true", "application/x-ndjson",
		record("music")))
	assert.Equal(t, 1, written.Hints.Written)
	require.Equal(t, "music", hint())
}
