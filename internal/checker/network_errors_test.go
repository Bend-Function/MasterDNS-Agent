package checker

import (
	"context"
	"net"
	"os"
	"syscall"
	"testing"

	"github.com/Bend-Function/MasterDNS-Agent/internal/protocol"
)

func TestWrappedNetworkErrors(t *testing.T) {
	for _, errno := range []error{syscall.ENETUNREACH, syscall.EAFNOSUPPORT, syscall.EADDRNOTAVAIL} {
		checkNetworkError(t, errno, protocol.OutcomeUnavailable, "network_unavailable")
	}
	for _, errno := range []error{syscall.EHOSTUNREACH, syscall.ECONNREFUSED, context.DeadlineExceeded} {
		checkNetworkError(t, errno, protocol.OutcomeFailure, "")
	}
}

func checkNetworkError(t *testing.T, errno error, outcome, code string) {
	t.Helper()
	for _, kind := range []string{"tcp", "http"} {
		t.Run(kind+"/"+errno.Error(), func(t *testing.T) {
			checker := mustChecker(t, true, false, func(context.Context, string, string) (net.Conn, error) {
				return nil, &net.OpError{Op: "dial", Net: "tcp4", Err: os.NewSyscallError("connectex", errno)}
			})
			task := tcpTask("192.0.2.35", 4, 80)
			if kind == "http" {
				task = httpTask("192.0.2.35", 4, 80)
			}
			got := checker.Check(context.Background(), task)
			wantCode := code
			if wantCode == "" {
				wantCode = kind + "_failed"
			}
			if got.Outcome != outcome || got.ErrorCode != wantCode {
				t.Fatalf("Check() outcome=%s code=%s, want %s %s", got.Outcome, got.ErrorCode, outcome, wantCode)
			}
		})
	}
}
