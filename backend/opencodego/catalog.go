package opencodego

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	backendhttp "github.com/eul-ai/eul/backend/httpclient"
)

const (
	defaultCatalogURL            = "https://models.opencode.ai/api.json"
	catalogCacheVersion          = 1
	catalogCacheFreshness        = 5 * time.Minute
	maxLiveModelsResponseBytes   = int64(1024 * 1024)
	maxCatalogResponseBytes      = int64(8 * 1024 * 1024)
	maxCatalogCacheBytes         = int64(2 * 1024 * 1024)
	maxCatalogErrorResponseBytes = int64(64 * 1024)
)

type catalogCache struct {
	Version   int             `json:"version"`
	ETag      string          `json:"etag,omitempty"`
	FetchedAt time.Time       `json:"fetched_at"`
	Provider  catalogProvider `json:"provider"`
}

type liveModelsResponse struct {
	Data []struct {
		ID string `json:"id"`
	} `json:"data"`
}

func (configured *runtime) loadLiveModels(ctx context.Context) (map[string]struct{}, error) {
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, configured.baseURL+"/models", nil)
	if err != nil {
		return nil, err
	}
	request.Header.Set("Accept", "application/json")
	if err := prepareBearerRequest(configured.apiKey)(ctx, request); err != nil {
		return nil, err
	}

	response, err := configured.credentialClient.Do(request)
	if err != nil {
		if contextErr := ctx.Err(); contextErr != nil {
			return nil, contextErr
		}
		return nil, err
	}
	defer response.Body.Close()
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return nil, configured.responseError(response)
	}

	body, truncated, err := backendhttp.ReadBounded(response.Body, maxLiveModelsResponseBytes)
	if err != nil {
		return nil, err
	}
	if truncated {
		return nil, fmt.Errorf("response exceeds %d bytes", maxLiveModelsResponseBytes)
	}

	var result liveModelsResponse
	if err := json.Unmarshal(body, &result); err != nil {
		return nil, err
	}
	models := make(map[string]struct{}, len(result.Data))
	for _, model := range result.Data {
		if model.ID != "" {
			models[model.ID] = struct{}{}
		}
	}
	if len(models) == 0 {
		return nil, errors.New("response contains no models")
	}
	return models, nil
}

func (configured *runtime) loadCatalog(ctx context.Context) (catalogProvider, error) {
	cached, hasCache := readCatalogCache(configured.catalogCachePath)
	now := configured.currentTime()
	if hasCache && catalogCacheFresh(cached, now) {
		return cached.Provider, nil
	}

	refreshed, err := configured.refreshCatalog(ctx, cached, hasCache)
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

func (configured *runtime) refreshCatalog(ctx context.Context, cached catalogCache, hasCache bool) (catalogCache, error) {
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, configured.catalogURL, nil)
	if err != nil {
		return catalogCache{}, err
	}
	request.Header.Set("Accept", "application/json")
	request.Header.Set("User-Agent", "eul")
	if hasCache && cached.ETag != "" {
		request.Header.Set("If-None-Match", cached.ETag)
	}

	response, err := configured.credentialClient.Do(request)
	if err != nil {
		return catalogCache{}, err
	}
	defer response.Body.Close()

	if response.StatusCode == http.StatusNotModified {
		if !hasCache {
			return catalogCache{}, errors.New("model catalog returned HTTP 304 without a cache")
		}
		cached.FetchedAt = configured.currentTime()
		_ = writeCatalogCache(configured.catalogCachePath, cached)
		return cached, nil
	}
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return catalogCache{}, configured.responseError(response)
	}

	body, truncated, err := backendhttp.ReadBounded(response.Body, maxCatalogResponseBytes)
	if err != nil {
		return catalogCache{}, err
	}
	if truncated {
		return catalogCache{}, fmt.Errorf("response exceeds %d bytes", maxCatalogResponseBytes)
	}

	var providers map[string]json.RawMessage
	if err := json.Unmarshal(body, &providers); err != nil {
		return catalogCache{}, err
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
		FetchedAt: configured.currentTime(),
		Provider:  provider,
	}
	_ = writeCatalogCache(configured.catalogCachePath, refreshed)
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

	body, truncated, err := backendhttp.ReadBounded(file, maxCatalogCacheBytes)
	if err != nil || truncated {
		return catalogCache{}, false
	}
	var cached catalogCache
	if err := json.Unmarshal(body, &cached); err != nil {
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
	body, err := json.Marshal(cached)
	if err != nil {
		return err
	}
	if int64(len(body)) > maxCatalogCacheBytes {
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

func (configured *runtime) currentTime() time.Time {
	if configured.now != nil {
		return configured.now()
	}
	return time.Now()
}

func (configured *runtime) responseError(response *http.Response) error {
	body, _, _ := backendhttp.ReadBounded(response.Body, maxCatalogErrorResponseBytes)
	detail := strings.TrimSpace(string(body))
	if detail == "" {
		detail = "empty response"
	}
	return fmt.Errorf("HTTP %s: %s", response.Status, detail)
}
