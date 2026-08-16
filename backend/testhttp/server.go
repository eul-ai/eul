package testhttp

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"sync"
)

const serverURL = "eul-test-http://server"

type Server struct {
	URL     string
	handler http.Handler
	mu      sync.RWMutex
	closed  bool
}

func NewServer(handler http.Handler) *Server {
	return &Server{URL: serverURL, handler: handler}
}

func (server *Server) Client() *http.Client {
	return &http.Client{Transport: &transport{server: server}}
}

func (server *Server) Close() {
	server.mu.Lock()
	server.closed = true
	server.mu.Unlock()
}

func (server *Server) loadHandler() (http.Handler, bool) {
	server.mu.RLock()
	defer server.mu.RUnlock()
	return server.handler, server.closed
}

type transport struct {
	server *Server
}

func (transport *transport) RoundTrip(request *http.Request) (*http.Response, error) {
	handler, closed := transport.server.loadHandler()
	if closed {
		return nil, errClosedServer
	}

	reader, writer := io.Pipe()
	responseWriter := &pipeResponseWriter{
		header: make(http.Header),
		writer: writer,
		ready:  make(chan struct{}),
	}
	done := make(chan struct{})

	handlerRequest := request.Clone(request.Context())
	handlerRequest.Proto = "HTTP/1.1"
	handlerRequest.ProtoMajor = 1
	handlerRequest.ProtoMinor = 1
	go func() {
		defer close(done)
		defer func() {
			if recover() != nil {
				responseWriter.WriteHeader(http.StatusInternalServerError)
				_ = writer.CloseWithError(io.ErrUnexpectedEOF)
				return
			}
			responseWriter.WriteHeader(http.StatusOK)
			if err := handlerRequest.Context().Err(); err != nil {
				_ = writer.CloseWithError(err)
				return
			}
			_ = writer.Close()
		}()
		handler.ServeHTTP(responseWriter, handlerRequest)
	}()

	select {
	case <-responseWriter.ready:
		status, header := responseWriter.response()
		go func() {
			select {
			case <-request.Context().Done():
				_ = reader.CloseWithError(request.Context().Err())
			case <-done:
			}
		}()
		return &http.Response{
			StatusCode: status,
			Status:     fmt.Sprintf("%d %s", status, http.StatusText(status)),
			Header:     header,
			Body:       &contextBody{reader: reader, ctx: request.Context()},
			Request:    request,
		}, nil
	case <-request.Context().Done():
		_ = reader.CloseWithError(request.Context().Err())
		_ = writer.CloseWithError(request.Context().Err())
		return nil, request.Context().Err()
	}
}

var errClosedServer = errors.New("test HTTP server is closed")

type contextBody struct {
	reader *io.PipeReader
	ctx    context.Context
}

func (body *contextBody) Read(data []byte) (int, error) {
	read, err := body.reader.Read(data)
	if err != nil && body.ctx.Err() != nil {
		return read, body.ctx.Err()
	}
	return read, err
}

func (body *contextBody) Close() error {
	return body.reader.Close()
}

type pipeResponseWriter struct {
	header         http.Header
	responseHeader http.Header
	writer         *io.PipeWriter
	ready          chan struct{}
	once           sync.Once
	mu             sync.Mutex
	status         int
}

func (writer *pipeResponseWriter) Header() http.Header {
	return writer.header
}

func (writer *pipeResponseWriter) WriteHeader(status int) {
	writer.mu.Lock()
	if writer.status == 0 {
		writer.status = status
		writer.responseHeader = writer.header.Clone()
		writer.once.Do(func() { close(writer.ready) })
	}
	writer.mu.Unlock()
}

func (writer *pipeResponseWriter) Write(data []byte) (int, error) {
	writer.WriteHeader(http.StatusOK)
	return writer.writer.Write(data)
}

func (writer *pipeResponseWriter) Flush() {
	writer.WriteHeader(http.StatusOK)
}

func (writer *pipeResponseWriter) response() (int, http.Header) {
	writer.mu.Lock()
	defer writer.mu.Unlock()
	return writer.status, writer.responseHeader
}
