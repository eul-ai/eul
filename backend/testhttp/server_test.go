package testhttp

import (
	"io"
	"net/http"
	"testing"
)

func TestServerSnapshotsResponseHeaders(t *testing.T) {
	release := make(chan struct{})
	server := NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		writer.Header().Set("X-Test", "before")
		writer.WriteHeader(http.StatusAccepted)
		writer.Header().Set("X-Test", "after")
		<-release
	}))
	defer server.Close()

	response, err := server.Client().Get(server.URL)
	if err != nil {
		t.Fatal(err)
	}
	if response.StatusCode != http.StatusAccepted || response.Header.Get("X-Test") != "before" {
		t.Fatalf("response = %s, headers=%v", response.Status, response.Header)
	}
	close(release)
	response.Body.Close()
}

func TestServerReportsClosedResponseBody(t *testing.T) {
	firstWritten := make(chan struct{})
	writeAgain := make(chan struct{})
	writeError := make(chan error, 1)
	server := NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(writer, "first")
		close(firstWritten)
		<-writeAgain
		_, err := io.WriteString(writer, "second")
		writeError <- err
	}))
	defer server.Close()

	response, err := server.Client().Get(server.URL)
	if err != nil {
		t.Fatal(err)
	}
	body := make([]byte, len("first"))
	if _, err := io.ReadFull(response.Body, body); err != nil {
		t.Fatal(err)
	}
	<-firstWritten
	response.Body.Close()
	close(writeAgain)
	if err := <-writeError; err == nil {
		t.Fatal("write after response close succeeded")
	}
}
