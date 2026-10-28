package oauth

import (
	"context"
	"net/http"
	"time"

	"github.com/eul-ai/eul/backend"
	backendhttp "github.com/eul-ai/eul/backend/httpclient"
)

const (
	clientID             = "app_EMoamEEZ73f0CkXaXp7hrann"
	authBaseURL          = "https://auth.openai.com"
	callbackAddress      = "127.0.0.1:1455"
	browserCallbackPath  = "/auth/callback"
	deviceRedirectPath   = "/deviceauth/callback"
	maxAuthResponseBytes = int64(64 * 1024)
	refreshSkew          = 5 * time.Minute
	authorizationTimeout = 15 * time.Minute
	httpTimeout          = 30 * time.Second
)

type AccessCredential struct {
	AccessToken string
	AccountID   string
}

type credentials struct {
	AccessToken  string `json:"access_token"`
	RefreshToken string `json:"refresh_token"`
	ExpiresAt    int64  `json:"expires_at"`
	AccountID    string `json:"account_id"`
}

type Manager struct {
	store      *credentialStore
	httpClient *http.Client
}

func NewManager(path string) *Manager {
	return &Manager{
		store:      &credentialStore{path: path},
		httpClient: backendhttp.CloneNoRedirects(nil, httpTimeout),
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
	return m.store.writeLocked(ctx, credential)
}

func (m *Manager) LoginDevice(ctx context.Context, presentCode func(backend.DeviceCode) error) error {
	if err := ctx.Err(); err != nil {
		return err
	}

	credential, err := m.loginDevice(ctx, presentCode)
	if err != nil {
		return err
	}
	return m.store.writeLocked(ctx, credential)
}

func (m *Manager) Resolve(ctx context.Context) (AccessCredential, error) {
	var result credentials
	err := m.store.withLock(ctx, func() error {
		credential, err := m.store.read()
		if err != nil {
			return err
		}

		if time.Now().Add(refreshSkew).UnixMilli() < credential.ExpiresAt {
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
