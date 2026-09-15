package main

import (
	"bytes"
	"context"
	"encoding/pem"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Bend-Function/MasterDNS-Agent/internal/config"
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

func TestEnrollRequiresConfigAndRejectsTokenArgument(t *testing.T) {
	if err := execute([]string{"enroll"}, &bytes.Buffer{}); err == nil {
		t.Fatal("enroll accepted missing --config")
	}
	if err := execute([]string{"enroll", "--config", "agent.json", "plain-secret"}, &bytes.Buffer{}); err == nil || !strings.Contains(err.Error(), "positional") {
		t.Fatalf("enroll positional token error = %v", err)
	}
}

func TestEnrollExchangesStdinTokenAndPersistsIdentity(t *testing.T) {
	const installToken = "single-use-secret"
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/v1/probe-agent/exchange" || r.Header.Get("Authorization") != "" {
			t.Fatalf("request = %s, Authorization = %q", r.URL.Path, r.Header.Get("Authorization"))
		}
		data, err := io.ReadAll(r.Body)
		if err != nil || !bytes.Contains(data, []byte(installToken)) {
			t.Fatalf("request body = %q, %v", data, err)
		}
		w.Header().Set("Content-Type", "application/json")
		io.WriteString(w, `{"probeId":"11111111-1111-4111-8111-111111111111","runtimeToken":"runtime-secret","protocol":"probe-agent/v1"}`)
	}))
	defer server.Close()
	dir := t.TempDir()
	caPath := filepath.Join(dir, "ca.pem")
	pemData := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: server.Certificate().Raw})
	if err := os.WriteFile(caPath, pemData, 0o600); err != nil {
		t.Fatal(err)
	}
	tokenPath := filepath.Join(dir, "runtime-token")
	configPath := filepath.Join(dir, "agent.json")
	body := `{"serverUrl":"` + server.URL + `","caFile":"` + caPath + `","probeId":"","tokenFile":"` + tokenPath + `","stateDir":"` + filepath.Join(dir, "state") + `","maxConcurrency":1,"allowIpv4":true,"allowIpv6":false,"allowedPrivateCidrs":[]}`
	if err := os.WriteFile(configPath, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	if err := executeWithInput(context.Background(), []string{"enroll", "--config", configPath}, strings.NewReader(installToken+"\n"), &out); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(out.String(), installToken) || strings.Contains(out.String(), "runtime-secret") {
		t.Fatalf("enroll output exposed token: %q", out.String())
	}
	loaded, err := config.Load(configPath)
	if err != nil || loaded.ProbeID != "11111111-1111-4111-8111-111111111111" {
		t.Fatalf("config.Load() = %#v, %v", loaded, err)
	}
}
