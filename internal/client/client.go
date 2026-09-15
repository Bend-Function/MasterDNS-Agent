package client

import (
	"bytes"
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"

	"github.com/Bend-Function/MasterDNS-Agent/internal/protocol"
)

const (
	requestTimeout   = 15 * time.Second
	maxResponseBytes = 1 << 20
)

var ErrUnauthorized = errors.New("platform authentication rejected")

type HTTPError struct {
	StatusCode int
}

func (e *HTTPError) Error() string { return fmt.Sprintf("platform returned HTTP %d", e.StatusCode) }

type Capabilities struct {
	AgentVersion   string
	IPv4           bool
	IPv6           bool
	MaxConcurrency int
}

type Enrollment = protocol.Enrollment
type LeaseResponse = protocol.LeaseResponse
type Ack = protocol.Ack

type Client struct {
	baseURL      *url.URL
	runtimeToken string
	httpClient   *http.Client
}

type Option func(*http.Transport) error

func WithCAFile(path string) Option {
	return func(transport *http.Transport) error {
		pemData, err := os.ReadFile(path)
		if err != nil {
			return fmt.Errorf("read platform CA file: %w", err)
		}
		roots, err := x509.SystemCertPool()
		if err != nil || roots == nil {
			roots = x509.NewCertPool()
		}
		if !roots.AppendCertsFromPEM(pemData) {
			return errors.New("platform CA file contains no certificates")
		}
		transport.TLSClientConfig = &tls.Config{RootCAs: roots, MinVersion: tls.VersionTLS12}
		return nil
	}
}

func New(serverURL, runtimeToken string, options ...Option) (*Client, error) {
	u, err := parseBaseURL(serverURL)
	if err != nil {
		return nil, err
	}
	if u.Scheme != "https" {
		return nil, errors.New("platform URL must use HTTPS")
	}
	transport, ok := http.DefaultTransport.(*http.Transport)
	if !ok {
		return nil, errors.New("default HTTP transport is unavailable")
	}
	transport = transport.Clone()
	for _, option := range options {
		if err := option(transport); err != nil {
			return nil, err
		}
	}
	return buildClient(u, runtimeToken, &http.Client{Timeout: requestTimeout, Transport: transport})
}

func newClient(serverURL, runtimeToken string, httpClient *http.Client) (*Client, error) {
	u, err := parseBaseURL(serverURL)
	if err != nil {
		return nil, err
	}
	return buildClient(u, runtimeToken, httpClient)
}

func buildClient(baseURL *url.URL, runtimeToken string, httpClient *http.Client) (*Client, error) {
	if httpClient == nil || httpClient.Timeout <= 0 {
		return nil, errors.New("HTTP client must have a bounded timeout")
	}
	copyClient := *httpClient
	copyClient.CheckRedirect = func(_ *http.Request, _ []*http.Request) error {
		return errors.New("platform redirects are forbidden")
	}
	return &Client{baseURL: baseURL, runtimeToken: runtimeToken, httpClient: &copyClient}, nil
}

func parseBaseURL(raw string) (*url.URL, error) {
	u, err := url.Parse(raw)
	if err != nil || u.Host == "" || u.User != nil || (u.Scheme != "https" && u.Scheme != "http") {
		return nil, errors.New("platform URL must be an absolute HTTP URL without credentials")
	}
	if u.RawQuery != "" || u.Fragment != "" {
		return nil, errors.New("platform URL must not contain a query or fragment")
	}
	u.Path = strings.TrimRight(u.Path, "/")
	return u, nil
}

func (c *Client) Exchange(ctx context.Context, installToken string) (Enrollment, error) {
	var enrollment Enrollment
	err := c.post(ctx, "/api/v1/probe-agent/exchange", struct {
		InstallToken string `json:"installToken"`
	}{installToken}, false, &enrollment)
	if err != nil {
		return Enrollment{}, err
	}
	if !protocol.ValidID(enrollment.ProbeID) || enrollment.RuntimeToken == "" || enrollment.Protocol != protocol.Version {
		return Enrollment{}, errors.New("platform returned invalid enrollment")
	}
	return enrollment, nil
}

func (c *Client) Heartbeat(ctx context.Context, capabilities Capabilities) error {
	request := protocol.HeartbeatRequest{
		Protocol: protocol.Version, AgentVersion: capabilities.AgentVersion,
		Capabilities:   protocol.Capabilities{IPv4: capabilities.IPv4, IPv6: capabilities.IPv6},
		MaxConcurrency: capabilities.MaxConcurrency,
	}
	return c.post(ctx, "/api/v1/probe-agent/heartbeat", request, true, nil)
}

func (c *Client) Lease(ctx context.Context, capacity int) (LeaseResponse, error) {
	if capacity < 1 || capacity > 100 {
		return LeaseResponse{}, errors.New("lease capacity must be between 1 and 100")
	}
	var response LeaseResponse
	if err := c.post(ctx, "/api/v1/probe-agent/tasks/lease", protocol.LeaseRequest{Protocol: protocol.Version, Capacity: capacity}, true, &response); err != nil {
		return LeaseResponse{}, err
	}
	response.SetReceivedAt(time.Now())
	if response.ServerTime.IsZero() || response.RetryAfterMS < 0 || len(response.Tasks) > 100 {
		return LeaseResponse{}, errors.New("platform returned invalid lease response")
	}
	return response, nil
}

func (c *Client) Submit(ctx context.Context, results []protocol.Result) ([]Ack, error) {
	if len(results) < 1 || len(results) > 100 {
		return nil, errors.New("result batch must contain between 1 and 100 items")
	}
	var acknowledgements []Ack
	request := protocol.SubmitRequest{Protocol: protocol.Version, Results: results}
	if err := c.post(ctx, "/api/v1/probe-agent/results", request, true, &acknowledgements); err != nil {
		return nil, err
	}
	if len(acknowledgements) != len(results) {
		return nil, errors.New("platform returned invalid acknowledgement count")
	}
	submitted := make(map[string]struct{}, len(results))
	for _, result := range results {
		submitted[result.TaskID] = struct{}{}
	}
	for _, acknowledgement := range acknowledgements {
		switch acknowledgement.Status {
		case protocol.AckAccepted, protocol.AckDuplicate, protocol.AckStale, protocol.AckRejected:
		default:
			return nil, errors.New("platform returned invalid acknowledgement status")
		}
		if _, ok := submitted[acknowledgement.TaskID]; !ok {
			return nil, errors.New("platform acknowledged an unknown task")
		}
		delete(submitted, acknowledgement.TaskID)
	}
	if len(submitted) != 0 {
		return nil, errors.New("platform omitted a task acknowledgement")
	}
	return acknowledgements, nil
}

func (c *Client) post(ctx context.Context, path string, body any, authenticated bool, response any) error {
	payload, err := json.Marshal(body)
	if err != nil {
		return fmt.Errorf("encode platform request: %w", err)
	}
	requestURL := *c.baseURL
	requestURL.Path += path
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, requestURL.String(), bytes.NewReader(payload))
	if err != nil {
		return fmt.Errorf("create platform request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")
	if authenticated {
		if c.runtimeToken == "" {
			return errors.New("runtime token is required")
		}
		req.Header.Set("Authorization", "Bearer "+c.runtimeToken)
	}

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return fmt.Errorf("platform request failed: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusUnauthorized {
		return ErrUnauthorized
	}
	if resp.StatusCode == http.StatusTooManyRequests || resp.StatusCode >= 500 {
		return &RetryError{StatusCode: resp.StatusCode, After: retryAfter(resp.Header, time.Now())}
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return &HTTPError{StatusCode: resp.StatusCode}
	}
	if response == nil {
		response = &struct{}{}
	}
	mediaType, _, err := mime.ParseMediaType(resp.Header.Get("Content-Type"))
	if err != nil || mediaType != "application/json" {
		return errors.New("platform response is not JSON")
	}
	limited := io.LimitReader(resp.Body, maxResponseBytes+1)
	data, err := io.ReadAll(limited)
	if err != nil {
		return fmt.Errorf("read platform response: %w", err)
	}
	if len(data) > maxResponseBytes {
		return errors.New("platform response exceeds size limit")
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(response); err != nil {
		return fmt.Errorf("decode platform response: %w", err)
	}
	var extra any
	if err := decoder.Decode(&extra); !errors.Is(err, io.EOF) {
		return errors.New("platform response contains multiple JSON values")
	}
	return nil
}
