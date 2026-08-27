package hostnet

import (
	"errors"
	"testing"

	"github.com/google/nftables"

	"github.com/roflware/app-runner/internal/nat"
)

func TestHostFirewallDiagnosticDistinguishesFilterTables(t *testing.T) {
	list := func(tables ...*nftables.Table) func() ([]*nftables.Table, error) {
		return func() ([]*nftables.Table, error) { return tables, nil }
	}
	if got := hostFirewallDiagnostic(false, nil); got.Status != DiagnosticInfo {
		t.Fatalf("without CAP_NET_ADMIN = %#v", got)
	}
	if got := hostFirewallDiagnostic(true, func() ([]*nftables.Table, error) { return nil, errors.New("denied") }); got.Status != DiagnosticInfo {
		t.Fatalf("listing failure = %#v", got)
	}
	if got := hostFirewallDiagnostic(true, list(&nftables.Table{Family: nftables.TableFamilyIPv4, Name: "filter"})); got.Status != DiagnosticPass {
		t.Fatalf("iptables-nft filter = %#v", got)
	}
	if got := hostFirewallDiagnostic(true, list(&nftables.Table{Family: nftables.TableFamilyIPv4, Name: nat.TableName})); got.Status != DiagnosticPass {
		t.Fatalf("only App Runner's own table = %#v", got)
	}
	got := hostFirewallDiagnostic(true, list(&nftables.Table{Family: nftables.TableFamilyINet, Name: "firewalld"}))
	if got.Status != DiagnosticWarning || got.Remediation == "" {
		t.Fatalf("native nftables firewall = %#v", got)
	}
}
