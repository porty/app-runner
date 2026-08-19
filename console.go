package main

import (
	"context"
	"io"
	"net"
	"net/http"
	"strings"
	"time"

	"github.com/coder/websocket"
)

func consoleProxyHandler(manager *vmManager) http.Handler {
	return http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		if request.Method != http.MethodGet {
			http.Error(response, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		id := strings.TrimPrefix(request.URL.Path, "/console/")
		if id == "" || strings.Contains(id, "/") {
			http.NotFound(response, request)
			return
		}
		socketPath, err := manager.ConsoleSocket(id)
		if err != nil {
			http.Error(response, err.Error(), http.StatusConflict)
			return
		}
		vncConnection, err := net.DialTimeout("unix", socketPath, 3*time.Second)
		if err != nil {
			http.Error(response, "the VM console is not ready", http.StatusServiceUnavailable)
			return
		}
		defer vncConnection.Close()

		websocketConnection, err := websocket.Accept(response, request, nil)
		if err != nil {
			return
		}
		defer websocketConnection.CloseNow()

		ctx, cancel := context.WithCancel(request.Context())
		defer cancel()
		browserConnection := websocket.NetConn(ctx, websocketConnection, websocket.MessageBinary)
		defer browserConnection.Close()

		copyFinished := make(chan struct{}, 2)
		go func() {
			_, _ = io.Copy(vncConnection, browserConnection)
			copyFinished <- struct{}{}
		}()
		go func() {
			_, _ = io.Copy(browserConnection, vncConnection)
			copyFinished <- struct{}{}
		}()
		<-copyFinished
	})
}
