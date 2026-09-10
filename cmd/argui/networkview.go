package main

import (
	"context"
	"fmt"
	"strings"

	"github.com/mappu/miqt/qt6"

	apprunnerv1 "github.com/roflware/app-runner/internal/gen/apprunner/v1"
)

// fillDiagnostics renders diagnostics into a Status / Check / Result /
// Remediation table.
func newDiagnosticsTable() *qt6.QTreeWidget {
	table := newTable("Status", "Check", "Diagnostic result", "Remediation")
	table.SetWordWrap(true)
	table.SetIconSize(qt6.NewQSize2(12, 12))
	table.Header().SetSectionResizeMode2(0, qt6.QHeaderView__ResizeToContents)
	table.Header().SetSectionResizeMode2(1, qt6.QHeaderView__ResizeToContents)
	table.Header().SetSectionResizeMode2(2, qt6.QHeaderView__Stretch)
	table.Header().SetSectionResizeMode2(3, qt6.QHeaderView__Stretch)
	return table
}

func fillDiagnostics(table *qt6.QTreeWidget, diagnostics []*apprunnerv1.NetworkDiagnostic) {
	table.Clear()
	for _, diagnostic := range diagnostics {
		label, hex := diagnosticLabel(diagnostic.GetStatus())
		item := qt6.NewQTreeWidgetItem2([]string{label, diagnostic.GetLabel(), diagnostic.GetDetail(), firstNonEmpty(diagnostic.GetRemediation(), "—")})
		item.SetIcon(0, dotIcon(hex))
		setBold(item, 1)
		if diagnostic.GetRemediation() == "" {
			setMuted(item, 3)
		}
		item.SetToolTip(2, diagnostic.GetDetail())
		item.SetToolTip(3, diagnostic.GetRemediation())
		table.AddTopLevelItem(item)
	}
}

func newLeaseTable() *qt6.QTreeWidget {
	table := newTable("Virtual machine", "Address", "Lease", "MAC address")
	table.Header().SetSectionResizeMode2(0, qt6.QHeaderView__Stretch)
	table.Header().SetSectionResizeMode2(1, qt6.QHeaderView__ResizeToContents)
	table.Header().SetSectionResizeMode2(2, qt6.QHeaderView__ResizeToContents)
	table.Header().SetSectionResizeMode2(3, qt6.QHeaderView__ResizeToContents)
	return table
}

func fillLeases(table *qt6.QTreeWidget, leases []*apprunnerv1.DHCPLease) {
	selected := itemData(table.CurrentItem())
	table.Clear()
	for _, lease := range leases {
		expiry := "Static reservation"
		if !lease.GetIsStatic() {
			expiry = formatTime(lease.GetExpiresAt())
		}
		item := qt6.NewQTreeWidgetItem2([]string{firstNonEmpty(lease.GetVmName(), "Unmanaged client"), lease.GetAddress(), expiry, lease.GetHardwareAddress()})
		setItemData(item, lease.GetHardwareAddress())
		if lease.GetVmName() != "" {
			setBold(item, 0)
		} else {
			setMuted(item, 0)
		}
		setMono(item, 1)
		setMono(item, 3)
		table.AddTopLevelItem(item)
		if lease.GetHardwareAddress() == selected {
			table.SetCurrentItem(item)
		}
	}
}

func findLease(leases []*apprunnerv1.DHCPLease, hardwareAddress string) *apprunnerv1.DHCPLease {
	for _, lease := range leases {
		if lease.GetHardwareAddress() == hardwareAddress {
			return lease
		}
	}
	return nil
}

// bridgeView shows one Linux bridge: state, guest services, workloads,
// members, DHCP leases, and diagnostics.
type bridgeView struct {
	mw         *mainWindow
	root       *qt6.QWidget
	tb         *qt6.QToolBar
	bridgeName string
	bridge     *apprunnerv1.NetworkBridge

	heading  *qt6.QLabel
	summary  *qt6.QLabel
	issues   *qt6.QLabel
	services *qt6.QLabel

	workloads *qt6.QTreeWidget
	members   *qt6.QTreeWidget
	detach    *qt6.QPushButton
	leases    *qt6.QTreeWidget
	static    *qt6.QPushButton
	remove    *qt6.QPushButton
	clear     *qt6.QPushButton
	diags     *qt6.QTreeWidget
	missing   *qt6.QLabel
}

func newBridgeView(mw *mainWindow) *bridgeView {
	v := &bridgeView{mw: mw}
	content := qt6.NewQWidget2()
	layout := qt6.NewQVBoxLayout(content)
	layout.SetContentsMargins(14, 12, 14, 12)
	layout.SetSpacing(10)
	v.heading = richLabel("")
	layout.AddWidget(v.heading.QWidget)
	v.summary = richLabel("")
	layout.AddWidget(v.summary.QWidget)
	v.issues = richLabel("")
	layout.AddWidget(v.issues.QWidget)
	v.missing = richLabel("")
	layout.AddWidget(v.missing.QWidget)

	servicesBox := qt6.NewQGroupBox3("Guest network services")
	servicesLayout := qt6.NewQVBoxLayout(servicesBox.QWidget)
	v.services = richLabel("")
	servicesLayout.AddWidget(v.services.QWidget)
	layout.AddWidget(servicesBox.QWidget)

	row := qt6.NewQHBoxLayout2()
	row.SetSpacing(10)
	workloadsBox := qt6.NewQGroupBox3("Workloads")
	workloadsLayout := qt6.NewQVBoxLayout(workloadsBox.QWidget)
	v.workloads = newTable("Name", "Type", "State")
	v.workloads.SetIconSize(qt6.NewQSize2(12, 12))
	v.workloads.SetMinimumHeight(110)
	v.workloads.Header().SetSectionResizeMode2(0, qt6.QHeaderView__Stretch)
	v.workloads.OnItemDoubleClicked(func(item *qt6.QTreeWidgetItem, _ int) {
		mw.showView("vms")
		mw.vmView.selectVM(itemData(item))
	})
	workloadsLayout.AddWidget(v.workloads.QWidget)
	row.AddWidget2(workloadsBox.QWidget, 1)
	membersBox := qt6.NewQGroupBox3("Member interfaces")
	membersLayout := qt6.NewQVBoxLayout(membersBox.QWidget)
	v.members = newTable("Interface", "State", "Addresses")
	v.members.SetIconSize(qt6.NewQSize2(12, 12))
	v.members.SetMinimumHeight(110)
	v.members.Header().SetSectionResizeMode2(0, qt6.QHeaderView__Stretch)
	v.members.OnItemSelectionChanged(func() { v.updateButtons() })
	membersLayout.AddWidget(v.members.QWidget)
	memberButtons := qt6.NewQHBoxLayout2()
	attach := qt6.NewQPushButton4(themeIcon("network-wired", 0), "Attach Interface…")
	attach.OnClicked(func() { mw.attachInterface(v.bridgeName) })
	memberButtons.AddWidget(attach.QWidget)
	v.detach = qt6.NewQPushButton3("Detach")
	v.detach.OnClicked(func() {
		if member := itemData(v.members.CurrentItem()); member != "" {
			mw.detachInterface(v.bridgeName, member)
		}
	})
	memberButtons.AddWidget(v.detach.QWidget)
	memberButtons.AddStretch()
	membersLayout.AddLayout(memberButtons.QLayout)
	row.AddWidget2(membersBox.QWidget, 1)
	layout.AddLayout(row.QLayout)

	leasesBox := qt6.NewQGroupBox3("DHCP leases")
	leasesLayout := qt6.NewQVBoxLayout(leasesBox.QWidget)
	leasesLayout.AddWidget(mutedLabel("Static reservations persist when dynamic leases are cleared.").QWidget)
	v.leases = newLeaseTable()
	v.leases.SetMinimumHeight(120)
	v.leases.OnItemSelectionChanged(func() { v.updateButtons() })
	leasesLayout.AddWidget(v.leases.QWidget)
	leaseButtons := qt6.NewQHBoxLayout2()
	v.static = qt6.NewQPushButton3("Make Static…")
	v.static.OnClicked(func() { v.makeStatic() })
	v.remove = qt6.NewQPushButton3("Delete Lease")
	v.remove.OnClicked(func() { v.deleteLease() })
	v.clear = qt6.NewQPushButton3("Clear Dynamic Leases")
	v.clear.OnClicked(func() {
		if confirm(mw.QWidget, "Clear Leases", "Clear all dynamic DHCP leases on this bridge? Static reservations will remain.", "Clear Leases", true) {
			name := v.bridgeName
			mw.operation("", func(ctx context.Context) error {
				_, err := mw.client.api.ClearBridgeDHCPLeases(ctx, &apprunnerv1.ClearBridgeDHCPLeasesRequest{BridgeName: name})
				return err
			})
		}
	})
	leaseButtons.AddWidget(v.static.QWidget)
	leaseButtons.AddWidget(v.remove.QWidget)
	leaseButtons.AddStretch()
	leaseButtons.AddWidget(v.clear.QWidget)
	leasesLayout.AddLayout(leaseButtons.QLayout)
	layout.AddWidget(leasesBox.QWidget)

	diagsBox := qt6.NewQGroupBox3("Diagnostics")
	diagsLayout := qt6.NewQVBoxLayout(diagsBox.QWidget)
	v.diags = newDiagnosticsTable()
	v.diags.SetMinimumHeight(140)
	diagsLayout.AddWidget(v.diags.QWidget)
	layout.AddWidget(diagsBox.QWidget)
	layout.AddStretch()
	v.root = scrollable(content).QWidget

	v.tb = qt6.NewQToolBar2("Network")
	n := mw.network
	for _, a := range []*qt6.QAction{n.configure, n.upDown, n.attach, n.restore, nil, n.deleteBridge} {
		if a == nil {
			v.tb.AddSeparator()
		} else {
			v.tb.AddAction(a)
		}
	}
	v.tb.AddWidget(spacer())
	v.tb.AddAction(mw.refreshAction)
	return v
}

func (v *bridgeView) widget() *qt6.QWidget   { return v.root }
func (v *bridgeView) toolbar() *qt6.QToolBar { return v.tb }

func (v *bridgeView) title() string {
	return "Bridge " + v.bridgeName
}

func (v *bridgeView) setBridge(name string) {
	if v.bridgeName != name {
		v.bridgeName = name
		v.refresh(v.mw.snapshot)
	}
}

func (v *bridgeView) selectedLease() *apprunnerv1.DHCPLease {
	return findLease(v.bridge.GetDhcp().GetLeases(), itemData(v.leases.CurrentItem()))
}

func (v *bridgeView) makeStatic() {
	lease := v.selectedLease()
	if lease == nil {
		return
	}
	dhcp := v.bridge.GetDhcp()
	suggested := lease.GetAddress()
	if !lease.GetIsStatic() {
		suggested = firstAvailableStaticAddress(dhcp.GetCidr(), dhcp.GetPoolStart(), dhcp.GetLeases())
	}
	address, ok := staticLeaseDialog(v.mw.QWidget, lease, suggested)
	if !ok {
		return
	}
	name := v.bridgeName
	v.mw.operation("", func(ctx context.Context) error {
		_, err := v.mw.client.api.SetBridgeDHCPStaticLease(ctx, &apprunnerv1.SetBridgeDHCPStaticLeaseRequest{BridgeName: name, HardwareAddress: lease.GetHardwareAddress(), Address: address})
		return err
	})
}

func (v *bridgeView) deleteLease() {
	lease := v.selectedLease()
	if lease == nil {
		return
	}
	kind := "DHCP lease"
	if lease.GetIsStatic() {
		kind = "static reservation"
	}
	if !confirm(v.mw.QWidget, "Delete Lease", fmt.Sprintf("Delete the %s for %s?", kind, firstNonEmpty(lease.GetVmName(), lease.GetHardwareAddress())), "Delete", true) {
		return
	}
	name := v.bridgeName
	v.mw.operation("", func(ctx context.Context) error {
		_, err := v.mw.client.api.DeleteBridgeDHCPLease(ctx, &apprunnerv1.DeleteBridgeDHCPLeaseRequest{BridgeName: name, HardwareAddress: lease.GetHardwareAddress()})
		return err
	})
}

func (v *bridgeView) refresh(s *snapshot) {
	v.bridge = findBridge(s.network, v.bridgeName)
	bridge := v.bridge
	if bridge == nil {
		v.heading.SetText("<h2>" + escape(v.bridgeName) + "</h2>")
		v.summary.SetText(mutedHTML("This bridge is no longer reported by the host."))
		v.issues.SetText("")
		v.missing.SetText("")
		v.services.SetText("")
		v.workloads.Clear()
		v.members.Clear()
		v.leases.Clear()
		v.diags.Clear()
		v.updateButtons()
		return
	}
	state, hex := "Down", colorInactive
	if bridge.GetIsUp() {
		state, hex = "Up", colorPositive
	}
	if bridgeMissing(bridge) {
		state, hex = "Missing", colorNegative
	}
	heading := fmt.Sprintf(`<span style="font-size:15pt; font-weight:bold">%s</span> &nbsp; %s %s`, escape(bridge.GetName()), dotHTML(hex), escape(state))
	if bridge.GetDescription() != "" {
		heading += " &nbsp; " + mutedHTML(bridge.GetDescription())
	}
	v.heading.SetText(heading)
	details := []string{}
	if len(bridge.GetAddresses()) > 0 {
		details = append(details, monoHTML(strings.Join(bridge.GetAddresses(), ", ")))
	} else {
		details = append(details, mutedHTML("No addresses"))
	}
	if bridge.GetMtu() > 0 {
		details = append(details, fmt.Sprintf("MTU %d", bridge.GetMtu()))
	}
	if bridge.GetHardwareAddress() != "" {
		details = append(details, monoHTML(bridge.GetHardwareAddress()))
	}
	v.summary.SetText(strings.Join(details, " · "))

	var chips []string
	for _, diagnostic := range bridge.GetDiagnostics() {
		switch diagnostic.GetStatus() {
		case apprunnerv1.DiagnosticStatus_DIAGNOSTIC_STATUS_FAIL:
			chips = append(chips, chipHTML(diagnostic.GetLabel(), colorNegative))
		case apprunnerv1.DiagnosticStatus_DIAGNOSTIC_STATUS_WARNING:
			chips = append(chips, chipHTML(diagnostic.GetLabel(), colorNeutral))
		}
	}
	if len(chips) == 0 && !bridge.GetUsableByQemu() {
		chips = append(chips, chipHTML("Diagnostics required", colorNeutral))
	}
	if len(chips) == 0 {
		chips = append(chips, chipHTML("Usable by QEMU", colorPositive))
	}
	v.issues.SetText(strings.Join(chips, " "))
	if bridgeMissing(bridge) {
		v.missing.SetText(fmt.Sprintf(`<span style="color:%s">App Runner remembers this bridge, but the host no longer has it. Use Network → Restore Bridge to recreate it.</span>`, colorNeutral))
	} else {
		v.missing.SetText("")
	}

	dhcp := bridge.GetDhcp()
	if !dhcp.GetEnabled() {
		v.services.SetText(mutedHTML("No managed services. App Runner DHCP, NAT, and DNS can be enabled with Configure Bridge…"))
	} else {
		serviceChip := func(label string, running bool) string {
			if running {
				return chipHTML(label, colorPositive)
			}
			return chipHTML(label+" · not running", colorNeutral)
		}
		parts := []string{serviceChip("DHCP "+dhcp.GetCidr(), dhcp.GetRunning())}
		if dhcp.GetNatEnabled() {
			parts = append(parts, serviceChip("NAT", dhcp.GetNatRunning()))
		}
		if dhcp.GetDnsEnabled() {
			label := "DNS"
			if dhcp.GetAutoDns() && dhcp.GetDnsSuffix() != "" {
				label += " · " + dhcp.GetDnsSuffix()
			}
			parts = append(parts, serviceChip(label, dhcp.GetDnsRunning()))
		}
		if dhcp.GetHttpBootUrl() != "" {
			parts = append(parts, chipHTML("UEFI HTTP boot", colorHighlight))
		}
		text := strings.Join(parts, " ") + "<br>" + mutedHTML(fmt.Sprintf("Server %s · pool %s – %s · %s", dhcp.GetServerAddress(), dhcp.GetPoolStart(), dhcp.GetPoolEnd(), pluralize(int(dhcp.GetActiveLeases()), "active lease", "active leases")))
		if len(dhcp.GetDnsForwarders()) > 0 {
			text += mutedHTML(" · forwarders " + strings.Join(dhcp.GetDnsForwarders(), ", "))
		}
		if dhcp.GetHttpBootUrl() != "" {
			text += "<br>" + mutedHTML("HTTP boot ") + monoHTML(dhcp.GetHttpBootUrl())
		}
		if dhcp.GetLastError() != "" {
			text += fmt.Sprintf(`<br><span style="color:%s">%s</span>`, colorNegative, escape(dhcp.GetLastError()))
		}
		v.services.SetText(text)
	}

	v.workloads.Clear()
	for _, workload := range bridge.GetWorkloads() {
		state, hex := "Stopped", colorInactive
		if workload.GetRunning() {
			state, hex = "Running", colorPositive
		}
		item := qt6.NewQTreeWidgetItem2([]string{workload.GetName(), strings.ReplaceAll(workload.GetWorkloadType(), "_", " "), state})
		item.SetIcon(2, dotIcon(hex))
		setItemData(item, workload.GetId())
		setBold(item, 0)
		v.workloads.AddTopLevelItem(item)
	}
	selectedMember := itemData(v.members.CurrentItem())
	v.members.Clear()
	for _, member := range bridge.GetMemberInterfaces() {
		state, hex, addresses := "Unknown", colorInactive, ""
		for _, iface := range s.network.GetInterfaces() {
			if iface.GetName() == member {
				state, hex = "Down", colorInactive
				if iface.GetIsUp() {
					state, hex = "Up", colorPositive
				}
				addresses = strings.Join(iface.GetAddresses(), ", ")
			}
		}
		item := qt6.NewQTreeWidgetItem2([]string{member, state, addresses})
		item.SetIcon(1, dotIcon(hex))
		setItemData(item, member)
		setBold(item, 0)
		setMono(item, 2)
		v.members.AddTopLevelItem(item)
		if member == selectedMember {
			v.members.SetCurrentItem(item)
		}
	}
	fillLeases(v.leases, dhcp.GetLeases())
	fillDiagnostics(v.diags, bridge.GetDiagnostics())
	v.updateButtons()
}

func (v *bridgeView) updateButtons() {
	network := v.mw.snapshot.network
	canManage := !v.mw.busy && network.GetCanManage() && network.GetPendingChange() == nil && v.bridge != nil
	v.detach.SetEnabled(canManage && v.members.CurrentItem() != nil)
	lease := v.selectedLease()
	v.static.SetEnabled(!v.mw.busy && lease != nil)
	if lease != nil && lease.GetIsStatic() {
		v.static.SetText("Change Static IP…")
	} else {
		v.static.SetText("Make Static…")
	}
	v.remove.SetEnabled(!v.mw.busy && lease != nil)
	dynamic := false
	for _, candidate := range v.bridge.GetDhcp().GetLeases() {
		dynamic = dynamic || !candidate.GetIsStatic()
	}
	v.clear.SetEnabled(!v.mw.busy && dynamic)
}

func (v *bridgeView) statusText() string {
	if v.bridge == nil {
		return "Bridge " + v.bridgeName + " not found"
	}
	return fmt.Sprintf("%s · %s · %s", bridgeServices(v.bridge), pluralize(len(v.bridge.GetWorkloads()), "workload", "workloads"), pluralize(len(v.bridge.GetMemberInterfaces()), "member", "members"))
}

// interfacesView shows the host's interfaces, the backend identity, and the
// host-level networking diagnostics.
type interfacesView struct {
	mw         *mainWindow
	root       *qt6.QWidget
	tb         *qt6.QToolBar
	identity   *qt6.QLabel
	interfaces *qt6.QTreeWidget
	diags      *qt6.QTreeWidget
	network    *apprunnerv1.NetworkingStatus
}

func newInterfacesView(mw *mainWindow) *interfacesView {
	v := &interfacesView{mw: mw}
	content := qt6.NewQWidget2()
	layout := qt6.NewQVBoxLayout(content)
	layout.SetContentsMargins(14, 12, 14, 12)
	layout.SetSpacing(10)
	identityBox := qt6.NewQGroupBox3("Backend identity")
	identityLayout := qt6.NewQVBoxLayout(identityBox.QWidget)
	v.identity = richLabel("")
	identityLayout.AddWidget(v.identity.QWidget)
	layout.AddWidget(identityBox.QWidget)

	interfacesBox := qt6.NewQGroupBox3("Host interfaces")
	interfacesLayout := qt6.NewQVBoxLayout(interfacesBox.QWidget)
	v.interfaces = newTable("Interface", "State", "Master", "Addresses", "MTU", "MAC address")
	v.interfaces.SetIconSize(qt6.NewQSize2(12, 12))
	v.interfaces.SetMinimumHeight(180)
	v.interfaces.Header().SetSectionResizeMode2(0, qt6.QHeaderView__ResizeToContents)
	v.interfaces.Header().SetSectionResizeMode2(3, qt6.QHeaderView__Stretch)
	interfacesLayout.AddWidget(v.interfaces.QWidget)
	layout.AddWidget2(interfacesBox.QWidget, 1)

	diagsBox := qt6.NewQGroupBox3("Backend diagnostics")
	diagsLayout := qt6.NewQVBoxLayout(diagsBox.QWidget)
	v.diags = newDiagnosticsTable()
	v.diags.SetMinimumHeight(160)
	diagsLayout.AddWidget(v.diags.QWidget)
	layout.AddWidget2(diagsBox.QWidget, 1)
	v.root = scrollable(content).QWidget

	v.tb = qt6.NewQToolBar2("Host interfaces")
	v.tb.AddAction(mw.network.createBridge)
	v.tb.AddWidget(spacer())
	v.tb.AddAction(mw.refreshAction)
	return v
}

func (v *interfacesView) widget() *qt6.QWidget   { return v.root }
func (v *interfacesView) toolbar() *qt6.QToolBar { return v.tb }
func (v *interfacesView) title() string          { return "Host Interfaces" }

func (v *interfacesView) refresh(s *snapshot) {
	v.network = s.network
	user := s.network.GetUser()
	manage := chipHTML("Diagnostics only", colorNeutral)
	if s.network.GetCanManage() {
		manage = chipHTML("Can modify bridges", colorPositive)
	}
	capability := chipHTML("No CAP_NET_ADMIN", colorInactive)
	if user.GetHasCapNetAdmin() {
		capability = chipHTML("CAP_NET_ADMIN present", colorPositive)
	}
	extra := []string{}
	if user.GetIsRoot() {
		extra = append(extra, chipHTML("root", colorNeutral))
	}
	if user.GetHasCapNetBindService() {
		extra = append(extra, chipHTML("CAP_NET_BIND_SERVICE", colorPositive))
	}
	if user.GetHasCapNetRaw() {
		extra = append(extra, chipHTML("CAP_NET_RAW", colorPositive))
	}
	groups := strings.Join(user.GetGroups(), ", ")
	if groups == "" {
		groups = "none reported"
	}
	v.identity.SetText(fmt.Sprintf("<b>%s</b> %s<br>%s<br>%s", escape(user.GetUsername()), mutedHTML(fmt.Sprintf("UID %d · groups: %s", user.GetUid(), groups)), strings.Join(append([]string{manage, capability}, extra...), " "), mutedHTML("Bridge changes are temporary until confirmed; the backend reverts them after 15 seconds otherwise.")))

	v.interfaces.Clear()
	for _, iface := range s.network.GetInterfaces() {
		state, hex := "Down", colorInactive
		if iface.GetIsUp() {
			state, hex = "Up", colorPositive
		}
		name := iface.GetName()
		if iface.GetIsBridge() {
			name += "  (bridge)"
		}
		item := qt6.NewQTreeWidgetItem2([]string{name, state, firstNonEmpty(iface.GetMaster(), "—"), firstNonEmpty(strings.Join(iface.GetAddresses(), ", "), "—"), fmt.Sprint(iface.GetMtu()), iface.GetHardwareAddress()})
		item.SetIcon(1, dotIcon(hex))
		setBold(item, 0)
		setMono(item, 3)
		setMono(item, 5)
		if iface.GetMaster() == "" {
			setMuted(item, 2)
		}
		if iface.GetCanAttach() {
			item.SetToolTip(0, "Can be attached to a bridge")
		}
		v.interfaces.AddTopLevelItem(item)
	}
	fillDiagnostics(v.diags, s.network.GetDiagnostics())
}

func (v *interfacesView) statusText() string {
	passed, total := 0, len(v.network.GetDiagnostics())
	for _, diagnostic := range v.network.GetDiagnostics() {
		if diagnostic.GetStatus() == apprunnerv1.DiagnosticStatus_DIAGNOSTIC_STATUS_PASS {
			passed++
		}
	}
	return fmt.Sprintf("%s · %s · %d of %d diagnostics passed", pluralize(len(v.network.GetInterfaces()), "interface", "interfaces"), pluralize(len(v.network.GetBridges()), "bridge", "bridges"), passed, total)
}
