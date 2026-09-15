package main

import (
	"crypto/rand"
	"crypto/rsa"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/json"
	"encoding/pem"
	"flag"
	"fmt"
	"log"
	"math/big"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/Bend-Function/MasterDNS-Agent/internal/protocol"
)

const (
	probeID      = "33333333-3333-4333-8333-333333333333"
	roundID      = "22222222-2222-4222-8222-222222222222"
	installToken = "ci-install-token"
	runtimeToken = "ci-runtime-token"
)

type fixture struct {
	dir     string
	ports   [2]int
	mu      sync.Mutex
	leased  bool
	results map[string]bool
}

func main() {
	dir := flag.String("dir", "", "fixture state directory")
	flag.Parse()
	if *dir == "" || flag.NArg() != 0 {
		log.Fatal("--dir is required")
	}
	if err := os.MkdirAll(*dir, 0o700); err != nil {
		log.Fatal(err)
	}

	ipv4, err := net.Listen("tcp4", "192.0.2.10:0")
	if err != nil {
		log.Fatalf("listen IPv4 target: %v", err)
	}
	defer ipv4.Close()
	ipv6, err := net.Listen("tcp6", "[2001:db8::10]:0")
	if err != nil {
		log.Fatalf("listen IPv6 target: %v", err)
	}
	defer ipv6.Close()
	go acceptTCP(ipv4)
	go acceptTCP(ipv6)

	certificate, caPEM, err := selfSignedCertificate()
	if err != nil {
		log.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(*dir, "ca.pem"), caPEM, 0o600); err != nil {
		log.Fatal(err)
	}
	listener, err := net.Listen("tcp4", "127.0.0.1:18443")
	if err != nil {
		log.Fatalf("listen platform API: %v", err)
	}
	defer listener.Close()

	f := &fixture{
		dir: *dir,
		ports: [2]int{
			ipv4.Addr().(*net.TCPAddr).Port,
			ipv6.Addr().(*net.TCPAddr).Port,
		},
		results: make(map[string]bool),
	}
	server := &http.Server{Handler: f, ReadHeaderTimeout: 5 * time.Second}
	if err := os.WriteFile(filepath.Join(*dir, "ready"), []byte("ready\n"), 0o600); err != nil {
		log.Fatal(err)
	}
	log.Printf("fixture ready with TCP ports %d and %d", f.ports[0], f.ports[1])
	if err := server.Serve(tls.NewListener(listener, &tls.Config{Certificates: []tls.Certificate{certificate}, MinVersion: tls.VersionTLS12})); err != nil && err != http.ErrServerClosed {
		log.Fatal(err)
	}
}

func acceptTCP(listener net.Listener) {
	for {
		connection, err := listener.Accept()
		if err != nil {
			return
		}
		connection.Close()
	}
}

func (f *fixture) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	switch r.URL.Path {
	case "/api/v1/probe-agent/exchange":
		var request struct {
			InstallToken string `json:"installToken"`
		}
		if r.Method != http.MethodPost || json.NewDecoder(r.Body).Decode(&request) != nil || request.InstallToken != installToken {
			http.Error(w, "invalid enrollment", http.StatusBadRequest)
			return
		}
		json.NewEncoder(w).Encode(protocol.Enrollment{ProbeID: probeID, RuntimeToken: runtimeToken, Protocol: protocol.Version})
		return
	}
	if r.Header.Get("Authorization") != "Bearer "+runtimeToken {
		w.WriteHeader(http.StatusUnauthorized)
		return
	}
	switch r.URL.Path {
	case "/api/v1/probe-agent/heartbeat":
		json.NewEncoder(w).Encode(struct{}{})
	case "/api/v1/probe-agent/tasks/lease":
		f.lease(w)
	case "/api/v1/probe-agent/results":
		f.submit(w, r)
	default:
		http.NotFound(w, r)
	}
}

func (f *fixture) lease(w http.ResponseWriter) {
	f.mu.Lock()
	defer f.mu.Unlock()
	now := time.Now().UTC()
	response := protocol.LeaseResponse{ServerTime: now, RetryAfterMS: 250}
	if !f.leased {
		f.leased = true
		response.Tasks = []protocol.Task{
			tcpTask("11111111-1111-4111-8111-111111111111", "44444444-4444-4444-8444-444444444444", "192.0.2.10", 4, f.ports[0], now),
			tcpTask("55555555-5555-4555-8555-555555555555", "66666666-6666-4666-8666-666666666666", "2001:db8::10", 6, f.ports[1], now),
		}
	}
	json.NewEncoder(w).Encode(response)
}

func tcpTask(taskID, leaseID, address string, family, port int, now time.Time) protocol.Task {
	return protocol.Task{
		Protocol: protocol.Version, TaskID: taskID, RoundID: roundID, ProbeID: probeID,
		LeaseID: leaseID, AddressVersion: 1, ConfigVersion: 1,
		Address: address, Family: family,
		Config:   protocol.CheckConfig{Type: "tcp", Port: port, TimeoutMS: 3000},
		Deadline: now.Add(20 * time.Second),
	}
}

func (f *fixture) submit(w http.ResponseWriter, r *http.Request) {
	var request protocol.SubmitRequest
	if err := json.NewDecoder(r.Body).Decode(&request); err != nil || request.Protocol != protocol.Version || len(request.Results) == 0 {
		http.Error(w, "invalid results", http.StatusBadRequest)
		return
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	response := protocol.SubmitResponse{}
	for _, result := range request.Results {
		response.Results = append(response.Results, protocol.Ack{TaskID: result.TaskID, Status: protocol.AckAccepted})
		if result.Outcome != protocol.OutcomeSuccess {
			f.fail(fmt.Sprintf("task %s returned %s (%s)", result.TaskID, result.Outcome, result.ErrorCode))
			continue
		}
		f.results[result.TaskID] = true
	}
	if f.results["11111111-1111-4111-8111-111111111111"] && f.results["55555555-5555-4555-8555-555555555555"] {
		if err := os.WriteFile(filepath.Join(f.dir, "passed"), []byte("IPv4 and IPv6 TCP probes passed\n"), 0o600); err != nil {
			log.Printf("write pass marker: %v", err)
		}
	}
	json.NewEncoder(w).Encode(response)
}

func (f *fixture) fail(message string) {
	if err := os.WriteFile(filepath.Join(f.dir, "failed"), []byte(message+"\n"), 0o600); err != nil {
		log.Printf("write failure marker: %v", err)
	}
}

func selfSignedCertificate() (tls.Certificate, []byte, error) {
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		return tls.Certificate{}, nil, err
	}
	now := time.Now()
	template := x509.Certificate{
		SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: "MasterDNS CI fixture"},
		NotBefore: now.Add(-time.Minute), NotAfter: now.Add(time.Hour),
		KeyUsage:              x509.KeyUsageDigitalSignature | x509.KeyUsageKeyEncipherment | x509.KeyUsageCertSign,
		ExtKeyUsage:           []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		BasicConstraintsValid: true, IsCA: true,
		IPAddresses: []net.IP{net.ParseIP("127.0.0.1")},
	}
	der, err := x509.CreateCertificate(rand.Reader, &template, &template, &key.PublicKey, key)
	if err != nil {
		return tls.Certificate{}, nil, err
	}
	certPEM := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})
	keyPEM := pem.EncodeToMemory(&pem.Block{Type: "RSA PRIVATE KEY", Bytes: x509.MarshalPKCS1PrivateKey(key)})
	certificate, err := tls.X509KeyPair(certPEM, keyPEM)
	return certificate, certPEM, err
}
