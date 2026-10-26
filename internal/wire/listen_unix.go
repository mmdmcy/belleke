//go:build !windows

package wire

import (
	"context"
	"net"
	"syscall"

	"golang.org/x/sys/unix"
)

// listenBroadcast binds the discovery port with SO_REUSEADDR and SO_BROADCAST,
// so a second Belleke on the same machine (tests) can listen too.
func listenBroadcast(port int) (*net.UDPConn, error) {
	cfg := net.ListenConfig{Control: func(network, address string, c syscall.RawConn) error {
		var inner error
		err := c.Control(func(fd uintptr) {
			inner = unix.SetsockoptInt(int(fd), unix.SOL_SOCKET, unix.SO_REUSEADDR, 1)
			if inner == nil {
				inner = unix.SetsockoptInt(int(fd), unix.SOL_SOCKET, unix.SO_BROADCAST, 1)
			}
		})
		if err != nil {
			return err
		}
		return inner
	}}
	pc, err := cfg.ListenPacket(context.Background(), "udp4", net.JoinHostPort("0.0.0.0", itoa(port)))
	if err != nil {
		return nil, err
	}
	return pc.(*net.UDPConn), nil
}
