package main

import (
	"bytes"
	"strings"
	"testing"
)

func TestVersionCommand(t *testing.T) {
	var out bytes.Buffer
	if err := execute([]string{"version"}, &out); err != nil {
		t.Fatalf("execute() error = %v", err)
	}
	if got := strings.TrimSpace(out.String()); got != version {
		t.Fatalf("version output = %q, want %q", got, version)
	}
}

func TestRunRequiresConfig(t *testing.T) {
	if err := execute([]string{"run"}, &bytes.Buffer{}); err == nil {
		t.Fatal("run accepted missing --config")
	}
}

func TestRunLoadsConfigBeforeAgentStartup(t *testing.T) {
	err := execute([]string{"run", "--config", "does-not-exist.json"}, &bytes.Buffer{})
	if err == nil || !strings.Contains(err.Error(), "does-not-exist.json") {
		t.Fatalf("execute() error = %v", err)
	}
}
