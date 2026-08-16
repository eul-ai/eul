package oauth

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"net/url"
)

func (m *Manager) loginBrowser(ctx context.Context, presentURL func(string) error) (credentials, error) {
	loginCtx, cancelLogin := context.WithTimeout(ctx, authorizationTimeout)
	defer cancelLogin()

	verifier, challenge, err := generatePKCE()
	if err != nil {
		return credentials{}, err
	}
	state, err := randomHex(16)
	if err != nil {
		return credentials{}, err
	}

	callback, err := m.startLoopbackCallback(state)
	if err != nil {
		return credentials{}, err
	}
	defer callback.stop()

	if presentURL == nil {
		return credentials{}, errors.New("oauth: browser login interaction is unavailable")
	}
	authURL := m.authorizationURL(callback.redirectURI, challenge, state)
	if err := presentURL(authURL); err != nil {
		return credentials{}, fmt.Errorf("oauth: present authorization URL: %w", err)
	}

	code, err := callback.wait(loginCtx)
	if errors.Is(err, context.DeadlineExceeded) && ctx.Err() == nil {
		return credentials{}, errors.New("oauth: browser authorization timed out")
	}
	if err != nil {
		return credentials{}, err
	}
	return m.exchangeCode(loginCtx, code, verifier, callback.redirectURI)
}

func (m *Manager) authorizationURL(redirectURI, challenge, state string) string {
	values := url.Values{
		"response_type":              {"code"},
		"client_id":                  {clientID},
		"redirect_uri":               {redirectURI},
		"scope":                      {"openid profile email offline_access"},
		"code_challenge":             {challenge},
		"code_challenge_method":      {"S256"},
		"state":                      {state},
		"id_token_add_organizations": {"true"},
		"codex_cli_simplified_flow":  {"true"},
		"originator":                 {"eul"},
	}
	return m.authBaseURL + "/oauth/authorize?" + values.Encode()
}

func generatePKCE() (string, string, error) {
	bytes := make([]byte, 32)
	if _, err := rand.Read(bytes); err != nil {
		return "", "", errors.New("oauth: generate PKCE verifier")
	}

	verifier := base64.RawURLEncoding.EncodeToString(bytes)
	hash := sha256.Sum256([]byte(verifier))
	return verifier, base64.RawURLEncoding.EncodeToString(hash[:]), nil
}

func randomHex(size int) (string, error) {
	value := make([]byte, size)
	if _, err := rand.Read(value); err != nil {
		return "", errors.New("oauth: generate state")
	}
	return hex.EncodeToString(value), nil
}
