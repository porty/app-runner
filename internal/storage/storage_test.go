package storage

import (
	"bytes"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"github.com/roflware/app-runner/internal/config"
)

func testStorageService(t *testing.T) (*Service, config.Config) {
	t.Helper()
	base := t.TempDir()
	settings := config.Default(base)
	if err := config.PrepareDirectories(settings); err != nil {
		t.Fatalf("prepare storage directories: %v", err)
	}
	return NewService(settings), settings
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
