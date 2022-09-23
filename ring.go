package xdp

import (
	"sync/atomic"

	"golang.org/x/sys/unix"
)

type xsk_ring_prod xsk_ring[uint64]
type xsk_ring_cons xsk_ring[uint64]
type xsk_ring_rx xsk_ring[unix.XDPDesc]
type xsk_ring_tx xsk_ring[unix.XDPDesc]

type xsk_ring[T any] struct {
	CachedProd uint32
	CacheCons  uint32
	Mask       uint32
	Size       uint32
	Producer   *uint32
	Consumer   *uint32
	Flags      *uint32
	Ring       []T
}

// prod_nb_free  ring 中有多少个空闲的 slot 可供填充
func (x *xsk_ring_prod) prod_nb_free(n uint32) uint32 {
	free := x.CacheCons - x.CachedProd
	if free >= n {
		return free
	}
	x.CacheCons = atomic.LoadUint32(x.Consumer)
	x.CacheCons += x.Size
	return x.CacheCons - x.CachedProd
}

func (x *xsk_ring_prod) fill_addr(addr uint64) {
	x.Ring[x.CachedProd&x.Mask] = addr
	x.CachedProd++
}

func (x *xsk_ring_prod) submit_prod(n uint32) {
	x.CachedProd = atomic.AddUint32(x.Producer, n)
}

// cons_nb_avail ring 中有多少个 slot 可供消费
func (x *xsk_ring_rx) cons_nb_avail(max uint32) uint32 {
	n := x.CachedProd - x.CacheCons
	if n == 0 {
		x.CachedProd = atomic.LoadUint32(x.Producer)
		n = x.CachedProd - x.CacheCons
	}
	if n > max {
		return max
	}
	return n
}

// descs  返回 ring 中的可消费的 slot
func (x *xsk_ring_rx) descs(max uint32) []unix.XDPDesc {
	n := x.cons_nb_avail(max)
	descs := make([]unix.XDPDesc, n)
	for i := uint32(0); i < n; i++ {
		descs[i] = x.Ring[x.CacheCons&x.Mask]
		x.CacheCons++
	}
	return descs
}

// submit_cons 完成消费个数
func (x *xsk_ring_rx) submit_cons(n uint32) {
	x.CacheCons = atomic.AddUint32(x.Consumer, n)
}
