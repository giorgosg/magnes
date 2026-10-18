package enhancement

import (
	"bytes"
	"context"
	_ "embed"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/bitmagnet-io/bitmagnet/internal/lazy"
	"github.com/bitmagnet-io/bitmagnet/internal/model"
	"github.com/bitmagnet-io/bitmagnet/internal/tmdb"
)

// Evaluator has no database or application dependencies. Reuse it across a command
// so concurrency, requests, billed output and spending share one process-local bound.
type Evaluator struct {
	config    Config
	catalogue lazy.Lazy[tmdb.Client]
	http      *http.Client
	slots     chan struct{}
	mu        sync.Mutex
	requests  int
	output    int
	cost      float64
}

func NewEvaluator(c Config, cat lazy.Lazy[tmdb.Client], client *http.Client) (*Evaluator, error) {
	if err := c.Validate(); err != nil {
		return nil, err
	}

	if client == nil {
		client = http.DefaultClient
	}

	return &Evaluator{config: c, catalogue: cat, http: client, slots: make(chan struct{}, c.Concurrency)}, nil
}

//revive:disable:nested-structs
type toolCall struct {
	ID       string `json:"id"`
	Type     string `json:"type"`
	Function struct {
		Name      string `json:"name"`
		Arguments string `json:"arguments"`
	} `json:"function"`
}
type message struct {
	ReasoningContent *string `json:"reasoning_content,omitempty"`
	// OpenRouter's reasoning blocks, returned verbatim on later turns as it recommends.
	ReasoningDetails json.RawMessage `json:"reasoning_details,omitempty"`
	Role             string          `json:"role"`
	Content          string          `json:"content,omitempty"`
	ToolCalls        []toolCall      `json:"tool_calls,omitempty"`
	ToolCallID       string          `json:"tool_call_id,omitempty"`
}

var errMalformedEnvelope = errors.New("invalid model response envelope")

// maxRequestBytes bounds each serialized request. A token is never shorter than a
// UTF-8 byte, so the same number also bounds input tokens without assuming the
// model's tokenizer, and is what a request reserves and is checked against.
// Measured on DeepSeek, 32,000 bytes of this request is about 9,500 tokens.
const maxRequestBytes = 32000

type completion struct {
	Choices []struct {
		Message      message `json:"message"`
		FinishReason string  `json:"finish_reason"`
	} `json:"choices"`
	Usage *struct {
		PromptTokens     *int `json:"prompt_tokens"`
		CompletionTokens *int `json:"completion_tokens"`
		Details          struct {
			ReasoningTokens int `json:"reasoning_tokens"`
		} `json:"completion_tokens_details"`
	} `json:"usage"`
}

// instructions are the model's fixed rules. The configured tag list follows them, so the
// whole system prompt is one stable, cacheable prefix unless operator instructions are set.
//
//go:embed instructions.txt
var instructions string

const tagsInstruction = "\ntags: only from this list, when the torrent evidence itself shows them: "

var searchTool = json.RawMessage(
	`{
  "type": "function",
  "function": {
    "name": "search_works",
    "description": "Search movies or series; refine title or known premiere year. At most ten candidates.",
    "parameters": {
      "type": "object",
      "properties": {
        "title": {
          "type": "string"
        },
        "type": {
          "type": "string",
          "enum": [
            "movie",
            "tv_show"
          ]
        },
        "year": {
          "type": "integer"
        }
      },
      "required": [
        "title",
        "type"
      ],
      "additionalProperties": false
    }
  }
}`,
)

func (e *Evaluator) Evaluate(parent context.Context, evidence Evidence) (result Evaluation) {
	start := time.Now()
	result.InfoHash = evidence.InfoHash

	result.Outcome = OutcomeLimited
	defer func() { result.LatencyMS = time.Since(start).Milliseconds() }()

	ctx, cancel := context.WithTimeout(parent, e.config.Timeout)
	defer cancel()

	select {
	case e.slots <- struct{}{}:
		defer func() { <-e.slots }()
	case <-ctx.Done():
		result.Error = "evaluation canceled"
		return result
	}

	input, err := modelEvidence(evidence)
	if err != nil {
		result.Outcome = OutcomeMalformed
		result.Error = "invalid evidence"

		return result
	}

	prompt := instructions + tagsInstruction + strings.Join(e.config.Tags, ",")
	if e.config.Prompt != "" {
		prompt += "\nAdditional operator instructions: " + e.config.Prompt
	}

	messages := []message{{Role: "system", Content: prompt}, {Role: "user", Content: string(input)}}
	candidates := map[Work]struct{}{}
	retries, malformed := 0, 0

	for {
		result.Outcome = OutcomeLimited

		result.Error = ""
		if ctx.Err() != nil {
			result.Error = canceledMessage
			return result
		}

		remaining := min(e.config.RequestOutputTokens, e.config.MaxOutputTokens-result.Usage.OutputTokens)
		if result.Usage.Requests >= e.config.MaxRequests || remaining <= 0 {
			result.Error = "evaluation request/output limit"
			return result
		}

		payload := map[string]any{
			"model":                       e.config.Model,
			"messages":                    messages,
			"tools":                       []json.RawMessage{searchTool},
			"temperature":                 e.config.Temperature,
			e.config.OutputTokenParameter: remaining,
		}

		addReasoning(payload, e.config)

		if len(e.config.ProviderOrder) > 0 {
			payload["provider"] = map[string]any{
				"order":              e.config.ProviderOrder,
				"allow_fallbacks":    false,
				"require_parameters": true,
			}
		}

		body, marshalErr := json.Marshal(payload)
		if marshalErr != nil {
			result.Outcome = OutcomeMalformed
			result.Error = "invalid request"

			return result
		}
		// A conservative byte ceiling bounds tokens without guessing the configured model's tokenizer.
		// It covers instructions, evidence, tool definitions, previous calls and search summaries.
		if len(body) > maxRequestBytes {
			result.Error = fmt.Sprintf("input context limit (%d UTF-8 bytes)", maxRequestBytes)
			return result
		}

		reservedCost := float64(
			maxRequestBytes,
		)*e.config.InputCostPerMillion/1e6 + float64(
			remaining,
		)*e.config.OutputCostPerMillion/1e6
		if !e.reserve(remaining, reservedCost) {
			result.Error = "process-local run budget exhausted"
			return result
		}

		accounting := automaticAccounting(ctx)
		reservation := ""

		if accounting != nil {
			var reserveErr error

			reservation, reserveErr = accounting.reserve(ctx, reservedCost)
			if reserveErr != nil {
				e.reconcile(remaining, 0, reservedCost, 0)

				result.Outcome = OutcomeDeferred
				// Once a request was made, a budget interruption completes this
				// attempt as limited; a restart must not reset its effective caps.
				if result.Usage.Requests > 0 {
					result.Outcome = OutcomeLimited
				}

				result.Error = reserveErr.Error()

				return result
			}
		}

		result.Usage.Requests++

		reply, status, requestErr := e.request(ctx, body)

		knownUsage := reply.Usage != nil && reply.Usage.PromptTokens != nil &&
			reply.Usage.CompletionTokens != nil && *reply.Usage.PromptTokens >= 0 &&
			*reply.Usage.CompletionTokens >= 0 && reply.Usage.Details.ReasoningTokens >= 0
		if accounting != nil && (requestErr != nil || !knownUsage) {
			if settleErr := accounting.settle(ctx, reservation, nil, status); settleErr != nil {
				result.Outcome = OutcomeProviderFailure
				result.Error = "shared accounting unavailable"

				return result
			}
		}

		if requestErr != nil {
			// Unknown usage remains charged at the full reservation: a timed-out request may be billed.
			e.accountUnknownUsage(&result.Usage, remaining, reservedCost)

			if ctx.Err() != nil {
				result.Error = canceledMessage
				return result
			}

			result.Outcome = OutcomeProviderFailure
			result.Error = "model request failed"
			// Only explicit HTTP rejection guarantees no generation; allow transient retries
			// while retaining the conservative run reservation for unavailable usage.
			if errors.Is(requestErr, errMalformedEnvelope) {
				malformed++

				retries++
				if malformed <= 1 && retries < 3 && backoff(ctx, retries) {
					continue
				}

				result.Outcome = OutcomeMalformed
				result.Error = "invalid model response envelope"

				return result
			}

			if status == 0 || status == 429 || status >= 500 {
				// Keep unknown billed output charged against the evaluation bound.
				retries++
				if retries < 3 && backoff(ctx, retries) {
					result.Outcome = OutcomeLimited
					continue
				}
			}

			return result
		}

		if !knownUsage {
			e.accountUnknownUsage(&result.Usage, remaining, reservedCost)
			result.Outcome = OutcomeProviderFailure
			result.Error = "missing or invalid billed usage"

			return result
		}

		billed := *reply.Usage.CompletionTokens

		// OpenAI completion_tokens already includes reasoning; do not double count it.
		if reply.Usage.Details.ReasoningTokens > billed {
			billed = reply.Usage.Details.ReasoningTokens
		}

		actualCost := float64(
			*reply.Usage.PromptTokens,
		)*e.config.InputCostPerMillion/1e6 + float64(
			billed,
		)*e.config.OutputCostPerMillion/1e6
		if accounting != nil {
			if settleErr := accounting.settle(ctx, reservation, &actualCost, status); settleErr != nil {
				result.Outcome = OutcomeProviderFailure
				result.Error = "shared accounting unavailable"

				return result
			}
		}

		e.reconcile(remaining, billed, reservedCost, actualCost)

		result.Usage.InputTokens += *reply.Usage.PromptTokens
		result.Usage.OutputTokens += billed

		result.Usage.ReasoningTokens += reply.Usage.Details.ReasoningTokens
		if e.config.InputCostPerMillion > 0 && e.config.OutputCostPerMillion > 0 {
			if result.Usage.Cost == nil {
				result.Usage.Cost = new(float64)
			}

			*result.Usage.Cost += actualCost
		}

		if ctx.Err() != nil {
			result.Error = canceledMessage
			return result
		}

		if billed > remaining || *reply.Usage.PromptTokens > maxRequestBytes || e.overBudget() {
			result.Error = "provider exceeded reserved budget"
			return result
		}

		bad := len(reply.Choices) != 1
		if !bad {
			choice := reply.Choices[0]

			msg := choice.Message
			if choice.FinishReason == "length" {
				result.Error = "model output limit"
				return result
			}

			if len(msg.ToolCalls) > 0 {
				if result.Usage.ToolCalls+len(msg.ToolCalls) > e.config.MaxToolCalls {
					result.Error = "tool call limit"
					return result
				}

				result.Usage.ToolCalls += len(msg.ToolCalls)
				for _, call := range msg.ToolCalls {
					if _, err := parseSearch(call); err != nil {
						bad = true
						break
					}
				}

				if !bad {
					messages = append(messages, msg)
					for _, call := range msg.ToolCalls {
						found, searchErr := e.search(ctx, call)
						if searchErr != nil {
							if ctx.Err() != nil {
								result.Error = canceledMessage
								return result
							}

							result.Outcome = OutcomeProviderFailure
							result.Error = "catalogue search failed"
							result.Failure = catalogueFailure(searchErr)

							return result
						}

						for _, candidate := range found {
							candidates[candidate.Work] = struct{}{}
						}

						encoded, encodeErr := json.Marshal(found)
						if encodeErr != nil {
							result.Outcome = OutcomeMalformed
							result.Error = "invalid search response"

							return result
						}

						messages = append(
							messages,
							message{
								Role:       "tool",
								ToolCallID: call.ID,
								Content:    string(encoded),
							},
						)
					}

					continue
				}
			} else {
				metadata, validationErr := validateMetadata(msg.Content, e.config, candidates)
				if validationErr == nil && choice.FinishReason == "stop" {
					if ctx.Err() != nil {
						result.Error = canceledMessage
						return result
					}

					result.Result = metadata
					result.Outcome = OutcomeAccepted

					empty, encodeErr := json.Marshal(metadata)
					if encodeErr != nil {
						result.Result = nil
						result.Outcome = OutcomeMalformed

						return result
					}

					if string(empty) == "{}" {
						result.Outcome = OutcomeUnresolved
					}

					return result
				}
			}
		}

		malformed++

		retries++
		if malformed > 1 || retries >= 3 {
			result.Outcome = OutcomeMalformed
			result.Error = "invalid structured output"

			return result
		}

		messages = append(
			messages,
			message{
				Role:    "user",
				Content: "Return valid metadata JSON with allowed fields and retrieved IDs; omit unknowns.",
			},
		)
	}
}

// Keep the report aligned with the full reservation retained in the run ledger.
// Missing counters are unknown, never a zero-token completion or a refund.
func (e *Evaluator) accountUnknownUsage(usage *Usage, output int, cost float64) {
	usage.InputTokens += maxRequestBytes
	usage.OutputTokens += output

	usage.Estimated = true
	if e.config.InputCostPerMillion > 0 && e.config.OutputCostPerMillion > 0 {
		if usage.Cost == nil {
			usage.Cost = new(float64)
		}

		*usage.Cost += cost
	}
}

func (e *Evaluator) reserve(output int, cost float64) bool {
	e.mu.Lock()
	defer e.mu.Unlock()

	if e.requests >= e.config.MaxRunRequests || e.output+output > e.config.MaxRunOutputTokens ||
		(e.config.MaxRunCost > 0 && e.cost+cost > e.config.MaxRunCost) {
		return false
	}

	e.requests++
	e.output += output
	e.cost += cost

	return true
}

func (e *Evaluator) reconcile(reserved, actual int, rc, ac float64) {
	e.mu.Lock()
	defer e.mu.Unlock()

	e.output += actual - reserved
	e.cost += ac - rc
}

func (e *Evaluator) overBudget() bool {
	e.mu.Lock()
	defer e.mu.Unlock()

	return e.output > e.config.MaxRunOutputTokens || (e.config.MaxRunCost > 0 && e.cost > e.config.MaxRunCost)
}

func backoff(ctx context.Context, attempt int) bool {
	timer := time.NewTimer(time.Duration(1<<uint(attempt-1)) * 100 * time.Millisecond)
	defer timer.Stop()

	select {
	case <-timer.C:
		return true
	case <-ctx.Done():
		return false
	}
}

func (e *Evaluator) request(ctx context.Context, body []byte) (completion, int, error) {
	var c completion

	req, err := http.NewRequestWithContext(
		ctx,
		http.MethodPost,
		strings.TrimRight(e.config.BaseURL, "/")+"/chat/completions",
		bytes.NewReader(body),
	)
	if err != nil {
		return c, 0, err
	}

	req.Header.Set("Authorization", "Bearer "+e.config.APIKey)
	req.Header.Set("Content-Type", "application/json")

	resp, err := e.http.Do(req)
	if err != nil {
		return c, 0, errors.New("model transport failure")
	}

	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusOK {
		return c, resp.StatusCode, fmt.Errorf("model HTTP %d", resp.StatusCode)
	}

	data, err := io.ReadAll(io.LimitReader(resp.Body, 256*1024+1))
	if err != nil || len(data) > 256*1024 {
		return c, resp.StatusCode, errors.New("invalid model response size")
	}

	if err = json.Unmarshal(data, &c); err != nil {
		return c, resp.StatusCode, errMalformedEnvelope
	}

	return c, resp.StatusCode, nil
}

type searchArguments struct {
	Title string `json:"title"`
	Type  string `json:"type"`
	Year  int    `json:"year"`
}

func parseSearch(call toolCall) (searchArguments, error) {
	var args searchArguments
	if call.ID == "" || call.Type != "function" || call.Function.Name != "search_works" {
		return args, errors.New("unknown tool")
	}

	if err := decodeStrict([]byte(call.Function.Arguments), &args); err != nil {
		return args, err
	}

	if strings.TrimSpace(args.Title) == "" || len(args.Title) > 300 ||
		(args.Type != WorkMovie && args.Type != WorkSeries) ||
		(args.Year != 0 && (args.Year < 1800 || args.Year > 2200)) {
		return args, errors.New("invalid search arguments")
	}

	return args, nil
}

func (e *Evaluator) search(ctx context.Context, call toolCall) ([]Candidate, error) {
	args, err := parseSearch(call)
	if err != nil {
		return nil, err
	}

	if e.catalogue == nil {
		return nil, tmdb.ErrConfiguration
	}

	client, err := e.catalogue.Get()
	if err != nil {
		return nil, err
	}

	found := make([]Candidate, 0, 10)

	if args.Type == WorkMovie {
		req := tmdb.SearchMovieRequest{Query: args.Title}
		if args.Year != 0 {
			req.Year = model.Year(args.Year)
		}

		response, err := client.SearchMovie(ctx, req)
		if err != nil {
			return nil, err
		}

		for _, r := range response.Results {
			if len(found) == 10 {
				break
			}

			found = append(
				found,
				Candidate{
					Work:          Work{Type: WorkMovie, ID: r.ID},
					Title:         clip(r.Title, 200),
					OriginalTitle: distinct(r.OriginalTitle, r.Title),
					Date:          r.ReleaseDate,
					Overview:      clip(r.Overview, 100),
				},
			)
		}
	} else {
		req := tmdb.SearchTvRequest{Query: args.Title}
		if args.Year != 0 {
			req.FirstAirDateYear = model.Year(args.Year)
		}

		response, err := client.SearchTv(ctx, req)
		if err != nil {
			return nil, err
		}

		for _, r := range response.Results {
			if len(found) == 10 {
				break
			}

			found = append(
				found,
				Candidate{
					Work:          Work{Type: WorkSeries, ID: r.ID},
					Title:         clip(r.Name, 200),
					OriginalTitle: distinct(r.OriginalName, r.Name),
					Date:          r.FirstAirDate,
					Overview:      clip(r.Overview, 100),
				},
			)
		}
	}

	return found, nil
}

// addReasoning expresses thinking and effort in the configured provider's own field.
func addReasoning(payload map[string]any, c Config) {
	if c.ReasoningParameter == reasoningOpenRouter {
		switch {
		case c.ReasoningEffort != "":
			payload[reasoningOpenRouter] = map[string]string{"effort": c.ReasoningEffort}
		case c.Thinking != "":
			payload[reasoningOpenRouter] = map[string]bool{"enabled": c.Thinking == thinkingEnabled}
		}

		return
	}

	if c.Thinking != "" {
		payload[reasoningNative] = map[string]string{"type": c.Thinking}
	}

	if c.ReasoningEffort != "" {
		payload["reasoning_effort"] = c.ReasoningEffort
	}
}

// distinct omits an original title that only repeats the title.
func distinct(original, title string) string {
	if original == title {
		return ""
	}

	return clip(original, 200)
}

// modelEvidence serializes evidence for the model without null, empty-string or
// empty-array values, which only spend request bytes. Empty objects are kept because
// they carry meaning, such as an episode marker. Fingerprints use Evidence unchanged.
func modelEvidence(e Evidence) ([]byte, error) {
	data, err := json.Marshal(e)
	if err != nil {
		return nil, err
	}

	// Keep numbers as their original text rather than float64.
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.UseNumber()

	var v any
	if err := decoder.Decode(&v); err != nil {
		return nil, err
	}

	v, _ = compact(v)

	return json.Marshal(v)
}

// compact reports false for values to drop. An object or array is dropped only if
// compaction emptied it.
func compact(v any) (any, bool) {
	switch t := v.(type) {
	case nil:
		return nil, false
	case string:
		return t, t != ""
	case []any:
		out := make([]any, 0, len(t))

		for _, item := range t {
			if c, ok := compact(item); ok {
				out = append(out, c)
			}
		}

		return out, len(out) > 0
	case map[string]any:
		if len(t) == 0 {
			return t, true
		}

		out := make(map[string]any, len(t))

		for k, item := range t {
			if c, ok := compact(item); ok {
				out[k] = c
			}
		}

		return out, len(out) > 0
	}

	return v, true
}

func clip(s string, n int) string {
	r := []rune(s)
	if len(r) > n {
		return string(r[:n])
	}

	return s
}
