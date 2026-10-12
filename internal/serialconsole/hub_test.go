package serialconsole

import (
	"io"
	"net"
	"path/filepath"
	"testing"
	"time"
)

func startFakeSerial(t *testing.T) (string, <-chan net.Conn) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "serial.sock")
	listener, err := net.Listen("unix", path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = listener.Close() })
	accepted := make(chan net.Conn, 4)
	go func() {
		for {
			connection, acceptErr := listener.Accept()
			if acceptErr != nil {
				return
			}
			accepted <- connection
		}
	}()
	return path, accepted
}

func TestHubReplaysOutputAndRelaysInBothDirections(t *testing.T) {
	path, accepted := startFakeSerial(t)
	hub := New(func(string) string { return path }, nil)
	t.Cleanup(hub.Close)
	if err := hub.Start("vm"); err != nil {
		t.Fatal(err)
	}
	qemu := <-accepted
	t.Cleanup(func() { _ = qemu.Close() })
	if _, err := qemu.Write([]byte("booted\n")); err != nil {
		t.Fatal(err)
	}

	stream, err := hub.Attach("vm")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = stream.Close() })
	if err := stream.(*Stream).session.upstream.SetDeadline(time.Now().Add(time.Second)); err != nil {
		t.Fatal(err)
	}
	output := make([]byte, len("booted\n"))
	if _, err := io.ReadFull(stream, output); err != nil {
		t.Fatal(err)
	}
	if string(output) != "booted\n" {
		t.Fatalf("replayed output = %q", output)
	}
	if _, err := stream.Write([]byte("root\n")); err != nil {
		t.Fatal(err)
	}
	input := make([]byte, len("root\n"))
	if _, err := io.ReadFull(qemu, input); err != nil {
		t.Fatal(err)
	}
	if string(input) != "root\n" {
		t.Fatalf("relayed input = %q", input)
	}
}

func TestHubAllowsOnlyOneAttachment(t *testing.T) {
	path, accepted := startFakeSerial(t)
	hub := New(func(string) string { return path }, nil)
	t.Cleanup(hub.Close)
	if err := hub.Start("vm"); err != nil {
		t.Fatal(err)
	}
	qemu := <-accepted
	t.Cleanup(func() { _ = qemu.Close() })
	first, err := hub.Attach("vm")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := hub.Attach("vm"); err != ErrInUse {
		t.Fatalf("second attachment error = %v, want %v", err, ErrInUse)
	}
	_ = first.Close()
	second, err := hub.Attach("vm")
	if err != nil {
		t.Fatalf("attach after close: %v", err)
	}
	_ = second.Close()
}

func TestHubReconnectsAfterUpstreamDisconnect(t *testing.T) {
	path, accepted := startFakeSerial(t)
	hub := New(func(string) string { return path }, nil)
	t.Cleanup(hub.Close)
	if err := hub.Start("vm"); err != nil {
		t.Fatal(err)
	}
	firstUpstream := <-accepted
	first, err := hub.Attach("vm")
	if err != nil {
		t.Fatal(err)
	}
	if err := firstUpstream.Close(); err != nil {
		t.Fatal(err)
	}
	readFinished := make(chan error, 1)
	go func() {
		_, err := first.Read(make([]byte, 1))
		readFinished <- err
	}()
	select {
	case err := <-readFinished:
		if err != io.EOF {
			t.Fatalf("attached stream ended with %v, want EOF", err)
		}
	case <-time.After(time.Second):
		t.Fatal("attached stream did not close after upstream disconnect")
	}

	var secondUpstream net.Conn
	select {
	case secondUpstream = <-accepted:
	case <-time.After(time.Second):
		t.Fatal("hub did not reconnect to the serial socket")
	}
	defer secondUpstream.Close()
	var second io.ReadWriteCloser
	deadline := time.Now().Add(time.Second)
	for second == nil {
		second, err = hub.Attach("vm")
		if err == nil {
			break
		}
		if err != ErrNotConnected || time.Now().After(deadline) {
			t.Fatal(err)
		}
		time.Sleep(time.Millisecond)
	}
	defer second.Close()
	if _, err := secondUpstream.Write([]byte("ready")); err != nil {
		t.Fatal(err)
	}
	output := make([]byte, len("ready"))
	if _, err := io.ReadFull(second, output); err != nil {
		t.Fatal(err)
	}
	if string(output) != "ready" {
		t.Fatalf("reconnected output = %q", output)
	}
}

func TestHubKeepsOnlyBoundedHistory(t *testing.T) {
	path, accepted := startFakeSerial(t)
	hub := New(func(string) string { return path }, nil)
	hub.historyBytes = 4
	t.Cleanup(hub.Close)
	if err := hub.Start("vm"); err != nil {
		t.Fatal(err)
	}
	qemu := <-accepted
	t.Cleanup(func() { _ = qemu.Close() })
	_, _ = qemu.Write([]byte("123456"))
	deadline := time.Now().Add(time.Second)
	for {
		hub.mu.Lock()
		session := hub.sessions["vm"]
		hub.mu.Unlock()
		session.mu.Lock()
		captured := len(session.history)
		session.mu.Unlock()
		if captured == 4 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("serial output was not captured")
		}
		time.Sleep(time.Millisecond)
	}

	stream, err := hub.Attach("vm")
	if err != nil {
		t.Fatal(err)
	}
	defer stream.Close()
	output := make([]byte, 4)
	if _, err := io.ReadFull(stream, output); err != nil {
		t.Fatal(err)
	}
	if string(output) != "3456" {
		t.Fatalf("bounded history = %q, want %q", output, "3456")
	}
}
