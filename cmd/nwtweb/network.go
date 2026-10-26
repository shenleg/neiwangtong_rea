// 本文件只读识别本机IPv4与真实子网掩码，Linux优先默认路由网卡；不向网络对端探测或广播。
package main

import (
	"bufio"
	"fmt"
	"io"
	"net"
	"net/netip"
	"os"
	"runtime"
	"strconv"
	"strings"
)

type interfaceNetwork struct {
	name   string
	prefix netip.Prefix
}

// resolveLocalNetwork 将省略的本机地址或掩码交给detect识别，显式CIDR和-prefix仍按原参数解析。
// detect读取本机配置，不应联网；无法识别、非法地址或冲突子网返回错误，不猜测掩码。
func resolveLocalNetwork(localText, prefixText string, detect func(netip.Addr) (netip.Addr, netip.Prefix, error)) (netip.Addr, netip.Prefix, error) {
	if localText == "" {
		local, prefix, err := detect(netip.Addr{})
		if err != nil {
			return netip.Addr{}, netip.Prefix{}, err
		}
		if prefixText != "" {
			return parseLocalNetwork(local.String(), prefixText)
		}
		return local, prefix, nil
	}
	if strings.Contains(localText, "/") || prefixText != "" {
		return parseLocalNetwork(localText, prefixText)
	}
	local, err := netip.ParseAddr(localText)
	if err != nil || !local.Is4() {
		return netip.Addr{}, netip.Prefix{}, fmt.Errorf("-local须为本机IPv4或IPv4/前缀长度")
	}
	return detect(local)
}

// detectLocalNetwork 读取正在运行的网卡IPv4及掩码；requested有效时仅查该IP，否则优先Linux默认路由网卡。
// 多个候选、没有可用局域网地址或系统读取失败时返回错误，操作者可用-local IPv4/CIDR明确覆盖。
func detectLocalNetwork(requested netip.Addr) (netip.Addr, netip.Prefix, error) {
	interfaces, err := net.Interfaces()
	if err != nil {
		return netip.Addr{}, netip.Prefix{}, fmt.Errorf("读取本机网卡: %w", err)
	}
	var networks []interfaceNetwork
	for _, iface := range interfaces {
		if iface.Flags&net.FlagUp == 0 {
			continue
		}
		addresses, err := iface.Addrs()
		if err != nil {
			return netip.Addr{}, netip.Prefix{}, fmt.Errorf("读取网卡%s的地址: %w", iface.Name, err)
		}
		for _, address := range addresses {
			prefix, err := netip.ParsePrefix(address.String())
			if err != nil {
				return netip.Addr{}, netip.Prefix{}, fmt.Errorf("解析网卡%s的地址: %w", iface.Name, err)
			}
			if prefix.Addr().Is4() {
				networks = append(networks, interfaceNetwork{name: iface.Name, prefix: prefix})
			}
		}
	}
	preferred := ""
	if !requested.IsValid() && runtime.GOOS == "linux" {
		data, err := os.ReadFile("/proc/net/route")
		if err != nil {
			return netip.Addr{}, netip.Prefix{}, fmt.Errorf("读取默认IPv4路由: %w", err)
		}
		preferred, err = defaultIPv4Interface(strings.NewReader(string(data)))
		if err != nil {
			return netip.Addr{}, netip.Prefix{}, err
		}
	}
	return selectInterfaceNetwork(networks, preferred, requested)
}

// selectInterfaceNetwork 从已启用网卡的IPv4中选择地址，保留主机位；没有优先候选时仅接受唯一私有IPv4。
// requested可明确选择回环等本机地址；无法唯一选择或没有匹配地址时报错，不按枚举顺序选取。
func selectInterfaceNetwork(networks []interfaceNetwork, preferred string, requested netip.Addr) (netip.Addr, netip.Prefix, error) {
	var candidates, preferredCandidates []netip.Prefix
	for _, network := range networks {
		if requested.IsValid() {
			if network.prefix.Addr() == requested {
				candidates = append(candidates, network.prefix)
			}
			continue
		}
		if !network.prefix.Addr().IsPrivate() || network.prefix.Bits() > 30 {
			continue
		}
		candidates = append(candidates, network.prefix)
		if network.name == preferred {
			preferredCandidates = append(preferredCandidates, network.prefix)
		}
	}
	if len(preferredCandidates) != 0 {
		candidates = preferredCandidates
	}
	unique := make(map[netip.Prefix]bool)
	for _, prefix := range candidates {
		unique[prefix] = true
	}
	if len(unique) == 0 {
		return netip.Addr{}, netip.Prefix{}, fmt.Errorf("没有找到匹配的本机局域网IPv4及掩码，请用-local IPv4/CIDR明确指定")
	}
	if len(unique) != 1 {
		return netip.Addr{}, netip.Prefix{}, fmt.Errorf("存在多个可用IPv4或不同掩码，无法自动选择，请用-local IPv4/CIDR明确指定")
	}
	prefix := candidates[0] // 唯一性已验证，重复的同一IP/掩码不影响选择结果。
	return prefix.Addr(), prefix.Masked(), nil
}

// defaultIPv4Interface 解析Linux路由表，选择已启用且metric最低的默认路由网卡；无默认路由返回空名称。
// 多个等优先级网卡或格式损坏返回错误，源只读，不发任何网络请求。
func defaultIPv4Interface(source io.Reader) (string, error) {
	const routeUp = 0x1 // Linux RTF_UP：未启用的默认路由不能决定发现网卡。
	scanner := bufio.NewScanner(source)
	name, metric := "", uint64(0)
	ambiguous := false
	for scanner.Scan() {
		fields := strings.Fields(scanner.Text())
		if len(fields) == 0 || fields[0] == "Iface" {
			continue
		}
		if len(fields) < 8 {
			return "", fmt.Errorf("默认IPv4路由表字段不完整")
		}
		if fields[1] != "00000000" || fields[7] != "00000000" {
			continue
		}
		flags, err := strconv.ParseUint(fields[3], 16, 32)
		if err != nil {
			return "", fmt.Errorf("默认IPv4路由标志无效: %w", err)
		}
		if flags&routeUp == 0 {
			continue
		}
		value, err := strconv.ParseUint(fields[6], 10, 64)
		if err != nil {
			return "", fmt.Errorf("默认IPv4路由metric无效: %w", err)
		}
		if name == "" || value < metric {
			name, metric, ambiguous = fields[0], value, false
		} else if value == metric && fields[0] != name {
			ambiguous = true
		}
	}
	if err := scanner.Err(); err != nil {
		return "", fmt.Errorf("读取默认IPv4路由表: %w", err)
	}
	if ambiguous {
		return "", fmt.Errorf("存在多个等优先级默认IPv4路由，请用-local IPv4/CIDR明确指定")
	}
	return name, nil
}
