package checker

import (
	"context"
	"crypto/x509"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/Bend-Function/MasterDNS-Agent/internal/protocol"
)

func TestHTTPPinsAddressFamilyHostAndPreservesQuery(t *testing.T) {
	var gotMethod, gotHost, gotURI, gotHeader string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotMethod, gotHost, gotURI = r.Method, r.Host, r.RequestURI
		gotHeader = r.Header.Get("X-Probe")
		fmt.Fprint(w, "ready=true")
	}))
	defer server.Close()

	dialer := &recordingDialer{target: server.Listener.Addr().String()}
	checker := mustChecker(t, true, false, dialer.DialContext)
	task := httpTask("192.0.2.20", 4, server.Listener.Addr().(*net.TCPAddr).Port)
	task.Config.Path = "/health?ready=1&region=nz"
	task.Config.Headers = map[string]string{"X-Probe": "masterdns"}
	task.Config.BodyContains = "ready=true"

	got := checker.Check(context.Background(), task)
	if got.Outcome != protocol.OutcomeSuccess || got.StatusCode != http.StatusOK {
		t.Fatalf("Check() = %#v", got)
	}
	if gotMethod != http.MethodGet || gotHost != "health.example.test" || gotURI != "/health?ready=1&region=nz" {
		t.Fatalf("request = method %q, host %q, URI %q", gotMethod, gotHost, gotURI)
	}
	if gotHeader != "masterdns" {
		t.Fatalf("X-Probe = %q", gotHeader)
	}
	if calls := dialer.Calls(); len(calls) != 1 || calls[0] != "tcp4 192.0.2.20:"+portString(server.Listener) {
		t.Fatalf("dial calls = %v", calls)
	}
}

func TestHTTPSUsesConfiguredHostForHostAndSNI(t *testing.T) {
	var gotHost string
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotHost = r.Host
		fmt.Fprint(w, "ok")
	}))
	defer server.Close()

	dialer := &recordingDialer{target: server.Listener.Addr().String()}
	checker := mustChecker(t, true, false, dialer.DialContext)
	checker.rootCAs = certPool(t, server.Certificate())
	task := httpTask("192.0.2.21", 4, server.Listener.Addr().(*net.TCPAddr).Port)
	task.Config.Protocol = "https"
	task.Config.Hostname = "example.com"
	task.Hostname = "ignored.example.test"
	task.Config.VerifyTLS = true

	got := checker.Check(context.Background(), task)
	if got.Outcome != protocol.OutcomeSuccess || gotHost != "example.com" {
		t.Fatalf("Check() = %#v, Host = %q", got, gotHost)
	}
	if calls := dialer.Calls(); len(calls) != 1 || !strings.HasPrefix(calls[0], "tcp4 192.0.2.21:") {
		t.Fatalf("dial calls = %v", calls)
	}
}

func TestHTTPSCertificateNameMismatchFails(t *testing.T) {
	server := httptest.NewTLSServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	defer server.Close()
	checker := mustChecker(t, true, false, (&recordingDialer{target: server.Listener.Addr().String()}).DialContext)
	checker.rootCAs = certPool(t, server.Certificate())
	task := httpTask("192.0.2.22", 4, server.Listener.Addr().(*net.TCPAddr).Port)
	task.Config.Protocol, task.Config.Hostname, task.Config.VerifyTLS = "https", "wrong.example.test", true

	got := checker.Check(context.Background(), task)
	if got.Outcome != protocol.OutcomeFailure || got.ErrorCode != "tls_failed" {
		t.Fatalf("Check() = %#v", got)
	}
}

func TestHTTPSVerificationCanBeExplicitlyDisabled(t *testing.T) {
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { fmt.Fprint(w, "ok") }))
	defer server.Close()
	checker := mustChecker(t, true, false, (&recordingDialer{target: server.Listener.Addr().String()}).DialContext)
	task := httpTask("192.0.2.23", 4, server.Listener.Addr().(*net.TCPAddr).Port)
	task.Config.Protocol, task.Config.Hostname, task.Config.VerifyTLS = "https", "wrong.example.test", false

	got := checker.Check(context.Background(), task)
	if got.Outcome != protocol.OutcomeSuccess {
		t.Fatalf("Check() = %#v", got)
	}
}

func TestHTTPHeadAndStatusMatchers(t *testing.T) {
	var gotMethod string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotMethod = r.Method
		w.WriteHeader(http.StatusNoContent)
	}))
	defer server.Close()

	for name, configure := range map[string]func(*protocol.CheckConfig){
		"range": func(c *protocol.CheckConfig) { c.ExpectedStatusMin, c.ExpectedStatusMax = 200, 299 },
		"list":  func(c *protocol.CheckConfig) { c.ExpectedStatuses = []int{201, 204} },
	} {
		t.Run(name, func(t *testing.T) {
			checker := mustChecker(t, true, false, (&recordingDialer{target: server.Listener.Addr().String()}).DialContext)
			task := httpTask("192.0.2.24", 4, server.Listener.Addr().(*net.TCPAddr).Port)
			task.Config.Method = http.MethodHead
			configure(&task.Config)
			got := checker.Check(context.Background(), task)
			if got.Outcome != protocol.OutcomeSuccess || gotMethod != http.MethodHead {
				t.Fatalf("Check() = %#v, method = %q", got, gotMethod)
			}
		})
	}
}

func TestHTTPReportsUnexpectedStatus(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusServiceUnavailable) }))
	defer server.Close()
	checker := mustChecker(t, true, false, (&recordingDialer{target: server.Listener.Addr().String()}).DialContext)
	task := httpTask("192.0.2.25", 4, server.Listener.Addr().(*net.TCPAddr).Port)
	task.Config.ExpectedStatuses = []int{200, 204}

	got := checker.Check(context.Background(), task)
	if got.Outcome != protocol.OutcomeFailure || got.ErrorCode != "unexpected_status" || got.StatusCode != 503 {
		t.Fatalf("Check() = %#v", got)
	}
}

func TestHTTPBodyMatchers(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { fmt.Fprint(w, "state=ready token=abc123") }))
	defer server.Close()
	for name, configure := range map[string]func(*protocol.CheckConfig){
		"contains": func(c *protocol.CheckConfig) { c.BodyContains = "state=ready" },
		"pattern":  func(c *protocol.CheckConfig) { c.BodyPattern = `token=[a-z]+\d+` },
	} {
		t.Run(name, func(t *testing.T) {
			checker := mustChecker(t, true, false, (&recordingDialer{target: server.Listener.Addr().String()}).DialContext)
			task := httpTask("192.0.2.26", 4, server.Listener.Addr().(*net.TCPAddr).Port)
			configure(&task.Config)
			if got := checker.Check(context.Background(), task); got.Outcome != protocol.OutcomeSuccess {
				t.Fatalf("Check() = %#v", got)
			}
		})
	}
}

func TestHTTPBodyMismatchDoesNotExposeBody(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { fmt.Fprint(w, "secret response") }))
	defer server.Close()
	checker := mustChecker(t, true, false, (&recordingDialer{target: server.Listener.Addr().String()}).DialContext)
	task := httpTask("192.0.2.27", 4, server.Listener.Addr().(*net.TCPAddr).Port)
	task.Config.BodyContains = "healthy"

	got := checker.Check(context.Background(), task)
	if got.Outcome != protocol.OutcomeFailure || got.ErrorCode != "body_mismatch" || strings.Contains(fmt.Sprintf("%#v", got), "secret") {
		t.Fatalf("Check() = %#v", got)
	}
}

func TestHTTPBodyMatcherRejectsOversizeBody(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(w, strings.Repeat("x", 1<<20+1))
	}))
	defer server.Close()
	checker := mustChecker(t, true, false, (&recordingDialer{target: server.Listener.Addr().String()}).DialContext)
	task := httpTask("192.0.2.28", 4, server.Listener.Addr().(*net.TCPAddr).Port)
	task.Config.BodyContains = "ready"

	got := checker.Check(context.Background(), task)
	if got.Outcome != protocol.OutcomeFailure || got.ErrorCode != "body_limit_exceeded" {
		t.Fatalf("Check() = %#v", got)
	}
}

func TestHTTPRedirectsStayOnConfiguredOriginAndPinnedAddress(t *testing.T) {
	var requests []string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests = append(requests, r.Host+r.RequestURI)
		if r.URL.Path == "/start" {
			http.Redirect(w, r, "/ready?from=redirect", http.StatusFound)
			return
		}
		fmt.Fprint(w, "ok")
	}))
	defer server.Close()
	dialer := &recordingDialer{target: server.Listener.Addr().String()}
	checker := mustChecker(t, true, false, dialer.DialContext)
	task := httpTask("192.0.2.29", 4, server.Listener.Addr().(*net.TCPAddr).Port)
	task.Config.Path, task.Config.FollowRedirects = "/start", true

	got := checker.Check(context.Background(), task)
	if got.Outcome != protocol.OutcomeSuccess {
		t.Fatalf("Check() = %#v", got)
	}
	if strings.Join(requests, ",") != "health.example.test/start,health.example.test/ready?from=redirect" {
		t.Fatalf("requests = %v", requests)
	}
	if calls := dialer.Calls(); len(calls) != 2 {
		t.Fatalf("dial calls = %v", calls)
	}
}

func TestHTTPRejectsCrossOriginRedirect(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "http://other.example.test/ready", http.StatusFound)
	}))
	defer server.Close()
	checker := mustChecker(t, true, false, (&recordingDialer{target: server.Listener.Addr().String()}).DialContext)
	task := httpTask("192.0.2.30", 4, server.Listener.Addr().(*net.TCPAddr).Port)
	task.Config.FollowRedirects = true

	got := checker.Check(context.Background(), task)
	if got.Outcome != protocol.OutcomeFailure || got.ErrorCode != "redirect_forbidden" {
		t.Fatalf("Check() = %#v", got)
	}
}

func TestHTTPSRejectsDowngradeRedirect(t *testing.T) {
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "http://example.com/ready", http.StatusFound)
	}))
	defer server.Close()
	checker := mustChecker(t, true, false, (&recordingDialer{target: server.Listener.Addr().String()}).DialContext)
	checker.rootCAs = certPool(t, server.Certificate())
	task := httpTask("192.0.2.36", 4, server.Listener.Addr().(*net.TCPAddr).Port)
	task.Config.Protocol, task.Config.Hostname, task.Config.VerifyTLS = "https", "example.com", true
	task.Config.FollowRedirects = true

	got := checker.Check(context.Background(), task)
	if got.Outcome != protocol.OutcomeFailure || got.ErrorCode != "redirect_forbidden" {
		t.Fatalf("Check() = %#v", got)
	}
}

func TestHTTPDoesNotFollowRedirectWhenDisabled(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "/ready", http.StatusFound)
	}))
	defer server.Close()
	checker := mustChecker(t, true, false, (&recordingDialer{target: server.Listener.Addr().String()}).DialContext)
	task := httpTask("192.0.2.31", 4, server.Listener.Addr().(*net.TCPAddr).Port)
	task.Config.FollowRedirects = false
	task.Config.ExpectedStatuses = []int{http.StatusFound}

	got := checker.Check(context.Background(), task)
	if got.Outcome != protocol.OutcomeSuccess || got.StatusCode != http.StatusFound {
		t.Fatalf("Check() = %#v", got)
	}
}

func TestHTTPStopsAfterFiveRedirects(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, fmt.Sprintf("/step?n=%d", len(r.URL.Query().Get("n"))+1), http.StatusFound)
	}))
	defer server.Close()
	checker := mustChecker(t, true, false, (&recordingDialer{target: server.Listener.Addr().String()}).DialContext)
	task := httpTask("192.0.2.32", 4, server.Listener.Addr().(*net.TCPAddr).Port)
	task.Config.FollowRedirects = true

	got := checker.Check(context.Background(), task)
	if got.Outcome != protocol.OutcomeFailure || got.ErrorCode != "too_many_redirects" {
		t.Fatalf("Check() = %#v", got)
	}
}

func TestHTTPCallerCancellationIsUnavailable(t *testing.T) {
	checker := mustChecker(t, true, false, func(context.Context, string, string) (net.Conn, error) {
		return nil, &net.OpError{Op: "dial", Net: "tcp4", Err: fmt.Errorf("wrapped: %w", io.ErrClosedPipe)}
	})
	task := httpTask("192.0.2.33", 4, 80)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	got := checker.Check(ctx, task)
	if got.Outcome != protocol.OutcomeUnavailable || got.ErrorCode != "check_canceled" {
		t.Fatalf("Check() = %#v", got)
	}
}

func TestHTTPLocalNetworkFailureIsUnavailable(t *testing.T) {
	checker := mustChecker(t, true, false, func(context.Context, string, string) (net.Conn, error) {
		return nil, syscall.ENETUNREACH
	})
	got := checker.Check(context.Background(), httpTask("192.0.2.35", 4, 80))
	if got.Outcome != protocol.OutcomeUnavailable || got.ErrorCode != "network_unavailable" {
		t.Fatalf("Check() = %#v", got)
	}
}

type recordingDialer struct {
	mu     sync.Mutex
	target string
	calls  []string
}

func (d *recordingDialer) DialContext(ctx context.Context, network, address string) (net.Conn, error) {
	d.mu.Lock()
	d.calls = append(d.calls, network+" "+address)
	d.mu.Unlock()
	return (&net.Dialer{}).DialContext(ctx, network, d.target)
}

func (d *recordingDialer) Calls() []string {
	d.mu.Lock()
	defer d.mu.Unlock()
	return append([]string(nil), d.calls...)
}

func httpTask(address string, family, port int) protocol.Task {
	task := tcpTask(address, family, port)
	task.Hostname = "health.example.test"
	task.Config = protocol.CheckConfig{
		Type: "http", Protocol: "http", Port: port, Method: http.MethodGet, Path: "/",
		ExpectedStatusMin: 200, ExpectedStatusMax: 399, TimeoutMS: 1000,
	}
	return task
}

func certPool(t *testing.T, cert *x509.Certificate) *x509.CertPool {
	t.Helper()
	pool := x509.NewCertPool()
	pool.AddCert(cert)
	return pool
}

func TestHTTPCallerCancellationWhileReadingBodyIsUnavailable(t *testing.T) {
	headersFlushed := make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Length", "5")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("r"))
		if f, ok := w.(http.Flusher); ok {
			f.Flush()
		}
		close(headersFlushed)
		<-r.Context().Done()
	}))
	defer server.Close()
	checker := mustChecker(t, true, false, (&recordingDialer{target: server.Listener.Addr().String()}).DialContext)
	task := httpTask("192.0.2.37", 4, server.Listener.Addr().(*net.TCPAddr).Port)
	task.Config.BodyContains = "ready"
	ctx, cancel := context.WithCancel(context.Background())
	result := make(chan protocol.Result, 1)
	go func() { result <- checker.Check(ctx, task) }()
	<-headersFlushed
	time.Sleep(50 * time.Millisecond)
	cancel()

	got := <-result
	if got.Outcome != protocol.OutcomeUnavailable || got.ErrorCode != "check_canceled" {
		t.Fatalf("Check() = %#v", got)
	}
}

func TestHTTPTaskDeadlineBoundsBodyRead(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
		if f, ok := w.(http.Flusher); ok {
			f.Flush()
		}
		<-time.After(time.Second)
	}))
	defer server.Close()
	checker := mustChecker(t, true, false, (&recordingDialer{target: server.Listener.Addr().String()}).DialContext)
	task := httpTask("192.0.2.34", 4, server.Listener.Addr().(*net.TCPAddr).Port)
	task.Config.BodyContains = "ready"
	task.Deadline = time.Now().Add(100 * time.Millisecond)
	got := checker.Check(context.Background(), task)
	if got.Outcome != protocol.OutcomeFailure || got.ErrorCode != "http_failed" || got.LatencyMS > 500 {
		t.Fatalf("Check() = %#v", got)
	}
}
