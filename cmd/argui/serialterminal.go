package main

/*
#cgo pkg-config: qtermwidget6
#include "serialterminal_bridge.h"
*/
import "C"

import (
	"errors"
	"io"
	"sync"
	"time"
	"unsafe"

	"github.com/mappu/miqt/qt6"
)

// serialTerminalWidget is the small Go seam around QTermWidget's external
// teletype mode. QTermWidget owns emulation and painting; Go owns transport.
type serialTerminalWidget struct {
	*qt6.QWidget
	bridge unsafe.Pointer
	once   sync.Once
	runs   sync.WaitGroup
}

func newSerialTerminalWidget() *serialTerminalWidget {
	bridge := C.serial_terminal_new(nil)
	widget := qt6.UnsafeNewQWidget(C.serial_terminal_widget(bridge))
	widget.SetMinimumSize2(320, 200)
	return &serialTerminalWidget{QWidget: widget, bridge: bridge}
}

func (t *serialTerminalWidget) run(connection io.ReadWriteCloser) error {
	t.runs.Add(1)
	defer t.runs.Done()
	done := make(chan struct{})
	inputDone := make(chan struct{})
	defer func() {
		close(done)
		<-inputDone
	}()
	inputError := make(chan error, 1)
	go func() {
		defer close(inputDone)
		ticker := time.NewTicker(10 * time.Millisecond)
		defer ticker.Stop()
		buffer := make([]byte, 4096)
		for {
			select {
			case <-done:
				return
			case <-ticker.C:
				count := t.readInput(buffer)
				if count > 0 {
					if err := writeAll(connection, buffer[:count]); err != nil {
						inputError <- err
						_ = connection.Close()
						return
					}
				}
			}
		}
	}()

	buffer := make([]byte, 32*1024)
	for {
		count, err := connection.Read(buffer)
		if count > 0 {
			if written := int(C.serial_terminal_feed(t.bridge, (*C.char)(unsafe.Pointer(&buffer[0])), C.int(count))); written != count {
				return errors.New("write serial output to terminal")
			}
		}
		if err != nil {
			return err
		}
		select {
		case err := <-inputError:
			return err
		default:
		}
	}
}

func (t *serialTerminalWidget) readInput(buffer []byte) int {
	if len(buffer) == 0 {
		return 0
	}
	return int(C.serial_terminal_read_input(t.bridge, (*C.char)(unsafe.Pointer(&buffer[0])), C.int(len(buffer))))
}

func writeAll(writer io.Writer, contents []byte) error {
	for len(contents) > 0 {
		count, err := writer.Write(contents)
		if err != nil {
			return err
		}
		if count == 0 {
			return io.ErrShortWrite
		}
		contents = contents[count:]
	}
	return nil
}

func (t *serialTerminalWidget) clear() { C.serial_terminal_clear(t.bridge) }
func (t *serialTerminalWidget) focus() { C.serial_terminal_focus(t.bridge) }
func (t *serialTerminalWidget) copy()  { C.serial_terminal_copy(t.bridge) }
func (t *serialTerminalWidget) paste() { C.serial_terminal_paste(t.bridge) }

func (t *serialTerminalWidget) destroy() {
	t.once.Do(func() {
		t.runs.Wait()
		C.serial_terminal_delete(t.bridge)
	})
}
