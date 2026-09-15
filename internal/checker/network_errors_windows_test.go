package checker

import (
	"testing"

	"github.com/Bend-Function/MasterDNS-Agent/internal/protocol"
	"golang.org/x/sys/windows"
)

func TestWrappedWinsockErrors(t *testing.T) {
	for _, errno := range []error{windows.WSAENETUNREACH, windows.WSAEAFNOSUPPORT, windows.WSAEADDRNOTAVAIL} {
		checkNetworkError(t, errno, protocol.OutcomeUnavailable, "network_unavailable")
	}
	for _, errno := range []error{windows.WSAEHOSTUNREACH, windows.WSAECONNREFUSED, windows.WSAETIMEDOUT} {
		checkNetworkError(t, errno, protocol.OutcomeFailure, "")
	}
}
