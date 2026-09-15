package checker

import (
	"errors"

	"golang.org/x/sys/windows"
)

// Winsock errors retain their Windows codes through net.OpError and
// os.SyscallError; syscall.E* values on Windows are synthetic application errors.
func platformNetworkUnavailable(err error) bool {
	return errors.Is(err, windows.WSAENETUNREACH) ||
		errors.Is(err, windows.WSAEAFNOSUPPORT) || errors.Is(err, windows.WSAEADDRNOTAVAIL)
}
