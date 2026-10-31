package tmdb

import (
	"net"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/zap"
	"go.uber.org/zap/zaptest/observer"
)

func TestRequesterErrorsAndLogsOmitAPIKey(t *testing.T) {
	t.Parallel()

	const key = "0123456789abcdef0123456789abcdef"

	// Reserve a port, then close it, so the request fails to dial.
	listener, err := (&net.ListenConfig{}).Listen(t.Context(), "tcp", "127.0.0.1:0")
	require.NoError(t, err)

	addr := listener.Addr().String()
	require.NoError(t, listener.Close())

	core, logs := observer.New(zap.DebugLevel)

	client := newRestyClient(
		Config{BaseURL: "http://" + addr + "/3", APIKey: key},
		zap.New(core).Sugar(),
	).SetRetryCount(1).SetRetryWaitTime(0).SetRetryMaxWaitTime(0)

	_, err = requester{resty: client}.Request(t.Context(), "/authentication", nil, nil)
	require.Error(t, err)

	var opErr *net.OpError
	require.ErrorAs(t, err, &opErr, "the network error stays inspectable")
	assert.NotContains(t, err.Error(), key)
	assert.Contains(t, err.Error(), "/authentication")

	require.NotZero(t, logs.Len(), "resty logs the failed attempts")

	for _, entry := range logs.All() {
		assert.NotContains(t, entry.Message, key)
	}
}
