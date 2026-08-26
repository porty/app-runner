package main

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"testing"
)

func vmListServer(t *testing.T) *httptest.Server {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		if request.URL.Path != "/twirp/apprunner.v1.AppRunnerService/ListVMs" {
			t.Errorf("path = %q", request.URL.Path)
		}
		if request.Method != http.MethodPost {
			t.Errorf("method = %q", request.Method)
		}
		response.Header().Set("Content-Type", "application/json")
		_, _ = response.Write([]byte(`{"virtual_machines":[{"id":"vm-1","name":"example"}]}`))
	}))
	t.Cleanup(server.Close)
	return server
}

func TestVMListUsesJSONTwirpAPI(t *testing.T) {
	server := vmListServer(t)
	var output bytes.Buffer
	if err := run([]string{"--server", server.URL, "-o", "json", "vm", "list"}, &output); err != nil {
		t.Fatal(err)
	}
	if got, want := output.String(), "[{\"id\":\"vm-1\",\"name\":\"example\"}]\n"; got != want {
		t.Errorf("output = %q, want %q", got, want)
	}
}

func TestVMListDefaultsToTable(t *testing.T) {
	server := vmListServer(t)
	var output bytes.Buffer
	if err := run([]string{"--server", server.URL, "vm", "list"}, &output); err != nil {
		t.Fatal(err)
	}
	if got, want := output.String(), "ID    NAME\nvm-1  example\n"; got != want {
		t.Errorf("output = %q, want %q", got, want)
	}
}

func TestVMListYAML(t *testing.T) {
	server := vmListServer(t)
	var output bytes.Buffer
	if err := run([]string{"--server", server.URL, "-o", "yaml", "vm", "list"}, &output); err != nil {
		t.Fatal(err)
	}
	if got, want := output.String(), "- id: vm-1\n  name: example\n"; got != want {
		t.Errorf("output = %q, want %q", got, want)
	}
}

func TestUnknownOutputFormat(t *testing.T) {
	var output bytes.Buffer
	if err := run([]string{"-o", "xml", "vm", "list"}, &output); err == nil {
		t.Fatal("expected an error for -o xml")
	}
}
