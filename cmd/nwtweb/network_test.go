// 本文件用合成网卡和Linux路由表验证自动识别、真实掩码及手动覆盖，不读取本机资料或建立socket。
package main

import (
	"errors"
	"net/netip"
	"strings"
	"testing"
)

// TestDefaultIPv4Interface 验证按最低metric选择已启用默认路由，同优先级不同网卡拒绝，不按表中顺序决定。
func TestDefaultIPv4Interface(t *testing.T) {
	const header = "Iface Destination Gateway Flags RefCnt Use Metric Mask MTU Window IRTT\n"
	for _, tc := range []struct {
		name, rows, want string
		invalid          bool
	}{
		{"none", "eth0 0000010A 00000000 0001 0 0 0 00FFFFFF 0 0 0\n", "", false},
		{"metric", "eth1 00000000 0100010A 0003 0 0 200 00000000 0 0 0\neth0 00000000 0100010A 0003 0 0 100 00000000 0 0 0\n", "eth0", false},
		{"disabled", "eth1 00000000 00000000 0000 0 0 0 00000000 0 0 0\neth0 00000000 00000000 0001 0 0 100 00000000 0 0 0\n", "eth0", false},
		{"same-interface", "eth0 00000000 00000000 0001 0 0 100 00000000 0 0 0\neth0 00000000 00000000 0001 0 0 100 00000000 0 0 0\n", "eth0", false},
		{"ambiguous", "eth0 00000000 00000000 0001 0 0 100 00000000 0 0 0\neth1 00000000 00000000 0001 0 0 100 00000000 0 0 0\n", "", true},
		{"lower-clears-tie", "eth0 00000000 00000000 0001 0 0 100 00000000 0 0 0\neth1 00000000 00000000 0001 0 0 100 00000000 0 0 0\neth2 00000000 00000000 0001 0 0 50 00000000 0 0 0\n", "eth2", false},
		{"short", "eth0 00000000\n", "", true},
		{"bad-flags", "eth0 00000000 00000000 bad-flags 0 0 0 00000000 0 0 0\n", "", true},
		{"bad-metric", "eth0 00000000 00000000 0001 0 0 bad-metric 00000000 0 0 0\n", "", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, err := defaultIPv4Interface(strings.NewReader(header + tc.rows))
			if (err != nil) != tc.invalid || got != tc.want {
				t.Fatalf("interface=%q err=%v", got, err)
			}
		})
	}
}

// TestSelectInterfaceNetwork 验证默认网卡优先于Docker网桥、使用真实前缀、唯一无路由局域网可识别，多候选要求明确指定。
func TestSelectInterfaceNetwork(t *testing.T) {
	networks := []interfaceNetwork{
		{name: "docker0", prefix: netip.MustParsePrefix("172.17.0.1/16")},
		{name: "lo", prefix: netip.MustParsePrefix("127.0.0.1/8")},
		{name: "eth0", prefix: netip.MustParsePrefix("10.1.8.30/20")},
		{name: "eth1", prefix: netip.MustParsePrefix("192.168.50.10/24")},
	}
	for _, tc := range []struct {
		name, preferred, requested, wantLocal, wantPrefix string
	}{
		{"default-route", "eth0", "", "10.1.8.30", "10.1.0.0/20"},
		{"other-default", "eth1", "", "192.168.50.10", "192.168.50.0/24"},
		{"manual-ip", "eth0", "192.168.50.10", "192.168.50.10", "192.168.50.0/24"},
		{"manual-loopback", "eth0", "127.0.0.1", "127.0.0.1", "127.0.0.0/8"},
		{"manual-missing", "eth0", "192.168.50.11", "", ""},
		{"ambiguous", "", "", "", ""},
		{"unknown-default", "missing", "", "", ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var requested netip.Addr
			if tc.requested != "" {
				requested = netip.MustParseAddr(tc.requested)
			}
			local, prefix, err := selectInterfaceNetwork(networks, tc.preferred, requested)
			if tc.wantLocal == "" {
				if err == nil {
					t.Fatal("应拒绝无法唯一选择的本机地址")
				}
				return
			}
			if err != nil || local.String() != tc.wantLocal || prefix.String() != tc.wantPrefix {
				t.Fatalf("local=%s prefix=%s err=%v", local, prefix, err)
			}
		})
	}
	for _, tc := range []struct {
		name     string
		networks []interfaceNetwork
		invalid  bool
	}{
		{"isolated-lan", networks[2:3], false},
		{"duplicate", []interfaceNetwork{networks[2], networks[2]}, false},
		{"no-lan", []interfaceNetwork{networks[1], {name: "public", prefix: netip.MustParsePrefix("8.8.8.8/24")}}, true},
		{"point-to-point", []interfaceNetwork{{name: "vpn", prefix: netip.MustParsePrefix("10.8.0.1/32")}}, true},
		{"multiple-ip-on-default", []interfaceNetwork{networks[2], {name: "eth0", prefix: netip.MustParsePrefix("10.1.8.31/20")}}, true},
		{"same-ip-different-mask", []interfaceNetwork{networks[2], {name: "eth0", prefix: netip.MustParsePrefix("10.1.8.30/24")}}, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, _, err := selectInterfaceNetwork(tc.networks, "eth0", netip.Addr{})
			if (err != nil) != tc.invalid {
				t.Fatalf("err=%v", err)
			}
		})
	}
}

// TestResolveLocalNetwork 验证省略参数会识别、裸IP只补掩码，完整手动覆盖不读取系统网络，识别失败不猜地址。
func TestResolveLocalNetwork(t *testing.T) {
	for _, tc := range []struct {
		name, localText, prefixText, requested, wantLocal, wantPrefix string
		calls                                                         int
	}{
		{"auto", "", "", "", "10.1.8.30", "10.1.0.0/20", 1},
		{"manual-ip", "192.168.50.10", "", "192.168.50.10", "192.168.50.10", "192.168.50.0/24", 1},
		{"manual-cidr", "192.168.50.10/24", "", "", "192.168.50.10", "192.168.50.0/24", 0},
		{"legacy-prefix", "192.168.50.10", "192.168.50.0/24", "", "192.168.50.10", "192.168.50.0/24", 0},
		{"auto-prefix-override", "", "10.1.8.0/24", "", "10.1.8.30", "10.1.8.0/24", 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			calls := 0
			detect := func(requested netip.Addr) (netip.Addr, netip.Prefix, error) {
				calls++
				if requested.IsValid() != (tc.requested != "") || requested.IsValid() && requested.String() != tc.requested {
					t.Fatalf("识别请求地址错误：%s", requested)
				}
				if requested.IsValid() {
					return requested, netip.MustParsePrefix("192.168.50.0/24"), nil
				}
				return netip.MustParseAddr("10.1.8.30"), netip.MustParsePrefix("10.1.0.0/20"), nil
			}
			local, prefix, err := resolveLocalNetwork(tc.localText, tc.prefixText, detect)
			if err != nil || calls != tc.calls || local.String() != tc.wantLocal || prefix.String() != tc.wantPrefix {
				t.Fatalf("local=%s prefix=%s calls=%d err=%v", local, prefix, calls, err)
			}
		})
	}
	failure := errors.New("synthetic interface read failure")
	_, _, err := resolveLocalNetwork("", "", func(netip.Addr) (netip.Addr, netip.Prefix, error) {
		return netip.Addr{}, netip.Prefix{}, failure
	})
	if !errors.Is(err, failure) {
		t.Fatalf("系统读取错误被吞掉：%v", err)
	}
	for _, text := range []string{"invalid", "::1"} {
		_, _, err := resolveLocalNetwork(text, "", func(netip.Addr) (netip.Addr, netip.Prefix, error) {
			t.Fatal("非法地址不能进入系统识别")
			return netip.Addr{}, netip.Prefix{}, nil
		})
		if err == nil {
			t.Fatalf("非法参数被接受：%s", text)
		}
	}
}
