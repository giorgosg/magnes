package enhancementcmd_test

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/bitmagnet-io/bitmagnet/internal/app/appfx"
	"github.com/bitmagnet-io/bitmagnet/internal/app/cli/hooks"
	"github.com/bitmagnet-io/bitmagnet/internal/database/dbtest"
	"github.com/bitmagnet-io/bitmagnet/internal/database/postgres"
	"github.com/bitmagnet-io/bitmagnet/internal/enhancement"
	"github.com/bitmagnet-io/bitmagnet/internal/model"
	"github.com/bitmagnet-io/bitmagnet/internal/protocol"
	"github.com/stretchr/testify/require"
	"github.com/urfave/cli/v2"
	"go.uber.org/fx"
)

func TestEvaluateFlagsSelectReasoningFieldAndProvider(t *testing.T) {
	t.Parallel()
	lockCLI(t)

	db := dbtest.New(t)
	hash := protocol.MustParseID(strings.Repeat("02", 20))
	require.NoError(t, db.Gorm.Create(&model.Torrent{
		InfoHash:    hash,
		Name:        "A release",
		FilesStatus: model.FilesStatusSingle,
		Size:        42,
		FilesCount:  model.NewNullUint(1),
	}).Error)

	requests := make(chan map[string]json.RawMessage, 4)

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var request map[string]json.RawMessage
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			t.Error(err)
		}

		requests <- request

		_, _ = w.Write(
			[]byte(`{"choices":[{"message":{"role":"assistant","content":"{}"},"finish_reason":"stop"}],
"usage":{"prompt_tokens":200,"completion_tokens":5}}`),
		)
	}))
	defer server.Close()

	cfg := enhancement.NewDefaultConfig()
	cfg.BaseURL = server.URL
	cfg.APIKey = "fake"
	cfg.Model = "fake"
	cfg.MaxRunCost = 1
	cfg.InputCostPerMillion = 1
	cfg.OutputCostPerMillion = 1
	pc := postgres.NewDefaultConfig()
	pc.DSN = db.DSN + "?default_transaction_read_only=on"

	var output bytes.Buffer

	app := fx.New(
		appfx.New(),
		fx.NopLogger,
		fx.Replace(cfg, pc),
		fx.Replace(fx.Annotate([]string{
			"bitmagnet", "enhancement", "evaluate", "--hash", hash.String(),
			"--reasoning-parameter", "reasoning", "--reasoning-effort", "low",
			"--provider-order", "deepseek",
		}, fx.ResultTags(`name:"cli_args"`))),
		fx.Decorate(func(commandApp *cli.App) *cli.App { commandApp.Writer = &output; return commandApp }),
		// The attached hooks stop the application once the command returns.
		fx.Invoke(func(_ *cli.App, _ hooks.AttachedHooks) {}),
	)
	require.NoError(t, app.Err())
	require.NoError(t, app.Start(t.Context()))

	select {
	case <-app.Done():
	case <-time.After(10 * time.Second):
		t.Fatal("command did not terminate")
	}

	require.NoError(t, app.Stop(t.Context()))
	require.Len(t, requests, 1)

	request := <-requests
	require.JSONEq(t, `{"effort":"low"}`, string(request["reasoning"]))
	require.JSONEq(
		t,
		`{"order":["deepseek"],"allow_fallbacks":false,"require_parameters":true}`,
		string(request["provider"]),
	)
	require.NotContains(t, request, "thinking")

	var report struct {
		Recipe map[string]any `json:"recipe"`
	}
	require.NoError(t, json.Unmarshal(output.Bytes(), &report))
	require.Equal(t, "reasoning", report.Recipe["reasoningParameter"])
	require.Equal(t, "low", report.Recipe["reasoningEffort"])
	require.Equal(t, []any{"deepseek"}, report.Recipe["providerOrder"])
}
