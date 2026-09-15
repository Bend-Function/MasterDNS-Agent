package config

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestLoadValidConfig(t *testing.T) {
	dir := t.TempDir()
	tokenFile := filepath.Join(dir, "token")
	if err := os.WriteFile(tokenFile, []byte("secret\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	path := writeConfig(t, dir, `{
  "serverUrl": "https://masterdns.example",
  "probeId": "33333333-3333-4333-8333-333333333333",
  "tokenFile": "`+tokenFile+`",
  "stateDir": "`+filepath.Join(dir, "state")+`",
  "maxConcurrency": 8,
  "allowIpv4": true,
  "allowIpv6": false,
  "allowedPrivateCidrs": ["10.0.0.0/8"]
}`)

	got, err := Load(path)
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if got.ServerURL != "https://masterdns.example" || got.MaxConcurrency != 8 || !got.AllowIPv4 || got.AllowIPv6 {
		t.Fatalf("Load() = %#v", got)
	}
}

func TestLoadRejectsInvalidConfiguration(t *testing.T) {
	dir := t.TempDir()
	tokenFile := filepath.Join(dir, "token")
	if err := os.WriteFile(tokenFile, []byte("secret"), 0o600); err != nil {
		t.Fatal(err)
	}
	base := `{"serverUrl":"https://masterdns.example","probeId":"33333333-3333-4333-8333-333333333333","tokenFile":"` + tokenFile + `","stateDir":"` + filepath.Join(dir, "state") + `","maxConcurrency":8,"allowIpv4":true,"allowIpv6":false,"allowedPrivateCidrs":[]}`
	tests := map[string]string{
		"unknown field":       strings.TrimSuffix(base, "}") + `,"extra":true}`,
		"non HTTPS server":    strings.Replace(base, "https://", "http://", 1),
		"invalid probe UUID":  strings.Replace(base, "33333333-3333-4333-8333-333333333333", "not-a-uuid", 1),
		"above runner limit":  strings.Replace(base, `"maxConcurrency":8`, `"maxConcurrency":65`, 1),
		"unbounded workers":   strings.Replace(base, `"maxConcurrency":8`, `"maxConcurrency":101`, 1),
		"zero workers":        strings.Replace(base, `"maxConcurrency":8`, `"maxConcurrency":0`, 1),
		"no address families": strings.Replace(base, `"allowIpv4":true`, `"allowIpv4":false`, 1),
		"invalid CIDR":        strings.Replace(base, `"allowedPrivateCidrs":[]`, `"allowedPrivateCidrs":["bad"]`, 1),
	}
	for name, body := range tests {
		t.Run(name, func(t *testing.T) {
			if _, err := Load(writeConfig(t, dir, body)); err == nil {
				t.Fatal("Load() accepted invalid configuration")
			}
		})
	}
}

func TestLoadRejectsInsecureTokenFileOnUnix(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Unix permission semantics")
	}
	dir := t.TempDir()
	tokenFile := filepath.Join(dir, "token")
	if err := os.WriteFile(tokenFile, []byte("secret"), 0o644); err != nil {
		t.Fatal(err)
	}
	body := `{"serverUrl":"https://masterdns.example","probeId":"33333333-3333-4333-8333-333333333333","tokenFile":"` + tokenFile + `","stateDir":"` + filepath.Join(dir, "state") + `","maxConcurrency":1,"allowIpv4":true,"allowIpv6":false,"allowedPrivateCidrs":[]}`
	if _, err := Load(writeConfig(t, dir, body)); err == nil {
		t.Fatal("Load() accepted group/world-readable token file")
	}
}

func TestLoadForTestingAllowsLoopbackHTTP(t *testing.T) {
	dir := t.TempDir()
	tokenFile := filepath.Join(dir, "token")
	if err := os.WriteFile(tokenFile, []byte("secret"), 0o600); err != nil {
		t.Fatal(err)
	}
	body := `{"serverUrl":"http://127.0.0.1:12345","probeId":"33333333-3333-4333-8333-333333333333","tokenFile":"` + tokenFile + `","stateDir":"` + filepath.Join(dir, "state") + `","maxConcurrency":1,"allowIpv4":true,"allowIpv6":false,"allowedPrivateCidrs":[]}`
	if _, err := LoadForTesting(writeConfig(t, dir, body)); err != nil {
		t.Fatalf("LoadForTesting() error = %v", err)
	}
}

func TestLoadForEnrollmentDoesNotRequireIssuedIdentity(t *testing.T) {
	dir := t.TempDir()
	body := `{"serverUrl":"https://masterdns.example","caFile":"` + filepath.Join(dir, "ca.pem") + `","probeId":"","tokenFile":"` + filepath.Join(dir, "runtime-token") + `","stateDir":"` + filepath.Join(dir, "state") + `","maxConcurrency":1,"allowIpv4":true,"allowIpv6":false,"allowedPrivateCidrs":[]}`
	got, err := LoadForEnrollment(writeConfig(t, dir, body))
	if err != nil {
		t.Fatalf("LoadForEnrollment() error = %v", err)
	}
	if got.ProbeID != "" || got.CAFile != filepath.Join(dir, "ca.pem") {
		t.Fatalf("LoadForEnrollment() = %#v", got)
	}
	if _, err := Load(writeConfig(t, dir, body)); err == nil {
		t.Fatal("normal Load accepted config without issued identity")
	}
}

func writeConfig(t *testing.T, dir, body string) string {
	t.Helper()
	path := filepath.Join(dir, strings.ReplaceAll(t.Name(), "/", "-")+".json")
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestLoadDefaultsConcurrencyWhenOmitted(t *testing.T) {
	dir := t.TempDir()
	token := filepath.Join(dir, "token")
	if err := os.WriteFile(token, []byte("secret"), 0600); err != nil {
		t.Fatal(err)
	}
	body := `{"serverUrl":"https://masterdns.example","probeId":"33333333-3333-4333-8333-333333333333","tokenFile":"` + token + `","stateDir":"` + dir + `","allowIpv4":true}`
	cfg, err := Load(writeConfig(t, dir, body))
	if err != nil || cfg.MaxConcurrency != 8 {
		t.Fatalf("default concurrency = %d, %v", cfg.MaxConcurrency, err)
	}
}
