package client

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
	"path/filepath"
	"runtime"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Bend-Function/MasterDNS-Agent/internal/config"
	"github.com/Bend-Function/MasterDNS-Agent/internal/protocol"
)

const testToken = "test-secret"

func newTestClient(serverURL, token string) *Client {
	c, err := newClient(serverURL, token, &http.Client{Timeout: time.Second})
	if err != nil {
		panic(err)
	}
	return c
}

func TestNewRequiresHTTPS(t *testing.T) {
	if _, err := New("http://example.com", testToken); err == nil {
		t.Fatal("New accepted plaintext HTTP")
	}
}

func TestNewTrustsConfiguredPrivateCA(t *testing.T) {
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		io.WriteString(w, `{"serverTime":"2026-09-15T00:00:00Z","tasks":[],"retryAfterMs":0}`)
	}))
	defer server.Close()
	certificate := server.Certificate()
	if certificate == nil {
		t.Fatal("test server has no certificate")
	}
	caPath := filepath.Join(t.TempDir(), "ca.pem")
	pemData := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: certificate.Raw})
	if err := os.WriteFile(caPath, pemData, 0o600); err != nil {
		t.Fatal(err)
	}
	client, err := New(server.URL, testToken, WithCAFile(caPath))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := client.Lease(context.Background(), 1); err != nil {
		t.Fatal(err)
	}
}

func TestExchangeUsesExactPathWithoutBearer(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/v1/probe-agent/exchange" || r.Method != http.MethodPost {
			t.Fatalf("request = %s %s", r.Method, r.URL.Path)
		}
		if got := r.Header.Get("Authorization"); got != "" {
			t.Fatalf("Authorization = %q", got)
		}
		var body map[string]string
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Fatal(err)
		}
		if got := body["installToken"]; got != testToken {
			t.Fatalf("installToken = %q", got)
		}
		w.Header().Set("Content-Type", "application/json")
		io.WriteString(w, `{"probeId":"11111111-1111-4111-8111-111111111111","runtimeToken":"runtime-secret","protocol":"probe-agent/v1"}`)
	}))
	defer server.Close()

	got, err := newTestClient(server.URL, "").Exchange(context.Background(), testToken)
	if err != nil {
		t.Fatal(err)
	}
	if got.ProbeID != "11111111-1111-4111-8111-111111111111" || got.RuntimeToken != "runtime-secret" || got.Protocol != protocol.Version {
		t.Fatalf("Exchange() = %#v", got)
	}
}

func TestAuthenticatedRequestsUseBearerAndExactPayloads(t *testing.T) {
	var paths []string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got := r.Header.Get("Authorization"); got != "Bearer "+testToken {
			t.Fatalf("Authorization = %q", got)
		}
		paths = append(paths, r.URL.Path)
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/api/v1/probe-agent/heartbeat":
			var got protocol.HeartbeatRequest
			json.NewDecoder(r.Body).Decode(&got)
			if got.Protocol != protocol.Version || got.AgentVersion != "1.2.3" || got.MaxConcurrency != 4 || !got.Capabilities.IPv4 || got.Capabilities.IPv6 {
				t.Fatalf("heartbeat = %#v", got)
			}
			io.WriteString(w, `{}`)
		case "/api/v1/probe-agent/tasks/lease":
			var got protocol.LeaseRequest
			json.NewDecoder(r.Body).Decode(&got)
			if got.Protocol != protocol.Version || got.Capacity != 4 {
				t.Fatalf("lease = %#v", got)
			}
			io.WriteString(w, `{"serverTime":"2026-09-15T00:00:00Z","tasks":[],"retryAfterMs":250}`)
		default:
			t.Fatalf("unexpected path %q", r.URL.Path)
		}
	}))
	defer server.Close()
	c := newTestClient(server.URL, testToken)

	if err := c.Heartbeat(context.Background(), Capabilities{AgentVersion: "1.2.3", IPv4: true, MaxConcurrency: 4}); err != nil {
		t.Fatal(err)
	}
	lease, err := c.Lease(context.Background(), 4)
	if err != nil {
		t.Fatal(err)
	}
	if lease.RetryAfterMS != 250 || len(paths) != 2 {
		t.Fatalf("lease = %#v, paths = %v", lease, paths)
	}
}

func TestSubmitPreservesTaskAndLeaseIdentity(t *testing.T) {
	result := protocol.Result{Protocol: protocol.Version, TaskID: "11111111-1111-4111-8111-111111111111", LeaseID: "22222222-2222-4222-8222-222222222222", AddressVersion: 3, ConfigVersion: 7, Outcome: protocol.OutcomeSuccess, MeasuredAt: time.Date(2026, 9, 15, 0, 0, 0, 0, time.UTC)}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var got protocol.SubmitRequest
		if err := json.NewDecoder(r.Body).Decode(&got); err != nil {
			t.Fatal(err)
		}
		if len(got.Results) != 1 || got.Results[0].TaskID != result.TaskID || got.Results[0].LeaseID != result.LeaseID {
			t.Fatalf("results = %#v", got.Results)
		}
		w.Header().Set("Content-Type", "application/json")
		io.WriteString(w, `{"results":[{"taskId":"11111111-1111-4111-8111-111111111111","status":"duplicate"}]}`)
	}))
	defer server.Close()

	acks, err := newTestClient(server.URL, testToken).Submit(context.Background(), []protocol.Result{result})
	if err != nil || len(acks) != 1 || acks[0].Status != protocol.AckDuplicate {
		t.Fatalf("Submit() = %#v, %v", acks, err)
	}
}

func TestUnauthorizedIsTerminal(t *testing.T) {
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		w.WriteHeader(http.StatusUnauthorized)
	}))
	defer server.Close()

	_, err := newTestClient(server.URL, testToken).Lease(context.Background(), 1)
	if !errors.Is(err, ErrUnauthorized) || calls.Load() != 1 {
		t.Fatalf("error = %v, calls = %d", err, calls.Load())
	}
}

func TestRateLimitExposesRetryAfterWithoutRetrying(t *testing.T) {
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		w.Header().Set("Retry-After", "3")
		w.WriteHeader(http.StatusTooManyRequests)
	}))
	defer server.Close()

	_, err := newTestClient(server.URL, testToken).Lease(context.Background(), 1)
	var retry *RetryError
	if !errors.As(err, &retry) || retry.After != 3*time.Second || calls.Load() != 1 {
		t.Fatalf("error = %#v, calls = %d", err, calls.Load())
	}
}

func TestAuthenticatedRedirectIsRejectedBeforeCredentialsCrossOrigin(t *testing.T) {
	var redirected atomic.Int32
	destination := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		redirected.Add(1)
	}))
	defer destination.Close()
	source := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, destination.URL, http.StatusTemporaryRedirect)
	}))
	defer source.Close()

	_, err := newTestClient(source.URL, testToken).Lease(context.Background(), 1)
	if err == nil || redirected.Load() != 0 || strings.Contains(err.Error(), testToken) {
		t.Fatalf("error = %v, redirected calls = %d", err, redirected.Load())
	}
}

func TestRejectsNonJSONAndOversizedResponses(t *testing.T) {
	for name, handler := range map[string]http.HandlerFunc{
		"non-json": func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "text/plain")
			io.WriteString(w, `{}`)
		},
		"oversized": func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			io.WriteString(w, `{"padding":"`+strings.Repeat("x", maxResponseBytes)+`"}`)
		},
	} {
		t.Run(name, func(t *testing.T) {
			server := httptest.NewServer(handler)
			defer server.Close()
			_, err := newTestClient(server.URL, testToken).Lease(context.Background(), 1)
			if err == nil || strings.Contains(err.Error(), testToken) {
				t.Fatalf("Lease() error = %v", err)
			}
		})
	}
}

func TestHeartbeatRejectsOversizedResponse(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		io.WriteString(w, `{"padding":"`+strings.Repeat("x", maxResponseBytes)+`"}`)
	}))
	defer server.Close()
	err := newTestClient(server.URL, testToken).Heartbeat(context.Background(), Capabilities{AgentVersion: "1", IPv4: true, MaxConcurrency: 1})
	if err == nil {
		t.Fatal("Heartbeat accepted an oversized response")
	}
}

func TestSubmitRejectsAcknowledgementForDifferentTask(t *testing.T) {
	result := protocol.Result{Protocol: protocol.Version, TaskID: "11111111-1111-4111-8111-111111111111"}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		io.WriteString(w, `{"results":[{"taskId":"22222222-2222-4222-8222-222222222222","status":"accepted"}]}`)
	}))
	defer server.Close()
	if _, err := newTestClient(server.URL, testToken).Submit(context.Background(), []protocol.Result{result}); err == nil {
		t.Fatal("Submit accepted acknowledgement for a different task")
	}
}

func TestSubmitRejectsBareAcknowledgementArray(t *testing.T) {
	result := protocol.Result{Protocol: protocol.Version, TaskID: "11111111-1111-4111-8111-111111111111"}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		io.WriteString(w, `[{"taskId":"11111111-1111-4111-8111-111111111111","status":"accepted"}]`)
	}))
	defer server.Close()
	if _, err := newTestClient(server.URL, testToken).Submit(context.Background(), []protocol.Result{result}); err == nil {
		t.Fatal("Submit accepted a bare acknowledgement array")
	}
}

func TestLeaseRejectsExcessiveRetryAfter(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		io.WriteString(w, `{"serverTime":"2026-09-15T00:00:00Z","tasks":[],"retryAfterMs":3600001}`)
	}))
	defer server.Close()
	if _, err := newTestClient(server.URL, testToken).Lease(context.Background(), 1); err == nil {
		t.Fatal("Lease accepted retryAfterMs above one hour")
	}
}

func TestLeaseBudgetUsesServerTimeAndElapsedMonotonicTime(t *testing.T) {
	lease := protocol.LeaseResponse{ServerTime: time.Date(2026, 9, 15, 0, 0, 0, 0, time.UTC)}
	received := time.Now()
	lease.SetReceivedAt(received)
	task := protocol.Task{Deadline: lease.ServerTime.Add(10 * time.Second)}
	remaining := lease.Remaining(task, received.Add(3*time.Second))
	if remaining != 7*time.Second {
		t.Fatalf("Remaining() = %v", remaining)
	}
}

func TestPlatformErrorDoesNotExposeTokenOrBody(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
		io.WriteString(w, "rejected "+testToken)
	}))
	defer server.Close()

	_, err := newTestClient(server.URL, "").Exchange(context.Background(), testToken)
	if err == nil || strings.Contains(err.Error(), testToken) {
		t.Fatalf("Exchange() error = %v", err)
	}
}

func TestExchangeRejectsInvalidProbeIdentity(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		io.WriteString(w, `{"probeId":"not-a-uuid","runtimeToken":"runtime-secret","protocol":"probe-agent/v1"}`)
	}))
	defer server.Close()
	if _, err := newTestClient(server.URL, "").Exchange(context.Background(), testToken); err == nil {
		t.Fatal("Exchange accepted invalid probe identity")
	}
}

func TestReadInstallTokenFromStdinOrProtectedFile(t *testing.T) {
	got, err := ReadInstallToken("", strings.NewReader("stdin-secret\n"))
	if err != nil || got != "stdin-secret" {
		t.Fatalf("ReadInstallToken(stdin) = %q, %v", got, err)
	}

	path := filepath.Join(t.TempDir(), "install-token")
	if err := os.WriteFile(path, []byte("file-secret\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	got, err = ReadInstallToken(path, strings.NewReader("ignored"))
	if err != nil || got != "file-secret" {
		t.Fatalf("ReadInstallToken(file) = %q, %v", got, err)
	}
	if runtime.GOOS == "windows" {
		return
	}
	if err := os.Chmod(path, 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := ReadInstallToken(path, strings.NewReader("ignored")); err == nil {
		t.Fatal("ReadInstallToken accepted a group/world-readable token file")
	}
}

func TestPersistEnrollmentAtomicallyCreatesValidRuntimeConfig(t *testing.T) {
	dir := t.TempDir()
	tokenPath := filepath.Join(dir, "runtime-token")
	configPath := filepath.Join(dir, "agent.json")
	cfg := config.Config{
		ServerURL: "https://platform.example", TokenFile: tokenPath, StateDir: filepath.Join(dir, "state"),
		MaxConcurrency: 4, AllowIPv4: true,
	}
	configJSON := `{"serverUrl":"https://platform.example","probeId":"","tokenFile":"` + tokenPath + `","stateDir":"` + cfg.StateDir + `","maxConcurrency":4,"allowIpv4":true,"allowIpv6":false,"allowedPrivateCidrs":[]}`
	if err := os.WriteFile(configPath, configFixtureJSON(configJSON), 0o600); err != nil {
		t.Fatal(err)
	}
	enrollment := protocol.Enrollment{ProbeID: "11111111-1111-4111-8111-111111111111", RuntimeToken: "runtime-secret", Protocol: protocol.Version}
	if err := PersistEnrollment(configPath, cfg, enrollment); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(tokenPath)
	if err != nil || strings.TrimSpace(string(data)) != "runtime-secret" {
		t.Fatalf("runtime token = %q, %v", data, err)
	}
	info, err := os.Stat(tokenPath)
	if runtime.GOOS != "windows" && (err != nil || info.Mode().Perm() != 0o600) {
		t.Fatalf("runtime token mode = %v, %v", info.Mode().Perm(), err)
	}
	loaded, err := config.Load(configPath)
	if err != nil || loaded.ProbeID != enrollment.ProbeID {
		t.Fatalf("config.Load() = %#v, %v", loaded, err)
	}
}

func TestPersistEnrollmentRejectsInvalidProbeID(t *testing.T) {
	cfg := config.Config{TokenFile: filepath.Join(t.TempDir(), "token")}
	err := PersistEnrollment(filepath.Join(t.TempDir(), "config.json"), cfg, protocol.Enrollment{ProbeID: "not-a-uuid", RuntimeToken: "secret", Protocol: protocol.Version})
	if err == nil {
		t.Fatal("PersistEnrollment accepted an invalid probe ID")
	}
}

func TestEnrollmentCanRecoverAfterConfigWriteFailure(t *testing.T) {
	dir := t.TempDir()
	tokenPath := filepath.Join(dir, "runtime-token")
	configPath := filepath.Join(dir, "agent.json")
	oldToken := []byte("old-runtime-secret\n")
	oldConfig := []byte(`{"serverUrl":"https://platform.example","probeId":"22222222-2222-4222-8222-222222222222","tokenFile":"` + tokenPath + `","stateDir":"` + filepath.Join(dir, "state") + `","maxConcurrency":1,"allowIpv4":true,"allowIpv6":false,"allowedPrivateCidrs":[]}`)
	if err := os.WriteFile(tokenPath, oldToken, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(configPath, configFixtureJSON(string(oldConfig)), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, err := config.Load(configPath)
	if err != nil {
		t.Fatal(err)
	}
	enrollment := protocol.Enrollment{ProbeID: "11111111-1111-4111-8111-111111111111", RuntimeToken: "new-runtime-secret", Protocol: protocol.Version}
	err = persistEnrollment(configPath, cfg, enrollment, func(path string, data []byte, mode os.FileMode) error {
		if path == configPath {
			return errors.New("injected config failure")
		}
		return atomicWrite(path, data, mode)
	})
	if err == nil {
		t.Fatal("PersistEnrollment succeeded despite injected config failure")
	}
	if got, _ := os.ReadFile(tokenPath); !bytes.Equal(got, oldToken) {
		t.Fatalf("old runtime token changed: %q", got)
	}
	if got, _ := os.ReadFile(configPath); !bytes.Equal(got, configFixtureJSON(string(oldConfig))) {
		t.Fatalf("old config changed: %q", got)
	}
	pendingInfo, err := os.Stat(tokenPath + ".pending")
	if err != nil {
		t.Fatalf("recoverable pending enrollment missing: %v", err)
	}
	if runtime.GOOS != "windows" && pendingInfo.Mode().Perm() != 0o600 {
		t.Fatalf("pending enrollment mode = %v", pendingInfo.Mode().Perm())
	}
	recovered, err := RecoverEnrollment(configPath, cfg)
	if err != nil || !recovered {
		t.Fatalf("RecoverEnrollment() = %v, %v", recovered, err)
	}
	loaded, err := config.Load(configPath)
	if err != nil || loaded.ProbeID != enrollment.ProbeID {
		t.Fatalf("recovered config = %#v, %v", loaded, err)
	}
	if got, _ := os.ReadFile(tokenPath); strings.TrimSpace(string(got)) != enrollment.RuntimeToken {
		t.Fatalf("recovered runtime token = %q", got)
	}
}

func configFixtureJSON(body string) []byte {
	if runtime.GOOS == "windows" {
		body = strings.ReplaceAll(body, `\`, `\\`)
	}
	return []byte(body)
}
