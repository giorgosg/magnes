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
		exchangeHeader+"\n"+recordOfLength(t, httpserver.Limits.MaxLineBytes)+"\n"))
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())

	rec = post(t, engine, "/import?origin=bob", "application/x-ndjson", strings.NewReader(
		exchangeHeader+"\n"+
			`{"infoHash":"0000000000000000000000000000000000000002","name":"before"}`+"\n"+
			recordOfLength(t, httpserver.Limits.MaxLineBytes+1)+"\n"+
			`{"infoHash":"0000000000000000000000000000000000000003","name":"after"}`+"\n"))
	require.Equal(t, http.StatusRequestEntityTooLarge, rec.Code)

	report := decodeReport(t, rec)
	assert.Contains(t, report.Error, "line 3: line too long")
	assert.Equal(t, 1, report.Torrents.New, "the record before the long line is written")
	assert.Equal(t, 2, count(t, db, `SELECT count(*) FROM torrents`))
}

// More distinct reported entries than the endpoint's limit stop the import with 413, and
// the report it answers with stays within the limit. As for any fatal stream error, the
// records before the line that crossed it are written.
func TestImportRefusesMoreDistinctReportedEntriesThanTheLimit(t *testing.T) {
	t.Parallel()

	db := dbtest.New(t)

	var fields strings.Builder
	for i := range httpserver.Limits.MaxReported {
		fmt.Fprintf(&fields, `,"unknown-%d":1`, i)
	}

	rec := post(t, newDBEngine(t, db), "/import?origin=alice", "application/x-ndjson", strings.NewReader(
		exchangeHeader+"\n"+
			`{"infoHash":"0000000000000000000000000000000000000002","name":"before"}`+"\n"+
			`{"infoHash":"0000000000000000000000000000000000000001","name":"x"`+fields.String()+"}\n"))
	require.Equal(t, http.StatusRequestEntityTooLarge, rec.Code)

	report := decodeReport(t, rec)
	assert.Contains(t, report.Error, "line 3: too much to report")
	assert.LessOrEqual(
		t,
		len(report.Headers)+len(report.Malformed)+len(report.Dropped),
		httpserver.Limits.MaxReported,
	)
	assert.Equal(t, 1, report.Torrents.New, "the record before the line that crossed the limit is written")
	assert.Equal(t, 1, count(t, db, `SELECT count(*) FROM torrents`))
}

// Each string the report keeps is clipped.
func TestImportClipsReportedStrings(t *testing.T) {
	t.Parallel()

	db := dbtest.New(t)
	long := strings.Repeat("u", httpserver.Limits.MaxReportedBytes+1)

	rec := post(t, newDBEngine(t, db), "/import?origin=alice", "application/x-ndjson", strings.NewReader(
		exchangeHeader+"\n"+`{"infoHash":"0000000000000000000000000000000000000001","name":"x","`+long+`":1}`+"\n",
	))
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())

	report := decodeReport(t, rec)
	require.Len(t, report.Dropped, 1)
	assert.Equal(t, long[:httpserver.Limits.MaxReportedBytes]+"…", report.Dropped[0].Path)
}

// Invalid tags are reported by the import rather than the reader, and one record can carry
// as many as its line holds. They count against the same limit.
func TestImportRefusesMoreDistinctInvalidTagsThanTheLimit(t *testing.T) {
	t.Parallel()

	db := dbtest.New(t)

	tags := make([]string, httpserver.Limits.MaxReported)
	for i := range tags {
		tags[i] = fmt.Sprintf(`"Bad %d"`, i)
	}

	rec := post(t, newDBEngine(t, db), "/import?origin=alice", "application/x-ndjson", strings.NewReader(
		exchangeHeader+"\n"+`{"infoHash":"0000000000000000000000000000000000000001","name":"x",`+
			`"magnes":{"tags":[`+strings.Join(tags, ",")+`]}}`+"\n"))
	require.Equal(t, http.StatusRequestEntityTooLarge, rec.Code)

	report := decodeReport(t, rec)
	assert.Contains(t, report.Error, "too much to report")
	assert.LessOrEqual(t,
		len(report.Headers)+len(report.Malformed)+len(report.Dropped), httpserver.Limits.MaxReported)
	assert.Zero(t, count(t, db, `SELECT count(*) FROM torrents`))
}

// A format error quotes what the sender wrote. Its text is clipped like the report's.
func TestImportClipsTheError(t *testing.T) {
	t.Parallel()

	name := strings.Repeat("n", 10_000)
	rec := post(t, newDBEngine(t, dbtest.New(t)), "/import?origin=alice", "application/x-ndjson",
		strings.NewReader(`{"format":{"name":"`+name+`","version":{"major":1,"minor":0}}}`+"\n"))
	require.Equal(t, http.StatusUnprocessableEntity, rec.Code)

	report := decodeReport(t, rec)
	assert.Contains(t, report.Error, "unsupported format name")
	assert.LessOrEqual(t, len(report.Error), httpserver.Limits.MaxReportedBytes+len("…"))
}
