package codex

import (
	"context"
	"fmt"
	"net/http"

	"github.com/eul-ai/eul/agent"
	"github.com/eul-ai/eul/backend/codex/oauth"
	"github.com/eul-ai/eul/backend/protocol"
	"github.com/eul-ai/eul/backend/protocol/responses"
)

const baseURL = "https://chatgpt.com/backend-api"

type provider struct {
	*protocol.Client
	codec responses.Codec
}

func newProvider(manager *oauth.Manager, httpClient *http.Client) provider {
	codec := responses.Codec{Options: requestOptions, InboxAsAgentMessage: true}
	return provider{
		Client: protocol.New(protocol.Options{
			Codec:       codec,
			HTTPClient:  httpClient,
			Endpoint:    baseURL + "/codex/responses",
			ErrorPrefix: "openai",
			PrepareRequest: func(ctx context.Context, request *http.Request) error {
				return prepareRequest(ctx, manager, request)
			},
			ContextWindow: func(model string) int64 { return models[model].contextWindow },
		}),
		codec: codec,
	}
}

func prepareRequest(ctx context.Context, manager *oauth.Manager, request *http.Request) error {
	credential, err := resolveCredential(ctx, manager)
	if err != nil {
		return err
	}
	setCredentialHeaders(request, credential)
	request.Header.Set("x-codex-beta-features", "remote_compaction_v2")
	request.Header.Set("OpenAI-Beta", "responses=experimental")
	return nil
}

func requestOptions(request agent.Request) (responses.RequestOptions, error) {
	reasoning, err := reasoningFor(request.Model, request.ThinkingLevel)
	if err != nil {
		return responses.RequestOptions{}, err
	}

	serviceTier := ""
	if request.FastMode {
		serviceTier = "priority"
	}
	return responses.RequestOptions{
		Reasoning:         reasoning,
		ServiceTier:       serviceTier,
		TextVerbosity:     "low",
		Include:           []string{"reasoning.encrypted_content"},
		ToolChoice:        "auto",
		ParallelToolCalls: true,
	}, nil
}

func resolveCredential(ctx context.Context, manager *oauth.Manager) (oauth.AccessCredential, error) {
	credential, err := manager.Resolve(ctx)
	if err != nil {
		return oauth.AccessCredential{}, fmt.Errorf("openai: resolve authentication: %w", err)
	}
	return credential, nil
}

func setCredentialHeaders(request *http.Request, credential oauth.AccessCredential) {
	request.Header.Set("Authorization", "Bearer "+credential.AccessToken)
	request.Header.Set("chatgpt-account-id", credential.AccountID)
	request.Header.Set("originator", "eul")
	request.Header.Set("User-Agent", "eul")
}
