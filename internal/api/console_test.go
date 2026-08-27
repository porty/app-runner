package api

import (
	"context"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/coder/websocket"

	"github.com/roflware/app-runner/internal/vm/vmtest"
)

func TestConsoleProxyRejectsInactiveVM(t *testing.T) {
	manager, _, _ := vmtest.NewManager(t)
	handler := consoleProxyHandler(manager)
	request := httptest.NewRequest(http.MethodGet, "/console/missing", nil)
	response := httptest.NewRecorder()

	handler.ServeHTTP(response, request)

	if response.Code != http.StatusConflict {
		t.Fatalf("expected status %d, got %d", http.StatusConflict, response.Code)
	}
}

func TestConsoleProxyRelaysBinaryVNCData(t *testing.T) {
	manager, _, settings := vmtest.NewManager(t)
	machine, err := manager.Create(context.Background(), vmtest.InstallerOptions("console-machine"))
	if err != nil {
		t.Fatal(err)
	}
	machine, err = manager.Start(machine.ID)
	if err != nil {
		t.Fatal(err)
	}

	listener, err := net.Listen("unix", settings.VNCSocketPath(machine.ID))
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	server := httptest.NewServer(consoleProxyHandler(manager))
	defer server.Close()

	serverFinished := make(chan error, 1)
	go func() {
		connection, acceptErr := listener.Accept()
		if acceptErr != nil {
			serverFinished <- acceptErr
			return
		}
		defer connection.Close()
		_, writeErr := connection.Write([]byte("RFB 003.008\n"))
		serverFinished <- writeErr
	}()

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	websocketURL := "ws" + strings.TrimPrefix(server.URL, "http") + "/console/" + machine.ID
	connection, _, err := websocket.Dial(ctx, websocketURL, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer connection.CloseNow()
	messageType, contents, err := connection.Read(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if messageType != websocket.MessageBinary || string(contents) != "RFB 003.008\n" {
		t.Fatalf("unexpected relayed message: type=%v contents=%q", messageType, contents)
	}
	if err := <-serverFinished; err != nil {
		t.Fatal(err)
	}
}
