package tmdb

import (
	"context"
	"fmt"
	"net"
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

func newTestLazy(clock *fakeClock, build func(context.Context) (Requester, error)) *requesterLazy {
	return &requesterLazy{
		logger: zap.NewNop().Sugar(),
		build:  build,
		now:    clock.now,
	}
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

	clock := &fakeClock{t: time.Unix(1_800_000_000, 0)}

	var calls atomic.Int32

	r := newTestLazy(clock, scriptedBuild(&calls, dnsFailure()))

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

func TestRequesterLazyPermanentFailuresAreKept(t *testing.T) {
	t.Parallel()

	for name, failure := range map[string]error{
		"disabled":            ErrConfiguration,
		"default key invalid": fmt.Errorf("default api key is invalid: %w", ErrUnauthorized),
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			clock := &fakeClock{t: time.Unix(1_800_000_000, 0)}

			var calls atomic.Int32

			r := newTestLazy(clock, scriptedBuild(&calls, failure))

			for range 3 {
				_, err := r.Request(t.Context(), "/movie/1", nil, nil)
				require.ErrorIs(t, err, failure)
				clock.advance(10 * initRetryInterval)
			}

			assert.Equal(t, int32(1), calls.Load())
		})
	}
}

func TestRequesterLazyCancelledFirstCallerDoesNotPoison(t *testing.T) {
	t.Parallel()

	clock := &fakeClock{t: time.Unix(1_800_000_000, 0)}

	var calls atomic.Int32

	// The build fails only if it sees its context cancelled, as a validation call would.
	r := newTestLazy(clock, func(ctx context.Context) (Requester, error) {
		calls.Add(1)

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

func TestRequesterLazyConcurrentFirstCallersInitialiseOnce(t *testing.T) {
	t.Parallel()

	clock := &fakeClock{t: time.Unix(1_800_000_000, 0)}

	var calls atomic.Int32

	release := make(chan struct{})

	r := newTestLazy(clock, func(context.Context) (Requester, error) {
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

	clock := &fakeClock{t: time.Unix(1_800_000_000, 0)}
	release := make(chan struct{})

	t.Cleanup(func() { close(release) })

	r := newTestLazy(clock, func(context.Context) (Requester, error) {
		<-release

		return okRequester{}, nil
	})

	ctx, cancel := context.WithTimeout(t.Context(), 50*time.Millisecond)
	defer cancel()

	_, err := r.Request(ctx, "/movie/1", nil, nil)
	require.ErrorIs(t, err, context.DeadlineExceeded)
}
