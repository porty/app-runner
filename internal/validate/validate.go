// Package validate holds input validation shared across App Runner's
// subsystems and the field-level error type the API maps to invalid
// argument responses.
package validate

import (
	"errors"
	"fmt"
	"regexp"
)

// FieldError reports that one named request field is invalid.
type FieldError struct {
	Field   string
	Message string
}

func (e *FieldError) Error() string {
	return fmt.Sprintf("%s: %s", e.Field, e.Message)
}

// Field builds a FieldError.
func Field(field, message string) error {
	return &FieldError{Field: field, Message: message}
}

var interfaceNamePattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_.-]{0,14}$`)

// InterfaceName checks that a name can be used for a Linux network
// interface that App Runner manages.
func InterfaceName(name string) error {
	if !interfaceNamePattern.MatchString(name) {
		return errors.New("must be 1-15 characters using letters, numbers, dot, underscore, or hyphen")
	}
	if name == "lo" {
		return errors.New("loopback cannot be used as a bridge or bridge member")
	}
	return nil
}

// DNSLabel checks that a value is a valid DNS hostname label, which VM
// names must be so Auto DNS can publish them.
func DNSLabel(label string) error {
	if len(label) == 0 || len(label) > 63 {
		return errors.New("must contain between 1 and 63 characters")
	}
	if label[0] == '-' || label[len(label)-1] == '-' {
		return errors.New("must not begin or end with a hyphen")
	}
	for _, character := range label {
		if (character >= 'a' && character <= 'z') || (character >= 'A' && character <= 'Z') ||
			(character >= '0' && character <= '9') || character == '-' {
			continue
		}
		return errors.New("must use only letters, numbers, and interior hyphens")
	}
	return nil
}
