package xdp

import (
	"math"
	"reflect"
	"sync"
	"syscall"
	"unsafe"

	"github.com/pkg/errors"
	"golang.org/x/sys/unix"
)

type Umem struct {
	fill     *xsk_ring_prod
	comp     *xsk_ring_cons
	data     []byte
	config   UmemConfig
	fd       int
	refCount int

	frameLock  sync.Mutex
	freeFrame  uint32
	framesAddr []uint64
}

type UmemConfig struct {
	FillSize      uint32
	CompSize      uint32
	Size          uint32
	FrameHeadroom uint32
	Flags         uint32
}

var defaultUmemConfig = UmemConfig{
	FillSize:      DEFAULT_FILL_SIZE,
	CompSize:      DEFAULT_COMP_SIZE,
	Size:          _DEFAULT_FRAME_SIZE * DEFAULT_FRAME_NUM, //16M
	FrameHeadroom: 0,
	Flags:         0,
}

func NewUmem(config *UmemConfig) (*Umem, error) {
	umem := new(Umem)
	if config == nil {
		config = &defaultUmemConfig
	}
	umem.config = *config
	var err error
	umem.fd, err = syscall.Socket(unix.AF_XDP, unix.SOCK_RAW, 0)
	if err != nil {
		return nil, err
	}
	umem.data = Posix_memalign(int(umem.config.Size))
	framenum := umem.config.Size / _DEFAULT_FRAME_SIZE
	umem.framesAddr = make([]uint64, framenum)
	for i := uint32(0); i < framenum; i++ {
		umem.putFrame(uint64(i) * uint64(_DEFAULT_FRAME_SIZE))
	}
	mr := unix.XDPUmemReg{
		Addr:     uint64(uintptr(unsafe.Pointer(&umem.data[0]))),
		Len:      uint64(len(umem.data)),
		Size:     _DEFAULT_FRAME_SIZE,
		Headroom: 0,
		Flags:    0,
	}
	_, _, errno := unix.Syscall6(syscall.SYS_SETSOCKOPT, uintptr(umem.fd),
		unix.SOL_XDP, unix.XDP_UMEM_REG,
		uintptr(unsafe.Pointer(&mr)), unsafe.Sizeof(mr), 0,
	)
	if errno != 0 {
		return nil, errors.WithMessage(errno, "XDP_UMEM_REG")
	}
	err = syscall.SetsockoptInt(umem.fd, unix.SOL_XDP, unix.XDP_UMEM_FILL_RING, int(umem.config.FillSize))
	if err != nil {
		return nil, err
	}
	err = unix.SetsockoptInt(umem.fd, unix.SOL_XDP, unix.XDP_UMEM_COMPLETION_RING, int(umem.config.CompSize))
	if err != nil {
		return nil, err
	}
	off, err := xsk_get_mmap_offsets(umem.fd)
	if err != nil {
		return nil, err
	}
	fillBuffer, err := syscall.Mmap(umem.fd, unix.XDP_UMEM_PGOFF_FILL_RING,
		int(off.Fr.Desc)+int(umem.config.FillSize*uint32(unsafe.Sizeof(uint64(0)))),
		syscall.PROT_READ|syscall.PROT_WRITE,
		syscall.MAP_SHARED|syscall.MAP_POPULATE,
	)
	if err != nil {
		return nil, err
	}
	umem.fill = new(xsk_ring_prod)
	umem.fill.Mask = umem.config.FillSize - 1
	umem.fill.Size = umem.config.FillSize
	umem.fill.Producer = (*uint32)(unsafe.Pointer(uintptr(unsafe.Pointer(&fillBuffer[0])) + uintptr(off.Fr.Producer)))
	umem.fill.Consumer = (*uint32)(unsafe.Pointer(uintptr(unsafe.Pointer(&fillBuffer[0])) + uintptr(off.Fr.Consumer)))
	umem.fill.Flags = (*uint32)(unsafe.Pointer(uintptr(unsafe.Pointer(&fillBuffer[0])) + uintptr(off.Fr.Flags)))
	umem.fill.CacheCons = umem.config.FillSize
	(*reflect.SliceHeader)(unsafe.Pointer(&umem.fill.Ring)).Data = uintptr(unsafe.Pointer(uintptr(unsafe.Pointer(&fillBuffer[0])) + uintptr(off.Fr.Desc)))
	(*reflect.SliceHeader)(unsafe.Pointer(&umem.fill.Ring)).Len = int(umem.config.FillSize)
	(*reflect.SliceHeader)(unsafe.Pointer(&umem.fill.Ring)).Cap = int(umem.config.FillSize)

	umem.fill_fr()

	compBuffer, err := syscall.Mmap(umem.fd, unix.XDP_UMEM_PGOFF_COMPLETION_RING,
		int(off.Cr.Desc)+int(umem.config.CompSize*uint32(unsafe.Sizeof(uint64(0)))),
		syscall.PROT_READ|syscall.PROT_WRITE,
		syscall.MAP_SHARED|syscall.MAP_POPULATE,
	)
	if err != nil {
		return nil, err
	}
	umem.comp = new(xsk_ring_cons)
	umem.comp.Mask = umem.config.CompSize - 1
	umem.comp.Size = umem.config.CompSize
	umem.comp.Producer = (*uint32)(unsafe.Pointer(uintptr(unsafe.Pointer(&compBuffer[0])) + uintptr(off.Cr.Producer)))
	umem.comp.Consumer = (*uint32)(unsafe.Pointer(uintptr(unsafe.Pointer(&compBuffer[0])) + uintptr(off.Cr.Consumer)))
	umem.comp.Flags = (*uint32)(unsafe.Pointer(uintptr(unsafe.Pointer(&compBuffer[0])) + uintptr(off.Cr.Flags)))
	(*reflect.SliceHeader)(unsafe.Pointer(&umem.comp.Ring)).Data = uintptr(unsafe.Pointer(uintptr(unsafe.Pointer(&compBuffer[0])) + uintptr(off.Cr.Desc)))
	(*reflect.SliceHeader)(unsafe.Pointer(&umem.comp.Ring)).Len = int(umem.config.CompSize)
	(*reflect.SliceHeader)(unsafe.Pointer(&umem.comp.Ring)).Cap = int(umem.config.CompSize)
	// go func() {
	// 	// runtime.LockOSThread()
	// 	for {
	// 		umem.fill_fr()
	// 		umem.cons_cr()
	// 	}
	// }()
	return umem, nil
}

func (u *Umem) getFrame() (uint64, bool) {
	u.frameLock.Lock()
	defer u.frameLock.Unlock()
	if u.freeFrame == 0 {
		// fmt.Println("Umem:No free frame")
		return math.MaxUint64, false
	}

	u.freeFrame--
	addr := u.framesAddr[u.freeFrame]
	u.framesAddr[u.freeFrame] = math.MaxUint64
	return addr, true
}

func (u *Umem) putFrame(addr uint64) {
	u.frameLock.Lock()
	defer u.frameLock.Unlock()
	u.framesAddr[u.freeFrame] = addr
	u.freeFrame++
}

// 填满fill ring
func (u *Umem) fill_fr() {
	n := u.fill.prod_nb_free(u.freeFrame)
	if n > 0 {
		var prod uint32
		for i := uint32(0); i < n; i++ {
			addr, ok := u.getFrame()
			if !ok {
				break
			}
			u.fill.fill_slot(addr)
			prod++
		}
		u.fill.submit_prod(prod)
	}
}

func (u *Umem) DescData(d unix.XDPDesc) []byte {
	return u.data[d.Addr : d.Addr+uint64(d.Len)]
}

// 消费comp ring
func (u *Umem) cons_cr() {
	addrs := u.comp.slots(u.config.CompSize)
	if n := uint32(len(addrs)); n > 0 {
		for _, addr := range addrs {
			u.putFrame(addr)
		}
		u.comp.submit_cons(n)
	}
}
