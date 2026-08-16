package opencodego

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/eul-ai/eul/agent"
	"github.com/eul-ai/eul/backend"
	backendhttp "github.com/eul-ai/eul/backend/httpclient"
)

const (
	ID   = "opencode-go"
	Name = "OpenCode Go"

	defaultBaseURL        = "https://opencode.ai/zen/go/v1"
	credentialHTTPTimeout = 30 * time.Second
)

type Driver struct {
	getenv     func(string) string
	baseURL    string
	catalogURL string
	httpClient *http.Client
	now        func() time.Time
}

func New() *Driver {
	return &Driver{
		getenv:     os.Getenv,
		baseURL:    defaultBaseURL,
		catalogURL: defaultCatalogURL,
		now:        time.Now,
	}
}

func (*Driver) Descriptor() backend.Descriptor {
	return backend.Descriptor{ID: ID, Name: Name}
}

func (driver *Driver) Open(options backend.Options) (backend.Runtime, error) {
	getenv := driver.getenv
	if getenv == nil {
		getenv = os.Getenv
	}
	apiKey := strings.TrimSpace(getenv("OPENCODE_API_KEY"))
	if apiKey == "" {
		return nil, errors.New("opencode go: OPENCODE_API_KEY is required")
	}

	baseURL := strings.TrimRight(driver.baseURL, "/")
	if baseURL == "" {
		baseURL = defaultBaseURL
	}
	catalogURL := strings.TrimSpace(driver.catalogURL)
	if catalogURL == "" {
		catalogURL = defaultCatalogURL
	}
	var catalogCachePath string
	if options.Home != "" {
		catalogCachePath = filepath.Join(options.Home, "cache", "opencode-go-models.json")
	}
	credentialClient := backendhttp.CloneNoRedirects(driver.httpClient, credentialHTTPTimeout)
	return &runtime{
		apiKey:           apiKey,
		baseURL:          baseURL,
		generationClient: driver.httpClient,
		credentialClient: credentialClient,
		catalog: &catalogLoader{
			url:       catalogURL,
			cachePath: catalogCachePath,
			client:    credentialClient,
			now:       driver.now,
		},
	}, nil
}

type runtime struct {
	apiKey           string
	baseURL          string
	generationClient *http.Client
	credentialClient *http.Client
	catalog          *catalogLoader
	modelsMu         sync.RWMutex
	models           map[string]modelInfo
}

func (configured *runtime) InitializeModels(ctx context.Context) error {
	live, err := configured.loadLiveModels(ctx)
	if err != nil {
		return fmt.Errorf("opencode go: load available models: %w", err)
	}

	catalog, err := configured.catalog.Load(ctx)
	if err != nil {
		return fmt.Errorf("opencode go: load model catalog: %w", err)
	}
	models := buildModels(catalog, live)
	if len(models) == 0 {
		return errors.New("opencode go: no supported models are available")
	}

	configured.modelsMu.Lock()
	configured.models = models
	configured.modelsMu.Unlock()
	return nil
}

func (configured *runtime) NewProvider() (agent.Provider, error) {
	configured.modelsMu.RLock()
	models := configured.models
	configured.modelsMu.RUnlock()
	if len(models) == 0 {
		return nil, errors.New("opencode go: model catalog is unavailable")
	}
	return newProvider(configured.apiKey, configured.baseURL, configured.generationClient, models)
}

func (configured *runtime) ModelMetadata(model string) backend.ModelMetadata {
	configured.modelsMu.RLock()
	defer configured.modelsMu.RUnlock()
	return metadataFor(configured.models, model)
}

func (configured *runtime) ValidateModel(model string) error {
	configured.modelsMu.RLock()
	_, ok := configured.models[model]
	configured.modelsMu.RUnlock()
	if !ok {
		return fmt.Errorf("opencode go: model %q is not supported", model)
	}
	return nil
}

func (*runtime) Close() error {
	return nil
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
