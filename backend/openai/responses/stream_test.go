package responses

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/eul-ai/eul/agent"
)

func TestResponsesSSEOrdersCompletedItemsByOutputIndex(t *testing.T) {
	body := strings.Join([]string{
		`data: {"type":"response.output_item.done","output_index":1,"item":{"type":"message","content":[{"type":"output_text","text":"second"}]}}`,
		`data: {"type":"response.output_item.done","output_index":0,"item":{"type":"message","content":[{"type":"output_text","text":"first"}]}}`,
		`data: {"type":"response.completed","response":{"status":"completed"}}`,
	}, "\n\n") + "\n\n"

	response, err := readResponsesSSE(strings.NewReader(body), 1<<20, nil)
	if err != nil {
		t.Fatal(err)
	}
	text, _, _, err := normalizeResponse(response)
	if err != nil {
		t.Fatal(err)
	}
	if text != "firstsecond" {
		t.Fatalf("text = %q, want %q", text, "firstsecond")
	}
}

func TestResponsesSSEDoesNotRetryAfterDelivery(t *testing.T) {
	tests := []struct {
		name string
		body string
	}{
		{name: "text", body: `data: {"type":"response.output_text.delta","delta":"partial"}` + "\n\n"},
		{name: "reasoning", body: `data: {"type":"response.reasoning_text.delta","delta":"thinking"}` + "\n\n"},
		{name: "tool", body: `data: {"type":"response.output_item.added","output_index":0,"item":{"type":"function_call","call_id":"call_1","name":"read","arguments":""}}` + "\n\n"},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			deliveries := 0
			observer := &streamObserver{observer: agent.StreamObserver{
				Text: func(string) error {
					deliveries++
					return nil
				},
				Reasoning: func(string) error {
					deliveries++
					return nil
				},
				ToolCall: func(agent.ToolCallSnapshot) error {
					deliveries++
					return nil
				},
			}}
			_, err := readResponsesSSE(strings.NewReader(test.body), 1024, observer)
			var partial *partialResponseError
			if deliveries != 1 || !errors.As(err, &partial) {
				t.Fatalf("deliveries=%d error=%v", deliveries, err)
			}
			if _, retry := (&Client{}).RetryGeneration(err, 1); retry {
				t.Fatal("partial response is retryable")
			}
		})
	}

	_, err := readResponsesSSE(strings.NewReader(tests[0].body), 1024, &streamObserver{})
	var partial *partialResponseError
	if errors.As(err, &partial) {
		t.Fatalf("undelivered response was marked partial: %v", err)
	}
	if _, retry := (&Client{}).RetryGeneration(err, 1); !retry {
		t.Fatal("undelivered incomplete response is not retryable")
	}
}

func TestClientStreamsTextDeltas(t *testing.T) {
	releaseTerminal := make(chan struct{})
	released := false
	server := newTestServer(t, http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		var wire createResponseRequest
		if err := json.NewDecoder(request.Body).Decode(&wire); err != nil {
			t.Error(err)
		}
		if !wire.Stream || request.Header.Get("Accept") != "text/event-stream" {
			t.Errorf("stream=%v accept=%q", wire.Stream, request.Header.Get("Accept"))
		}
		writer.Header().Set("Content-Type", "text/event-stream; charset=utf-8")
		fmt.Fprint(writer, "data: {\"type\":\"response.output_text.delta\",\"sequence_number\":0,\"delta\":\"streamed \"}\n\n")
		writer.(http.Flusher).Flush()
		<-releaseTerminal
		fmt.Fprint(writer, "data: {\"type\":\"response.output_text.delta\",\"sequence_number\":1,\"delta\":\"answer\"}\n\n")
		fmt.Fprint(writer, "data: {\"type\":\"response.output_item.done\",\"sequence_number\":2,\"output_index\":0,\"item\":{\"type\":\"message\",\"content\":[{\"type\":\"output_text\",\"text\":\"streamed answer\"}]}}\n\n")
		fmt.Fprint(writer, "data: {\"type\":\"response.completed\",\"sequence_number\":3,\"response\":{\"status\":\"completed\"}}\n\n")
	}))
	defer server.Close()
	defer func() {
		if !released {
			close(releaseTerminal)
		}
	}()

	client := newTestClient(t, "key", server.URL, Options{})
	var deltas []string
	seenDelta := make(chan string, 2)
	type outcome struct {
		response agent.Response
		err      error
	}
	done := make(chan outcome, 1)
	go func() {
		response, err := generate(client, context.Background(), baseRequest(), func(delta string) error {
			deltas = append(deltas, delta)
			seenDelta <- delta
			return nil
		}, nil, nil)
		done <- outcome{response: response, err: err}
	}()
	select {
	case delta := <-seenDelta:
		if delta != "streamed " {
			t.Fatalf("first delta = %q", delta)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("first delta was not delivered before the terminal event")
	}
	close(releaseTerminal)
	released = true
	result := <-done
	if result.err != nil {
		t.Fatal(result.err)
	}
	if result.response.Text != "streamed answer" || !slices.Equal(deltas, []string{"streamed ", "answer"}) {
		t.Fatalf("response=%+v deltas=%q", result.response, deltas)
	}
}

func TestClientStreamsRefusal(t *testing.T) {
	server := newTestServer(t, http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		writer.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprint(writer, "data: {\"type\":\"response.refusal.delta\",\"delta\":\"Cannot comply.\"}\n\n")
		fmt.Fprint(writer, "data: {\"type\":\"response.output_item.done\",\"item\":{\"type\":\"message\",\"content\":[{\"type\":\"refusal\",\"refusal\":\"Cannot comply.\"}]}}\n\n")
		fmt.Fprint(writer, "data: {\"type\":\"response.completed\",\"response\":{\"status\":\"completed\"}}\n\n")
	}))
	defer server.Close()

	client := newTestClient(t, "key", server.URL, Options{})
	var delivered string
	response, err := generate(client, context.Background(), baseRequest(), func(delta string) error { delivered += delta; return nil }, nil, nil)
	if err != nil || response.Text != "Cannot comply." || delivered != response.Text {
		t.Fatalf("response=%+v delivered=%q error=%v", response, delivered, err)
	}
}

func TestClientDecodesRefusalAndPreservesMalformedToolArguments(t *testing.T) {
	server := responseServer(t, http.StatusOK, `{
		"status":"completed",
		"output":[
			{"type":"message","content":[{"type":"refusal","refusal":"Cannot comply."}]},
			{"type":"function_call","id":"fc_item","call_id":"call_1","name":"read","arguments":"{bad"}
		]
	}`)
	defer server.Close()
	client := newTestClient(t, "key", server.URL, Options{})
	response, err := generate(client, context.Background(), baseRequest(), nil, nil, nil)
	if err != nil {
		t.Fatalf("Generate() error = %v", err)
	}
	if response.Text != "Cannot comply." || len(response.ToolCalls) != 1 || string(response.ToolCalls[0].Arguments) != "{bad" {
		t.Fatalf("response = %+v", response)
	}
}

func TestClientRejectsMalformedResponses(t *testing.T) {
	tests := []struct {
		name string
		body string
	}{
		{name: "malformed JSON", body: `{`},
		{name: "trailing JSON", body: `{"status":"completed","output":[]} {}`},
		{name: "incomplete", body: `{"status":"incomplete","incomplete_details":{"reason":"max_output_tokens"},"output":[]}`},
		{name: "response error", body: `{"status":"failed","error":{"type":"server_error","code":"bad","message":"failed"},"output":[]}`},
		{name: "missing call ID", body: `{"status":"completed","output":[{"type":"function_call","name":"read","arguments":"{}"}]}`},
		{name: "missing call name", body: `{"status":"completed","output":[{"type":"function_call","call_id":"call_1","arguments":"{}"}]}`},
		{name: "duplicate call ID", body: `{"status":"completed","output":[{"type":"function_call","call_id":"call_1","name":"read","arguments":"{}"},{"type":"function_call","call_id":"call_1","name":"bash","arguments":"{}"}]}`},
		{name: "negative usage", body: `{"status":"completed","output":[],"usage":{"input_tokens":-1,"output_tokens":0,"total_tokens":0}}`},
		{name: "non-object output", body: `{"status":"completed","output":[3]}`},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			server := responseServer(t, http.StatusOK, test.body)
			defer server.Close()
			client := newTestClient(t, "key", server.URL, Options{})
			_, err := generate(client, context.Background(), baseRequest(), func(string) error {
				t.Fatal("text sink called for malformed response")
				return nil
			}, nil, nil)
			if err == nil {
				t.Fatal("Generate() succeeded")
			}
		})
	}
}
