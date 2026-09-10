package main

import (
	"sync"
	"unsafe"

	"github.com/mappu/miqt/qt6"
	"github.com/mappu/miqt/qt6/mainthread"

	"github.com/roflware/app-runner/internal/rfb"
)

// vncWidget paints an RFB framebuffer and forwards keyboard and pointer
// input. It implements rfb.Framebuffer; those methods are called from the
// connection goroutine and copy pixels straight into the QImage under a
// mutex, so painting never observes a half-written rectangle.
type vncWidget struct {
	*qt6.QWidget
	mu     sync.Mutex
	image  *qt6.QImage
	bits   []byte
	stride int
	width  int
	height int

	scale   bool
	client  *rfb.Client
	buttons uint8
	pressed map[uint32]bool
	onCut   func(string)
	onKey   func(keysym uint32, down bool)
}

func newVNCWidget() *vncWidget {
	v := &vncWidget{QWidget: qt6.NewQWidget2(), scale: true, pressed: map[uint32]bool{}}
	v.SetMouseTracking(true)
	v.SetFocusPolicy(qt6.StrongFocus)
	v.SetAttribute(qt6.WA_OpaquePaintEvent)
	v.SetMinimumSize2(320, 200)
	v.OnPaintEvent(func(super func(event *qt6.QPaintEvent), event *qt6.QPaintEvent) { v.paint() })
	v.OnMousePressEvent(func(super func(event *qt6.QMouseEvent), event *qt6.QMouseEvent) {
		v.SetFocus()
		v.pointer(event)
	})
	v.OnMouseReleaseEvent(func(super func(event *qt6.QMouseEvent), event *qt6.QMouseEvent) { v.pointer(event) })
	v.OnMouseMoveEvent(func(super func(event *qt6.QMouseEvent), event *qt6.QMouseEvent) { v.pointer(event) })
	v.OnMouseDoubleClickEvent(func(super func(event *qt6.QMouseEvent), event *qt6.QMouseEvent) { v.pointer(event) })
	v.OnWheelEvent(func(super func(event *qt6.QWheelEvent), event *qt6.QWheelEvent) { v.wheel(event) })
	v.OnKeyPressEvent(func(super func(event *qt6.QKeyEvent), event *qt6.QKeyEvent) { v.key(event, true) })
	v.OnKeyReleaseEvent(func(super func(event *qt6.QKeyEvent), event *qt6.QKeyEvent) { v.key(event, false) })
	v.OnFocusOutEvent(func(super func(event *qt6.QFocusEvent), event *qt6.QFocusEvent) {
		v.releaseAll()
		super(event)
	})
	v.OnFocusNextPrevChild(func(super func(next bool) bool, next bool) bool { return false })
	v.OnEvent(func(super func(event *qt6.QEvent) bool, event *qt6.QEvent) bool {
		// Claim every key while focused so application shortcuts reach the
		// guest instead.
		if event.Type() == qt6.QEvent__ShortcutOverride {
			event.Accept()
			return true
		}
		return super(event)
	})
	return v
}

func (v *vncWidget) setClient(client *rfb.Client) {
	v.mu.Lock()
	v.client = client
	v.mu.Unlock()
	v.releaseAll()
}

func (v *vncWidget) currentClient() *rfb.Client {
	v.mu.Lock()
	defer v.mu.Unlock()
	return v.client
}

func (v *vncWidget) setScale(scale bool) {
	v.scale = scale
	v.Update()
}

// Resize implements rfb.Framebuffer.
func (v *vncWidget) Resize(width, height int) {
	mainthread.Wait(func() {
		v.mu.Lock()
		defer v.mu.Unlock()
		image := qt6.NewQImage3(width, height, qt6.QImage__Format_RGB32)
		image.Fill(0)
		v.image = image
		v.width, v.height = width, height
		v.stride = int(image.BytesPerLine())
		v.bits = unsafe.Slice(image.Bits(), int(image.SizeInBytes()))
	})
	mainthread.Start(func() { v.Update() })
}

// Fill implements rfb.Framebuffer.
func (v *vncWidget) Fill(x, y, width, height int, pixels []byte) {
	v.mu.Lock()
	defer v.mu.Unlock()
	if v.bits == nil {
		return
	}
	for row := 0; row < height; row++ {
		target := y + row
		if target < 0 || target >= v.height {
			continue
		}
		left := max(x, 0)
		right := min(x+width, v.width)
		if right <= left {
			continue
		}
		source := pixels[(row*width+(left-x))*4 : (row*width+(right-x))*4]
		copy(v.bits[target*v.stride+left*4:], source)
	}
}

// Copy implements rfb.Framebuffer.
func (v *vncWidget) Copy(srcX, srcY, x, y, width, height int) {
	v.mu.Lock()
	defer v.mu.Unlock()
	if v.bits == nil {
		return
	}
	block := make([]byte, 0, width*height*4)
	for row := 0; row < height; row++ {
		sourceRow := srcY + row
		if sourceRow < 0 || sourceRow >= v.height {
			block = append(block, make([]byte, width*4)...)
			continue
		}
		start := sourceRow*v.stride + max(srcX, 0)*4
		end := sourceRow*v.stride + min(srcX+width, v.width)*4
		if end < start {
			end = start
		}
		row := make([]byte, width*4)
		copy(row, v.bits[start:end])
		block = append(block, row...)
	}
	for row := 0; row < height; row++ {
		target := y + row
		if target < 0 || target >= v.height {
			continue
		}
		left := max(x, 0)
		right := min(x+width, v.width)
		if right <= left {
			continue
		}
		copy(v.bits[target*v.stride+left*4:], block[(row*width+(left-x))*4:(row*width+(right-x))*4])
	}
}

// Updated implements rfb.Framebuffer.
func (v *vncWidget) Updated() {
	mainthread.Start(func() { v.Update() })
}

// Bell implements rfb.Framebuffer.
func (v *vncWidget) Bell() {
	mainthread.Start(func() { qt6.QApplication_Beep() })
}

// CutText implements rfb.Framebuffer.
func (v *vncWidget) CutText(text string) {
	mainthread.Start(func() {
		if v.onCut != nil {
			v.onCut(text)
		}
	})
}

// target reports where the framebuffer is drawn and at what scale.
func (v *vncWidget) target() (x, y, width, height int, scale float64) {
	if v.width == 0 || v.height == 0 {
		return 0, 0, 0, 0, 1
	}
	scale = 1
	if v.scale {
		scale = min(float64(v.Width())/float64(v.width), float64(v.Height())/float64(v.height))
	}
	width = int(float64(v.width) * scale)
	height = int(float64(v.height) * scale)
	x = (v.Width() - width) / 2
	y = (v.Height() - height) / 2
	return x, y, width, height, scale
}

func (v *vncWidget) paint() {
	painter := qt6.NewQPainter2(v.QPaintDevice)
	defer painter.End()
	painter.FillRect6(v.Rect(), qt6.NewQColor2(qt6.Black))
	v.mu.Lock()
	defer v.mu.Unlock()
	if v.image == nil {
		return
	}
	x, y, width, height, scale := v.target()
	if scale != 1 {
		painter.SetRenderHint(qt6.QPainter__SmoothPixmapTransform)
	}
	painter.DrawImage(qt6.NewQRectF4(float64(x), float64(y), float64(width), float64(height)), v.image, qt6.NewQRectF4(0, 0, float64(v.width), float64(v.height)))
}

func (v *vncWidget) framebufferPoint(position *qt6.QPointF) (int, int) {
	x, y, _, _, scale := v.target()
	return int((position.X() - float64(x)) / scale), int((position.Y() - float64(y)) / scale)
}

func buttonMask(buttons qt6.MouseButton) uint8 {
	var mask uint8
	if buttons&qt6.LeftButton != 0 {
		mask |= rfb.ButtonLeft
	}
	if buttons&qt6.MiddleButton != 0 {
		mask |= rfb.ButtonMiddle
	}
	if buttons&qt6.RightButton != 0 {
		mask |= rfb.ButtonRight
	}
	return mask
}

func (v *vncWidget) pointer(event *qt6.QMouseEvent) {
	client := v.currentClient()
	if client == nil {
		return
	}
	x, y := v.framebufferPoint(event.Position())
	v.buttons = buttonMask(event.Buttons())
	_ = client.PointerEvent(x, y, v.buttons)
}

func (v *vncWidget) wheel(event *qt6.QWheelEvent) {
	client := v.currentClient()
	if client == nil {
		return
	}
	x, y := v.framebufferPoint(event.Position())
	delta := event.AngleDelta()
	var wheel uint8
	switch {
	case delta.Y() > 0:
		wheel = rfb.WheelUp
	case delta.Y() < 0:
		wheel = rfb.WheelDown
	case delta.X() > 0:
		wheel = 1 << 5
	case delta.X() < 0:
		wheel = 1 << 6
	default:
		return
	}
	_ = client.PointerEvent(x, y, v.buttons|wheel)
	_ = client.PointerEvent(x, y, v.buttons)
}

func (v *vncWidget) key(event *qt6.QKeyEvent, down bool) {
	keysym := keysymFor(event)
	if keysym == 0 {
		return
	}
	if down {
		v.pressed[keysym] = true
	} else {
		delete(v.pressed, keysym)
	}
	v.sendKey(keysym, down)
}

func (v *vncWidget) sendKey(keysym uint32, down bool) {
	if client := v.currentClient(); client != nil {
		_ = client.KeyEvent(keysym, down)
	}
	if v.onKey != nil {
		v.onKey(keysym, down)
	}
}

// sendSequence presses keys in order and releases them in reverse, for
// chords such as Ctrl+Alt+Del.
func (v *vncWidget) sendSequence(keysyms ...uint32) {
	for _, keysym := range keysyms {
		v.sendKey(keysym, true)
	}
	for index := len(keysyms) - 1; index >= 0; index-- {
		v.sendKey(keysyms[index], false)
	}
}

// typeText sends text as key events, one character at a time.
func (v *vncWidget) typeText(text string) {
	for _, r := range text {
		switch r {
		case '\n':
			v.sendSequence(rfb.KeyReturn)
		case '\t':
			v.sendSequence(rfb.KeyTab)
		case '\r':
		default:
			v.sendSequence(rfb.UnicodeKeysym(r))
		}
	}
}

func (v *vncWidget) releaseAll() {
	for keysym := range v.pressed {
		v.sendKey(keysym, false)
	}
	v.pressed = map[uint32]bool{}
}

// keysymFor maps a Qt key event to an X11 keysym; printable characters use
// the typed text so keyboard layouts are respected.
func keysymFor(event *qt6.QKeyEvent) uint32 {
	key := qt6.Key(event.Key())
	switch key {
	case qt6.Key_Escape:
		return rfb.KeyEscape
	case qt6.Key_Tab, qt6.Key_Backtab:
		return rfb.KeyTab
	case qt6.Key_Backspace:
		return rfb.KeyBackspace
	case qt6.Key_Return, qt6.Key_Enter:
		return rfb.KeyReturn
	case qt6.Key_Insert:
		return rfb.KeyInsert
	case qt6.Key_Delete:
		return rfb.KeyDelete
	case qt6.Key_Pause:
		return rfb.KeyPause
	case qt6.Key_Print:
		return rfb.KeyPrint
	case qt6.Key_Home:
		return rfb.KeyHome
	case qt6.Key_End:
		return rfb.KeyEnd
	case qt6.Key_Left:
		return rfb.KeyLeft
	case qt6.Key_Up:
		return rfb.KeyUp
	case qt6.Key_Right:
		return rfb.KeyRight
	case qt6.Key_Down:
		return rfb.KeyDown
	case qt6.Key_PageUp:
		return rfb.KeyPageUp
	case qt6.Key_PageDown:
		return rfb.KeyPageDown
	case qt6.Key_Shift:
		return rfb.KeyShiftL
	case qt6.Key_Control:
		return rfb.KeyControlL
	case qt6.Key_Meta, qt6.Key_Super_L:
		return rfb.KeySuperL
	case qt6.Key_Alt:
		return rfb.KeyAltL
	case qt6.Key_AltGr:
		return rfb.KeyISOLevel3
	case qt6.Key_CapsLock:
		return rfb.KeyCapsLock
	case qt6.Key_NumLock:
		return rfb.KeyNumLock
	case qt6.Key_ScrollLock:
		return rfb.KeyScrollLock
	case qt6.Key_Menu:
		return rfb.KeyMenu
	}
	if key >= qt6.Key_F1 && key <= qt6.Key_F1+34 {
		return rfb.KeyF(int(key-qt6.Key_F1) + 1)
	}
	text := []rune(event.Text())
	if len(text) == 1 && text[0] >= 0x20 && text[0] != 0x7f {
		return rfb.UnicodeKeysym(text[0])
	}
	if key >= 0x20 && key <= 0xff {
		r := rune(key)
		if r >= 'A' && r <= 'Z' && event.Modifiers()&qt6.ShiftModifier == 0 {
			r += 'a' - 'A'
		}
		return rfb.UnicodeKeysym(r)
	}
	if key > 0xff && key < 0x01000000 {
		return rfb.UnicodeKeysym(rune(key))
	}
	return 0
}
