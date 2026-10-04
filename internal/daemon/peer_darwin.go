//go:build darwin

package daemon

import (
	"net"

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
		cred, e := unix.GetsockoptXucred(int(fd), unix.SOL_LOCAL, unix.LOCAL_PEERCRED)
		if e != nil {
			serr = e
			return
		}
		uid = cred.Uid
	})
	if err != nil {
		return 0, err
	}
	return uid, serr
}
