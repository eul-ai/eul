package openrouter

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/eul-ai/eul/agent"
	"github.com/eul-ai/eul/backend"
)

const (
	ID   = "openrouter"
	Name = "OpenRouter"

	defaultBaseURL        = "https://openrouter.ai/api/v1"
	credentialHTTPTimeout = 30 * time.Second
	maxResponseBytes      = int64(16 * 1024 * 1024)
)

type Driver struct {
	getenv     func(string) string
	baseURL    string
	httpClient *http.Client
}

var (
	_ backend.Driver                = (*Driver)(nil)
	_ backend.Runtime               = (*runtime)(nil)
	_ backend.CredentialChecker     = (*runtime)(nil)
	_ backend.UsageProvider         = (*runtime)(nil)
	_ backend.ModelMetadataProvider = (*runtime)(nil)
)

func New() *Driver {
	return &Driver{getenv: os.Getenv, baseURL: defaultBaseURL}
}

func (*Driver) Descriptor() backend.Descriptor {
	return backend.Descriptor{ID: ID, Name: Name}
}

func (driver *Driver) Open(backend.Options) (backend.Runtime, error) {
	getenv := driver.getenv
	if getenv == nil {
		getenv = os.Getenv
	}
	apiKey := strings.TrimSpace(getenv("OPENROUTER_API_KEY"))
	if apiKey == "" {
		return nil, errors.New("openrouter: OPENROUTER_API_KEY is required")
	}

	baseURL := strings.TrimRight(driver.baseURL, "/")
	if baseURL == "" {
		baseURL = defaultBaseURL
	}
	generationClient := &http.Client{}
	if driver.httpClient != nil {
		*generationClient = *driver.httpClient
	}
	generationClient.CheckRedirect = func(*http.Request, []*http.Request) error {
		return http.ErrUseLastResponse
	}
	credentialClient := *generationClient
	if credentialClient.Timeout <= 0 {
		credentialClient.Timeout = credentialHTTPTimeout
	}

	return &runtime{
		apiKey:           apiKey,
		baseURL:          baseURL,
		generationClient: generationClient,
		credentialClient: &credentialClient,
		models:           make(map[string]modelMetadata),
	}, nil
}

type runtime struct {
	apiKey           string
	baseURL          string
	generationClient *http.Client
	credentialClient *http.Client

	mu     sync.RWMutex
	models map[string]modelMetadata
}

func (configured *runtime) CheckCredentials(ctx context.Context) error {
	if err := configured.get(ctx, "/key", nil); err != nil {
		return fmt.Errorf("openrouter: validate API key: %w", err)
	}
	var catalog modelCatalog
	if err := configured.get(ctx, "/models", &catalog); err != nil {
		return fmt.Errorf("openrouter: load models: %w", err)
	}
	models := make(map[string]modelMetadata, len(catalog.Data))
	for _, model := range catalog.Data {
		if strings.TrimSpace(model.ID) == "" || model.ContextLength < 0 {
			continue
		}
		models[model.ID] = modelMetadata{
			contextWindow: model.ContextLength,
			reasoning:     contains(model.SupportedParameters, "reasoning"),
		}
	}
	configured.mu.Lock()
	configured.models = models
	configured.mu.Unlock()
	return nil
}

type keyResponse struct {
	Data keyUsage `json:"data"`
}

type keyUsage struct {
	MonthlyUsage   float64  `json:"usage_monthly"`
	LimitRemaining *float64 `json:"limit_remaining"`
}

func (configured *runtime) Usage(ctx context.Context) (backend.AccountUsage, error) {
	var response keyResponse
	if err := configured.get(ctx, "/key", &response); err != nil {
		return backend.AccountUsage{}, fmt.Errorf("openrouter: load usage: %w", err)
	}

	return backend.AccountUsage{
		MonthlyUsageUSD:   &response.Data.MonthlyUsage,
		LimitRemainingUSD: response.Data.LimitRemaining,
	}, nil
}

func (configured *runtime) NewProvider() (agent.Provider, error) {
	return newClient(
		configured.apiKey,
		configured.baseURL+"/responses",
		configured.generationClient,
		configured.supportsReasoning,
		configured.contextWindow,
	)
}

func (configured *runtime) supportsReasoning(model string) bool {
	configured.mu.RLock()
	defer configured.mu.RUnlock()
	return configured.models[model].reasoning
}

func (configured *runtime) contextWindow(model string) int64 {
	configured.mu.RLock()
	defer configured.mu.RUnlock()
	return configured.models[model].contextWindow
}

func (configured *runtime) ModelMetadata(model string) backend.ModelMetadata {
	configured.mu.RLock()
	metadata, ok := configured.models[model]
	configured.mu.RUnlock()
	if !ok {
		return backend.ModelMetadata{ThinkingLevels: []agent.ThinkingLevel{agent.ThinkingOff}}
	}

	levels := []agent.ThinkingLevel{agent.ThinkingOff}
	if metadata.reasoning {
		levels = []agent.ThinkingLevel{
			agent.ThinkingOff,
			agent.ThinkingMinimal,
			agent.ThinkingLow,
			agent.ThinkingMedium,
			agent.ThinkingHigh,
			agent.ThinkingXHigh,
		}
	}
	return backend.ModelMetadata{ContextWindow: metadata.contextWindow, ThinkingLevels: levels}
}

func (*runtime) Close() error {
	return nil
}

func (configured *runtime) get(ctx context.Context, path string, target any) error {
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, configured.baseURL+path, nil)
	if err != nil {
		return err
	}
	if path != "/models" {
		request.Header.Set("Authorization", "Bearer "+configured.apiKey)
	}
	request.Header.Set("Accept", "application/json")
	request.Header.Set("HTTP-Referer", "https://github.com/eul-ai/eul")
	request.Header.Set("X-Title", "Eul")
	request.Header.Set("User-Agent", "eul")

	response, err := configured.credentialClient.Do(request)
	if err != nil {
		if contextErr := ctx.Err(); contextErr != nil {
			return contextErr
		}
		return err
	}
	defer response.Body.Close()
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		body, _ := io.ReadAll(io.LimitReader(response.Body, 64*1024))
		detail := configured.redact(strings.TrimSpace(string(body)))
		if detail == "" {
			detail = "empty response"
		}
		return fmt.Errorf("HTTP %s: %s", response.Status, detail)
	}
	limited := &io.LimitedReader{R: response.Body, N: maxResponseBytes + 1}
	if target == nil {
		if _, err := io.Copy(io.Discard, limited); err != nil {
			return err
		}
	} else if err := json.NewDecoder(limited).Decode(target); err != nil {
		return err
	}
	if limited.N == 0 {
		return fmt.Errorf("response exceeds %d bytes", maxResponseBytes)
	}
	return nil
}

func (configured *runtime) redact(message string) string {
	return strings.ReplaceAll(message, configured.apiKey, "[redacted]")
}

func contains(values []string, target string) bool {
	for _, value := range values {
		if value == target {
			return true
		}
	}
	return false
}
