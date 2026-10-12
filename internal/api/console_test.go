package api

import (
	"context"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/coder/websocket"

	"github.com/roflware/app-runner/internal/rfb"
	"github.com/roflware/app-runner/internal/serialconsole"
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

func startSerialConsoleServer(t *testing.T) (string, *httptest.Server, <-chan error) {
	t.Helper()
	manager, _, settings := vmtest.NewManager(t)
	machine, err := manager.Create(context.Background(), vmtest.InstallerOptions("serial-machine"))
	if err != nil {
		t.Fatal(err)
	}
	machine, err = manager.Start(machine.ID)
	if err != nil {
		t.Fatal(err)
	}
	listener, err := net.Listen("unix", settings.SerialSocketPath(machine.ID))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = listener.Close() })
	hub := serialconsole.New(settings.SerialSocketPath, nil)
	t.Cleanup(hub.Close)
	if err := hub.Start(machine.ID); err != nil {
		t.Fatal(err)
	}
	upstream, err := listener.Accept()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = upstream.Close() })

	finished := make(chan error, 1)
	go func() {
		if _, err := upstream.Write([]byte("login: ")); err != nil {
			finished <- err
			return
		}
		input := make([]byte, 5)
		if _, err := io.ReadFull(upstream, input); err != nil {
			finished <- err
			return
		}
		_, err := upstream.Write(append([]byte("echo "), input...))
		finished <- err
	}()
	server := httptest.NewServer(serialConsoleProxyHandler(manager, hub))
	t.Cleanup(server.Close)
	return "/serial-console/" + machine.ID, server, finished
}

func TestSerialConsoleProxyRelaysRawStream(t *testing.T) {
	consolePath, server, serverFinished := startSerialConsoleServer(t)
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	stream, err := serialconsole.DialHTTP(ctx, server.URL+consolePath)
	if err != nil {
		t.Fatal(err)
	}
	defer stream.Close()
	output := make([]byte, 7)
	if _, err := io.ReadFull(stream, output); err != nil {
		t.Fatal(err)
	}
	if string(output) != "login: " {
		t.Fatalf("serial output = %q", output)
	}
	if _, err := stream.Write([]byte("root\n")); err != nil {
		t.Fatal(err)
	}
	reply := make([]byte, 10)
	if _, err := io.ReadFull(stream, reply); err != nil {
		t.Fatal(err)
	}
	if string(reply) != "echo root\n" {
		t.Fatalf("serial reply = %q", reply)
	}
	if err := <-serverFinished; err != nil {
		t.Fatal(err)
	}
}

func TestSerialConsoleProxyRejectsASecondAttachment(t *testing.T) {
	consolePath, server, _ := startSerialConsoleServer(t)
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	first, err := serialconsole.DialHTTP(ctx, server.URL+consolePath)
	if err != nil {
		t.Fatal(err)
	}
	defer first.Close()
	if _, err := serialconsole.DialHTTP(ctx, server.URL+consolePath); err == nil || !strings.Contains(err.Error(), "already in use") {
		t.Fatalf("second serial attachment error = %v", err)
	}
}

func TestSerialConsoleProxyRelaysBinaryWebsocket(t *testing.T) {
	consolePath, server, serverFinished := startSerialConsoleServer(t)
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	websocketURL := "ws" + strings.TrimPrefix(server.URL, "http") + consolePath
	connection, _, err := websocket.Dial(ctx, websocketURL, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer connection.CloseNow()
	messageType, output, err := connection.Read(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if messageType != websocket.MessageBinary || string(output) != "login: " {
		t.Fatalf("unexpected serial output: type=%v contents=%q", messageType, output)
	}
	if err := connection.Write(ctx, websocket.MessageBinary, []byte("root\n")); err != nil {
		t.Fatal(err)
	}
	_, reply, err := connection.Read(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if string(reply) != "echo root\n" {
		t.Fatalf("serial reply = %q", reply)
	}
	if err := <-serverFinished; err != nil {
		t.Fatal(err)
	}
}

// startConsoleServer runs a fake VNC server for a started VM behind the
// console proxy. The fake sends the RFB version, then echoes what it reads
// back with a prefix, and reports on finished.
func startConsoleServer(t *testing.T) (string, *httptest.Server, <-chan error) {
	t.Helper()
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
	t.Cleanup(func() { listener.Close() })
	server := httptest.NewServer(consoleProxyHandler(manager))
	t.Cleanup(server.Close)

	finished := make(chan error, 1)
	go func() {
		connection, acceptErr := listener.Accept()
		if acceptErr != nil {
			finished <- acceptErr
			return
		}
		defer connection.Close()
		if _, err := connection.Write([]byte("RFB 003.008\n")); err != nil {
			finished <- err
			return
		}
		received := make([]byte, 12)
		if _, err := io.ReadFull(connection, received); err != nil {
			finished <- err
			return
		}
		_, err := connection.Write(append([]byte("echo "), received...))
		finished <- err
	}()
	return "/console/" + machine.ID, server, finished
}

func TestConsoleProxyRelaysRawRFBOverHTTPUpgrade(t *testing.T) {
	consolePath, server, serverFinished := startConsoleServer(t)

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	stream, err := rfb.DialHTTP(ctx, server.URL+consolePath)
	if err != nil {
		t.Fatal(err)
	}
	defer stream.Close()
	version := make([]byte, 12)
	if _, err := io.ReadFull(stream, version); err != nil {
		t.Fatal(err)
	}
	if string(version) != "RFB 003.008\n" {
		t.Fatalf("unexpected relayed version: %q", version)
	}
	if _, err := stream.Write([]byte("RFB 003.008\n")); err != nil {
		t.Fatal(err)
	}
	reply := make([]byte, 17)
	if _, err := io.ReadFull(stream, reply); err != nil {
		t.Fatal(err)
	}
	if string(reply) != "echo RFB 003.008\n" {
		t.Fatalf("unexpected relayed reply: %q", reply)
	}
	if err := <-serverFinished; err != nil {
		t.Fatal(err)
	}
}

func TestConsoleUpgradeReportsInactiveVM(t *testing.T) {
	manager, _, _ := vmtest.NewManager(t)
	server := httptest.NewServer(consoleProxyHandler(manager))
	defer server.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	_, err := rfb.DialHTTP(ctx, server.URL+"/console/missing")
	if err == nil {
		t.Fatal("DialHTTP should fail for a VM that is not running")
	}
	if strings.Contains(err.Error(), "409") || strings.Contains(err.Error(), "Conflict") {
		t.Fatalf("expected the server's message, got %q", err)
	}
}

func TestConsoleProxyRelaysBinaryVNCData(t *testing.T) {
	consolePath, server, serverFinished := startConsoleServer(t)

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	websocketURL := "ws" + strings.TrimPrefix(server.URL, "http") + consolePath
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
	if err := connection.Write(ctx, websocket.MessageBinary, []byte("RFB 003.008\n")); err != nil {
		t.Fatal(err)
	}
	_, contents, err = connection.Read(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if string(contents) != "echo RFB 003.008\n" {
		t.Fatalf("unexpected relayed reply: %q", contents)
	}
	if err := <-serverFinished; err != nil {
		t.Fatal(err)
	}
}
