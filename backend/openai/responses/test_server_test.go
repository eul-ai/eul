package responses

import (
	"io"
	"net/http"
	"net/http/httptest"
)

func handlerClient(handler http.Handler) *http.Client {
	return &http.Client{Transport: roundTripperFunc(func(request *http.Request) (*http.Response, error) {
		recorder := httptest.NewRecorder()
		handler.ServeHTTP(recorder, request)
		return recorder.Result(), nil
	})}
}

func streamingClient(stream func(*http.Request, *io.PipeWriter)) *http.Client {
	return &http.Client{Transport: roundTripperFunc(func(request *http.Request) (*http.Response, error) {
		reader, writer := io.Pipe()
		go stream(request, writer)
		return &http.Response{
			StatusCode: http.StatusOK,
			Status:     "200 OK",
			Header:     http.Header{"Content-Type": {"text/event-stream"}},
			Body:       reader,
		}, nil
	})}
}
