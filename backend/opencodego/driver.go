package opencodego

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"net/http"
	"os"
	"strings"
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
	maxUsageResponseBytes = int64(1024 * 1024)
)

type Driver struct {
	getenv     func(string) string
	baseURL    string
	httpClient *http.Client
}

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
	apiKey := strings.TrimSpace(getenv("OPENCODE_API_KEY"))
	if apiKey == "" {
		return nil, errors.New("opencode go: OPENCODE_API_KEY is required")
	}

	baseURL := strings.TrimRight(driver.baseURL, "/")
	if baseURL == "" {
		baseURL = defaultBaseURL
	}
	return &runtime{
		apiKey:           apiKey,
		baseURL:          baseURL,
		generationClient: driver.httpClient,
		credentialClient: backendhttp.New(driver.httpClient, credentialHTTPTimeout),
	}, nil
}

type runtime struct {
	apiKey           string
	baseURL          string
	generationClient *http.Client
	credentialClient *http.Client
}

func (configured *runtime) CheckCredentials(ctx context.Context) error {
	if _, err := configured.loadUsage(ctx); err != nil {
		return fmt.Errorf("opencode go: validate API key and subscription: %w", err)
	}
	return nil
}

func (configured *runtime) Usage(ctx context.Context) (backend.AccountUsage, error) {
	usage, err := configured.loadUsage(ctx)
	if err != nil {
		return backend.AccountUsage{}, fmt.Errorf("opencode go: load usage: %w", err)
	}

	windows := make([]backend.UsageWindow, 0, 3)
	for _, item := range []struct {
		duration time.Duration
		window   usageWindow
	}{
		{duration: 5 * time.Hour, window: usage.Usage.Rolling},
		{duration: 7 * 24 * time.Hour, window: usage.Usage.Weekly},
		{duration: 30 * 24 * time.Hour, window: usage.Usage.Monthly},
	} {
		resetsAt, err := time.Parse(time.RFC3339, item.window.ResetsAt)
		if err != nil {
			return backend.AccountUsage{}, fmt.Errorf("invalid usage reset time %q: %w", item.window.ResetsAt, err)
		}
		percent := int(math.Round(item.window.Percent))
		percent = max(0, min(percent, 100))
		windows = append(windows, backend.UsageWindow{
			Duration:    item.duration,
			UsedPercent: percent,
			ResetsAt:    resetsAt,
		})
	}
	return backend.AccountUsage{Windows: windows}, nil
}

func (configured *runtime) NewProvider() (agent.Provider, error) {
	return newProvider(configured.apiKey, configured.baseURL, configured.generationClient)
}

func (*runtime) ModelMetadata(model string) backend.ModelMetadata {
	return metadataFor(model)
}

func (*runtime) Close() error {
	return nil
}

type usageResponse struct {
	Usage struct {
		Rolling usageWindow `json:"rolling"`
		Weekly  usageWindow `json:"weekly"`
		Monthly usageWindow `json:"monthly"`
	} `json:"usage"`
}

type usageWindow struct {
	Status   string  `json:"status"`
	Percent  float64 `json:"percent"`
	ResetsAt string  `json:"resetsAt"`
}

func (configured *runtime) loadUsage(ctx context.Context) (usageResponse, error) {
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, configured.baseURL+"/usage", nil)
	if err != nil {
		return usageResponse{}, err
	}
	request.Header.Set("Authorization", "Bearer "+configured.apiKey)
	request.Header.Set("Accept", "application/json")
	request.Header.Set("User-Agent", "eul")
	request.Header.Set("x-opencode-client", "eul")

	response, err := configured.credentialClient.Do(request)
	if err != nil {
		if contextErr := ctx.Err(); contextErr != nil {
			return usageResponse{}, contextErr
		}
		return usageResponse{}, err
	}
	defer response.Body.Close()
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		body, _ := io.ReadAll(io.LimitReader(response.Body, 64*1024))
		detail := configured.redact(strings.TrimSpace(string(body)))
		if detail == "" {
			detail = "empty response"
		}
		return usageResponse{}, fmt.Errorf("HTTP %s: %s", response.Status, detail)
	}

	limited := &io.LimitedReader{R: response.Body, N: maxUsageResponseBytes + 1}
	var result usageResponse
	if err := json.NewDecoder(limited).Decode(&result); err != nil {
		return usageResponse{}, err
	}
	if limited.N == 0 {
		return usageResponse{}, fmt.Errorf("response exceeds %d bytes", maxUsageResponseBytes)
	}
	return result, nil
}

func (configured *runtime) redact(message string) string {
	return backendhttp.Redact(message, []string{configured.apiKey})
}

var (
	_ backend.Driver                = (*Driver)(nil)
	_ backend.Runtime               = (*runtime)(nil)
	_ backend.CredentialChecker     = (*runtime)(nil)
	_ backend.UsageProvider         = (*runtime)(nil)
	_ backend.ModelMetadataProvider = (*runtime)(nil)
)
