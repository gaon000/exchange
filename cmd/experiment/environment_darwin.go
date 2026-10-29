//go:build darwin

package main

import (
	"fmt"
	"syscall"
)

type storageInfo struct {
	Type           string `json:"type"`
	BlockBytes     uint64 `json:"blockBytes,string"`
	AvailableBytes uint64 `json:"availableBytes,string"`
	Local          bool   `json:"local"`
}

func readStorage(dir string) (storageInfo, error) {
	var s syscall.Statfs_t
	if err := syscall.Statfs(dir, &s); err != nil {
		return storageInfo{}, err
	}
	b := make([]byte, 0, len(s.Fstypename))
	for _, v := range s.Fstypename {
		if v == 0 {
			break
		}
		b = append(b, byte(v))
	}
	// Darwin sys/mount.h MNT_LOCAL; not exported by Go's frozen syscall package.
	const mountLocal = 0x00001000
	r := storageInfo{string(b), uint64(s.Bsize), s.Bavail * uint64(s.Bsize), s.Flags&mountLocal != 0}
	if r.Type != "apfs" || !r.Local {
		return r, fmt.Errorf("experiment requires local APFS")
	}
	return r, nil
}
