package tmdb

import (
	"context"
	"net"
	"net/http"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/go-resty/resty/v2"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/zap"
)

type okRequester struct{}

func (okRequester) Request(context.Context, string, map[string]string, any) (*resty.Response, error) {
	return &resty.Response{}, nil
}

type fakeClock struct {
	mu sync.Mutex
	t  time.Time
}

func (c *fakeClock) now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()

	return c.t
}

func (c *fakeClock) advance(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()

	c.t = c.t.Add(d)
}

// scriptedBuild returns each error in turn, then succeeds, counting calls.
func scriptedBuild(calls *atomic.Int32, errs ...error) func(context.Context) (Requester, error) {
	return func(context.Context) (Requester, error) {
		n := int(calls.Add(1)) - 1
		if n < len(errs) {
			return nil, errs[n]
		}

		return okRequester{}, nil
	}
}

func newTestLazy(build func(context.Context) (Requester, error)) (*requesterLazy, *fakeClock) {
	clock := &fakeClock{t: time.Unix(1_800_000_000, 0)}

	return &requesterLazy{
		logger: zap.NewNop().Sugar(),
		build:  build,
		now:    clock.now,
	}, clock
}

func dnsFailure() error {
	return &net.OpError{
		Op:  "dial",
		Net: "tcp",
		Err: &net.DNSError{Err: "server misbehaving", Name: "api.themoviedb.org", Server: "127.0.0.53:53"},
	}
}

func TestRequesterLazyRetriesTransientInitFailure(t *testing.T) {
	t.Parallel()

	var calls atomic.Int32

	r, clock := newTestLazy(scriptedBuild(&calls, dnsFailure()))

	_, err := r.Request(t.Context(), "/movie/1", nil, nil)

	var opErr *net.OpError
	require.ErrorAs(t, err, &opErr)

	// Within the retry window the failure is reported without another validation call.
	clock.advance(initRetryInterval / 2)

	_, err = r.Request(t.Context(), "/movie/1", nil, nil)
	require.ErrorAs(t, err, &opErr)
	assert.Equal(t, int32(1), calls.Load())

	clock.advance(initRetryInterval)

	_, err = r.Request(t.Context(), "/movie/1", nil, nil)
	require.NoError(t, err)
	assert.Equal(t, int32(2), calls.Load())
}

func TestRequesterLazyCancelledFirstCallerDoesNotPoison(t *testing.T) {
	t.Parallel()

	// The build fails only if it sees its context cancelled, as a validation call would.
	r, _ := newTestLazy(func(ctx context.Context) (Requester, error) {
		if err := ctx.Err(); err != nil {
			return nil, err
		}

		return okRequester{}, nil
	})

	cancelled, cancel := context.WithCancel(t.Context())
	cancel()

	_, _ = r.Request(cancelled, "/movie/1", nil, nil)

	_, err := r.Request(t.Context(), "/movie/1", nil, nil)
	require.NoError(t, err)
}

func TestRequesterLazyConcurrentFirstCallersInitializeOnce(t *testing.T) {
	t.Parallel()

	var calls atomic.Int32

	release := make(chan struct{})

	r, _ := newTestLazy(func(context.Context) (Requester, error) {
		calls.Add(1)
		<-release

		return okRequester{}, nil
	})

	const callers = 8

	var wg sync.WaitGroup

	errs := make(chan error, callers)

	for range callers {
		wg.Go(func() {
			_, err := r.Request(t.Context(), "/movie/1", nil, nil)
			errs <- err
		})
	}

	time.Sleep(50 * time.Millisecond)
	close(release)
	wg.Wait()
	close(errs)

	for err := range errs {
		require.NoError(t, err)
	}

	assert.Equal(t, int32(1), calls.Load())
}

func TestRequesterLazyWaiterHonoursOwnContext(t *testing.T) {
	t.Parallel()

	release := make(chan struct{})

	t.Cleanup(func() { close(release) })

	r, _ := newTestLazy(func(context.Context) (Requester, error) {
		<-release

		return okRequester{}, nil
	})

	ctx, cancel := context.WithTimeout(t.Context(), 50*time.Millisecond)
	defer cancel()

	_, err := r.Request(ctx, "/movie/1", nil, nil)
	require.ErrorIs(t, err, context.DeadlineExceeded)
}

func TestRequesterLazyFailsFastWhileRetrying(t *testing.T) {
	t.Parallel()

	release := make(chan struct{})

	var calls atomic.Int32

	r, clock := newTestLazy(func(context.Context) (Requester, error) {
		if calls.Add(1) == 1 {
			return nil, dnsFailure()
		}

		<-release

		return okRequester{}, nil
	})

	_, err := r.Request(t.Context(), "/movie/1", nil, nil)
	require.Error(t, err)

	clock.advance(initRetryInterval)

	retried := make(chan error, 1)

	go func() {
		_, err := r.Request(t.Context(), "/movie/1", nil, nil)
		retried <- err
	}()

	require.Eventually(t, func() bool { return calls.Load() == 2 }, time.Second, time.Millisecond)

	// Another job during the retry gets the last failure at once instead of queueing behind it.
	var opErr *net.OpError

	_, err = r.Request(t.Context(), "/movie/1", nil, nil)
	require.ErrorAs(t, err, &opErr)

	close(release)
	require.NoError(t, <-retried, "the caller that started the retry waits for it")
}

// fakeTMDB answers /authentication with the status configured for each API key, and counts the calls.
// Statuses may be changed between requests through setStatus.
func fakeTMDB(
	t *testing.T,
	statusByKey map[string]int,
) (server *httptest.Server, calls *atomic.Int32, setStatus func(key string, status int)) {
	t.Helper()

	var mu sync.Mutex

	calls = &atomic.Int32{}
	setStatus = func(key string, status int) {
		mu.Lock()
		defer mu.Unlock()

		statusByKey[key] = status
	}

	server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		calls.Add(1)

		mu.Lock()
		status, ok := statusByKey[req.URL.Query().Get("api_key")]
		mu.Unlock()

		if !ok {
			status = http.StatusUnauthorized
		}

		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		_, _ = w.Write([]byte(`{}`))
	}))
	t.Cleanup(server.Close)

	return server, calls, setStatus
}

func newHTTPTestLazy(server *httptest.Server, apiKey string) (*requesterLazy, *fakeClock) {
	clock := &fakeClock{t: time.Unix(1_800_000_000, 0)}

	config := NewDefaultConfig()
	config.BaseURL = server.URL + "/3"
	config.APIKey = apiKey

	return &requesterLazy{config: config, logger: zap.NewNop().Sugar(), now: clock.now}, clock
}

func TestRequesterLazyPermanentFailuresAreKept(t *testing.T) {
	t.Parallel()

	t.Run("disabled", func(t *testing.T) {
		t.Parallel()

		server, calls, _ := fakeTMDB(t, map[string]int{})
		r, clock := newHTTPTestLazy(server, defaultTmdbAPIKey)
		r.config.Enabled = false

		for range 3 {
			_, err := r.Request(t.Context(), "/authentication", nil, nil)
			require.ErrorIs(t, err, ErrConfiguration)
			clock.advance(10 * initRetryInterval)
		}

		assert.Zero(t, calls.Load())
	})

	t.Run("default key rejected with 401", func(t *testing.T) {
		t.Parallel()

		server, calls, _ := fakeTMDB(t, map[string]int{defaultTmdbAPIKey: http.StatusUnauthorized})
		r, clock := newHTTPTestLazy(server, defaultTmdbAPIKey)

		for range 3 {
			_, err := r.Request(t.Context(), "/authentication", nil, nil)
			require.ErrorIs(t, err, ErrUnauthorized)
			clock.advance(10 * initRetryInterval)
		}

		assert.Equal(t, int32(1), calls.Load())
	})
}

func TestRequesterLazyRetriesNonFinalHTTPFailures(t *testing.T) {
	t.Parallel()

	// A 400 or 403 at startup can come from a proxy, captive portal or CDN, not from TMDB judging the key.
	for _, status := range []int{http.StatusBadRequest, http.StatusForbidden} {
		t.Run(http.StatusText(status), func(t *testing.T) {
			t.Parallel()

			server, calls, setStatus := fakeTMDB(t, map[string]int{defaultTmdbAPIKey: status})
			r, clock := newHTTPTestLazy(server, defaultTmdbAPIKey)

			_, err := r.Request(t.Context(), "/authentication", nil, nil)
			require.Error(t, err)

			setStatus(defaultTmdbAPIKey, http.StatusOK)

			clock.advance(initRetryInterval)

			_, err = r.Request(t.Context(), "/authentication", nil, nil)
			require.NoError(t, err)
			// Failed validation, retried validation, then the request itself.
			assert.Equal(t, int32(3), calls.Load())
		})
	}
}

func TestRequesterLazyFallsBackToDefaultKey(t *testing.T) {
	t.Parallel()

	const personalKey = "0123456789abcdef0123456789abcdef"

	server, calls, _ := fakeTMDB(t, map[string]int{
		personalKey:       http.StatusUnauthorized,
		defaultTmdbAPIKey: http.StatusOK,
	})
	r, _ := newHTTPTestLazy(server, personalKey)

	_, err := r.Request(t.Context(), "/authentication", nil, nil)
	require.NoError(t, err)
	assert.Equal(t, int32(3), calls.Load(), "personal key rejected, default key validated, then the request")
}
