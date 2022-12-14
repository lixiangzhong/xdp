package xdp

import (
	"syscall"

	"golang.org/x/sys/unix"
)

/*
	union
	{
		char	ifrn_name[IFNAMSIZ];
	} ifr_ifrn;

	union {
		struct	sockaddr ifru_addr;
		struct	sockaddr ifru_dstaddr;
		struct	sockaddr ifru_broadaddr;
		struct	sockaddr ifru_netmask;
		struct  sockaddr ifru_hwaddr;
		short	ifru_flags;
		int	ifru_ivalue;
		int	ifru_mtu;
		struct  ifmap ifru_map;
		char	ifru_slave[IFNAMSIZ];	// Just fits the size
		char	ifru_newname[IFNAMSIZ];
		void __user *	ifru_data;
		struct	if_settings ifru_settings;
	} ifr_ifru;
*/

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
