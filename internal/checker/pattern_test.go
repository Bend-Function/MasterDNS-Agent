package checker

import "testing"

func TestRejectUnsupportedRegex(t *testing.T) {
	if err := ValidatePattern("(?<=token)ok"); err == nil {
		t.Fatal("accepted lookbehind")
	}
}

func TestAcceptRE2Regex(t *testing.T) {
	if err := ValidatePattern(`^ready=(true|1)$`); err != nil {
		t.Fatalf("rejected RE2 pattern: %v", err)
	}
}

func TestRejectBackreference(t *testing.T) {
	if err := ValidatePattern(`(ok)\1`); err == nil {
		t.Fatal("accepted backreference")
	}
}
