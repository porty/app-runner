package main

import (
	"fmt"
	"strings"

	"github.com/mappu/miqt/qt6"

	apprunnerv1 "github.com/roflware/app-runner/internal/gen/apprunner/v1"
)

const (
	vmColumnName = iota
	vmColumnStatus
	vmColumnResources
	vmColumnNetwork
	vmColumnMedia
	vmColumnIPMI
)

// vmView is the Virtual Machines place: a details table with the machine
// toolbar, filter, and context menu.
type vmView struct {
	mw     *mainWindow
	root   *qt6.QWidget
	tb     *qt6.QToolBar
	filter *qt6.QLineEdit
	table  *qt6.QTreeWidget
	empty  *qt6.QLabel
	vms    []*apprunnerv1.VirtualMachine
	byID   map[string]*apprunnerv1.VirtualMachine
}

func newVMView(mw *mainWindow) *vmView {
	v := &vmView{mw: mw, byID: map[string]*apprunnerv1.VirtualMachine{}}
	v.root = qt6.NewQWidget2()
	layout := qt6.NewQVBoxLayout(v.root)
	layout.SetContentsMargins(0, 0, 0, 0)
	layout.SetSpacing(0)

	v.table = newTable("Name", "Status", "Resources", "Network", "Media", "IPMI")
	v.table.SetFrameShape(qt6.QFrame__NoFrame)
	v.table.SetIconSize(qt6.NewQSize2(12, 12))
	v.table.SetContextMenuPolicy(qt6.CustomContextMenu)
	header := v.table.Header()
	header.SetSectionResizeMode2(vmColumnName, qt6.QHeaderView__Interactive)
	header.SetSectionResizeMode2(vmColumnStatus, qt6.QHeaderView__ResizeToContents)
	header.SetSectionResizeMode2(vmColumnResources, qt6.QHeaderView__ResizeToContents)
	header.SetSectionResizeMode2(vmColumnNetwork, qt6.QHeaderView__Interactive)
	header.SetSectionResizeMode2(vmColumnMedia, qt6.QHeaderView__Stretch)
	header.SetSectionResizeMode2(vmColumnIPMI, qt6.QHeaderView__ResizeToContents)
	header.SetStretchLastSection(false)
	v.table.SetColumnWidth(vmColumnName, 220)
	v.table.SetColumnWidth(vmColumnNetwork, 230)
	v.table.OnItemSelectionChanged(func() { mw.updateActions() })
	v.table.OnItemDoubleClicked(func(item *qt6.QTreeWidgetItem, column int) {
		vm := v.byID[itemData(item)]
		if vm == nil {
			return
		}
		if consoleAvailable(vm) {
			mw.openConsole(vm)
		} else {
			mw.openSettings(vm)
		}
	})
	v.table.OnCustomContextMenuRequested(func(pos *qt6.QPoint) {
		if item := v.table.ItemAt(pos); item != nil {
			v.table.SetCurrentItem(item)
		}
		menu := qt6.NewQMenu(v.table.QWidget)
		m := mw.machine
		for _, a := range []*qt6.QAction{m.start, m.shutdown, m.reboot, m.forceStop, nil, m.console, m.clone, m.settings, nil, m.delete} {
			if a == nil {
				menu.AddSeparator()
			} else {
				menu.AddAction(a)
			}
		}
		menu.ExecWithPos(v.table.Viewport().MapToGlobal(qt6.NewQPointF3(float64(pos.X()), float64(pos.Y()))).ToPoint())
	})
	layout.AddWidget2(v.table.QWidget, 1)

	v.empty = richLabel("")
	v.empty.SetAlignment(qt6.AlignCenter)
	v.empty.Hide()
	layout.AddWidget2(v.empty.QWidget, 1)

	v.tb = qt6.NewQToolBar2("Machine")
	m := mw.machine
	for _, a := range []*qt6.QAction{m.newVM, m.start, m.shutdown, m.forceStop, nil, m.console, m.clone, m.settings} {
		if a == nil {
			v.tb.AddSeparator()
		} else {
			v.tb.AddAction(a)
		}
	}
	v.tb.AddWidget(spacer())
	v.filter = qt6.NewQLineEdit2()
	v.filter.SetPlaceholderText("Filter machines…")
	v.filter.SetClearButtonEnabled(true)
	v.filter.SetFixedWidth(240)
	v.filter.OnTextChanged(func(string) { v.applyFilter() })
	v.tb.AddWidget(v.filter.QWidget)
	return v
}

func (v *vmView) widget() *qt6.QWidget   { return v.root }
func (v *vmView) toolbar() *qt6.QToolBar { return v.tb }
func (v *vmView) title() string          { return "Virtual Machines" }

func (v *vmView) selected() *apprunnerv1.VirtualMachine {
	return v.byID[itemData(v.table.CurrentItem())]
}

func (v *vmView) selectVM(id string) {
	for i := 0; i < v.table.TopLevelItemCount(); i++ {
		if item := v.table.TopLevelItem(i); itemData(item) == id {
			v.table.SetCurrentItem(item)
			return
		}
	}
}

func (v *vmView) refresh(s *snapshot) {
	selectedID := itemData(v.table.CurrentItem())
	v.vms = s.vms
	v.byID = map[string]*apprunnerv1.VirtualMachine{}
	v.table.BlockSignals(true)
	v.table.Clear()
	for _, vm := range s.vms {
		v.byID[vm.GetId()] = vm
		item := qt6.NewQTreeWidgetItem2(vmRow(vm))
		setItemData(item, vm.GetId())
		setBold(item, vmColumnName)
		item.SetIcon(vmColumnStatus, dotIcon(vmStatusColor(vm.GetStatus())))
		if vm.GetLastError() != "" {
			item.SetToolTip(vmColumnName, vm.GetLastError())
			item.SetForeground(vmColumnStatus, qt6.NewQBrush3(color(colorNegative)))
		}
		if ipmi := vm.GetIpmi(); ipmi.GetEnabled() {
			setMono(item, vmColumnIPMI)
			if !ipmi.GetRunning() {
				item.SetForeground(vmColumnIPMI, qt6.NewQBrush3(color(colorNegative)))
				item.SetToolTip(vmColumnIPMI, "IPMI is enabled but not listening: "+firstNonEmpty(ipmi.GetLastError(), "not running"))
			} else {
				item.SetToolTip(vmColumnIPMI, fmt.Sprintf("ipmitool -I lanplus -H %s -U %s power status", ipmi.GetAddress(), ipmi.GetUsername()))
			}
		} else {
			setMuted(item, vmColumnIPMI)
		}
		if len(vm.GetNics()) == 0 {
			setMuted(item, vmColumnNetwork)
		}
		if len(vm.GetDisks()) == 0 && len(vm.GetCdroms()) == 0 {
			setMuted(item, vmColumnMedia)
		}
		v.table.AddTopLevelItem(item)
	}
	v.table.BlockSignals(false)
	v.applyFilter()
	if selectedID != "" {
		v.selectVM(selectedID)
	}
	if len(s.vms) == 0 && v.mw.loaded {
		v.empty.SetText(fmt.Sprintf("<h3>No virtual machines yet</h3>%s", mutedHTML("Create a VM from an installer ISO or a cloud image with Machine → New Machine…")))
		v.empty.Show()
		v.table.Hide()
	} else {
		v.empty.Hide()
		v.table.Show()
	}
}

func vmRow(vm *apprunnerv1.VirtualMachine) []string {
	name := vm.GetName()
	if vm.GetDescription() != "" {
		name += "\n" + vm.GetDescription()
	}
	if vm.GetLastError() != "" {
		name += "\n⚠ " + vm.GetLastError()
	}
	var network []string
	for _, nic := range vm.GetNics() {
		line := nicModeLabel(nic) + "  " + nic.GetMacAddress()
		for _, address := range nic.GetAddresses() {
			line += "\n" + address
		}
		network = append(network, line)
	}
	if len(network) == 0 {
		network = []string{"No network interfaces"}
	}
	var media []string
	for index, disk := range vm.GetDisks() {
		media = append(media, diskLabel(index, disk))
	}
	for _, cdrom := range vm.GetCdroms() {
		media = append(media, "CD-ROM · "+firstNonEmpty(cdrom.GetIsoName(), "empty"))
	}
	if len(media) == 0 {
		media = []string{"No media"}
	}
	ipmi := "—"
	if vm.GetIpmi().GetEnabled() {
		port := vm.GetIpmi().GetPort()
		if port == 0 {
			port = 623
		}
		ipmi = fmt.Sprintf("%s:%d", vm.GetIpmi().GetAddress(), port)
		if !vm.GetIpmi().GetRunning() {
			ipmi += "\nnot listening"
		}
	}
	return []string{name, vmStatusLabel(vm.GetStatus()), vmResources(vm), strings.Join(network, "\n"), strings.Join(media, "\n"), ipmi}
}

func (v *vmView) applyFilter() {
	needle := strings.ToLower(strings.TrimSpace(v.filter.Text()))
	for i := 0; i < v.table.TopLevelItemCount(); i++ {
		item := v.table.TopLevelItem(i)
		vm := v.byID[itemData(item)]
		haystack := strings.ToLower(strings.Join([]string{vm.GetName(), vm.GetDescription(), vmStatusLabel(vm.GetStatus()), strings.Join(vmAddresses(vm), " "), item.Text(vmColumnNetwork), item.Text(vmColumnMedia), item.Text(vmColumnIPMI)}, " "))
		item.SetHidden(needle != "" && !strings.Contains(haystack, needle))
	}
}

func (v *vmView) statusText() string {
	running, stopped, other := 0, 0, 0
	for _, vm := range v.vms {
		switch vm.GetStatus() {
		case apprunnerv1.VMStatus_VM_STATUS_RUNNING:
			running++
		case apprunnerv1.VMStatus_VM_STATUS_STOPPED:
			stopped++
		default:
			other++
		}
	}
	text := fmt.Sprintf("%s · %d running · %d stopped", pluralize(len(v.vms), "machine", "machines"), running, stopped)
	if other > 0 {
		text += fmt.Sprintf(" · %d other", other)
	}
	return text
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if value != "" {
			return value
		}
	}
	return ""
}
