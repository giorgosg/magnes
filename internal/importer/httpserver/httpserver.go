package httpserver

import (
	"errors"
	"fmt"
	"mime"
	"net/http"
	"net/url"

	"github.com/bitmagnet-io/bitmagnet/internal/auth/http_auth"
	"github.com/bitmagnet-io/bitmagnet/internal/exchange"
	"github.com/bitmagnet-io/bitmagnet/internal/httpserver"
	"github.com/bitmagnet-io/bitmagnet/internal/importer"
	"github.com/bitmagnet-io/bitmagnet/internal/lazy"
	"github.com/gin-gonic/gin"
	"go.uber.org/fx"
	"go.uber.org/zap"
)

type Params struct {
	fx.In
	Importer lazy.Lazy[*importer.ExchangeImporter]
	Guard    http_auth.Guard
	Logger   *zap.SugaredLogger
}

type Result struct {
	fx.Out
	Option httpserver.Option `group:"http_server_options"`
}

func New(p Params) Result {
	return Result{
		Option: &builder{
			importer: p.Importer,
			guard:    p.Guard,
			logger:   p.Logger.Named("importer"),
		},
	}
}

// The media types POST /import accepts. Both name the newline-delimited JSON an exchange
// file is; application/json is kept because it is what clients of this endpoint have
// always sent.
const (
	NDJSONMediaType = "application/x-ndjson"
	JSONMediaType   = "application/json"
)

// The query parameters POST /import reads.
const (
	originParam         = "origin"
	overwriteHintsParam = "overwriteHints"
)

type builder struct {
	importer lazy.Lazy[*importer.ExchangeImporter]
	guard    http_auth.Guard
	logger   *zap.SugaredLogger
}

func (builder) Key() string {
	return "import"
}

func (b builder) Apply(e *gin.Engine) error {
	i, err := b.importer.Get()
	if err != nil {
		return err
	}

	e.POST("/import", func(ctx *gin.Context) {
		// This endpoint writes torrents, so it must not stay open once
		// anonymous access is disabled.
		if !b.guard.Allow(ctx, http_auth.ObjectActionImport) {
			ctx.AbortWithStatus(http.StatusUnauthorized)

			return
		}

		// Reject anything but the two NDJSON media types before touching the body.
		// text/plain, multipart/form-data and application/x-www-form-urlencoded are the
		// three media types that make a cross-origin POST a CORS *simple request*: the
		// browser sends it with no preflight, so no CORS response header is needed for
		// this write to land. Neither accepted type is one of them, so every request
		// that gets past this check was preflighted, and the origin policy got to answer
		// first. A missing Content-Type is refused for the same reason.
		if !hasImportMediaType(ctx.Request) {
			ctx.AbortWithStatus(http.StatusUnsupportedMediaType)

			return
		}

		options, err := parseOptions(ctx.Request.URL.Query())
		if err != nil {
			ctx.AbortWithStatusJSON(http.StatusBadRequest, errorResponse{Error: err.Error()})

			return
		}

		b.handle(ctx, i, options)
	})

	return nil
}

// hasImportMediaType reports whether the request body is declared as NDJSON. Parameters
// such as charset are allowed; a missing or unparseable header is not, because a request
// that declares nothing is exactly the simple request this is here to refuse.
// ParseMediaType lowercases what it returns, so the comparison is already
// case-insensitive as RFC 9110 requires.
func hasImportMediaType(req *http.Request) bool {
	mediaType, _, err := mime.ParseMediaType(req.Header.Get("Content-Type"))
	if err != nil {
		return false
	}

	return mediaType == NDJSONMediaType || mediaType == JSONMediaType
}

// parseOptions reads the query: origin, required, and overwriteHints, true or false.
// Anything else, or either one given twice, is refused rather than guessed at.
func parseOptions(query url.Values) (importer.ExchangeOptions, error) {
	var options importer.ExchangeOptions

	for name, values := range query {
		switch name {
		case originParam, overwriteHintsParam:
			if len(values) != 1 {
				return options, fmt.Errorf("%s is given %d times; give it once", name, len(values))
			}
		default:
			return options, fmt.Errorf("unknown query parameter %q; expected %s and %s",
				name, originParam, overwriteHintsParam)
		}
	}

	origin := query.Get(originParam)
	if !exchange.ValidOriginLabel(origin) {
		return options, fmt.Errorf("%s must be one lowercase kebab-case label, such as alice; got %q",
			originParam, origin)
	}

	options.Origin = origin

	if values, ok := query[overwriteHintsParam]; ok {
		switch values[0] {
		case "true":
			options.OverwriteHints = true
		case "false":
		default:
			return options, fmt.Errorf("%s must be true or false; got %q", overwriteHintsParam, values[0])
		}
	}

	return options, nil
}

func (b builder) handle(ctx *gin.Context, i *importer.ExchangeImporter, options importer.ExchangeOptions) {
	report, err := i.Import(ctx.Request.Context(), ctx.Request.Body, options)

	response := newReportResponse(report)
	status := http.StatusOK

	if err != nil {
		status = statusFor(err)
		response.Error = err.Error()

		if status >= http.StatusInternalServerError {
			if ctx.Request.Context().Err() != nil {
				// The client went away, and with it the batch in flight.
				b.logger.Infow("import interrupted", "origin", options.Origin, "error", err)
			} else {
				b.logger.Errorw("import failed", "origin", options.Origin, "error", err)
			}

			// The cause is in the log. A database error is no business of the caller's.
			response.Error = "import failed; the batches counted here were committed and the rest were not"
		}
	}

	ctx.JSON(status, response)
}

// statusFor answers an import that stopped. The report it carries counts what was
// committed before it stopped.
func statusFor(err error) int {
	var fatal *exchange.FatalError

	switch {
	case errors.Is(err, exchange.ErrRecordBeforeHeader),
		errors.Is(err, exchange.ErrUnsupportedFormat),
		errors.Is(err, exchange.ErrUnsupportedVersion),
		errors.Is(err, exchange.ErrInvalidHeader):
		return http.StatusUnprocessableEntity
	case errors.As(err, &fatal):
		// Any other stream error is a failure to read the request body.
		return http.StatusBadRequest
	default:
		return http.StatusInternalServerError
	}
}
