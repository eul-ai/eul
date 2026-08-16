package oauth

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/eul-ai/eul/backend"
)

type deviceAuthorization struct {
	id       string
	userCode string
	interval time.Duration
}

type deviceAuthorizationResponse struct {
	DeviceAuthID string          `json:"device_auth_id"`
	UserCode     string          `json:"user_code"`
	Interval     json.RawMessage `json:"interval"`
}

type deviceAuthorizationCompletion struct {
	AuthorizationCode string `json:"authorization_code"`
	CodeVerifier      string `json:"code_verifier"`
}

type deviceAuthorizationError struct {
	Error any `json:"error"`
}

func (m *Manager) loginDevice(ctx context.Context, presentCode func(backend.DeviceCode) error) (credentials, error) {
	authorization, err := m.startDeviceAuthorization(ctx)
	if err != nil {
		return credentials{}, err
	}
	if presentCode == nil {
		return credentials{}, errors.New("oauth: device login interaction is unavailable")
	}
	if err := presentCode(backend.DeviceCode{
		VerificationURL: m.authBaseURL + "/codex/device",
		UserCode:        authorization.userCode,
	}); err != nil {
		return credentials{}, fmt.Errorf("oauth: present device code: %w", err)
	}

	pollCtx, cancel := context.WithTimeout(ctx, authorizationTimeout)
	defer cancel()

	completion, err := m.pollDeviceAuthorization(pollCtx, authorization)
	if errors.Is(err, context.DeadlineExceeded) && ctx.Err() == nil {
		return credentials{}, errors.New("oauth: device authorization timed out")
	}
	if err != nil {
		return credentials{}, err
	}
	return m.exchangeCode(pollCtx, completion.AuthorizationCode, completion.CodeVerifier, m.authBaseURL+deviceRedirectPath)
}

func (m *Manager) startDeviceAuthorization(ctx context.Context) (deviceAuthorization, error) {
	requestBody, _ := json.Marshal(map[string]string{"client_id": clientID})
	response, err := m.doJSON(ctx, http.MethodPost, "/api/accounts/deviceauth/usercode", requestBody)
	if err != nil {
		return deviceAuthorization{}, err
	}
	if response.status == http.StatusNotFound {
		return deviceAuthorization{}, errors.New("oauth: OpenAI device authorization is not enabled")
	}
	if response.status < 200 || response.status >= 300 {
		return deviceAuthorization{}, fmt.Errorf("oauth: device authorization request failed with HTTP %d", response.status)
	}

	var wire deviceAuthorizationResponse
	if err := json.Unmarshal(response.body, &wire); err != nil {
		return deviceAuthorization{}, errors.New("oauth: invalid device authorization response")
	}
	interval, err := parseInterval(wire.Interval)
	if err != nil || wire.DeviceAuthID == "" || wire.UserCode == "" {
		return deviceAuthorization{}, errors.New("oauth: invalid device authorization response")
	}
	return deviceAuthorization{id: wire.DeviceAuthID, userCode: wire.UserCode, interval: interval}, nil
}

func (m *Manager) pollDeviceAuthorization(ctx context.Context, authorization deviceAuthorization) (deviceAuthorizationCompletion, error) {
	pollDelay := authorization.interval
	for {
		if err := m.sleep(ctx, pollDelay); err != nil {
			return deviceAuthorizationCompletion{}, err
		}

		body, _ := json.Marshal(map[string]string{"device_auth_id": authorization.id, "user_code": authorization.userCode})
		response, err := m.doJSON(ctx, http.MethodPost, "/api/accounts/deviceauth/token", body)
		if err != nil {
			return deviceAuthorizationCompletion{}, err
		}

		if response.status < 200 || response.status >= 300 {
			var oauthError deviceAuthorizationError
			_ = json.Unmarshal(response.body, &oauthError)
			code := errorCode(oauthError.Error)
			if (response.status == http.StatusForbidden || response.status == http.StatusNotFound) && code == "" {
				continue
			}
			switch code {
			case "deviceauth_authorization_pending", "authorization_pending":
				continue
			case "slow_down":
				pollDelay += 5 * time.Second
				continue
			}
			return deviceAuthorizationCompletion{}, fmt.Errorf("oauth: device authorization failed with HTTP %d", response.status)
		}

		var completion deviceAuthorizationCompletion
		if err := json.Unmarshal(response.body, &completion); err != nil || completion.AuthorizationCode == "" || completion.CodeVerifier == "" {
			return deviceAuthorizationCompletion{}, errors.New("oauth: invalid device authorization completion")
		}
		return completion, nil
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

	if math.IsNaN(number) || math.IsInf(number, 0) || number < 1 || number > 300 {
		return 0, errors.New("invalid interval")
	}

	return time.Duration(number * float64(time.Second)), nil
}
