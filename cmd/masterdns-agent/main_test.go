package main

import (
	"bytes"
	"context"
	"encoding/json"
	"encoding/pem"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync/atomic"
	"syscall"
	"testing"
	"time"

	"github.com/Bend-Function/MasterDNS-Agent/internal/client"
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

func TestRunActuallyHeartbeatsAndReturnsUnauthorized(t *testing.T) {
	var called atomic.Bool
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/v1/probe-agent/heartbeat" {
			t.Errorf("first request = %s", r.URL.Path)
		}
		called.Store(true)
		w.WriteHeader(http.StatusUnauthorized)
	}))
	defer server.Close()
	dir := t.TempDir()
	ca := filepath.Join(dir, "ca.pem")
	token := filepath.Join(dir, "token")
	path := filepath.Join(dir, "agent.json")
	if err := os.WriteFile(ca, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: server.Certificate().Raw}), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(token, []byte("runtime-secret\n"), 0600); err != nil {
		t.Fatal(err)
	}
	cfg := config.Config{ServerURL: server.URL, CAFile: ca, ProbeID: "11111111-1111-4111-8111-111111111111", TokenFile: token, StateDir: filepath.Join(dir, "state"), MaxConcurrency: 8, AllowIPv4: true}
	data, _ := json.Marshal(cfg)
	if err := os.WriteFile(path, data, 0600); err != nil {
		t.Fatal(err)
	}
	err := executeWithInput(context.Background(), []string{"run", "--config", path}, strings.NewReader(""), io.Discard)
	if !errors.Is(err, client.ErrUnauthorized) || !called.Load() {
		t.Fatalf("run = %v, heartbeat = %v", err, called.Load())
	}
	if exitCode(err) != 3 || exitCode(nil) != 0 || exitCode(errors.New("failure")) != 1 {
		t.Fatal("incorrect run exit codes")
	}
}

func TestRunStopsOnSIGTERM(t *testing.T) {
	if os.Getenv("MASTERDNS_TEST_SIGNAL_CHILD") == "1" {
		err := execute([]string{"run", "--config", os.Getenv("MASTERDNS_TEST_CONFIG")}, io.Discard)
		os.Exit(exitCode(err))
	}
	if runtime.GOOS == "windows" {
		t.Skip("SIGTERM is a Unix process signal")
	}
	heartbeat := make(chan struct{}, 1)
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/api/v1/probe-agent/heartbeat":
			select {
			case heartbeat <- struct{}{}:
			default:
			}
			io.WriteString(w, `{}`)
		case "/api/v1/probe-agent/tasks/lease":
			json.NewEncoder(w).Encode(map[string]any{"serverTime": time.Now(), "tasks": []any{}, "retryAfterMs": 2000})
		default:
			w.WriteHeader(404)
		}
	}))
	defer server.Close()
	dir := t.TempDir()
	ca := filepath.Join(dir, "ca.pem")
	token := filepath.Join(dir, "token")
	path := filepath.Join(dir, "agent.json")
	if err := os.WriteFile(ca, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: server.Certificate().Raw}), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(token, []byte("runtime-secret"), 0600); err != nil {
		t.Fatal(err)
	}
	cfg := config.Config{ServerURL: server.URL, CAFile: ca, ProbeID: "11111111-1111-4111-8111-111111111111", TokenFile: token, StateDir: filepath.Join(dir, "state"), MaxConcurrency: 8, AllowIPv4: true}
	data, _ := json.Marshal(cfg)
	if err := os.WriteFile(path, data, 0600); err != nil {
		t.Fatal(err)
	}
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	command := exec.Command(executable, "-test.run=^TestRunStopsOnSIGTERM$")
	command.Env = append(os.Environ(), "MASTERDNS_TEST_SIGNAL_CHILD=1", "MASTERDNS_TEST_CONFIG="+path)
	if err := command.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = command.Process.Kill() })
	done := make(chan error, 1)
	go func() { done <- command.Wait() }()
	select {
	case <-heartbeat:
	case err := <-done:
		t.Fatalf("run exited before heartbeat: %v", err)
	case <-time.After(5 * time.Second):
		t.Fatal("run did not heartbeat")
	}
	if err := command.Process.Signal(syscall.SIGTERM); err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("SIGTERM exit = %v", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("SIGTERM did not stop the run command")
	}
}
