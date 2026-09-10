package main

import (
	"os"
	"runtime"
	"testing"

	"github.com/mappu/miqt/qt6"
	"github.com/mappu/miqt/qt6/mainthread"
)

func TestMain(m *testing.M) {
	os.Setenv("QT_QPA_PLATFORM", "offscreen")
	runtime.LockOSThread()
	qt6.NewQApplication([]string{"argui.test"})
	qt6.QGuiApplication_SetQuitOnLastWindowClosed(false)
	result := make(chan int, 1)
	go func() {
		result <- m.Run()
		mainthread.Start(qt6.QCoreApplication_Quit)
	}()
	qt6.QApplication_Exec()
	os.Exit(<-result)
}

func TestSerialTerminalClaimsApplicationShortcuts(t *testing.T) {
	type result struct {
		accepted       bool
		closeTriggered bool
		windowVisible  bool
		input          []byte
	}
	got := mainthread.Wait2(func() result {
		window := qt6.NewQMainWindow2()
		defer window.Delete()
		terminal := newSerialTerminalWidget()
		defer terminal.destroy()
		window.SetCentralWidget(terminal.QWidget)
		closeTriggered := false
		closeAction := qt6.NewQAction2("Close")
		defer closeAction.Delete()
		closeAction.SetShortcut(qt6.NewQKeySequence2("Ctrl+W"))
		closeAction.OnTriggered(func() {
			closeTriggered = true
			window.Close()
		})
		window.AddAction(closeAction)
		window.Show()
		terminal.focus()
		qt6.QCoreApplication_ProcessEvents()
		focus := qt6.QApplication_FocusWidget()
		if focus == nil {
			return result{}
		}
		event := qt6.NewQKeyEvent(qt6.QEvent__ShortcutOverride, int(qt6.Key_W), qt6.ControlModifier)
		defer event.Delete()
		event.SetAccepted(false)
		qt6.QCoreApplication_SendEvent(focus.QObject, event.QEvent)

		keyPress := qt6.NewQKeyEvent3(qt6.QEvent__KeyPress, int(qt6.Key_W), qt6.ControlModifier, "\x17")
		defer keyPress.Delete()
		qt6.QCoreApplication_SendEvent(focus.QObject, keyPress.QEvent)
		input := make([]byte, 8)
		count := terminal.readInput(input)
		return result{
			accepted: event.IsAccepted(), closeTriggered: closeTriggered,
			windowVisible: window.IsVisible(), input: input[:count],
		}
	})
	if got.closeTriggered || !got.windowVisible {
		t.Fatal("Ctrl+W activated the console window's Close shortcut")
	}
	if !got.accepted {
		t.Fatal("serial terminal did not claim Ctrl+W from application shortcuts")
	}
	if string(got.input) != "\x17" {
		t.Fatalf("serial terminal emitted %q for Ctrl+W, want control-W", got.input)
	}
}
