// 本文件验证新节点的正常默认初始化与独立身份保存；不使用真实客户端标识或网络。
package nativeproto

import (
	"bytes"
	"encoding/binary"
	"encoding/hex"
	"net/netip"
	"os"
	"path/filepath"
	"testing"
)

// TestRegistrationDefaultLayout 验证已恢复的产品 FEATURE、GUID 原始字节、500字段及显式零区。
func TestRegistrationDefaultLayout(t *testing.T) {
	guid, err := NewSessionGUID(bytes.NewReader(make([]byte, 16)))
	if err != nil {
		t.Fatal(err)
	}
	node, err := NewNodePacket(NodeRegistration{UserID: "synthetic-new-user", SessionGUID: guid, Address: netip.MustParseAddr("127.0.0.1"),
		Broadcast: netip.MustParseAddr("127.255.255.255"), DiscoveryPort: 1234, ENetPort: 4321}, 1)
	if err != nil {
		t.Fatal(err)
	}
	data, err := node.Encode()
	if err != nil {
		t.Fatal(err)
	}
	if binary.BigEndian.Uint32(data[12:]) != 0x5fc1d8ec || binary.BigEndian.Uint32(data[0xa0:]) != 500 {
		t.Fatal("default registration constants changed")
	}
	if !bytes.Equal(data[0x5f:0x5f+16], guid[:]) || !bytes.Equal(data[0x5f+16:0xa0], make([]byte, 49)) {
		t.Fatal("session treated as text or reused UID")
	}
	if !bytes.Equal(data[0xa7:], make([]byte, len(data)-0xa7)) {
		t.Fatal("default reserved/feature tail not zero initialized")
	}
	other := node
	other.raw[0x5f]++
	order, err := node.SessionOrder(other)
	if err != nil || order != -1 {
		t.Fatalf("session order=%d, %v", order, err)
	}
	order, err = other.SessionOrder(node)
	if err != nil || order != 1 {
		t.Fatal("active/passive order reversed")
	}
}

// TestProbeRequiredNativeFields 验证基础上线协议必填字段，防止把联系人分组或消息版本留空后才真实试发。
func TestProbeRequiredNativeFields(t *testing.T) {
	profile, err := ProbeHandshake("Synthetic probe")
	if err != nil {
		t.Fatal(err)
	}
	for _, target := range []string{"APP", "MSG", "NAME", "GROUP"} {
		copyProfile := profile
		copyProfile.Features = append([]XMLField(nil), profile.Features...)
		copyProfile.Info = append([]XMLField(nil), profile.Info...)
		for i := range copyProfile.Features {
			if copyProfile.Features[i].Name == target {
				copyProfile.Features[i].Value = ""
			}
		}
		for i := range copyProfile.Info {
			if copyProfile.Info[i].Name == target {
				copyProfile.Info[i].Value = ""
			}
		}
		if err := validateClientProfile(copyProfile); err == nil {
			t.Fatalf("accepted empty %s", target)
		}
	}
}

// TestIndependentIdentityPersistence 验证 UserID 是原始 Windows GUID 内存顺序的32hex，后续读取不换身份。
func TestIndependentIdentityPersistence(t *testing.T) {
	path := filepath.Join(t.TempDir(), "identity")
	source := make([]byte, 16)
	for i := range source {
		source[i] = byte(i)
	}
	id, err := LoadOrCreateIdentity(path, bytes.NewReader(source))
	if err != nil {
		t.Fatal(err)
	}
	expected := append([]byte(nil), source...)
	expected[7] = expected[7]&15 | 64
	expected[8] = expected[8]&63 | 128
	if id != hex.EncodeToString(expected) {
		t.Fatal("identity is not GUID memory byte order")
	}
	again, err := LoadOrCreateIdentity(path, bytes.NewReader(nil))
	if err != nil || again != id {
		t.Fatal("existing own identity was replaced")
	}
	data, err := os.ReadFile(path)
	if err != nil || len(data) != 32 {
		t.Fatal("identity not durably written")
	}
}

// TestIdentityRejectsCorruption 验证损坏身份保留原文件、随机源失败不创建半个身份，不自动重置。
func TestIdentityRejectsCorruption(t *testing.T) {
	path := filepath.Join(t.TempDir(), "identity")
	if err := os.WriteFile(path, []byte("broken"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadOrCreateIdentity(path, bytes.NewReader(make([]byte, 16))); err == nil {
		t.Fatal("corrupt identity reset")
	}
	data, err := os.ReadFile(path)
	if err != nil || string(data) != "broken" {
		t.Fatal("corrupt identity overwritten")
	}
	other := filepath.Join(t.TempDir(), "new-identity")
	if _, err := LoadOrCreateIdentity(other, bytes.NewReader(nil)); err == nil {
		t.Fatal("missing randomness accepted")
	}
	if _, err := os.Stat(other); !os.IsNotExist(err) {
		t.Fatal("random failure created identity file")
	}
}
