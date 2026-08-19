package main

import "testing"

func TestBridgeConfigurationAllowsNamedBridgeOrAll(t *testing.T) {
	for _, test := range []struct {
		name          string
		configuration string
		allowed       bool
	}{
		{name: "named bridge", configuration: "allow br0\n", allowed: true},
		{name: "all bridges", configuration: "allow all # managed by administrator\n", allowed: true},
		{name: "different bridge", configuration: "allow lab0\n", allowed: false},
		{name: "commented rule", configuration: "# allow br0\n", allowed: false},
		{name: "explicit deny wins", configuration: "allow all\ndeny br0\n", allowed: false},
	} {
		t.Run(test.name, func(t *testing.T) {
			if actual := bridgeConfigurationAllows(test.configuration, "br0"); actual != test.allowed {
				t.Fatalf("expected %v, got %v", test.allowed, actual)
			}
		})
	}
}
