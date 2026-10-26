// 本文件以合成联系人验证列表选择、身份归属、JSON边界和副本隔离，不读取原客户端资料或访问网络。
package nativeproto

import (
	"bytes"
	"net/netip"
	"reflect"
	"strings"
	"testing"
)

// syntheticContacts 返回两个同名但不同身份和端点的合成联系人，用于验证按ID明确选择而不猜同名对象。
func syntheticContacts() []Contact {
	return []Contact{
		{UserID: "synthetic-a", Name: "同名联系人", Discovery: netip.MustParseAddrPort("127.0.0.2:9011")},
		{UserID: "synthetic-b", Name: "同名联系人", Discovery: netip.MustParseAddrPort("127.0.0.3:9011")},
	}
}

// TestDirectoryRoundTripAndSelection 验证本身份列表往返、同名ID选择及列表副本不会修改内部目录。
func TestDirectoryRoundTripAndSelection(t *testing.T) {
	contacts := syntheticContacts()
	directory, err := NewDirectory("synthetic-own", contacts)
	if err != nil {
		t.Fatal(err)
	}
	contacts[0].Name = "mutated"
	if got, err := directory.ContactByID("synthetic-b"); err != nil || got != syntheticContacts()[1] {
		t.Fatalf("contact=%+v err=%v", got, err)
	}
	if _, err := directory.ContactByID("同名联系人"); err == nil {
		t.Fatal("selected by ambiguous name instead of ID")
	}
	copy := directory.ListContacts()
	copy[0].Name = "mutated copy"
	var output bytes.Buffer
	if err := directory.Write(&output); err != nil {
		t.Fatal(err)
	}
	got, err := LoadDirectory(&output)
	if err != nil || got.OwnUserID() != "synthetic-own" || !reflect.DeepEqual(got.ListContacts(), syntheticContacts()) {
		t.Fatalf("directory=%+v err=%v", got, err)
	}
}

// TestDirectoryRejectsBadContacts 验证重复/本机身份、无效文本、广播和缺失端点不能成为可选联系人。
func TestDirectoryRejectsBadContacts(t *testing.T) {
	for _, change := range []func([]Contact){
		func(c []Contact) { c[0].UserID = "" },
		func(c []Contact) { c[0].UserID = "synthetic-own" },
		func(c []Contact) { c[0].UserID = c[1].UserID },
		func(c []Contact) { c[0].UserID = strings.Repeat("x", 65) },
		func(c []Contact) { c[0].Name = "" },
		func(c []Contact) { c[0].Name = "bad\x01name" },
		func(c []Contact) { c[0].Discovery = netip.AddrPort{} },
		func(c []Contact) { c[0].Discovery = netip.MustParseAddrPort("0.0.0.0:9011") },
		func(c []Contact) { c[0].Discovery = netip.MustParseAddrPort("255.255.255.255:9011") },
		func(c []Contact) { c[0].Discovery = netip.MustParseAddrPort("224.0.0.1:9011") },
		func(c []Contact) { c[0].Discovery = netip.MustParseAddrPort("127.0.0.1:0") },
	} {
		contacts := syntheticContacts()
		change(contacts)
		if _, err := NewDirectory("synthetic-own", contacts); err == nil {
			t.Fatalf("accepted %+v", contacts)
		}
	}
}

// TestDirectoryRejectsMalformedDocuments 验证列表JSON拒绝未知字段和尾随文档，而不是静默忽略配置错误。
func TestDirectoryRejectsMalformedDocuments(t *testing.T) {
	for _, text := range []string{
		`{}`, `null`, `{"own_user_id":"own","contacts":[],"unknown":true}`,
		`{"own_user_id":"own","contacts":[]} {}`,
		`{"own_user_id":"own","contacts":[]} invalid`,
		`{"own_user_id":"own","contacts":[{"user_id":"peer","name":"peer","discovery":"127.0.0.2:9011","password":"not-a-field"}]}`,
	} {
		if _, err := LoadDirectory(strings.NewReader(text)); err == nil {
			t.Fatalf("accepted %s", text)
		}
	}
}
