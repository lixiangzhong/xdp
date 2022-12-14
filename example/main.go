package main

import (
	"flag"
	"fmt"
	"log"
	"net"
	"sync/atomic"

	"github.com/lixiangzhong/xdp"

	"github.com/rcrowley/go-metrics"

	"github.com/cilium/ebpf"
	"github.com/cilium/ebpf/asm"
	"github.com/cilium/ebpf/features"
	"github.com/cilium/ebpf/link"
	"github.com/cilium/ebpf/rlimit"
	"github.com/google/gopacket"
	"github.com/google/gopacket/layers"
	"golang.org/x/sys/unix"
)

var ifname string
var XDPGenericMode bool
var umemsize uint64
var bpf bpfObjects

var samplesIN = metrics.NewRegistry()
var samplesOUT = metrics.NewRegistry()

func init() {
	flag.StringVar(&ifname, "i", "", "")
	flag.BoolVar(&XDPGenericMode, "g", false, "")
	flag.Uint64Var(&umemsize, "s", 16, "-s 16 表示每网卡队列分配16M内存")
	flag.Parse()
	log.SetFlags(log.Lshortfile | log.LstdFlags)

	err := rlimit.RemoveMemlock()
	if err != nil {
		log.Fatal(err)
	}

	// log.Println(features.HaveBoundedLoops())
	// log.Println(features.HaveLargeInstructions())
	log.Println("Support XDP", features.HaveProgType(ebpf.XDP) == nil)
	log.Println("Support XSKMap", features.HaveMapType(ebpf.XSKMap) == nil)
	log.Println("Support bpf_redirect_map", features.HaveProgramHelper(ebpf.XDP, asm.FnRedirectMap) == nil)

}

//go:generate go run github.com/cilium/ebpf/cmd/bpf2go -cc clang-14 -cflags "-O2 -g -Wall -Werror"  bpf af_xdp_kern.c
func main() {
	iface, err := net.InterfaceByName(ifname)
	if err != nil {
		log.Fatal(err)
	}
	err = xdp.SetNicPromisc(ifname, true)
	if err != nil {
		log.Fatal(err)
	}
	defer xdp.SetNicPromisc(ifname, false)

	if err := loadBpfObjects(&bpf, nil); err != nil {
		log.Fatalf("loading objects: %s", err)
	}
	defer bpf.Close()
	// Attach the program.
	l, err := link.AttachXDP(link.XDPOptions{
		Program:   bpf.XdpSockProg,
		Interface: iface.Index,
		// Flags:     unix.XDP_FLAGS_UPDATE_IF_NOEXIST,
	})
	if err != nil {
		log.Fatalf("could not attach XDP program: %s", err)
	}
	defer l.Close()
	log.Printf("Attached XDP program to iface %s\n", iface.Name)
	log.Printf("Press Ctrl-C to exit and remove the program\n")

	n, _, err := xdp.GetNicQueues(ifname)
	if err != nil {
		log.Fatal(err)
	}
	log.Printf("iface (%s) queues :%v", ifname, n)
	for queueid := 0; queueid < n; queueid++ {
		go createsocket(iface.Index, queueid)
	}
	select {}
}

type InOut struct {
	In  uint64
	Out uint64
}

var pkts uint64
var bytesin uint64

func createsocket(ifidx int, queueid int) {
	umem, err := xdp.NewUmem(&xdp.UmemConfig{
		FillSize: xdp.DEFAULT_FILL_SIZE,
		CompSize: xdp.DEFAULT_COMP_SIZE,
		Size:     uint32(umemsize << 20),
	})
	if err != nil {
		log.Fatal(err)
	}
	log.Println(umemsize << 20)
	xsk, err := xdp.NewSocket(ifidx, umem, &xdp.SocketConfig{
		RxSize:    xdp.DEFAULT_RX_SIZE,
		TxSize:    xdp.DEFAULT_TX_SIZE,
		QueueID:   queueid,
		BindFlags: unix.XDP_ZEROCOPY,
		Poll:      true,
	})
	if err != nil {
		log.Fatal(err)
	}

	err = bpf.XsksMap.Put(uint32(queueid), uint32(xsk.FD()))
	if err != nil {
		log.Fatal(err)
	}
	opt := gopacket.DecodeOptions{NoCopy: true, Lazy: true}
	xsk.HandleRecv(func(x unix.XDPDesc, b []byte) bool {
		atomic.AddUint64(&pkts, 1)
		atomic.AddUint64(&bytesin, uint64(x.Len))
		p := gopacket.NewPacket(b, layers.LayerTypeEthernet, opt)
		l := p.NetworkLayer()
		if l != nil {
			fmt.Println(l)
		}
		return true
	})
}

func FormatBps(bytes uint64) string {
	bps := bytes * 8
	if bps < 1<<10 {
		return fmt.Sprintf("%v bps", bps)
	}
	if bps < 1<<20 {
		return fmt.Sprintf("%v kbps", bps>>10)
	}
	if bps < 1<<30 {
		return fmt.Sprintf("%v mbps", bps>>20)
	}
	return fmt.Sprintf("%v gbps", bps>>30)
}
