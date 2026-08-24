package main

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"

	apprunnerv1 "github.com/roflware/app-runner/internal/gen/apprunner/v1"
	"github.com/twitchtv/twirp"
)

func (s *appRunnerService) ListStorage(context.Context, *apprunnerv1.ListStorageRequest) (*apprunnerv1.ListStorageResponse, error) {
	if s.storage == nil {
		return nil, twirp.InternalError("storage is not configured")
	}
	roots, err := s.storage.list()
	if err != nil {
		return nil, twirp.InternalErrorWith(err)
	}
	response := &apprunnerv1.ListStorageResponse{Roots: make([]*apprunnerv1.StorageRoot, 0, len(roots))}
	for _, root := range roots {
		entries := make([]*apprunnerv1.StorageEntry, 0, len(root.Entries))
		for _, entry := range root.Entries {
			entries = append(entries, storageNodeToProto(entry))
		}
		response.Roots = append(response.Roots, &apprunnerv1.StorageRoot{Name: root.Name, Entries: entries})
	}
	return response, nil
}

func (s *appRunnerService) DownloadStorageFile(ctx context.Context, request *apprunnerv1.DownloadStorageFileRequest) (*apprunnerv1.DownloadStorageFileResponse, error) {
	if s.storage == nil {
		return nil, twirp.InternalError("storage is not configured")
	}
	destination, err := s.storage.destinationDirectory(request.GetRoot(), request.GetDirectory())
	if err != nil {
		return nil, twirp.InvalidArgumentError("directory", err.Error())
	}
	parsedURL, err := url.Parse(strings.TrimSpace(request.GetUrl()))
	if err != nil || (parsedURL.Scheme != "http" && parsedURL.Scheme != "https") || parsedURL.Host == "" {
		return nil, twirp.InvalidArgumentError("url", "must be an absolute HTTP or HTTPS URL")
	}

	downloadRequest, err := http.NewRequestWithContext(ctx, http.MethodGet, parsedURL.String(), nil)
	if err != nil {
		return nil, twirp.InvalidArgumentError("url", "is invalid")
	}
	downloadResponse, err := s.storage.client.Do(downloadRequest)
	if err != nil {
		if errors.Is(err, context.Canceled) {
			return nil, twirp.NewError(twirp.Canceled, "download was canceled")
		}
		if errors.Is(err, context.DeadlineExceeded) {
			return nil, twirp.NewError(twirp.DeadlineExceeded, "download deadline was exceeded")
		}
		return nil, twirp.NewError(twirp.Unavailable, fmt.Sprintf("download failed: %v", err))
	}
	defer downloadResponse.Body.Close()
	if downloadResponse.StatusCode < 200 || downloadResponse.StatusCode >= 300 {
		return nil, twirp.NewError(twirp.FailedPrecondition, fmt.Sprintf("download returned %s", downloadResponse.Status))
	}

	filename := strings.TrimSpace(request.GetFilename())
	if filename == "" {
		filename = downloadFilename(downloadResponse, parsedURL)
	}
	filename, err = cleanUploadFilename(filename)
	if err != nil {
		return nil, twirp.InvalidArgumentError("filename", "is required when it cannot be inferred from the URL")
	}
	if _, err := os.Lstat(filepath.Join(destination, filename)); err == nil {
		return nil, twirp.NewError(twirp.AlreadyExists, fmt.Sprintf("%s already exists", filename))
	} else if !errors.Is(err, os.ErrNotExist) {
		return nil, twirp.InternalErrorWith(err)
	}

	node, err := saveStorageFile(destination, filename, downloadResponse.Body)
	if err != nil {
		if errors.Is(err, os.ErrExist) {
			return nil, twirp.NewError(twirp.AlreadyExists, fmt.Sprintf("%s already exists", filename))
		}
		return nil, twirp.InternalErrorWith(fmt.Errorf("save %s: %w", filename, err))
	}
	node.Path = filepath.ToSlash(filepath.Join(request.GetDirectory(), filename))
	return &apprunnerv1.DownloadStorageFileResponse{File: storageNodeToProto(node)}, nil
}

func storageNodeToProto(node *storageNode) *apprunnerv1.StorageEntry {
	kind := apprunnerv1.StorageEntryKind_STORAGE_ENTRY_KIND_FILE
	if node.Kind == "directory" {
		kind = apprunnerv1.StorageEntryKind_STORAGE_ENTRY_KIND_DIRECTORY
	}
	entry := &apprunnerv1.StorageEntry{
		Name:       node.Name,
		Path:       node.Path,
		Kind:       kind,
		SizeBytes:  uint64(node.SizeBytes),
		ModifiedAt: node.ModifiedAt,
		Children:   make([]*apprunnerv1.StorageEntry, 0, len(node.Children)),
	}
	for _, child := range node.Children {
		entry.Children = append(entry.Children, storageNodeToProto(child))
	}
	return entry
}
