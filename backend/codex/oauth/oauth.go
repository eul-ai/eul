package oauth

import (
	"context"
	"net"
	"net/http"
	"strings"
	"time"

	"github.com/eul-ai/eul/backend"
	backendhttp "github.com/eul-ai/eul/backend/httpclient"
)

const (
	clientID             = "app_EMoamEEZ73f0CkXaXp7hrann"
	defaultAuthBaseURL   = "https://auth.openai.com"
	browserCallbackPath  = "/auth/callback"
	deviceRedirectPath   = "/deviceauth/callback"
	credentialVersion    = 1
	maxAuthResponseBytes = int64(64 * 1024)
	maxCredentialBytes   = int64(64 * 1024)
	refreshSkew          = 5 * time.Minute
	authorizationTimeout = 15 * time.Minute
	defaultHTTPTimeout   = 30 * time.Second
)

type AccessCredential struct {
	AccessToken string
	AccountID   string
}

type credentials struct {
	Version      int    `json:"version"`
	Type         string `json:"type"`
	AccessToken  string `json:"access_token"`
	RefreshToken string `json:"refresh_token"`
	ExpiresAt    int64  `json:"expires_at"`
	AccountID    string `json:"account_id"`
}

type Options struct {
	HTTPClient      *http.Client
	AuthBaseURL     string
	CallbackAddress string
	Now             func() time.Time
	Sleep           func(context.Context, time.Duration) error
}

type Manager struct {
	store           *credentialStore
	httpClient      *http.Client
	authBaseURL     string
	callbackAddress string
	now             func() time.Time
	sleep           func(context.Context, time.Duration) error
	listen          func(string, string) (net.Listener, error)
}

func NewManager(path string, options Options) *Manager {
	base := options.AuthBaseURL
	if base == "" {
		base = defaultAuthBaseURL
	}

	client := backendhttp.CloneNoRedirects(options.HTTPClient, defaultHTTPTimeout)

	now := options.Now
	if now == nil {
		now = time.Now
	}

	sleep := options.Sleep
	if sleep == nil {
		sleep = sleepContext
	}

	callback := options.CallbackAddress
	if callback == "" {
		callback = "127.0.0.1:1455"
	}

	return &Manager{
		store:           &credentialStore{path: path, sleep: sleep},
		httpClient:      client,
		authBaseURL:     strings.TrimRight(base, "/"),
		callbackAddress: callback,
		now:             now,
		sleep:           sleep,
		listen:          net.Listen,
	}
}

func (m *Manager) LoginBrowser(ctx context.Context, presentURL func(string) error) error {
	if err := ctx.Err(); err != nil {
		return err
	}

	credential, err := m.loginBrowser(ctx, presentURL)
	if err != nil {
		return err
	}
	return m.store.withLock(ctx, func() error { return m.store.write(credential) })
}

func (m *Manager) LoginDevice(ctx context.Context, presentCode func(backend.DeviceCode) error) error {
	if err := ctx.Err(); err != nil {
		return err
	}

	credential, err := m.loginDevice(ctx, presentCode)
	if err != nil {
		return err
	}
	return m.store.withLock(ctx, func() error { return m.store.write(credential) })
}

func (m *Manager) Resolve(ctx context.Context) (AccessCredential, error) {
	var result credentials
	err := m.store.withLock(ctx, func() error {
		credential, err := m.store.read()
		if err != nil {
			return err
		}

		if m.now().Add(refreshSkew).UnixMilli() < credential.ExpiresAt {
			result = credential
			return nil
		}

		refreshed, err := m.refresh(ctx, credential)
		if err != nil {
			return err
		}

		if err := m.store.write(refreshed); err != nil {
			return err
		}

		result = refreshed
		return nil
	})
	if err != nil {
		return AccessCredential{}, err
	}
	return AccessCredential{AccessToken: result.AccessToken, AccountID: result.AccountID}, nil
}

func (m *Manager) Logout(ctx context.Context) error {
	return m.store.withLock(ctx, m.store.remove)
}

func sleepContext(ctx context.Context, duration time.Duration) error {
	timer := time.NewTimer(duration)
	defer timer.Stop()

	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}

var (
	_ backend.BrowserAuthenticator = (*Manager)(nil)
	_ backend.DeviceAuthenticator  = (*Manager)(nil)
	_ backend.Authenticator        = (*Manager)(nil)
)
