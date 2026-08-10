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
)

func (m *Manager) loginDevice(ctx context.Context, interaction Interaction) (credentials, error) {
	requestBody, _ := json.Marshal(map[string]string{"client_id": clientID})
	response, err := m.doJSON(ctx, http.MethodPost, "/api/accounts/deviceauth/usercode", requestBody)
	if err != nil {
		return credentials{}, err
	}

	if response.status == http.StatusNotFound {
		return credentials{}, errors.New("oauth: OpenAI device authorization is not enabled")
	}
	if response.status < 200 || response.status >= 300 {
		return credentials{}, fmt.Errorf("oauth: device authorization request failed with HTTP %d", response.status)
	}

	var raw struct {
		DeviceAuthID string          `json:"device_auth_id"`
		UserCode     string          `json:"user_code"`
		Interval     json.RawMessage `json:"interval"`
	}
	if err := json.Unmarshal(response.body, &raw); err != nil {
		return credentials{}, errors.New("oauth: invalid device authorization response")
	}

	interval, err := parseInterval(raw.Interval)
	if err != nil || raw.DeviceAuthID == "" || raw.UserCode == "" {
		return credentials{}, errors.New("oauth: invalid device authorization response")
	}
	if interaction.DeviceCode == nil {
		return credentials{}, errors.New("oauth: device login interaction is unavailable")
	}
	if err := interaction.DeviceCode(DeviceCode{VerificationURL: m.authBaseURL + "/codex/device", UserCode: raw.UserCode}); err != nil {
		return credentials{}, fmt.Errorf("oauth: present device code: %w", err)
	}

	pollCtx, cancel := context.WithTimeout(ctx, deviceTimeout)
	defer cancel()
	pollDelay := interval
	for {
		if err := m.sleep(pollCtx, pollDelay); err != nil {
			if errors.Is(err, context.DeadlineExceeded) && ctx.Err() == nil {
				return credentials{}, errors.New("oauth: device authorization timed out")
			}
			return credentials{}, err
		}

		body, _ := json.Marshal(map[string]string{"device_auth_id": raw.DeviceAuthID, "user_code": raw.UserCode})
		poll, err := m.doJSON(pollCtx, http.MethodPost, "/api/accounts/deviceauth/token", body)
		if err != nil {
			return credentials{}, err
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
			return credentials{}, fmt.Errorf("oauth: device authorization failed with HTTP %d", poll.status)
		}

		var completed struct {
			AuthorizationCode string `json:"authorization_code"`
			CodeVerifier      string `json:"code_verifier"`
		}
		if err := json.Unmarshal(poll.body, &completed); err != nil || completed.AuthorizationCode == "" || completed.CodeVerifier == "" {
			return credentials{}, errors.New("oauth: invalid device authorization completion")
		}
		return m.exchangeCode(pollCtx, completed.AuthorizationCode, completed.CodeVerifier, m.authBaseURL+deviceRedirectPath)
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

	if math.IsNaN(number) || math.IsInf(number, 0) || number < 0 || number > 300 {
		return 0, errors.New("invalid interval")
	}

	return time.Duration(number * float64(time.Second)), nil
}
