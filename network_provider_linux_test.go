package apprunner

import (
	"net/netip"
	"testing"
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
