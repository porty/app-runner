package api

import (
	"encoding/json"
	"io/fs"
	"log/slog"
	"net/http"
	"path"
	"strings"

	apprunnerv1 "github.com/roflware/app-runner/internal/gen/apprunner/v1"

	"github.com/roflware/app-runner/internal/cloudinit"
	"github.com/roflware/app-runner/internal/hostnet"
	"github.com/roflware/app-runner/internal/serialconsole"
	"github.com/roflware/app-runner/internal/storage"
	"github.com/roflware/app-runner/internal/vm"
)

// Runtime is everything the API serves.
type Runtime struct {
	VMs            *vm.Manager
	SerialConsoles *serialconsole.Hub
	Networking     *hostnet.Manager
	Storage        *storage.Service
	Templates      *cloudinit.TemplateManager
	Capabilities   func() vm.HostCapabilities
}

func NewHandler(frontend fs.FS, runtime Runtime) http.Handler {
	mux := http.NewServeMux()
	rpcServer := apprunnerv1.NewAppRunnerServiceServer(NewService(runtime))
	mux.Handle(rpcServer.PathPrefix(), rpcServer)
	if runtime.Storage != nil {
		// Large browser uploads stay multipart HTTP; storage control operations use Twirp.
		mux.Handle("/api/storage", runtime.Storage)
		mux.Handle("/api/storage/", runtime.Storage)
	}
	if runtime.VMs != nil {
		mux.Handle("/console/", consoleProxyHandler(runtime.VMs))
		if runtime.SerialConsoles != nil {
			mux.Handle("/serial-console/", serialConsoleProxyHandler(runtime.VMs, runtime.SerialConsoles))
		}
	}

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
