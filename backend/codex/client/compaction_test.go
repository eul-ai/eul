package client

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"testing"

	"github.com/eul-ai/eul/agent"
)

func TestCodexClientCompactsAndReplaysSharedState(t *testing.T) {
	calls := 0
	server := newTestServer(t, http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		calls++
		var wire struct {
			Input []json.RawMessage `json:"input"`
		}
		if err := json.NewDecoder(request.Body).Decode(&wire); err != nil {
			t.Fatal(err)
		}
		if request.Header.Get("x-codex-beta-features") != "remote_compaction_v2" {
			t.Errorf("headers = %v", request.Header)
		}
		writer.Header().Set("Content-Type", "text/event-stream")
		switch calls {
		case 1:
			if len(wire.Input) != 2 {
				t.Errorf("compact input = %s", wire.Input)
			}
			fmt.Fprint(writer, "data: {\"type\":\"response.output_item.done\",\"output_index\":0,\"item\":{\"type\":\"compaction\",\"encrypted_content\":\"opaque\"}}\n\n")
			fmt.Fprint(writer, "data: {\"type\":\"response.completed\",\"response\":{\"status\":\"completed\"}}\n\n")
		case 2:
			if len(wire.Input) != 3 {
				t.Errorf("replayed input = %s", wire.Input)
			}
			fmt.Fprint(writer, "data: {\"type\":\"response.output_item.done\",\"output_index\":0,\"item\":{\"type\":\"message\",\"content\":[{\"type\":\"output_text\",\"text\":\"done\"}]}}\n\n")
			fmt.Fprint(writer, "data: {\"type\":\"response.completed\",\"response\":{\"status\":\"completed\"}}\n\n")
		}
	}))
	defer server.Close()

	client := newTestClient(t, "token", server.URL, Options{})
	compacted, err := client.Compact(context.Background(), agent.Request{
		Model: ModelGPT56Sol, Inputs: []agent.Input{agent.NewTextInput("compact me")},
	})
	if err != nil {
		t.Fatal(err)
	}
	response, err := client.Generate(context.Background(), agent.Request{
		Model: ModelGPT56Sol, State: compacted.State, Inputs: []agent.Input{agent.NewTextInput("continue")},
	}, agent.StreamObserver{})
	if err != nil || response.Text != "done" || calls != 2 {
		t.Fatalf("response=%+v error=%v calls=%d", response, err, calls)
	}
}
