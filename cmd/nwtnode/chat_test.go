// 本文件验证单聊命令的列表选择、默认只读、显式发送开关与独占记录；不连接真实节点或发送消息。
package main

import (
	"bytes"
	"net/netip"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"neiwangtong-gateway/internal/nativeproto"
)

// chatArgs 准备合成联系人列表和节点参数，返回参数及身份/记录路径；只写测试目录文件，失败终止测试。
func chatArgs(t *testing.T) ([]string, string, string) {
	t.Helper()
	args, id, record := nodeArgs(t)
	directory, err := nativeproto.NewDirectory("synthetic-own", []nativeproto.Contact{{UserID: "synthetic-peer", Name: "合成联系人", Discovery: netip.MustParseAddrPort("127.0.0.2:9011")}})
	if err != nil {
		t.Fatal(err)
	}
	var data bytes.Buffer
	if err := directory.Write(&data); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "contacts.json")
	if err := os.WriteFile(path, data.Bytes(), 0600); err != nil {
		t.Fatal(err)
	}
	return append(args, "-contacts", path), id, record
}

// TestChatValidationOnly 验证列表选定联系人及-send不隐含-run，完整正文不会打印或写入私有状态。
func TestChatValidationOnly(t *testing.T) {
	args, id, record := chatArgs(t)
	args = append(args, "-send", "-user-id", "synthetic-peer", "-message", "合成私有正文<&>\n第二行")
	var out, errOut bytes.Buffer
	if code := run(args, &out, &errOut); code != 0 || !bytes.Contains(out.Bytes(), []byte(`"network_invoked":false`)) || !bytes.Contains(out.Bytes(), []byte(`"chat_requested":true`)) {
		t.Fatalf("code=%d out=%s err=%s", code, &out, &errOut)
	}
	if bytes.Contains(out.Bytes(), []byte("合成私有正文")) {
		t.Fatal("validation output disclosed content")
	}
	for _, path := range []string{id, record} {
		if _, err := os.Stat(path); !os.IsNotExist(err) {
			t.Fatal("validation wrote identity or record")
		}
	}
}

// TestChatRejectsIncompleteOrImplicitSend 验证参数不完整、目标不在列表、端点不符或缺-send时在网络操作前失败。
func TestChatRejectsIncompleteOrImplicitSend(t *testing.T) {
	for _, extra := range [][]string{
		{"-send"},
		{"-send", "-message", "synthetic"},
		{"-send", "-user-id", "synthetic-peer"},
		{"-send", "-user-id", "not-in-list", "-message", "synthetic"},
		{"-send", "-user-id", "synthetic-peer", "-message", "bad\x01text"},
		{"-send", "-user-id", "synthetic-peer", "-message", "synthetic", "-remote", "127.0.0.3:9011"},
		{"-send", "-user-id", "synthetic-peer", "-message", "synthetic", "-contacts", ""},
		{"-run", "-user-id", "synthetic-peer", "-message", "synthetic"},
		{"-group-id", "synthetic-group"},
	} {
		args, id, record := chatArgs(t)
		var out, errOut bytes.Buffer
		if code := run(append(args, extra...), &out, &errOut); code != 2 {
			t.Fatalf("extra=%v code=%d err=%s", extra, code, &errOut)
		}
		for _, path := range []string{id, record} {
			if _, err := os.Stat(path); !os.IsNotExist(err) {
				t.Fatal("invalid request created private state")
			}
		}
	}
}

// TestChatRecordCollisionStopsBeforeIdentity 验证真实发送参数不能覆盖已有运行记录，也不会换文件或身份再试。
func TestChatRecordCollisionStopsBeforeIdentity(t *testing.T) {
	args, id, record := chatArgs(t)
	if err := os.WriteFile(record, []byte("occupied-chat-attempt"), 0600); err != nil {
		t.Fatal(err)
	}
	args = append(args, "-run", "-send", "-user-id", "synthetic-peer", "-message", "synthetic")
	var out, errOut bytes.Buffer
	if code := run(args, &out, &errOut); code != 1 {
		t.Fatalf("code=%d err=%s", code, &errOut)
	}
	if _, err := os.Stat(id); !os.IsNotExist(err) {
		t.Fatal("collision created an identity")
	}
	data, err := os.ReadFile(record)
	if err != nil || string(data) != "occupied-chat-attempt" {
		t.Fatal("collision overwrote the occupied record")
	}
}

// TestListContactsIsOffline 验证不提供节点/身份/运行参数也能只读列出可选择联系人。
func TestListContactsIsOffline(t *testing.T) {
	args, _, _ := chatArgs(t)
	path := args[len(args)-1]
	var out, errOut bytes.Buffer
	if code := run([]string{"-list-contacts", "-contacts", path}, &out, &errOut); code != 0 || !strings.Contains(out.String(), "synthetic-peer") {
		t.Fatalf("code=%d out=%s err=%s", code, &out, &errOut)
	}
}

// TestSaveContactsUpdatesCopy 验证观察到的联系人只写入新文件，不修改原目录，且其他身份列表被拒绝。
func TestSaveContactsUpdatesCopy(t *testing.T) {
	original := nativeproto.Contact{UserID: "peer", Name: "old", Discovery: netip.MustParseAddrPort("127.0.0.2:9011")}
	directory, err := nativeproto.NewDirectory("own", []nativeproto.Contact{original})
	if err != nil {
		t.Fatal(err)
	}
	file, err := os.Create(filepath.Join(t.TempDir(), "new-contacts.json"))
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()
	updated := original
	updated.Name = "new"
	if err := saveContacts(file, "own", directory, updated); err != nil {
		t.Fatal(err)
	}
	if _, err := file.Seek(0, 0); err != nil {
		t.Fatal(err)
	}
	got, err := nativeproto.LoadDirectory(file)
	if err != nil || got.ListContacts()[0] != updated || directory.ListContacts()[0] != original {
		t.Fatalf("got=%+v err=%v", got, err)
	}
	if err := saveContacts(file, "other-own", directory, updated); err == nil {
		t.Fatal("accepted other identity's contact list")
	}
}
