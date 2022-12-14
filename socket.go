package xdp

import (
	"log"
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
	BindFlags uint16 //unix.XDP_COPY unix.XDP_ZEROCOPY unix.XDP_SHARED_UMEM unix.XDP_USE_NEED_WAKEUP
	QueueID   int
	Poll      bool
}

var defaultSocketConfig = SocketConfig{
	RxSize:    DEFAULT_RX_SIZE,
	TxSize:    DEFAULT_TX_SIZE,
	BindFlags: 0,
	QueueID:   0,
}

func NewSocket(ifindex int, umem *Umem, cfg *SocketConfig) (*Socket, error) {
	var err error
	if cfg == nil {
		cfg = &defaultSocketConfig
	}
	if umem == nil {
		umem, err = NewUmem(nil)
		if err != nil {
			return nil, errors.WithMessage(err, "NewUmem")
		}
	}
	var socket Socket
	if umem.refCount > 0 {
		fd, err := syscall.Socket(unix.AF_XDP, unix.SOCK_RAW|unix.SOCK_CLOEXEC, 0)
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
		sxdp.Flags |= unix.XDP_SHARED_UMEM
		sxdp.SharedUmemFD = uint32(umem.fd)
	} else {
		sxdp.Flags = socket.config.BindFlags
	}
	if socket.config.Poll {
		sxdp.Flags |= unix.XDP_USE_NEED_WAKEUP
	}
	err = unix.Bind(socket.fd, &sxdp)
	if err != nil {
		log.Println(int(err.(syscall.Errno)))
		return nil, errors.WithMessage(err, "Bind")
	}
	umem.refCount++
	return &socket, nil
}

// HandleRecv  handler 返回false时表示已将此frame直接放入Tx队列,不回收frame
func (s *Socket) HandleRecv(handler func(unix.XDPDesc, []byte) bool) {
	fds := make([]unix.PollFd, 1)
	fds[0].Fd = int32(s.fd)
	fds[0].Events = unix.POLLIN
	for {
		if s.config.Poll {
			unix.Poll(fds, -1)
		}
		descs := s.rx.slots(s.rx.Size)
		s.umem.fill_fr()
		for i := range descs {
			data := s.umem.DescData(descs[i])
			if handler(descs[i], data) {
				s.umem.putFrame(descs[i].Addr)
			}
		}
		s.rx.submit_cons(uint32(len(descs)))
	}
}

func (s *Socket) write(b []byte) bool {
	addr, ok := s.umem.getFrame()
	if !ok {
		return false
	}
	desc := unix.XDPDesc{Addr: addr, Len: _DEFAULT_FRAME_SIZE}
	frame := s.umem.DescData(desc)
	n := copy(frame, b)
	desc.Len = uint32(n)
	s.tx.fill_slot(desc)
	return true
}

func (s *Socket) Write(bs ...[]byte) uint32 {
	s.umem.cons_cr()
	var n uint32
	for _, b := range bs {
		if s.write(b) {
			n++
		}
	}
	if n > 0 {
		s.tx.submit_prod(n)
		sendto(s.fd)
	}
	return n
}

func (s *Socket) FD() int {
	return s.fd
}

// WriteDesc 包已写入umem ,直接把desc加入tx队列发送
func (s *Socket) WriteDesc(d unix.XDPDesc) {
	s.tx.fill_slot(d)
	s.tx.submit_prod(1)
	sendto(s.fd)
}

func (s *Socket) Stats() (unix.XDPStatistics, error) {
	var stats unix.XDPStatistics
	_, _, errno := syscall.Syscall6(unix.SYS_GETSOCKOPT, uintptr(s.fd), unix.SOL_XDP, unix.XDP_STATISTICS, uintptr(unsafe.Pointer(&stats)), 48, 0)
	if errno != 0 {
		return stats, errno
	}
	return stats, nil
}
