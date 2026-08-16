package oauth

import (
	"context"
	"crypto/subtle"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"strconv"
	"time"
)

type loopbackCallback struct {
	redirectURI string
	result      <-chan callbackResult
	serveResult <-chan error
	serveDone   <-chan struct{}
	server      *http.Server
}

func (m *Manager) startLoopbackCallback(expectedState string) (*loopbackCallback, error) {
	listener, err := m.listen("tcp", m.callbackAddress)
	if err != nil {
		return nil, fmt.Errorf("oauth: start loopback callback: %w", err)
	}

	port := listener.Addr().(*net.TCPAddr).Port
	result := make(chan callbackResult, 1)
	server := &http.Server{ReadHeaderTimeout: 10 * time.Second, Handler: callbackHandler(expectedState, result)}
	serveResult := make(chan error, 1)
	serveDone := make(chan struct{})
	go func() {
		defer close(serveDone)
		serveResult <- server.Serve(listener)
	}()

	return &loopbackCallback{
		redirectURI: "http://localhost:" + strconv.Itoa(port) + browserCallbackPath,
		result:      result,
		serveResult: serveResult,
		serveDone:   serveDone,
		server:      server,
	}, nil
}

func (callback *loopbackCallback) wait(ctx context.Context) (string, error) {
	select {
	case <-ctx.Done():
		return "", ctx.Err()
	case serveErr := <-callback.serveResult:
		if errors.Is(serveErr, http.ErrServerClosed) {
			return "", errors.New("oauth: loopback callback stopped before authorization completed")
		}
		return "", errors.New("oauth: loopback callback failed")
	case result := <-callback.result:
		return result.code, result.err
	}
}

func (callback *loopbackCallback) stop() {
	shutdownCtx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if err := callback.server.Shutdown(shutdownCtx); err != nil {
		_ = callback.server.Close()
	}
	<-callback.serveDone
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
