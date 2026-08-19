package main

import (
	"context"
	"encoding/json"
	"io/fs"
	"log/slog"
	"net"
	"net/http"
	"path"
	"strings"

	apprunnerv1 "github.com/roflware/app-runner/internal/gen/apprunner/v1"
)

type appRuntime struct {
	manager      *vmManager
	networking   *networkManager
	capabilities func() hostCapabilities
}

func newHTTPHandler(frontend fs.FS, runtimes ...appRuntime) http.Handler {
	var runtime appRuntime
	if len(runtimes) != 0 {
		runtime = runtimes[0]
	}
	mux := http.NewServeMux()
	rpcServer := apprunnerv1.NewAppRunnerServiceServer(newAppRunnerService(runtime.manager, runtime.capabilities, runtime.networking))
	mux.Handle(rpcServer.PathPrefix(), rpcServer)
	if runtime.manager != nil {
		mux.Handle("/console/", consoleProxyHandler(runtime.manager))
	}

	if frontend == nil {
		mux.HandleFunc("/", developmentInfo)
	} else {
		mux.Handle("/", spaHandler(frontend))
	}

	return requestLogger(withClientAddress(mux))
}

type clientAddressContextKey struct{}

func withClientAddress(next http.Handler) http.Handler {
	return http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		host, _, err := net.SplitHostPort(request.RemoteAddr)
		if err != nil {
			host = request.RemoteAddr
		}
		address := net.ParseIP(host)
		if address != nil && address.IsLoopback() {
			forwardedFor := strings.TrimSpace(strings.Split(request.Header.Get("X-Forwarded-For"), ",")[0])
			if forwardedAddress := net.ParseIP(forwardedFor); forwardedAddress != nil {
				address = forwardedAddress
			}
		}
		ctx := context.WithValue(request.Context(), clientAddressContextKey{}, address)
		next.ServeHTTP(response, request.WithContext(ctx))
	})
}

func isLoopbackRequest(ctx context.Context) bool {
	address, _ := ctx.Value(clientAddressContextKey{}).(net.IP)
	return address != nil && address.IsLoopback()
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
