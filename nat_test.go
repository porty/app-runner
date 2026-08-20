package main

import (
	"errors"
	"net/netip"
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/google/nftables/expr"
)

type fakeNATBackend struct {
	forwarding     bool
	ipForwardCalls int
	setCalls       []bool
	ruleCalls      [][]natNetwork
	rulesErr       error
	disableErr     error
}

func (b *fakeNATBackend) IPForwarding() (bool, error) {
	b.ipForwardCalls++
	return b.forwarding, nil
}

func (b *fakeNATBackend) SetIPForwarding(enabled bool) error {
	if !enabled && b.disableErr != nil {
		return b.disableErr
	}
	b.forwarding = enabled
	b.setCalls = append(b.setCalls, enabled)
	return nil
}

func (b *fakeNATBackend) ReplaceRules(networks []natNetwork) error {
	copyOfNetworks := append([]natNetwork(nil), networks...)
	b.ruleCalls = append(b.ruleCalls, copyOfNetworks)
	return b.rulesErr
}

func TestNATManagerSharesForwardingAcrossBridgesAndRestoresIt(t *testing.T) {
	path := filepath.Join(t.TempDir(), "nat-runtime.json")
	backend := &fakeNATBackend{}
	manager, err := newNATManagerWithBackend(path, backend)
	if err != nil {
		t.Fatal(err)
	}
	first := netip.MustParsePrefix("192.168.100.0/24")
	second := netip.MustParsePrefix("192.168.101.0/24")
	if err := manager.Start("br0", first); err != nil {
		t.Fatal(err)
	}
	if err := manager.Start("br1", second); err != nil {
		t.Fatal(err)
	}
	if !backend.forwarding || !reflect.DeepEqual(backend.setCalls, []bool{true}) {
		t.Fatalf("IPv4 forwarding was not enabled once: forwarding=%v calls=%v", backend.forwarding, backend.setCalls)
	}
	if got := backend.ruleCalls[len(backend.ruleCalls)-1]; len(got) != 2 || got[0].Bridge != "br0" || got[1].Bridge != "br1" {
		t.Fatalf("unexpected active NAT rules: %#v", got)
	}
	if err := manager.Stop("br0"); err != nil {
		t.Fatal(err)
	}
	if !backend.forwarding || manager.Running("br0") || !manager.Running("br1") {
		t.Fatal("stopping the first bridge affected the remaining NAT network")
	}
	if err := manager.Stop("br1"); err != nil {
		t.Fatal(err)
	}
	if backend.forwarding || !reflect.DeepEqual(backend.setCalls, []bool{true, false}) {
		t.Fatalf("original forwarding state was not restored: forwarding=%v calls=%v", backend.forwarding, backend.setCalls)
	}
	if _, err := os.Stat(path); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("runtime recovery state remained after clean shutdown: %v", err)
	}
}

func TestNATManagerDoesNotDisablePreexistingForwarding(t *testing.T) {
	backend := &fakeNATBackend{forwarding: true}
	manager, err := newNATManagerWithBackend(filepath.Join(t.TempDir(), "nat-runtime.json"), backend)
	if err != nil {
		t.Fatal(err)
	}
	if err := manager.Start("br0", netip.MustParsePrefix("10.20.0.0/24")); err != nil {
		t.Fatal(err)
	}
	if err := manager.Stop("br0"); err != nil {
		t.Fatal(err)
	}
	if !backend.forwarding || len(backend.setCalls) != 0 {
		t.Fatalf("preexisting forwarding was modified: forwarding=%v calls=%v", backend.forwarding, backend.setCalls)
	}
}

func TestNATManagerRecoversAbandonedHostChanges(t *testing.T) {
	path := filepath.Join(t.TempDir(), "nat-runtime.json")
	backend := &fakeNATBackend{}
	first, err := newNATManagerWithBackend(path, backend)
	if err != nil {
		t.Fatal(err)
	}
	if err := first.Start("br0", netip.MustParsePrefix("192.168.100.0/24")); err != nil {
		t.Fatal(err)
	}
	if !backend.forwarding {
		t.Fatal("test setup did not enable forwarding")
	}

	restarted, err := newNATManagerWithBackend(path, backend)
	if err != nil {
		t.Fatal(err)
	}
	if err := restarted.FinishRecovery(); err != nil {
		t.Fatal(err)
	}
	if backend.forwarding {
		t.Fatal("abandoned IPv4 forwarding change was not restored")
	}
	if got := backend.ruleCalls[len(backend.ruleCalls)-1]; len(got) != 0 {
		t.Fatalf("abandoned nftables rules were not removed: %#v", got)
	}
}

func TestNATManagerRetainsOriginalStateWhenRestoreMustBeRetried(t *testing.T) {
	backend := &fakeNATBackend{}
	manager, err := newNATManagerWithBackend(filepath.Join(t.TempDir(), "nat-runtime.json"), backend)
	if err != nil {
		t.Fatal(err)
	}
	prefix := netip.MustParsePrefix("192.168.100.0/24")
	if err := manager.Start("br0", prefix); err != nil {
		t.Fatal(err)
	}
	backend.disableErr = errors.New("temporarily denied")
	if err := manager.Stop("br0"); err == nil {
		t.Fatal("forwarding restore failure was not reported")
	}
	backend.disableErr = nil
	if err := manager.Start("br1", prefix); err != nil {
		t.Fatal(err)
	}
	if backend.ipForwardCalls != 1 {
		t.Fatalf("retry recaptured the temporary forwarding value: reads=%d", backend.ipForwardCalls)
	}
	if err := manager.Stop("br1"); err != nil {
		t.Fatal(err)
	}
	if backend.forwarding {
		t.Fatal("retry did not eventually restore the original forwarding value")
	}
}

func TestNATRuleExpressionsConstrainBridgeSubnetAndMasquerade(t *testing.T) {
	network := natNetwork{Bridge: "br0", Prefix: netip.MustParsePrefix("192.168.100.0/24")}
	outbound := outboundForwardExpressions(network)
	if len(outbound) != 6 {
		t.Fatalf("unexpected outbound expression count: %d", len(outbound))
	}
	if verdict, ok := outbound[len(outbound)-1].(*expr.Verdict); !ok || verdict.Kind != expr.VerdictAccept {
		t.Fatalf("outbound rule does not end in accept: %#v", outbound)
	}
	masquerade := masqueradeExpressions(network)
	if _, ok := masquerade[len(masquerade)-1].(*expr.Masq); !ok {
		t.Fatalf("postrouting rule does not end in masquerade: %#v", masquerade)
	}
	comparison, ok := masquerade[len(masquerade)-2].(*expr.Cmp)
	if !ok || comparison.Op != expr.CmpOpNeq {
		t.Fatalf("postrouting rule does not exclude the source bridge: %#v", masquerade)
	}
}

func TestFirewallServiceRulesConstrainBridgeProtocolAndPort(t *testing.T) {
	network := natNetwork{Bridge: "br0", Prefix: netip.MustParsePrefix("192.168.100.0/24")}
	for _, test := range []struct {
		protocol byte
		port     uint16
	}{
		{protocol: 17, port: 67},
		{protocol: 17, port: 53},
		{protocol: 6, port: 53},
	} {
		expressions := serviceInputExpressions(network, test.protocol, test.port)
		if len(expressions) != 7 {
			t.Fatalf("unexpected service rule expression count: %d", len(expressions))
		}
		if verdict, ok := expressions[len(expressions)-1].(*expr.Verdict); !ok || verdict.Kind != expr.VerdictAccept {
			t.Fatalf("service rule does not end in accept: %#v", expressions)
		}
	}
}
