package apprunner

import (
	"errors"
	"net/netip"
	"testing"

	"github.com/google/nftables"
)

func TestDHCPAddressConflicts(t *testing.T) {
	network, err := parseDHCPRange("192.168.100.0/24")
	if err != nil {
		t.Fatal(err)
	}
	tests := []struct {
		name     string
		onBridge bool
		existing string
		want     bool
	}{
		{"unrelated subnet elsewhere", false, "10.0.0.1/8", false},
		{"unrelated subnet on the bridge", true, "10.0.0.1/8", false},
		{"overlapping subnet on another interface", false, "192.168.100.7/24", true},
		{"wider subnet on another interface", false, "192.168.0.1/16", true},
		{"server address on the bridge", true, "192.168.100.1/24", false},
		{"server address with another prefix length", true, "192.168.100.1/25", true},
		{"virtual BMC address below the pool on the bridge", true, "192.168.100.20/24", false},
		{"address inside the pool on the bridge", true, "192.168.100.50/24", true},
		{"pool end on the bridge", true, "192.168.100.254/24", true},
		{"wider subnet on the bridge", true, "192.168.0.1/16", true},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := dhcpAddressConflicts(network, test.onBridge, netip.MustParsePrefix(test.existing)); got != test.want {
				t.Errorf("dhcpAddressConflicts(%s, onBridge=%v) = %v, want %v", test.existing, test.onBridge, got, test.want)
			}
		})
	}
}

func TestHostFirewallDiagnosticDistinguishesFilterTables(t *testing.T) {
	list := func(tables ...*nftables.Table) func() ([]*nftables.Table, error) {
		return func() ([]*nftables.Table, error) { return tables, nil }
	}
	if got := hostFirewallDiagnostic(false, nil); got.Status != diagnosticInfo {
		t.Fatalf("without CAP_NET_ADMIN = %#v", got)
	}
	if got := hostFirewallDiagnostic(true, func() ([]*nftables.Table, error) { return nil, errors.New("denied") }); got.Status != diagnosticInfo {
		t.Fatalf("listing failure = %#v", got)
	}
	if got := hostFirewallDiagnostic(true, list(&nftables.Table{Family: nftables.TableFamilyIPv4, Name: "filter"})); got.Status != diagnosticPass {
		t.Fatalf("iptables-nft filter = %#v", got)
	}
	if got := hostFirewallDiagnostic(true, list(&nftables.Table{Family: nftables.TableFamilyIPv4, Name: appRunnerNATTable})); got.Status != diagnosticPass {
		t.Fatalf("only App Runner's own table = %#v", got)
	}
	got := hostFirewallDiagnostic(true, list(&nftables.Table{Family: nftables.TableFamilyINet, Name: "firewalld"}))
	if got.Status != diagnosticWarning || got.Remediation == "" {
		t.Fatalf("native nftables firewall = %#v", got)
	}
}
