package opencodego

import (
	"context"
	"errors"
	"net/http"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestCatalogCachesSelectedProviderAndReusesFreshCache(t *testing.T) {
	now := time.Date(2026, 8, 16, 12, 0, 0, 0, time.UTC)
	cachePath := filepath.Join(t.TempDir(), "cache", "opencode-go-models.json")
	requests := 0
	client := &http.Client{Transport: roundTripperFunc(func(request *http.Request) (*http.Response, error) {
		requests++
		response := testHTTPResponse(http.StatusOK, testCatalogJSON(t))
		response.Header.Set("ETag", `"catalog-v1"`)
		return response, nil
	})}
	configured := &catalogLoader{
		url:       "https://catalog.test/api.json",
		cachePath: cachePath,
		client:    client,
		now:       func() time.Time { return now },
	}
	provider, err := configured.Load(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if provider.ID != ID || requests != 1 {
		t.Fatalf("provider=%+v requests=%d", provider, requests)
	}

	cacheInfo, err := os.Stat(cachePath)
	if err != nil {
		t.Fatal(err)
	}
	directoryInfo, err := os.Stat(filepath.Dir(cachePath))
	if err != nil {
		t.Fatal(err)
	}
	if cacheInfo.Mode().Perm() != 0o600 || directoryInfo.Mode().Perm() != 0o700 {
		t.Fatalf("cache permissions file=%o directory=%o", cacheInfo.Mode().Perm(), directoryInfo.Mode().Perm())
	}
	cached, ok := readCatalogCache(cachePath)
	if !ok || cached.ETag != `"catalog-v1"` || cached.Provider.Models["grok-4.5"].Limit.Context != 500_000 {
		t.Fatalf("cache = %+v, present=%v", cached, ok)
	}

	configured.client = &http.Client{Transport: roundTripperFunc(func(*http.Request) (*http.Response, error) {
		t.Fatal("fresh cache made an HTTP request")
		return nil, nil
	})}
	configured.now = func() time.Time { return now.Add(catalogCacheFreshness - time.Second) }
	provider, err = configured.Load(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if provider.ID != ID {
		t.Fatalf("provider = %+v", provider)
	}
}

func TestCatalogDiscoverySucceedsWhenCacheCannotBeWritten(t *testing.T) {
	blockedDirectory := filepath.Join(t.TempDir(), "blocked")
	if err := os.WriteFile(blockedDirectory, []byte("not a directory"), 0o600); err != nil {
		t.Fatal(err)
	}
	configured := &catalogLoader{
		url:       "https://catalog.test/api.json",
		cachePath: filepath.Join(blockedDirectory, "opencode-go-models.json"),
		client: &http.Client{Transport: roundTripperFunc(func(*http.Request) (*http.Response, error) {
			return testHTTPResponse(http.StatusOK, testCatalogJSON(t)), nil
		})},
		now: time.Now,
	}
	provider, err := configured.Load(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if provider.ID != ID {
		t.Fatalf("provider = %+v", provider)
	}
}

func TestCatalogRevalidatesStaleCacheWithETag(t *testing.T) {
	now := time.Date(2026, 8, 16, 12, 0, 0, 0, time.UTC)
	cachePath := filepath.Join(t.TempDir(), "cache", "opencode-go-models.json")
	if err := writeCatalogCache(cachePath, catalogCache{
		Version:   catalogCacheVersion,
		ETag:      `"catalog-v1"`,
		FetchedAt: now.Add(-catalogCacheFreshness),
		Provider:  testCatalogProvider(t),
	}); err != nil {
		t.Fatal(err)
	}

	requests := 0
	configured := &catalogLoader{
		url:       "https://catalog.test/api.json",
		cachePath: cachePath,
		client: &http.Client{Transport: roundTripperFunc(func(request *http.Request) (*http.Response, error) {
			requests++
			if request.Header.Get("If-None-Match") != `"catalog-v1"` {
				t.Fatalf("If-None-Match = %q", request.Header.Get("If-None-Match"))
			}
			return testHTTPResponse(http.StatusNotModified, ""), nil
		})},
		now: func() time.Time { return now },
	}
	provider, err := configured.Load(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	cached, ok := readCatalogCache(cachePath)
	if provider.ID != ID || requests != 1 || !ok || !cached.FetchedAt.Equal(now) || cached.ETag != `"catalog-v1"` {
		t.Fatalf("provider=%+v requests=%d cache=%+v present=%v", provider, requests, cached, ok)
	}
}

func TestCatalogFallsBackToValidStaleCache(t *testing.T) {
	now := time.Date(2026, 8, 16, 12, 0, 0, 0, time.UTC)
	cachePath := filepath.Join(t.TempDir(), "cache", "opencode-go-models.json")
	if err := writeCatalogCache(cachePath, catalogCache{
		Version:   catalogCacheVersion,
		FetchedAt: now.Add(-time.Hour),
		Provider:  testCatalogProvider(t),
	}); err != nil {
		t.Fatal(err)
	}

	configured := &catalogLoader{
		url:       "https://catalog.test/api.json",
		cachePath: cachePath,
		client: &http.Client{Transport: roundTripperFunc(func(*http.Request) (*http.Response, error) {
			return testHTTPResponse(http.StatusServiceUnavailable, "unavailable"), nil
		})},
		now: func() time.Time { return now },
	}
	provider, err := configured.Load(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if provider.ID != ID {
		t.Fatalf("provider = %+v", provider)
	}
}

func TestCatalogRefreshFailureWithoutCacheFails(t *testing.T) {
	configured := &catalogLoader{
		url:       "https://catalog.test/api.json",
		cachePath: filepath.Join(t.TempDir(), "cache", "opencode-go-models.json"),
		client: &http.Client{Transport: roundTripperFunc(func(*http.Request) (*http.Response, error) {
			return testHTTPResponse(http.StatusServiceUnavailable, "unavailable"), nil
		})},
		now: time.Now,
	}
	if _, err := configured.Load(context.Background()); err == nil {
		t.Fatal("catalog refresh failure was accepted without a cache")
	}
}

func TestCatalogNotModifiedWithoutCacheFails(t *testing.T) {
	configured := &catalogLoader{
		url:       "https://catalog.test/api.json",
		cachePath: filepath.Join(t.TempDir(), "cache", "opencode-go-models.json"),
		client: &http.Client{Transport: roundTripperFunc(func(*http.Request) (*http.Response, error) {
			return testHTTPResponse(http.StatusNotModified, ""), nil
		})},
		now: time.Now,
	}
	if _, err := configured.Load(context.Background()); err == nil {
		t.Fatal("HTTP 304 was accepted without a cache")
	}
}

func TestCatalogDoesNotUseCorruptCacheAsFallback(t *testing.T) {
	cachePath := filepath.Join(t.TempDir(), "cache", "opencode-go-models.json")
	if err := os.MkdirAll(filepath.Dir(cachePath), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(cachePath, []byte(`{"version":1} trailing`), 0o600); err != nil {
		t.Fatal(err)
	}
	configured := &catalogLoader{
		url:       "https://catalog.test/api.json",
		cachePath: cachePath,
		client: &http.Client{Transport: roundTripperFunc(func(*http.Request) (*http.Response, error) {
			return nil, errors.New("offline")
		})},
		now: time.Now,
	}
	if _, err := configured.Load(context.Background()); err == nil {
		t.Fatal("corrupt cache was used")
	}
}

func TestProviderKeepsRuntimeModelSnapshot(t *testing.T) {
	configured := &runtime{
		apiKey:           "secret",
		baseURL:          "https://example.test/zen/go/v1",
		generationClient: &http.Client{},
		models: map[string]modelInfo{
			"grok-4.5": testModelInfos(t)["grok-4.5"],
		},
	}
	created, err := configured.NewProvider()
	if err != nil {
		t.Fatal(err)
	}
	provider := created.(*provider)

	configured.modelsMu.Lock()
	configured.models = map[string]modelInfo{
		"qwen3.8-max": testModelInfos(t)["qwen3.8-max"],
	}
	configured.modelsMu.Unlock()

	if _, ok := provider.models["grok-4.5"]; !ok {
		t.Fatal("provider lost its original model")
	}
	if _, ok := provider.models["qwen3.8-max"]; ok {
		t.Fatal("provider observed a later runtime model refresh")
	}
}
