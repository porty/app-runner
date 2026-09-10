package main

import (
	"context"
	"fmt"
	"strings"

	"github.com/mappu/miqt/qt6"

	apprunnerv1 "github.com/roflware/app-runner/internal/gen/apprunner/v1"
)

// bridgeDialog is Configure Bridge: a KPageDialog with the managed guest
// network services form, DHCP leases, diagnostics, and members.
type bridgeDialog struct {
	*qt6.QDialog
	mw     *mainWindow
	name   string
	bridge *apprunnerv1.NetworkBridge
	busy   bool

	pages  *qt6.QListWidget
	stack  *qt6.QStackedWidget
	locked *messageWidget
	errors *messageWidget

	description *qt6.QLineEdit
	dhcp        *qt6.QCheckBox
	cidr        *qt6.QLineEdit
	nat         *qt6.QCheckBox
	dns         *qt6.QCheckBox
	forwarders  *qt6.QLineEdit
	autoDNS     *qt6.QCheckBox
	suffix      *qt6.QLineEdit
	suffixHint  *qt6.QLabel
	httpBoot    *qt6.QLineEdit

	leases  *qt6.QTreeWidget
	static  *qt6.QPushButton
	remove  *qt6.QPushButton
	clear   *qt6.QPushButton
	diags   *qt6.QTreeWidget
	members *qt6.QTreeWidget
	detach  *qt6.QPushButton

	okButton, applyButton *qt6.QPushButton
}

func openBridgeDialog(mw *mainWindow, name string) *bridgeDialog {
	bridge := findBridge(mw.snapshot.network, name)
	if bridge == nil {
		return nil
	}
	d := &bridgeDialog{QDialog: qt6.NewQDialog(mw.QWidget), mw: mw, name: name}
	d.SetWindowTitle("Configure Bridge — " + name)
	d.SetAttribute(qt6.WA_DeleteOnClose)
	d.Resize(760, 560)
	d.build()
	d.load(bridge)
	d.Show()
	return d
}

func (d *bridgeDialog) build() {
	outer := qt6.NewQVBoxLayout(d.QWidget)
	outer.SetContentsMargins(0, 0, 0, 0)
	outer.SetSpacing(0)
	split := qt6.NewQHBoxLayout2()
	split.SetSpacing(0)
	d.pages = qt6.NewQListWidget2()
	d.pages.SetFixedWidth(150)
	d.pages.SetFrameShape(qt6.QFrame__NoFrame)
	for _, label := range []string{"Network services", "DHCP leases", "Diagnostics", "Members"} {
		d.pages.AddItem(label)
	}
	split.AddWidget(d.pages.QWidget)
	right := qt6.NewQWidget2()
	rightLayout := qt6.NewQVBoxLayout(right)
	rightLayout.SetContentsMargins(14, 12, 14, 8)
	d.locked = newMessageWidget(false)
	d.errors = newMessageWidget(true)
	rightLayout.AddWidget(d.locked.QWidget)
	rightLayout.AddWidget(d.errors.QWidget)
	d.stack = qt6.NewQStackedWidget2()
	d.stack.AddWidget(d.buildServices())
	d.stack.AddWidget(d.buildLeases())
	d.stack.AddWidget(d.buildDiagnostics())
	d.stack.AddWidget(d.buildMembers())
	rightLayout.AddWidget2(d.stack.QWidget, 1)
	split.AddWidget2(right, 1)
	outer.AddLayout2(split.QLayout, 1)
	d.pages.OnCurrentRowChanged(func(row int) { d.stack.SetCurrentIndex(row) })
	d.pages.SetCurrentRow(0)

	buttons := qt6.NewQHBoxLayout2()
	buttons.SetContentsMargins(14, 8, 14, 12)
	help := qt6.NewQPushButton3("Help")
	help.OnClicked(func() {
		qt6.QMessageBox_Information(d.QWidget, "Configure Bridge", "App Runner can run DHCP for guests on this bridge, with NAT for outbound traffic and a DNS forwarder that publishes VM names. The bridge takes the first address of the range and dynamic leases start at .50, leaving earlier addresses for static reservations.")
	})
	buttons.AddWidget(help.QWidget)
	buttons.AddStretch()
	d.okButton = qt6.NewQPushButton3("OK")
	d.okButton.SetDefault(true)
	d.okButton.OnClicked(func() { d.apply(true) })
	d.applyButton = qt6.NewQPushButton3("Apply")
	d.applyButton.OnClicked(func() { d.apply(false) })
	cancel := qt6.NewQPushButton3("Cancel")
	cancel.OnClicked(func() { d.Reject() })
	buttons.AddWidget(d.okButton.QWidget)
	buttons.AddWidget(d.applyButton.QWidget)
	buttons.AddWidget(cancel.QWidget)
	outer.AddLayout(buttons.QLayout)
}

func (d *bridgeDialog) buildServices() *qt6.QWidget {
	page := qt6.NewQWidget2()
	layout := qt6.NewQVBoxLayout(page)
	layout.SetContentsMargins(0, 0, 0, 0)
	form := qt6.NewQFormLayout2()
	fillForm(form)
	d.description = qt6.NewQLineEdit2()
	d.description.SetMaxLength(255)
	d.description.SetPlaceholderText("Shown when selecting this bridge for a virtual machine")
	form.AddRow3("Description:", d.description.QWidget)
	d.dhcp = qt6.NewQCheckBox3("Enable App Runner DHCP for this bridge")
	form.AddRow3("", d.dhcp.QWidget)
	d.cidr = qt6.NewQLineEdit2()
	d.cidr.SetFont(monospaceFont())
	d.cidr.SetMaximumWidth(220)
	form.AddRow3("IPv4 range (CIDR):", d.cidr.QWidget)
	form.AddRow3("", hint("The bridge uses .1; dynamic leases begin at .50, leaving earlier addresses for static reservations.").QWidget)
	d.nat = qt6.NewQCheckBox3("Enable NAT and outbound routing for this range")
	form.AddRow3("", d.nat.QWidget)
	layout.AddLayout(form.QLayout)

	dnsBox := qt6.NewQGroupBox3("DNS")
	dnsForm := qt6.NewQFormLayout(dnsBox.QWidget)
	fillForm(dnsForm)
	d.dns = qt6.NewQCheckBox3("Enable managed DNS for this bridge")
	dnsForm.AddRow3("", d.dns.QWidget)
	d.forwarders = qt6.NewQLineEdit2()
	d.forwarders.SetFont(monospaceFont())
	d.forwarders.SetPlaceholderText("1.1.1.1, 8.8.8.8")
	dnsForm.AddRow3("Forwarders:", d.forwarders.QWidget)
	dnsForm.AddRow3("", hint("Separate addresses with commas or spaces. An optional port is supported.").QWidget)
	d.autoDNS = qt6.NewQCheckBox3("Auto DNS for virtual machines on this bridge")
	dnsForm.AddRow3("", d.autoDNS.QWidget)
	suffixRow := qt6.NewQHBoxLayout2()
	d.suffix = qt6.NewQLineEdit2()
	d.suffix.SetMaximumWidth(220)
	suffixRow.AddWidget(d.suffix.QWidget)
	d.suffixHint = hint("")
	suffixRow.AddWidget2(d.suffixHint.QWidget, 1)
	dnsForm.AddRow4("Auto DNS suffix:", suffixRow.QLayout)
	layout.AddWidget(dnsBox.QWidget)

	bootForm := qt6.NewQFormLayout2()
	fillForm(bootForm)
	d.httpBoot = qt6.NewQLineEdit2()
	d.httpBoot.SetFont(monospaceFont())
	d.httpBoot.SetPlaceholderText("http://192.168.100.50/boot/bootx64.efi")
	bootForm.AddRow3("UEFI HTTP boot URL:", d.httpBoot.QWidget)
	bootForm.AddRow3("", hint("Offered as the boot file to UEFI HTTP boot clients on this bridge. Leave empty to disable.").QWidget)
	layout.AddLayout(bootForm.QLayout)
	layout.AddStretch()

	for _, box := range []*qt6.QCheckBox{d.dhcp, d.nat, d.dns, d.autoDNS} {
		box.OnToggled(func(bool) { d.updateForm() })
	}
	for _, edit := range []*qt6.QLineEdit{d.description, d.cidr, d.forwarders, d.suffix, d.httpBoot} {
		edit.OnTextChanged(func(string) { d.updateForm() })
	}
	return page
}

func (d *bridgeDialog) buildLeases() *qt6.QWidget {
	page := qt6.NewQWidget2()
	layout := qt6.NewQVBoxLayout(page)
	layout.SetContentsMargins(0, 0, 0, 0)
	layout.AddWidget(mutedLabel("Static reservations persist when dynamic leases are cleared.").QWidget)
	d.leases = newLeaseTable()
	d.leases.OnItemSelectionChanged(func() { d.updateButtons() })
	layout.AddWidget2(d.leases.QWidget, 1)
	buttons := qt6.NewQHBoxLayout2()
	d.static = qt6.NewQPushButton3("Make Static…")
	d.static.OnClicked(func() {
		lease := findLease(d.bridge.GetDhcp().GetLeases(), itemData(d.leases.CurrentItem()))
		if lease == nil {
			return
		}
		suggested := lease.GetAddress()
		if !lease.GetIsStatic() {
			suggested = firstAvailableStaticAddress(d.bridge.GetDhcp().GetCidr(), d.bridge.GetDhcp().GetPoolStart(), d.bridge.GetDhcp().GetLeases())
		}
		address, ok := staticLeaseDialog(d.QWidget, lease, suggested)
		if !ok {
			return
		}
		d.leaseOperation(func(ctx context.Context) (*apprunnerv1.NetworkingStatus, error) {
			r, err := d.mw.client.api.SetBridgeDHCPStaticLease(ctx, &apprunnerv1.SetBridgeDHCPStaticLeaseRequest{BridgeName: d.name, HardwareAddress: lease.GetHardwareAddress(), Address: address})
			return r.GetStatus(), err
		})
	})
	d.remove = qt6.NewQPushButton3("Delete Lease")
	d.remove.OnClicked(func() {
		lease := findLease(d.bridge.GetDhcp().GetLeases(), itemData(d.leases.CurrentItem()))
		if lease == nil || !confirm(d.QWidget, "Delete Lease", fmt.Sprintf("Delete the lease for %s?", firstNonEmpty(lease.GetVmName(), lease.GetHardwareAddress())), "Delete", true) {
			return
		}
		d.leaseOperation(func(ctx context.Context) (*apprunnerv1.NetworkingStatus, error) {
			r, err := d.mw.client.api.DeleteBridgeDHCPLease(ctx, &apprunnerv1.DeleteBridgeDHCPLeaseRequest{BridgeName: d.name, HardwareAddress: lease.GetHardwareAddress()})
			return r.GetStatus(), err
		})
	})
	d.clear = qt6.NewQPushButton3("Clear Dynamic Leases")
	d.clear.OnClicked(func() {
		if !confirm(d.QWidget, "Clear Leases", "Clear all dynamic DHCP leases on this bridge? Static reservations will remain.", "Clear Leases", true) {
			return
		}
		d.leaseOperation(func(ctx context.Context) (*apprunnerv1.NetworkingStatus, error) {
			r, err := d.mw.client.api.ClearBridgeDHCPLeases(ctx, &apprunnerv1.ClearBridgeDHCPLeasesRequest{BridgeName: d.name})
			return r.GetStatus(), err
		})
	})
	buttons.AddWidget(d.static.QWidget)
	buttons.AddWidget(d.remove.QWidget)
	buttons.AddStretch()
	buttons.AddWidget(d.clear.QWidget)
	layout.AddLayout(buttons.QLayout)
	return page
}

func (d *bridgeDialog) buildDiagnostics() *qt6.QWidget {
	page := qt6.NewQWidget2()
	layout := qt6.NewQVBoxLayout(page)
	layout.SetContentsMargins(0, 0, 0, 0)
	d.diags = newDiagnosticsTable()
	layout.AddWidget2(d.diags.QWidget, 1)
	return page
}

func (d *bridgeDialog) buildMembers() *qt6.QWidget {
	page := qt6.NewQWidget2()
	layout := qt6.NewQVBoxLayout(page)
	layout.SetContentsMargins(0, 0, 0, 0)
	layout.AddWidget(mutedLabel("Host interfaces enslaved to this bridge. Attaching or detaching is a temporary change until confirmed.").QWidget)
	d.members = newTable("Interface")
	d.members.OnItemSelectionChanged(func() { d.updateButtons() })
	layout.AddWidget2(d.members.QWidget, 1)
	buttons := qt6.NewQHBoxLayout2()
	attach := qt6.NewQPushButton4(themeIcon("network-wired", 0), "Attach Interface…")
	attach.OnClicked(func() {
		d.mw.attachInterface(d.name)
		d.Reject()
	})
	d.detach = qt6.NewQPushButton3("Detach")
	d.detach.OnClicked(func() {
		if member := itemData(d.members.CurrentItem()); member != "" {
			d.mw.detachInterface(d.name, member)
			d.Reject()
		}
	})
	buttons.AddWidget(attach.QWidget)
	buttons.AddWidget(d.detach.QWidget)
	buttons.AddStretch()
	layout.AddLayout(buttons.QLayout)
	return page
}

func (d *bridgeDialog) runningWorkloads() []string {
	var names []string
	for _, workload := range d.bridge.GetWorkloads() {
		if workload.GetRunning() {
			names = append(names, workload.GetName())
		}
	}
	return names
}

func (d *bridgeDialog) load(bridge *apprunnerv1.NetworkBridge) {
	d.bridge = bridge
	dhcp := bridge.GetDhcp()
	d.description.SetText(bridge.GetDescription())
	d.dhcp.SetChecked(dhcp.GetEnabled())
	d.cidr.SetText(firstNonEmpty(dhcp.GetCidr(), "192.168.100.0/24"))
	d.nat.SetChecked(dhcp.GetNatEnabled())
	d.dns.SetChecked(dhcp.GetDnsEnabled())
	d.forwarders.SetText(strings.Join(dhcp.GetDnsForwarders(), ", "))
	d.autoDNS.SetChecked(dhcp.GetAutoDns())
	d.suffix.SetText(firstNonEmpty(dhcp.GetDnsSuffix(), strings.ToLower(bridge.GetName())+".internal"))
	d.httpBoot.SetText(dhcp.GetHttpBootUrl())
	fillLeases(d.leases, dhcp.GetLeases())
	fillDiagnostics(d.diags, bridge.GetDiagnostics())
	d.members.Clear()
	for _, member := range bridge.GetMemberInterfaces() {
		item := qt6.NewQTreeWidgetItem2([]string{member})
		setItemData(item, member)
		d.members.AddTopLevelItem(item)
	}
	if running := d.runningWorkloads(); len(running) > 0 {
		d.locked.show(messageWarning, fmt.Sprintf("Network service settings are locked while workloads on this bridge are running (%s). The description can still be changed.", escape(strings.Join(running, ", "))))
	} else {
		d.locked.Hide()
	}
	d.updateForm()
}

func (d *bridgeDialog) locking() bool {
	return len(d.runningWorkloads()) > 0
}

func (d *bridgeDialog) updateForm() {
	locked := d.locking() || d.busy
	enabled := d.dhcp.IsChecked()
	d.dhcp.SetEnabled(!locked)
	d.cidr.SetEnabled(!locked && enabled)
	d.nat.SetEnabled(!locked && enabled)
	d.dns.SetEnabled(!locked && enabled)
	dns := enabled && d.dns.IsChecked()
	d.forwarders.SetEnabled(!locked && dns)
	d.autoDNS.SetEnabled(!locked && dns)
	auto := dns && d.autoDNS.IsChecked()
	d.suffix.SetEnabled(!locked && auto)
	d.httpBoot.SetEnabled(!locked && enabled)
	if validDNSSuffix(d.suffix.Text()) {
		d.suffixHint.SetText(mutedHTML("VMs are published as VM-NAME." + strings.TrimSuffix(strings.TrimSpace(d.suffix.Text()), ".")))
	} else {
		d.suffixHint.SetText(fmt.Sprintf(`<span style="color:%s">Use one or more DNS labels separated by dots.</span>`, colorNegative))
	}
	d.updateButtons()
}

func (d *bridgeDialog) request() *apprunnerv1.ConfigureBridgeRequest {
	enabled := d.dhcp.IsChecked()
	dns := enabled && d.dns.IsChecked()
	auto := dns && d.autoDNS.IsChecked()
	request := &apprunnerv1.ConfigureBridgeRequest{
		BridgeName:  d.name,
		Description: strings.TrimSpace(d.description.Text()),
		DhcpEnabled: enabled,
		Cidr:        strings.TrimSpace(d.cidr.Text()),
		NatEnabled:  enabled && d.nat.IsChecked(),
		DnsEnabled:  dns,
		AutoDns:     auto,
	}
	if dns {
		for _, forwarder := range strings.FieldsFunc(d.forwarders.Text(), func(r rune) bool { return r == ',' || r == ' ' || r == '\n' || r == '\t' }) {
			request.DnsForwarders = append(request.DnsForwarders, forwarder)
		}
	}
	if auto {
		request.DnsSuffix = strings.TrimSpace(d.suffix.Text())
	}
	if enabled {
		request.HttpBootUrl = strings.TrimSpace(d.httpBoot.Text())
	}
	return request
}

func (d *bridgeDialog) dirty() bool {
	r := d.request()
	dhcp := d.bridge.GetDhcp()
	return r.Description != d.bridge.GetDescription() || r.DhcpEnabled != dhcp.GetEnabled() || (r.DhcpEnabled && (r.Cidr != dhcp.GetCidr() || r.NatEnabled != dhcp.GetNatEnabled() || r.DnsEnabled != dhcp.GetDnsEnabled() || strings.Join(r.DnsForwarders, ",") != strings.Join(dhcp.GetDnsForwarders(), ",") || r.AutoDns != dhcp.GetAutoDns() || r.DnsSuffix != dhcp.GetDnsSuffix() || r.HttpBootUrl != dhcp.GetHttpBootUrl()))
}

func (d *bridgeDialog) valid() bool {
	r := d.request()
	if !r.DhcpEnabled {
		return true
	}
	if r.Cidr == "" {
		return false
	}
	if r.DnsEnabled && len(r.DnsForwarders) == 0 {
		return false
	}
	if r.AutoDns && !validDNSSuffix(r.DnsSuffix) {
		return false
	}
	return true
}

func (d *bridgeDialog) updateButtons() {
	d.applyButton.SetEnabled(!d.busy && d.dirty() && d.valid())
	d.okButton.SetEnabled(!d.busy && (!d.dirty() || d.valid()))
	lease := findLease(d.bridge.GetDhcp().GetLeases(), itemData(d.leases.CurrentItem()))
	d.static.SetEnabled(!d.busy && lease != nil)
	if lease != nil && lease.GetIsStatic() {
		d.static.SetText("Change Static IP…")
	} else {
		d.static.SetText("Make Static…")
	}
	d.remove.SetEnabled(!d.busy && lease != nil)
	dynamic := false
	for _, candidate := range d.bridge.GetDhcp().GetLeases() {
		dynamic = dynamic || !candidate.GetIsStatic()
	}
	d.clear.SetEnabled(!d.busy && dynamic)
	canManage := !d.busy && d.mw.snapshot.network.GetCanManage() && d.mw.snapshot.network.GetPendingChange() == nil
	d.detach.SetEnabled(canManage && d.members.CurrentItem() != nil)
}

func (d *bridgeDialog) setBusy(busy bool) {
	d.busy = busy
	d.updateForm()
}

func (d *bridgeDialog) leaseOperation(fn func(ctx context.Context) (*apprunnerv1.NetworkingStatus, error)) {
	d.errors.Hide()
	d.setBusy(true)
	call(fn, func(status *apprunnerv1.NetworkingStatus, err error) {
		d.setBusy(false)
		d.mw.refresh()
		if err != nil {
			d.errors.show(messageError, escape(errorMessage(err)))
			return
		}
		if bridge := findBridge(status, d.name); bridge != nil {
			d.load(bridge)
		}
	})
}

func (d *bridgeDialog) apply(closeAfter bool) {
	if !d.dirty() {
		if closeAfter {
			d.Accept()
		}
		return
	}
	if !d.valid() {
		return
	}
	request := d.request()
	d.errors.Hide()
	d.setBusy(true)
	call(func(ctx context.Context) (*apprunnerv1.NetworkingStatus, error) {
		r, err := d.mw.client.api.ConfigureBridge(ctx, request)
		return r.GetStatus(), err
	}, func(status *apprunnerv1.NetworkingStatus, err error) {
		d.setBusy(false)
		d.mw.refresh()
		if err != nil {
			d.errors.show(messageError, escape(errorMessage(err)))
			return
		}
		if bridge := findBridge(status, d.name); bridge != nil {
			d.load(bridge)
		}
		if closeAfter {
			d.Accept()
		}
	})
}
