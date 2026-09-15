package checker

import (
	"bytes"
	"context"
	"crypto/tls"
	"errors"
	"io"
	"net"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"time"

	"github.com/Bend-Function/MasterDNS-Agent/internal/protocol"
)

const maxResponseBody = 1 << 20

var (
	errRedirectForbidden = errors.New("redirect changes configured origin")
	errTooManyRedirects  = errors.New("more than five redirects")
)

func (c *Checker) checkHTTP(ctx context.Context, task protocol.Task, result protocol.Result, started time.Time) protocol.Result {
	config := task.Config
	hostname := config.Hostname
	if hostname == "" {
		hostname = task.Hostname
	}
	scheme := config.Protocol
	if scheme == "" {
		scheme = "https"
	}
	port := config.Port
	if port == 0 {
		port = 80
		if scheme == "https" {
			port = 443
		}
	}

	deadline := started.Add(httpTimeout(config.TimeoutMS))
	if task.Deadline.Before(deadline) {
		deadline = task.Deadline
	}
	checkCtx, cancel := context.WithDeadline(ctx, deadline)
	defer cancel()

	requestURL := &url.URL{Scheme: scheme, Host: net.JoinHostPort(hostname, strconv.Itoa(port))}
	path := config.Path
	if path == "" {
		path = "/"
	}
	parsedPath, err := url.ParseRequestURI(path)
	if err != nil {
		return httpError(result, started, protocol.OutcomeUnavailable, "invalid_task")
	}
	requestURL.Path, requestURL.RawPath, requestURL.RawQuery = parsedPath.Path, parsedPath.RawPath, parsedPath.RawQuery
	method := config.Method
	if method == "" {
		method = http.MethodGet
	}
	request, err := http.NewRequestWithContext(checkCtx, method, requestURL.String(), nil)
	if err != nil {
		return httpError(result, started, protocol.OutcomeUnavailable, "invalid_task")
	}
	for name, value := range config.Headers {
		request.Header.Set(name, value)
	}
	request.Host = hostname

	network := "tcp4"
	if task.Family == 6 {
		network = "tcp6"
	}
	target := net.JoinHostPort(task.Address, strconv.Itoa(port))
	transport := &http.Transport{
		DialContext: func(dialCtx context.Context, _, _ string) (net.Conn, error) {
			return c.dial(dialCtx, network, target)
		},
		DisableKeepAlives: true,
		TLSClientConfig: &tls.Config{
			ServerName: hostname,
			RootCAs:    c.rootCAs,
			// This is controlled by the task's explicit verifyTls setting.
			InsecureSkipVerify: !config.VerifyTLS, //nolint:gosec
		},
	}
	defer transport.CloseIdleConnections()
	client := &http.Client{Transport: transport}
	client.CheckRedirect = redirectPolicy(config.FollowRedirects, scheme, hostname, port)

	response, err := client.Do(request)
	if err != nil {
		if errors.Is(err, errRedirectForbidden) {
			return httpError(result, started, protocol.OutcomeFailure, "redirect_forbidden")
		}
		if errors.Is(err, errTooManyRedirects) {
			return httpError(result, started, protocol.OutcomeFailure, "too_many_redirects")
		}
		if ctx.Err() != nil && errors.Is(ctx.Err(), context.Canceled) {
			return httpError(result, started, protocol.OutcomeUnavailable, "check_canceled")
		}
		if localNetworkUnavailable(err) {
			return httpError(result, started, protocol.OutcomeUnavailable, "network_unavailable")
		}
		var certificateError *tls.CertificateVerificationError
		if errors.As(err, &certificateError) {
			return httpError(result, started, protocol.OutcomeFailure, "tls_failed")
		}
		return httpError(result, started, protocol.OutcomeFailure, "http_failed")
	}
	defer response.Body.Close()
	result.StatusCode = response.StatusCode
	result.LatencyMS = latencyMilliseconds(time.Since(started))
	if !expectedStatus(config, response.StatusCode) {
		result.Outcome = protocol.OutcomeFailure
		result.ErrorCode = "unexpected_status"
		return result
	}

	if config.BodyContains != "" || config.BodyPattern != "" {
		body, readErr := io.ReadAll(io.LimitReader(response.Body, maxResponseBody+1))
		result.LatencyMS = latencyMilliseconds(time.Since(started))
		if readErr != nil {
			if ctx.Err() != nil && errors.Is(ctx.Err(), context.Canceled) {
				result.Outcome = protocol.OutcomeUnavailable
				result.ErrorCode = "check_canceled"
				return result
			}
			result.Outcome = protocol.OutcomeFailure
			result.ErrorCode = "http_failed"
			return result
		}
		if len(body) > maxResponseBody {
			result.Outcome = protocol.OutcomeFailure
			result.ErrorCode = "body_limit_exceeded"
			return result
		}
		if config.BodyContains != "" && !bytes.Contains(body, []byte(config.BodyContains)) {
			result.Outcome = protocol.OutcomeFailure
			result.ErrorCode = "body_mismatch"
			return result
		}
		if config.BodyPattern != "" {
			pattern, compileErr := regexp.Compile(config.BodyPattern)
			if compileErr != nil {
				result.Outcome = protocol.OutcomeUnavailable
				result.ErrorCode = "invalid_pattern"
				return result
			}
			if !pattern.Match(body) {
				result.Outcome = protocol.OutcomeFailure
				result.ErrorCode = "body_mismatch"
				return result
			}
		}
	}

	result.Outcome = protocol.OutcomeSuccess
	return result
}

func httpTimeout(milliseconds int) time.Duration {
	if milliseconds <= 0 {
		return 3 * time.Second
	}
	return time.Duration(milliseconds) * time.Millisecond
}

func httpError(result protocol.Result, started time.Time, outcome, code string) protocol.Result {
	result.LatencyMS = latencyMilliseconds(time.Since(started))
	result.Outcome = outcome
	result.ErrorCode = code
	return result
}

func expectedStatus(config protocol.CheckConfig, status int) bool {
	if len(config.ExpectedStatuses) > 0 {
		for _, expected := range config.ExpectedStatuses {
			if status == expected {
				return true
			}
		}
		return false
	}
	minimum, maximum := config.ExpectedStatusMin, config.ExpectedStatusMax
	if minimum == 0 {
		minimum = 200
	}
	if maximum == 0 {
		maximum = 399
	}
	return status >= minimum && status <= maximum
}

func redirectPolicy(follow bool, scheme, hostname string, port int) func(*http.Request, []*http.Request) error {
	return func(request *http.Request, via []*http.Request) error {
		if !follow {
			return http.ErrUseLastResponse
		}
		if len(via) > 5 {
			return errTooManyRedirects
		}
		redirectPort := request.URL.Port()
		if redirectPort == "" {
			redirectPort = "80"
			if request.URL.Scheme == "https" {
				redirectPort = "443"
			}
		}
		if request.URL.Scheme != scheme || request.URL.Hostname() != hostname || redirectPort != strconv.Itoa(port) {
			return errRedirectForbidden
		}
		request.Host = hostname
		return nil
	}
}
