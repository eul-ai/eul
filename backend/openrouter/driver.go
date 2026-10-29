package openrouter

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/eul-ai/eul/backend"
	backendhttp "github.com/eul-ai/eul/backend/httpclient"
)

const (
	ID   = "openrouter"
	Name = "OpenRouter"

	defaultBaseURL        = "https://openrouter.ai/api/v1"
	credentialHTTPTimeout = 30 * time.Second
)

type driver struct {
	baseURL    string
	httpClient *http.Client
}

func New() backend.Backend {
	return backend.Backend{ID: ID, Name: Name, Open: driver{baseURL: defaultBaseURL}.open}
}

func (driver driver) open(ctx context.Context, _ string) (*backend.Runtime, error) {
	apiKey := strings.TrimSpace(os.Getenv("OPENROUTER_API_KEY"))
	if apiKey == "" {
		return nil, errors.New("openrouter: OPENROUTER_API_KEY is required")
	}

	generationClient := backendhttp.CloneNoRedirects(driver.httpClient, 0)
	account := account{
		apiKey:  apiKey,
		baseURL: driver.baseURL,
		client:  backendhttp.CloneNoRedirects(generationClient, credentialHTTPTimeout),
	}
	if _, err := account.loadKey(ctx); err != nil {
		return nil, fmt.Errorf("openrouter: validate API key: %w", err)
	}
	catalog, err := account.loadModelCatalog(ctx)
	if err != nil {
		return nil, fmt.Errorf("openrouter: load models: %w", err)
	}

	models := buildModels(catalog)
	metadata := make(map[string]backend.ModelMetadata, len(models))
	for id, model := range models {
		metadata[id] = model.backendMetadata()
	}
	return &backend.Runtime{
		Provider:    newClient(apiKey, driver.baseURL+"/responses", generationClient, models),
		Usage:       account.usage,
		Catalog:     metadata,
		ReportsCost: true,
	}, nil
}

type account struct {
	apiKey  string
	baseURL string
	client  *http.Client
}

func (account account) usage(ctx context.Context) (backend.AccountUsage, error) {
	response, err := account.loadKey(ctx)
	if err != nil {
		return backend.AccountUsage{}, fmt.Errorf("openrouter: load usage: %w", err)
	}

	return backend.AccountUsage{
		MonthlyUsageUSD:   &response.Data.MonthlyUsage,
		LimitRemainingUSD: response.Data.LimitRemaining,
	}, nil
}
