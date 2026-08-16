package oauth

import (
	"bufio"
	"context"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"sync"
	"testing"
)

func TestBrowserCallbackRejectsWrongStateThenAcceptsValidState(t *testing.T) {
	access := testJWT(t, "account", "state")
	server := newTestServer(t, http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		writeTestJSON(t, writer, map[string]any{"access_token": access, "refresh_token": "refresh", "expires_in": 3600})
	}))
	defer server.Close()

	callbackListener := newPipeListener()
	manager := NewManager(filepath.Join(t.TempDir(), "auth.json"), Options{HTTPClient: server.Client(), AuthBaseURL: server.URL, CallbackAddress: "127.0.0.1:0"})
	manager.listen = func(string, string) (net.Listener, error) { return callbackListener, nil }
	err := manager.LoginBrowser(context.Background(), func(raw string) error {
		parsed, _ := url.Parse(raw)
		redirect := parsed.Query().Get("redirect_uri")
		wrong, err := requestCallback(callbackListener, redirect+"?code=secret-code&state=wrong")
		if err != nil {
			return err
		}
		wrong.Body.Close()
		if wrong.StatusCode != http.StatusBadRequest {
			t.Errorf("wrong-state status = %d", wrong.StatusCode)
		}
		valid, err := requestCallback(callbackListener, redirect+"?code=valid-code&state="+url.QueryEscape(parsed.Query().Get("state")))
		if err != nil {
			return err
		}
		valid.Body.Close()
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}

func TestBrowserCallbackReportsMissingCode(t *testing.T) {
	result := make(chan callbackResult, 1)
	request := httptest.NewRequest(http.MethodGet, browserCallbackPath+"?state=expected", nil)
	writer := httptest.NewRecorder()

	callbackHandler("expected", result).ServeHTTP(writer, request)
	if writer.Code != http.StatusBadRequest {
		t.Fatalf("callback status = %d", writer.Code)
	}
	select {
	case callback := <-result:
		if callback.err == nil {
			t.Fatalf("callback result = %+v", callback)
		}
	default:
		t.Fatal("missing-code callback did not finish the login")
	}
}

type pipeListener struct {
	connections chan net.Conn
	closed      chan struct{}
	closeOnce   sync.Once
}

func newPipeListener() *pipeListener {
	return &pipeListener{
		connections: make(chan net.Conn, 1),
		closed:      make(chan struct{}),
	}
}

func (listener *pipeListener) Accept() (net.Conn, error) {
	select {
	case connection := <-listener.connections:
		return connection, nil
	case <-listener.closed:
		return nil, net.ErrClosed
	}
}

func (listener *pipeListener) Close() error {
	listener.closeOnce.Do(func() { close(listener.closed) })
	return nil
}

func (*pipeListener) Addr() net.Addr {
	return &net.TCPAddr{IP: net.ParseIP("127.0.0.1"), Port: 1455}
}

func requestCallback(listener *pipeListener, rawURL string) (*http.Response, error) {
	serverConnection, clientConnection := net.Pipe()
	select {
	case listener.connections <- serverConnection:
	case <-listener.closed:
		serverConnection.Close()
		clientConnection.Close()
		return nil, net.ErrClosed
	}

	request, err := http.NewRequest(http.MethodGet, rawURL, nil)
	if err != nil {
		serverConnection.Close()
		clientConnection.Close()
		return nil, err
	}
	request.Close = true
	if err := request.Write(clientConnection); err != nil {
		clientConnection.Close()
		return nil, err
	}

	response, err := http.ReadResponse(bufio.NewReader(clientConnection), request)
	if err != nil {
		clientConnection.Close()
		return nil, err
	}
	return response, nil
}
