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
	backendhttp "github.com/eul-ai/eul/backend/httpclient"
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
	_ backend.ModelInitializer      = (*runtime)(nil)
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
	generationClient := backendhttp.CloneNoRedirects(driver.httpClient, 0)
	credentialClient := backendhttp.CloneNoRedirects(generationClient, credentialHTTPTimeout)

	return &runtime{
		apiKey:           apiKey,
		baseURL:          baseURL,
		generationClient: generationClient,
		credentialClient: credentialClient,
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
	if err := configured.get(ctx, "/key", nil, true); err != nil {
		return fmt.Errorf("openrouter: validate API key: %w", err)
	}
	return nil
}

func (configured *runtime) InitializeModels(ctx context.Context) error {
	var catalog modelCatalog
	if err := configured.get(ctx, "/models", &catalog, false); err != nil {
		return fmt.Errorf("openrouter: load models: %w", err)
	}

	configured.mu.Lock()
	configured.models = buildModels(catalog)
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
	if err := configured.get(ctx, "/key", &response, true); err != nil {
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
		configured.modelMetadata,
	)
}

func (configured *runtime) modelMetadata(model string) modelMetadata {
	configured.mu.RLock()
	defer configured.mu.RUnlock()
	return configured.models[model]
}

func (configured *runtime) ModelMetadata(model string) backend.ModelMetadata {
	return configured.modelMetadata(model).backendMetadata()
}

func (*runtime) Close() error {
	return nil
}

func (configured *runtime) get(ctx context.Context, path string, target any, authenticated bool) error {
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, configured.baseURL+path, nil)
	if err != nil {
		return err
	}
	if authenticated {
		request.Header.Set("Authorization", "Bearer "+configured.apiKey)
	}
	request.Header.Set("Accept", "application/json")
	setCommonHeaders(request)

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
		detail := strings.TrimSpace(string(body))
		if detail == "" {
			detail = "empty response"
		}
		return fmt.Errorf("HTTP %s: %s", response.Status, detail)
	}
	body, truncated, err := backendhttp.ReadBounded(response.Body, maxResponseBytes)
	if err != nil {
		return err
	}
	if truncated {
		return fmt.Errorf("response exceeds %d bytes", maxResponseBytes)
	}
	if target != nil {
		if err := json.Unmarshal(body, target); err != nil {
			return err
		}
	}
	return nil
}
