package xdp

import (
	"reflect"
	"syscall"
	"unsafe"

	"github.com/pkg/errors"
	"golang.org/x/sys/unix"
)

type Socket struct {
	rx     *xsk_ring_rx
	tx     *xsk_ring_tx
	umem   *Umem
	config SocketConfig
	fd     int
}

type SocketConfig struct {
	RxSize    uint32
	TxSize    uint32
	XDPFlags  uint32 //unix.XDP_FLAGS_*
	BindFlags uint16 //unix.XDP_COPY unix.XDP_ZEROCOPY unix.XDP_SHARED_UMEM
	QueueID   int
}

func defaultSocketConfig() SocketConfig {
	return SocketConfig{
		RxSize:    DEFAULT_RX_SIZE,
		TxSize:    DEFAULT_TX_SIZE,
		XDPFlags:  0,
		BindFlags: 0,
		QueueID:   0,
	}
}

func NewSocket(ifindex int, umem *Umem, cfg *SocketConfig) (*Socket, error) {
	var err error
	if cfg == nil {
		*cfg = defaultSocketConfig()
	}
	if umem == nil {
		umem, err = NewUmem(nil)
		if err != nil {
			return nil, errors.WithMessage(err, "NewUmem")
		}
	}
	var socket Socket
	if umem.refCount > 0 {
		fd, err := syscall.Socket(unix.AF_XDP, unix.SOCK_RAW, 0)
		if err != nil {
			return nil, errors.WithMessage(err, "AF_XDP")
		}
		socket.fd = fd
	} else {
		socket.fd = umem.fd
	}
	socket.umem = umem
	socket.config = *cfg
	off, err := xsk_get_mmap_offsets(socket.fd)
	if err != nil {
		return nil, err
	}
	if socket.config.RxSize > 0 {
		err = syscall.SetsockoptInt(socket.fd, unix.SOL_XDP, unix.XDP_RX_RING, int(socket.config.RxSize))
		if err != nil {
			return nil, errors.WithMessage(err, "XDP_RX_RING")
		}
		b, err := syscall.Mmap(
			socket.fd,
			unix.XDP_PGOFF_RX_RING,
			int(off.Rx.Desc)+int(socket.config.RxSize*uint32(unsafe.Sizeof(unix.XDPDesc{}))),
			unix.PROT_READ|unix.PROT_WRITE,
			unix.MAP_SHARED|unix.MAP_POPULATE)
		if err != nil {
			return nil, errors.WithMessage(err, "Mmap RxRing")
		}
		rx := new(xsk_ring_rx)
		rx.Mask = socket.config.RxSize - 1
		rx.Size = socket.config.RxSize
		rx.Producer = (*uint32)(unsafe.Pointer(&b[off.Rx.Producer]))
		rx.Consumer = (*uint32)(unsafe.Pointer(&b[off.Rx.Consumer]))
		rx.Flags = (*uint32)(unsafe.Pointer(&b[off.Rx.Flags]))
		ring := (*reflect.SliceHeader)(unsafe.Pointer(&rx.Ring))
		ring.Data = uintptr(unsafe.Pointer(&b[off.Rx.Desc]))
		ring.Len = int(socket.config.RxSize)
		ring.Cap = ring.Len
		socket.rx = rx
	}
	if socket.config.TxSize > 0 {
		err = syscall.SetsockoptInt(socket.fd, unix.SOL_XDP, unix.XDP_TX_RING, int(socket.config.TxSize))
		if err != nil {
			return nil, errors.WithMessage(err, "XDP_TX_RING")
		}
		b, err := syscall.Mmap(
			socket.fd,
			unix.XDP_PGOFF_TX_RING,
			int(off.Tx.Desc)+int(socket.config.TxSize*uint32(unsafe.Sizeof(unix.XDPDesc{}))),
			unix.PROT_READ|unix.PROT_WRITE,
			unix.MAP_SHARED|unix.MAP_POPULATE)
		if err != nil {
			return nil, errors.WithMessage(err, "Mmap TxRing")
		}
		tx := new(xsk_ring_tx)
		tx.Mask = socket.config.TxSize - 1
		tx.Size = socket.config.TxSize
		tx.Producer = (*uint32)(unsafe.Pointer(&b[off.Tx.Producer]))
		tx.Consumer = (*uint32)(unsafe.Pointer(&b[off.Tx.Consumer]))
		tx.Flags = (*uint32)(unsafe.Pointer(&b[off.Tx.Flags]))
		ring := (*reflect.SliceHeader)(unsafe.Pointer(&tx.Ring))
		ring.Data = uintptr(unsafe.Pointer(&b[off.Tx.Desc]))
		ring.Len = int(socket.config.TxSize)
		ring.Cap = ring.Len
		socket.tx = tx
	}
	var sxdp = unix.SockaddrXDP{
		QueueID: uint32(socket.config.QueueID),
		Ifindex: uint32(ifindex),
	}
	if umem.refCount > 0 {
		sxdp.Flags = unix.XDP_SHARED_UMEM
		sxdp.SharedUmemFD = uint32(socket.fd)
	} else {
		sxdp.Flags = socket.config.BindFlags
	}
	err = unix.Bind(socket.fd, &sxdp)
	if err != nil {
		return nil, errors.WithMessage(err, "Bind")
	}
	umem.refCount++
	return &socket, nil
}
