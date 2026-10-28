package openrouter

import (
	"context"
	"fmt"
	"net/http"

	"github.com/eul-ai/eul/agent"
	"github.com/eul-ai/eul/backend/protocol"
	"github.com/eul-ai/eul/backend/protocol/responses"
)

func newClient(apiKey, endpoint string, httpClient *http.Client, metadata func(string) modelMetadata) *protocol.Client {
	return protocol.New(protocol.Options{
		Codec:       responses.Codec{Options: requestOptions(metadata)},
		HTTPClient:  httpClient,
		Endpoint:    endpoint,
		ErrorPrefix: "openrouter",
		PrepareRequest: func(_ context.Context, request *http.Request) error {
			request.Header.Set("Authorization", "Bearer "+apiKey)
			setCommonHeaders(request)
			return nil
		},
		ContextWindow: func(model string) int64 { return metadata(model).contextWindow },
	})
}

func setCommonHeaders(request *http.Request) {
	request.Header.Set("HTTP-Referer", "https://github.com/eul-ai/eul")
	request.Header.Set("X-Title", "Eul")
	request.Header.Set("User-Agent", "eul")
}

func requestOptions(metadataFor func(string) modelMetadata) func(agent.Request) (responses.RequestOptions, error) {
	return func(request agent.Request) (responses.RequestOptions, error) {
		options := responses.RequestOptions{SessionID: request.SessionID, ToolChoice: "auto", ParallelToolCalls: true}
		efforts := metadataFor(request.Model).efforts
		effort, supported := efforts[request.ThinkingLevel]
		switch {
		case len(efforts) == 0 && request.ThinkingLevel == agent.ThinkingOff:
			return options, nil
		case !supported:
			return responses.RequestOptions{}, fmt.Errorf("thinking level %q is not supported by model %q", request.ThinkingLevel, request.Model)
		}

		options.Reasoning = &responses.Reasoning{Effort: effort}
		options.Include = []string{"reasoning.encrypted_content"}
		return options, nil
	}
}
