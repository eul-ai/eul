package httpclient

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"unicode/utf8"
)

func MarshalBoundedJSON(value any, maximum int64) ([]byte, bool, error) {
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

func DecodeBoundedJSON(reader io.Reader, maximum int64, target any) (bool, error) {
	data, truncated, err := ReadBounded(reader, maximum)
	if err != nil || truncated {
		return truncated, err
	}
	return false, json.Unmarshal(data, target)
}

func ReadBoundedText(reader io.Reader, maximum int64) (string, bool, error) {
	data, truncated, err := ReadBounded(reader, maximum)
	if err != nil {
		return "", false, err
	}
	return strings.TrimSpace(strings.ToValidUTF8(string(data), "�")), truncated, nil
}

func ReadHTTPStatusError(response *http.Response, maximum int64) error {
	detail, truncated, err := ReadBoundedText(response.Body, maximum)
	switch {
	case err != nil:
		detail = "read error response: " + err.Error()
	case detail == "":
		detail = "empty response"
	case truncated:
		detail += " [truncated]"
	}
	return errors.New(FormatErrorMessage("", maximum, "HTTP %s: %s", response.Status, detail))
}

func FormatErrorMessage(prefix string, maximum int64, format string, arguments ...any) string {
	message := strings.ToValidUTF8(fmt.Sprintf(format, arguments...), "�")
	if prefix != "" {
		message = prefix + ": " + message
	}
	return TruncateUTF8(message, int(maximum))
}

func TruncateUTF8(text string, maximum int) string {
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
