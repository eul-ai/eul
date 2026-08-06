package openai

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"
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
)

// LoginMethod selects the interactive OAuth ceremony.
type LoginMethod string

const (
	LoginBrowser LoginMethod = "browser"
	LoginDevice  LoginMethod = "device"
)

// DeviceCode is safe display data for the device authorization flow.
type DeviceCode struct {
	VerificationURL string
	UserCode        string
}

// Interaction lets the CLI render OAuth steps without coupling auth to a terminal.
type Interaction struct {
	AuthURL    func(string) error
	DeviceCode func(DeviceCode) error
}

// Credentials are yaah-owned ChatGPT OAuth credentials.
type Credentials struct {
	Version      int    `json:"version"`
	Type         string `json:"type"`
	AccessToken  string `json:"access_token"`
	RefreshToken string `json:"refresh_token"`
	ExpiresAt    int64  `json:"expires_at"`
	AccountID    string `json:"account_id"`
}

// Options supplies hermetic seams for OAuth tests.
type Options struct {
	HTTPClient      *http.Client
	AuthBaseURL     string
	CallbackAddress string
	Now             func() time.Time
	Sleep           func(context.Context, time.Duration) error
}

// Manager owns yaah's credential file and refresh lifecycle.
type Manager struct {
	path            string
	httpClient      *http.Client
	authBaseURL     string
	callbackAddress string
	now             func() time.Time
	sleep           func(context.Context, time.Duration) error
}

// DefaultCredentialPath resolves a yaah-owned auth file without consulting other clients.
func DefaultCredentialPath(yaahHome string) (string, error) {
	if yaahHome != "" && !filepath.IsAbs(yaahHome) {
		return "", errors.New("oauth: YAAH_HOME must be an absolute path")
	}
	if yaahHome != "" {
		return filepath.Join(filepath.Clean(yaahHome), "auth.json"), nil
	}
	config, err := os.UserConfigDir()
	if err != nil {
		return "", fmt.Errorf("oauth: resolve user config directory: %w", err)
	}
	return filepath.Join(config, "yaah", "auth.json"), nil
}

// NewManager constructs an OAuth credential manager.
func NewManager(path string, options Options) *Manager {
	base := options.AuthBaseURL
	if base == "" {
		base = defaultAuthBaseURL
	}
	client := options.HTTPClient
	if client == nil {
		client = &http.Client{Timeout: 30 * time.Second}
	} else if client.Timeout <= 0 {
		client.Timeout = 30 * time.Second
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

// Login completes the selected OAuth flow and atomically stores its credentials.
func (m *Manager) Login(ctx context.Context, method LoginMethod, interaction Interaction) (Credentials, error) {
	if err := ctx.Err(); err != nil {
		return Credentials{}, err
	}
	var (
		credential Credentials
		err        error
	)
	switch method {
	case LoginBrowser:
		credential, err = m.loginBrowser(ctx, interaction)
	case LoginDevice:
		credential, err = m.loginDevice(ctx, interaction)
	default:
		return Credentials{}, errors.New("oauth: unsupported login method")
	}
	if err != nil {
		return Credentials{}, err
	}
	if err := m.withFileLock(ctx, func() error { return writeCredentials(m.path, credential) }); err != nil {
		return Credentials{}, err
	}
	return credential, nil
}

// Resolve returns a valid credential, refreshing and persisting it when needed.
func (m *Manager) Resolve(ctx context.Context) (Credentials, error) {
	var result Credentials
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
	return result, err
}

// Logout removes only yaah's credential file.
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

func (m *Manager) loginBrowser(ctx context.Context, interaction Interaction) (Credentials, error) {
	loginCtx, cancelLogin := context.WithTimeout(ctx, deviceTimeout)
	defer cancelLogin()
	verifier, challenge, err := generatePKCE()
	if err != nil {
		return Credentials{}, err
	}
	state, err := randomHex(16)
	if err != nil {
		return Credentials{}, err
	}
	listener, err := net.Listen("tcp", m.callbackAddress)
	if err != nil {
		return Credentials{}, fmt.Errorf("oauth: start loopback callback: %w", err)
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
		return Credentials{}, errors.New("oauth: browser login interaction is unavailable")
	}
	if err := interaction.AuthURL(authURL); err != nil {
		return Credentials{}, fmt.Errorf("oauth: present authorization URL: %w", err)
	}
	select {
	case <-loginCtx.Done():
		if ctx.Err() != nil {
			return Credentials{}, ctx.Err()
		}
		return Credentials{}, errors.New("oauth: browser authorization timed out")
	case serveErr := <-serveDone:
		if errors.Is(serveErr, http.ErrServerClosed) {
			return Credentials{}, errors.New("oauth: loopback callback stopped before authorization completed")
		}
		return Credentials{}, errors.New("oauth: loopback callback failed")
	case callback := <-result:
		if callback.err != nil {
			return Credentials{}, callback.err
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
		"originator":                 {"yaah"},
	}
	return m.authBaseURL + "/oauth/authorize?" + values.Encode()
}

func (m *Manager) loginDevice(ctx context.Context, interaction Interaction) (Credentials, error) {
	requestBody, _ := json.Marshal(map[string]string{"client_id": clientID})
	response, err := m.doJSON(ctx, http.MethodPost, "/api/accounts/deviceauth/usercode", requestBody)
	if err != nil {
		return Credentials{}, err
	}
	if response.status == http.StatusNotFound {
		return Credentials{}, errors.New("oauth: OpenAI device authorization is not enabled")
	}
	if response.status < 200 || response.status >= 300 {
		return Credentials{}, fmt.Errorf("oauth: device authorization request failed with HTTP %d", response.status)
	}
	var raw struct {
		DeviceAuthID string          `json:"device_auth_id"`
		UserCode     string          `json:"user_code"`
		Interval     json.RawMessage `json:"interval"`
	}
	if err := json.Unmarshal(response.body, &raw); err != nil {
		return Credentials{}, errors.New("oauth: invalid device authorization response")
	}
	interval, err := parseInterval(raw.Interval)
	if err != nil || raw.DeviceAuthID == "" || raw.UserCode == "" {
		return Credentials{}, errors.New("oauth: invalid device authorization response")
	}
	if interaction.DeviceCode == nil {
		return Credentials{}, errors.New("oauth: device login interaction is unavailable")
	}
	if err := interaction.DeviceCode(DeviceCode{VerificationURL: m.authBaseURL + "/codex/device", UserCode: raw.UserCode}); err != nil {
		return Credentials{}, fmt.Errorf("oauth: present device code: %w", err)
	}

	pollCtx, cancel := context.WithTimeout(ctx, deviceTimeout)
	defer cancel()
	pollDelay := interval
	for {
		if err := m.sleep(pollCtx, pollDelay); err != nil {
			if errors.Is(err, context.DeadlineExceeded) && ctx.Err() == nil {
				return Credentials{}, errors.New("oauth: device authorization timed out")
			}
			return Credentials{}, err
		}
		body, _ := json.Marshal(map[string]string{"device_auth_id": raw.DeviceAuthID, "user_code": raw.UserCode})
		poll, err := m.doJSON(pollCtx, http.MethodPost, "/api/accounts/deviceauth/token", body)
		if err != nil {
			return Credentials{}, err
		}
		if poll.status < 200 || poll.status >= 300 {
			var oauthError struct {
				Error any `json:"error"`
			}
			_ = json.Unmarshal(poll.body, &oauthError)
			code := errorCode(oauthError.Error)
			if (poll.status == http.StatusForbidden || poll.status == http.StatusNotFound) && code == "" {
				continue
			}
			switch code {
			case "deviceauth_authorization_pending", "authorization_pending":
				continue
			case "slow_down":
				pollDelay += 5 * time.Second
				continue
			}
			return Credentials{}, fmt.Errorf("oauth: device authorization failed with HTTP %d", poll.status)
		}
		var completed struct {
			AuthorizationCode string `json:"authorization_code"`
			CodeVerifier      string `json:"code_verifier"`
		}
		if err := json.Unmarshal(poll.body, &completed); err != nil || completed.AuthorizationCode == "" || completed.CodeVerifier == "" {
			return Credentials{}, errors.New("oauth: invalid device authorization completion")
		}
		return m.exchangeCode(ctx, completed.AuthorizationCode, completed.CodeVerifier, m.authBaseURL+deviceRedirectPath)
	}
}

func errorCode(value any) string {
	switch typed := value.(type) {
	case string:
		return typed
	case map[string]any:
		code, _ := typed["code"].(string)
		return code
	default:
		return ""
	}
}

func parseInterval(raw json.RawMessage) (time.Duration, error) {
	if len(raw) == 0 {
		return 0, errors.New("missing interval")
	}
	var number float64
	if err := json.Unmarshal(raw, &number); err != nil {
		var text string
		if json.Unmarshal(raw, &text) != nil {
			return 0, errors.New("invalid interval")
		}
		parsed, err := strconv.ParseFloat(strings.TrimSpace(text), 64)
		if err != nil {
			return 0, errors.New("invalid interval")
		}
		number = parsed
	}
	if number < 0 || number > 300 {
		return 0, errors.New("invalid interval")
	}
	return time.Duration(number * float64(time.Second)), nil
}

func (m *Manager) exchangeCode(ctx context.Context, code, verifier, redirectURI string) (Credentials, error) {
	values := url.Values{
		"grant_type":    {"authorization_code"},
		"client_id":     {clientID},
		"code":          {code},
		"code_verifier": {verifier},
		"redirect_uri":  {redirectURI},
	}
	response, err := m.doForm(ctx, "/oauth/token", values)
	if err != nil {
		return Credentials{}, err
	}
	return m.credentialsFromTokenResponse(response, "")
}

func (m *Manager) refresh(ctx context.Context, old Credentials) (Credentials, error) {
	values := url.Values{
		"grant_type":    {"refresh_token"},
		"refresh_token": {old.RefreshToken},
		"client_id":     {clientID},
	}
	response, err := m.doForm(ctx, "/oauth/token", values)
	if err != nil {
		return Credentials{}, err
	}
	return m.credentialsFromTokenResponse(response, old.RefreshToken)
}

type tokenResponse struct {
	AccessToken  string  `json:"access_token"`
	RefreshToken string  `json:"refresh_token"`
	ExpiresIn    float64 `json:"expires_in"`
}

func (m *Manager) credentialsFromTokenResponse(response boundedResponse, previousRefresh string) (Credentials, error) {
	if response.status < 200 || response.status >= 300 {
		return Credentials{}, fmt.Errorf("oauth: token request failed with HTTP %d", response.status)
	}
	var token tokenResponse
	if err := json.Unmarshal(response.body, &token); err != nil || token.AccessToken == "" || token.ExpiresIn <= 0 || token.ExpiresIn > 366*24*60*60 {
		return Credentials{}, errors.New("oauth: invalid token response")
	}
	if token.RefreshToken == "" {
		token.RefreshToken = previousRefresh
	}
	if token.RefreshToken == "" {
		return Credentials{}, errors.New("oauth: invalid token response")
	}
	accountID, err := accountIDFromJWT(token.AccessToken)
	if err != nil {
		return Credentials{}, err
	}
	return Credentials{
		Version:      credentialVersion,
		Type:         "oauth",
		AccessToken:  token.AccessToken,
		RefreshToken: token.RefreshToken,
		ExpiresAt:    m.now().Add(time.Duration(token.ExpiresIn * float64(time.Second))).UnixMilli(),
		AccountID:    accountID,
	}, nil
}

type boundedResponse struct {
	status int
	body   []byte
}

func (m *Manager) doJSON(ctx context.Context, method, path string, body []byte) (boundedResponse, error) {
	request, err := http.NewRequestWithContext(ctx, method, m.authBaseURL+path, bytes.NewReader(body))
	if err != nil {
		return boundedResponse{}, errors.New("oauth: create authentication request")
	}
	request.Header.Set("Content-Type", "application/json")
	return m.do(request)
}

func (m *Manager) doForm(ctx context.Context, path string, values url.Values) (boundedResponse, error) {
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, m.authBaseURL+path, strings.NewReader(values.Encode()))
	if err != nil {
		return boundedResponse{}, errors.New("oauth: create token request")
	}
	request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	return m.do(request)
}

func (m *Manager) do(request *http.Request) (boundedResponse, error) {
	response, err := m.httpClient.Do(request)
	if err != nil {
		if request.Context().Err() != nil {
			return boundedResponse{}, request.Context().Err()
		}
		return boundedResponse{}, errors.New("oauth: authentication request failed")
	}
	defer response.Body.Close()
	body, truncated, err := readBounded(response.Body, maxAuthResponseBytes)
	if err != nil {
		return boundedResponse{}, errors.New("oauth: read authentication response")
	}
	if truncated {
		return boundedResponse{}, errors.New("oauth: authentication response is too large")
	}
	return boundedResponse{status: response.StatusCode, body: body}, nil
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

func readCredentials(path string) (Credentials, error) {
	info, err := os.Lstat(path)
	if errors.Is(err, os.ErrNotExist) {
		return Credentials{}, errors.New("oauth: not logged in; run 'yaah login'")
	}
	if err != nil {
		return Credentials{}, fmt.Errorf("oauth: inspect credential file: %w", err)
	}
	if !info.Mode().IsRegular() {
		return Credentials{}, errors.New("oauth: credential path is not a regular file")
	}
	if info.Mode().Perm()&0o077 != 0 {
		return Credentials{}, errors.New("oauth: credential file permissions must be 0600")
	}
	file, err := os.Open(path)
	if err != nil {
		return Credentials{}, fmt.Errorf("oauth: open credential file: %w", err)
	}
	defer file.Close()
	body, truncated, err := readBounded(file, maxCredentialBytes)
	if err != nil {
		return Credentials{}, fmt.Errorf("oauth: read credential file: %w", err)
	}
	if truncated {
		return Credentials{}, errors.New("oauth: credential file is too large")
	}
	var credential Credentials
	if err := json.Unmarshal(body, &credential); err != nil || credential.Version != credentialVersion || credential.Type != "oauth" || credential.ExpiresAt <= 0 || credential.AccessToken == "" || credential.RefreshToken == "" || credential.AccountID == "" {
		return Credentials{}, errors.New("oauth: invalid credential file")
	}
	return credential, nil
}

func writeCredentials(path string, credential Credentials) error {
	directory := filepath.Dir(path)
	if err := os.MkdirAll(directory, 0o700); err != nil {
		return fmt.Errorf("oauth: create credential directory: %w", err)
	}
	if err := os.Chmod(directory, 0o700); err != nil {
		return fmt.Errorf("oauth: secure credential directory: %w", err)
	}
	info, err := os.Lstat(path)
	if err == nil && !info.Mode().IsRegular() {
		return errors.New("oauth: credential path is not a regular file")
	}
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("oauth: inspect credential path: %w", err)
	}
	encoded, err := json.MarshalIndent(credential, "", "  ")
	if err != nil {
		return errors.New("oauth: encode credentials")
	}
	encoded = append(encoded, '\n')
	temporary, err := os.CreateTemp(directory, ".auth-*")
	if err != nil {
		return fmt.Errorf("oauth: create credential temporary file: %w", err)
	}
	temporaryPath := temporary.Name()
	cleanup := func() {
		_ = temporary.Close()
		_ = os.Remove(temporaryPath)
	}
	if err := temporary.Chmod(0o600); err != nil {
		cleanup()
		return fmt.Errorf("oauth: secure credential temporary file: %w", err)
	}
	if _, err := temporary.Write(encoded); err != nil {
		cleanup()
		return fmt.Errorf("oauth: write credential temporary file: %w", err)
	}
	if err := temporary.Sync(); err != nil {
		cleanup()
		return fmt.Errorf("oauth: sync credential temporary file: %w", err)
	}
	if err := temporary.Close(); err != nil {
		_ = os.Remove(temporaryPath)
		return fmt.Errorf("oauth: close credential temporary file: %w", err)
	}
	if err := os.Rename(temporaryPath, path); err != nil {
		_ = os.Remove(temporaryPath)
		return fmt.Errorf("oauth: replace credential file: %w", err)
	}
	if err := os.Chmod(path, 0o600); err != nil {
		return fmt.Errorf("oauth: secure credential file: %w", err)
	}
	return nil
}

func (m *Manager) withFileLock(ctx context.Context, function func() error) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	lockPath := m.path + ".lock"
	lockDirectory := filepath.Dir(lockPath)
	if err := os.MkdirAll(lockDirectory, 0o700); err != nil {
		return fmt.Errorf("oauth: create lock directory: %w", err)
	}
	if err := os.Chmod(lockDirectory, 0o700); err != nil {
		return fmt.Errorf("oauth: secure lock directory: %w", err)
	}
	lock, err := os.OpenFile(lockPath, os.O_RDWR|os.O_CREATE, 0o600)
	if err != nil {
		return fmt.Errorf("oauth: open credential lock: %w", err)
	}
	defer lock.Close()
	if err := lock.Chmod(0o600); err != nil {
		return fmt.Errorf("oauth: secure credential lock: %w", err)
	}
	for {
		err := syscall.Flock(int(lock.Fd()), syscall.LOCK_EX|syscall.LOCK_NB)
		if err == nil {
			break
		}
		if !errors.Is(err, syscall.EWOULDBLOCK) && !errors.Is(err, syscall.EAGAIN) {
			return fmt.Errorf("oauth: acquire credential lock: %w", err)
		}
		if err := m.sleep(ctx, 50*time.Millisecond); err != nil {
			return err
		}
	}
	if err := ctx.Err(); err != nil {
		_ = syscall.Flock(int(lock.Fd()), syscall.LOCK_UN)
		return err
	}
	result := function()
	unlockErr := syscall.Flock(int(lock.Fd()), syscall.LOCK_UN)
	if result != nil {
		return result
	}
	if unlockErr != nil {
		return fmt.Errorf("oauth: release credential lock: %w", unlockErr)
	}
	return nil
}

func readBounded(reader io.Reader, maximum int64) ([]byte, bool, error) {
	body, err := io.ReadAll(io.LimitReader(reader, maximum+1))
	if err != nil {
		return nil, false, err
	}
	if int64(len(body)) <= maximum {
		return body, false, nil
	}
	return body[:maximum], true, nil
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
