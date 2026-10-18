// Package enhancement evaluates bounded torrent evidence and safely applies retained metadata.
package enhancement

import (
	"errors"
	"math"
	"net/url"
	"regexp"
	"strings"
	"time"
)

type Config struct {
	Automatic            bool
	DailyBudget          float64
	PollInterval         time.Duration
	ReconsiderInterval   time.Duration
	BaseURL              string
	APIKey               string
	Model                string
	Temperature          float64
	Thinking             string
	ReasoningEffort      string
	ReasoningParameter   string
	ProviderOrder        []string
	Prompt               string
	Tags                 []string
	Concurrency          int
	MaxRequests          int
	MaxToolCalls         int
	Timeout              time.Duration
	OutputTokenParameter string
	RequestOutputTokens  int
	MaxOutputTokens      int
	MaxRunRequests       int
	MaxRunOutputTokens   int
	RunTimeout           time.Duration
	MaxRunCost           float64
	InputCostPerMillion  float64
	OutputCostPerMillion float64
}

const (
	thinkingEnabled  = "enabled"
	thinkingDisabled = "disabled"
	// reasoningNative is DeepSeek's own API field; reasoningOpenRouter is OpenRouter's.
	reasoningNative     = "thinking"
	reasoningOpenRouter = "reasoning"
)

// providerPattern accepts OpenRouter provider slugs such as deepseek or open-inference/fp4.
var providerPattern = regexp.MustCompile(`^[a-z0-9][a-z0-9./-]{0,63}$`)

func NewDefaultConfig() Config {
	return Config{
		Concurrency:          2,
		PollInterval:         5 * time.Second,
		ReconsiderInterval:   24 * time.Hour,
		MaxRequests:          4,
		MaxToolCalls:         6,
		Timeout:              2 * time.Minute,
		MaxOutputTokens:      8000,
		RequestOutputTokens:  2000,
		OutputTokenParameter: "max_completion_tokens",
		ReasoningParameter:   reasoningNative,
		MaxRunRequests:       40,
		MaxRunOutputTokens:   80000,
		RunTimeout:           10 * time.Minute,
		Tags: []string{
			"llm-platform-windows",
			tagPlatformLinux,
			"llm-platform-macos",
			"llm-platform-android",
			"llm-platform-ios",
			tagEditionDeluxe,
			"llm-edition-collectors",
		},
	}
}

func (c Config) Validate() error {
	if c.Automatic && (c.DailyBudget <= 0 || math.IsNaN(c.DailyBudget) || math.IsInf(c.DailyBudget, 0) ||
		c.InputCostPerMillion <= 0 || c.OutputCostPerMillion <= 0 ||
		c.PollInterval <= 0 || c.ReconsiderInterval <= 0) {
		return errors.New(
			"automatic enhancement requires positive budget, token prices and scheduling intervals",
		)
	}

	if c.Thinking != "" && c.Thinking != thinkingEnabled && c.Thinking != thinkingDisabled {
		return errors.New("thinking must be empty, enabled or disabled")
	}

	if c.ReasoningParameter != "" && c.ReasoningParameter != reasoningNative &&
		c.ReasoningParameter != reasoningOpenRouter {
		return errors.New("reasoning parameter must be empty, thinking or reasoning")
	}

	if c.ReasoningEffort != "" && c.ReasoningEffort != "low" && c.ReasoningEffort != "high" &&
		c.ReasoningEffort != "max" {
		return errors.New("reasoning effort must be empty, low, high or max")
	}

	if c.ReasoningEffort != "" && c.Thinking == thinkingDisabled {
		return errors.New("reasoning effort requires thinking to be enabled or unset")
	}

	for _, provider := range c.ProviderOrder {
		if !providerPattern.MatchString(provider) {
			return errors.New("invalid provider in provider order")
		}
	}

	if c.OutputTokenParameter != "max_completion_tokens" && c.OutputTokenParameter != "max_tokens" {
		return errors.New("output token parameter must be max_completion_tokens or max_tokens")
	}

	u, err := url.Parse(c.BaseURL)
	if err != nil || u.Host == "" || (u.Scheme != "https" && u.Scheme != "http") || c.Model == "" ||
		c.APIKey == "" {
		return errors.New("enhancement requires an explicit HTTP(S) base URL, API key and model")
	}

	if c.Concurrency < 1 || c.MaxRequests < 1 || c.MaxToolCalls < 1 || c.Timeout <= 0 ||
		c.RequestOutputTokens < 1 ||
		c.MaxOutputTokens < 1 ||
		c.MaxRunRequests < 1 ||
		c.MaxRunOutputTokens < 1 ||
		c.RunTimeout <= 0 ||
		c.Temperature < 0 ||
		c.Temperature > 2 {
		return errors.New("enhancement limits must be positive and temperature in [0,2]")
	}

	if math.IsNaN(c.Temperature) || math.IsInf(c.Temperature, 0) || math.IsNaN(c.MaxRunCost) ||
		math.IsInf(c.MaxRunCost, 0) ||
		math.IsNaN(c.InputCostPerMillion) ||
		math.IsInf(c.InputCostPerMillion, 0) ||
		math.IsNaN(c.OutputCostPerMillion) ||
		math.IsInf(c.OutputCostPerMillion, 0) {
		return errors.New("enhancement numeric settings must be finite")
	}

	if c.MaxRunCost < 0 || c.InputCostPerMillion < 0 || c.OutputCostPerMillion < 0 {
		return errors.New("enhancement costs cannot be negative")
	}

	if c.MaxRunCost > 0 && (c.InputCostPerMillion <= 0 || c.OutputCostPerMillion <= 0) {
		return errors.New("spending bounds require positive input/output token prices")
	}

	for _, tag := range c.Tags {
		if !strings.HasPrefix(tag, "llm-") || len(tag) > 30 || !tagPattern.MatchString(tag) {
			return errors.New("invalid enhancement tag whitelist")
		}
	}

	return nil
}
