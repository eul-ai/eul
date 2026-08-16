package httpclient

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math/rand/v2"
	"net"
	"net/http"
	"strconv"
	"strings"
	"syscall"
	"time"
	"unicode/utf8"
)

func New(source *http.Client, timeout time.Duration) *http.Client {
	client := &http.Client{}
	if source != nil {
		*client = *source
	}
	if timeout > 0 && client.Timeout <= 0 {
		client.Timeout = timeout
	}
	client.CheckRedirect = func(*http.Request, []*http.Request) error {
		return http.ErrUseLastResponse
	}
	return client
}

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

func ParseRetryAfter(value string, now time.Time) time.Duration {
	value = strings.TrimSpace(value)
	if value == "" {
		return 0
	}
	if seconds, err := strconv.ParseInt(value, 10, 64); err == nil {
		if seconds <= 0 {
			return 0
		}
		maximumSeconds := int64(time.Duration(1<<63-1) / time.Second)
		if seconds > maximumSeconds {
			return time.Duration(1<<63 - 1)
		}
		return time.Duration(seconds) * time.Second
	}

	when, err := http.ParseTime(value)
	if err != nil || !when.After(now) {
		return 0
	}
	return when.Sub(now)
}

func RetryableHTTPStatus(status int) bool {
	return status == http.StatusRequestTimeout ||
		status == http.StatusConflict ||
		status == http.StatusTooManyRequests ||
		status >= http.StatusInternalServerError && status <= 599
}

func RetryDelay(failedAttempts int, base, maximum time.Duration) time.Duration {
	delay := base
	for attempt := 1; attempt < failedAttempts && delay < maximum; attempt++ {
		delay = min(delay*2, maximum)
	}

	quarter := delay / 4
	if quarter == 0 {
		return delay
	}
	delay += time.Duration(rand.Int64N(int64(quarter)*2+1)) - quarter
	return min(delay, maximum)
}

// net/http exposes its internal HTTP/2 stream errors to matching structs through errors.As.
type http2StreamErrorCode uint32

type http2StreamError struct {
	StreamID uint32
	Code     http2StreamErrorCode
	Cause    error
}

func (http2StreamError) Error() string { return "HTTP/2 stream error" }

func RetryableNetworkError(err error) bool {
	const http2InternalError http2StreamErrorCode = 2

	var streamErr http2StreamError
	if errors.As(err, &streamErr) && streamErr.Code == http2InternalError {
		return true
	}

	for _, target := range []error{
		io.EOF,
		io.ErrUnexpectedEOF,
		syscall.ECONNABORTED,
		syscall.ECONNREFUSED,
		syscall.ECONNRESET,
		syscall.EPIPE,
		syscall.ETIMEDOUT,
	} {
		if errors.Is(err, target) {
			return true
		}
	}

	var networkErr net.Error
	return errors.As(err, &networkErr) && networkErr.Timeout()
}
