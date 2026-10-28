package openrouter

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"os"
	"strings"
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
	_ backend.ModelValidator        = (*runtime)(nil)
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
		models:           backend.NewModels[modelMetadata]("openrouter"),
	}, nil
}

type runtime struct {
	apiKey           string
	baseURL          string
	generationClient *http.Client
	credentialClient *http.Client
	models           *backend.Models[modelMetadata]
}

func (configured *runtime) CheckCredentials(ctx context.Context) error {
	if _, err := configured.loadKey(ctx); err != nil {
		return fmt.Errorf("openrouter: validate API key: %w", err)
	}
	return nil
}

func (configured *runtime) InitializeModels(ctx context.Context) error {
	catalog, err := configured.loadModelCatalog(ctx)
	if err != nil {
		return fmt.Errorf("openrouter: load models: %w", err)
	}

	configured.models.Replace(buildModels(catalog))
	return nil
}

func (configured *runtime) Usage(ctx context.Context) (backend.AccountUsage, error) {
	response, err := configured.loadKey(ctx)
	if err != nil {
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
	), nil
}

func (configured *runtime) modelMetadata(model string) modelMetadata {
	metadata, _ := configured.models.Lookup(model)
	return metadata
}

func (configured *runtime) ModelMetadata(model string) backend.ModelMetadata {
	return configured.modelMetadata(model).backendMetadata()
}

func (configured *runtime) ValidateModel(model string) error {
	_, err := configured.models.Lookup(model)
	return err
}

func (*runtime) Close() error {
	return nil
}
