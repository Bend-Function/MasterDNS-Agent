package integration

import (
	"context"
	"encoding/json"
	"encoding/pem"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Bend-Function/MasterDNS-Agent/internal/checker"
	"github.com/Bend-Function/MasterDNS-Agent/internal/client"
	"github.com/Bend-Function/MasterDNS-Agent/internal/config"
	"github.com/Bend-Function/MasterDNS-Agent/internal/protocol"
	"github.com/Bend-Function/MasterDNS-Agent/internal/runner"
	"github.com/Bend-Function/MasterDNS-Agent/internal/spool"
)

// This exercises the actual agent stack against a protocol server. Cross-repo
// platform round aggregation is deliberately left to the P12 environment.
func TestEnrollmentLeaseAndSubmit(t *testing.T) {
	for _, kind := range []string{"tcp", "https"} {
		t.Run(kind, func(t *testing.T) { runProtocolIntegration(t, kind) })
	}
}

func runProtocolIntegration(t *testing.T, kind string) {
	const id = "11111111-1111-4111-8111-111111111111"
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	var leases, dials, uploads atomic.Int32
	var targetRequests atomic.Int32
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		targetRequests.Add(1)
		if r.Host != "check.example" || r.URL.Path != "/health" || r.TLS == nil || r.TLS.ServerName != "check.example" {
			t.Errorf("unexpected HTTPS request: host=%s path=%s TLS=%v", r.Host, r.URL.Path, r.TLS)
		}
		w.WriteHeader(http.StatusOK)
	})
	var target *httptest.Server
	if kind == "https" {
		target = httptest.NewTLSServer(handler)
	} else {
		target = httptest.NewServer(handler)
	}
	defer target.Close()
	checkConfig := protocol.CheckConfig{Type: "tcp", Port: 443, TimeoutMS: 1000}
	if kind == "https" {
		// Only the isolated target opts out of certificate verification. The
		// platform connection below always verifies its explicit private CA.
		checkConfig = protocol.CheckConfig{Type: "http", Protocol: "https", Port: 443, Hostname: "check.example", Path: "/health", Method: "GET", ExpectedStatusMin: 200, ExpectedStatusMax: 200, VerifyTLS: false, TimeoutMS: 1000}
	}
	submitted := make(chan protocol.Result, 1)
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.URL.Path != "/api/v1/probe-agent/exchange" && r.Header.Get("Authorization") != "Bearer test-runtime" {
			t.Error("missing runtime authorization")
			w.WriteHeader(401)
			return
		}
		switch r.URL.Path {
		case "/api/v1/probe-agent/exchange":
			json.NewEncoder(w).Encode(protocol.Enrollment{ProbeID: id, RuntimeToken: "test-runtime", Protocol: protocol.Version})
		case "/api/v1/probe-agent/heartbeat":
			json.NewEncoder(w).Encode(struct{}{})
		case "/api/v1/probe-agent/tasks/lease":
			response := protocol.LeaseResponse{ServerTime: time.Now().Add(-time.Hour), RetryAfterMS: 2000}
			if leases.Add(1) == 1 {
				response.Tasks = []protocol.Task{{Protocol: protocol.Version, TaskID: id, RoundID: id, ProbeID: id, LeaseID: "22222222-2222-4222-8222-222222222222", AddressVersion: 1, ConfigVersion: 1, Address: "8.8.8.8", Family: 4, Config: checkConfig, Deadline: response.ServerTime.Add(10 * time.Second)}}
			}
			if kind == "https" && len(response.Tasks) > 0 {
				// bool omitempty in the DTO would omit false; the wire server must send
				// an explicit override because HTTP config decoding defaults TLS to true.
				payload, _ := json.Marshal(response)
				var wire map[string]any
				if err := json.Unmarshal(payload, &wire); err != nil {
					t.Error(err)
					return
				}
				wire["tasks"].([]any)[0].(map[string]any)["config"].(map[string]any)["verifyTls"] = false
				json.NewEncoder(w).Encode(wire)
			} else {
				json.NewEncoder(w).Encode(response)
			}
		case "/api/v1/probe-agent/results":
			var request protocol.SubmitRequest
			if err := json.NewDecoder(r.Body).Decode(&request); err != nil || len(request.Results) != 1 {
				t.Error("invalid results")
				w.WriteHeader(400)
				return
			}
			if uploads.Add(1) == 1 {
				w.WriteHeader(http.StatusServiceUnavailable)
				return
			}
			submitted <- request.Results[0]
			json.NewEncoder(w).Encode(protocol.SubmitResponse{Results: []protocol.Ack{{TaskID: id, Status: protocol.AckAccepted}}})
		default:
			t.Error("unexpected endpoint")
			w.WriteHeader(404)
		}
	}))
	defer server.Close()
	dir := t.TempDir()
	ca := filepath.Join(dir, "ca.pem")
	if err := os.WriteFile(ca, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: server.Certificate().Raw}), 0600); err != nil {
		t.Fatal(err)
	}
	platform, err := client.New(server.URL, "", client.WithCAFile(ca))
	if err != nil {
		t.Fatal(err)
	}
	enrollment, err := platform.Exchange(ctx, "test-install")
	if err != nil {
		t.Fatal(err)
	}
	platform, err = client.New(server.URL, enrollment.RuntimeToken, client.WithCAFile(ca))
	if err != nil {
		t.Fatal(err)
	}
	check, err := checker.New(true, false, nil, func(ctx context.Context, network, address string) (net.Conn, error) {
		if network != "tcp4" || address != "8.8.8.8:443" {
			t.Errorf("dial target = %s %s", network, address)
		}
		dials.Add(1)
		return (&net.Dialer{}).DialContext(ctx, network, target.Listener.Addr().String())
	})
	if err != nil {
		t.Fatal(err)
	}
	disk, err := spool.Open(filepath.Join(dir, "spool"), 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer disk.Close()
	run, err := runner.New(platform, check, disk, config.Config{ProbeID: enrollment.ProbeID, MaxConcurrency: 8, AllowIPv4: true}, nil)
	if err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() { done <- run.Run(ctx) }()
	select {
	case result := <-submitted:
		if result.Outcome != protocol.OutcomeSuccess || dials.Load() != 1 || uploads.Load() != 2 {
			t.Fatalf("result = %#v, dials=%d", result, dials.Load())
		}
	case <-ctx.Done():
		t.Fatal("result never submitted")
	}
	if kind == "https" && targetRequests.Load() != 1 {
		t.Fatal("upload retry repeated HTTPS check")
	}
	cancel()
	if err := <-done; err != nil {
		t.Fatal(err)
	}
}
