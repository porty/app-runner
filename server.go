package main

import (
	"encoding/json"
	"io/fs"
	"log/slog"
	"net/http"
	"path"
	"strings"

	apprunnerv1 "github.com/roflware/app-runner/internal/gen/apprunner/v1"
)

func newHTTPHandler(frontend fs.FS) http.Handler {
	mux := http.NewServeMux()
	rpcServer := apprunnerv1.NewAppRunnerServiceServer(newAppRunnerService())
	mux.Handle(rpcServer.PathPrefix(), rpcServer)

	if frontend == nil {
		mux.HandleFunc("/", developmentInfo)
	} else {
		mux.Handle("/", spaHandler(frontend))
	}

	return requestLogger(mux)
}

func developmentInfo(response http.ResponseWriter, _ *http.Request) {
	response.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(response).Encode(map[string]string{
		"message": "App Runner backend is ready; use the Vite development server for the frontend",
	})
}

func spaHandler(frontend fs.FS) http.Handler {
	fileServer := http.FileServer(http.FS(frontend))

	return http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		requestedPath := strings.TrimPrefix(path.Clean(request.URL.Path), "/")
		if requestedPath == "." {
			requestedPath = "index.html"
		}

		file, err := frontend.Open(requestedPath)
		if err == nil {
			info, statErr := file.Stat()
			_ = file.Close()
			if statErr == nil && !info.IsDir() {
				fileServer.ServeHTTP(response, request)
				return
			}
		}

		fallback := request.Clone(request.Context())
		fallback.URL.Path = "/"
		fileServer.ServeHTTP(response, fallback)
	})
}

func requestLogger(next http.Handler) http.Handler {
	return http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		slog.Debug("http request", "method", request.Method, "path", request.URL.Path)
		next.ServeHTTP(response, request)
	})
}
