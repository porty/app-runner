package main

import (
	"context"
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

func TestClientAddressContextAllowsOnlyLoopback(t *testing.T) {
	var loopback bool
	handler := withClientAddress(http.HandlerFunc(func(_ http.ResponseWriter, request *http.Request) {
		loopback = isLoopbackRequest(request.Context())
	}))

	request := httptest.NewRequest(http.MethodPost, "/", nil)
	request.RemoteAddr = "127.0.0.1:12345"
	handler.ServeHTTP(httptest.NewRecorder(), request)
	if !loopback {
		t.Fatal("loopback request was not identified")
	}

	loopback = true
	request = request.WithContext(context.Background())
	request.RemoteAddr = "192.0.2.10:12345"
	handler.ServeHTTP(httptest.NewRecorder(), request)
	if loopback {
		t.Fatal("remote request was identified as loopback")
	}

	loopback = true
	request.RemoteAddr = "127.0.0.1:12345"
	request.Header.Set("X-Forwarded-For", "192.0.2.10")
	handler.ServeHTTP(httptest.NewRecorder(), request)
	if loopback {
		t.Fatal("remote request forwarded by the local development proxy was identified as loopback")
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
