package main

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/mappu/miqt/qt6"
	"github.com/mappu/miqt/qt6/mainthread"

	apprunnerv1 "github.com/roflware/app-runner/internal/gen/apprunner/v1"
	"github.com/roflware/app-runner/internal/rfb"
)

// consoleWindow is a VM's console: its own window with a power menu, key
// chords, the VNC display, and a status bar.
type consoleWindow struct {
	*qt6.QMainWindow
	mw       *mainWindow
	vm       *apprunnerv1.VirtualMachine
	onClosed func()

	stack                               *qt6.QStackedWidget
	offLabel                            *qt6.QLabel
	startBtn                            *qt6.QPushButton
	vnc                                 *vncWidget
	banner                              *messageWidget
	toolbar                             *qt6.QToolBar
	scaleBox                            *qt6.QCheckBox
	statusConn, statusVM, statusAddress *qt6.QLabel

	startAction, shutdownAction, rebootAction, forceAction, reconnectAction, fullAction *qt6.QAction

	client     *rfb.Client
	connecting bool
	expected   bool
	generation int
	busy       bool
	timer      *qt6.QTimer
	closed     bool
}

func newConsoleWindow(mw *mainWindow, vm *apprunnerv1.VirtualMachine) *consoleWindow {
	c := &consoleWindow{QMainWindow: qt6.NewQMainWindow2(), mw: mw, vm: vm}
	c.SetAttribute(qt6.WA_DeleteOnClose)
	c.SetWindowIcon(themeIcon("utilities-terminal", qt6.QStyle__SP_ComputerIcon))
	c.Resize(1000, 720)
	c.build()
	c.timer = qt6.NewQTimer2(c.QObject)
	c.timer.OnTimeout(func() { c.poll() })
	c.timer.Start(2000)
	c.OnCloseEvent(func(super func(event *qt6.QCloseEvent), event *qt6.QCloseEvent) {
		c.closed = true
		c.timer.Stop()
		c.disconnect(true)
		if c.onClosed != nil {
			c.onClosed()
		}
		super(event)
	})
	c.vmChanged(vm)
	return c
}

func (c *consoleWindow) build() {
	c.vnc = newVNCWidget()
	c.vnc.setScale(c.mw.settings.consoleScaleToWindow())
	c.vnc.onCut = func(text string) { qt6.QGuiApplication_Clipboard().SetText(text) }

	c.startAction = c.mw.action("&Start", "media-playback-start", qt6.QStyle__SP_MediaPlay, "", func() { c.power("start") })
	c.shutdownAction = c.mw.action("Shut &Down", "system-shutdown", 0, "", func() { c.power("shutdown") })
	c.rebootAction = c.mw.action("&Reboot", "system-reboot", qt6.QStyle__SP_BrowserReload, "", func() { c.power("reboot") })
	c.forceAction = c.mw.action("&Force Off…", "process-stop", qt6.QStyle__SP_MediaStop, "", func() { c.power("force") })
	c.reconnectAction = c.mw.action("Re&connect", "view-refresh", qt6.QStyle__SP_BrowserReload, "Ctrl+R", func() {
		c.disconnect(true)
		c.connect()
	})
	c.fullAction = c.mw.action("&Full Screen", "view-fullscreen", 0, "F11", func() { c.toggleFullScreen() })
	c.fullAction.SetCheckable(true)
	closeAction := c.mw.action("&Close", "window-close", 0, "Ctrl+W", func() { c.Close() })

	bar := c.MenuBar()
	machine := bar.AddMenuWithTitle("&Machine")
	for _, a := range []*qt6.QAction{c.startAction, c.shutdownAction, c.rebootAction, c.forceAction, nil, c.reconnectAction, nil, closeAction} {
		if a == nil {
			machine.AddSeparator()
		} else {
			machine.AddAction(a)
		}
	}
	viewMenu := bar.AddMenuWithTitle("&View")
	scaleAction := c.mw.action("&Scale to Window", "zoom-fit-best", 0, "Ctrl+Shift+S", nil)
	scaleAction.SetCheckable(true)
	scaleAction.SetChecked(c.vnc.scale)
	scaleAction.OnToggled(func(checked bool) { c.setScale(checked) })
	viewMenu.AddAction(scaleAction)
	viewMenu.AddAction(c.fullAction)
	input := bar.AddMenuWithTitle("&Input")
	sendKeys := c.buildSendKeyMenu()
	input.AddMenu(sendKeys)
	input.AddAction(c.mw.action("&Paste Clipboard as Keystrokes", "edit-paste", 0, "Ctrl+Shift+V", func() {
		c.vnc.typeText(qt6.QGuiApplication_Clipboard().Text())
	}))
	help := bar.AddMenuWithTitle("&Help")
	help.AddAction(c.mw.action("Console &Help", "help-contents", 0, "", func() {
		qt6.QMessageBox_Information(c.QWidget, "Console", "Click the display to give the guest keyboard focus; every key, including application shortcuts, then goes to the guest. Use Input → Send Key for chords the desktop intercepts, such as Ctrl+Alt+Del. Shut Down asks the guest to power off via ACPI; Force Off stops QEMU immediately.")
	}))

	c.toolbar = qt6.NewQToolBar2("Console")
	c.toolbar.SetMovable(false)
	c.toolbar.SetToolButtonStyle(qt6.ToolButtonTextBesideIcon)
	c.toolbar.AddAction(c.startAction)
	power := qt6.NewQToolButton2()
	power.SetText("Power")
	power.SetIcon(themeIcon("system-shutdown", 0))
	power.SetToolButtonStyle(qt6.ToolButtonTextBesideIcon)
	power.SetPopupMode(qt6.QToolButton__InstantPopup)
	powerMenu := qt6.NewQMenu(c.QWidget)
	powerMenu.AddAction(c.shutdownAction)
	powerMenu.AddAction(c.rebootAction)
	powerMenu.AddSeparator()
	powerMenu.AddAction(c.forceAction)
	power.SetMenu(powerMenu)
	c.toolbar.AddWidget(power.QWidget)
	c.toolbar.AddSeparator()
	keys := qt6.NewQToolButton2()
	keys.SetText("Send Key")
	keys.SetIcon(themeIcon("input-keyboard", 0))
	keys.SetToolButtonStyle(qt6.ToolButtonTextBesideIcon)
	keys.SetPopupMode(qt6.QToolButton__InstantPopup)
	keys.SetMenu(sendKeys)
	c.toolbar.AddWidget(keys.QWidget)
	c.toolbar.AddAction(c.reconnectAction)
	c.toolbar.AddWidget(spacer())
	c.scaleBox = qt6.NewQCheckBox3("Scale to window")
	c.scaleBox.SetChecked(c.vnc.scale)
	c.scaleBox.OnToggled(func(checked bool) {
		if scaleAction.IsChecked() != checked {
			scaleAction.SetChecked(checked)
		}
	})
	c.toolbar.AddWidget(c.scaleBox.QWidget)
	c.toolbar.AddAction(c.fullAction)
	c.AddToolBarWithToolbar(c.toolbar)

	central := qt6.NewQWidget2()
	layout := qt6.NewQVBoxLayout(central)
	layout.SetContentsMargins(0, 0, 0, 0)
	layout.SetSpacing(0)
	c.banner = newMessageWidget(true)
	bannerLayout := qt6.NewQVBoxLayout2()
	bannerLayout.SetContentsMargins(8, 8, 8, 0)
	bannerLayout.AddWidget(c.banner.QWidget)
	layout.AddLayout(bannerLayout.QLayout)
	c.stack = qt6.NewQStackedWidget2()
	off := qt6.NewQWidget2()
	offLayout := qt6.NewQVBoxLayout(off)
	offLayout.AddStretch()
	c.offLabel = richLabel("")
	c.offLabel.SetAlignment(qt6.AlignCenter)
	offLayout.AddWidget(c.offLabel.QWidget)
	c.startBtn = qt6.NewQPushButton4(themeIcon("media-playback-start", qt6.QStyle__SP_MediaPlay), "Start")
	c.startBtn.OnClicked(func() { c.power("start") })
	offLayout.AddWidget3(c.startBtn.QWidget, 0, qt6.AlignHCenter)
	offLayout.AddStretch()
	c.stack.AddWidget(off)
	c.stack.AddWidget(c.vnc.QWidget)
	layout.AddWidget2(c.stack.QWidget, 1)
	c.SetCentralWidget(central)

	status := c.StatusBar()
	c.statusConn = inlineLabel("")
	c.statusVM = qt6.NewQLabel2()
	c.statusAddress = qt6.NewQLabel2()
	c.statusAddress.SetFont(monospaceFont())
	status.AddWidget(c.statusConn.QWidget)
	status.AddWidget(verticalLine().QWidget)
	status.AddWidget(c.statusVM.QWidget)
	status.AddWidget(verticalLine().QWidget)
	status.AddWidget(c.statusAddress.QWidget)
	status.AddPermanentWidget(mutedLabel("Shared session").QWidget)
}

func (c *consoleWindow) buildSendKeyMenu() *qt6.QMenu {
	menu := qt6.NewQMenu4("Send &Key", c.QWidget)
	add := func(label string, keysyms ...uint32) {
		menu.AddAction(c.mw.action(label, "", 0, "", func() { c.vnc.sendSequence(keysyms...) }))
	}
	add("Ctrl+Alt+Del", rfb.KeyControlL, rfb.KeyAltL, rfb.KeyDelete)
	add("Ctrl+Alt+Backspace", rfb.KeyControlL, rfb.KeyAltL, rfb.KeyBackspace)
	add("Alt+F4", rfb.KeyAltL, rfb.KeyF(4))
	add("Alt+Tab", rfb.KeyAltL, rfb.KeyTab)
	add("Print Screen", rfb.KeyPrint)
	add("Escape", rfb.KeyEscape)
	menu.AddSeparator()
	consoles := qt6.NewQMenu4("Ctrl+Alt+F&n", c.QWidget)
	for n := 1; n <= 7; n++ {
		function := n
		consoles.AddAction(c.mw.action(fmt.Sprintf("Ctrl+Alt+F%d", function), "", 0, "", func() {
			c.vnc.sendSequence(rfb.KeyControlL, rfb.KeyAltL, rfb.KeyF(function))
		}))
	}
	menu.AddMenu(consoles)
	return menu
}

func (c *consoleWindow) setScale(scale bool) {
	c.vnc.setScale(scale)
	if c.scaleBox.IsChecked() != scale {
		c.scaleBox.SetChecked(scale)
	}
	c.mw.settings.ConsoleScale = &scale
	_ = c.mw.settings.save()
}

func (c *consoleWindow) toggleFullScreen() {
	if c.IsFullScreen() {
		c.ShowNormal()
		c.MenuBar().Show()
		c.toolbar.Show()
		c.StatusBar().Show()
		c.fullAction.SetChecked(false)
	} else {
		c.ShowFullScreen()
		c.MenuBar().Hide()
		c.toolbar.Hide()
		c.StatusBar().Hide()
		c.fullAction.SetChecked(true)
	}
}

// vmChanged applies a fresh VM record: window title, status bar, actions,
// and whether the display or the "off" page is shown.
func (c *consoleWindow) vmChanged(vm *apprunnerv1.VirtualMachine) {
	if c.closed {
		return
	}
	c.vm = vm
	c.SetWindowTitle(vm.GetName() + " — Console — App Runner")
	c.statusVM.SetText(fmt.Sprintf("%s · %d vCPU · %s", vmStatusLabel(vm.GetStatus()), vm.GetCpus(), formatMemory(vm.GetMemoryMib())))
	c.statusAddress.SetText(strings.Join(vmAddresses(vm), ", "))
	c.updateActions()
	if consoleAvailable(vm) {
		c.stack.SetCurrentWidget(c.vnc.QWidget)
		if c.client == nil && !c.connecting {
			c.connect()
		}
		return
	}
	c.offLabel.SetText(fmt.Sprintf("<h2>%s is %s</h2>%s", escape(vm.GetName()), escape(strings.ToLower(vmStatusLabel(vm.GetStatus()))), mutedHTML("Start the virtual machine to open its console.")))
	if vm.GetLastError() != "" {
		c.offLabel.SetText(c.offLabel.Text() + fmt.Sprintf(`<br><span style="color:%s">%s</span>`, colorNegative, escape(vm.GetLastError())))
	}
	c.stack.SetCurrentIndex(0)
	if c.client != nil || c.connecting {
		c.disconnect(true)
	}
	c.setConnection(colorInactive, "Not connected")
}

func (c *consoleWindow) updateActions() {
	vm := c.vm
	c.startAction.SetEnabled(!c.busy && !vmActive(vm))
	c.startBtn.SetEnabled(!c.busy && !vmActive(vm))
	c.shutdownAction.SetEnabled(!c.busy && vmRunning(vm))
	c.rebootAction.SetEnabled(!c.busy && vmRunning(vm))
	c.forceAction.SetEnabled(!c.busy && vmActive(vm))
	c.reconnectAction.SetEnabled(consoleAvailable(vm) && !c.connecting)
}

func (c *consoleWindow) setConnection(hex, text string) {
	c.statusConn.SetText(dotHTML(hex) + " " + escape(text))
}

func (c *consoleWindow) poll() {
	id := c.vm.GetId()
	call(func(ctx context.Context) (*apprunnerv1.VirtualMachine, error) {
		r, err := c.mw.client.api.GetVM(ctx, &apprunnerv1.GetVMRequest{Id: id})
		return r.GetVirtualMachine(), err
	}, func(vm *apprunnerv1.VirtualMachine, err error) {
		if err != nil || c.closed {
			return
		}
		c.vmChanged(vm)
	})
}

func (c *consoleWindow) connect() {
	if c.connecting || c.client != nil || c.closed {
		return
	}
	c.connecting = true
	c.expected = false
	c.generation++
	generation := c.generation
	c.setConnection(colorNeutral, "Connecting…")
	c.updateActions()
	url := c.mw.client.consoleURL(c.vm.GetConsolePath())
	fb := c.vnc
	go func() {
		dialCtx, dialCancel := context.WithTimeout(context.Background(), 10*time.Second)
		conn, err := rfb.DialHTTP(dialCtx, url)
		dialCancel()
		if err != nil {
			mainthread.Start(func() { c.connectFailed(generation, err) })
			return
		}
		client, err := rfb.Dial(conn, fb, true)
		if err != nil {
			mainthread.Start(func() { c.connectFailed(generation, err) })
			return
		}
		mainthread.Start(func() { c.connected(generation, client) })
		runErr := client.Run()
		mainthread.Start(func() { c.disconnected(generation, runErr) })
	}()
}

func (c *consoleWindow) connectFailed(generation int, err error) {
	if generation != c.generation || c.closed {
		return
	}
	c.connecting = false
	c.setConnection(colorNegative, "Disconnected")
	if !c.expected {
		c.banner.setActions(bannerAction{label: "Reconnect", handler: func() { c.connect() }})
		c.banner.show(messageError, escape("The console could not be opened: "+errorMessage(err)))
	}
	c.updateActions()
}

func (c *consoleWindow) connected(generation int, client *rfb.Client) {
	if generation != c.generation || c.closed {
		client.Close()
		return
	}
	c.connecting = false
	c.client = client
	c.vnc.setClient(client)
	c.banner.Hide()
	width, height := client.Size()
	c.setConnection(colorPositive, fmt.Sprintf("Connected · %s · %d×%d", c.vm.GetConsolePath(), width, height))
	c.updateActions()
	c.vnc.SetFocus()
}

func (c *consoleWindow) disconnected(generation int, err error) {
	if generation != c.generation || c.closed {
		return
	}
	c.client = nil
	c.connecting = false
	c.vnc.setClient(nil)
	c.setConnection(colorNegative, "Disconnected")
	if err != nil && !c.expected {
		c.banner.setActions(bannerAction{label: "Reconnect", handler: func() { c.connect() }})
		c.banner.show(messageError, escape("The console connection ended unexpectedly: "+errorMessage(err)))
	}
	c.updateActions()
	// A reboot keeps QEMU running; reconnect once the display is back.
	if err == nil && c.expected && consoleAvailable(c.vm) {
		c.expected = false
	}
}

func (c *consoleWindow) disconnect(expected bool) {
	c.expected = expected
	c.generation++
	if c.client != nil {
		c.client.Close()
		c.client = nil
		c.vnc.setClient(nil)
	}
	c.connecting = false
	c.updateActions()
}

// power runs a lifecycle operation; failures show in this window's banner.
func (c *consoleWindow) power(action string) {
	vm := c.vm
	switch action {
	case "force":
		if !confirm(c.QWidget, "Force Off", fmt.Sprintf("Force %s off? Unsaved guest data may be lost.", vm.GetName()), "Force Off", true) {
			return
		}
	case "reboot":
		if !confirm(c.QWidget, "Reboot", fmt.Sprintf("Reboot %s now?", vm.GetName()), "Reboot", false) {
			return
		}
	}
	api := c.mw.client.api
	id := vm.GetId()
	c.banner.Hide()
	c.busy = true
	c.updateActions()
	if action != "start" {
		c.expected = true
	}
	call(func(ctx context.Context) (*apprunnerv1.VirtualMachine, error) {
		switch action {
		case "start":
			r, err := api.StartVM(ctx, &apprunnerv1.StartVMRequest{Id: id})
			return r.GetVirtualMachine(), err
		case "shutdown":
			r, err := api.StopVM(ctx, &apprunnerv1.StopVMRequest{Id: id})
			return r.GetVirtualMachine(), err
		case "force":
			r, err := api.StopVM(ctx, &apprunnerv1.StopVMRequest{Id: id, Force: true})
			return r.GetVirtualMachine(), err
		default:
			r, err := api.RebootVM(ctx, &apprunnerv1.RebootVMRequest{Id: id})
			return r.GetVirtualMachine(), err
		}
	}, func(updated *apprunnerv1.VirtualMachine, err error) {
		c.busy = false
		if err != nil {
			c.expected = false
			c.banner.setActions()
			c.banner.show(messageError, escape(errorMessage(err)))
			c.updateActions()
			return
		}
		c.vmChanged(updated)
		c.mw.refresh()
	})
}
