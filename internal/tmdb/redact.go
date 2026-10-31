package tmdb

import (
	"errors"
	"fmt"
	"net/url"
	"regexp"
)

// TMDB v3 takes the API key only as the api_key query parameter (its Authorization header wants a v4 read access
// token instead), and Go's HTTP client puts the full URL into every transport error. These helpers keep the key out
// of errors and logs.

var apiKeyParam = regexp.MustCompile(`(api_key=)[^&\s"]*`)

func redactAPIKey(s string) string {
	return apiKeyParam.ReplaceAllString(s, "${1}REDACTED")
}

// redactAPIKeyError returns err with the API key removed from its message, keeping the chain for errors.Is/As.
func redactAPIKeyError(err error) error {
	if err == nil || !apiKeyParam.MatchString(err.Error()) {
		return err
	}

	var urlErr *url.Error
	if errors.As(err, &urlErr) && urlErr == err { //nolint:errorlint // only a top-level *url.Error can be replaced
		return &url.Error{Op: urlErr.Op, URL: redactAPIKey(urlErr.URL), Err: redactAPIKeyError(urlErr.Err)}
	}

	return redactedError{err}
}

type redactedError struct{ err error }

func (e redactedError) Error() string { return redactAPIKey(e.err.Error()) }

func (e redactedError) Unwrap() error { return e.err }

// redactingLogger is a resty.Logger that removes the API key from resty's own log lines.
type redactingLogger struct {
	logger interface {
		Errorf(format string, v ...any)
		Warnf(format string, v ...any)
		Debugf(format string, v ...any)
	}
}

func (l redactingLogger) Errorf(format string, v ...any) {
	l.logger.Errorf("%s", redactAPIKey(fmt.Sprintf(format, v...)))
}

func (l redactingLogger) Warnf(format string, v ...any) {
	l.logger.Warnf("%s", redactAPIKey(fmt.Sprintf(format, v...)))
}

func (l redactingLogger) Debugf(format string, v ...any) {
	l.logger.Debugf("%s", redactAPIKey(fmt.Sprintf(format, v...)))
}
