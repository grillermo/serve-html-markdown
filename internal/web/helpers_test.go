package web

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"serve-html-markdown/internal/config"
	"serve-html-markdown/internal/db/testdb"
)

// newTestServer builds a Server on the shared test database and a temp FilesDir.
// Tests using it must not call t.Parallel (testdb is shared).
func newTestServer(t *testing.T) *Server {
	t.Helper()
	cfg := config.Config{FilesDir: t.TempDir(), Host: "localhost", APIToken: "test-token"}
	return New(cfg, testdb.New(t))
}

func do(srv http.Handler, method, path string, hdr map[string]string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(method, path, nil)
	for k, v := range hdr {
		req.Header.Set(k, v)
	}
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, req)
	return rec
}
