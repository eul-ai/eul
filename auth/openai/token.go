package openai

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"strings"
	"time"
)

func (m *Manager) exchangeCode(ctx context.Context, code, verifier, redirectURI string) (credentials, error) {
	values := url.Values{
		"grant_type":    {"authorization_code"},
		"client_id":     {clientID},
		"code":          {code},
		"code_verifier": {verifier},
		"redirect_uri":  {redirectURI},
	}

	response, err := m.doForm(ctx, "/oauth/token", values)
	if err != nil {
		return credentials{}, err
	}
	return m.credentialsFromTokenResponse(response, "")
}

func (m *Manager) refresh(ctx context.Context, old credentials) (credentials, error) {
	values := url.Values{
		"grant_type":    {"refresh_token"},
		"refresh_token": {old.RefreshToken},
		"client_id":     {clientID},
	}

	response, err := m.doForm(ctx, "/oauth/token", values)
	if err != nil {
		return credentials{}, err
	}
	return m.credentialsFromTokenResponse(response, old.RefreshToken)
}

type tokenResponse struct {
	AccessToken  string  `json:"access_token"`
	RefreshToken string  `json:"refresh_token"`
	ExpiresIn    float64 `json:"expires_in"`
}

func (m *Manager) credentialsFromTokenResponse(response boundedResponse, previousRefresh string) (credentials, error) {
	if response.status < 200 || response.status >= 300 {
		return credentials{}, fmt.Errorf("oauth: token request failed with HTTP %d", response.status)
	}

	var token tokenResponse
	if err := json.Unmarshal(response.body, &token); err != nil || token.AccessToken == "" || token.ExpiresIn <= 0 || token.ExpiresIn > 366*24*60*60 {
		return credentials{}, errors.New("oauth: invalid token response")
	}
	if token.RefreshToken == "" {
		token.RefreshToken = previousRefresh
	}
	if token.RefreshToken == "" {
		return credentials{}, errors.New("oauth: invalid token response")
	}

	accountID, err := accountIDFromJWT(token.AccessToken)
	if err != nil {
		return credentials{}, err
	}

	return credentials{
		Version:      credentialVersion,
		Type:         "oauth",
		AccessToken:  token.AccessToken,
		RefreshToken: token.RefreshToken,
		ExpiresAt:    m.now().Add(time.Duration(token.ExpiresIn * float64(time.Second))).UnixMilli(),
		AccountID:    accountID,
	}, nil
}

func accountIDFromJWT(token string) (string, error) {
	parts := strings.Split(token, ".")
	if len(parts) != 3 {
		return "", errors.New("oauth: access token is not a valid JWT")
	}

	payload, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil || len(payload) > 64*1024 {
		return "", errors.New("oauth: access token has an invalid JWT payload")
	}

	var claims struct {
		Auth struct {
			AccountID string `json:"chatgpt_account_id"`
		} `json:"https://api.openai.com/auth"`
	}
	if err := json.Unmarshal(payload, &claims); err != nil || claims.Auth.AccountID == "" {
		return "", errors.New("oauth: access token is missing a valid ChatGPT account ID")
	}

	return claims.Auth.AccountID, nil
}
