package rfb

// X11 keysyms for keys that have no Unicode character, as sent in KeyEvent.
const (
	KeyBackspace  uint32 = 0xff08
	KeyTab        uint32 = 0xff09
	KeyReturn     uint32 = 0xff0d
	KeyPause      uint32 = 0xff13
	KeyScrollLock uint32 = 0xff14
	KeySysReq     uint32 = 0xff15
	KeyEscape     uint32 = 0xff1b
	KeyHome       uint32 = 0xff50
	KeyLeft       uint32 = 0xff51
	KeyUp         uint32 = 0xff52
	KeyRight      uint32 = 0xff53
	KeyDown       uint32 = 0xff54
	KeyPageUp     uint32 = 0xff55
	KeyPageDown   uint32 = 0xff56
	KeyEnd        uint32 = 0xff57
	KeyPrint      uint32 = 0xff61
	KeyInsert     uint32 = 0xff63
	KeyMenu       uint32 = 0xff67
	KeyNumLock    uint32 = 0xff7f
	KeyF1         uint32 = 0xffbe
	KeyShiftL     uint32 = 0xffe1
	KeyControlL   uint32 = 0xffe3
	KeyCapsLock   uint32 = 0xffe5
	KeyMetaL      uint32 = 0xffe7
	KeyAltL       uint32 = 0xffe9
	KeySuperL     uint32 = 0xffeb
	KeyDelete     uint32 = 0xffff
	KeyISOLevel3  uint32 = 0xfe03
)

// KeyF returns the keysym for function key n (1-based).
func KeyF(n int) uint32 {
	return KeyF1 + uint32(n-1)
}

// UnicodeKeysym converts a character to its keysym: Latin-1 characters map
// directly and everything else uses the Unicode keysym range.
func UnicodeKeysym(r rune) uint32 {
	if r < 0x100 {
		return uint32(r)
	}
	return 0x01000000 | uint32(r)
}
