package protocol

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"time"
)

const Version = "probe-agent/v1"

const (
	OutcomeSuccess     = "success"
	OutcomeFailure     = "failure"
	OutcomeUnavailable = "unavailable"
)

type Task struct {
	Protocol       string         `json:"protocol"`
	TaskID         string         `json:"taskId"`
	RoundID        string         `json:"roundId"`
	ProbeID        string         `json:"probeId"`
	LeaseID        string         `json:"leaseId"`
	AddressVersion int            `json:"addressVersion"`
	ConfigVersion  int            `json:"configVersion"`
	Address        string         `json:"address"`
	Family         int            `json:"family"`
	Hostname       string         `json:"hostname,omitempty"`
	Config         CheckConfig    `json:"config"`
	Deadline       time.Time      `json:"deadline"`
	NetworkPolicy  *NetworkPolicy `json:"networkPolicy,omitempty"`
}

type NetworkPolicy struct {
	AllowedPrivateCIDRs []string `json:"allowedPrivateCIDRs"`
}

type CheckConfig struct {
	Type              string            `json:"type"`
	Protocol          string            `json:"protocol,omitempty"`
	Port              int               `json:"port,omitempty"`
	Hostname          string            `json:"hostname,omitempty"`
	Method            string            `json:"method,omitempty"`
	Path              string            `json:"path,omitempty"`
	Headers           map[string]string `json:"headers,omitempty"`
	ExpectedStatuses  []int             `json:"expectedStatuses,omitempty"`
	ExpectedStatusMin int               `json:"expectedStatusMin,omitempty"`
	ExpectedStatusMax int               `json:"expectedStatusMax,omitempty"`
	BodyContains      string            `json:"bodyContains,omitempty"`
	BodyPattern       string            `json:"bodyPattern,omitempty"`
	FollowRedirects   bool              `json:"followRedirects,omitempty"`
	VerifyTLS         bool              `json:"verifyTls,omitempty"`
	TimeoutMS         int               `json:"timeoutMs,omitempty"`
}

func (c *CheckConfig) UnmarshalJSON(data []byte) error {
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(data, &fields); err != nil {
		return err
	}
	var kind struct {
		Type string `json:"type"`
	}
	if err := json.Unmarshal(data, &kind); err != nil {
		return err
	}
	if kind.Type == "tcp" {
		for name := range fields {
			if name != "type" && name != "port" && name != "timeoutMs" {
				return fmt.Errorf("field %q is not allowed for TCP checks", name)
			}
		}
	}

	type plainCheckConfig CheckConfig
	decoded := plainCheckConfig{}
	if kind.Type == "http" {
		decoded.Protocol = "https"
		decoded.Method = "GET"
		decoded.Path = "/"
		decoded.Headers = map[string]string{}
		decoded.ExpectedStatusMin = 200
		decoded.ExpectedStatusMax = 399
		decoded.FollowRedirects = true
		decoded.VerifyTLS = true
		decoded.TimeoutMS = 3000
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&decoded); err != nil {
		return err
	}
	*c = CheckConfig(decoded)
	return nil
}

type Result struct {
	Protocol       string    `json:"protocol"`
	TaskID         string    `json:"taskId"`
	LeaseID        string    `json:"leaseId"`
	AddressVersion int       `json:"addressVersion"`
	ConfigVersion  int       `json:"configVersion"`
	Outcome        string    `json:"outcome"`
	LatencyMS      float64   `json:"latencyMs"`
	MeasuredAt     time.Time `json:"measuredAt"`
	StatusCode     int       `json:"statusCode,omitempty"`
	ErrorCode      string    `json:"errorCode,omitempty"`
}

type Enrollment struct {
	ProbeID      string `json:"probeId"`
	RuntimeToken string `json:"runtimeToken"`
	Protocol     string `json:"protocol"`
}

type Capabilities struct {
	IPv4 bool `json:"ipv4"`
	IPv6 bool `json:"ipv6"`
}

type HeartbeatRequest struct {
	Protocol       string       `json:"protocol"`
	AgentVersion   string       `json:"agentVersion"`
	Capabilities   Capabilities `json:"capabilities"`
	MaxConcurrency int          `json:"maxConcurrency"`
}

type LeaseRequest struct {
	Protocol string `json:"protocol"`
	Capacity int    `json:"capacity"`
}

type LeaseResponse struct {
	ServerTime   time.Time `json:"serverTime"`
	Tasks        []Task    `json:"tasks"`
	RetryAfterMS int       `json:"retryAfterMs"`
	receivedAt   time.Time
}

func (r *LeaseResponse) SetReceivedAt(receivedAt time.Time) {
	r.receivedAt = receivedAt
}

// Remaining translates a server deadline into a duration measured with the
// local monotonic clock, so wall-clock skew cannot extend a task lease.
func (r LeaseResponse) Remaining(task Task, now time.Time) time.Duration {
	remainingAtReceipt := task.Deadline.Sub(r.ServerTime)
	if r.receivedAt.IsZero() {
		return remainingAtReceipt
	}
	remaining := remainingAtReceipt - now.Sub(r.receivedAt)
	if remaining < 0 {
		return 0
	}
	return remaining
}

type SubmitRequest struct {
	Protocol string   `json:"protocol"`
	Results  []Result `json:"results"`
}

const (
	AckAccepted  = "accepted"
	AckDuplicate = "duplicate"
	AckStale     = "stale"
	AckRejected  = "rejected"
)

type Ack struct {
	TaskID string `json:"taskId"`
	Status string `json:"status"`
}

type SubmitResponse struct {
	Results []Ack `json:"results"`
}

func DecodeTask(data []byte, task *Task) error {
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(task); err != nil {
		return err
	}
	var extra any
	if err := decoder.Decode(&extra); !errors.Is(err, io.EOF) {
		if err == nil {
			return errors.New("multiple JSON values")
		}
		return err
	}
	return nil
}
