package rfb

import (
	"bytes"
	"encoding/binary"
	"net"
	"testing"
	"time"
)

type recorder struct {
	width, height int
	fills         []string
	copies        int
	updates       int
}

func (r *recorder) Resize(width, height int) { r.width, r.height = width, height }
func (r *recorder) Fill(x, y, width, height int, pixels []byte) {
	r.fills = append(r.fills, string(pixels))
}
func (r *recorder) Copy(int, int, int, int, int, int) { r.copies++ }
func (r *recorder) Updated()                          { r.updates++ }
func (r *recorder) Bell()                             {}
func (r *recorder) CutText(string)                    {}

func serverInit(width, height uint16, name string) []byte {
	var buffer bytes.Buffer
	binary.Write(&buffer, binary.BigEndian, width)
	binary.Write(&buffer, binary.BigEndian, height)
	buffer.Write(make([]byte, 16))
	binary.Write(&buffer, binary.BigEndian, uint32(len(name)))
	buffer.WriteString(name)
	return buffer.Bytes()
}

func TestDialHandshakesWithoutAuthentication(t *testing.T) {
	client, server := net.Pipe()
	defer server.Close()
	fb := &recorder{}
	go func() {
		server.Write([]byte("RFB 003.008\n"))
		version := make([]byte, 12)
		server.Read(version)
		server.Write([]byte{1, 1})
		choice := make([]byte, 1)
		server.Read(choice)
		server.Write([]byte{0, 0, 0, 0})
		shared := make([]byte, 1)
		server.Read(shared)
		server.Write(serverInit(4, 2, "QEMU (vm)"))
		// Consume SetPixelFormat, SetEncodings, and the first update request.
		consumed := make([]byte, 20+4+12+10)
		server.SetReadDeadline(time.Now().Add(time.Second))
		total := 0
		for total < len(consumed) {
			n, err := server.Read(consumed[total:])
			if err != nil {
				return
			}
			total += n
		}
		var update bytes.Buffer
		update.Write([]byte{messageFramebufferUpdate, 0, 0, 2})
		binary.Write(&update, binary.BigEndian, struct{ X, Y, W, H uint16 }{0, 0, 1, 1})
		binary.Write(&update, binary.BigEndian, encodingRaw)
		update.Write([]byte{1, 2, 3, 0})
		binary.Write(&update, binary.BigEndian, struct{ X, Y, W, H uint16 }{1, 0, 1, 1})
		binary.Write(&update, binary.BigEndian, encodingCopyRect)
		binary.Write(&update, binary.BigEndian, struct{ X, Y uint16 }{0, 0})
		server.Write(update.Bytes())
		request := make([]byte, 10)
		server.Read(request)
		server.Close()
	}()
	c, err := Dial(client, fb, true)
	if err != nil {
		t.Fatalf("Dial: %v", err)
	}
	if fb.width != 4 || fb.height != 2 {
		t.Fatalf("framebuffer was resized to %dx%d, want 4x2", fb.width, fb.height)
	}
	if c.Name() != "QEMU (vm)" {
		t.Fatalf("desktop name %q", c.Name())
	}
	if err := c.Run(); err == nil {
		t.Fatal("Run should report the closed connection")
	}
	if len(fb.fills) != 1 || fb.fills[0] != "\x01\x02\x03\x00" {
		t.Fatalf("raw rectangle was not delivered: %q", fb.fills)
	}
	if fb.copies != 1 || fb.updates != 1 {
		t.Fatalf("copies=%d updates=%d", fb.copies, fb.updates)
	}
}

func TestDialRejectsAuthentication(t *testing.T) {
	client, server := net.Pipe()
	go func() {
		server.Write([]byte("RFB 003.008\n"))
		version := make([]byte, 12)
		server.Read(version)
		server.Write([]byte{1, 2})
		server.Close()
	}()
	if _, err := Dial(client, &recorder{}, true); err == nil {
		t.Fatal("Dial should fail when only VNC authentication is offered")
	}
}

func TestPointerEventClampsToDesktop(t *testing.T) {
	client, server := net.Pipe()
	defer server.Close()
	c := &Client{conn: client, width: 100, height: 50}
	go c.PointerEvent(500, -3, ButtonLeft)
	message := make([]byte, 6)
	server.SetReadDeadline(time.Now().Add(time.Second))
	if _, err := server.Read(message); err != nil {
		t.Fatal(err)
	}
	x := binary.BigEndian.Uint16(message[2:])
	y := binary.BigEndian.Uint16(message[4:])
	if message[1] != ButtonLeft || x != 99 || y != 0 {
		t.Fatalf("pointer event %v", message)
	}
}

func TestUnicodeKeysym(t *testing.T) {
	if UnicodeKeysym('a') != 0x61 || UnicodeKeysym('é') != 0xe9 || UnicodeKeysym('€') != 0x010020ac {
		t.Fatal("unexpected keysym mapping")
	}
	if KeyF(12) != 0xffc9 {
		t.Fatalf("F12 keysym %#x", KeyF(12))
	}
}
