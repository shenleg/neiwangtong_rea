// 本文件验证独立Web节点的显式网络配置，并读取属于该身份的联系人快照；不访问Windows客户端资料。
package nativeweb

import (
	"errors"
	"fmt"
	"net/netip"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"neiwangtong-gateway/internal/nativeproto"
)

// Config 指定独立节点的身份目录、本机公告及发现方式；默认单对端，Discover显式启用本子网广播和多对端。
type Config struct {
	Local           netip.Addr
	Prefix          netip.Prefix
	Remote          netip.AddrPort
	Name            string
	DataDir         string
	DiscoveryPort   uint16
	ENetPort        uint16
	Discover        bool
	SessionDuration time.Duration
}

// Validate 只读校验配置与本机资料；返回参数错误，不创建文件、socket或身份。
func (c Config) Validate() error {
	if !unicast(c.Local) || !c.Prefix.IsValid() || !c.Prefix.Addr().Is4() || !c.Prefix.Contains(c.Local) || c.Prefix.Bits() > 30 {
		return fmt.Errorf("须提供明确本机IPv4和包含它且不窄于/30的子网")
	}
	if c.Remote.IsValid() {
		if !unicast(c.Remote.Addr()) || c.Remote.Port() == 0 || c.Remote.Addr() == c.Local || c.Remote.Addr() == broadcast(c.Prefix) || c.Discover && !c.Prefix.Contains(c.Remote.Addr()) {
			return fmt.Errorf("指定发现对端须为不同于本机的IPv4单播地址")
		}
	} else if !c.Discover {
		return fmt.Errorf("未启用局域网发现时须指定唯一IPv4单播发现对端")
	}
	if c.SessionDuration < 0 {
		return fmt.Errorf("会话时限不得为负；0表示常驻")
	}
	if c.DiscoveryPort == 0 || c.ENetPort == 0 || c.DiscoveryPort == c.ENetPort {
		return fmt.Errorf("发现与ENet端口必须非零且不同")
	}
	if !filepath.IsAbs(c.DataDir) {
		return fmt.Errorf("数据目录必须为绝对路径")
	}
	return validateDisplayName(c.Name)
}

// ErrInvalidName 表示上线名称为空、包含控制字符、非法UTF-8或不符合原生资料编码，调用方应修改输入而不是重试联网。
var ErrInvalidName = errors.New("本机名称无效")

func validateDisplayName(name string) error {
	if !utf8.ValidString(name) || strings.TrimSpace(name) == "" || strings.IndexFunc(name, unicode.IsControl) >= 0 {
		return fmt.Errorf("%w：名称必须非空且不能包含换行、控制字符或非法UTF-8", ErrInvalidName)
	}
	if _, err := nativeproto.ProbeHandshake(name); err != nil {
		return fmt.Errorf("%w：%v", ErrInvalidName, err)
	}
	return nil
}

func unicast(address netip.Addr) bool {
	return address.IsValid() && address.Is4() && !address.IsUnspecified() && !address.IsMulticast() && address != netip.MustParseAddr("255.255.255.255")
}

// broadcast 只计算节点公告字段，不产生广播网络操作。
func broadcast(prefix netip.Prefix) netip.Addr {
	value := prefix.Masked().Addr().As4()
	for bit := prefix.Bits(); bit < 32; bit++ {
		value[bit/8] |= 1 << uint(7-bit%8)
	}
	return netip.AddrFrom4(value)
}

// loadDirectory 读取最近一次成功保存的本身份联系人快照；损坏时直接报错，不退回旧文件或忽略错误。
func loadDirectory(dataDir, ownID string) (*nativeproto.Directory, error) {
	paths, err := filepath.Glob(filepath.Join(dataDir, "directory-*.json"))
	if err != nil {
		return nil, err
	}
	if len(paths) == 0 {
		return nativeproto.NewDirectory(ownID, nil)
	}
	sort.Strings(paths)
	file, err := os.Open(paths[len(paths)-1])
	if err != nil {
		return nil, err
	}
	directory, readErr := nativeproto.LoadDirectory(file)
	if err := errors.Join(readErr, file.Close()); err != nil {
		return nil, err
	}
	if directory.OwnUserID() != ownID {
		return nil, fmt.Errorf("联系人快照不属于当前独立身份")
	}
	return directory, nil
}
