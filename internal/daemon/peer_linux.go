//go:build linux

package daemon

import (
	"net"
	"syscall"

	"golang.org/x/sys/unix"
)

func defaultPeerUID(c *net.UnixConn) (uint32, error) {
	var uid uint32
	raw, err := c.SyscallConn()
	if err != nil {
		return 0, err
	}
	var serr error
	err = raw.Control(func(fd uintptr) {
		cred, e := unix.GetsockoptUcred(int(fd), syscall.SOL_SOCKET, syscall.SO_PEERCRED)
		if e != nil {
			serr = e
			return
		}
		uid = uint32(cred.Uid)
	})
	if err != nil {
		return 0, err
	}
	return uid, serr
}
