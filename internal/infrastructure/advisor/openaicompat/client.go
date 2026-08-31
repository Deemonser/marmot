// Package openaicompat talks to any endpoint that speaks the OpenAI chat
// completions protocol: DeepSeek, OpenAI itself, Kimi, Qwen, OpenRouter, and a
// locally hosted vLLM or Ollama. One protocol covers all of them, so "supporting
// several providers" is a configuration field rather than several adapters
// (ADR-0061 §5).
//
// Written against net/http rather than a vendor SDK on purpose: the protocol
// surface used here is one POST, and adding a dependency for it would mean
// pinning and licence-recording a library for something the standard library
// does completely.
package openaicompat

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"example.com/marmot/internal/domain/recommendation"
	"example.com/marmot/internal/ports"
)

// Config is what the user supplies. The key is never logged, never included in
// an error, and never returned by Describe.
type Config struct {
	// BaseURL with or without a trailing /v1, e.g. https://api.deepseek.com.
	BaseURL string
	Model   string
	APIKey  string
	// Temperature is sent when non-nil. It is not optional in practice: the task
	// is extraction and classification against a fixed contract, and a provider
	// default of 1.0 makes the model sample rather than decide. Measured with no
	// temperature set: three runs over one identical evidence pack produced 42,
	// 48 and 27 suggestions with a pairwise Jaccard of 0.35 -- two thirds of what
	// a user sees would change between two clicks on the same disk.
	Temperature *float64
	// ReasoningEffort is sent as thinking.reasoning_effort when non-empty.
	// Reasoning models default to a high effort, and on this task that is money
	// and latency spent on the wrong thing: classifying rows against a fixed
	// output contract is not what deep deliberation is for. Measured on
	// deepseek-v4-flash at the default effort, one round spent 239s and then hit
	// the output cap with the answer unfinished.
	//
	// Empty means the field is omitted entirely, because an endpoint that does
	// not know it will reject the request.
	ReasoningEffort string
	// JSONMode constrains the reply format. Empty relies on the prompt alone,
	// which every endpoint supports; "json_object" is understood by most;
	// "json_schema" by some. The reply is parsed leniently either way, so a
	// provider that ignores the field still works.
	JSONMode string
	Timeout  time.Duration
}

const (
	JSONModeNone   = ""
	JSONModeObject = "json_object"
	JSONModeSchema = "json_schema"

	// A reasoning model producing a long structured reply can run for minutes,
	// and http.Client.Timeout covers the whole exchange including reading the
	// body -- so a non-streaming request with a short client timeout fails while
	// the model is still working. Measured: 120s was not enough for one round on
	// a 20k-token evidence pack. Streaming removes the "wait for everything,
	// then read" shape; this remains as a whole-run ceiling on the context.
	defaultTimeout     = 15 * time.Minute
	defaultMaxOutput   = 8192
	maxErrorBodyLength = 400
)

type Client struct {
	config  Config
	timeout time.Duration
	http    *http.Client
}

func New(config Config) (*Client, error) {
	if strings.TrimSpace(config.BaseURL) == "" {
		return nil, errors.New("advisor endpoint is required")
	}
	if strings.TrimSpace(config.Model) == "" {
		return nil, errors.New("advisor model is required")
	}
	timeout := config.Timeout
	if timeout <= 0 {
		timeout = defaultTimeout
	}
	// Deliberately no http.Client.Timeout: it bounds the entire exchange
	// including the streamed body, which would cut a long reply off mid-answer.
	// The deadline lives on the request context instead, where the caller's own
	// cancellation already is.
	return &Client{config: config, timeout: timeout, http: &http.Client{}}, nil
}

// Describe is for the UI. It names where the request goes and which model
// answers, and deliberately carries no credential.
func (c *Client) Describe() string {
	return c.config.Model + " @ " + endpointHost(c.config.BaseURL)
}

type chatMessage struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

type chatRequest struct {
	Model          string          `json:"model"`
	Messages       []chatMessage   `json:"messages"`
	MaxTokens      int             `json:"max_tokens,omitempty"`
	Temperature    *float64        `json:"temperature,omitempty"`
	ResponseFormat *responseFormat `json:"response_format,omitempty"`
	Stream         bool            `json:"stream"`
	StreamOptions  *streamOptions  `json:"stream_options,omitempty"`
	Thinking       *thinkingConfig `json:"thinking,omitempty"`
}

// thinkingConfig mirrors the provider's shape: `type` is required alongside the
// effort, and omitting it is a 400 rather than a default.
type thinkingConfig struct {
	Type            string `json:"type"`
	ReasoningEffort string `json:"reasoning_effort,omitempty"`
}

const reasoningDisabled = "disabled"

// streamOptions asks for the usage totals in the final chunk. Without it a
// streamed response reports no token counts at all, and the panel would have
// nothing to say about what a run cost.
type streamOptions struct {
	IncludeUsage bool `json:"include_usage"`
}

type usageCounts struct {
	PromptTokens     int64 `json:"prompt_tokens"`
	CompletionTokens int64 `json:"completion_tokens"`
	Details          struct {
		// Tracked separately so a truncated answer can say whether the budget
		// went on thinking rather than on the reply.
		ReasoningTokens int64 `json:"reasoning_tokens"`
	} `json:"completion_tokens_details"`
}

// streamChunk is one server-sent event. Reasoning models also emit
// reasoning_content deltas; those are the model thinking, not the answer, and
// are deliberately not accumulated -- concatenating them into the reply would
// corrupt the JSON.
type streamChunk struct {
	Choices []struct {
		Delta struct {
			Content string `json:"content"`
		} `json:"delta"`
		FinishReason string `json:"finish_reason"`
	} `json:"choices"`
	Usage *usageCounts `json:"usage"`
	Error *struct {
		Message string `json:"message"`
	} `json:"error"`
}

type responseFormat struct {
	Type string `json:"type"`
}

func (c *Client) Advise(ctx context.Context, request ports.AdviceRequest) (recommendation.AdvisorResult, error) {
	maxTokens := request.MaxOutputTokens
	if maxTokens <= 0 {
		maxTokens = defaultMaxOutput
	}
	body := chatRequest{
		Model:         c.config.Model,
		MaxTokens:     maxTokens,
		Stream:        true,
		StreamOptions: &streamOptions{IncludeUsage: true},
		Messages: []chatMessage{
			{Role: "system", Content: request.System},
			{Role: "user", Content: request.User},
		},
	}
	switch c.config.JSONMode {
	case JSONModeObject, JSONModeSchema:
		body.ResponseFormat = &responseFormat{Type: c.config.JSONMode}
	}
	body.Temperature = c.config.Temperature
	switch effort := strings.TrimSpace(c.config.ReasoningEffort); effort {
	case "":
		// Field omitted entirely: an endpoint that does not know it would reject
		// the request outright.
	case reasoningDisabled:
		body.Thinking = &thinkingConfig{Type: reasoningDisabled}
	default:
		body.Thinking = &thinkingConfig{Type: "enabled", ReasoningEffort: effort}
	}

	encoded, err := json.Marshal(body)
	if err != nil {
		return recommendation.AdvisorResult{}, err
	}
	// The ceiling is on the context so it composes with the caller's own
	// cancellation: whichever fires first wins, and stopping an analysis stops
	// the request rather than abandoning it.
	ctx, cancel := context.WithTimeout(ctx, c.timeout)
	defer cancel()

	httpRequest, err := http.NewRequestWithContext(ctx, http.MethodPost, completionsURL(c.config.BaseURL), bytes.NewReader(encoded))
	if err != nil {
		return recommendation.AdvisorResult{}, err
	}
	httpRequest.Header.Set("Content-Type", "application/json")
	httpRequest.Header.Set("Accept", "text/event-stream")
	if key := strings.TrimSpace(c.config.APIKey); key != "" {
		httpRequest.Header.Set("Authorization", "Bearer "+key)
	}

	response, err := c.http.Do(httpRequest)
	if err != nil {
		// Cancellation is not a failure of the advisor, and must reach the caller
		// as cancellation so a stopped analysis does not read as a broken one.
		if ctx.Err() != nil {
			return recommendation.AdvisorResult{}, ctx.Err()
		}
		return recommendation.AdvisorResult{}, fmt.Errorf("无法连接 %s: %w", endpointHost(c.config.BaseURL), err)
	}
	defer response.Body.Close()

	if response.StatusCode != http.StatusOK {
		raw, _ := io.ReadAll(io.LimitReader(response.Body, 64<<10))
		return recommendation.AdvisorResult{}, statusError(response.StatusCode, raw)
	}
	return readStream(ctx, response.Body)
}

// readStream accumulates the content deltas of a server-sent event stream.
//
// Streaming is not a nicety here. The reply is a long structured document from a
// reasoning model, and a non-streaming request makes the client wait for the
// whole thing before the first byte arrives -- measured, that blew a 120s client
// timeout on one round of a 20k-token pack. Reading incrementally also means a
// cancelled analysis stops at the next chunk instead of at the end.
func readStream(ctx context.Context, body io.Reader) (recommendation.AdvisorResult, error) {
	var content strings.Builder
	var usage usageCounts
	finish := ""

	scanner := bufio.NewScanner(body)
	// One SSE line can carry a large delta; the default 64 KB limit is not
	// enough for providers that batch.
	scanner.Buffer(make([]byte, 0, 64<<10), 4<<20)
	for scanner.Scan() {
		if err := ctx.Err(); err != nil {
			return recommendation.AdvisorResult{}, err
		}
		line := strings.TrimSpace(scanner.Text())
		if line == "" || !strings.HasPrefix(line, "data:") {
			continue
		}
		payload := strings.TrimSpace(strings.TrimPrefix(line, "data:"))
		if payload == "[DONE]" {
			break
		}
		var chunk streamChunk
		if err := json.Unmarshal([]byte(payload), &chunk); err != nil {
			// A malformed chunk is not worth failing the whole run over; the
			// contract check on the assembled document is the real gate.
			continue
		}
		if chunk.Error != nil && chunk.Error.Message != "" {
			return recommendation.AdvisorResult{}, fmt.Errorf("模型返回错误: %s", chunk.Error.Message)
		}
		if chunk.Usage != nil {
			usage = *chunk.Usage
		}
		for _, choice := range chunk.Choices {
			content.WriteString(choice.Delta.Content)
			if choice.FinishReason != "" {
				finish = choice.FinishReason
			}
		}
	}
	if err := scanner.Err(); err != nil {
		if ctx.Err() != nil {
			return recommendation.AdvisorResult{}, ctx.Err()
		}
		return recommendation.AdvisorResult{}, fmt.Errorf("读取响应流失败: %w", err)
	}
	// A truncated reply is truncated JSON, and parsing it would silently drop
	// whatever the model had not finished saying. The counts go in the message
	// because they say which knob to turn: budget spent on reasoning means
	// lowering the effort, budget spent on the answer means raising the cap.
	if finish == "length" {
		return recommendation.AdvisorResult{}, fmt.Errorf(
			"模型输出被长度上限截断，建议不完整（已生成 %d token，其中推理 %d）",
			usage.CompletionTokens, usage.Details.ReasoningTokens)
	}
	result, err := parseResult(content.String())
	if err != nil {
		return recommendation.AdvisorResult{}, err
	}
	result.InputTokens = usage.PromptTokens
	result.OutputTokens = usage.CompletionTokens
	result.ReasoningTokens = usage.Details.ReasoningTokens
	return result, nil
}

// statusError keeps enough of the body to be diagnosable and not so much that a
// provider's error page floods the UI. The key is in a header, never the body,
// so this cannot leak it.
func statusError(status int, body []byte) error {
	text := strings.TrimSpace(string(body))
	if len(text) > maxErrorBodyLength {
		text = text[:maxErrorBodyLength] + "…"
	}
	switch status {
	case http.StatusUnauthorized, http.StatusForbidden:
		return fmt.Errorf("API key 被拒绝 (HTTP %d)", status)
	case http.StatusNotFound:
		return fmt.Errorf("endpoint 或模型名不存在 (HTTP %d): %s", status, text)
	case http.StatusTooManyRequests:
		return fmt.Errorf("触发限流 (HTTP %d)，稍后重试", status)
	}
	return fmt.Errorf("请求失败 (HTTP %d): %s", status, text)
}

// parseResult is deliberately lenient about the wrapper and strict about the
// contents. Models wrap JSON in ```json fences even when told not to, and an
// endpoint that ignores response_format is common enough that refusing a fenced
// reply would just be a worse product for no safety gain -- the fields are
// validated afterwards either way.
func parseResult(content string) (recommendation.AdvisorResult, error) {
	trimmed := strings.TrimSpace(content)
	if trimmed == "" {
		return recommendation.AdvisorResult{}, errors.New("模型返回了空内容")
	}
	if fenced := extractFenced(trimmed); fenced != "" {
		trimmed = fenced
	}
	start := strings.IndexByte(trimmed, '{')
	end := strings.LastIndexByte(trimmed, '}')
	if start < 0 || end <= start {
		return recommendation.AdvisorResult{}, fmt.Errorf("模型没有返回 JSON: %s", snippet(trimmed))
	}
	var result recommendation.AdvisorResult
	if err := json.Unmarshal([]byte(trimmed[start:end+1]), &result); err != nil {
		return recommendation.AdvisorResult{}, fmt.Errorf("模型返回的 JSON 无法解析: %w", err)
	}
	return result, nil
}

func extractFenced(text string) string {
	const fence = "```"
	first := strings.Index(text, fence)
	if first < 0 {
		return ""
	}
	rest := text[first+len(fence):]
	if newline := strings.IndexByte(rest, '\n'); newline >= 0 {
		rest = rest[newline+1:]
	}
	if closing := strings.Index(rest, fence); closing >= 0 {
		return strings.TrimSpace(rest[:closing])
	}
	return strings.TrimSpace(rest)
}

func snippet(text string) string {
	if len(text) > 160 {
		return text[:160] + "…"
	}
	return text
}

// completionsURL accepts a bare host, a host with /v1, or a full path, so a user
// pasting any of the three forms a provider's docs use gets a working endpoint.
func completionsURL(base string) string {
	trimmed := strings.TrimRight(strings.TrimSpace(base), "/")
	if strings.HasSuffix(trimmed, "/chat/completions") {
		return trimmed
	}
	if strings.HasSuffix(trimmed, "/v1") {
		return trimmed + "/chat/completions"
	}
	return trimmed + "/v1/chat/completions"
}

func endpointHost(base string) string {
	trimmed := strings.TrimSpace(base)
	trimmed = strings.TrimPrefix(strings.TrimPrefix(trimmed, "https://"), "http://")
	if slash := strings.IndexByte(trimmed, '/'); slash > 0 {
		trimmed = trimmed[:slash]
	}
	return trimmed
}
