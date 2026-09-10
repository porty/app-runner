package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"time"

	"github.com/mappu/miqt/qt6/mainthread"

	apprunnerv1 "github.com/roflware/app-runner/internal/gen/apprunner/v1"
)

// client talks to an App Runner server. api applies the request timeout;
// slow has none, for operations such as URL downloads that the server
// completes before replying.
type client struct {
	server string
	http   *http.Client
	api    apprunnerv1.AppRunnerService
	slow   apprunnerv1.AppRunnerService
}

func newClient(server string, timeout time.Duration) *client {
	server = strings.TrimRight(server, "/")
	return &client{
		server: server,
		http:   &http.Client{},
		api:    apprunnerv1.NewAppRunnerServiceJSONClient(server, &http.Client{Timeout: timeout}),
		slow:   apprunnerv1.NewAppRunnerServiceJSONClient(server, &http.Client{}),
	}
}

// hostLabel is the server as shown in the status bar.
func (c *client) hostLabel() string {
	parsed, err := url.Parse(c.server)
	if err != nil || parsed.Host == "" {
		return c.server
	}
	return parsed.Host
}

// consoleURL is the absolute address of a server console path such as
// /console/<id>.
func (c *client) consoleURL(path string) string {
	return c.server + path
}

// call runs fn on a goroutine and delivers its result on the Qt main
// thread, where done may touch widgets.
func call[T any](fn func(ctx context.Context) (T, error), done func(T, error)) {
	go func() {
		result, err := fn(context.Background())
		mainthread.Start(func() { done(result, err) })
	}()
}

// uploadedFile is the storage service's description of a saved upload.
type uploadedFile struct {
	Name      string `json:"name"`
	Path      string `json:"path"`
	SizeBytes int64  `json:"size_bytes"`
}

// upload streams local files to /api/storage/upload as a multipart form,
// reporting progress as bytes are sent.
func (c *client) upload(ctx context.Context, root, directory string, paths []string, progress func(sent, total int64)) ([]uploadedFile, error) {
	var total int64
	for _, path := range paths {
		info, err := os.Stat(path)
		if err != nil {
			return nil, err
		}
		total += info.Size()
	}
	reader, writer := io.Pipe()
	form := multipart.NewWriter(writer)
	var sent atomic.Int64
	go func() {
		err := func() error {
			if err := form.WriteField("root", root); err != nil {
				return err
			}
			if err := form.WriteField("directory", directory); err != nil {
				return err
			}
			for _, path := range paths {
				file, err := os.Open(path)
				if err != nil {
					return err
				}
				part, err := form.CreateFormFile("files", filepath.Base(path))
				if err != nil {
					file.Close()
					return err
				}
				_, err = io.Copy(part, &countingReader{reader: file, count: func(n int) {
					if progress != nil {
						progress(sent.Add(int64(n)), total)
					}
				}})
				file.Close()
				if err != nil {
					return err
				}
			}
			return form.Close()
		}()
		writer.CloseWithError(err)
	}()
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, c.server+"/api/storage/upload", reader)
	if err != nil {
		return nil, err
	}
	request.Header.Set("Content-Type", form.FormDataContentType())
	response, err := c.http.Do(request)
	if err != nil {
		return nil, err
	}
	defer response.Body.Close()
	body, err := io.ReadAll(io.LimitReader(response.Body, 1<<20))
	if err != nil {
		return nil, err
	}
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		var failure struct {
			Error string `json:"error"`
		}
		if json.Unmarshal(body, &failure) == nil && failure.Error != "" {
			return nil, errors.New(failure.Error)
		}
		return nil, fmt.Errorf("upload failed: %s", response.Status)
	}
	var result struct {
		Uploaded []uploadedFile `json:"uploaded"`
	}
	if err := json.Unmarshal(body, &result); err != nil {
		return nil, fmt.Errorf("unexpected upload response: %w", err)
	}
	return result.Uploaded, nil
}

type countingReader struct {
	reader io.Reader
	count  func(int)
}

func (r *countingReader) Read(p []byte) (int, error) {
	n, err := r.reader.Read(p)
	if n > 0 {
		r.count(n)
	}
	return n, err
}
