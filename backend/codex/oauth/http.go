package oauth

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net/http"
	"net/url"
	"strings"
)

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
