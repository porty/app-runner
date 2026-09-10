package api

import (
	"context"
	"io"
	"net"
	"net/http"
	"strings"
	"time"

	"github.com/coder/websocket"

	"github.com/roflware/app-runner/internal/rfb"
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
			serveRawConsole(response, vncConnection)
			return
		}
		serveWebsocketConsole(response, request, vncConnection)
	})
}

func serveRawConsole(response http.ResponseWriter, vncConnection net.Conn) {
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
	_, _ = buffered.WriteString("HTTP/1.1 101 Switching Protocols\r\nUpgrade: " + rfb.UpgradeProtocol + "\r\nConnection: Upgrade\r\n\r\n")
	if err := buffered.Flush(); err != nil {
		return
	}
	// The buffered reader may already hold bytes sent after the request.
	relay(struct {
		io.Reader
		io.Writer
	}{buffered.Reader, clientConnection}, vncConnection)
}

func serveWebsocketConsole(response http.ResponseWriter, request *http.Request, vncConnection net.Conn) {
	websocketConnection, err := websocket.Accept(response, request, nil)
	if err != nil {
		return
	}
	defer websocketConnection.CloseNow()

	ctx, cancel := context.WithCancel(request.Context())
	defer cancel()
	browserConnection := websocket.NetConn(ctx, websocketConnection, websocket.MessageBinary)
	defer browserConnection.Close()
	relay(browserConnection, vncConnection)
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
