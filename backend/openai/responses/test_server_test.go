package responses

import (
	"net/http"
	"testing"

	"github.com/eul-ai/eul/backend/testhttp"
)

func newTestServer(t *testing.T, handler http.Handler) *testhttp.Server {
	t.Helper()
	return testhttp.NewServer(handler)
}
