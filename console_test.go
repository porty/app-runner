package main

import (
	"context"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/coder/websocket"
)

func TestConsoleProxyRejectsInactiveVM(t *testing.T) {
	manager, _, _ := newTestVMManager(t)
	handler := consoleProxyHandler(manager)
	request := httptest.NewRequest(http.MethodGet, "/console/missing", nil)
	response := httptest.NewRecorder()

	handler.ServeHTTP(response, request)

	if response.Code != http.StatusConflict {
		t.Fatalf("expected status %d, got %d", http.StatusConflict, response.Code)
	}
}

func TestConsoleProxyRelaysBinaryVNCData(t *testing.T) {
	manager, _, _ := newTestVMManager(t)
	vm, err := manager.Create(context.Background(), createVMOptions{
		Name: "Console VM", CPUs: 1, MemoryMiB: 512, DiskGiB: 1,
		ISOName: "installer.iso", NetworkMode: networkModeNAT,
	})
	if err != nil {
		t.Fatal(err)
	}
	vm, err = manager.Start(vm.ID)
	if err != nil {
		t.Fatal(err)
	}

	listener, err := net.Listen("unix", manager.vncSocketPath(vm.ID))
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
	websocketURL := "ws" + strings.TrimPrefix(server.URL, "http") + "/console/" + vm.ID
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
