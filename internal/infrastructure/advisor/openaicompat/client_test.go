package openaicompat

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"example.com/marmot/internal/ports"
)

const secretKey = "sk-do-not-leak-this-anywhere"

// reply serves the content as a server-sent event stream, split across several
// chunks so the test exercises the accumulation rather than a single delta.
func reply(t *testing.T, content string, finish string) http.HandlerFunc {
	t.Helper()
	return func(writer http.ResponseWriter, request *http.Request) {
		writer.Header().Set("Content-Type", "text/event-stream")
		writer.WriteHeader(http.StatusOK)
		flusher, _ := writer.(http.Flusher)
		emit := func(payload map[string]any) {
			encoded, _ := json.Marshal(payload)
			_, _ = writer.Write([]byte("data: " + string(encoded) + "\n\n"))
			if flusher != nil {
				flusher.Flush()
			}
		}
		runes := []rune(content)
		for start := 0; start < len(runes); start += 7 {
			end := start + 7
			if end > len(runes) {
				end = len(runes)
			}
			emit(map[string]any{"choices": []any{map[string]any{"delta": map[string]any{"content": string(runes[start:end])}}}})
		}
		emit(map[string]any{"choices": []any{map[string]any{"delta": map[string]any{}, "finish_reason": finish}}})
		// Reasoning deltas are the model thinking, not the answer, and must not
		// be concatenated into the document.
		emit(map[string]any{"choices": []any{map[string]any{"delta": map[string]any{"reasoning_content": "内部推理，不应进入结果"}}}})
		emit(map[string]any{"choices": []any{}, "usage": map[string]any{"prompt_tokens": 1234, "completion_tokens": 56}})
		_, _ = writer.Write([]byte("data: [DONE]\n\n"))
	}
}

func clientFor(t *testing.T, server *httptest.Server) *Client {
	t.Helper()
	client, err := New(Config{BaseURL: server.URL, Model: "deepseek-test", APIKey: secretKey, JSONMode: JSONModeObject})
	if err != nil {
		t.Fatal(err)
	}
	return client
}

func TestCompletionsURLAcceptsEveryFormProvidersDocument(t *testing.T) {
	for base, want := range map[string]string{
		"https://api.deepseek.com":                     "https://api.deepseek.com/v1/chat/completions",
		"https://api.deepseek.com/":                    "https://api.deepseek.com/v1/chat/completions",
		"https://api.deepseek.com/v1":                  "https://api.deepseek.com/v1/chat/completions",
		"https://api.deepseek.com/v1/chat/completions": "https://api.deepseek.com/v1/chat/completions",
		"http://localhost:11434/v1":                    "http://localhost:11434/v1/chat/completions",
	} {
		if got := completionsURL(base); got != want {
			t.Fatalf("%s -> %s, want %s", base, got, want)
		}
	}
}

func TestAdviseSendsTheTwoBlocksAndReadsUsage(t *testing.T) {
	var seen chatRequest
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		raw, _ := io.ReadAll(request.Body)
		_ = json.Unmarshal(raw, &seen)
		if got := request.Header.Get("Authorization"); got != "Bearer "+secretKey {
			t.Errorf("authorization header is %q", got)
		}
		reply(t, `{"suggestions":[{"node_id":7,"name":"caches"}],"needs_expansion":[]}`, "stop")(writer, request)
	}))
	defer server.Close()

	result, err := clientFor(t, server).Advise(context.Background(), ports.AdviceRequest{System: "SYS", User: "USR"})
	if err != nil {
		t.Fatal(err)
	}
	if len(seen.Messages) != 2 || seen.Messages[0].Role != "system" || seen.Messages[0].Content != "SYS" {
		t.Fatalf("system block was not sent as sent: %#v", seen.Messages)
	}
	if seen.Messages[1].Role != "user" || seen.Messages[1].Content != "USR" {
		t.Fatalf("user block was not sent as sent: %#v", seen.Messages)
	}
	if seen.ResponseFormat == nil || seen.ResponseFormat.Type != JSONModeObject {
		t.Fatalf("response_format was not requested: %#v", seen.ResponseFormat)
	}
	if !seen.Stream || seen.StreamOptions == nil || !seen.StreamOptions.IncludeUsage {
		t.Fatalf("the request was not a usage-reporting stream: %#v", seen)
	}
	if len(result.Suggestions) != 1 || result.Suggestions[0].NodeID != 7 {
		t.Fatalf("suggestions did not survive the round trip: %#v", result.Suggestions)
	}
	if result.InputTokens != 1234 || result.OutputTokens != 56 {
		t.Fatalf("usage not reported: %d/%d", result.InputTokens, result.OutputTokens)
	}
}

// Reasoning deltas must not reach the document: concatenating the model's
// thinking into its answer would corrupt the JSON.
func TestAdviseIgnoresReasoningDeltas(t *testing.T) {
	server := httptest.NewServer(reply(t, `{"suggestions":[{"node_id":5,"name":"y"}]}`, "stop"))
	defer server.Close()
	result, err := clientFor(t, server).Advise(context.Background(), ports.AdviceRequest{})
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Suggestions) != 1 || result.Suggestions[0].NodeID != 5 {
		t.Fatalf("reasoning content leaked into the answer: %#v", result)
	}
}

// Models wrap JSON in fences even when told not to, and plenty of endpoints
// ignore response_format. Refusing a fenced reply would be a worse product for
// no safety gain: the fields are validated afterwards either way.
func TestAdviseAcceptsAFencedReply(t *testing.T) {
	server := httptest.NewServer(reply(t, "这是结果：\n```json\n{\"suggestions\":[{\"node_id\":3,\"name\":\"x\"}]}\n```\n", "stop"))
	defer server.Close()
	result, err := clientFor(t, server).Advise(context.Background(), ports.AdviceRequest{})
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Suggestions) != 1 || result.Suggestions[0].NodeID != 3 {
		t.Fatalf("fenced JSON was not recovered: %#v", result)
	}
}

// Truncated output is truncated JSON. Parsing what arrived would silently drop
// whatever the model had not finished saying.
func TestAdviseRefusesATruncatedReply(t *testing.T) {
	server := httptest.NewServer(reply(t, `{"suggestions":[{"node_id":3`, "length"))
	defer server.Close()
	if _, err := clientFor(t, server).Advise(context.Background(), ports.AdviceRequest{}); err == nil {
		t.Fatal("a length-truncated reply was accepted")
	}
}

// The credential must not reach a message the user or a log can see.
func TestErrorsAndDescribeNeverCarryTheKey(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		writer.WriteHeader(http.StatusUnauthorized)
		// A provider that echoes the credential back is not hypothetical.
		_, _ = writer.Write([]byte(`{"error":{"message":"invalid key ` + secretKey + `"}}`))
	}))
	defer server.Close()
	client := clientFor(t, server)

	_, err := client.Advise(context.Background(), ports.AdviceRequest{})
	if err == nil {
		t.Fatal("a 401 was not reported")
	}
	if strings.Contains(err.Error(), secretKey) {
		t.Fatalf("the key leaked into an error: %v", err)
	}
	if strings.Contains(client.Describe(), secretKey) {
		t.Fatalf("the key leaked into Describe: %s", client.Describe())
	}
}

// A stopped analysis must stop the request in flight and report cancellation,
// not a broken advisor.
func TestAdviseReportsCancellationAsCancellation(t *testing.T) {
	release := make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		<-release
	}))
	defer server.Close()
	defer close(release)

	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		time.Sleep(30 * time.Millisecond)
		cancel()
	}()
	_, err := clientFor(t, server).Advise(ctx, ports.AdviceRequest{})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("expected context.Canceled, got %v", err)
	}
}

func TestNewRefusesIncompleteConfiguration(t *testing.T) {
	if _, err := New(Config{Model: "m"}); err == nil {
		t.Fatal("an endpoint-less config was accepted")
	}
	if _, err := New(Config{BaseURL: "https://x"}); err == nil {
		t.Fatal("a model-less config was accepted")
	}
}

// The provider requires `type` alongside the effort; omitting it is a 400, not a
// default. Disabling thinking is a different shape again.
func TestAdviseSendsTheThinkingShapeTheProviderRequires(t *testing.T) {
	for effort, want := range map[string]*thinkingConfig{
		"":         nil,
		"low":      {Type: "enabled", ReasoningEffort: "low"},
		"max":      {Type: "enabled", ReasoningEffort: "max"},
		"disabled": {Type: "disabled"},
	} {
		var seen chatRequest
		server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
			raw, _ := io.ReadAll(request.Body)
			_ = json.Unmarshal(raw, &seen)
			reply(t, `{"suggestions":[]}`, "stop")(writer, request)
		}))
		client, err := New(Config{BaseURL: server.URL, Model: "m", ReasoningEffort: effort})
		if err != nil {
			t.Fatal(err)
		}
		if _, err := client.Advise(context.Background(), ports.AdviceRequest{}); err != nil {
			t.Fatal(err)
		}
		server.Close()
		switch {
		case want == nil && seen.Thinking != nil:
			t.Fatalf("effort %q sent %#v, expected the field to be omitted", effort, seen.Thinking)
		case want != nil && seen.Thinking == nil:
			t.Fatalf("effort %q sent no thinking field", effort)
		case want != nil && *seen.Thinking != *want:
			t.Fatalf("effort %q sent %#v, want %#v", effort, *seen.Thinking, *want)
		}
	}
}
