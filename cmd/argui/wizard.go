package main

import (
	"context"
	"fmt"
	"strings"

	"github.com/mappu/miqt/qt6"

	apprunnerv1 "github.com/roflware/app-runner/internal/gen/apprunner/v1"
)

// wizardSeed pre-fills the wizard from a storage file or a cloud-init
// template.
type wizardSeed struct {
	sourceRoot, sourcePath string
	template               *apprunnerv1.CloudInitTemplate
}

const (
	pageGeneral = iota
	pageStorage
	pageNetwork
	pageCloudInit
	pageSummary
)

var wizardStepNames = []string{"General", "Storage", "Network", "Cloud-init", "Summary"}

type diskDraft struct {
	sizeGiB uint32
	image   string
}

// newMachineWizard is the KAssistantDialog-style New Virtual Machine flow:
// steps down the left, one page at a time, and a summary before creating.
type newMachineWizard struct {
	*qt6.QWizard
	mw    *mainWindow
	s     *snapshot
	pages []*qt6.QWizardPage
	steps []*qt6.QLabel
	side  *qt6.QLabel

	name        *qt6.QLineEdit
	nameHint    *qt6.QLabel
	description *qt6.QPlainTextEdit
	cpus        *qt6.QSpinBox
	memory      *qt6.QSpinBox
	firmware    *qt6.QComboBox

	disks      []diskDraft
	diskTable  *qt6.QTreeWidget
	cdroms     []string
	cdromTable *qt6.QTreeWidget
	cdromNote  *qt6.QLabel

	nics       []*nicEditor
	nicBoxes   []*qt6.QGroupBox
	nicLayout  *qt6.QVBoxLayout
	nicNote    *messageWidget
	removeNIC  *qt6.QPushButton
	nicContent *qt6.QWidget
	nicPage    *qt6.QWizardPage

	cloudInit     *qt6.QCheckBox
	template      *qt6.QComboBox
	userData      *qt6.QPlainTextEdit
	saveTemplate  *qt6.QCheckBox
	templateName  *qt6.QLineEdit
	templateHint  *qt6.QLabel
	cloudInitBody *qt6.QWidget

	summary *qt6.QLabel
	busy    bool
}

func openNewMachineWizard(mw *mainWindow, seed wizardSeed) *newMachineWizard {
	w := &newMachineWizard{QWizard: qt6.NewQWizard(mw.QWidget), mw: mw, s: mw.snapshot}
	w.SetWindowTitle("New Virtual Machine — App Runner")
	w.SetWizardStyle(qt6.QWizard__ClassicStyle)
	w.SetOption(qt6.QWizard__HaveHelpButton)
	w.SetOption(qt6.QWizard__NoBackButtonOnStartPage)
	w.SetButtonText(qt6.QWizard__BackButton, "‹ Back")
	w.SetButtonText(qt6.QWizard__NextButton, "Next ›")
	w.SetButtonText(qt6.QWizard__FinishButton, "Finish")
	w.SetAttribute(qt6.WA_DeleteOnClose)
	w.Resize(800, 600)

	w.buildSide()
	w.pages = []*qt6.QWizardPage{w.buildGeneral(), w.buildStorage(), w.buildNetwork(), w.buildCloudInit(), w.buildSummary()}
	for _, page := range w.pages {
		w.AddPage(page)
	}
	w.applySeed(seed)
	w.OnCurrentIdChanged(func(id int) {
		w.updateSide()
		if id == pageSummary {
			w.updateSummary()
		}
	})
	w.OnValidateCurrentPage(func(super func() bool) bool {
		if w.CurrentId() != pageSummary {
			return super()
		}
		w.submit()
		return false
	})
	w.OnHelpRequested(func() {
		qt6.QMessageBox_Information(w.QWidget, "New Virtual Machine", "Each step configures part of the machine. Disks attach in order and the first is the boot disk; a disk backed by a cloud image is a linked overlay of that image. Cloud-init user-data is written verbatim to a NoCloud ISO attached at first boot.")
	})
	w.updateSide()
	w.Show()
	return w
}

func (w *newMachineWizard) buildSide() {
	side := qt6.NewQWidget2()
	side.SetMinimumWidth(170)
	layout := qt6.NewQVBoxLayout(side)
	layout.SetContentsMargins(6, 8, 14, 8)
	layout.SetSpacing(4)
	for range wizardStepNames {
		label := richLabel("")
		layout.AddWidget(label.QWidget)
		w.steps = append(w.steps, label)
	}
	layout.AddStretch()
	w.side = mutedLabel("")
	layout.AddWidget(w.side.QWidget)
	w.SetSideWidget(side)
}

func (w *newMachineWizard) updateSide() {
	current := w.CurrentId()
	for index, label := range w.steps {
		name := escape(wizardStepNames[index])
		switch {
		case index < current:
			label.SetText(fmt.Sprintf(`<span style="color:%s">&#10003;</span>&nbsp; <span style="color:%s">%s</span>`, colorPositive, colorInactive, name))
		case index == current:
			label.SetText(fmt.Sprintf(`<span style="color:%s">&#9679;</span>&nbsp; <b>%s</b>`, colorHighlight, name))
		default:
			label.SetText(fmt.Sprintf(`<span style="color:%s">%d&nbsp; %s</span>`, colorInactive, index+1, name))
		}
	}
	name := strings.TrimSpace(w.name.Text())
	if name == "" {
		name = "New machine"
	}
	w.side.SetText(mutedHTML(fmt.Sprintf("%s\n%d vCPU · %d MiB\n%s", name, w.cpus.Value(), w.memory.Value(), w.firmwareSummary())))
	w.side.SetText(strings.ReplaceAll(w.side.Text(), "\n", "<br>"))
}

func (w *newMachineWizard) firmwareSummary() string {
	switch w.firmwareValue() {
	case apprunnerv1.VMFirmware_VM_FIRMWARE_BIOS:
		return "BIOS (SeaBIOS)"
	case apprunnerv1.VMFirmware_VM_FIRMWARE_UEFI:
		return "UEFI (OVMF)"
	}
	for _, disk := range w.disks {
		if disk.image != "" {
			return "UEFI (automatic)"
		}
	}
	return "BIOS (automatic)"
}

func (w *newMachineWizard) firmwareValue() apprunnerv1.VMFirmware {
	return apprunnerv1.VMFirmware(w.firmware.CurrentData().ToInt())
}

func pageWithTitle(title, subtitle string) (*qt6.QWizardPage, *qt6.QVBoxLayout) {
	page := qt6.NewQWizardPage2()
	page.SetTitle(title)
	page.SetSubTitle(subtitle)
	layout := qt6.NewQVBoxLayout(page.QWidget)
	layout.SetSpacing(10)
	return page, layout
}

func (w *newMachineWizard) buildGeneral() *qt6.QWizardPage {
	page, layout := pageWithTitle("General", "Choose the machine's name and its initial compute allocation.")
	form := qt6.NewQFormLayout2()
	fillForm(form)
	w.name = qt6.NewQLineEdit2()
	w.name.SetMaxLength(63)
	w.name.SetPlaceholderText("machine-name")
	form.AddRow3("Name:", w.name.QWidget)
	w.nameHint = hint("1–63 letters, numbers, or interior hyphens; also used by Auto DNS.")
	form.AddRow3("", w.nameHint.QWidget)
	w.description = qt6.NewQPlainTextEdit2()
	w.description.SetPlaceholderText("Optional description")
	w.description.SetMaximumHeight(70)
	form.AddRow3("Description:", w.description.QWidget)
	w.cpus = qt6.NewQSpinBox2()
	w.cpus.SetRange(1, maxCPUs)
	w.cpus.SetValue(2)
	w.cpus.SetSuffix(" vCPU")
	form.AddRow3("Processors:", w.cpus.QWidget)
	w.memory = qt6.NewQSpinBox2()
	w.memory.SetRange(minMemoryMiB, maxMemoryMiB)
	w.memory.SetSingleStep(256)
	w.memory.SetValue(2048)
	w.memory.SetSuffix(" MiB")
	form.AddRow3("Memory:", w.memory.QWidget)
	w.firmware = qt6.NewQComboBox2()
	w.firmware.AddItem3("Automatic", qt6.NewQVariant4(int(apprunnerv1.VMFirmware_VM_FIRMWARE_UNSPECIFIED)))
	w.firmware.AddItem3("BIOS (SeaBIOS)", qt6.NewQVariant4(int(apprunnerv1.VMFirmware_VM_FIRMWARE_BIOS)))
	w.firmware.AddItem3("UEFI (OVMF)", qt6.NewQVariant4(int(apprunnerv1.VMFirmware_VM_FIRMWARE_UEFI)))
	form.AddRow3("Firmware:", w.firmware.QWidget)
	form.AddRow3("", hint("Automatic selects UEFI when a disk overlays a cloud image and BIOS otherwise.").QWidget)
	layout.AddLayout(form.QLayout)
	layout.AddStretch()

	page.OnIsComplete(func(super func() bool) bool { return w.nameValid() })
	w.name.OnTextChanged(func(string) {
		w.updateNameHint()
		page.CompleteChanged()
		w.updateSide()
	})
	w.cpus.OnValueChanged(func(int) { w.updateSide() })
	w.memory.OnValueChanged(func(int) { w.updateSide() })
	w.firmware.OnCurrentIndexChanged(func(int) { w.updateSide() })
	return page
}

func (w *newMachineWizard) nameValid() bool {
	name := strings.TrimSpace(w.name.Text())
	return validDNSLabel(name) && !nameTaken(name, w.s.vms, "")
}

func (w *newMachineWizard) updateNameHint() {
	name := strings.TrimSpace(w.name.Text())
	switch {
	case nameTaken(name, w.s.vms, ""):
		w.nameHint.SetText(fmt.Sprintf(`<span style="color:%s">A virtual machine with this name already exists.</span>`, colorNegative))
	case name != "" && !validDNSLabel(name):
		w.nameHint.SetText(fmt.Sprintf(`<span style="color:%s">Use 1–63 letters, numbers, or interior hyphens.</span>`, colorNegative))
	default:
		w.nameHint.SetText(mutedHTML("1–63 letters, numbers, or interior hyphens; also used by Auto DNS."))
	}
}

func (w *newMachineWizard) buildStorage() *qt6.QWizardPage {
	page, layout := pageWithTitle("Storage", "Disks attach in order; the first disk is the boot disk.")
	layout.AddWidget(headingLabel("Disks").QWidget)
	layout.AddWidget(richLabel(mutedHTML("The first disk is the boot disk. A disk may be a linked overlay of a qcow2 cloud image; the backing image must remain in ") + monoHTML("iso/") + mutedHTML(".")).QWidget)
	w.diskTable = newTable("Device", "Size", "Cloud image")
	w.diskTable.SetAlternatingRowColors(false)
	w.diskTable.Header().SetSectionResizeMode2(0, qt6.QHeaderView__ResizeToContents)
	w.diskTable.Header().SetSectionResizeMode2(1, qt6.QHeaderView__ResizeToContents)
	w.diskTable.SetMinimumHeight(110)
	layout.AddWidget2(w.diskTable.QWidget, 1)
	diskButtons := qt6.NewQHBoxLayout2()
	addDisk := qt6.NewQPushButton4(themeIcon("list-add", 0), "Add Disk")
	addDisk.OnClicked(func() {
		w.disks = append(w.disks, diskDraft{sizeGiB: 20})
		w.rebuildDisks()
	})
	removeDisk := qt6.NewQPushButton3("Remove")
	removeDisk.OnClicked(func() {
		index := w.diskTable.IndexOfTopLevelItem(w.diskTable.CurrentItem())
		if index >= 0 && index < len(w.disks) {
			w.disks = append(w.disks[:index], w.disks[index+1:]...)
			w.rebuildDisks()
		}
	})
	diskButtons.AddWidget(addDisk.QWidget)
	diskButtons.AddWidget(removeDisk.QWidget)
	diskButtons.AddStretch()
	diskButtons.AddWidget(hint("The disk must be at least as large as the image's virtual size.").QWidget)
	layout.AddLayout(diskButtons.QLayout)

	layout.AddWidget(headingLabel("CD-ROM drives").QWidget)
	layout.AddWidget(mutedLabel("Attach installer media from ISO storage, or add an empty drive.").QWidget)
	w.cdromTable = newTable("Device", "ISO media")
	w.cdromTable.SetAlternatingRowColors(false)
	w.cdromTable.Header().SetSectionResizeMode2(0, qt6.QHeaderView__ResizeToContents)
	w.cdromTable.SetMinimumHeight(70)
	layout.AddWidget2(w.cdromTable.QWidget, 1)
	cdromButtons := qt6.NewQHBoxLayout2()
	addCDROM := qt6.NewQPushButton4(themeIcon("list-add", 0), "Add CD-ROM")
	addCDROM.OnClicked(func() {
		name := ""
		if len(w.s.isos) > 0 {
			name = w.s.isos[0].GetName()
		}
		w.cdroms = append(w.cdroms, name)
		w.rebuildCDROMs()
	})
	removeCDROM := qt6.NewQPushButton3("Remove")
	removeCDROM.OnClicked(func() {
		index := w.cdromTable.IndexOfTopLevelItem(w.cdromTable.CurrentItem())
		if index >= 0 && index < len(w.cdroms) {
			w.cdroms = append(w.cdroms[:index], w.cdroms[index+1:]...)
			w.rebuildCDROMs()
		}
	})
	cdromButtons.AddWidget(addCDROM.QWidget)
	cdromButtons.AddWidget(removeCDROM.QWidget)
	cdromButtons.AddStretch()
	w.cdromNote = hint("No installer ISOs are in ISO storage.")
	cdromButtons.AddWidget(w.cdromNote.QWidget)
	layout.AddLayout(cdromButtons.QLayout)
	page.OnIsComplete(func(super func() bool) bool { return true })
	return page
}

func (w *newMachineWizard) rebuildDisks() {
	w.diskTable.Clear()
	for index := range w.disks {
		row := index
		item := qt6.NewQTreeWidgetItem2([]string{deviceName(index, "disk"), "", ""})
		setBold(item, 0)
		w.diskTable.AddTopLevelItem(item)
		size := qt6.NewQSpinBox2()
		size.SetRange(1, maxDiskGiB)
		size.SetValue(int(w.disks[index].sizeGiB))
		size.SetSuffix(" GiB")
		size.OnValueChanged(func(value int) { w.disks[row].sizeGiB = uint32(value) })
		w.diskTable.SetItemWidget(item, 1, size.QWidget)
		image := imageCombo(w.s.images, w.disks[index].image)
		image.OnCurrentIndexChanged(func(int) {
			w.disks[row].image = image.CurrentData().ToString()
			w.updateSide()
		})
		w.diskTable.SetItemWidget(item, 2, image.QWidget)
	}
	w.updateSide()
}

func (w *newMachineWizard) rebuildCDROMs() {
	w.cdromTable.Clear()
	for index := range w.cdroms {
		row := index
		item := qt6.NewQTreeWidgetItem2([]string{deviceName(index, "cdrom"), ""})
		setBold(item, 0)
		w.cdromTable.AddTopLevelItem(item)
		combo := isoCombo(w.s.isos, w.cdroms[index])
		combo.OnCurrentIndexChanged(func(int) { w.cdroms[row] = combo.CurrentData().ToString() })
		w.cdromTable.SetItemWidget(item, 1, combo.QWidget)
	}
	w.cdromNote.SetVisible(len(w.s.isos) == 0)
}

func (w *newMachineWizard) buildNetwork() *qt6.QWizardPage {
	page, layout := pageWithTitle("Network", "Each interface uses user-mode NAT or attaches to a host bridge. MAC addresses are generated unless specified.")
	w.nicContent = qt6.NewQWidget2()
	w.nicLayout = qt6.NewQVBoxLayout(w.nicContent)
	w.nicLayout.SetContentsMargins(0, 0, 8, 0)
	w.nicLayout.SetSpacing(10)
	w.nicLayout.AddStretch()
	layout.AddWidget2(scrollable(w.nicContent).QWidget, 1)
	buttons := qt6.NewQHBoxLayout2()
	add := qt6.NewQPushButton4(themeIcon("list-add", 0), "Add Interface")
	add.OnClicked(func() {
		w.addNIC(&apprunnerv1.VMNICSpec{Mode: apprunnerv1.NetworkMode_NETWORK_MODE_NAT})
		page.CompleteChanged()
	})
	w.removeNIC = qt6.NewQPushButton3("Remove Last")
	w.removeNIC.OnClicked(func() {
		if len(w.nics) == 0 {
			return
		}
		last := len(w.nics) - 1
		w.nicLayout.RemoveWidget(w.nicBoxes[last].QWidget)
		w.nicBoxes[last].Hide()
		w.nicBoxes[last].DeleteLater()
		w.nics = w.nics[:last]
		w.nicBoxes = w.nicBoxes[:last]
		w.updateNICNote()
		page.CompleteChanged()
	})
	buttons.AddWidget(add.QWidget)
	buttons.AddWidget(w.removeNIC.QWidget)
	buttons.AddStretch()
	layout.AddLayout(buttons.QLayout)
	w.nicNote = newMessageWidget(false)
	layout.AddWidget(w.nicNote.QWidget)
	page.OnIsComplete(func(super func() bool) bool {
		for _, nic := range w.nics {
			if !nic.valid() {
				return false
			}
		}
		return true
	})
	w.nicPage = page
	return page
}

func (w *newMachineWizard) addNIC(spec *apprunnerv1.VMNICSpec) {
	editor := newNICEditor(w.s.network.GetBridges(), spec)
	box := qt6.NewQGroupBox3(fmt.Sprintf("NIC %d", len(w.nics)+1))
	boxLayout := qt6.NewQVBoxLayout(box.QWidget)
	boxLayout.AddWidget(editor.widget)
	w.nicLayout.InsertWidget(len(w.nics), box.QWidget)
	editor.changed = func() {
		w.updateNICNote()
		if w.nicPage != nil {
			w.nicPage.CompleteChanged()
		}
	}
	w.nics = append(w.nics, editor)
	w.nicBoxes = append(w.nicBoxes, box)
	w.updateNICNote()
}

func (w *newMachineWizard) updateNICNote() {
	w.removeNIC.SetEnabled(len(w.nics) > 0)
	if len(w.nics) == 0 {
		w.nicNote.show(messageInfo, "This virtual machine will have no network connectivity.")
		return
	}
	for _, nic := range w.nics {
		if !nic.bridged() {
			continue
		}
		bridge := findBridge(w.s.network, nic.bridge.CurrentData().ToString())
		if bridge == nil || !bridge.GetDhcp().GetEnabled() {
			continue
		}
		dhcp := bridge.GetDhcp()
		services := []string{"DHCP"}
		if dhcp.GetNatEnabled() {
			services = append(services, "NAT")
		}
		if dhcp.GetDnsEnabled() && dhcp.GetAutoDns() {
			services = append(services, "Auto DNS")
		}
		text := fmt.Sprintf("%s has %s.", escape(bridge.GetName()), escape(strings.Join(services, ", ")))
		if dhcp.GetDnsEnabled() && dhcp.GetAutoDns() && strings.TrimSpace(w.name.Text()) != "" {
			text += fmt.Sprintf(" This VM will be reachable as %s.", monoHTML(strings.TrimSpace(w.name.Text())+"."+strings.TrimSuffix(dhcp.GetDnsSuffix(), ".")))
		}
		w.nicNote.show(messageInfo, text)
		return
	}
	w.nicNote.Hide()
}

func (w *newMachineWizard) buildCloudInit() *qt6.QWizardPage {
	page, layout := pageWithTitle("Cloud-init", "A cidata ISO with generated per-VM meta-data is attached at first boot.")
	w.cloudInit = qt6.NewQCheckBox3("Enable cloud-init")
	layout.AddWidget(w.cloudInit.QWidget)
	w.cloudInitBody = qt6.NewQWidget2()
	body := qt6.NewQVBoxLayout(w.cloudInitBody)
	body.SetContentsMargins(0, 0, 0, 0)
	form := qt6.NewQFormLayout2()
	fillForm(form)
	w.template = qt6.NewQComboBox2()
	w.template.AddItem3("Custom / blank", qt6.NewQVariant14(""))
	for _, template := range w.s.templates {
		w.template.AddItem3(template.GetName(), qt6.NewQVariant14(template.GetName()))
	}
	w.template.OnActivated(func(int) {
		name := w.template.CurrentData().ToString()
		for _, template := range w.s.templates {
			if template.GetName() == name {
				w.userData.SetPlainText(template.GetUserData())
			}
		}
	})
	form.AddRow3("Load template:", w.template.QWidget)
	form.AddRow3("", hint("Template contents are copied into this VM and can be customised here.").QWidget)
	body.AddLayout(form.QLayout)
	w.userData = newYAMLEditor()
	w.userData.SetPlaceholderText("#cloud-config\nusers:\n  - name: app")
	body.AddWidget2(w.userData.QWidget, 1)
	body.AddWidget(hint("Written verbatim. Maximum size: 1 MiB.").QWidget)
	w.saveTemplate = qt6.NewQCheckBox3("Save this user-data as a new template")
	body.AddWidget(w.saveTemplate.QWidget)
	nameForm := qt6.NewQFormLayout2()
	fillForm(nameForm)
	w.templateName = qt6.NewQLineEdit2()
	w.templateName.SetMaxLength(maxTemplateName)
	w.templateName.SetEnabled(false)
	nameForm.AddRow3("Template name:", w.templateName.QWidget)
	w.templateHint = hint("The template remains independent of the new VM.")
	nameForm.AddRow3("", w.templateHint.QWidget)
	body.AddLayout(nameForm.QLayout)
	layout.AddWidget2(w.cloudInitBody, 1)

	w.cloudInit.OnToggled(func(checked bool) {
		w.cloudInitBody.SetEnabled(checked)
		page.CompleteChanged()
	})
	w.cloudInitBody.SetEnabled(false)
	w.saveTemplate.OnToggled(func(checked bool) {
		w.templateName.SetEnabled(checked)
		page.CompleteChanged()
	})
	w.templateName.OnTextChanged(func(string) {
		w.updateTemplateHint()
		page.CompleteChanged()
	})
	w.userData.OnTextChanged(func() { page.CompleteChanged() })
	page.OnIsComplete(func(super func() bool) bool { return w.cloudInitValid() })
	return page
}

func (w *newMachineWizard) templateNameValid() bool {
	name := strings.TrimSpace(w.templateName.Text())
	if name == "" || len(name) > maxTemplateName {
		return false
	}
	for _, template := range w.s.templates {
		if strings.EqualFold(template.GetName(), name) {
			return false
		}
	}
	return true
}

func (w *newMachineWizard) updateTemplateHint() {
	name := strings.TrimSpace(w.templateName.Text())
	for _, template := range w.s.templates {
		if strings.EqualFold(template.GetName(), name) {
			w.templateHint.SetText(fmt.Sprintf(`<span style="color:%s">A template with this name already exists.</span>`, colorNegative))
			return
		}
	}
	w.templateHint.SetText(mutedHTML("The template remains independent of the new VM."))
}

func (w *newMachineWizard) cloudInitValid() bool {
	if len(w.userData.ToPlainText()) > maxUserDataSize {
		return false
	}
	if !w.cloudInit.IsChecked() || !w.saveTemplate.IsChecked() {
		return true
	}
	return w.templateNameValid()
}

func (w *newMachineWizard) buildSummary() *qt6.QWizardPage {
	page, layout := pageWithTitle("Summary", "Review the machine before it is created.")
	w.summary = richLabel("")
	w.summary.SetAlignment(qt6.AlignTop | qt6.AlignLeft)
	layout.AddWidget2(scrollable(w.summary.QWidget).QWidget, 1)
	page.SetFinalPage(true)
	return page
}

func (w *newMachineWizard) request() *apprunnerv1.CreateVMRequest {
	request := &apprunnerv1.CreateVMRequest{
		Name:              strings.TrimSpace(w.name.Text()),
		Description:       strings.TrimSpace(w.description.ToPlainText()),
		Cpus:              uint32(w.cpus.Value()),
		MemoryMib:         uint32(w.memory.Value()),
		Firmware:          w.firmwareValue(),
		CloudInitEnabled:  w.cloudInit.IsChecked(),
		CloudInitUserData: "",
	}
	if request.CloudInitEnabled {
		request.CloudInitUserData = w.userData.ToPlainText()
	}
	for _, disk := range w.disks {
		request.Disks = append(request.Disks, &apprunnerv1.VMDiskSpec{SizeGib: disk.sizeGiB, SourceImage: disk.image})
	}
	for _, iso := range w.cdroms {
		request.Cdroms = append(request.Cdroms, &apprunnerv1.VMCDROMSpec{IsoName: iso})
	}
	for _, nic := range w.nics {
		request.Nics = append(request.Nics, nic.spec())
	}
	return request
}

func (w *newMachineWizard) updateSummary() {
	r := w.request()
	var b strings.Builder
	row := func(label, value string) {
		fmt.Fprintf(&b, `<tr><td style="padding:2px 12px 2px 0; color:%s" align="right">%s</td><td style="padding:2px 0">%s</td></tr>`, colorInactive, escape(label), value)
	}
	b.WriteString("<table>")
	row("Name", "<b>"+escape(r.Name)+"</b>")
	if r.Description != "" {
		row("Description", escape(r.Description))
	}
	row("Resources", fmt.Sprintf("%d vCPU · %s", r.Cpus, formatMemory(r.MemoryMib)))
	row("Firmware", escape(w.firmwareSummary()))
	if len(r.Disks) == 0 {
		row("Disks", mutedHTML("none"))
	}
	for index, disk := range r.Disks {
		value := fmt.Sprintf("%d GiB", disk.SizeGib)
		if disk.SourceImage != "" {
			value += " · linked to " + monoHTML("iso/"+disk.SourceImage)
		}
		row(deviceName(index, "disk"), value)
	}
	for index, cdrom := range r.Cdroms {
		row(deviceName(index, "cdrom"), escape(firstNonEmpty(cdrom.IsoName, "empty")))
	}
	if len(r.Nics) == 0 {
		row("Network", mutedHTML("no interfaces"))
	}
	for index, nic := range r.Nics {
		value := "User-mode NAT"
		if nic.Mode == apprunnerv1.NetworkMode_NETWORK_MODE_BRIDGE {
			value = "Host bridge " + escape(nic.BridgeName)
		}
		if nic.MacAddress != "" {
			value += " · " + monoHTML(nic.MacAddress)
		} else {
			value += " · " + mutedHTML("MAC generated")
		}
		row(deviceName(index, "nic"), value)
	}
	if r.CloudInitEnabled {
		value := fmt.Sprintf("enabled · %s of user-data", formatBytes(uint64(len(r.CloudInitUserData))))
		if w.saveTemplate.IsChecked() {
			value += " · saved as template " + escape(strings.TrimSpace(w.templateName.Text()))
		}
		row("Cloud-init", value)
	} else {
		row("Cloud-init", mutedHTML("disabled"))
	}
	b.WriteString("</table>")
	w.summary.SetText(b.String())
}

func (w *newMachineWizard) applySeed(seed wizardSeed) {
	bridges := w.s.network.GetBridges()
	suggestedBridge := ""
	for _, bridge := range bridges {
		if bridge.GetUsableByQemu() {
			suggestedBridge = bridge.GetName()
			break
		}
	}
	if suggestedBridge == "" && len(bridges) > 0 {
		suggestedBridge = bridges[0].GetName()
	}
	requestedCloudImage := seed.sourceRoot == "iso" && cloudImageName.MatchString(seed.sourcePath)
	requestedInstaller := seed.sourceRoot == "iso" && isoName.MatchString(seed.sourcePath)
	useCloudImage := requestedCloudImage || (!requestedInstaller && len(w.s.isos) == 0 && len(w.s.images) > 0)
	cloudImage := ""
	if requestedCloudImage {
		cloudImage = seed.sourcePath
	} else if len(w.s.images) > 0 {
		cloudImage = w.s.images[0].GetName()
	}
	installer := ""
	if requestedInstaller {
		installer = seed.sourcePath
	} else if len(w.s.isos) > 0 {
		installer = w.s.isos[0].GetName()
	}
	w.disks = []diskDraft{{sizeGiB: 20}}
	if useCloudImage && cloudImage != "" {
		w.disks[0].image = cloudImage
	}
	if !useCloudImage && installer != "" {
		w.cdroms = []string{installer}
	}
	w.rebuildDisks()
	w.rebuildCDROMs()
	if suggestedBridge != "" {
		w.addNIC(&apprunnerv1.VMNICSpec{Mode: apprunnerv1.NetworkMode_NETWORK_MODE_BRIDGE, BridgeName: suggestedBridge})
	} else {
		w.addNIC(&apprunnerv1.VMNICSpec{Mode: apprunnerv1.NetworkMode_NETWORK_MODE_NAT})
	}
	if useCloudImage {
		w.cloudInit.SetChecked(true)
	}
	if seed.template != nil {
		w.cloudInit.SetChecked(true)
		if index := w.template.FindData(qt6.NewQVariant14(seed.template.GetName())); index >= 0 {
			w.template.SetCurrentIndex(index)
		}
		w.userData.SetPlainText(seed.template.GetUserData())
	}
	w.updateNameHint()
}

func (w *newMachineWizard) setBusy(busy bool) {
	w.busy = busy
	for _, which := range []qt6.QWizard__WizardButton{qt6.QWizard__BackButton, qt6.QWizard__FinishButton, qt6.QWizard__CancelButton} {
		w.Button(which).SetEnabled(!busy)
	}
	if busy {
		w.Button(qt6.QWizard__FinishButton).SetText("Creating…")
	} else {
		w.Button(qt6.QWizard__FinishButton).SetText("Finish")
	}
}

func (w *newMachineWizard) submit() {
	if w.busy {
		return
	}
	request := w.request()
	saveTemplate := w.cloudInit.IsChecked() && w.saveTemplate.IsChecked()
	templateName := strings.TrimSpace(w.templateName.Text())
	api := w.mw.client.slow
	w.setBusy(true)
	call(func(ctx context.Context) (*apprunnerv1.VirtualMachine, error) {
		if saveTemplate {
			if _, err := api.CreateCloudInitTemplate(ctx, &apprunnerv1.CreateCloudInitTemplateRequest{Name: templateName, UserData: request.CloudInitUserData}); err != nil {
				return nil, err
			}
		}
		response, err := api.CreateVM(ctx, request)
		return response.GetVirtualMachine(), err
	}, func(vm *apprunnerv1.VirtualMachine, err error) {
		w.setBusy(false)
		if err != nil {
			showError(w.QWidget, "Create Virtual Machine", errorMessage(err))
			return
		}
		w.mw.showNotice(fmt.Sprintf("Created %s.", vm.GetName()))
		w.mw.refresh()
		w.Accept()
	})
}
