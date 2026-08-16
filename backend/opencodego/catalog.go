package opencodego

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"time"

	backendhttp "github.com/eul-ai/eul/backend/httpclient"
)

const (
	defaultCatalogURL       = "https://models.opencode.ai/api.json"
	catalogCacheVersion     = 1
	catalogCacheFreshness   = 5 * time.Minute
	maxCatalogResponseBytes = int64(8 * 1024 * 1024)
	maxCatalogCacheBytes    = int64(2 * 1024 * 1024)
)

type catalogLoader struct {
	url       string
	cachePath string
	client    *http.Client
	now       func() time.Time
}

type catalogCache struct {
	Version   int             `json:"version"`
	ETag      string          `json:"etag,omitempty"`
	FetchedAt time.Time       `json:"fetched_at"`
	Provider  catalogProvider `json:"provider"`
}

func (loader *catalogLoader) Load(ctx context.Context) (catalogProvider, error) {
	cached, hasCache := readCatalogCache(loader.cachePath)
	now := loader.currentTime()
	if hasCache && catalogCacheFresh(cached, now) {
		return cached.Provider, nil
	}

	refreshed, err := loader.refresh(ctx, cached, hasCache)
	if err == nil {
		return refreshed.Provider, nil
	}
	if contextErr := ctx.Err(); contextErr != nil {
		return catalogProvider{}, contextErr
	}
	if hasCache {
		return cached.Provider, nil
	}
	return catalogProvider{}, err
}

func (loader *catalogLoader) refresh(ctx context.Context, cached catalogCache, hasCache bool) (catalogCache, error) {
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, loader.url, nil)
	if err != nil {
		return catalogCache{}, err
	}
	request.Header.Set("Accept", "application/json")
	request.Header.Set("User-Agent", "eul")
	if hasCache && cached.ETag != "" {
		request.Header.Set("If-None-Match", cached.ETag)
	}

	response, err := loader.client.Do(request)
	if err != nil {
		return catalogCache{}, err
	}
	defer response.Body.Close()

	if response.StatusCode == http.StatusNotModified {
		if !hasCache {
			return catalogCache{}, errors.New("model catalog returned HTTP 304 without a cache")
		}
		cached.FetchedAt = loader.currentTime()
		_ = writeCatalogCache(loader.cachePath, cached)
		return cached, nil
	}
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return catalogCache{}, responseError(response)
	}

	var providers map[string]json.RawMessage
	truncated, err := backendhttp.DecodeBoundedJSON(response.Body, maxCatalogResponseBytes, &providers)
	if err != nil {
		return catalogCache{}, err
	}
	if truncated {
		return catalogCache{}, fmt.Errorf("response exceeds %d bytes", maxCatalogResponseBytes)
	}

	rawProvider, ok := providers[ID]
	if !ok {
		return catalogCache{}, fmt.Errorf("model catalog has no %q provider", ID)
	}
	var provider catalogProvider
	if err := json.Unmarshal(rawProvider, &provider); err != nil {
		return catalogCache{}, err
	}
	if !validCatalogProvider(provider) {
		return catalogCache{}, fmt.Errorf("model catalog provider %q is invalid", ID)
	}

	refreshed := catalogCache{
		Version:   catalogCacheVersion,
		ETag:      response.Header.Get("ETag"),
		FetchedAt: loader.currentTime(),
		Provider:  provider,
	}
	_ = writeCatalogCache(loader.cachePath, refreshed)
	return refreshed, nil
}

func readCatalogCache(path string) (catalogCache, bool) {
	if path == "" {
		return catalogCache{}, false
	}
	file, err := os.Open(path)
	if err != nil {
		return catalogCache{}, false
	}
	defer file.Close()

	var cached catalogCache
	truncated, err := backendhttp.DecodeBoundedJSON(file, maxCatalogCacheBytes, &cached)
	if err != nil || truncated {
		return catalogCache{}, false
	}
	if cached.Version != catalogCacheVersion || cached.FetchedAt.IsZero() || !validCatalogProvider(cached.Provider) {
		return catalogCache{}, false
	}
	return cached, true
}

func writeCatalogCache(path string, cached catalogCache) error {
	if path == "" {
		return nil
	}
	body, oversized, err := backendhttp.MarshalBoundedJSON(cached, maxCatalogCacheBytes)
	if err != nil {
		return err
	}
	if oversized {
		return fmt.Errorf("model catalog cache exceeds %d bytes", maxCatalogCacheBytes)
	}

	directory := filepath.Dir(path)
	if err := os.MkdirAll(directory, 0o700); err != nil {
		return err
	}
	file, err := os.CreateTemp(directory, ".opencode-go-models-*")
	if err != nil {
		return err
	}
	temporaryPath := file.Name()
	defer os.Remove(temporaryPath)

	if _, err := file.Write(body); err != nil {
		file.Close()
		return err
	}
	if err := file.Close(); err != nil {
		return err
	}
	if err := os.Rename(temporaryPath, path); err != nil {
		return err
	}
	return nil
}

func catalogCacheFresh(cached catalogCache, now time.Time) bool {
	age := now.Sub(cached.FetchedAt)
	return age >= 0 && age < catalogCacheFreshness
}

func validCatalogProvider(provider catalogProvider) bool {
	return provider.ID == ID && len(provider.Models) > 0
}

func (loader *catalogLoader) currentTime() time.Time {
	if loader.now != nil {
		return loader.now()
	}
	return time.Now()
}
