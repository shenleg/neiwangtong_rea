// 本文件校验本机默认产品 FEATURE 与单个发现对端是否有交集，不读取或推断远端组织配置。
package nativeproto

import "encoding/binary"

func sharedNodeFeature(local, remote NodePacket) bool {
	positions := []int{0x0c, 0xa9, 0xad, 0xb1, 0xb5, 0xb9, 0xbd, 0xc1, 0xc5, 0xc9}
	for _, left := range positions {
		value := binary.BigEndian.Uint32(local.raw[left:])
		if value == 0 {
			continue
		}
		for _, right := range positions {
			if value == binary.BigEndian.Uint32(remote.raw[right:]) {
				return true
			}
		}
	}
	return false
}
