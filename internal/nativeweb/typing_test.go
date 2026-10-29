// 本文件验证网页只发布当前在线联系人的有效输入状态，不生成消息、未读计数或网络操作。
package nativeweb

import (
	"testing"
	"time"

	"neiwangtong-gateway/internal/nativeproto"
)

// TestTypingStateExpiresAndRequiresOnlinePeer 验证输入状态按真实在线身份过滤、五秒到期撤销，不能混入群或最近消息。
func TestTypingStateExpiresAndRequiresOnlinePeer(t *testing.T) {
	manager, fake := testManager(t)
	fake.snapshot.Typing = []nativeproto.TypingStatus{
		{UserID: "synthetic-peer", Until: time.Now().Add(nativeproto.TypingDuration)},
		{UserID: "other-peer", Until: time.Now().Add(nativeproto.TypingDuration)},
	}
	if err := manager.Start(manager.State().Name); err != nil {
		t.Fatal(err)
	}
	state := awaitPhase(t, manager, "online")
	if len(state.Typing) != 1 || state.Typing[0].UserID != "synthetic-peer" || state.Typing[0].RemainingMS <= 0 || state.Typing[0].RemainingMS > 5000 {
		t.Fatalf("输入状态或剩余时长错误: %+v", state.Typing)
	}
	if len(state.Messages) != 0 || len(state.SentMessages) != 0 || len(state.RecentConversations) != 0 {
		t.Fatal("输入通知不应产生聊天记录或最近联系人")
	}
	fake.mu.Lock()
	fake.snapshot.Typing[0].Until = time.Now().Add(-time.Millisecond)
	fake.mu.Unlock()
	if len(manager.State().Typing) != 0 {
		t.Fatal("过期或离线联系人仍显示正在输入")
	}
	manager.Stop()
	if len(awaitPhase(t, manager, "offline").Typing) != 0 {
		t.Fatal("下线后仍显示正在输入")
	}
}
