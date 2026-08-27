package validate

import (
	"errors"
	"testing"
)

func TestDNSLabelRejectsInvalidLabels(t *testing.T) {
	for _, invalid := range []string{"", "has space", "-leading", "trailing-", "under_score"} {
		if err := DNSLabel(invalid); err == nil {
			t.Errorf("invalid DNS label %q was accepted", invalid)
		}
	}
	for _, valid := range []string{"a", "web-01", "K8S"} {
		if err := DNSLabel(valid); err != nil {
			t.Errorf("valid DNS label %q was rejected: %v", valid, err)
		}
	}
}

func TestInterfaceNameRejectsUnsafeNames(t *testing.T) {
	for _, name := range []string{"", "lo", "bridge with spaces", "this-name-is-far-too-long"} {
		if err := InterfaceName(name); err == nil {
			t.Errorf("expected interface name %q to be rejected", name)
		}
	}
	if err := InterfaceName("br0"); err != nil {
		t.Errorf("br0 was rejected: %v", err)
	}
}

func TestFieldErrorIsMatchable(t *testing.T) {
	err := Field("name", "a name is required")
	var fieldErr *FieldError
	if !errors.As(err, &fieldErr) || fieldErr.Field != "name" || err.Error() != "name: a name is required" {
		t.Fatalf("unexpected field error: %#v", err)
	}
}
