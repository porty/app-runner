package main

import (
	"io/fs"
	"net/http"
	"net/http/httptest"
	"testing"
	"testing/fstest"
)

func TestSPAHandlerServesRouteFallback(t *testing.T) {
	frontend := fstest.MapFS{
		"index.html":    &fstest.MapFile{Data: []byte("<html>app</html>")},
		"assets/app.js": &fstest.MapFile{Data: []byte("console.log('app')")},
	}
	handler := newHTTPHandler(fs.FS(frontend))

	request := httptest.NewRequest(http.MethodGet, "/instances/virtual-machines", nil)
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)

	if response.Code != http.StatusOK {
		t.Fatalf("expected status 200, got %d", response.Code)
	}
	if response.Body.String() != "<html>app</html>" {
		t.Fatalf("expected SPA index, got %q", response.Body.String())
	}
}

func TestDevelopmentHandlerDescribesFrontendWorkflow(t *testing.T) {
	handler := newHTTPHandler(nil)
	request := httptest.NewRequest(http.MethodGet, "/", nil)
	response := httptest.NewRecorder()

	handler.ServeHTTP(response, request)

	if response.Code != http.StatusOK {
		t.Fatalf("expected status 200, got %d", response.Code)
	}
	if response.Header().Get("Content-Type") != "application/json" {
		t.Fatalf("unexpected content type: %q", response.Header().Get("Content-Type"))
	}
}
