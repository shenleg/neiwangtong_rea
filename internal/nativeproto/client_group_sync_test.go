// 本文件使用loopback群推送与成员回复，验证从正常同步到3011提交、成员拒绝和快照持久化。
package nativeproto

import (
	"context"
	"errors"
	"strconv"
	"testing"
	"time"
)

// TestGroupSyncThenContinuousSend 验证3003推送或3002/3004→3006→3007后，才能在同一连接顺序提交两条3011。
func TestGroupSyncThenContinuousSend(t *testing.T) {
	for _, pushCommand := range []uint32{3002, 3003, 3004} {
		t.Run(strconv.Itoa(int(pushCommand)), func(t *testing.T) {
			initial := syntheticGroupPush(pushCommand, "synthetic-local", "synthetic-peer")
			if pushCommand == 3004 {
				var err error
				initial, err = EncodeGroupInfoRequest("synthetic-group")
				if err != nil {
					t.Fatal(err)
				}
			}
			events := make(chan ClientEvent, 128)
			messages := make(chan GroupMessage, 2)
			requests := 0
			client, cancel, done, peerDone := residentFixture(t, true, func(frame Frame) (*Frame, error) {
				if frame.Command == CommandGroupUserRequest {
					id, err := DecodeGroupUserRequest(frame)
					if err != nil || id != "synthetic-group" {
						return nil, errors.New("错误成员请求")
					}
					requests++
					response := syntheticGroupUserResponse(20, "synthetic-local", "synthetic-peer")
					return &response, nil
				}
				message, err := DecodeGroupMessage(frame)
				if err == nil {
					messages <- message
				}
				return nil, err
			}, func(event ClientEvent) error { events <- event; return nil }, initial)
			awaitResidentEvent(t, events, "group_synchronized", "")
			for i := 0; i < 2; i++ {
				result, err := client.SendGroup(context.Background(), "synthetic-group", "synthetic text "+strconv.Itoa(i))
				if err != nil || !result.Submitted || !result.TransportAcknowledged || result.Command != 3011 {
					t.Fatalf("result=%+v err=%v", result, err)
				}
				if message := <-messages; strconv.FormatUint(message.MessageID, 10) != result.MessageID {
					t.Fatal("编号不符")
				}
			}
			result := finishResident(t, cancel, done, peerDone)
			expectedRequests := 0
			if pushCommand != 3003 {
				expectedRequests = 1
			}
			if result.ChatSends != 2 || result.DiscoverySends != 1 || requests != expectedRequests {
				t.Fatalf("result=%+v requests=%d", result, requests)
			}
		})
	}
}

// TestGroupResponseMembershipBits 验证RET=12即使带本机ID也不视为已加入；RET=20仍需本机实际出现在成员表。
func TestGroupResponseMembershipBits(t *testing.T) {
	for _, test := range []struct {
		ret     uint32
		members []string
		allowed bool
	}{
		{12, []string{"synthetic-local", "synthetic-peer"}, false},
		{10, nil, false},
		{20, []string{"synthetic-peer"}, false},
		{20, []string{"synthetic-local", "synthetic-peer"}, true},
	} {
		client := &Client{config: ClientConfig{Node: NodePacket{UserID: "synthetic-local"}}, peer: newSyntheticPeer(t), origin: time.Now(), groups: map[string]GroupInfo{}, groupPending: map[string]bool{"synthetic-group": true}, observe: func(ClientEvent) error { return nil }}
		frame := syntheticGroupUserResponse(test.ret, test.members...)
		wire, err := Encode(frame.Command, frame.Payload)
		if err != nil {
			t.Fatal(err)
		}
		if err := client.receiveGroupUserResponse(wire, frame); err != nil {
			t.Fatal(err)
		}
		_, present := client.groups["synthetic-group"]
		if present != test.allowed || client.groupPending["synthetic-group"] {
			t.Fatalf("ret=%d present=%v pending=%v", test.ret, present, client.groupPending)
		}
	}
}

// TestGroupSnapshotRecordFailure 验证快照保存失败时不开放发送，回调修改返回切片也不会改内部成员。
func TestGroupSnapshotRecordFailure(t *testing.T) {
	recordErr := errors.New("synthetic snapshot failure")
	client := &Client{config: ClientConfig{Node: NodePacket{UserID: "local"}, ObserveGroup: func(GroupInfo) error { return recordErr }}, groups: map[string]GroupInfo{}, observe: func(ClientEvent) error { return nil }}
	group := GroupInfo{GroupID: "g", Name: "Synthetic", Members: []string{"local", "remote"}}
	if err := client.saveGroup(group, 3003); !errors.Is(err, recordErr) || len(client.groups) != 0 {
		t.Fatalf("groups=%v err=%v", client.groups, err)
	}
	client.config.ObserveGroup = func(snapshot GroupInfo) error { snapshot.Members[0] = "changed"; return nil }
	if err := client.saveGroup(group, 3003); err != nil {
		t.Fatal(err)
	}
	if client.groups["g"].Members[0] != "local" {
		t.Fatal("回调修改了内部成员快照")
	}
}
