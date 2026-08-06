package openai

import (
	"testing"

	"yaah/agent"
)

func TestClientShouldCompact(t *testing.T) {
	client := &Client{}
	solLimit := modelContextWindows["gpt-5.6-sol"] * 9 / 10
	terraLimit := modelContextWindows["gpt-5.6-terra"] * 9 / 10
	lunaLimit := modelContextWindows["gpt-5.6-luna"] * 9 / 10
	tests := []struct {
		name    string
		request agent.Request
		usage   agent.Usage
		want    bool
	}{
		{name: "no state", request: agent.Request{Model: "gpt-5.6-sol"}, usage: agent.Usage{TotalTokens: solLimit}, want: false},
		{name: "no usage", request: agent.Request{Model: "gpt-5.6-sol", State: []byte("state")}, want: false},
		{name: "unknown model", request: agent.Request{Model: "unknown", State: []byte("state")}, usage: agent.Usage{TotalTokens: solLimit}, want: false},
		{name: "below limit", request: agent.Request{Model: "gpt-5.6-sol", State: []byte("state")}, usage: agent.Usage{TotalTokens: solLimit - 1}, want: false},
		{name: "sol at limit", request: agent.Request{Model: "gpt-5.6-sol", State: []byte("state")}, usage: agent.Usage{TotalTokens: solLimit}, want: true},
		{name: "terra at limit", request: agent.Request{Model: "gpt-5.6-terra", State: []byte("state")}, usage: agent.Usage{TotalTokens: terraLimit}, want: true},
		{name: "pending input crosses luna limit", request: agent.Request{Model: "gpt-5.6-luna", State: []byte("state"), Inputs: []agent.Input{{Text: "12345678"}}}, usage: agent.Usage{TotalTokens: lunaLimit - 2}, want: true},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := client.ShouldCompact(test.request, test.usage); got != test.want {
				t.Fatalf("ShouldCompact() = %v, want %v", got, test.want)
			}
		})
	}
}
