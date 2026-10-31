package tmdb

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"sync"
	"time"

	"github.com/bitmagnet-io/bitmagnet/internal/concurrency"
	"github.com/go-resty/resty/v2"
	"go.uber.org/zap"
	"golang.org/x/sync/semaphore"
	"golang.org/x/time/rate"
)

// requesterLazy defers instantiation of the requester (and possible failure) until the first request is made,
// avoiding failure when the TMDB client is not needed.
//
// Only a permanent failure (TMDB disabled, or the default API key rejected with 401) is kept for the life of the
// process. Any other failure (DNS, dial, timeout, 5xx) is reported until initRetryInterval has passed, and the next
// request after that tries again, so a network outage at startup does not disable TMDB until restart.
type requesterLazy struct {
	config Config
	logger *zap.SugaredLogger
	// build constructs and validates the underlying requester; nil means newRequester.
	build func(ctx context.Context) (Requester, error)
	// now is the clock for the retry window; nil means time.Now.
	now func() time.Time

	mu           sync.Mutex
	requester    Requester
	permanentErr error
	transientErr error         // returned until retryAfter
	retryAfter   time.Time     // when the next initialization may start after transientErr
	inflight     chan struct{} // closed when the running initialization finishes
}

const (
	// initRetryInterval is the minimum time between initialization attempts after a transient failure.
	initRetryInterval = 30 * time.Second
	// initTimeout bounds one initialization, including resty's own retries and the default-key fallback.
	initTimeout = 2 * time.Minute
)

// permanentError marks an initialization failure that retrying cannot fix.
type permanentError struct{ error }

func (e permanentError) Unwrap() error { return e.error }

func (r *requesterLazy) Request(
	ctx context.Context,
	path string,
	queryParams map[string]string,
	result interface{},
) (*resty.Response, error) {
	requester, err := r.get(ctx)
	if err != nil {
		return nil, err
	}

	return requester.Request(ctx, path, queryParams, result)
}

func (r *requesterLazy) get(ctx context.Context) (Requester, error) {
	started := false

	for {
		r.mu.Lock()

		switch {
		case r.requester != nil:
			requester := r.requester
			r.mu.Unlock()

			return requester, nil
		case r.permanentErr != nil:
			err := r.permanentErr
			r.mu.Unlock()

			return nil, err
		case r.inflight != nil && (started || r.transientErr == nil):
			// The caller that started this attempt waits for its result, as does everyone on the first one.
			inflight := r.inflight
			r.mu.Unlock()

			select {
			case <-inflight:
				continue
			case <-ctx.Done():
				return nil, ctx.Err()
			}
		case r.inflight != nil, r.clock().Before(r.retryAfter):
			// TMDB is known to be failing: fail fast rather than queue every job behind a retry.
			err := r.transientErr
			r.mu.Unlock()

			return nil, err
		}

		// Initialize in the background, detached from this caller's context, so that a cancelled or timed-out
		// job neither fails initialization for everyone nor keeps every other caller waiting on it.
		inflight := make(chan struct{})
		r.inflight = inflight
		started = true
		r.mu.Unlock()

		go r.initialize(context.WithoutCancel(ctx), inflight)
	}
}

func (r *requesterLazy) initialize(ctx context.Context, done chan struct{}) {
	ctx, cancel := context.WithTimeout(ctx, initTimeout)
	defer cancel()

	build := r.build
	if build == nil {
		build = func(ctx context.Context) (Requester, error) {
			return newRequester(ctx, r.config, r.logger)
		}
	}

	requester, err := build(ctx)

	r.mu.Lock()
	defer r.mu.Unlock()

	var permanent permanentError

	switch {
	case err == nil:
		r.requester = requester
		r.transientErr = nil
	case errors.As(err, &permanent):
		r.permanentErr = permanent.error
	default:
		r.transientErr = err
		r.retryAfter = r.clock().Add(initRetryInterval)
		r.logger.Warnw(
			"TMDB client initialization failed; will retry",
			"error", err,
			"retryIn", initRetryInterval,
		)
	}

	r.inflight = nil

	close(done)
}

func (r *requesterLazy) clock() time.Time {
	if r.now == nil {
		return time.Now()
	}

	return r.now()
}

func newRequester(ctx context.Context, config Config, logger *zap.SugaredLogger) (Requester, error) {
	if !config.Enabled {
		return nil, permanentError{ErrConfiguration}
	}

	if config.APIKey == defaultTmdbAPIKey {
		logger.Warnln(
			"you are using the default TMDB api key; TMDB requests will be limited to 1 per second; " +
				"to remove this warning please configure a personal TMDB api key",
		)

		config.RateLimit = time.Second
		config.RateLimitBurst = 8
	}

	r := requesterLogger{
		requester: requesterFailFast{
			requester: requesterSemaphore{
				requester: requesterLimiter{
					requester: requester{
						resty: newRestyClient(config, logger),
					},
					limiter: rate.NewLimiter(rate.Every(config.RateLimit), config.RateLimitBurst),
				},
				semaphore: semaphore.NewWeighted(2),
			},
			isUnauthorized: &concurrency.AtomicValue[bool]{},
		},
		logger: logger,
	}

	res, err := r.Request(ctx, "/authentication", nil, nil)
	if errors.Is(err, ErrUnauthorized) {
		if config.APIKey == defaultTmdbAPIKey {
			err = fmt.Errorf("default api key is invalid: %w", err)
			// A 403 can come from a proxy or CDN rather than TMDB, so only a 401 is final.
			if res != nil && res.StatusCode() == http.StatusUnauthorized {
				err = permanentError{err}
			}

			return nil, err
		}

		logger.Errorw("invalid api key, falling back to default", "error", err)

		config.APIKey = defaultTmdbAPIKey

		return newRequester(ctx, config, logger)
	}

	if err != nil {
		return nil, err
	}

	return r, nil
}

func newRestyClient(config Config, logger *zap.SugaredLogger) *resty.Client {
	return resty.New().
		SetBaseURL(config.BaseURL).
		SetQueryParam("api_key", config.APIKey).
		SetRetryCount(3).
		SetRetryWaitTime(2 * time.Second).
		SetRetryMaxWaitTime(20 * time.Second).
		SetTimeout(10 * time.Second).
		EnableTrace().
		SetLogger(redactingLogger{logger})
}
