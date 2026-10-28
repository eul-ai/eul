package opencodego

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/eul-ai/eul/backend"
	backendhttp "github.com/eul-ai/eul/backend/httpclient"
)

const (
	ID   = "opencode-go"
	Name = "OpenCode Go"

	defaultBaseURL        = "https://opencode.ai/zen/go/v1"
	credentialHTTPTimeout = 30 * time.Second
)

type driver struct {
	baseURL    string
	catalogURL string
	httpClient *http.Client
}

func New() backend.Backend {
	return backend.Backend{
		ID:   ID,
		Name: Name,
		Open: driver{baseURL: defaultBaseURL, catalogURL: defaultCatalogURL}.open,
	}
}

func (driver driver) open(ctx context.Context, home string) (*backend.Runtime, error) {
	apiKey := strings.TrimSpace(os.Getenv("OPENCODE_API_KEY"))
	if apiKey == "" {
		return nil, errors.New("opencode go: OPENCODE_API_KEY is required")
	}

	credentialClient := backendhttp.CloneNoRedirects(driver.httpClient, credentialHTTPTimeout)
	account := account{apiKey: apiKey, baseURL: driver.baseURL, client: credentialClient}
	if _, err := account.loadUsage(ctx); err != nil {
		return nil, fmt.Errorf("opencode go: validate API key and subscription: %w", err)
	}
	live, err := account.loadLiveModels(ctx)
	if err != nil {
		return nil, fmt.Errorf("opencode go: load available models: %w", err)
	}
	loader := catalogLoader{
		url:       driver.catalogURL,
		cachePath: filepath.Join(home, "cache", "opencode-go-models.json"),
		client:    credentialClient,
	}
	catalog, err := loader.Load(ctx)
	if err != nil {
		return nil, fmt.Errorf("opencode go: load model catalog: %w", err)
	}
	models := buildModels(catalog, live)
	if len(models) == 0 {
		return nil, errors.New("opencode go: no supported models are available")
	}

	return &backend.Runtime{
		Provider: newProvider(apiKey, driver.baseURL, driver.httpClient, models),
		Usage:    account.usage,
		Catalog:  metadata(models),
	}, nil
}

type account struct {
	apiKey  string
	baseURL string
	client  *http.Client
}
