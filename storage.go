package apprunner

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

const storageMultipartMemory = 32 << 20

type storageService struct {
	roots  map[string]string
	client *http.Client
}

type storageRoot struct {
	Name    string         `json:"name"`
	Entries []*storageNode `json:"entries"`
}

type storageNode struct {
	Name       string         `json:"name"`
	Path       string         `json:"path"`
	Kind       string         `json:"kind"`
	SizeBytes  int64          `json:"size_bytes,omitempty"`
	ModifiedAt string         `json:"modified_at,omitempty"`
	Children   []*storageNode `json:"children,omitempty"`
}

func newStorageService(settings config) *storageService {
	return &storageService{
		roots: map[string]string{
			"iso":  settings.ISODir,
			"disk": settings.DiskDir,
		},
		client: &http.Client{},
	}
}

func (s *storageService) ServeHTTP(response http.ResponseWriter, request *http.Request) {
	if request.URL.Path != "/api/storage/upload" {
		http.NotFound(response, request)
		return
	}
	if request.Method != http.MethodPost {
		response.Header().Set("Allow", http.MethodPost)
		writeStorageError(response, http.StatusMethodNotAllowed, "storage uploads require POST")
		return
	}
	s.upload(response, request)
}

func (s *storageService) list() ([]storageRoot, error) {
	roots := make([]storageRoot, 0, 2)
	for _, name := range []string{"iso", "disk"} {
		entries, err := listStorageDirectory(s.roots[name], "")
		if err != nil {
			return nil, fmt.Errorf("list %s storage: %w", name, err)
		}
		roots = append(roots, storageRoot{Name: name, Entries: entries})
	}
	return roots, nil
}

func listStorageDirectory(directory, relativeDirectory string) ([]*storageNode, error) {
	entries, err := os.ReadDir(directory)
	if err != nil {
		return nil, err
	}
	nodes := make([]*storageNode, 0, len(entries))
	for _, entry := range entries {
		info, err := entry.Info()
		if err != nil {
			return nil, err
		}
		relativePath := filepath.ToSlash(filepath.Join(relativeDirectory, entry.Name()))
		node := &storageNode{
			Name:       entry.Name(),
			Path:       relativePath,
			Kind:       "file",
			SizeBytes:  info.Size(),
			ModifiedAt: info.ModTime().UTC().Format(time.RFC3339),
		}
		if entry.IsDir() {
			node.Kind = "directory"
			node.SizeBytes = 0
			node.Children, err = listStorageDirectory(filepath.Join(directory, entry.Name()), relativePath)
			if err != nil {
				return nil, err
			}
		}
		nodes = append(nodes, node)
	}
	sort.Slice(nodes, func(left, right int) bool {
		if nodes[left].Kind != nodes[right].Kind {
			return nodes[left].Kind == "directory"
		}
		return strings.ToLower(nodes[left].Name) < strings.ToLower(nodes[right].Name)
	})
	return nodes, nil
}

func (s *storageService) upload(response http.ResponseWriter, request *http.Request) {
	if err := request.ParseMultipartForm(storageMultipartMemory); err != nil {
		writeStorageError(response, http.StatusBadRequest, "invalid multipart upload")
		return
	}
	if request.MultipartForm != nil {
		defer request.MultipartForm.RemoveAll()
	}
	directory, err := s.destinationDirectory(request.FormValue("root"), request.FormValue("directory"))
	if err != nil {
		writeStorageError(response, http.StatusBadRequest, err.Error())
		return
	}
	files := request.MultipartForm.File["files"]
	if len(files) == 0 {
		files = request.MultipartForm.File["file"]
	}
	if len(files) == 0 {
		writeStorageError(response, http.StatusBadRequest, "at least one file is required")
		return
	}

	names := make(map[string]struct{}, len(files))
	for _, header := range files {
		name, err := cleanUploadFilename(header.Filename)
		if err != nil {
			writeStorageError(response, http.StatusBadRequest, err.Error())
			return
		}
		if _, duplicate := names[name]; duplicate {
			writeStorageError(response, http.StatusBadRequest, fmt.Sprintf("file %q was selected more than once", name))
			return
		}
		names[name] = struct{}{}
		if _, err := os.Lstat(filepath.Join(directory, name)); err == nil {
			writeStorageError(response, http.StatusConflict, fmt.Sprintf("%s already exists", name))
			return
		} else if !errors.Is(err, os.ErrNotExist) {
			writeStorageError(response, http.StatusInternalServerError, err.Error())
			return
		}
	}

	uploaded := make([]*storageNode, 0, len(files))
	for _, header := range files {
		source, err := header.Open()
		if err != nil {
			writeStorageError(response, http.StatusBadRequest, fmt.Sprintf("open %s: %v", header.Filename, err))
			return
		}
		name, _ := cleanUploadFilename(header.Filename)
		node, saveErr := saveStorageFile(directory, name, source)
		_ = source.Close()
		if saveErr != nil {
			status := http.StatusInternalServerError
			if errors.Is(saveErr, os.ErrExist) {
				status = http.StatusConflict
			}
			writeStorageError(response, status, fmt.Sprintf("save %s: %v", name, saveErr))
			return
		}
		uploaded = append(uploaded, node)
	}
	writeStorageJSON(response, http.StatusCreated, map[string]any{"uploaded": uploaded})
}

func (s *storageService) destinationDirectory(rootName, relativeDirectory string) (string, error) {
	root, found := s.roots[strings.TrimSpace(rootName)]
	if !found {
		return "", errors.New("storage root must be iso or disk")
	}
	relativeDirectory = filepath.FromSlash(strings.TrimSpace(relativeDirectory))
	if filepath.IsAbs(relativeDirectory) {
		return "", errors.New("destination directory must be relative to its storage root")
	}
	cleanRelative := filepath.Clean(relativeDirectory)
	if cleanRelative == "." {
		cleanRelative = ""
	}
	if cleanRelative == ".." || strings.HasPrefix(cleanRelative, ".."+string(filepath.Separator)) {
		return "", errors.New("destination directory must stay within its storage root")
	}

	resolvedRoot, err := filepath.EvalSymlinks(root)
	if err != nil {
		return "", fmt.Errorf("resolve storage root: %w", err)
	}
	destination := filepath.Join(resolvedRoot, cleanRelative)
	resolvedDestination, err := filepath.EvalSymlinks(destination)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return "", errors.New("destination directory does not exist")
		}
		return "", fmt.Errorf("resolve destination directory: %w", err)
	}
	relativeToRoot, err := filepath.Rel(resolvedRoot, resolvedDestination)
	if err != nil || relativeToRoot == ".." || strings.HasPrefix(relativeToRoot, ".."+string(filepath.Separator)) {
		return "", errors.New("destination directory must stay within its storage root")
	}
	info, err := os.Stat(resolvedDestination)
	if err != nil || !info.IsDir() {
		return "", errors.New("destination directory does not exist")
	}
	return resolvedDestination, nil
}

func cleanUploadFilename(name string) (string, error) {
	name = strings.TrimSpace(name)
	if name == "" || name == "." || name == ".." || filepath.Base(name) != name || strings.ContainsAny(name, `/\\`) {
		return "", errors.New("filename must contain only a file name, not a path")
	}
	return name, nil
}

func downloadFilename(response *http.Response, sourceURL *url.URL) string {
	if _, parameters, err := mime.ParseMediaType(response.Header.Get("Content-Disposition")); err == nil {
		if name := parameters["filename"]; name != "" {
			return filepath.Base(name)
		}
	}
	if name, err := url.PathUnescape(filepath.Base(sourceURL.Path)); err == nil && name != "." && name != "/" {
		return name
	}
	return ""
}

func saveStorageFile(directory, name string, source io.Reader) (*storageNode, error) {
	temporary, err := os.CreateTemp(directory, ".app-runner-upload-*")
	if err != nil {
		return nil, err
	}
	temporaryName := temporary.Name()
	defer os.Remove(temporaryName)

	if _, err = io.Copy(temporary, source); err != nil {
		_ = temporary.Close()
		return nil, err
	}
	if err = temporary.Chmod(0o640); err != nil {
		_ = temporary.Close()
		return nil, err
	}
	if err = temporary.Sync(); err != nil {
		_ = temporary.Close()
		return nil, err
	}
	if err = temporary.Close(); err != nil {
		return nil, err
	}
	destination := filepath.Join(directory, name)
	if err = os.Link(temporaryName, destination); err != nil {
		return nil, err
	}
	info, err := os.Stat(destination)
	if err != nil {
		return nil, err
	}
	return &storageNode{
		Name:       name,
		Path:       name,
		Kind:       "file",
		SizeBytes:  info.Size(),
		ModifiedAt: info.ModTime().UTC().Format(time.RFC3339),
	}, nil
}

func writeStorageError(response http.ResponseWriter, status int, message string) {
	writeStorageJSON(response, status, map[string]string{"error": message})
}

func writeStorageJSON(response http.ResponseWriter, status int, value any) {
	response.Header().Set("Content-Type", "application/json")
	response.WriteHeader(status)
	_ = json.NewEncoder(response).Encode(value)
}
