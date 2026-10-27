package protocol

import (
	"errors"
	"fmt"
)

// ProtocolVersion is the protocol version (N) this build speaks.
const ProtocolVersion = 1

// ErrUnsupportedVersion is returned when a peer asks for a protocol version
// that is neither N nor N-1.
var ErrUnsupportedVersion = errors.New("unsupported protocol version")

// Supported lists the protocol versions this build accepts, newest first.
// Version 0 never exists, so with N = 1 the list is {1}.
func Supported() []int {
	return supported(ProtocolVersion)
}

func supported(n int) []int {
	if n > 1 {
		return []int{n, n - 1}
	}
	return []int{n}
}

// Negotiate returns the version to use for an agent that announced
// requested in agent.hello. It accepts N and N-1 and rejects the rest.
func Negotiate(requested int) (int, error) {
	return negotiate(ProtocolVersion, requested)
}

func negotiate(n, requested int) (int, error) {
	for _, v := range supported(n) {
		if v == requested {
			return v, nil
		}
	}
	return 0, fmt.Errorf("%w: %d (supported %v)", ErrUnsupportedVersion, requested, supported(n))
}
