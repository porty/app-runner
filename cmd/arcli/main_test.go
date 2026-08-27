package main

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
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

// twirpServer serves canned JSON responses keyed by Twirp method name.
func twirpServer(t *testing.T, responses map[string]string) *httptest.Server {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		method := request.URL.Path[strings.LastIndex(request.URL.Path, "/")+1:]
		body, ok := responses[method]
		if !ok {
			t.Errorf("unexpected method %q", method)
			http.Error(response, `{"code":"unimplemented","msg":"unexpected"}`, http.StatusNotFound)
			return
		}
		response.Header().Set("Content-Type", "application/json")
		_, _ = response.Write([]byte(body))
	}))
	t.Cleanup(server.Close)
	return server
}

const vmListJSON = `{"virtual_machines":[{"id":"vm-1","name":"example","nics":[{"id":"n0","mode":"NETWORK_MODE_BRIDGE","bridge_name":"br0","mac_address":"52:54:00:00:00:01"}]},{"id":"vm-2","name":"nat-vm","nics":[{"id":"n0","mode":"NETWORK_MODE_NAT","mac_address":"52:54:00:00:00:02"}]}]}`

func TestVMGetResolvesName(t *testing.T) {
	server := twirpServer(t, map[string]string{
		"ListVMs": vmListJSON,
		"GetVM":   `{"virtual_machine":{"id":"vm-1","name":"example"}}`,
	})
	var output bytes.Buffer
	if err := run([]string{"--server", server.URL, "-o", "json", "vm", "get", "EXAMPLE"}, &output); err != nil {
		t.Fatal(err)
	}
	if got, want := output.String(), "{\"id\":\"vm-1\",\"name\":\"example\"}\n"; got != want {
		t.Errorf("output = %q, want %q", got, want)
	}
	if err := run([]string{"--server", server.URL, "vm", "get", "missing"}, &output); err == nil || !strings.Contains(err.Error(), `"missing"`) {
		t.Errorf("err = %v, want unknown VM error", err)
	}
}

func TestGlobalFlagsAfterCommand(t *testing.T) {
	server := vmListServer(t)
	var output bytes.Buffer
	if err := run([]string{"vm", "list", "--server", server.URL, "-o=json"}, &output); err != nil {
		t.Fatal(err)
	}
	if got, want := output.String(), "[{\"id\":\"vm-1\",\"name\":\"example\"}]\n"; got != want {
		t.Errorf("output = %q, want %q", got, want)
	}
}

func TestVMAddressWaitsForLease(t *testing.T) {
	addressPollInterval = time.Millisecond
	var calls int
	server := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		response.Header().Set("Content-Type", "application/json")
		switch request.URL.Path {
		case "/twirp/apprunner.v1.AppRunnerService/ListVMs":
			_, _ = response.Write([]byte(vmListJSON))
		case "/twirp/apprunner.v1.AppRunnerService/GetNetworkingStatus":
			calls++
			leases := `[]`
			if calls >= 3 {
				leases = `[{"hardware_address":"52:54:00:00:00:01","address":"192.168.100.50","vm_name":"example"}]`
			}
			_, _ = response.Write([]byte(`{"status":{"bridges":[{"name":"br0","dhcp":{"leases":` + leases + `}}]}}`))
		default:
			t.Errorf("unexpected path %q", request.URL.Path)
		}
	}))
	t.Cleanup(server.Close)
	var output bytes.Buffer
	if err := run([]string{"--server", server.URL, "-o", "json", "vm", "address", "--wait", "5s", "example"}, &output); err != nil {
		t.Fatal(err)
	}
	if got, want := output.String(), "[{\"hardware_address\":\"52:54:00:00:00:01\",\"address\":\"192.168.100.50\",\"vm_name\":\"example\"}]\n"; got != want {
		t.Errorf("output = %q, want %q", got, want)
	}
	if calls < 3 {
		t.Errorf("networking status polled %d times, want at least 3", calls)
	}
	output.Reset()
	calls = 0
	if err := run([]string{"--server", server.URL, "vm", "address", "example"}, &output); err == nil || !strings.Contains(err.Error(), "no DHCP lease") {
		t.Errorf("err = %v, want missing lease error without --wait", err)
	}
	if err := run([]string{"--server", server.URL, "vm", "address", "nat-vm"}, &output); err == nil || !strings.Contains(err.Error(), "no bridged") {
		t.Errorf("err = %v, want no bridged interface error", err)
	}
}

func TestNetLeases(t *testing.T) {
	server := twirpServer(t, map[string]string{
		"GetNetworkingStatus": `{"status":{"bridges":[{"name":"br0","dhcp":{"leases":[{"hardware_address":"52:54:00:00:00:01","address":"192.168.100.50","vm_name":"example"}]}}]}}`,
	})
	var output bytes.Buffer
	if err := run([]string{"--server", server.URL, "net", "leases", "br0"}, &output); err != nil {
		t.Fatal(err)
	}
	if got, want := output.String(), "HARDWARE_ADDRESS   ADDRESS         VM_NAME\n52:54:00:00:00:01  192.168.100.50  example\n"; got != want {
		t.Errorf("output = %q, want %q", got, want)
	}
	if err := run([]string{"--server", server.URL, "net", "leases", "missing"}, &output); err == nil {
		t.Error("expected an error for an unknown bridge")
	}
}

func TestVMCreateFromTemplateAndStart(t *testing.T) {
	var created struct {
		Name              string `json:"name"`
		CloudInitEnabled  bool   `json:"cloud_init_enabled"`
		CloudInitUserData string `json:"cloud_init_user_data"`
		Disks             []struct {
			SizeGib     uint32 `json:"size_gib"`
			SourceImage string `json:"source_image"`
		} `json:"disks"`
		Nics []struct {
			Mode       string `json:"mode"`
			BridgeName string `json:"bridge_name"`
		} `json:"nics"`
	}
	started := false
	server := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		response.Header().Set("Content-Type", "application/json")
		switch request.URL.Path {
		case "/twirp/apprunner.v1.AppRunnerService/ListCloudInitTemplates":
			_, _ = response.Write([]byte(`{"templates":[{"name":"base","user_data":"#cloud-config\nhostname: x\n"}]}`))
		case "/twirp/apprunner.v1.AppRunnerService/CreateVM":
			if err := json.NewDecoder(request.Body).Decode(&created); err != nil {
				t.Error(err)
			}
			_, _ = response.Write([]byte(`{"virtual_machine":{"id":"vm-9","name":"node","status":"VM_STATUS_STOPPED"}}`))
		case "/twirp/apprunner.v1.AppRunnerService/StartVM":
			started = true
			_, _ = response.Write([]byte(`{"virtual_machine":{"id":"vm-9","name":"node","status":"VM_STATUS_RUNNING"}}`))
		default:
			t.Errorf("unexpected path %q", request.URL.Path)
		}
	}))
	t.Cleanup(server.Close)
	var output bytes.Buffer
	if err := run([]string{"--server", server.URL, "-o", "json", "vm", "create", "--name", "node", "--disk", "20:ubuntu.img", "--nic", "bridge:br0", "--cloud-init-template", "base", "--start"}, &output); err != nil {
		t.Fatal(err)
	}
	if created.Name != "node" || !created.CloudInitEnabled || created.CloudInitUserData != "#cloud-config\nhostname: x\n" {
		t.Errorf("create request = %+v", created)
	}
	if len(created.Disks) != 1 || created.Disks[0].SizeGib != 20 || created.Disks[0].SourceImage != "ubuntu.img" {
		t.Errorf("create request disks = %+v", created.Disks)
	}
	if len(created.Nics) != 1 || created.Nics[0].Mode != "NETWORK_MODE_BRIDGE" || created.Nics[0].BridgeName != "br0" {
		t.Errorf("create request nics = %+v", created.Nics)
	}
	if !started {
		t.Error("expected the VM to be started")
	}
	if got, want := output.String(), "{\"id\":\"vm-9\",\"name\":\"node\",\"status\":\"VM_STATUS_RUNNING\"}\n"; got != want {
		t.Errorf("output = %q, want %q", got, want)
	}
	if err := run([]string{"--server", server.URL, "vm", "create", "--name", "node", "--cloud-init-template", "base", "--cloud-init-file", "x"}, &output); err == nil {
		t.Error("expected an error when both cloud-init sources are given")
	}
}

func TestSubcommandFlagsAfterPositionalArguments(t *testing.T) {
	var request struct {
		ID    string `json:"id"`
		Force bool   `json:"force"`
	}
	server := twirpServer(t, map[string]string{"ListVMs": vmListJSON})
	stop := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/ListVMs") {
			response.Header().Set("Content-Type", "application/json")
			_, _ = response.Write([]byte(vmListJSON))
			return
		}
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			t.Error(err)
		}
		response.Header().Set("Content-Type", "application/json")
		_, _ = response.Write([]byte(`{"virtual_machine":{"id":"vm-1","name":"example","status":"VM_STATUS_STOPPING"}}`))
	}))
	t.Cleanup(stop.Close)
	server.Close()
	var output bytes.Buffer
	if err := run([]string{"--server", stop.URL, "vm", "stop", "example", "--force"}, &output); err != nil {
		t.Fatal(err)
	}
	if request.ID != "vm-1" || !request.Force {
		t.Errorf("request = %+v, want id vm-1 with force", request)
	}
	if got, want := output.String(), "id      vm-1\nname    example\nstatus  VM_STATUS_STOPPING\n"; got != want {
		t.Errorf("output = %q, want %q", got, want)
	}
}

func TestVMUpdateKeepsUnspecifiedFields(t *testing.T) {
	var request struct {
		ID          string `json:"id"`
		Name        string `json:"name"`
		Description string `json:"description"`
		Cpus        uint32 `json:"cpus"`
		MemoryMib   uint32 `json:"memory_mib"`
	}
	server := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, r *http.Request) {
		response.Header().Set("Content-Type", "application/json")
		if strings.HasSuffix(r.URL.Path, "/ListVMs") {
			_, _ = response.Write([]byte(`{"virtual_machines":[{"id":"vm-1","name":"example","description":"lab","cpus":4,"memory_mib":6144}]}`))
			return
		}
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			t.Error(err)
		}
		_, _ = response.Write([]byte(`{"virtual_machine":{"id":"vm-1","name":"example","cpus":4,"memory_mib":8192}}`))
	}))
	t.Cleanup(server.Close)
	var output bytes.Buffer
	if err := run([]string{"--server", server.URL, "-o", "json", "vm", "update", "example", "--memory", "8192"}, &output); err != nil {
		t.Fatal(err)
	}
	if request.ID != "vm-1" || request.Name != "example" || request.Description != "lab" || request.Cpus != 4 || request.MemoryMib != 8192 {
		t.Errorf("request = %+v, want current values with memory 8192", request)
	}
}
