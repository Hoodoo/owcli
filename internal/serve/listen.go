package serve

import (
	"errors"
	"fmt"
	"net"
	"syscall"
)

// Listen opens a loopback listener on port, trying the next ports when one
// is taken. The viewer is never reachable from the network.
func Listen(port int) (net.Listener, error) {
	const tries = 20
	for i := 0; i < tries; i++ {
		l, err := net.Listen("tcp", fmt.Sprintf("127.0.0.1:%d", port+i))
		if err == nil {
			return l, nil
		}
		if !errors.Is(err, syscall.EADDRINUSE) {
			return nil, err
		}
	}
	return nil, fmt.Errorf("ports %d-%d are all in use; pick another with --port", port, port+tries-1)
}
