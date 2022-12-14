package xdp

/*
#include <stdlib.h>
#include <unistd.h>

void *aligned_mem_alloc(size_t size){
	void *packet_buffer;
 	int ret = posix_memalign(&packet_buffer, getpagesize(), size);
	if (ret){
		exit(ret);
	}
	return packet_buffer;
}
*/
import "C"
import (
	"reflect"
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

func GetNicQueues(ifname string) (cur int, max int, err error) {
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

func sendto(fd int) {
	syscall.Syscall6(syscall.SYS_SENDTO, uintptr(fd), uintptr(0), uintptr(0), uintptr(syscall.MSG_DONTWAIT), uintptr(0), uintptr(0))
}

func Posix_memalign(size int) []byte {
	ptr := C.aligned_mem_alloc(C.size_t(size))
	var b []byte
	(*reflect.SliceHeader)(unsafe.Pointer(&b)).Data = uintptr(ptr)
	(*reflect.SliceHeader)(unsafe.Pointer(&b)).Len = size
	(*reflect.SliceHeader)(unsafe.Pointer(&b)).Cap = size
	return b
}

func SetNicPromisc(ifname string, promisc bool) error {
	ifreq, err := unix.NewIfreq(ifname)
	if err != nil {
		return err
	}
	fd, err := syscall.Socket(unix.AF_INET, unix.SOCK_DGRAM, 0)
	if err != nil {
		return err
	}
	err = unix.IoctlIfreq(fd, unix.SIOCGIFFLAGS, ifreq)
	if err != nil {
		return err
	}
	flags := ifreq.Uint16()
	// on := flags&unix.IFF_PROMISC > 0
	// if on == promisc {
	// 	return nil
	// }
	if promisc {
		flags |= unix.IFF_PROMISC
	} else {
		flags &= ^uint16(unix.IFF_PROMISC)
	}
	ifreq.SetUint16(flags)
	err = unix.IoctlIfreq(fd, unix.SIOCSIFFLAGS, ifreq)
	if err != nil {
		return err
	}
	return nil
}
