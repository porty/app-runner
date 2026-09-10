package api

import (
	"context"
	"errors"
	"io"
	"net"
	"net/http"
	"strings"
	"time"

	"github.com/coder/websocket"

	"github.com/roflware/app-runner/internal/rfb"
	"github.com/roflware/app-runner/internal/serialconsole"
	"github.com/roflware/app-runner/internal/vm"
)

// consoleProxyHandler relays a VM's VNC socket to the client. A request
// with "Upgrade: rfb" gets the raw byte stream on the hijacked connection,
// which is what native clients want; any other request is treated as a
// websocket upgrade for the browser console.
func consoleProxyHandler(manager *vm.Manager) http.Handler {
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

		if rfb.IsUpgradeRequest(request) {
			serveRawConsole(response, vncConnection, rfb.UpgradeProtocol)
			return
		}
		serveWebsocketConsole(response, request, vncConnection)
	})
}

func serialConsoleProxyHandler(manager *vm.Manager, hub *serialconsole.Hub) http.Handler {
	return http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		if request.Method != http.MethodGet {
			http.Error(response, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		id := strings.TrimPrefix(request.URL.Path, "/serial-console/")
		if id == "" || strings.Contains(id, "/") {
			http.NotFound(response, request)
			return
		}
		if _, err := manager.ConsoleSocket(id); err != nil {
			http.Error(response, err.Error(), http.StatusConflict)
			return
		}
		stream, err := hub.Attach(id)
		if err != nil {
			status := http.StatusServiceUnavailable
			if errors.Is(err, serialconsole.ErrInUse) {
				status = http.StatusConflict
			}
			http.Error(response, err.Error(), status)
			return
		}
		defer stream.Close()
		if serialconsole.IsUpgradeRequest(request) {
			serveRawConsole(response, stream, serialconsole.UpgradeProtocol)
			return
		}
		serveWebsocketConsole(response, request, stream)
	})
}

func serveRawConsole(response http.ResponseWriter, console io.ReadWriter, protocol string) {
	hijacker, ok := response.(http.Hijacker)
	if !ok {
		http.Error(response, "the connection cannot be upgraded", http.StatusInternalServerError)
		return
	}
	clientConnection, buffered, err := hijacker.Hijack()
	if err != nil {
		http.Error(response, "the connection cannot be upgraded", http.StatusInternalServerError)
		return
	}
	defer clientConnection.Close()
	_, _ = buffered.WriteString("HTTP/1.1 101 Switching Protocols\r\nUpgrade: " + protocol + "\r\nConnection: Upgrade\r\n\r\n")
	if err := buffered.Flush(); err != nil {
		return
	}
	// The buffered reader may already hold bytes sent after the request.
	relay(struct {
		io.Reader
		io.Writer
	}{buffered.Reader, clientConnection}, console)
}

func serveWebsocketConsole(response http.ResponseWriter, request *http.Request, console io.ReadWriter) {
	websocketConnection, err := websocket.Accept(response, request, nil)
	if err != nil {
		return
	}
	defer websocketConnection.CloseNow()

	ctx, cancel := context.WithCancel(request.Context())
	defer cancel()
	browserConnection := websocket.NetConn(ctx, websocketConnection, websocket.MessageBinary)
	defer browserConnection.Close()
	relay(browserConnection, console)
}

// relay copies in both directions until either side stops.
func relay(client, vnc io.ReadWriter) {
	copyFinished := make(chan struct{}, 2)
	go func() {
		_, _ = io.Copy(vnc, client)
		copyFinished <- struct{}{}
	}()
	go func() {
		_, _ = io.Copy(client, vnc)
		copyFinished <- struct{}{}
	}()
	<-copyFinished
}
