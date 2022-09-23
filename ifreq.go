package xdp

import (
	"syscall"

	"golang.org/x/sys/unix"
)

type ethtoolchannels struct {
	cmd           uint32
	maxrx         uint32
	maxtx         uint32
	maxother      uint32
	maxcombined   uint32
	rxcount       uint32
	txcount       uint32
	othercount    uint32
	combinedcount uint32
}

type ifreq struct {
	ifrn [unix.IFNAMSIZ]byte
	ifru uintptr
}

func (i *ifreq) SetIfrn(name string) syscall.Errno {
	if len(name) >= unix.IFNAMSIZ {
		return syscall.EINVAL
	}
	copy(i.ifrn[:], name)
	return 0
}
