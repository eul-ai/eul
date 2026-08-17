package httpclient

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
)

func NewJSONSSERequest(ctx context.Context, endpoint string, body []byte) (*http.Request, error) {
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	request.Header.Set("Accept", "text/event-stream")
	request.Header.Set("Content-Type", "application/json")
	return request, nil
}

type JSONSSEConfig struct {
	HTTPClient       *http.Client
	Endpoint         string
	ErrorConfig      APIErrorConfig
	PrepareRequest   func(context.Context, *http.Request) error
	MaxRequestBytes  int64
	MaxResponseBytes int64
}

func CompleteJSONSSE[T any](
	ctx context.Context,
	config JSONSSEConfig,
	wireRequest any,
	operation string,
	read func(io.Reader, int64) (T, error),
	nonRetryableReadError func(error) bool,
) (T, error) {
	var zero T

	body, oversized, err := MarshalBoundedJSON(wireRequest, config.MaxRequestBytes)
	if err != nil {
		return zero, config.ErrorConfig.Errorf("encode %s: %v", operation, err)
	}
	if oversized {
		return zero, config.ErrorConfig.Errorf("%s exceeds %d bytes", operation, config.MaxRequestBytes)
	}

	request, err := NewJSONSSERequest(ctx, config.Endpoint, body)
	if err != nil {
		return zero, config.ErrorConfig.Errorf("create %s: %v", operation, err)
	}
	if config.PrepareRequest != nil {
		if err := config.PrepareRequest(ctx, request); err != nil {
			if contextErr := ctx.Err(); contextErr != nil {
				return zero, contextErr
			}
			return zero, err
		}
	}

	response, err := config.ErrorConfig.Do(ctx, config.HTTPClient, request, operation)
	if err != nil {
		return zero, err
	}
	defer response.Body.Close()

	result, err := read(response.Body, config.MaxResponseBytes)
	if err == nil {
		return result, nil
	}
	if nonRetryableReadError != nil && nonRetryableReadError(err) {
		return zero, config.ErrorConfig.Wrapf(err, "%v", err)
	}

	classified := ClassifyTransportError(ctx, err)
	if classified.ReturnDirectly {
		return zero, classified.Cause
	}
	if classified.Retryable {
		return zero, config.ErrorConfig.RetryableWrapf(err, "%v", err)
	}
	return zero, config.ErrorConfig.Wrapf(classified.Cause, "%v", err)
}

func ReadSSE(reader io.Reader, maximum int64, handle func([]byte) (bool, error)) (bool, error) {
	limited := &io.LimitedReader{R: reader, N: maximum + 1}
	lines := sseLineReader{reader: bufio.NewReader(limited)}
	var dataLines [][]byte

	flush := func() (bool, error) {
		if len(dataLines) == 0 {
			return false, nil
		}
		data := bytes.Join(dataLines, []byte("\n"))
		dataLines = nil
		if len(data) == 0 {
			return false, nil
		}
		return handle(data)
	}

	for {
		line, err := lines.read()
		if err != nil && !errors.Is(err, io.EOF) {
			return false, err
		}
		if limited.N == 0 {
			return false, fmt.Errorf("SSE response exceeds %d bytes", maximum)
		}

		switch {
		case len(line) == 0:
			done, handleErr := flush()
			if handleErr != nil || done {
				return done, handleErr
			}
		case bytes.HasPrefix(line, []byte("data:")):
			data := line[len("data:"):]
			if len(data) > 0 && data[0] == ' ' {
				data = data[1:]
			}
			dataLines = append(dataLines, data)
		}

		if errors.Is(err, io.EOF) {
			return flush()
		}
	}
}

type sseLineReader struct {
	reader *bufio.Reader
	skipLF bool
}

func (lines *sseLineReader) read() ([]byte, error) {
	var line []byte
	for {
		value, err := lines.reader.ReadByte()
		if err != nil {
			return line, err
		}
		if lines.skipLF {
			lines.skipLF = false
			if value == '\n' {
				continue
			}
		}

		switch value {
		case '\n':
			return line, nil
		case '\r':
			lines.skipLF = true
			return line, nil
		default:
			line = append(line, value)
		}
	}
}
