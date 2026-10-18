package httpserver_test

import (
	"fmt"
	"net/http"
	"strings"
	"testing"

	"github.com/bitmagnet-io/bitmagnet/internal/database/dbtest"
	"github.com/bitmagnet-io/bitmagnet/internal/importer/httpserver"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// recordOfLength is a record line exactly n bytes long, newline not counted. It is padded
// with an unknown field, which is reported rather than stored: a name that long could not
// be indexed.
func recordOfLength(t *testing.T, n int) string {
	t.Helper()

	prefix, suffix := `{"infoHash":"0000000000000000000000000000000000000001","name":"x","padding":"`, `"}`
	require.Greater(t, n, len(prefix)+len(suffix))

	return prefix + strings.Repeat("x", n-len(prefix)-len(suffix)) + suffix
}

// A line longer than the endpoint's limit stops the import with 413 and a report of what
// was committed before it; a line exactly at the limit is imported.
func TestImportRefusesALineLongerThanTheLimit(t *testing.T) {
	t.Parallel()

	db := dbtest.New(t)
	engine := newDBEngine(t, db)

	rec := post(t, engine, "/import?origin=alice", "application/x-ndjson", strings.NewReader(
		exchangeHeader+"\n"+recordOfLength(t, httpserver.MaxLineBytes)+"\n"))
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())

	rec = post(t, engine, "/import?origin=bob", "application/x-ndjson", strings.NewReader(
		exchangeHeader+"\n"+
			`{"infoHash":"0000000000000000000000000000000000000002","name":"before"}`+"\n"+
			recordOfLength(t, httpserver.MaxLineBytes+1)+"\n"+
			`{"infoHash":"0000000000000000000000000000000000000003","name":"after"}`+"\n"))
	require.Equal(t, http.StatusRequestEntityTooLarge, rec.Code)

	report := decodeReport(t, rec)
	assert.Contains(t, report.Error, "line 3: line too long")
	assert.Equal(t, 1, report.Torrents.New, "the record before the long line is written")
	assert.Equal(t, 2, count(t, db, `SELECT count(*) FROM torrents`))
}

// More distinct reported entries than the endpoint's limit stop the import with 413, and
// the report it answers with stays within the limit.
func TestImportRefusesMoreDistinctReportedEntriesThanTheLimit(t *testing.T) {
	t.Parallel()

	db := dbtest.New(t)

	var fields strings.Builder
	for i := range httpserver.MaxReported {
		fmt.Fprintf(&fields, `,"unknown-%d":1`, i)
	}

	rec := post(t, newDBEngine(t, db), "/import?origin=alice", "application/x-ndjson", strings.NewReader(
		exchangeHeader+"\n"+
			`{"infoHash":"0000000000000000000000000000000000000001","name":"x"`+fields.String()+"}\n"))
	require.Equal(t, http.StatusRequestEntityTooLarge, rec.Code)

	report := decodeReport(t, rec)
	assert.Contains(t, report.Error, "line 2: too much to report")
	assert.LessOrEqual(t, len(report.Headers)+len(report.Malformed)+len(report.Dropped), httpserver.MaxReported)
	assert.Zero(t, count(t, db, `SELECT count(*) FROM torrents`))
}

// Each string the report keeps is clipped.
func TestImportClipsReportedStrings(t *testing.T) {
	t.Parallel()

	db := dbtest.New(t)
	long := strings.Repeat("u", httpserver.MaxReportedBytes+1)

	rec := post(t, newDBEngine(t, db), "/import?origin=alice", "application/x-ndjson", strings.NewReader(
		exchangeHeader+"\n"+`{"infoHash":"0000000000000000000000000000000000000001","name":"x","`+long+`":1}`+"\n",
	))
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())

	report := decodeReport(t, rec)
	require.Len(t, report.Dropped, 1)
	assert.Equal(t, long[:httpserver.MaxReportedBytes]+"…", report.Dropped[0].Path)
}
