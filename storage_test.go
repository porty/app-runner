package apprunner

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	apprunnerv1 "github.com/roflware/app-runner/internal/gen/apprunner/v1"
	"github.com/twitchtv/twirp"
)

func testStorageService(t *testing.T) (*storageService, config) {
	t.Helper()
	base := t.TempDir()
	settings := defaultConfig(base)
	if err := prepareDirectories(settings); err != nil {
		t.Fatalf("prepare storage directories: %v", err)
	}
	return newStorageService(settings), settings
}

func TestStorageListIncludesRootsAndNestedDirectories(t *testing.T) {
	service, settings := testStorageService(t)
	images := filepath.Join(settings.ISODir, "linux")
	if err := os.Mkdir(images, 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(images, "installer.iso"), []byte("image"), 0o640); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(settings.DiskDir, "vm.qcow2"), []byte("disk"), 0o640); err != nil {
		t.Fatal(err)
	}

	rpc := newAppRunnerService(appRuntime{storage: service})
	response, err := rpc.ListStorage(context.Background(), &apprunnerv1.ListStorageRequest{})
	if err != nil {
		t.Fatalf("list storage: %v", err)
	}
	if len(response.Roots) != 2 || response.Roots[0].Name != "iso" || response.Roots[1].Name != "disk" {
		t.Fatalf("unexpected roots: %#v", response.Roots)
	}
	if len(response.Roots[0].Entries) != 1 || response.Roots[0].Entries[0].Kind != apprunnerv1.StorageEntryKind_STORAGE_ENTRY_KIND_DIRECTORY {
		t.Fatalf("expected nested ISO directory, got %#v", response.Roots[0].Entries)
	}
	children := response.Roots[0].Entries[0].Children
	if len(children) != 1 || children[0].Path != "linux/installer.iso" || children[0].SizeBytes != 5 {
		t.Fatalf("unexpected nested ISO entry: %#v", children)
	}
}

func TestStorageListUsesTwirpHTTPRoute(t *testing.T) {
	storage, settings := testStorageService(t)
	if err := os.WriteFile(filepath.Join(settings.ISODir, "installer.iso"), []byte("image"), 0o640); err != nil {
		t.Fatal(err)
	}
	handler := newHTTPHandler(nil, appRuntime{storage: storage})
	request := httptest.NewRequest(http.MethodPost, "/twirp/apprunner.v1.AppRunnerService/ListStorage", bytes.NewBufferString("{}"))
	request.Header.Set("Content-Type", "application/json")
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusOK {
		t.Fatalf("expected status 200, got %d: %s", response.Code, response.Body.String())
	}
	var body struct {
		Roots []struct {
			Name    string `json:"name"`
			Entries []struct {
				Kind      string `json:"kind"`
				SizeBytes string `json:"size_bytes"`
			} `json:"entries"`
		} `json:"roots"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if len(body.Roots) != 2 || body.Roots[0].Name != "iso" || len(body.Roots[0].Entries) != 1 {
		t.Fatalf("unexpected Twirp storage response: %s", response.Body.String())
	}
	entry := body.Roots[0].Entries[0]
	if entry.Kind != "STORAGE_ENTRY_KIND_FILE" || entry.SizeBytes != "5" {
		t.Fatalf("unexpected Twirp entry encoding: %#v", entry)
	}
}

func TestStorageMultipartUploadToSubdirectory(t *testing.T) {
	service, settings := testStorageService(t)
	if err := os.Mkdir(filepath.Join(settings.ISODir, "linux"), 0o750); err != nil {
		t.Fatal(err)
	}
	newUploadRequest := func() *http.Request {
		var body bytes.Buffer
		writer := multipart.NewWriter(&body)
		_ = writer.WriteField("root", "iso")
		_ = writer.WriteField("directory", "linux")
		part, err := writer.CreateFormFile("files", "installer.iso")
		if err != nil {
			t.Fatal(err)
		}
		_, _ = part.Write([]byte("image data"))
		_ = writer.Close()
		request := httptest.NewRequest(http.MethodPost, "/api/storage/upload", &body)
		request.Header.Set("Content-Type", writer.FormDataContentType())
		return request
	}

	request := newUploadRequest()
	response := httptest.NewRecorder()
	service.ServeHTTP(response, request)
	if response.Code != http.StatusCreated {
		t.Fatalf("expected status 201, got %d: %s", response.Code, response.Body.String())
	}
	data, err := os.ReadFile(filepath.Join(settings.ISODir, "linux", "installer.iso"))
	if err != nil || string(data) != "image data" {
		t.Fatalf("unexpected uploaded file: %q, %v", data, err)
	}

	conflict := httptest.NewRecorder()
	service.ServeHTTP(conflict, newUploadRequest())
	if conflict.Code != http.StatusConflict {
		t.Fatalf("expected existing file conflict, got %d: %s", conflict.Code, conflict.Body.String())
	}
}

func TestStorageDownloadsHTTPURL(t *testing.T) {
	service, settings := testStorageService(t)
	rpc := newAppRunnerService(appRuntime{storage: service})
	source := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, _ *http.Request) {
		response.Header().Set("Content-Disposition", `attachment; filename="remote.iso"`)
		_, _ = response.Write([]byte("remote image"))
	}))
	defer source.Close()

	response, err := rpc.DownloadStorageFile(context.Background(), &apprunnerv1.DownloadStorageFileRequest{
		Root: "iso", Url: source.URL + "/download",
	})
	if err != nil {
		t.Fatalf("download storage file: %v", err)
	}
	if response.File.GetName() != "remote.iso" {
		t.Fatalf("unexpected downloaded entry: %#v", response.File)
	}
	data, err := os.ReadFile(filepath.Join(settings.ISODir, "remote.iso"))
	if err != nil || string(data) != "remote image" {
		t.Fatalf("unexpected downloaded file: %q, %v", data, err)
	}
}

func TestStorageRejectsDestinationsOutsideRoot(t *testing.T) {
	service, _ := testStorageService(t)
	rpc := newAppRunnerService(appRuntime{storage: service})
	_, err := rpc.DownloadStorageFile(context.Background(), &apprunnerv1.DownloadStorageFileRequest{
		Root: "iso", Directory: "../disk", Url: "https://example.com/image.iso",
	})
	var twirpError twirp.Error
	if !errors.As(err, &twirpError) || twirpError.Code() != twirp.InvalidArgument {
		t.Fatalf("expected invalid_argument, got %v", err)
	}
}
