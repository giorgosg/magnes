package cors_test

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/bitmagnet-io/bitmagnet/internal/httpserver"
	"github.com/bitmagnet-io/bitmagnet/internal/httpserver/cors"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/zap"
)

// preflightAllows reports the Access-Control-Allow-Headers value returned for a
// preflight asking for the given header, with an explicitly allowed origin and the default header policy.
//
// The requested name is lowercased, because that is what a browser sends: the
// Fetch standard guarantees CORS-unsafe request-header names in
// Access-Control-Request-Headers are lowercase, and rs/cors matches accordingly.
func preflightAllows(t *testing.T, requested string) string {
	t.Helper()

	gin.SetMode(gin.TestMode)

	cfg := httpserver.NewDefaultConfig()
	cfg.Cors.AllowedOrigins = []string{"https://attacker.example"}
	engine, err := httpserver.NewEngine(cfg)
	require.NoError(t, err)

	result := cors.New(cors.Params{
		Config: cfg,
		Logger: zap.NewNop().Sugar(),
	})
	require.NoError(t, result.Option.Apply(engine))

	engine.POST("/", func(c *gin.Context) { c.Status(http.StatusOK) })

	req := httptest.NewRequestWithContext(t.Context(), http.MethodOptions, "/", nil)
	req.Header.Set("Origin", "https://attacker.example")
	req.Header.Set("Access-Control-Request-Method", http.MethodPost)
	req.Header.Set("Access-Control-Request-Headers", strings.ToLower(requested))

	rec := httptest.NewRecorder()
	engine.ServeHTTP(rec, req)

	return rec.Header().Get("Access-Control-Allow-Headers")
}

func TestDefaultConfig_DoesNotAllowArbitraryHeaders(t *testing.T) {
	t.Parallel()

	assert.Empty(t, preflightAllows(t, "X-Something-Invented"),
		"the default CORS config reflects any requested header back")
}

func TestDefaultConfig_AllowsTheHeadersTheServerReads(t *testing.T) {
	t.Parallel()

	for _, header := range []string{"Content-Type", "Authorization", "X-Api-Key"} {
		t.Run(header, func(t *testing.T) {
			t.Parallel()

			assert.NotEmpty(t, preflightAllows(t, header),
				"%s is read by the server and must survive a preflight", header)
		})
	}
}

// X-Import-Id named a legacy import batch. The exchange import reads no such header, so
// the default no longer lets a cross-origin caller send it.
func TestDefaultConfig_DoesNotAllowTheRetiredImportIDHeader(t *testing.T) {
	t.Parallel()

	assert.Empty(t, preflightAllows(t, "X-Import-Id"),
		"no handler reads X-Import-Id since POST /import reads exchange files")
}

func TestDefaultConfig_CorsDebugIsOff(t *testing.T) {
	t.Parallel()

	assert.False(t, httpserver.NewDefaultConfig().Cors.Debug,
		"CORS debug logging should not be on by default")
}

func TestCrossOriginPolicy(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		name    string
		origins []string
		origin  string
		allowed string
	}{
		{
			name:    "default denies",
			origins: httpserver.NewDefaultConfig().Cors.AllowedOrigins,
			origin:  "https://other.example",
		},
		{name: "empty denies", origins: []string{}, origin: "https://other.example"},
		{
			name:    "explicit allows",
			origins: []string{"https://ui.example"},
			origin:  "https://ui.example",
			allowed: "https://ui.example",
		},
		{name: "explicit refuses others", origins: []string{"https://ui.example"}, origin: "https://other.example"},
		{name: "wildcard opt in", origins: []string{"*"}, origin: "https://other.example", allowed: "*"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			cfg := httpserver.NewDefaultConfig()
			cfg.Cors.AllowedOrigins = tc.origins
			engine, err := httpserver.NewEngine(cfg)
			require.NoError(t, err)
			require.NoError(
				t,
				cors.New(cors.Params{Config: cfg, Logger: zap.NewNop().Sugar()}).Option.Apply(engine),
			)
			engine.POST("/graphql", func(c *gin.Context) { c.Status(http.StatusOK) })

			for _, method := range []string{http.MethodPost, http.MethodOptions} {
				req := httptest.NewRequestWithContext(t.Context(), method, "/graphql", nil)
				req.Header.Set("Origin", tc.origin)

				if method == http.MethodOptions {
					req.Header.Set("Access-Control-Request-Method", http.MethodPost)
					req.Header.Set("Access-Control-Request-Headers", "content-type")
				}

				rec := httptest.NewRecorder()
				engine.ServeHTTP(rec, req)
				assert.Equal(t, tc.allowed, rec.Header().Get("Access-Control-Allow-Origin"), method)
			}

			req := httptest.NewRequestWithContext(t.Context(), http.MethodPost, "/graphql", nil)
			rec := httptest.NewRecorder()
			engine.ServeHTTP(rec, req)
			assert.Equal(t, http.StatusOK, rec.Code, "same-origin and non-browser requests still work")
		})
	}
}
