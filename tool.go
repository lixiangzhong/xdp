package xdp

import (
	"syscall"
	"unsafe"

	"github.com/pkg/errors"
	"golang.org/x/sys/unix"
)

func xsk_get_mmap_offsets(fd int) (unix.XDPMmapOffsets, error) {
	var off unix.XDPMmapOffsets
	length := uint32(unsafe.Sizeof(off))
	ret, _, errno := unix.Syscall6(unix.SYS_GETSOCKOPT, uintptr(fd), unix.SOL_XDP, unix.XDP_MMAP_OFFSETS, uintptr(unsafe.Pointer(&off)), uintptr(unsafe.Pointer(&length)), 0)
	if ret != 0 {
		return off, errors.WithMessage(errno, "xsk_get_mmap_offsets")
	}
	return off, nil
}

func ioctl(fd int, req uint, arg uintptr) syscall.Errno {
	_, _, errno := syscall.Syscall(syscall.SYS_IOCTL, uintptr(fd), uintptr(req), arg)
	if errno != 0 {
		return errno
	}
	return 0
}

func get_nic_queues(ifname string) (cur int, max int, err error) {
	var ifr ifreq
	errno := ifr.SetIfrn(ifname)
	if errno != 0 {
		return 0, 0, errors.WithMessage(errno, "ifreq.SetIfrn")
	}
	var ch ethtoolchannels
	ch.cmd = unix.ETHTOOL_GCHANNELS
	ifr.ifru = uintptr(unsafe.Pointer(&ch))
	fd, err := syscall.Socket(unix.AF_INET, unix.SOCK_DGRAM, 0)
	if err != nil {
		return 0, 0, errors.WithMessage(errno, "get_nic_queues.Socket")
	}
	errno = ioctl(fd, unix.SIOCETHTOOL, uintptr(unsafe.Pointer(&ifr)))
	if errno > 0 && syscall.EOPNOTSUPP != errno {
		return 0, 0, errors.WithMessage(errno, "get_nic_queues.ioctl")
	}
	if errno > 0 || ch.maxcombined == 0 {
		return 1, 1, nil
	}
	return int(ch.combinedcount), int(ch.maxcombined), nil
}
