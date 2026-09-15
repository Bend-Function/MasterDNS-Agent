package protocol

import (
	"bytes"
	"encoding/json"
	"errors"
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
	var kind struct {
		Type string `json:"type"`
	}
	if err := json.Unmarshal(data, &kind); err != nil {
		return err
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
