package main

import (
	"fmt"
	"strings"

	"github.com/mappu/miqt/qt6"

	apprunnerv1 "github.com/roflware/app-runner/internal/gen/apprunner/v1"
)

// formDialog is a small modal dialog with a form, an optional banner, and
// OK/Cancel buttons whose OK state follows a validation function.
type formDialog struct {
	*qt6.QDialog
	layout  *qt6.QVBoxLayout
	form    *qt6.QFormLayout
	buttons *qt6.QDialogButtonBox
	ok      *qt6.QPushButton
	valid   func() bool
}

func newFormDialog(parent *qt6.QWidget, title, okLabel string) *formDialog {
	d := &formDialog{QDialog: qt6.NewQDialog(parent)}
	d.SetWindowTitle(title)
	d.SetModal(true)
	d.layout = qt6.NewQVBoxLayout(d.QWidget)
	d.layout.SetSpacing(10)
	d.form = qt6.NewQFormLayout2()
	fillForm(d.form)
	d.layout.AddLayout(d.form.QLayout)
	d.buttons = qt6.NewQDialogButtonBox2()
	d.ok = d.buttons.AddButton2(okLabel, qt6.QDialogButtonBox__AcceptRole)
	d.ok.SetDefault(true)
	d.buttons.AddButton2("Cancel", qt6.QDialogButtonBox__RejectRole)
	d.buttons.OnAccepted(func() { d.Accept() })
	d.buttons.OnRejected(func() { d.Reject() })
	d.layout.AddStretch()
	d.layout.AddWidget(d.buttons.QWidget)
	return d
}

func (d *formDialog) revalidate() {
	if d.valid != nil {
		d.ok.SetEnabled(d.valid())
	}
}

func (d *formDialog) run() bool {
	d.revalidate()
	return d.Exec() == int(qt6.QDialog__Accepted)
}

// hint is secondary explanatory text under a form field; it takes the
// field column's full width rather than wrapping at its minimum size.
func hint(text string) *qt6.QLabel {
	label := mutedLabel(text)
	label.SetWordWrap(true)
	label.SetSizePolicy2(qt6.QSizePolicy__Expanding, qt6.QSizePolicy__Preferred)
	label.SetMinimumWidth(240)
	return label
}

// cloneDialog asks for the clone's name, suggesting an unused one.
func cloneDialog(parent *qt6.QWidget, source *apprunnerv1.VirtualMachine, vms []*apprunnerv1.VirtualMachine) (string, bool) {
	d := newFormDialog(parent, "Clone "+source.GetName(), "Clone")
	d.SetMinimumWidth(460)
	banner := newMessageWidget(false)
	banner.show(messageInfo, "All virtual disks will be fully copied. The clone receives new MAC addresses and starts with IPMI disabled. Guest hostnames, machine IDs, SSH keys, and static IP settings remain unchanged inside the copied disks.")
	d.layout.InsertWidget(0, banner.QWidget)
	name := qt6.NewQLineEdit3(suggestCloneName(source.GetName(), vms))
	name.SetMaxLength(63)
	name.SelectAll()
	d.form.AddRow3("Name:", name.QWidget)
	help := hint("1–63 letters, numbers, or interior hyphens; also used by Auto DNS.")
	d.form.AddRow3("", help.QWidget)
	d.valid = func() bool {
		value := strings.TrimSpace(name.Text())
		if nameTaken(value, vms, "") {
			help.SetText(fmt.Sprintf(`<span style="color:%s">A virtual machine with this name already exists.</span>`, colorNegative))
			return false
		}
		help.SetText(mutedHTML("1–63 letters, numbers, or interior hyphens; also used by Auto DNS."))
		return validDNSLabel(value)
	}
	name.OnTextChanged(func(string) { d.revalidate() })
	if !d.run() {
		return "", false
	}
	return strings.TrimSpace(name.Text()), true
}

func createBridgeDialog(parent *qt6.QWidget) (string, bool) {
	d := newFormDialog(parent, "Create Linux Bridge", "Create with Rollback")
	d.SetMinimumWidth(420)
	name := qt6.NewQLineEdit2()
	name.SetMaxLength(15)
	name.SetPlaceholderText("br0")
	d.form.AddRow3("Bridge name:", name.QWidget)
	d.form.AddRow3("", hint("1–15 letters, numbers, dots, underscores, or hyphens. The change reverts automatically unless it is confirmed within 15 seconds.").QWidget)
	d.valid = func() bool {
		value := strings.TrimSpace(name.Text())
		return value != "" && len(value) <= 15 && !strings.ContainsAny(value, " /\t")
	}
	name.OnTextChanged(func(string) { d.revalidate() })
	if !d.run() {
		return "", false
	}
	return strings.TrimSpace(name.Text()), true
}

func attachInterfaceDialog(parent *qt6.QWidget, bridgeName string, interfaces []*apprunnerv1.NetworkInterface) (string, bool, bool) {
	d := newFormDialog(parent, "Attach Interface to "+bridgeName, "Attach with Rollback")
	d.SetMinimumWidth(520)
	banner := newMessageWidget(false)
	banner.show(messageWarning, "Attaching the interface carrying this client's connection can interrupt connectivity. The backend reverts automatically unless you confirm within 15 seconds.")
	d.layout.InsertWidget(0, banner.QWidget)
	combo := qt6.NewQComboBox2()
	for _, iface := range interfaces {
		label := iface.GetName()
		if len(iface.GetAddresses()) > 0 {
			label += " · " + strings.Join(iface.GetAddresses(), ", ")
		}
		combo.AddItem3(label, qt6.NewQVariant14(iface.GetName()))
	}
	d.form.AddRow3("Host interface:", combo.QWidget)
	migrate := qt6.NewQCheckBox3("Move global IP addresses and non-kernel routes from the interface to the bridge")
	migrate.SetChecked(true)
	d.form.AddRow3("", migrate.QWidget)
	d.valid = func() bool { return combo.Count() > 0 }
	if !d.run() {
		return "", false, false
	}
	return combo.CurrentData().ToString(), migrate.IsChecked(), true
}

func staticLeaseDialog(parent *qt6.QWidget, lease *apprunnerv1.DHCPLease, suggested string) (string, bool) {
	d := newFormDialog(parent, "Set Static IP", "Save Static IP")
	d.SetMinimumWidth(420)
	d.form.AddRow3("Client:", newLabel(firstNonEmpty(lease.GetVmName(), "Unmanaged client")+" · "+lease.GetHardwareAddress()).QWidget)
	address := qt6.NewQLineEdit3(suggested)
	address.SetFont(monospaceFont())
	d.form.AddRow3("Static IPv4 address:", address.QWidget)
	d.form.AddRow3("", hint("Choose an unused address before the dynamic DHCP pool (for example .2 through .49).").QWidget)
	d.valid = func() bool { return strings.TrimSpace(address.Text()) != "" }
	address.OnTextChanged(func(string) { d.revalidate() })
	if !d.run() {
		return "", false
	}
	return strings.TrimSpace(address.Text()), true
}

// nicEditor is the Mode / Bridge / MAC form shared by the wizard, the NIC
// dialog, and the settings page.
type nicEditor struct {
	widget  *qt6.QWidget
	form    *qt6.QFormLayout
	mode    *qt6.QComboBox
	bridge  *qt6.QComboBox
	mac     *qt6.QLineEdit
	note    *qt6.QLabel
	bridges []*apprunnerv1.NetworkBridge
	changed func()
}

func newNICEditor(bridges []*apprunnerv1.NetworkBridge, spec *apprunnerv1.VMNICSpec) *nicEditor {
	e := &nicEditor{widget: qt6.NewQWidget2(), bridges: bridges}
	e.form = qt6.NewQFormLayout(e.widget)
	fillForm(e.form)
	e.form.SetContentsMargins(0, 0, 0, 0)
	e.mode = qt6.NewQComboBox2()
	e.mode.AddItem3("User-mode NAT", qt6.NewQVariant4(int(apprunnerv1.NetworkMode_NETWORK_MODE_NAT)))
	e.mode.AddItem3("Host bridge", qt6.NewQVariant4(int(apprunnerv1.NetworkMode_NETWORK_MODE_BRIDGE)))
	e.form.AddRow3("Mode:", e.mode.QWidget)
	e.bridge = qt6.NewQComboBox2()
	for _, bridge := range bridges {
		label := bridge.GetName()
		if bridge.GetDescription() != "" {
			label += " · " + bridge.GetDescription()
		}
		if !bridge.GetUsableByQemu() {
			label += " · diagnostics required"
		}
		e.bridge.AddItem3(label, qt6.NewQVariant14(bridge.GetName()))
	}
	e.form.AddRow3("Bridge:", e.bridge.QWidget)
	e.mac = qt6.NewQLineEdit2()
	e.mac.SetPlaceholderText("auto")
	e.mac.SetFont(monospaceFont())
	e.mac.SetMaxLength(17)
	e.form.AddRow3("MAC:", e.mac.QWidget)
	e.note = hint("")
	e.form.AddRow3("", e.note.QWidget)
	if spec != nil {
		if spec.GetMode() == apprunnerv1.NetworkMode_NETWORK_MODE_BRIDGE {
			e.mode.SetCurrentIndex(1)
			if index := e.bridge.FindData(qt6.NewQVariant14(spec.GetBridgeName())); index >= 0 {
				e.bridge.SetCurrentIndex(index)
			}
		}
		e.mac.SetText(spec.GetMacAddress())
	}
	e.mode.OnCurrentIndexChanged(func(int) { e.update() })
	e.bridge.OnCurrentIndexChanged(func(int) { e.update() })
	e.mac.OnTextChanged(func(string) { e.update() })
	e.update()
	return e
}

func (e *nicEditor) bridged() bool {
	return e.mode.CurrentIndex() == 1
}

func (e *nicEditor) update() {
	e.form.SetRowVisible2(e.bridge.QWidget, e.bridged())
	note := ""
	switch {
	case e.bridged() && len(e.bridges) == 0:
		note = fmt.Sprintf(`<span style="color:%s">No Linux bridges were detected. Create one under Network.</span>`, colorNegative)
	case e.bridged():
		if bridge := findBridgeIn(e.bridges, e.bridge.CurrentData().ToString()); bridge != nil && !bridge.GetUsableByQemu() {
			note = fmt.Sprintf(`<span style="color:%s">This bridge has failed diagnostics. Review it under Network before starting the VM.</span>`, colorNeutral)
		}
	}
	if mac := strings.TrimSpace(e.mac.Text()); mac != "" && !validMAC(mac) {
		note = fmt.Sprintf(`<span style="color:%s">Enter a unicast MAC address such as 52:54:00:12:34:56, or leave it empty to generate one.</span>`, colorNegative)
	}
	e.note.SetText(note)
	e.note.SetVisible(note != "")
	if e.changed != nil {
		e.changed()
	}
}

func findBridgeIn(bridges []*apprunnerv1.NetworkBridge, name string) *apprunnerv1.NetworkBridge {
	for _, bridge := range bridges {
		if bridge.GetName() == name {
			return bridge
		}
	}
	return nil
}

func (e *nicEditor) valid() bool {
	if e.bridged() && e.bridge.CurrentData().ToString() == "" {
		return false
	}
	mac := strings.TrimSpace(e.mac.Text())
	return mac == "" || validMAC(mac)
}

func (e *nicEditor) spec() *apprunnerv1.VMNICSpec {
	spec := &apprunnerv1.VMNICSpec{Mode: apprunnerv1.NetworkMode_NETWORK_MODE_NAT, MacAddress: strings.TrimSpace(e.mac.Text())}
	if e.bridged() {
		spec.Mode = apprunnerv1.NetworkMode_NETWORK_MODE_BRIDGE
		spec.BridgeName = e.bridge.CurrentData().ToString()
	}
	return spec
}

func (e *nicEditor) setEnabled(enabled bool) {
	e.mode.SetEnabled(enabled)
	e.bridge.SetEnabled(enabled)
	e.mac.SetEnabled(enabled)
}

// nicDialog adds or edits a network interface.
func nicDialog(parent *qt6.QWidget, title string, bridges []*apprunnerv1.NetworkBridge, spec *apprunnerv1.VMNICSpec) (*apprunnerv1.VMNICSpec, bool) {
	d := newFormDialog(parent, title, "OK")
	d.SetMinimumWidth(460)
	editor := newNICEditor(bridges, spec)
	d.layout.InsertWidget(0, editor.widget)
	d.valid = editor.valid
	editor.changed = d.revalidate
	if !d.run() {
		return nil, false
	}
	return editor.spec(), true
}

// diskDialog asks for a new disk's size and optional cloud image.
func diskDialog(parent *qt6.QWidget, images []*apprunnerv1.DiskImage) (*apprunnerv1.VMDiskSpec, bool) {
	d := newFormDialog(parent, "Add Disk", "Add Disk")
	d.SetMinimumWidth(460)
	size := qt6.NewQSpinBox2()
	size.SetRange(1, maxDiskGiB)
	size.SetValue(20)
	size.SetSuffix(" GiB")
	d.form.AddRow3("Size:", size.QWidget)
	image := qt6.NewQComboBox2()
	image.AddItem3("None (empty disk)", qt6.NewQVariant14(""))
	for _, entry := range images {
		image.AddItem3(fmt.Sprintf("%s · %s", entry.GetName(), formatBytes(entry.GetSizeBytes())), qt6.NewQVariant14(entry.GetName()))
	}
	d.form.AddRow3("Cloud image:", image.QWidget)
	d.form.AddRow3("", hint("A disk backed by a cloud image is a linked qcow2 overlay; the image must stay in iso/ and the disk must be at least as large as the image's virtual size.").QWidget)
	if !d.run() {
		return nil, false
	}
	return &apprunnerv1.VMDiskSpec{SizeGib: uint32(size.Value()), SourceImage: image.CurrentData().ToString()}, true
}

// isoCombo lists ISO storage with an "Empty" entry first.
func isoCombo(isos []*apprunnerv1.ISOImage, selected string) *qt6.QComboBox {
	combo := qt6.NewQComboBox2()
	combo.AddItem3("Empty", qt6.NewQVariant14(""))
	for _, iso := range isos {
		combo.AddItem3(fmt.Sprintf("%s · %s", iso.GetName(), formatBytes(iso.GetSizeBytes())), qt6.NewQVariant14(iso.GetName()))
	}
	if selected != "" {
		if index := combo.FindData(qt6.NewQVariant14(selected)); index >= 0 {
			combo.SetCurrentIndex(index)
		} else {
			combo.AddItem3(selected+" · missing", qt6.NewQVariant14(selected))
			combo.SetCurrentIndex(combo.Count() - 1)
		}
	}
	return combo
}

func imageCombo(images []*apprunnerv1.DiskImage, selected string) *qt6.QComboBox {
	combo := qt6.NewQComboBox2()
	combo.AddItem3("None (empty disk)", qt6.NewQVariant14(""))
	for _, image := range images {
		combo.AddItem3(fmt.Sprintf("%s · %s", image.GetName(), formatBytes(image.GetSizeBytes())), qt6.NewQVariant14(image.GetName()))
	}
	if selected != "" {
		if index := combo.FindData(qt6.NewQVariant14(selected)); index >= 0 {
			combo.SetCurrentIndex(index)
		}
	}
	return combo
}

func cdromDialog(parent *qt6.QWidget, isos []*apprunnerv1.ISOImage) (string, bool) {
	d := newFormDialog(parent, "Add CD-ROM", "Add CD-ROM")
	d.SetMinimumWidth(460)
	combo := isoCombo(isos, "")
	if len(isos) > 0 {
		combo.SetCurrentIndex(1)
	}
	d.form.AddRow3("ISO media:", combo.QWidget)
	if !d.run() {
		return "", false
	}
	return combo.CurrentData().ToString(), true
}
