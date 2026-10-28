package httpclient

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"unicode/utf8"
)

func marshalBoundedJSON(value any, maximum int64) ([]byte, bool, error) {
	body, err := json.Marshal(value)
	if err != nil {
		return nil, false, err
	}
	return body, int64(len(body)) > maximum, nil
}

func ReadBounded(reader io.Reader, maximum int64) ([]byte, bool, error) {
	data, err := io.ReadAll(io.LimitReader(reader, maximum+1))
	if err != nil {
		return nil, false, err
	}
	if int64(len(data)) <= maximum {
		return data, false, nil
	}
	return data[:maximum], true, nil
}

func decodeBoundedJSON(reader io.Reader, maximum int64, target any) (bool, error) {
	data, truncated, err := ReadBounded(reader, maximum)
	if err != nil || truncated {
		return truncated, err
	}
	return false, json.Unmarshal(data, target)
}

func readBoundedText(reader io.Reader, maximum int64) (string, bool, error) {
	data, truncated, err := ReadBounded(reader, maximum)
	if err != nil {
		return "", false, err
	}
	return strings.TrimSpace(strings.ToValidUTF8(string(data), "�")), truncated, nil
}

func GetJSON(ctx context.Context, client *http.Client, url string, prepare func(*http.Request), target any) error {
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return err
	}
	request.Header.Set("Accept", "application/json")
	prepare(request)

	response, err := client.Do(request)
	if err != nil {
		return contextOr(ctx, err)
	}
	defer response.Body.Close()

	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return readHTTPStatusError(response)
	}
	truncated, err := decodeBoundedJSON(response.Body, maxResponseBytes, target)
	if err != nil {
		return contextOr(ctx, err)
	}
	if truncated {
		return fmt.Errorf("response exceeds %d bytes", maxResponseBytes)
	}
	return nil
}

func contextOr(ctx context.Context, err error) error {
	if contextErr := ctx.Err(); contextErr != nil {
		return contextErr
	}
	return err
}

func readHTTPStatusError(response *http.Response) error {
	detail, truncated, err := readBoundedText(response.Body, maxErrorBytes)
	switch {
	case err != nil:
		detail = "read error response: " + err.Error()
	case detail == "":
		detail = "empty response"
	case truncated:
		detail += " [truncated]"
	}
	return errors.New(formatErrorMessage("", "HTTP %s: %s", response.Status, detail))
}

func formatErrorMessage(prefix string, format string, arguments ...any) string {
	message := strings.ToValidUTF8(fmt.Sprintf(format, arguments...), "�")
	if prefix != "" {
		message = prefix + ": " + message
	}
	return truncateUTF8(message, int(maxErrorBytes))
}

func truncateUTF8(text string, maximum int) string {
	if maximum < 0 {
		maximum = 0
	}
	if len(text) <= maximum {
		return text
	}

	end := maximum
	for end > 0 && end < len(text) && !utf8.RuneStart(text[end]) {
		end--
	}
	return text[:end]
}
