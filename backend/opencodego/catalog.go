package opencodego

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"time"

	backendhttp "github.com/eul-ai/eul/backend/httpclient"
)

const (
	defaultCatalogURL     = "https://models.opencode.ai/api.json"
	catalogCacheFreshness = 5 * time.Minute
)

type catalogLoader struct {
	url       string
	cachePath string
	client    *http.Client
}

type catalogCache struct {
	FetchedAt time.Time       `json:"fetched_at"`
	Provider  catalogProvider `json:"provider"`
}

func (loader *catalogLoader) Load(ctx context.Context) (catalogProvider, error) {
	if cached, ok := readCatalogCache(loader.cachePath); ok && time.Since(cached.FetchedAt) < catalogCacheFreshness {
		return cached.Provider, nil
	}

	var providers map[string]json.RawMessage
	if err := backendhttp.GetJSON(ctx, loader.client, loader.url, func(request *http.Request) {
		request.Header.Set("User-Agent", "eul")
	}, &providers); err != nil {
		return catalogProvider{}, err
	}

	var provider catalogProvider
	if err := json.Unmarshal(providers[ID], &provider); err != nil || !validCatalogProvider(provider) {
		return catalogProvider{}, fmt.Errorf("model catalog has no valid %q provider", ID)
	}
	_ = writeCatalogCache(loader.cachePath, catalogCache{FetchedAt: time.Now(), Provider: provider})
	return provider, nil
}

func readCatalogCache(path string) (catalogCache, bool) {
	body, err := os.ReadFile(path)
	if err != nil {
		return catalogCache{}, false
	}

	var cached catalogCache
	if err := json.Unmarshal(body, &cached); err != nil || !validCatalogProvider(cached.Provider) {
		return catalogCache{}, false
	}
	return cached, true
}

func writeCatalogCache(path string, cached catalogCache) error {
	body, err := json.Marshal(cached)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	return os.WriteFile(path, body, 0o600)
}

func validCatalogProvider(provider catalogProvider) bool {
	return provider.ID == ID && len(provider.Models) > 0
}
