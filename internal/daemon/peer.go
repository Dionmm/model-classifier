package daemon

import (
	"net"
	"os"
	"sync/atomic"
)

type PeerUIDGetter func(*net.UnixConn) (uint32, error)

type UIDListener struct {
	*net.UnixListener
	UID      uint32
	GetUID   PeerUIDGetter
	Rejected atomic.Uint64
}

func (l *UIDListener) Accept() (net.Conn, error) {
	for {
		c, err := l.UnixListener.Accept()
		if err != nil {
			return nil, err
		}
		uc, ok := c.(*net.UnixConn)
		if !ok {
			_ = c.Close()
			l.Rejected.Add(1)
			continue
		}
		get := l.GetUID
		if get == nil {
			get = defaultPeerUID
		}
		uid, err := get(uc)
		if err != nil || uid != l.UID {
			_ = c.Close()
			l.Rejected.Add(1)
			continue
		}
		return c, nil
	}
}

func currentUID() uint32 { return uint32(os.Getuid()) }
