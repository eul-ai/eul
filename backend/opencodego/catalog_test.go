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
	cachePath := filepath.Join(t.TempDir(), "cache", "opencode-go-models.json")
	requests := 0
	client := &http.Client{Transport: roundTripperFunc(func(request *http.Request) (*http.Response, error) {
		requests++
		return testHTTPResponse(http.StatusOK, testCatalogJSON(t)), nil
	})}
	configured := &catalogLoader{
		url:       "https://catalog.test/api.json",
		cachePath: cachePath,
		client:    client,
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
	if !ok || cached.Provider.Models["grok-4.5"].Limit.Context != 500_000 {
		t.Fatalf("cache = %+v, present=%v", cached, ok)
	}

	configured.client = &http.Client{Transport: roundTripperFunc(func(*http.Request) (*http.Response, error) {
		t.Fatal("fresh cache made an HTTP request")
		return nil, nil
	})}
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
	}
	if _, err := configured.Load(context.Background()); err == nil {
		t.Fatal("catalog refresh failure was accepted without a cache")
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
	}
	if _, err := configured.Load(context.Background()); err == nil {
		t.Fatal("corrupt cache was used")
	}
}

func TestCatalogRefreshesStaleCache(t *testing.T) {
	cachePath := filepath.Join(t.TempDir(), "cache", "opencode-go-models.json")
	if err := writeCatalogCache(cachePath, catalogCache{FetchedAt: time.Now().Add(-catalogCacheFreshness), Provider: testCatalogProvider(t)}); err != nil {
		t.Fatal(err)
	}

	requests := 0
	configured := &catalogLoader{
		url:       "https://catalog.test/api.json",
		cachePath: cachePath,
		client: &http.Client{Transport: roundTripperFunc(func(*http.Request) (*http.Response, error) {
			requests++
			return testHTTPResponse(http.StatusOK, testCatalogJSON(t)), nil
		})},
	}
	if _, err := configured.Load(context.Background()); err != nil || requests != 1 {
		t.Fatalf("requests=%d error=%v", requests, err)
	}
}
