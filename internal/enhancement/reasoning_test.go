package enhancement_test

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/bitmagnet-io/bitmagnet/internal/enhancement"
	"github.com/bitmagnet-io/bitmagnet/internal/tmdb"
	"github.com/stretchr/testify/require"
	"go.uber.org/zap"
)

// sentRequest returns the decoded top-level fields of the single model request.
func sentRequest(t *testing.T, change func(*enhancement.Config)) map[string]json.RawMessage {
	t.Helper()

	var request map[string]json.RawMessage

	e, calls := evaluator(t, func(w http.ResponseWriter, r *http.Request) {
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			t.Error(err)
		}

		answer(w, `{}`, 10, 0)
	}, change)
	require.Equal(t, "unresolved", e.Evaluate(t.Context(), enhancement.Evidence{}).Outcome)
	require.EqualValues(t, 1, calls.Load())

	return request
}

func TestReasoningSettingsFollowTheConfiguredParameter(t *testing.T) {
	t.Parallel()

	for name, tc := range map[string]struct {
		change  func(*enhancement.Config)
		present map[string]string
		absent  []string
	}{
		"default sends neither": {
			change: func(*enhancement.Config) {},
			absent: []string{"thinking", "reasoning", "reasoning_effort", "provider"},
		},
		"native thinking disabled": {
			change:  func(c *enhancement.Config) { c.Thinking = "disabled" },
			present: map[string]string{"thinking": `{"type":"disabled"}`},
			absent:  []string{"reasoning", "reasoning_effort"},
		},
		"native effort": {
			change:  func(c *enhancement.Config) { c.Thinking = "enabled"; c.ReasoningEffort = "low" },
			present: map[string]string{"thinking": `{"type":"enabled"}`, "reasoning_effort": `"low"`},
			absent:  []string{"reasoning"},
		},
		"openrouter reasoning disabled": {
			change: func(c *enhancement.Config) {
				c.ReasoningParameter = "reasoning"
				c.Thinking = "disabled"
			},
			present: map[string]string{"reasoning": `{"enabled":false}`},
			absent:  []string{"thinking", "reasoning_effort"},
		},
		"openrouter effort": {
			change: func(c *enhancement.Config) {
				c.ReasoningParameter = "reasoning"
				c.ReasoningEffort = "low"
			},
			present: map[string]string{"reasoning": `{"effort":"low"}`},
			absent:  []string{"thinking", "reasoning_effort"},
		},
		"pinned provider": {
			change: func(c *enhancement.Config) { c.ProviderOrder = []string{"deepseek"} },
			present: map[string]string{
				"provider": `{"order":["deepseek"],"allow_fallbacks":false,"require_parameters":true}`,
			},
		},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			request := sentRequest(t, tc.change)
			for key, want := range tc.present {
				require.JSONEq(t, want, string(request[key]), key)
			}

			for _, key := range tc.absent {
				require.NotContains(t, request, key)
			}
		})
	}
}

func TestReasoningSettingsAreValidated(t *testing.T) {
	t.Parallel()

	valid := enhancement.NewDefaultConfig()
	valid.BaseURL = "https://example.invalid/v1"
	valid.APIKey = "key"
	valid.Model = "model"
	require.NoError(t, valid.Validate())
	require.Equal(t, "thinking", valid.ReasoningParameter)

	empty := valid
	empty.ReasoningParameter = ""
	require.NoError(t, empty.Validate(), "an unset parameter means DeepSeek's own field")

	for name, change := range map[string]func(*enhancement.Config){
		"unknown parameter":         func(c *enhancement.Config) { c.ReasoningParameter = "think" },
		"unknown effort":            func(c *enhancement.Config) { c.ReasoningEffort = "medium" },
		"effort with thinking off":  func(c *enhancement.Config) { c.Thinking = "disabled"; c.ReasoningEffort = "low" },
		"empty provider":            func(c *enhancement.Config) { c.ProviderOrder = []string{""} },
		"provider with odd symbols": func(c *enhancement.Config) { c.ProviderOrder = []string{"deep seek"} },
	} {
		c := valid
		change(&c)
		require.Error(t, c.Validate(), name)
	}
}

func TestOpenRouterReasoningDetailsAreReplayed(t *testing.T) {
	t.Parallel()

	catalogue := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")

		if r.URL.Path == "/authentication" {
			_, _ = w.Write([]byte(`{"success":true}`))
			return
		}

		_, _ = w.Write([]byte(`{"results":[{"id":7,"title":"Film","release_date":"2001-01-01"}]}`))
	}))
	defer catalogue.Close()

	var second []map[string]json.RawMessage

	calls := 0

	model := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++

		if calls == 1 {
			_, _ = w.Write([]byte(`{"choices":[{"message":{"role":"assistant",
"reasoning_details":[{"type":"reasoning.text","text":"opaque"}],
"tool_calls":[{"id":"s1","type":"function","function":{"name":"search_works",
"arguments":"{\"title\":\"Film\",\"type\":\"movie\"}"}}]},
"finish_reason":"tool_calls"}],"usage":{"prompt_tokens":100,"completion_tokens":20}}`))

			return
		}

		var request struct {
			Messages []map[string]json.RawMessage `json:"messages"`
		}
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			t.Error(err)
		}

		second = request.Messages

		answer(w, `{}`, 10, 0)
	}))
	defer model.Close()

	cfg := enhancement.NewDefaultConfig()
	cfg.BaseURL = model.URL
	cfg.APIKey = "fake"
	cfg.Model = "fake"
	tc := tmdb.New(
		tmdb.Params{
			Config: tmdb.Config{Enabled: true, BaseURL: catalogue.URL, APIKey: "fake", RateLimitBurst: 5},
			Logger: zap.NewNop().Sugar(),
		},
	).Client
	e, err := enhancement.NewEvaluator(cfg, tc, http.DefaultClient)
	require.NoError(t, err)
	require.Equal(t, "unresolved", e.Evaluate(t.Context(), enhancement.Evidence{Name: "Film"}).Outcome)
	require.Len(t, second, 4)
	require.JSONEq(t, `[{"type":"reasoning.text","text":"opaque"}]`, string(second[2]["reasoning_details"]))
}
