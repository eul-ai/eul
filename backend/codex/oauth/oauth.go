package oauth

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
	deviceTimeout        = 15 * time.Minute
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
	path            string
	httpClient      *http.Client
	authBaseURL     string
	callbackAddress string
	now             func() time.Time
	sleep           func(context.Context, time.Duration) error
}

func DefaultCredentialPath(eulHome string) (string, error) {
	if eulHome != "" && !filepath.IsAbs(eulHome) {
		return "", errors.New("oauth: EUL_HOME must be an absolute path")
	}
	if eulHome != "" {
		return filepath.Join(filepath.Clean(eulHome), "auth.json"), nil
	}

	config, err := os.UserConfigDir()
	if err != nil {
		return "", fmt.Errorf("oauth: resolve user config directory: %w", err)
	}
	return filepath.Join(config, "eul", "auth.json"), nil
}

func NewManager(path string, options Options) *Manager {
	base := options.AuthBaseURL
	if base == "" {
		base = defaultAuthBaseURL
	}

	client := &http.Client{}
	if options.HTTPClient != nil {
		*client = *options.HTTPClient
	}
	if client.Timeout <= 0 {
		client.Timeout = defaultHTTPTimeout
	}
	client.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }

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
		path:            path,
		httpClient:      client,
		authBaseURL:     strings.TrimRight(base, "/"),
		callbackAddress: callback,
		now:             now,
		sleep:           sleep,
	}
}

func (m *Manager) Login(ctx context.Context, method backend.LoginMethod, interaction backend.LoginInteraction) error {
	if err := ctx.Err(); err != nil {
		return err
	}

	var (
		credential credentials
		err        error
	)
	switch method {
	case backend.LoginBrowser:
		credential, err = m.loginBrowser(ctx, interaction)
	case backend.LoginDevice:
		credential, err = m.loginDevice(ctx, interaction)
	default:
		return errors.New("oauth: unsupported login method")
	}

	if err != nil {
		return err
	}

	return m.withFileLock(ctx, func() error { return writeCredentials(m.path, credential) })
}

func (m *Manager) Resolve(ctx context.Context) (AccessCredential, error) {
	var result credentials
	err := m.withFileLock(ctx, func() error {
		credential, err := readCredentials(m.path)
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

		if err := writeCredentials(m.path, refreshed); err != nil {
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
	return m.withFileLock(ctx, func() error {
		info, err := os.Lstat(m.path)
		if errors.Is(err, os.ErrNotExist) {
			return nil
		}
		if err != nil {
			return fmt.Errorf("oauth: inspect credential file: %w", err)
		}
		if !info.Mode().IsRegular() {
			return errors.New("oauth: credential path is not a regular file")
		}

		if err := os.Remove(m.path); err != nil {
			return fmt.Errorf("oauth: remove credential file: %w", err)
		}

		return nil
	})
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
