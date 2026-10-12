package main

import (
	"context"
	"fmt"
	"strings"

	"github.com/mappu/miqt/qt6"

	apprunnerv1 "github.com/roflware/app-runner/internal/gen/apprunner/v1"
)

// settingsDialog is the KPageDialog-style machine settings window. General,
// IPMI, and cloud-init changes are collected and applied together; device
// changes (disks, CD-ROMs, NICs) apply immediately like the web UI.
type settingsDialog struct {
	*qt6.QDialog
	mw   *mainWindow
	vm   *apprunnerv1.VirtualMachine
	s    *snapshot
	busy bool

	pages  *qt6.QListWidget
	stack  *qt6.QStackedWidget
	state  *messageWidget
	errors *messageWidget

	name        *qt6.QLineEdit
	description *qt6.QPlainTextEdit
	cpus        *qt6.QSpinBox
	memory      *qt6.QSpinBox
	cpuHint     *qt6.QLabel
	memoryHint  *qt6.QLabel
	firmware    *qt6.QLabel

	diskTable   *qt6.QTreeWidget
	addDisk     *qt6.QPushButton
	removeDisk  *qt6.QPushButton
	cdromTable  *qt6.QTreeWidget
	addCDROM    *qt6.QPushButton
	ejectCDROM  *qt6.QPushButton
	removeCDROM *qt6.QPushButton

	nicTable     *qt6.QTreeWidget
	addNIC       *qt6.QPushButton
	editNIC      *qt6.QPushButton
	removeNIC    *qt6.QPushButton
	leaseInfo    *qt6.QLabel
	ipmi         *qt6.QGroupBox
	ipmiBridge   *qt6.QComboBox
	ipmiAddress  *qt6.QLineEdit
	ipmiUser     *qt6.QLineEdit
	ipmiPassword *qt6.QLineEdit
	ipmiHint     *qt6.QLabel
	ipmiStatus   *qt6.QLabel
	ipmiCommand  *qt6.QLabel

	cloudInit     *qt6.QCheckBox
	template      *qt6.QComboBox
	userData      *qt6.QPlainTextEdit
	cloudInitNote *qt6.QLabel

	okButton, applyButton, cancelButton *qt6.QPushButton
}

func openSettingsDialog(mw *mainWindow, id string) *settingsDialog {
	vm := mw.snapshot.vm(id)
	if vm == nil {
		return nil
	}
	d := &settingsDialog{QDialog: qt6.NewQDialog(mw.QWidget), mw: mw, vm: vm, s: mw.snapshot}
	d.SetWindowTitle("Settings — " + vm.GetName())
	d.SetAttribute(qt6.WA_DeleteOnClose)
	d.Resize(860, 620)
	d.build()
	d.load(vm)
	d.Show()
	return d
}

func (d *settingsDialog) build() {
	outer := qt6.NewQVBoxLayout(d.QWidget)
	outer.SetContentsMargins(0, 0, 0, 0)
	outer.SetSpacing(0)
	split := qt6.NewQHBoxLayout2()
	split.SetSpacing(0)
	d.pages = qt6.NewQListWidget2()
	d.pages.SetFixedWidth(170)
	d.pages.SetFrameShape(qt6.QFrame__NoFrame)
	d.pages.SetIconSize(qt6.NewQSize2(16, 16))
	for _, page := range []struct{ label, icon string }{{"General", "configure"}, {"Disks & CD-ROM", "drive-harddisk"}, {"Network", "network-wired"}, {"Cloud-init", "cloud-upload"}} {
		item := qt6.NewQListWidgetItem3(themeIcon(page.icon, 0), page.label)
		d.pages.AddItemWithItem(item)
	}
	split.AddWidget(d.pages.QWidget)
	right := qt6.NewQWidget2()
	rightLayout := qt6.NewQVBoxLayout(right)
	rightLayout.SetContentsMargins(14, 12, 14, 8)
	rightLayout.SetSpacing(10)
	d.state = newMessageWidget(false)
	d.errors = newMessageWidget(true)
	rightLayout.AddWidget(d.state.QWidget)
	rightLayout.AddWidget(d.errors.QWidget)
	d.stack = qt6.NewQStackedWidget2()
	d.stack.AddWidget(d.buildGeneral())
	d.stack.AddWidget(d.buildStorage())
	d.stack.AddWidget(d.buildNetwork())
	d.stack.AddWidget(d.buildCloudInit())
	rightLayout.AddWidget2(d.stack.QWidget, 1)
	split.AddWidget2(right, 1)
	outer.AddLayout2(split.QLayout, 1)
	d.pages.OnCurrentRowChanged(func(row int) { d.stack.SetCurrentIndex(row) })
	d.pages.SetCurrentRow(0)

	buttons := qt6.NewQHBoxLayout2()
	buttons.SetContentsMargins(14, 8, 14, 12)
	help := qt6.NewQPushButton3("Help")
	help.OnClicked(func() {
		qt6.QMessageBox_Information(d.QWidget, "Machine Settings", "Name, description, and IPMI changes are immediate; CPU and memory changes apply on the next start. Disks, CD-ROM devices, and network interfaces can be added or removed while the machine is stopped, and CD-ROM media can be changed at any time.")
	})
	buttons.AddWidget(help.QWidget)
	buttons.AddStretch()
	d.okButton = qt6.NewQPushButton3("OK")
	d.okButton.SetDefault(true)
	d.okButton.OnClicked(func() { d.apply(true) })
	d.applyButton = qt6.NewQPushButton3("Apply")
	d.applyButton.OnClicked(func() { d.apply(false) })
	d.cancelButton = qt6.NewQPushButton3("Cancel")
	d.cancelButton.OnClicked(func() { d.Reject() })
	buttons.AddWidget(d.okButton.QWidget)
	buttons.AddWidget(d.applyButton.QWidget)
	buttons.AddWidget(d.cancelButton.QWidget)
	outer.AddLayout(buttons.QLayout)
}

func (d *settingsDialog) buildGeneral() *qt6.QWidget {
	page := qt6.NewQWidget2()
	layout := qt6.NewQVBoxLayout(page)
	layout.SetContentsMargins(0, 0, 0, 0)
	form := qt6.NewQFormLayout2()
	fillForm(form)
	d.name = qt6.NewQLineEdit2()
	d.name.SetMaxLength(63)
	form.AddRow3("Name:", d.name.QWidget)
	form.AddRow3("", hint("Used as the display name and the Auto DNS hostname.").QWidget)
	d.description = qt6.NewQPlainTextEdit2()
	d.description.SetMaximumHeight(80)
	form.AddRow3("Description:", d.description.QWidget)
	d.cpus = qt6.NewQSpinBox2()
	d.cpus.SetRange(1, maxCPUs)
	d.cpus.SetSuffix(" vCPU")
	cpuRow := qt6.NewQHBoxLayout2()
	cpuRow.AddWidget(d.cpus.QWidget)
	d.cpuHint = inlineLabel("")
	cpuRow.AddWidget(d.cpuHint.QWidget)
	cpuRow.AddStretch()
	form.AddRow4("Processors:", cpuRow.QLayout)
	d.memory = qt6.NewQSpinBox2()
	d.memory.SetRange(minMemoryMiB, maxMemoryMiB)
	d.memory.SetSingleStep(256)
	d.memory.SetSuffix(" MiB")
	memoryRow := qt6.NewQHBoxLayout2()
	memoryRow.AddWidget(d.memory.QWidget)
	d.memoryHint = inlineLabel("")
	memoryRow.AddWidget(d.memoryHint.QWidget)
	memoryRow.AddStretch()
	form.AddRow4("Memory:", memoryRow.QLayout)
	d.firmware = qt6.NewQLabel2()
	form.AddRow3("Firmware:", d.firmware.QWidget)
	form.AddRow3("", hint("Firmware is fixed once the machine exists; its OVMF variable store lives in state/.").QWidget)
	layout.AddLayout(form.QLayout)
	layout.AddStretch()
	for _, edit := range []*qt6.QLineEdit{d.name} {
		edit.OnTextChanged(func(string) { d.updateButtons() })
	}
	d.description.OnTextChanged(func() { d.updateButtons() })
	d.cpus.OnValueChanged(func(int) { d.updateButtons() })
	d.memory.OnValueChanged(func(int) { d.updateButtons() })
	return page
}

func (d *settingsDialog) buildStorage() *qt6.QWidget {
	page := qt6.NewQWidget2()
	layout := qt6.NewQVBoxLayout(page)
	layout.SetContentsMargins(0, 0, 0, 0)
	layout.AddWidget(headingLabel("Disks").QWidget)
	layout.AddWidget(mutedLabel("The first disk is the boot disk. Disks can only be added or removed while the machine is stopped; removing a disk deletes its data.").QWidget)
	d.diskTable = newTable("Device", "Size", "Backing image", "File")
	d.diskTable.Header().SetSectionResizeMode2(0, qt6.QHeaderView__ResizeToContents)
	d.diskTable.Header().SetSectionResizeMode2(1, qt6.QHeaderView__ResizeToContents)
	d.diskTable.OnItemSelectionChanged(func() { d.updateButtons() })
	layout.AddWidget2(d.diskTable.QWidget, 1)
	diskButtons := qt6.NewQHBoxLayout2()
	d.addDisk = qt6.NewQPushButton4(themeIcon("list-add", 0), "Add Disk…")
	d.addDisk.OnClicked(func() {
		spec, ok := diskDialog(d.QWidget, d.s.images)
		if !ok {
			return
		}
		d.device(func(ctx context.Context) (*apprunnerv1.VirtualMachine, error) {
			r, err := d.mw.client.slow.AddVMDisk(ctx, &apprunnerv1.AddVMDiskRequest{Id: d.vm.GetId(), Disk: spec})
			return r.GetVirtualMachine(), err
		})
	})
	d.removeDisk = qt6.NewQPushButton3("Remove")
	d.removeDisk.OnClicked(func() {
		index := d.diskTable.IndexOfTopLevelItem(d.diskTable.CurrentItem())
		if index < 0 || index >= len(d.vm.GetDisks()) {
			return
		}
		disk := d.vm.GetDisks()[index]
		if !confirm(d.QWidget, "Remove Disk", fmt.Sprintf("Remove this %d GiB disk? Its data will be permanently deleted.", disk.GetSizeGib()), "Remove Disk", true) {
			return
		}
		d.device(func(ctx context.Context) (*apprunnerv1.VirtualMachine, error) {
			r, err := d.mw.client.api.RemoveVMDisk(ctx, &apprunnerv1.RemoveVMDiskRequest{Id: d.vm.GetId(), DiskId: disk.GetId()})
			return r.GetVirtualMachine(), err
		})
	})
	diskButtons.AddWidget(d.addDisk.QWidget)
	diskButtons.AddWidget(d.removeDisk.QWidget)
	diskButtons.AddStretch()
	layout.AddLayout(diskButtons.QLayout)

	layout.AddWidget(headingLabel("CD-ROM drives").QWidget)
	layout.AddWidget(mutedLabel("Changing or ejecting media applies immediately, even while running. Adding or removing a drive requires the machine to be stopped.").QWidget)
	d.cdromTable = newTable("Device", "ISO media")
	d.cdromTable.Header().SetSectionResizeMode2(0, qt6.QHeaderView__ResizeToContents)
	d.cdromTable.OnItemSelectionChanged(func() { d.updateButtons() })
	layout.AddWidget2(d.cdromTable.QWidget, 1)
	cdromButtons := qt6.NewQHBoxLayout2()
	d.addCDROM = qt6.NewQPushButton4(themeIcon("list-add", 0), "Add CD-ROM…")
	d.addCDROM.OnClicked(func() {
		iso, ok := cdromDialog(d.QWidget, d.s.isos)
		if !ok {
			return
		}
		d.device(func(ctx context.Context) (*apprunnerv1.VirtualMachine, error) {
			r, err := d.mw.client.api.AddVMCDROM(ctx, &apprunnerv1.AddVMCDROMRequest{Id: d.vm.GetId(), IsoName: iso})
			return r.GetVirtualMachine(), err
		})
	})
	d.ejectCDROM = qt6.NewQPushButton4(themeIcon("media-eject", 0), "Eject")
	d.ejectCDROM.OnClicked(func() {
		if cdrom := d.selectedCDROM(); cdrom != nil {
			d.changeCDROM(cdrom, "")
		}
	})
	d.removeCDROM = qt6.NewQPushButton3("Remove")
	d.removeCDROM.OnClicked(func() {
		cdrom := d.selectedCDROM()
		if cdrom == nil {
			return
		}
		d.device(func(ctx context.Context) (*apprunnerv1.VirtualMachine, error) {
			r, err := d.mw.client.api.RemoveVMCDROM(ctx, &apprunnerv1.RemoveVMCDROMRequest{Id: d.vm.GetId(), CdromId: cdrom.GetId()})
			return r.GetVirtualMachine(), err
		})
	})
	cdromButtons.AddWidget(d.addCDROM.QWidget)
	cdromButtons.AddWidget(d.ejectCDROM.QWidget)
	cdromButtons.AddWidget(d.removeCDROM.QWidget)
	cdromButtons.AddStretch()
	layout.AddLayout(cdromButtons.QLayout)
	return page
}

func (d *settingsDialog) selectedCDROM() *apprunnerv1.VMCDROM {
	index := d.cdromTable.IndexOfTopLevelItem(d.cdromTable.CurrentItem())
	if index < 0 || index >= len(d.vm.GetCdroms()) {
		return nil
	}
	return d.vm.GetCdroms()[index]
}

func (d *settingsDialog) changeCDROM(cdrom *apprunnerv1.VMCDROM, iso string) {
	if cdrom.GetIsoName() == iso {
		return
	}
	d.device(func(ctx context.Context) (*apprunnerv1.VirtualMachine, error) {
		r, err := d.mw.client.api.UpdateVMCDROM(ctx, &apprunnerv1.UpdateVMCDROMRequest{Id: d.vm.GetId(), CdromId: cdrom.GetId(), IsoName: iso})
		return r.GetVirtualMachine(), err
	})
}

func (d *settingsDialog) buildNetwork() *qt6.QWidget {
	page := qt6.NewQWidget2()
	layout := qt6.NewQVBoxLayout(page)
	layout.SetContentsMargins(0, 0, 0, 0)
	nics := qt6.NewQGroupBox3("Network interfaces")
	nicLayout := qt6.NewQVBoxLayout(nics.QWidget)
	d.nicTable = newTable("NIC", "Mode", "Bridge", "MAC address", "Addresses")
	d.nicTable.Header().SetSectionResizeMode2(0, qt6.QHeaderView__ResizeToContents)
	d.nicTable.Header().SetSectionResizeMode2(1, qt6.QHeaderView__ResizeToContents)
	d.nicTable.Header().SetSectionResizeMode2(2, qt6.QHeaderView__Stretch)
	d.nicTable.Header().SetSectionResizeMode2(3, qt6.QHeaderView__ResizeToContents)
	d.nicTable.Header().SetStretchLastSection(false)
	d.nicTable.SetMinimumHeight(90)
	d.nicTable.OnItemSelectionChanged(func() { d.updateButtons() })
	d.nicTable.OnItemDoubleClicked(func(*qt6.QTreeWidgetItem, int) {
		if d.editNIC.IsEnabled() {
			d.editSelectedNIC()
		}
	})
	nicLayout.AddWidget(d.nicTable.QWidget)
	nicButtons := qt6.NewQHBoxLayout2()
	d.addNIC = qt6.NewQPushButton4(themeIcon("list-add", 0), "Add Interface…")
	d.addNIC.OnClicked(func() {
		spec, ok := nicDialog(d.QWidget, "Add Interface", d.s.network.GetBridges(), d.defaultNIC())
		if !ok {
			return
		}
		d.device(func(ctx context.Context) (*apprunnerv1.VirtualMachine, error) {
			r, err := d.mw.client.api.AddVMNIC(ctx, &apprunnerv1.AddVMNICRequest{Id: d.vm.GetId(), Nic: spec})
			return r.GetVirtualMachine(), err
		})
	})
	d.editNIC = qt6.NewQPushButton3("Edit…")
	d.editNIC.OnClicked(func() { d.editSelectedNIC() })
	d.removeNIC = qt6.NewQPushButton3("Remove")
	d.removeNIC.OnClicked(func() {
		nic := d.selectedNIC()
		if nic == nil {
			return
		}
		d.device(func(ctx context.Context) (*apprunnerv1.VirtualMachine, error) {
			r, err := d.mw.client.api.RemoveVMNIC(ctx, &apprunnerv1.RemoveVMNICRequest{Id: d.vm.GetId(), NicId: nic.GetId()})
			return r.GetVirtualMachine(), err
		})
	})
	nicButtons.AddWidget(d.addNIC.QWidget)
	nicButtons.AddWidget(d.editNIC.QWidget)
	nicButtons.AddWidget(d.removeNIC.QWidget)
	nicButtons.AddStretch()
	d.leaseInfo = inlineLabel("")
	nicButtons.AddWidget(d.leaseInfo.QWidget)
	nicLayout.AddLayout(nicButtons.QLayout)
	layout.AddWidget(nics.QWidget)

	d.ipmi = qt6.NewQGroupBox3("Virtual IPMI (BMC)")
	d.ipmi.SetCheckable(true)
	ipmiLayout := qt6.NewQVBoxLayout(d.ipmi.QWidget)
	grid := qt6.NewQGridLayout2()
	grid.SetHorizontalSpacing(10)
	grid.SetVerticalSpacing(8)
	d.ipmiBridge = qt6.NewQComboBox2()
	for _, bridge := range d.s.network.GetBridges() {
		label := bridge.GetName()
		if managedSubnet(bridge) {
			label += " · managed subnet"
		}
		d.ipmiBridge.AddItem3(label, qt6.NewQVariant14(bridge.GetName()))
	}
	d.ipmiAddress = qt6.NewQLineEdit2()
	d.ipmiAddress.SetFont(monospaceFont())
	d.ipmiUser = qt6.NewQLineEdit2()
	d.ipmiUser.SetMaxLength(16)
	d.ipmiPassword = passwordEdit()
	d.ipmiPassword.SetMaxLength(20)
	addField := func(row, column int, label string, field *qt6.QWidget) {
		l := qt6.NewQLabel3(label)
		l.SetAlignment(qt6.AlignRight | qt6.AlignVCenter)
		grid.AddWidget2(l.QWidget, row, column*2)
		grid.AddWidget2(field, row, column*2+1)
	}
	addField(0, 0, "Bridge:", d.ipmiBridge.QWidget)
	addField(0, 1, "Address:", d.ipmiAddress.QWidget)
	addField(1, 0, "Username:", d.ipmiUser.QWidget)
	addField(1, 1, "Password:", d.ipmiPassword.QWidget)
	grid.SetColumnStretch(1, 1)
	grid.SetColumnStretch(3, 1)
	ipmiLayout.AddLayout(grid.QLayout)
	d.ipmiHint = hint("")
	ipmiLayout.AddWidget(d.ipmiHint.QWidget)
	d.ipmiStatus = richLabel("")
	ipmiLayout.AddWidget(d.ipmiStatus.QWidget)
	d.ipmiCommand = richLabel("")
	d.ipmiCommand.SetTextInteractionFlags(qt6.TextSelectableByMouse)
	ipmiLayout.AddWidget(d.ipmiCommand.QWidget)
	ipmiLayout.AddWidget(mutedLabel("Expose IPMI only on a trusted management network. The listener uses IPMI 2.0 lanplus on UDP port 623.").QWidget)
	layout.AddWidget(d.ipmi.QWidget)
	layout.AddStretch()

	d.ipmi.OnToggled(func(bool) { d.updateButtons() })
	d.ipmiBridge.OnActivated(func(int) {
		d.ipmiAddress.SetText(suggestIPMIAddress(d.ipmiBridge.CurrentData().ToString(), d.s.network, d.s.vms, d.vm.GetId()))
		d.updateButtons()
	})
	for _, edit := range []*qt6.QLineEdit{d.ipmiAddress, d.ipmiUser, d.ipmiPassword} {
		edit.OnTextChanged(func(string) { d.updateButtons() })
	}
	return page
}

func (d *settingsDialog) defaultNIC() *apprunnerv1.VMNICSpec {
	if bridges := d.s.network.GetBridges(); len(bridges) > 0 {
		return &apprunnerv1.VMNICSpec{Mode: apprunnerv1.NetworkMode_NETWORK_MODE_BRIDGE, BridgeName: bridges[0].GetName()}
	}
	return &apprunnerv1.VMNICSpec{Mode: apprunnerv1.NetworkMode_NETWORK_MODE_NAT}
}

func (d *settingsDialog) selectedNIC() *apprunnerv1.VMNIC {
	index := d.nicTable.IndexOfTopLevelItem(d.nicTable.CurrentItem())
	if index < 0 || index >= len(d.vm.GetNics()) {
		return nil
	}
	return d.vm.GetNics()[index]
}

func (d *settingsDialog) editSelectedNIC() {
	nic := d.selectedNIC()
	if nic == nil {
		return
	}
	spec, ok := nicDialog(d.QWidget, "Edit "+deviceName(d.nicTable.IndexOfTopLevelItem(d.nicTable.CurrentItem()), "nic"), d.s.network.GetBridges(), &apprunnerv1.VMNICSpec{Mode: nic.GetMode(), BridgeName: nic.GetBridgeName(), MacAddress: nic.GetMacAddress()})
	if !ok {
		return
	}
	d.device(func(ctx context.Context) (*apprunnerv1.VirtualMachine, error) {
		r, err := d.mw.client.api.UpdateVMNIC(ctx, &apprunnerv1.UpdateVMNICRequest{Id: d.vm.GetId(), NicId: nic.GetId(), Nic: spec})
		return r.GetVirtualMachine(), err
	})
}

func (d *settingsDialog) buildCloudInit() *qt6.QWidget {
	page := qt6.NewQWidget2()
	layout := qt6.NewQVBoxLayout(page)
	layout.SetContentsMargins(0, 0, 0, 0)
	layout.AddWidget(headingLabel("NoCloud configuration").QWidget)
	layout.AddWidget(mutedLabel("App Runner creates a cidata ISO containing user-data and generated per-VM meta-data. Changes require the machine to be stopped.").QWidget)
	d.cloudInitNote = richLabel("")
	layout.AddWidget(d.cloudInitNote.QWidget)
	d.cloudInit = qt6.NewQCheckBox3("Enable cloud-init")
	layout.AddWidget(d.cloudInit.QWidget)
	form := qt6.NewQFormLayout2()
	fillForm(form)
	d.template = qt6.NewQComboBox2()
	d.template.AddItem3("Choose a template…", qt6.NewQVariant14(""))
	for _, template := range d.s.templates {
		d.template.AddItem3(template.GetName(), qt6.NewQVariant14(template.GetName()))
	}
	d.template.OnActivated(func(int) {
		name := d.template.CurrentData().ToString()
		for _, template := range d.s.templates {
			if template.GetName() == name {
				d.cloudInit.SetChecked(true)
				d.userData.SetPlainText(template.GetUserData())
			}
		}
	})
	form.AddRow3("Load template:", d.template.QWidget)
	form.AddRow3("", hint("Loading a template copies its contents into this machine.").QWidget)
	layout.AddLayout(form.QLayout)
	d.userData = newYAMLEditor()
	layout.AddWidget2(d.userData.QWidget, 1)
	layout.AddWidget(hint("Written verbatim; cloud-config YAML and executable user-data scripts are both supported. Maximum size: 1 MiB.").QWidget)
	d.cloudInit.OnToggled(func(bool) { d.updateButtons() })
	d.userData.OnTextChanged(func() { d.updateButtons() })
	return page
}

// load fills every page from the VM.
func (d *settingsDialog) load(vm *apprunnerv1.VirtualMachine) {
	d.vm = vm
	d.SetWindowTitle("Settings — " + vm.GetName())
	d.name.SetText(vm.GetName())
	d.description.SetPlainText(vm.GetDescription())
	d.cpus.SetValue(int(vm.GetCpus()))
	d.memory.SetValue(int(vm.GetMemoryMib()))
	d.firmware.SetText(firmwareLabel(vm.GetFirmware()))

	d.diskTable.Clear()
	for index, disk := range vm.GetDisks() {
		backing := "—"
		if disk.GetSourceImage() != "" {
			backing = "iso/" + disk.GetSourceImage()
		}
		item := qt6.NewQTreeWidgetItem2([]string{deviceName(index, "disk"), fmt.Sprintf("%d GiB", disk.GetSizeGib()), backing, fmt.Sprintf("disk/%s-%s.qcow2", vm.GetId(), disk.GetId())})
		setBold(item, 0)
		setMono(item, 3)
		setMuted(item, 3)
		d.diskTable.AddTopLevelItem(item)
	}
	d.cdromTable.Clear()
	for index, cdrom := range vm.GetCdroms() {
		item := qt6.NewQTreeWidgetItem2([]string{deviceName(index, "cdrom"), ""})
		setBold(item, 0)
		d.cdromTable.AddTopLevelItem(item)
		combo := isoCombo(d.s.isos, cdrom.GetIsoName())
		target := cdrom
		combo.OnActivated(func(int) { d.changeCDROM(target, combo.CurrentData().ToString()) })
		combo.SetEnabled(!vmStopping(vm) && !d.busy)
		d.cdromTable.SetItemWidget(item, 1, combo.QWidget)
	}

	d.nicTable.Clear()
	var leases []string
	for index, nic := range vm.GetNics() {
		mode, bridge := "User-mode NAT", "—"
		if nic.GetMode() == apprunnerv1.NetworkMode_NETWORK_MODE_BRIDGE {
			mode = "Host bridge"
			bridge = nic.GetBridgeName()
			if b := findBridge(d.s.network, nic.GetBridgeName()); b != nil && b.GetDescription() != "" {
				bridge += " · " + b.GetDescription()
			}
		}
		item := qt6.NewQTreeWidgetItem2([]string{deviceName(index, "nic"), mode, bridge, nic.GetMacAddress(), strings.Join(nic.GetAddresses(), ", ")})
		setBold(item, 0)
		setMono(item, 3)
		setMono(item, 4)
		d.nicTable.AddTopLevelItem(item)
		leases = append(leases, nic.GetAddresses()...)
	}
	if len(leases) > 0 {
		d.leaseInfo.SetText(mutedHTML("Lease ") + monoHTML(strings.Join(leases, ", ")))
	} else {
		d.leaseInfo.SetText("")
	}

	ipmi := vm.GetIpmi()
	d.ipmi.SetChecked(ipmi.GetEnabled())
	bridgeName := ipmi.GetBridgeName()
	if bridgeName == "" {
		for _, bridge := range d.s.network.GetBridges() {
			if managedSubnet(bridge) {
				bridgeName = bridge.GetName()
				break
			}
		}
		if bridgeName == "" && len(d.s.network.GetBridges()) > 0 {
			bridgeName = d.s.network.GetBridges()[0].GetName()
		}
	}
	if index := d.ipmiBridge.FindData(qt6.NewQVariant14(bridgeName)); index >= 0 {
		d.ipmiBridge.SetCurrentIndex(index)
	}
	if ipmi.GetAddress() != "" {
		d.ipmiAddress.SetText(ipmi.GetAddress())
	} else {
		d.ipmiAddress.SetText(suggestIPMIAddress(bridgeName, d.s.network, d.s.vms, vm.GetId()))
	}
	d.ipmiUser.SetText(firstNonEmpty(ipmi.GetUsername(), "admin"))
	d.ipmiPassword.SetText("")
	if ipmi.GetEnabled() {
		d.ipmiHint.SetText(mutedHTML("Leave the password blank to keep the existing one. App Runner assigns the address to the bridge while IPMI is enabled."))
		port := ipmi.GetPort()
		if port == 0 {
			port = 623
		}
		if ipmi.GetRunning() {
			d.ipmiStatus.SetText(fmt.Sprintf("%s BMC listening on %s:%d", dotHTML(colorPositive), escape(ipmi.GetAddress()), port))
		} else {
			d.ipmiStatus.SetText(fmt.Sprintf("%s BMC not listening: %s", dotHTML(colorNegative), escape(firstNonEmpty(ipmi.GetLastError(), "not running"))))
		}
		d.ipmiCommand.SetText(monoHTML(fmt.Sprintf("ipmitool -I lanplus -H %s -U %s power status", ipmi.GetAddress(), ipmi.GetUsername())))
		d.ipmiStatus.Show()
		d.ipmiCommand.Show()
	} else {
		d.ipmiHint.SetText(mutedHTML("A password is required; IPMI passwords are limited to 20 bytes. The address must be unused on the bridge."))
		d.ipmiStatus.Hide()
		d.ipmiCommand.Hide()
	}

	d.cloudInit.SetChecked(vm.GetCloudInit().GetEnabled())
	d.userData.SetPlainText(vm.GetCloudInit().GetUserData())
	d.template.SetCurrentIndex(0)
	d.updateState()
	d.updateButtons()
}

func (d *settingsDialog) updateState() {
	vm := d.vm
	switch {
	case vmRunning(vm):
		d.state.show(messageInfo, fmt.Sprintf("<b>%s is running.</b> Name, description, and IPMI changes are immediate; CPU and memory changes apply on the next start. ISO media can be changed or ejected live; other devices can be changed after it stops.", escape(vm.GetName())))
	case vmStopping(vm):
		d.state.show(messageWarning, fmt.Sprintf("<b>%s is stopping.</b> Wait for it to stop before changing devices or network interfaces.", escape(vm.GetName())))
	default:
		d.state.Hide()
	}
	active := vmActive(vm)
	running := vmRunning(vm)
	if running {
		d.cpuHint.SetText(fmt.Sprintf(`<span style="color:%s">Applies on next start</span>`, colorNeutral))
		d.memoryHint.SetText(fmt.Sprintf(`<span style="color:%s">Applies on next start</span>`, colorNeutral))
	} else {
		d.cpuHint.SetText("")
		d.memoryHint.SetText("")
	}
	d.cloudInit.SetEnabled(!active && !d.busy)
	d.template.SetEnabled(!active && !d.busy)
	d.userData.SetReadOnly(active || d.busy || !d.cloudInit.IsChecked())
	if active {
		d.cloudInitNote.SetText(fmt.Sprintf(`<span style="color:%s">Stop the machine before changing its cloud-init configuration.</span>`, colorNeutral))
	} else {
		d.cloudInitNote.SetText("")
	}
}

func (d *settingsDialog) generalDirty() bool {
	vm := d.vm
	return strings.TrimSpace(d.name.Text()) != vm.GetName() || strings.TrimSpace(d.description.ToPlainText()) != vm.GetDescription() || d.cpus.Value() != int(vm.GetCpus()) || d.memory.Value() != int(vm.GetMemoryMib())
}

func (d *settingsDialog) ipmiDirty() bool {
	ipmi := d.vm.GetIpmi()
	if d.ipmi.IsChecked() != ipmi.GetEnabled() {
		return true
	}
	return d.ipmi.IsChecked() && (d.ipmiBridge.CurrentData().ToString() != ipmi.GetBridgeName() || strings.TrimSpace(d.ipmiAddress.Text()) != ipmi.GetAddress() || strings.TrimSpace(d.ipmiUser.Text()) != ipmi.GetUsername() || d.ipmiPassword.Text() != "")
}

func (d *settingsDialog) ipmiValid() bool {
	if !d.ipmi.IsChecked() {
		return true
	}
	return d.ipmiBridge.CurrentData().ToString() != "" && strings.TrimSpace(d.ipmiAddress.Text()) != "" && strings.TrimSpace(d.ipmiUser.Text()) != "" && (d.vm.GetIpmi().GetEnabled() || d.ipmiPassword.Text() != "")
}

func (d *settingsDialog) cloudInitDirty() bool {
	cloudInit := d.vm.GetCloudInit()
	return d.cloudInit.IsChecked() != cloudInit.GetEnabled() || d.userData.ToPlainText() != cloudInit.GetUserData()
}

func (d *settingsDialog) valid() bool {
	name := strings.TrimSpace(d.name.Text())
	return validDNSLabel(name) && !nameTaken(name, d.s.vms, d.vm.GetId()) && d.ipmiValid() && len(d.userData.ToPlainText()) <= maxUserDataSize && !(vmActive(d.vm) && d.cloudInitDirty())
}

func (d *settingsDialog) dirty() bool {
	return d.generalDirty() || d.ipmiDirty() || d.cloudInitDirty()
}

func (d *settingsDialog) updateButtons() {
	active := vmActive(d.vm)
	stopping := vmStopping(d.vm)
	d.applyButton.SetEnabled(!d.busy && d.dirty() && d.valid())
	d.okButton.SetEnabled(!d.busy && (!d.dirty() || d.valid()))
	d.addDisk.SetEnabled(!active && !d.busy)
	d.removeDisk.SetEnabled(!active && !d.busy && d.diskTable.CurrentItem() != nil)
	d.addCDROM.SetEnabled(!active && !d.busy)
	cdrom := d.selectedCDROM()
	d.ejectCDROM.SetEnabled(!stopping && !d.busy && cdrom != nil && cdrom.GetIsoName() != "")
	d.removeCDROM.SetEnabled(!active && !d.busy && cdrom != nil)
	d.addNIC.SetEnabled(!active && !d.busy)
	d.editNIC.SetEnabled(!active && !d.busy && d.selectedNIC() != nil)
	d.removeNIC.SetEnabled(!active && !d.busy && d.selectedNIC() != nil)
	d.userData.SetReadOnly(active || d.busy || !d.cloudInit.IsChecked())
}

func (d *settingsDialog) setBusy(busy bool) {
	d.busy = busy
	d.updateButtons()
}

// device runs an immediate device operation and reloads from the returned
// VM.
func (d *settingsDialog) device(fn func(ctx context.Context) (*apprunnerv1.VirtualMachine, error)) {
	d.errors.Hide()
	d.setBusy(true)
	call(fn, func(vm *apprunnerv1.VirtualMachine, err error) {
		d.setBusy(false)
		if err != nil {
			d.errors.show(messageError, escape(errorMessage(err)))
			return
		}
		d.load(vm)
		d.mw.refresh()
	})
}

// apply sends only the sections that changed, so an unchanged IPMI
// configuration never restarts the virtual BMC.
func (d *settingsDialog) apply(closeAfter bool) {
	if !d.dirty() {
		if closeAfter {
			d.Accept()
		}
		return
	}
	if !d.valid() {
		return
	}
	vm := d.vm
	api := d.mw.client.api
	var general *apprunnerv1.UpdateVMRequest
	if d.generalDirty() {
		general = &apprunnerv1.UpdateVMRequest{Id: vm.GetId()}
		if name := strings.TrimSpace(d.name.Text()); name != vm.GetName() {
			general.Name = &name
		}
		if description := strings.TrimSpace(d.description.ToPlainText()); description != vm.GetDescription() {
			general.Description = &description
		}
		if cpus := uint32(d.cpus.Value()); cpus != vm.GetCpus() {
			general.Cpus = &cpus
		}
		if memory := uint32(d.memory.Value()); memory != vm.GetMemoryMib() {
			general.MemoryMib = &memory
		}
	}
	var ipmi *apprunnerv1.ConfigureVMIPMIRequest
	if d.ipmiDirty() {
		ipmi = &apprunnerv1.ConfigureVMIPMIRequest{Id: vm.GetId(), Enabled: d.ipmi.IsChecked(), BridgeName: d.ipmiBridge.CurrentData().ToString(), Address: strings.TrimSpace(d.ipmiAddress.Text()), Username: strings.TrimSpace(d.ipmiUser.Text()), Password: d.ipmiPassword.Text()}
	}
	var cloudInit *apprunnerv1.ConfigureVMCloudInitRequest
	if d.cloudInitDirty() {
		cloudInit = &apprunnerv1.ConfigureVMCloudInitRequest{Id: vm.GetId(), Enabled: d.cloudInit.IsChecked(), UserData: d.userData.ToPlainText()}
	}
	d.errors.Hide()
	d.setBusy(true)
	call(func(ctx context.Context) (*apprunnerv1.VirtualMachine, error) {
		if general != nil {
			if _, err := api.UpdateVM(ctx, general); err != nil {
				return nil, err
			}
		}
		if ipmi != nil {
			if _, err := api.ConfigureVMIPMI(ctx, ipmi); err != nil {
				return nil, err
			}
		}
		if cloudInit != nil {
			if _, err := api.ConfigureVMCloudInit(ctx, cloudInit); err != nil {
				return nil, err
			}
		}
		r, err := api.GetVM(ctx, &apprunnerv1.GetVMRequest{Id: vm.GetId()})
		return r.GetVirtualMachine(), err
	}, func(updated *apprunnerv1.VirtualMachine, err error) {
		d.setBusy(false)
		d.mw.refresh()
		if err != nil {
			d.errors.show(messageError, escape(errorMessage(err)))
			return
		}
		d.load(updated)
		if closeAfter {
			d.Accept()
		}
	})
}
