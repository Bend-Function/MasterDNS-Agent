package protocol

import (
	"encoding/json"
	"os"
	"testing"
	"time"
)

func TestTaskFixtureParsesAndValidates(t *testing.T) {
	task := readTaskFixture(t)
	if err := ValidateTask(task, task.Deadline.Add(-time.Second)); err != nil {
		t.Fatalf("ValidateTask() error = %v", err)
	}
}

func TestTaskJSONRejectsUnknownFields(t *testing.T) {
	data, err := os.ReadFile("../../protocol/v1/fixtures/probe-task-v1.json")
	if err != nil {
		t.Fatal(err)
	}
	data = append(data[:len(data)-2], []byte(",\n  \"unexpected\": true\n}\n")...)
	var task Task
	if err := DecodeTask(data, &task); err == nil {
		t.Fatal("DecodeTask() accepted unknown field")
	}
}

func TestDecodeTaskRejectsHTTPOnlyFieldsOnTCPCheck(t *testing.T) {
	tests := map[string]string{
		"headers":               `"headers":{"X-Probe":"yes"}`,
		"body pattern":          `"bodyPattern":"ready"`,
		"redirects true":        `"followRedirects":true`,
		"redirects false":       `"followRedirects":false`,
		"explicit empty method": `"method":""`,
	}
	for name, field := range tests {
		t.Run(name, func(t *testing.T) {
			data := []byte(`{
  "protocol":"probe-agent/v1",
  "taskId":"11111111-1111-4111-8111-111111111111",
  "roundId":"22222222-2222-4222-8222-222222222222",
  "probeId":"33333333-3333-4333-8333-333333333333",
  "leaseId":"44444444-4444-4444-8444-444444444444",
  "addressVersion":1,
  "configVersion":1,
  "address":"192.0.2.10",
  "family":4,
  "config":{"type":"tcp","port":443,` + field + `},
  "deadline":"2026-09-15T12:00:00Z"
}`)
			var task Task
			if err := DecodeTask(data, &task); err == nil {
				t.Fatalf("accepted HTTP-only TCP field %s", field)
			}
		})
	}
}

func TestRejectWrongFamily(t *testing.T) {
	task := readTaskFixture(t)
	task.Address, task.Family = "192.0.2.10", 6
	if err := ValidateTask(task, task.Deadline.Add(-time.Second)); err == nil {
		t.Fatal("accepted IPv4 address as IPv6")
	}
}

func TestValidateTaskRejectsPermanentlyForbiddenTargets(t *testing.T) {
	tests := []struct {
		name    string
		address string
		family  int
		cidr    string
	}{
		{"unspecified", "0.0.0.0", 4, "0.0.0.0/0"},
		{"loopback", "127.0.0.1", 4, "127.0.0.0/8"},
		{"link local", "169.254.169.254", 4, "169.254.0.0/16"},
		{"multicast", "224.0.0.1", 4, "224.0.0.0/4"},
		{"future use", "240.0.0.1", 4, "240.0.0.0/4"},
		{"IPv6 loopback", "::1", 6, "::1/128"},
		{"IPv6 link local", "fe80::1", 6, "fe80::/10"},
		{"mapped IPv6", "::ffff:192.0.2.10", 6, "::/0"},
		{"Alibaba metadata", "100.100.100.200", 4, "100.64.0.0/10"},
		{"AWS IPv6 metadata", "fd00:ec2::254", 6, "fd00::/8"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			task := readTaskFixture(t)
			task.Address, task.Family = tc.address, tc.family
			task.NetworkPolicy = &NetworkPolicy{AllowedPrivateCIDRs: []string{tc.cidr}}
			if err := ValidateTask(task, task.Deadline.Add(-time.Second)); err == nil {
				t.Fatalf("accepted permanently forbidden address %s", tc.address)
			}
		})
	}
}

func TestValidateTaskRequiresPlatformAllowlistForPrivateTarget(t *testing.T) {
	task := readTaskFixture(t)
	task.Address = "10.1.2.3"
	if err := ValidateTask(task, task.Deadline.Add(-time.Second)); err == nil {
		t.Fatal("accepted private target without network policy")
	}
	task.NetworkPolicy = &NetworkPolicy{AllowedPrivateCIDRs: []string{"10.0.0.0/8"}}
	if err := ValidateTask(task, task.Deadline.Add(-time.Second)); err != nil {
		t.Fatalf("rejected platform-authorized private target: %v", err)
	}
}

func TestValidateTaskRejectsMalformedNetworkPolicy(t *testing.T) {
	task := readTaskFixture(t)
	task.NetworkPolicy = &NetworkPolicy{AllowedPrivateCIDRs: []string{"not-a-cidr"}}
	if err := ValidateTask(task, task.Deadline.Add(-time.Second)); err == nil {
		t.Fatal("accepted malformed network policy")
	}
}

func TestValidateTaskRejectsInvalidEnvelopeAndCheck(t *testing.T) {
	tests := map[string]func(*Task){
		"protocol":         func(task *Task) { task.Protocol = "probe-agent/v2" },
		"task UUID":        func(task *Task) { task.TaskID = "not-a-uuid" },
		"address version":  func(task *Task) { task.AddressVersion = 0 },
		"expired deadline": func(task *Task) { task.Deadline = time.Unix(1, 0) },
		"check type":       func(task *Task) { task.Config.Type = "icmp" },
		"TCP port":         func(task *Task) { task.Config.Port = 0 },
	}
	for name, mutate := range tests {
		t.Run(name, func(t *testing.T) {
			task := readTaskFixture(t)
			mutate(&task)
			if err := ValidateTask(task, time.Unix(2, 0)); err == nil {
				t.Fatal("ValidateTask() accepted invalid task")
			}
		})
	}
}

func TestValidateHTTPCheck(t *testing.T) {
	task := readTaskFixture(t)
	task.Config = CheckConfig{Type: "http", Protocol: "https", Port: 443, Hostname: "service.example.com", Method: "HEAD", Path: "/health", TimeoutMS: 3000, ExpectedStatusMin: 200, ExpectedStatusMax: 399}
	if err := ValidateTask(task, task.Deadline.Add(-time.Second)); err != nil {
		t.Fatalf("ValidateTask() error = %v", err)
	}
	for name, mutate := range map[string]func(*CheckConfig){
		"protocol":     func(c *CheckConfig) { c.Protocol = "ftp" },
		"method":       func(c *CheckConfig) { c.Method = "POST" },
		"path":         func(c *CheckConfig) { c.Path = "health" },
		"status range": func(c *CheckConfig) { c.ExpectedStatusMin, c.ExpectedStatusMax = 400, 200 },
	} {
		t.Run(name, func(t *testing.T) {
			invalid := task
			mutate(&invalid.Config)
			if err := ValidateTask(invalid, invalid.Deadline.Add(-time.Second)); err == nil {
				t.Fatal("ValidateTask() accepted invalid HTTP check")
			}
		})
	}
}

func TestValidateHTTPCheckAcceptsSchemaDefaults(t *testing.T) {
	task := readTaskFixture(t)
	task.Config = CheckConfig{Type: "http", Port: 443}
	if err := ValidateTask(task, task.Deadline.Add(-time.Second)); err != nil {
		t.Fatalf("ValidateTask() rejected schema defaults: %v", err)
	}
}

func TestDecodeTaskMaterializesHTTPDefaultsAndPreservesExplicitFalse(t *testing.T) {
	data := []byte(`{
  "protocol":"probe-agent/v1",
  "taskId":"11111111-1111-4111-8111-111111111111",
  "roundId":"22222222-2222-4222-8222-222222222222",
  "probeId":"33333333-3333-4333-8333-333333333333",
  "leaseId":"44444444-4444-4444-8444-444444444444",
  "addressVersion":1,
  "configVersion":1,
  "address":"192.0.2.10",
  "family":4,
  "hostname":"service.example.com",
  "config":{"type":"http","followRedirects":false},
  "deadline":"2026-09-15T12:00:00Z"
}`)
	var task Task
	if err := DecodeTask(data, &task); err != nil {
		t.Fatal(err)
	}
	if task.Config.Protocol != "https" || task.Config.Method != "GET" || task.Config.Path != "/" || task.Config.TimeoutMS != 3000 || task.Config.ExpectedStatusMin != 200 || task.Config.ExpectedStatusMax != 399 {
		t.Fatalf("HTTP defaults not materialized: %#v", task.Config)
	}
	if task.Config.FollowRedirects {
		t.Fatal("explicit followRedirects=false was overwritten")
	}
	if !task.Config.VerifyTLS {
		t.Fatal("omitted verifyTls did not default to true")
	}
}

func TestResultUsesProtocolWireNamesAndUnavailableOutcome(t *testing.T) {
	result := Result{Protocol: Version, TaskID: "11111111-1111-4111-8111-111111111111", LeaseID: "44444444-4444-4444-8444-444444444444", AddressVersion: 1, ConfigVersion: 1, Outcome: OutcomeUnavailable, LatencyMS: 1.5, MeasuredAt: time.Unix(0, 0).UTC(), ErrorCode: "ipv6_unavailable"}
	data, err := json.Marshal(result)
	if err != nil {
		t.Fatal(err)
	}
	want := `{"protocol":"probe-agent/v1","taskId":"11111111-1111-4111-8111-111111111111","leaseId":"44444444-4444-4444-8444-444444444444","addressVersion":1,"configVersion":1,"outcome":"unavailable","latencyMs":1.5,"measuredAt":"1970-01-01T00:00:00Z","errorCode":"ipv6_unavailable"}`
	if string(data) != want {
		t.Fatalf("json.Marshal() = %s, want %s", data, want)
	}
}

func readTaskFixture(t *testing.T) Task {
	t.Helper()
	data, err := os.ReadFile("../../protocol/v1/fixtures/probe-task-v1.json")
	if err != nil {
		t.Fatal(err)
	}
	var task Task
	if err := DecodeTask(data, &task); err != nil {
		t.Fatal(err)
	}
	return task
}
