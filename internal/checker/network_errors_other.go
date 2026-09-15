//go:build !windows

package checker

func platformNetworkUnavailable(error) bool { return false }
