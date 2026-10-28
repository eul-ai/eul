package codex

import (
	"context"
	"fmt"
	"net/http"
	"time"

	"github.com/eul-ai/eul/agent"
	"github.com/eul-ai/eul/backend/codex/oauth"
	"github.com/eul-ai/eul/backend/compaction"
	backendhttp "github.com/eul-ai/eul/backend/httpclient"
	"github.com/eul-ai/eul/backend/openai/responses"
)

const baseURL = "https://chatgpt.com/backend-api"

type client struct {
	manager   *oauth.Manager
	responses *responses.Client
}

var (
	_ agent.Provider              = (*client)(nil)
	_ agent.GenerationRetryPolicy = (*client)(nil)
	_ agent.Compactor             = (*client)(nil)
	_ agent.CompactionErrorPolicy = (*client)(nil)
)

func newClient(manager *oauth.Manager, httpClient *http.Client) *client {
	c := &client{manager: manager}
	c.responses = responses.New(responses.Options{
		HTTPClient:                httpClient,
		Endpoint:                  baseURL + "/codex/responses",
		ErrorPrefix:               "openai",
		PrepareRequest:            c.prepareRequest,
		RequestOptions:            requestOptions,
		EncodeInboxAsAgentMessage: true,
	})
	return c
}

func (c *client) Generate(ctx context.Context, request agent.Request, observer agent.StreamObserver) (agent.Response, error) {
	return c.responses.Generate(ctx, request, observer)
}

func (*client) ShouldCompact(request agent.Request, usage agent.Usage) bool {
	return compaction.ShouldCompact(request, usage, models[request.Model].contextWindow)
}

func (c *client) Compact(ctx context.Context, request agent.Request) (agent.CompactResponse, error) {
	return c.responses.Compact(ctx, request)
}

func (*client) RetryGeneration(err error, failedAttempts int) (time.Duration, bool) {
	return backendhttp.RetryGeneration(err, failedAttempts)
}

func (*client) ShouldCompactAfterError(_ agent.Request, err error) bool {
	return backendhttp.IsContextLimit(err)
}

func (c *client) prepareRequest(ctx context.Context, request *http.Request) error {
	credential, err := resolveCredential(ctx, c.manager)
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
