package httpserver_test

import (
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/bitmagnet-io/bitmagnet/internal/auth/rbac"
	"github.com/bitmagnet-io/bitmagnet/internal/importer"
	"github.com/bitmagnet-io/bitmagnet/internal/importer/httpserver"
	"github.com/bitmagnet-io/bitmagnet/internal/lazy"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/zap"
)

// allowGuard authorizes everything, so these tests exercise the handler rather
// than the permission model, which has a test of its own against the real guard.
type allowGuard struct{}

func (allowGuard) Allow(*gin.Context, rbac.ObjectAction) bool { return true }

// newEngine serves POST /import with an importer that has no database. It suits only
// requests refused before the import starts: one that got further would fail on the
// missing database.
func newEngine(t *testing.T) *gin.Engine {
	t.Helper()

	result := httpserver.New(httpserver.Params{
		Importer: lazy.New(func() (*importer.ExchangeImporter, error) {
			return importer.NewExchangeImporter(nil, nil, importer.ExchangeConfig{}), nil
		}),
		Guard:  allowGuard{},
		Logger: zap.NewNop().Sugar(),
	})

	engine := gin.New()
	require.NoError(t, result.Option.Apply(engine))

	return engine
}

// unreadBody fails the test if the handler reads a byte of it.
type unreadBody struct{ t *testing.T }

func (b unreadBody) Read([]byte) (int, error) {
	b.t.Error("the body was read before the request was validated")

	return 0, io.EOF
}

// The import endpoint writes the database. Without a Content-Type check a
// cross-origin POST of text/plain is a CORS *simple request*: the browser sends
// it with no preflight, so no CORS response header is needed for the write to
// land. Rejecting anything but NDJSON is what makes the request non-simple, and so
// preflighted, regardless of what the anonymous baseline grants.
func TestImportRejectsMediaTypesOtherThanNDJSON(t *testing.T) {
	t.Parallel()

	for _, contentType := range []string{
		"text/plain",
		"text/plain;charset=UTF-8",
		"text/plain; x=application/json",
		"application/x-www-form-urlencoded",
		"multipart/form-data; boundary=x",
		"application/jsonl",
		"application/json, text/plain",
		"",
	} {
		t.Run("content-type "+contentType, func(t *testing.T) {
			t.Parallel()

			req := httptest.NewRequestWithContext(t.Context(), http.MethodPost, "/import?origin=alice", unreadBody{t})
			if contentType != "" {
				req.Header.Set("Content-Type", contentType)
			}

			rec := httptest.NewRecorder()
			newEngine(t).ServeHTTP(rec, req)

			assert.Equal(t, http.StatusUnsupportedMediaType, rec.Code)
		})
	}
}
