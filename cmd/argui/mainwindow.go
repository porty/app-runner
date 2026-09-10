package main

import (
	"context"
	"fmt"
	"os"
	"strings"
	"sync"

	"github.com/mappu/miqt/qt6"

	apprunnerv1 "github.com/roflware/app-runner/internal/gen/apprunner/v1"
)

// snapshot is everything the window shows, fetched together on each refresh.
type snapshot struct {
	vms       []*apprunnerv1.VirtualMachine
	network   *apprunnerv1.NetworkingStatus
	host      *apprunnerv1.HostStatus
	storage   []*apprunnerv1.StorageRoot
	isos      []*apprunnerv1.ISOImage
	images    []*apprunnerv1.DiskImage
	templates []*apprunnerv1.CloudInitTemplate
}

func (s *snapshot) vm(id string) *apprunnerv1.VirtualMachine {
	for _, vm := range s.vms {
		if vm.GetId() == id {
			return vm
		}
	}
	return nil
}

func (s *snapshot) storageRoot(name string) *apprunnerv1.StorageRoot {
	for _, root := range s.storage {
		if root.GetName() == name {
			return root
		}
	}
	return nil
}

// view is one of the main window's places: it owns a widget in the central
// stack, a toolbar, and the text shown at the left of the status bar.
type view interface {
	widget() *qt6.QWidget
	toolbar() *qt6.QToolBar
	title() string
	refresh(s *snapshot)
	statusText() string
}

// machineActions are shared by the Machine menu, the toolbar, and the VM
// list's context menu.
type machineActions struct {
	newVM, start, shutdown, reboot, forceStop, console, clone, settings, delete *qt6.QAction
}

type networkActions struct {
	createBridge, configure, attach, upDown, deleteBridge, restore *qt6.QAction
}

type mainWindow struct {
	*qt6.QMainWindow
	client   *client
	settings *settings
	snapshot *snapshot
	loaded   bool
	busy     bool

	sidebar     *qt6.QTreeWidget
	stack       *qt6.QStackedWidget
	splitter    *qt6.QSplitter
	hostWarning *messageWidget
	banner      *messageWidget
	pending     *pendingBanner

	vmView         *vmView
	bridgeView     *bridgeView
	interfacesView *interfacesView
	storageView    *storageView
	cloudInitView  *cloudInitView
	views          []view
	current        view
	currentKey     string

	statusLeft, statusHost, statusCaps, statusRefresh, statusServer *qt6.QLabel

	machine                      machineActions
	network                      networkActions
	refreshAction, sidebarAction *qt6.QAction
	refreshGroup, schemeGroup    *qt6.QActionGroup

	timer      *qt6.QTimer
	refreshing bool
	consoles   map[string]*consoleWindow

	// initialView is shown once the first snapshot arrives; screenshot,
	// when set, saves the window to that path afterwards and quits.
	initialView string
	screenshot  string
	debugWindow *qt6.QWidget
}

func newMainWindow(c *client, prefs *settings) *mainWindow {
	mw := &mainWindow{QMainWindow: qt6.NewQMainWindow2(), client: c, settings: prefs, snapshot: &snapshot{}, consoles: map[string]*consoleWindow{}}
	mw.SetWindowTitle("App Runner")
	mw.SetWindowIcon(themeIcon("computer", qt6.QStyle__SP_ComputerIcon))
	mw.Resize(prefs.WindowWidth, prefs.WindowHeight)

	mw.buildActions()
	mw.vmView = newVMView(mw)
	mw.bridgeView = newBridgeView(mw)
	mw.interfacesView = newInterfacesView(mw)
	mw.storageView = newStorageView(mw)
	mw.cloudInitView = newCloudInitView(mw)
	mw.views = []view{mw.vmView, mw.bridgeView, mw.interfacesView, mw.storageView, mw.cloudInitView}

	mw.buildMenus()
	for _, v := range mw.views {
		tb := v.toolbar()
		tb.SetMovable(false)
		tb.SetToolButtonStyle(qt6.ToolButtonTextBesideIcon)
		mw.AddToolBarWithToolbar(tb)
		tb.Hide()
	}
	mw.buildCentral()
	mw.buildStatusBar()

	mw.timer = qt6.NewQTimer2(mw.QObject)
	mw.timer.OnTimeout(func() { mw.refresh() })
	mw.applyRefreshSpeed()

	mw.OnCloseEvent(func(super func(event *qt6.QCloseEvent), event *qt6.QCloseEvent) {
		mw.settings.WindowWidth = mw.Width()
		mw.settings.WindowHeight = mw.Height()
		if sizes := mw.splitter.Sizes(); len(sizes) > 0 && sizes[0] > 0 {
			mw.settings.SidebarWidth = sizes[0]
		}
		_ = mw.settings.save()
		for _, console := range mw.consoles {
			console.Close()
		}
		super(event)
	})

	mw.showView("vms")
	mw.refresh()
	return mw
}

func (mw *mainWindow) action(text, icon string, fallback qt6.QStyle__StandardPixmap, shortcut string, handler func()) *qt6.QAction {
	a := qt6.NewQAction5(text, mw.QObject)
	if icon != "" {
		a.SetIcon(themeIcon(icon, fallback))
	}
	if shortcut != "" {
		setKeyboardShortcut(a, shortcut)
	}
	if handler != nil {
		a.OnTriggered(handler)
	}
	return a
}

func (mw *mainWindow) buildActions() {
	m := &mw.machine
	m.newVM = mw.action("&New Machine…", "list-add", qt6.QStyle__SP_FileDialogNewFolder, "Ctrl+N", func() { mw.openWizard("", "") })
	m.start = mw.action("&Start", "media-playback-start", qt6.QStyle__SP_MediaPlay, "", func() { mw.startVM(mw.vmView.selected()) })
	m.shutdown = mw.action("Shut &Down", "system-shutdown", 0, "", func() { mw.stopVM(mw.vmView.selected(), false) })
	m.reboot = mw.action("&Reboot", "system-reboot", qt6.QStyle__SP_BrowserReload, "", func() { mw.rebootVM(mw.vmView.selected()) })
	m.forceStop = mw.action("&Force Stop", "process-stop", qt6.QStyle__SP_MediaStop, "", func() { mw.stopVM(mw.vmView.selected(), true) })
	m.console = mw.action("&Console", "utilities-terminal", qt6.QStyle__SP_ComputerIcon, "Ctrl+Return", func() { mw.openConsole(mw.vmView.selected()) })
	m.clone = mw.action("C&lone…", "edit-copy", 0, "", func() { mw.cloneVM(mw.vmView.selected()) })
	m.settings = mw.action("Se&ttings…", "configure", 0, "Ctrl+,", func() { mw.openSettings(mw.vmView.selected()) })
	m.delete = mw.action("D&elete", "edit-delete", qt6.QStyle__SP_TrashIcon, "", func() { mw.deleteVM(mw.vmView.selected()) })

	n := &mw.network
	n.createBridge = mw.action("Create &Bridge…", "list-add", 0, "", func() { mw.createBridge() })
	n.configure = mw.action("&Configure Bridge…", "configure", 0, "", func() { mw.configureBridge(mw.bridgeView.bridgeName) })
	n.attach = mw.action("&Attach Interface…", "network-wired", qt6.QStyle__SP_DriveNetIcon, "", func() { mw.attachInterface(mw.bridgeView.bridgeName) })
	n.upDown = mw.action("Bring &Down", "arrow-down", qt6.QStyle__SP_ArrowDown, "", func() { mw.toggleBridge(mw.bridgeView.bridgeName) })
	n.deleteBridge = mw.action("&Delete Bridge", "edit-delete", qt6.QStyle__SP_TrashIcon, "", func() { mw.deleteBridge(mw.bridgeView.bridgeName) })
	n.restore = mw.action("&Restore Bridge", "view-refresh", 0, "", func() { mw.restoreBridge(mw.bridgeView.bridgeName) })

	mw.refreshAction = mw.action("&Refresh", "view-refresh", qt6.QStyle__SP_BrowserReload, "F5", func() { mw.refresh() })
	mw.sidebarAction = mw.action("Show &Sidebar", "", 0, "F9", nil)
	mw.sidebarAction.SetCheckable(true)
	mw.sidebarAction.SetChecked(true)
	mw.sidebarAction.OnToggled(func(checked bool) { mw.sidebar.SetVisible(checked) })
}

func (mw *mainWindow) buildMenus() {
	bar := mw.MenuBar()

	file := bar.AddMenuWithTitle("&File")
	file.AddAction(mw.machine.newVM)
	file.AddAction(mw.action("&Add to Storage…", "document-import", 0, "Ctrl+U", func() { mw.storageView.openUpload("", "") }))
	file.AddSeparator()
	quit := mw.action("&Quit", "application-exit", 0, "Ctrl+Q", func() { mw.Close() })
	file.AddAction(quit)

	edit := bar.AddMenuWithTitle("&Edit")
	edit.AddAction(mw.action("&Copy IP Address", "edit-copy", 0, "Ctrl+Shift+C", func() { mw.copyAddress(mw.vmView.selected()) }))
	edit.AddAction(mw.action("&Filter Machines", "edit-find", 0, "Ctrl+F", func() {
		mw.showView("vms")
		mw.vmView.filter.SetFocus()
		mw.vmView.filter.SelectAll()
	}))

	viewMenu := bar.AddMenuWithTitle("&View")
	viewMenu.AddAction(mw.refreshAction)
	viewMenu.AddAction(mw.sidebarAction)
	viewMenu.AddSeparator()
	viewMenu.AddAction(mw.action("&Virtual Machines", "computer", 0, "Ctrl+1", func() { mw.showView("vms") }))
	viewMenu.AddAction(mw.action("&Host Interfaces", "network-wired", 0, "Ctrl+2", func() { mw.showView("interfaces") }))
	viewMenu.AddAction(mw.action("&ISO Storage", "media-optical", 0, "Ctrl+3", func() { mw.showView("storage:iso") }))
	viewMenu.AddAction(mw.action("&Disk Storage", "drive-harddisk", 0, "Ctrl+4", func() { mw.showView("storage:disk") }))
	viewMenu.AddAction(mw.action("&Cloud-init Templates", "cloud-upload", 0, "Ctrl+5", func() { mw.showView("cloudinit") }))

	machine := bar.AddMenuWithTitle("&Machine")
	for _, a := range []*qt6.QAction{mw.machine.newVM, nil, mw.machine.start, mw.machine.shutdown, mw.machine.reboot, mw.machine.forceStop, nil, mw.machine.console, mw.machine.clone, mw.machine.settings, nil, mw.machine.delete} {
		if a == nil {
			machine.AddSeparator()
		} else {
			machine.AddAction(a)
		}
	}

	network := bar.AddMenuWithTitle("&Network")
	for _, a := range []*qt6.QAction{mw.network.createBridge, nil, mw.network.configure, mw.network.attach, mw.network.upDown, mw.network.restore, nil, mw.network.deleteBridge} {
		if a == nil {
			network.AddSeparator()
		} else {
			network.AddAction(a)
		}
	}

	storage := bar.AddMenuWithTitle("&Storage")
	storage.AddAction(mw.action("&Upload…", "document-import", 0, "", func() { mw.storageView.openUpload(mw.storageView.rootName, "") }))
	storage.AddAction(mw.action("&Download from URL…", "download", 0, "", func() { mw.storageView.openDownload(mw.storageView.rootName, "") }))
	storage.AddSeparator()
	storage.AddAction(mw.action("New &Machine from Image…", "list-add", 0, "", func() { mw.storageView.newMachineFromSelection() }))

	settingsMenu := bar.AddMenuWithTitle("Se&ttings")
	refreshMenu := settingsMenu.AddMenuWithTitle("&Auto Update")
	mw.refreshGroup = qt6.NewQActionGroup(mw.QObject)
	for _, entry := range refreshSpeeds {
		speed := entry.speed
		a := qt6.NewQAction5(entry.label, mw.QObject)
		a.SetCheckable(true)
		a.SetChecked(speed == mw.settings.RefreshSpeed)
		a.OnTriggered(func() {
			mw.settings.RefreshSpeed = speed
			_ = mw.settings.save()
			mw.applyRefreshSpeed()
		})
		mw.refreshGroup.AddAction(a)
		refreshMenu.AddAction(a)
	}
	schemeMenu := settingsMenu.AddMenuWithTitle("&Colour Scheme")
	mw.schemeGroup = qt6.NewQActionGroup(mw.QObject)
	for _, entry := range []struct {
		scheme colourScheme
		label  string
	}{{schemeSystem, "&System"}, {schemeDark, "Breeze &Dark"}, {schemeLight, "Breeze &Light"}} {
		scheme := entry.scheme
		a := qt6.NewQAction5(entry.label, mw.QObject)
		a.SetCheckable(true)
		a.SetChecked(scheme == mw.settings.ColourScheme)
		a.OnTriggered(func() {
			mw.settings.ColourScheme = scheme
			_ = mw.settings.save()
			applyColourScheme(scheme)
		})
		mw.schemeGroup.AddAction(a)
		schemeMenu.AddAction(a)
	}

	help := bar.AddMenuWithTitle("&Help")
	help.AddAction(mw.action("&About App Runner", "help-about", 0, "", func() {
		qt6.QMessageBox_Information(mw.QWidget, "About App Runner", fmt.Sprintf("<b>App Runner</b><br>A local control plane for QEMU/KVM virtual machines.<br><br>Connected to %s", escape(mw.client.server)))
	}))
}

func (mw *mainWindow) buildCentral() {
	mw.splitter = qt6.NewQSplitter3(qt6.Horizontal)
	mw.splitter.SetChildrenCollapsible(false)

	mw.sidebar = qt6.NewQTreeWidget2()
	mw.sidebar.SetHeaderHidden(true)
	mw.sidebar.SetRootIsDecorated(false)
	mw.sidebar.SetIndentation(14)
	mw.sidebar.SetColumnCount(2)
	mw.sidebar.SetUniformRowHeights(true)
	mw.sidebar.SetFrameShape(qt6.QFrame__NoFrame)
	mw.sidebar.SetMinimumWidth(160)
	mw.sidebar.Header().SetStretchLastSection(false)
	mw.sidebar.Header().SetSectionResizeMode2(0, qt6.QHeaderView__Stretch)
	mw.sidebar.Header().SetSectionResizeMode2(1, qt6.QHeaderView__ResizeToContents)
	mw.sidebar.SetIconSize(qt6.NewQSize2(16, 16))
	mw.sidebar.OnCurrentItemChanged(func(current, previous *qt6.QTreeWidgetItem) {
		if key := itemData(current); key != "" && key != mw.currentKey {
			mw.showView(key)
		}
	})
	mw.splitter.AddWidget(mw.sidebar.QWidget)

	right := qt6.NewQWidget2()
	layout := qt6.NewQVBoxLayout(right)
	layout.SetContentsMargins(0, 0, 0, 0)
	layout.SetSpacing(0)
	mw.hostWarning = newMessageWidget(true)
	mw.hostWarning.onClose = func() {
		mw.settings.DismissedWarning = mw.hostWarningText()
		_ = mw.settings.save()
	}
	mw.banner = newMessageWidget(true)
	banners := qt6.NewQVBoxLayout2()
	banners.SetContentsMargins(8, 8, 8, 0)
	banners.SetSpacing(6)
	mw.pending = newPendingBanner(mw)
	banners.AddWidget(mw.hostWarning.QWidget)
	banners.AddWidget(mw.pending.QWidget)
	banners.AddWidget(mw.banner.QWidget)
	layout.AddLayout(banners.QLayout)
	mw.stack = qt6.NewQStackedWidget2()
	for _, v := range mw.views {
		mw.stack.AddWidget(v.widget())
	}
	layout.AddWidget2(mw.stack.QWidget, 1)
	mw.splitter.AddWidget(right)
	mw.splitter.SetStretchFactor(1, 1)
	mw.splitter.SetSizes([]int{mw.settings.SidebarWidth, max(mw.settings.WindowWidth-mw.settings.SidebarWidth, 400)})
	mw.SetCentralWidget(mw.splitter.QWidget)
	mw.rebuildSidebar()
}

func (mw *mainWindow) buildStatusBar() {
	bar := mw.StatusBar()
	bar.SetSizeGripEnabled(true)
	mw.statusLeft = qt6.NewQLabel2()
	mw.statusHost = inlineLabel("")
	mw.statusCaps = inlineLabel("")
	mw.statusRefresh = qt6.NewQLabel2()
	mw.statusServer = qt6.NewQLabel3(mw.client.hostLabel())
	mw.statusServer.SetToolTip(mw.client.server)
	bar.AddWidget(mw.statusLeft.QWidget)
	bar.AddWidget(verticalLine().QWidget)
	bar.AddWidget(mw.statusHost.QWidget)
	bar.AddWidget(verticalLine().QWidget)
	bar.AddWidget(mw.statusCaps.QWidget)
	bar.AddPermanentWidget(mw.statusRefresh.QWidget)
	bar.AddPermanentWidget(verticalLine().QWidget)
	bar.AddPermanentWidget(mw.statusServer.QWidget)
}

// sidebarEntry describes one place in the sidebar.
type sidebarEntry struct {
	key, label, icon string
	fallback         qt6.QStyle__StandardPixmap
	count            string
	muted            bool
}

func (mw *mainWindow) sidebarEntries() (sections []string, entries map[string][]sidebarEntry) {
	s := mw.snapshot
	entries = map[string][]sidebarEntry{}
	sections = []string{"Compute", "Network", "Storage"}
	entries["Compute"] = []sidebarEntry{{key: "vms", label: "Virtual Machines", icon: "computer", fallback: qt6.QStyle__SP_ComputerIcon, count: fmt.Sprint(len(s.vms))}}
	for _, bridge := range s.network.GetBridges() {
		entries["Network"] = append(entries["Network"], sidebarEntry{key: "bridge:" + bridge.GetName(), label: bridge.GetName(), icon: "network-workgroup", fallback: qt6.QStyle__SP_DriveNetIcon, count: fmt.Sprint(len(bridge.GetWorkloads()))})
	}
	entries["Network"] = append(entries["Network"], sidebarEntry{key: "interfaces", label: "Host interfaces", icon: "network-wired", fallback: qt6.QStyle__SP_DriveNetIcon, count: fmt.Sprint(len(s.network.GetInterfaces())), muted: true})
	countFiles := func(name string) string {
		root := s.storageRoot(name)
		if root == nil {
			return ""
		}
		return fmt.Sprint(countStorageFiles(root.GetEntries()))
	}
	entries["Storage"] = []sidebarEntry{
		{key: "storage:iso", label: "iso/", icon: "media-optical", fallback: qt6.QStyle__SP_DriveCDIcon, count: countFiles("iso")},
		{key: "storage:disk", label: "disk/", icon: "drive-harddisk", fallback: qt6.QStyle__SP_DriveHDIcon, count: countFiles("disk")},
		{key: "cloudinit", label: "Cloud-init templates", icon: "cloud-upload", fallback: qt6.QStyle__SP_FileIcon, count: fmt.Sprint(len(s.templates))},
	}
	return sections, entries
}

func countStorageFiles(entries []*apprunnerv1.StorageEntry) int {
	total := 0
	for _, entry := range entries {
		if entry.GetKind() == apprunnerv1.StorageEntryKind_STORAGE_ENTRY_KIND_DIRECTORY {
			total += countStorageFiles(entry.GetChildren())
		} else {
			total++
		}
	}
	return total
}

func (mw *mainWindow) rebuildSidebar() {
	mw.sidebar.BlockSignals(true)
	defer mw.sidebar.BlockSignals(false)
	mw.sidebar.Clear()
	sections, entries := mw.sidebarEntries()
	var selected *qt6.QTreeWidgetItem
	for _, section := range sections {
		header := qt6.NewQTreeWidgetItem2([]string{strings.ToUpper(section), ""})
		header.SetFlags(qt6.ItemIsEnabled)
		font := qt6.NewQFont()
		font.SetBold(true)
		font.SetPointSize(max(font.PointSize()-2, 7))
		header.SetFont(0, font)
		setMuted(header, 0)
		mw.sidebar.AddTopLevelItem(header)
		for _, entry := range entries[section] {
			item := qt6.NewQTreeWidgetItem2([]string{entry.label, entry.count})
			item.SetIcon(0, themeIcon(entry.icon, entry.fallback))
			item.SetTextAlignment(1, int(qt6.AlignRight|qt6.AlignVCenter))
			setMuted(item, 1)
			if entry.muted {
				setMuted(item, 0)
			}
			setItemData(item, entry.key)
			header.AddChild(item)
			if entry.key == mw.currentKey {
				selected = item
			}
		}
		header.SetExpanded(true)
	}
	if selected != nil {
		mw.sidebar.SetCurrentItem(selected)
	}
}

func (mw *mainWindow) viewForKey(key string) view {
	switch {
	case key == "vms":
		return mw.vmView
	case strings.HasPrefix(key, "bridge:"):
		mw.bridgeView.setBridge(strings.TrimPrefix(key, "bridge:"))
		return mw.bridgeView
	case key == "interfaces":
		return mw.interfacesView
	case strings.HasPrefix(key, "storage:"):
		mw.storageView.setRoot(strings.TrimPrefix(key, "storage:"))
		return mw.storageView
	case key == "cloudinit":
		return mw.cloudInitView
	}
	return nil
}

// showView switches the central area, toolbar, title, and sidebar selection
// to the place identified by key.
func (mw *mainWindow) showView(key string) {
	v := mw.viewForKey(key)
	if v == nil {
		return
	}
	if mw.current != nil && mw.current != v {
		mw.current.toolbar().Hide()
	}
	mw.current = v
	mw.currentKey = key
	v.toolbar().Show()
	mw.stack.SetCurrentWidget(v.widget())
	v.refresh(mw.snapshot)
	mw.updateTitle()
	mw.updateActions()
	mw.selectSidebar(key)
}

func (mw *mainWindow) selectSidebar(key string) {
	mw.sidebar.BlockSignals(true)
	defer mw.sidebar.BlockSignals(false)
	for i := 0; i < mw.sidebar.TopLevelItemCount(); i++ {
		header := mw.sidebar.TopLevelItem(i)
		for j := 0; j < header.ChildCount(); j++ {
			child := header.Child(j)
			if itemData(child) == key {
				mw.sidebar.SetCurrentItem(child)
				return
			}
		}
	}
}

func (mw *mainWindow) updateTitle() {
	if mw.current == nil {
		mw.SetWindowTitle("App Runner")
		return
	}
	mw.SetWindowTitle(mw.current.title() + " — App Runner")
	mw.statusLeft.SetText(mw.current.statusText())
}

func (mw *mainWindow) applyRefreshSpeed() {
	mw.statusRefresh.SetText("Auto update: " + refreshSpeedLabel(mw.settings.RefreshSpeed))
	mw.timer.Stop()
	if interval := refreshInterval(mw.settings.RefreshSpeed); interval > 0 {
		mw.timer.Start(interval)
	}
}

// refresh fetches a new snapshot. Failures are shown in the status bar and
// never replace an operation error in the banner.
func (mw *mainWindow) refresh() {
	if mw.refreshing {
		return
	}
	mw.refreshing = true
	c := mw.client
	call(func(ctx context.Context) (*snapshot, error) {
		s := &snapshot{}
		var wg sync.WaitGroup
		var mu sync.Mutex
		var firstErr error
		run := func(fn func() error) {
			wg.Add(1)
			go func() {
				defer wg.Done()
				if err := fn(); err != nil {
					mu.Lock()
					if firstErr == nil {
						firstErr = err
					}
					mu.Unlock()
				}
			}()
		}
		run(func() error {
			r, err := c.api.ListVMs(ctx, &apprunnerv1.ListVMsRequest{})
			s.vms = r.GetVirtualMachines()
			return err
		})
		run(func() error {
			r, err := c.api.GetNetworkingStatus(ctx, &apprunnerv1.GetNetworkingStatusRequest{})
			s.network = r.GetStatus()
			return err
		})
		run(func() error {
			r, err := c.api.GetHostStatus(ctx, &apprunnerv1.GetHostStatusRequest{})
			s.host = r.GetStatus()
			return err
		})
		run(func() error {
			r, err := c.api.ListStorage(ctx, &apprunnerv1.ListStorageRequest{})
			s.storage = r.GetRoots()
			return err
		})
		run(func() error {
			r, err := c.api.ListISOs(ctx, &apprunnerv1.ListISOsRequest{})
			s.isos = r.GetImages()
			return err
		})
		run(func() error {
			r, err := c.api.ListDiskImages(ctx, &apprunnerv1.ListDiskImagesRequest{})
			s.images = r.GetImages()
			return err
		})
		run(func() error {
			r, err := c.api.ListCloudInitTemplates(ctx, &apprunnerv1.ListCloudInitTemplatesRequest{})
			s.templates = r.GetTemplates()
			return err
		})
		wg.Wait()
		return s, firstErr
	}, func(s *snapshot, err error) {
		mw.refreshing = false
		if err != nil {
			mw.statusHost.SetText(dotHTML(colorNegative) + " Disconnected")
			mw.statusHost.SetToolTip(errorMessage(err))
			mw.statusCaps.SetText("")
			if !mw.loaded {
				mw.statusLeft.SetText("Cannot reach " + mw.client.server + ": " + errorMessage(err))
			}
			return
		}
		first := !mw.loaded
		mw.snapshot = s
		mw.loaded = true
		mw.applySnapshot()
		if first {
			mw.firstLoad()
		}
	})
}

func (mw *mainWindow) firstLoad() {
	if mw.initialView != "" && mw.initialView != mw.currentKey {
		mw.showView(mw.initialView)
	}
	mw.debugOpen(os.Getenv("ARGUI_OPEN"))
	if mw.screenshot == "" {
		return
	}
	path := mw.screenshot
	delay := qt6.NewQTimer2(mw.QObject)
	delay.SetSingleShot(true)
	delay.OnTimeout(func() {
		target := mw.QWidget
		if mw.debugWindow != nil {
			target = mw.debugWindow
		}
		if !target.Grab().Save(path) {
			fmt.Fprintln(os.Stderr, "argui: could not save screenshot to", path)
		}
		mw.Close()
	})
	delay.Start(1500)
}

// debugOpen opens a secondary window at startup for screenshots and manual
// testing: wizard[:PAGE], settings:NAME[:PAGE], console:NAME,
// bridge:NAME[:PAGE], or upload.
func (mw *mainWindow) debugOpen(spec string) {
	if spec == "" {
		return
	}
	parts := strings.Split(spec, ":")
	kind := parts[0]
	arg, page := "", 0
	if len(parts) > 1 {
		arg = parts[1]
	}
	if len(parts) > 2 {
		fmt.Sscan(parts[2], &page)
	}
	vmByName := func() *apprunnerv1.VirtualMachine {
		for _, vm := range mw.snapshot.vms {
			if vm.GetName() == arg {
				return vm
			}
		}
		return nil
	}
	switch kind {
	case "wizard":
		w := mw.openWizard("", "")
		fmt.Sscan(arg, &page)
		if page > 0 {
			w.name.SetText("k0-node3")
			for i := 0; i < page; i++ {
				w.Next()
			}
		}
		mw.debugWindow = w.QWidget
	case "settings":
		if d := mw.openSettings(vmByName()); d != nil {
			d.pages.SetCurrentRow(page)
			mw.debugWindow = d.QWidget
		}
	case "console":
		mw.debugWindow = mw.openConsole(vmByName())
	case "bridge":
		if d := openBridgeDialog(mw, arg); d != nil {
			d.pages.SetCurrentRow(page)
			mw.debugWindow = d.QWidget
		}
	case "upload":
		mw.debugWindow = mw.storageView.openUpload("iso", "")
	}
}

func (mw *mainWindow) applySnapshot() {
	s := mw.snapshot
	mw.rebuildSidebar()
	for _, v := range mw.views {
		v.refresh(s)
	}
	mw.updateTitle()
	mw.updateActions()
	mw.updateHostStatus()
	mw.updateHostWarning()
	mw.pending.update(s.network.GetPendingChange())
	for id, console := range mw.consoles {
		if vm := s.vm(id); vm != nil {
			console.vmChanged(vm)
		}
	}
}

func (mw *mainWindow) updateHostStatus() {
	host := mw.snapshot.host
	switch {
	case host.GetQemuAvailable() && host.GetKvmAvailable():
		mw.statusHost.SetText(dotHTML(colorPositive) + " QEMU · /dev/kvm")
		mw.statusHost.SetToolTip("QEMU is installed and KVM acceleration is available")
	case host.GetQemuAvailable():
		mw.statusHost.SetText(dotHTML(colorNeutral) + " QEMU · no KVM")
		mw.statusHost.SetToolTip("QEMU is installed but /dev/kvm is not available; guests run without hardware acceleration")
	default:
		mw.statusHost.SetText(dotHTML(colorNegative) + " QEMU missing")
		mw.statusHost.SetToolTip("QEMU is not installed on the host")
	}
	user := mw.snapshot.network.GetUser()
	if user.GetHasCapNetAdmin() || user.GetIsRoot() {
		mw.statusCaps.SetText(dotHTML(colorPositive) + " CAP_NET_ADMIN")
		mw.statusCaps.SetToolTip(fmt.Sprintf("Backend identity %s (UID %d) can manage host networking", user.GetUsername(), user.GetUid()))
	} else {
		mw.statusCaps.SetText(dotHTML(colorNeutral) + " No CAP_NET_ADMIN")
		mw.statusCaps.SetToolTip(fmt.Sprintf("Backend identity %s (UID %d) cannot modify bridges; diagnostics only", user.GetUsername(), user.GetUid()))
	}
}

func (mw *mainWindow) hostWarningText() string {
	bridges := mw.snapshot.network.GetBridges()
	if len(bridges) == 0 {
		return "No Linux bridges are available for bridged workloads."
	}
	for _, bridge := range bridges {
		if bridge.GetUsableByQemu() {
			return ""
		}
	}
	return fmt.Sprintf("%s detected, but none currently pass the QEMU access diagnostics.", pluralize(len(bridges), "bridge", "bridges"))
}

func (mw *mainWindow) updateHostWarning() {
	text := mw.hostWarningText()
	if text == "" || text == mw.settings.DismissedWarning {
		mw.hostWarning.Hide()
		return
	}
	mw.hostWarning.show(messageWarning, "<b>Bridge networking is unavailable.</b> "+escape(text))
	mw.hostWarning.setActions(bannerAction{label: "Diagnostics", handler: func() { mw.showView("interfaces") }})
}

// showError displays an operation failure until dismissed or another
// operation starts; background refreshes leave it alone.
func (mw *mainWindow) showError(text string) {
	mw.banner.setActions()
	mw.banner.show(messageError, escape(text))
}

func (mw *mainWindow) showNotice(text string) {
	mw.banner.setActions()
	mw.banner.show(messagePositive, escape(text))
}

func (mw *mainWindow) clearBanner() {
	mw.banner.Hide()
}

func (mw *mainWindow) setBusy(busy bool) {
	mw.busy = busy
	mw.updateActions()
}

// operation runs an API call off the main thread, then refreshes. A
// non-empty notice is shown on success.
func (mw *mainWindow) operation(notice string, fn func(ctx context.Context) error) {
	mw.clearBanner()
	mw.setBusy(true)
	call(func(ctx context.Context) (struct{}, error) { return struct{}{}, fn(ctx) }, func(_ struct{}, err error) {
		mw.setBusy(false)
		if err != nil {
			mw.showError(errorMessage(err))
		} else if notice != "" {
			mw.showNotice(notice)
		}
		mw.refresh()
	})
}

func (mw *mainWindow) updateActions() {
	vm := mw.vmView.selected()
	m := mw.machine
	m.newVM.SetEnabled(!mw.busy)
	m.start.SetEnabled(!mw.busy && vm != nil && !vmActive(vm))
	m.shutdown.SetEnabled(!mw.busy && vm != nil && vmRunning(vm))
	m.reboot.SetEnabled(!mw.busy && vm != nil && vmRunning(vm))
	m.forceStop.SetEnabled(!mw.busy && vm != nil && vmActive(vm))
	m.console.SetEnabled(vm != nil && consoleAvailable(vm))
	m.clone.SetEnabled(!mw.busy && vm != nil && vm.GetStatus() == apprunnerv1.VMStatus_VM_STATUS_STOPPED)
	m.settings.SetEnabled(!mw.busy && vm != nil)
	m.delete.SetEnabled(!mw.busy && vm != nil && !vmActive(vm))

	network := mw.snapshot.network
	canManage := !mw.busy && network.GetCanManage() && network.GetPendingChange() == nil
	bridge := findBridge(network, mw.bridgeView.bridgeName)
	onBridge := mw.current == mw.bridgeView && bridge != nil
	n := mw.network
	n.createBridge.SetEnabled(canManage)
	n.configure.SetEnabled(onBridge && canManage)
	attachable := false
	for _, iface := range network.GetInterfaces() {
		attachable = attachable || iface.GetCanAttach()
	}
	n.attach.SetEnabled(onBridge && canManage && attachable)
	n.upDown.SetEnabled(onBridge && canManage && !bridgeMissing(bridge))
	if bridge != nil && bridge.GetIsUp() {
		n.upDown.SetText("Bring &Down")
		n.upDown.SetIcon(themeIcon("arrow-down", qt6.QStyle__SP_ArrowDown))
	} else {
		n.upDown.SetText("Bring &Up")
		n.upDown.SetIcon(themeIcon("arrow-up", qt6.QStyle__SP_ArrowUp))
	}
	n.restore.SetVisible(onBridge && bridgeMissing(bridge))
	n.restore.SetEnabled(onBridge && canManage && bridgeMissing(bridge))
	n.deleteBridge.SetEnabled(onBridge && canManage && len(bridge.GetMemberInterfaces()) == 0 && len(bridge.GetWorkloads()) == 0 && !bridge.GetDhcp().GetEnabled())
	mw.updateTitle()
}

// Machine operations.

func (mw *mainWindow) startVM(vm *apprunnerv1.VirtualMachine) {
	if vm == nil {
		return
	}
	mw.operation("", func(ctx context.Context) error {
		_, err := mw.client.api.StartVM(ctx, &apprunnerv1.StartVMRequest{Id: vm.GetId()})
		return err
	})
}

func (mw *mainWindow) stopVM(vm *apprunnerv1.VirtualMachine, force bool) {
	if vm == nil {
		return
	}
	if force && !confirm(mw.QWidget, "Force Stop", fmt.Sprintf("Force %s off? Unsaved guest data may be lost.", vm.GetName()), "Force Off", true) {
		return
	}
	mw.operation("", func(ctx context.Context) error {
		_, err := mw.client.api.StopVM(ctx, &apprunnerv1.StopVMRequest{Id: vm.GetId(), Force: force})
		return err
	})
}

func (mw *mainWindow) rebootVM(vm *apprunnerv1.VirtualMachine) {
	if vm == nil || !confirm(mw.QWidget, "Reboot", fmt.Sprintf("Reboot %s now?", vm.GetName()), "Reboot", false) {
		return
	}
	mw.operation("", func(ctx context.Context) error {
		_, err := mw.client.api.RebootVM(ctx, &apprunnerv1.RebootVMRequest{Id: vm.GetId()})
		return err
	})
}

func (mw *mainWindow) deleteVM(vm *apprunnerv1.VirtualMachine) {
	if vm == nil || !confirm(mw.QWidget, "Delete Virtual Machine", fmt.Sprintf("Delete %s and its disks? This cannot be undone.", vm.GetName()), "Delete", true) {
		return
	}
	mw.operation(fmt.Sprintf("Deleted %s.", vm.GetName()), func(ctx context.Context) error {
		_, err := mw.client.api.DeleteVM(ctx, &apprunnerv1.DeleteVMRequest{Id: vm.GetId()})
		return err
	})
}

func (mw *mainWindow) cloneVM(vm *apprunnerv1.VirtualMachine) {
	if vm == nil {
		return
	}
	name, ok := cloneDialog(mw.QWidget, vm, mw.snapshot.vms)
	if !ok {
		return
	}
	mw.operation(fmt.Sprintf("Cloned %s as %s.", vm.GetName(), name), func(ctx context.Context) error {
		_, err := mw.client.slow.CloneVM(ctx, &apprunnerv1.CloneVMRequest{Id: vm.GetId(), Name: name})
		return err
	})
}

func (mw *mainWindow) copyAddress(vm *apprunnerv1.VirtualMachine) {
	if vm == nil {
		return
	}
	addresses := vmAddresses(vm)
	if len(addresses) == 0 {
		mw.StatusBar().ShowMessage2(vm.GetName()+" has no leased IP address", 4000)
		return
	}
	qt6.QGuiApplication_Clipboard().SetText(addresses[0])
	mw.StatusBar().ShowMessage2("Copied "+addresses[0], 4000)
}

func (mw *mainWindow) openSettings(vm *apprunnerv1.VirtualMachine) *settingsDialog {
	if vm == nil {
		return nil
	}
	return openSettingsDialog(mw, vm.GetId())
}

func (mw *mainWindow) openWizard(sourceRoot, sourcePath string) *newMachineWizard {
	return openNewMachineWizard(mw, wizardSeed{sourceRoot: sourceRoot, sourcePath: sourcePath})
}

func (mw *mainWindow) openWizardWithTemplate(template *apprunnerv1.CloudInitTemplate) *newMachineWizard {
	return openNewMachineWizard(mw, wizardSeed{template: template})
}

func (mw *mainWindow) openConsole(vm *apprunnerv1.VirtualMachine) *qt6.QWidget {
	if vm == nil {
		return nil
	}
	if console, ok := mw.consoles[vm.GetId()]; ok {
		console.Show()
		console.Raise()
		console.ActivateWindow()
		return console.QWidget
	}
	console := newConsoleWindow(mw, vm)
	mw.consoles[vm.GetId()] = console
	console.onClosed = func() { delete(mw.consoles, vm.GetId()) }
	console.Show()
	return console.QWidget
}

// Network operations.

func (mw *mainWindow) applyNetworkChange(change *apprunnerv1.ApplyNetworkChangeRequest) {
	mw.operation("", func(ctx context.Context) error {
		_, err := mw.client.api.ApplyNetworkChange(ctx, change)
		return err
	})
}

func (mw *mainWindow) createBridge() {
	name, ok := createBridgeDialog(mw.QWidget)
	if !ok {
		return
	}
	mw.applyNetworkChange(&apprunnerv1.ApplyNetworkChangeRequest{Type: apprunnerv1.NetworkChangeType_NETWORK_CHANGE_TYPE_CREATE_BRIDGE, BridgeName: name})
}

func (mw *mainWindow) attachInterface(bridgeName string) {
	var attachable []*apprunnerv1.NetworkInterface
	for _, iface := range mw.snapshot.network.GetInterfaces() {
		if iface.GetCanAttach() {
			attachable = append(attachable, iface)
		}
	}
	ifaceName, migrate, ok := attachInterfaceDialog(mw.QWidget, bridgeName, attachable)
	if !ok {
		return
	}
	mw.applyNetworkChange(&apprunnerv1.ApplyNetworkChangeRequest{Type: apprunnerv1.NetworkChangeType_NETWORK_CHANGE_TYPE_ATTACH_INTERFACE, BridgeName: bridgeName, InterfaceName: ifaceName, MigrateAddresses: migrate})
}

func (mw *mainWindow) detachInterface(bridgeName, ifaceName string) {
	mw.applyNetworkChange(&apprunnerv1.ApplyNetworkChangeRequest{Type: apprunnerv1.NetworkChangeType_NETWORK_CHANGE_TYPE_DETACH_INTERFACE, BridgeName: bridgeName, InterfaceName: ifaceName})
}

func (mw *mainWindow) toggleBridge(bridgeName string) {
	bridge := findBridge(mw.snapshot.network, bridgeName)
	if bridge == nil {
		return
	}
	kind := apprunnerv1.NetworkChangeType_NETWORK_CHANGE_TYPE_SET_BRIDGE_UP
	if bridge.GetIsUp() {
		kind = apprunnerv1.NetworkChangeType_NETWORK_CHANGE_TYPE_SET_BRIDGE_DOWN
	}
	mw.applyNetworkChange(&apprunnerv1.ApplyNetworkChangeRequest{Type: kind, BridgeName: bridgeName})
}

func (mw *mainWindow) deleteBridge(bridgeName string) {
	if !confirm(mw.QWidget, "Delete Bridge", fmt.Sprintf("Delete bridge %s? The change will still require confirmation.", bridgeName), "Delete", true) {
		return
	}
	mw.applyNetworkChange(&apprunnerv1.ApplyNetworkChangeRequest{Type: apprunnerv1.NetworkChangeType_NETWORK_CHANGE_TYPE_DELETE_BRIDGE, BridgeName: bridgeName})
}

// restoreBridge recreates a bridge that App Runner remembers but the host
// lacks. It has no members or addresses, so it is confirmed immediately.
func (mw *mainWindow) restoreBridge(bridgeName string) {
	mw.operation(fmt.Sprintf("Restored bridge %s.", bridgeName), func(ctx context.Context) error {
		pending, err := mw.client.api.ApplyNetworkChange(ctx, &apprunnerv1.ApplyNetworkChangeRequest{Type: apprunnerv1.NetworkChangeType_NETWORK_CHANGE_TYPE_CREATE_BRIDGE, BridgeName: bridgeName})
		if err != nil {
			return err
		}
		_, err = mw.client.api.ConfirmNetworkChange(ctx, &apprunnerv1.ConfirmNetworkChangeRequest{Id: pending.GetPendingChange().GetId()})
		return err
	})
}

func (mw *mainWindow) confirmNetworkChange(id string) {
	mw.operation("", func(ctx context.Context) error {
		_, err := mw.client.api.ConfirmNetworkChange(ctx, &apprunnerv1.ConfirmNetworkChangeRequest{Id: id})
		return err
	})
}

func (mw *mainWindow) revertNetworkChange(id string) {
	mw.operation("", func(ctx context.Context) error {
		_, err := mw.client.api.RevertNetworkChange(ctx, &apprunnerv1.RevertNetworkChangeRequest{Id: id})
		return err
	})
}

func (mw *mainWindow) configureBridge(bridgeName string) {
	if bridgeName == "" {
		return
	}
	openBridgeDialog(mw, bridgeName)
}
