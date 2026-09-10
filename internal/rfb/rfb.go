// Package rfb implements the subset of the Remote Framebuffer protocol
// (VNC) that a local console needs: version 3.8 handshake without
// authentication, raw and CopyRect framebuffer updates, desktop resizing, and
// keyboard and pointer input. The connection carries binary RFB over any
// byte stream; DialHTTP opens one to App Runner's console endpoint with a
// plain HTTP upgrade.
package rfb

import (
	"bufio"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"sync"
)

const (
	encodingRaw         int32 = 0
	encodingCopyRect    int32 = 1
	encodingDesktopSize int32 = -223

	messageFramebufferUpdate   = 0
	messageSetColourMapEntries = 1
	messageBell                = 2
	messageServerCutText       = 3

	clientSetPixelFormat           = 0
	clientSetEncodings             = 2
	clientFramebufferUpdateRequest = 3
	clientKeyEvent                 = 4
	clientPointerEvent             = 5
	clientCutText                  = 6
)

// Pointer button masks for PointerEvent.
const (
	ButtonLeft   uint8 = 1 << 0
	ButtonMiddle uint8 = 1 << 1
	ButtonRight  uint8 = 1 << 2
	WheelUp      uint8 = 1 << 3
	WheelDown    uint8 = 1 << 4
)

// Framebuffer receives decoded pixels. Pixels are 32-bit little-endian
// 0x00RRGGBB values (bytes B, G, R, X in memory), which is the layout of a
// QImage in Format_RGB32.
type Framebuffer interface {
	// Resize is called before any pixels are written and whenever the
	// server changes its desktop size.
	Resize(width, height int)
	// Fill copies a width×height block of pixels (4 bytes each, row-major,
	// no padding) to (x, y).
	Fill(x, y, width, height int, pixels []byte)
	// Copy moves a width×height block from (srcX, srcY) to (x, y).
	Copy(srcX, srcY, x, y, width, height int)
	// Updated is called after each complete framebuffer update message.
	Updated()
	// Bell is called when the server rings the bell.
	Bell()
	// CutText delivers text the server placed on its clipboard.
	CutText(text string)
}

// Client is a connected RFB client. Its methods are safe for concurrent use.
type Client struct {
	conn   io.ReadWriteCloser
	reader *bufio.Reader
	fb     Framebuffer

	writeMu sync.Mutex
	stateMu sync.Mutex
	width   int
	height  int
	name    string
	closed  bool
}

// Dial performs the RFB handshake over conn. The connection must be closed
// with Close; Run must be called to receive updates.
func Dial(conn io.ReadWriteCloser, fb Framebuffer, shared bool) (*Client, error) {
	c := &Client{conn: conn, reader: bufio.NewReaderSize(conn, 1<<16), fb: fb}
	if err := c.handshake(shared); err != nil {
		conn.Close()
		return nil, err
	}
	return c, nil
}

func (c *Client) handshake(shared bool) error {
	version := make([]byte, 12)
	if _, err := io.ReadFull(c.reader, version); err != nil {
		return fmt.Errorf("read protocol version: %w", err)
	}
	if string(version[:4]) != "RFB " {
		return errors.New("the console did not speak the RFB protocol")
	}
	if _, err := c.conn.Write([]byte("RFB 003.008\n")); err != nil {
		return fmt.Errorf("write protocol version: %w", err)
	}
	count, err := c.reader.ReadByte()
	if err != nil {
		return fmt.Errorf("read security types: %w", err)
	}
	if count == 0 {
		return fmt.Errorf("console refused the connection: %s", c.readReason())
	}
	types := make([]byte, count)
	if _, err := io.ReadFull(c.reader, types); err != nil {
		return fmt.Errorf("read security types: %w", err)
	}
	var chosen byte
	for _, t := range types {
		if t == 1 {
			chosen = 1
		}
	}
	if chosen == 0 {
		return errors.New("the console requires VNC authentication, which this client does not support")
	}
	if _, err := c.conn.Write([]byte{chosen}); err != nil {
		return fmt.Errorf("choose security type: %w", err)
	}
	var result uint32
	if err := binary.Read(c.reader, binary.BigEndian, &result); err != nil {
		return fmt.Errorf("read security result: %w", err)
	}
	if result != 0 {
		return fmt.Errorf("console security handshake failed: %s", c.readReason())
	}
	flag := byte(0)
	if shared {
		flag = 1
	}
	if _, err := c.conn.Write([]byte{flag}); err != nil {
		return fmt.Errorf("client init: %w", err)
	}
	var init struct {
		Width, Height uint16
		PixelFormat   [16]byte
		NameLength    uint32
	}
	if err := binary.Read(c.reader, binary.BigEndian, &init); err != nil {
		return fmt.Errorf("read server init: %w", err)
	}
	name := make([]byte, init.NameLength)
	if _, err := io.ReadFull(c.reader, name); err != nil {
		return fmt.Errorf("read desktop name: %w", err)
	}
	c.width, c.height, c.name = int(init.Width), int(init.Height), string(name)
	c.fb.Resize(c.width, c.height)

	// 32 bits per pixel, depth 24, little-endian, true colour with
	// red<<16 | green<<8 | blue.
	pixelFormat := []byte{clientSetPixelFormat, 0, 0, 0, 32, 24, 0, 1, 0, 255, 0, 255, 0, 255, 16, 8, 0, 0, 0, 0}
	if _, err := c.conn.Write(pixelFormat); err != nil {
		return fmt.Errorf("set pixel format: %w", err)
	}
	encodings := []int32{encodingCopyRect, encodingRaw, encodingDesktopSize}
	message := make([]byte, 4+4*len(encodings))
	message[0] = clientSetEncodings
	binary.BigEndian.PutUint16(message[2:], uint16(len(encodings)))
	for index, encoding := range encodings {
		binary.BigEndian.PutUint32(message[4+4*index:], uint32(encoding))
	}
	if _, err := c.conn.Write(message); err != nil {
		return fmt.Errorf("set encodings: %w", err)
	}
	return c.RequestUpdate(false)
}

func (c *Client) readReason() string {
	var length uint32
	if err := binary.Read(c.reader, binary.BigEndian, &length); err != nil || length > 4096 {
		return "unknown reason"
	}
	reason := make([]byte, length)
	if _, err := io.ReadFull(c.reader, reason); err != nil {
		return "unknown reason"
	}
	return string(reason)
}

// Size reports the current desktop size.
func (c *Client) Size() (width, height int) {
	c.stateMu.Lock()
	defer c.stateMu.Unlock()
	return c.width, c.height
}

// Name reports the desktop name sent by the server.
func (c *Client) Name() string {
	c.stateMu.Lock()
	defer c.stateMu.Unlock()
	return c.name
}

// Run reads server messages until the connection ends. It returns nil when
// Close was called and the underlying error otherwise.
func (c *Client) Run() error {
	for {
		kind, err := c.reader.ReadByte()
		if err != nil {
			return c.finish(err)
		}
		switch kind {
		case messageFramebufferUpdate:
			if err := c.readFramebufferUpdate(); err != nil {
				return c.finish(err)
			}
			if err := c.RequestUpdate(true); err != nil {
				return c.finish(err)
			}
		case messageSetColourMapEntries:
			var header struct {
				Padding    byte
				FirstColor uint16
				Count      uint16
			}
			if err := binary.Read(c.reader, binary.BigEndian, &header); err != nil {
				return c.finish(err)
			}
			if _, err := io.CopyN(io.Discard, c.reader, int64(header.Count)*6); err != nil {
				return c.finish(err)
			}
		case messageBell:
			c.fb.Bell()
		case messageServerCutText:
			var header struct {
				Padding [3]byte
				Length  uint32
			}
			if err := binary.Read(c.reader, binary.BigEndian, &header); err != nil {
				return c.finish(err)
			}
			if header.Length > 1<<20 {
				return c.finish(fmt.Errorf("server cut text of %d bytes is too large", header.Length))
			}
			text := make([]byte, header.Length)
			if _, err := io.ReadFull(c.reader, text); err != nil {
				return c.finish(err)
			}
			c.fb.CutText(string(text))
		default:
			return c.finish(fmt.Errorf("unsupported server message type %d", kind))
		}
	}
}

func (c *Client) finish(err error) error {
	c.stateMu.Lock()
	closed := c.closed
	c.stateMu.Unlock()
	if closed {
		return nil
	}
	return err
}

func (c *Client) readFramebufferUpdate() error {
	var header struct {
		Padding byte
		Count   uint16
	}
	if err := binary.Read(c.reader, binary.BigEndian, &header); err != nil {
		return err
	}
	for index := 0; index < int(header.Count); index++ {
		var rect struct {
			X, Y, Width, Height uint16
			Encoding            int32
		}
		if err := binary.Read(c.reader, binary.BigEndian, &rect); err != nil {
			return err
		}
		x, y, width, height := int(rect.X), int(rect.Y), int(rect.Width), int(rect.Height)
		switch rect.Encoding {
		case encodingRaw:
			pixels := make([]byte, width*height*4)
			if _, err := io.ReadFull(c.reader, pixels); err != nil {
				return err
			}
			c.fb.Fill(x, y, width, height, pixels)
		case encodingCopyRect:
			var source struct{ X, Y uint16 }
			if err := binary.Read(c.reader, binary.BigEndian, &source); err != nil {
				return err
			}
			c.fb.Copy(int(source.X), int(source.Y), x, y, width, height)
		case encodingDesktopSize:
			c.stateMu.Lock()
			c.width, c.height = width, height
			c.stateMu.Unlock()
			c.fb.Resize(width, height)
		default:
			return fmt.Errorf("server used unsupported encoding %d", rect.Encoding)
		}
	}
	c.fb.Updated()
	return nil
}

func (c *Client) write(message []byte) error {
	c.writeMu.Lock()
	defer c.writeMu.Unlock()
	_, err := c.conn.Write(message)
	return err
}

// RequestUpdate asks the server for the whole framebuffer; incremental
// requests only return changed regions.
func (c *Client) RequestUpdate(incremental bool) error {
	width, height := c.Size()
	message := make([]byte, 10)
	message[0] = clientFramebufferUpdateRequest
	if incremental {
		message[1] = 1
	}
	binary.BigEndian.PutUint16(message[6:], uint16(width))
	binary.BigEndian.PutUint16(message[8:], uint16(height))
	return c.write(message)
}

// KeyEvent sends an X11 keysym press or release.
func (c *Client) KeyEvent(keysym uint32, down bool) error {
	message := make([]byte, 8)
	message[0] = clientKeyEvent
	if down {
		message[1] = 1
	}
	binary.BigEndian.PutUint32(message[4:], keysym)
	return c.write(message)
}

// PointerEvent reports the pointer position and the pressed button mask.
func (c *Client) PointerEvent(x, y int, buttons uint8) error {
	width, height := c.Size()
	x = clamp(x, 0, width-1)
	y = clamp(y, 0, height-1)
	message := make([]byte, 6)
	message[0] = clientPointerEvent
	message[1] = buttons
	binary.BigEndian.PutUint16(message[2:], uint16(x))
	binary.BigEndian.PutUint16(message[4:], uint16(y))
	return c.write(message)
}

// CutText places text on the server's clipboard. Only Latin-1 text is
// representable; other characters are replaced.
func (c *Client) CutText(text string) error {
	encoded := make([]byte, 0, len(text))
	for _, r := range text {
		if r < 256 {
			encoded = append(encoded, byte(r))
		} else {
			encoded = append(encoded, '?')
		}
	}
	message := make([]byte, 8+len(encoded))
	message[0] = clientCutText
	binary.BigEndian.PutUint32(message[4:], uint32(len(encoded)))
	copy(message[8:], encoded)
	return c.write(message)
}

// Close ends the session; a Run in progress returns nil.
func (c *Client) Close() error {
	c.stateMu.Lock()
	c.closed = true
	c.stateMu.Unlock()
	return c.conn.Close()
}

func clamp(value, low, high int) int {
	if high < low {
		return low
	}
	return min(max(value, low), high)
}
