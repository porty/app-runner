package main

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestConsoleProxyRejectsInactiveVM(t *testing.T) {
	manager, _, _ := newTestVMManager(t)
	handler := consoleProxyHandler(manager)
	request := httptest.NewRequest(http.MethodGet, "/console/missing", nil)
	response := httptest.NewRecorder()

	handler.ServeHTTP(response, request)

	if response.Code != http.StatusConflict {
		t.Fatalf("expected status %d, got %d", http.StatusConflict, response.Code)
	}
}
