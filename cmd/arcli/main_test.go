package main

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestVMListUsesJSONTwirpAPI(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		if request.URL.Path != "/twirp/apprunner.v1.AppRunnerService/ListVMs" {
			t.Fatalf("path = %q", request.URL.Path)
		}
		if request.Method != http.MethodPost {
			t.Fatalf("method = %q", request.Method)
		}
		response.Header().Set("Content-Type", "application/json")
		_, _ = response.Write([]byte(`{"virtual_machines":[{"id":"vm-1","name":"example"}]}`))
	}))
	defer server.Close()

	var output bytes.Buffer
	if err := run([]string{"--server", server.URL, "vm", "list"}, &output); err != nil {
		t.Fatal(err)
	}
	if got, want := output.String(), "[{\"id\":\"vm-1\",\"name\":\"example\"}]\n"; got != want {
		t.Errorf("output = %q, want %q", got, want)
	}
}
