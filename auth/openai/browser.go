package openai

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"time"
)

func (m *Manager) loginBrowser(ctx context.Context, interaction Interaction) (credentials, error) {
	loginCtx, cancelLogin := context.WithTimeout(ctx, deviceTimeout)
	defer cancelLogin()

	verifier, challenge, err := generatePKCE()
	if err != nil {
		return credentials{}, err
	}
	state, err := randomHex(16)
	if err != nil {
		return credentials{}, err
	}

	listener, err := net.Listen("tcp", m.callbackAddress)
	if err != nil {
		return credentials{}, fmt.Errorf("oauth: start loopback callback: %w", err)
	}
	defer listener.Close()
	port := listener.Addr().(*net.TCPAddr).Port
	redirectURI := "http://localhost:" + strconv.Itoa(port) + browserCallbackPath
	result := make(chan callbackResult, 1)
	server := &http.Server{ReadHeaderTimeout: 10 * time.Second, Handler: callbackHandler(state, result)}
	serveDone := make(chan error, 1)
	go func() { serveDone <- server.Serve(listener) }()

	defer func() {
		shutdownCtx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		_ = server.Shutdown(shutdownCtx)
		select {
		case <-serveDone:
		default:
		}
	}()

	authURL := m.authorizationURL(redirectURI, challenge, state)
	if interaction.AuthURL == nil {
		return credentials{}, errors.New("oauth: browser login interaction is unavailable")
	}
	if err := interaction.AuthURL(authURL); err != nil {
		return credentials{}, fmt.Errorf("oauth: present authorization URL: %w", err)
	}

	select {
	case <-loginCtx.Done():
		if ctx.Err() != nil {
			return credentials{}, ctx.Err()
		}
		return credentials{}, errors.New("oauth: browser authorization timed out")
	case serveErr := <-serveDone:
		if errors.Is(serveErr, http.ErrServerClosed) {
			return credentials{}, errors.New("oauth: loopback callback stopped before authorization completed")
		}
		return credentials{}, errors.New("oauth: loopback callback failed")
	case callback := <-result:
		if callback.err != nil {
			return credentials{}, callback.err
		}
		return m.exchangeCode(loginCtx, callback.code, verifier, redirectURI)
	}
}

type callbackResult struct {
	code string
	err  error
}

func callbackHandler(expectedState string, result chan<- callbackResult) http.Handler {
	return http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		writer.Header().Set("Content-Type", "text/html; charset=utf-8")

		if request.Method != http.MethodGet || request.URL.Path != browserCallbackPath {
			http.Error(writer, "Callback route not found.", http.StatusNotFound)
			return
		}

		state := request.URL.Query().Get("state")
		if len(state) != len(expectedState) || subtle.ConstantTimeCompare([]byte(state), []byte(expectedState)) != 1 {
			http.Error(writer, "OAuth state mismatch.", http.StatusBadRequest)
			return
		}

		if request.URL.Query().Get("error") != "" {
			http.Error(writer, "OpenAI authorization failed.", http.StatusBadRequest)
			select {
			case result <- callbackResult{err: errors.New("oauth: OpenAI authorization failed")}:
			default:
			}
			return
		}

		code := request.URL.Query().Get("code")
		if code == "" {
			http.Error(writer, "Missing authorization code.", http.StatusBadRequest)
			select {
			case result <- callbackResult{err: errors.New("oauth: callback is missing an authorization code")}:
			default:
			}
			return
		}

		_, _ = io.WriteString(writer, "OpenAI authentication completed. You can close this window.")
		select {
		case result <- callbackResult{code: code}:
		default:
		}
	})
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
