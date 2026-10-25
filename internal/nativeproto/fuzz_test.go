// 本文件以小型合成种子模糊测试外部字节解析；成功解码必须可逐字节重新编码，不访问网络。
package nativeproto

import (
	"bytes"
	"encoding/hex"
	"testing"
)

// FuzzENetRoundTrip 检查任意 datagram 不触发 panic，并对成功解析的数据保留全部头和命令字节。
func FuzzENetRoundTrip(f *testing.F) {
	for _, text := range []string{"000005ff0001", "80000001860000010003616263", "00000100000100010001", "4000", ""} {
		data, err := hex.DecodeString(text)
		if err != nil {
			f.Fatal(err)
		}
		f.Add(data)
	}
	f.Fuzz(func(t *testing.T, data []byte) {
		packet, err := DecodeENet(data)
		if err != nil {
			return
		}
		encoded, err := EncodeENet(packet)
		if err != nil || !bytes.Equal(encoded, data) {
			t.Fatalf("round trip changed datagram: %v", err)
		}
	})
}

// FuzzRuntimeHandle 检查已连接核心面对任意外部包不会越界或 panic；失败包不伪造应用交付。
func FuzzRuntimeHandle(f *testing.F) {
	for _, text := range []string{"8007000086000001000178", "000701ff000100010000", "8007000088000001000100040000000200000000000000080000000061626364", ""} {
		data, err := hex.DecodeString(text)
		if err != nil {
			f.Fatal(err)
		}
		f.Add(data)
	}
	f.Fuzz(func(t *testing.T, data []byte) {
		runtime, _ := runtimePair(t)
		events, err := runtime.Handle(data, 0)
		if err != nil && (len(events.Messages) != 0 || len(events.Datagrams) != 0) {
			t.Fatal("error returned side effects as success")
		}
	})
}

// FuzzNodeRoundTrip 检查任意节点模板不触发 panic，成功解码时原始未知字节也必须保持不变。
func FuzzNodeRoundTrip(f *testing.F) {
	f.Add(syntheticNode(1))
	f.Add(syntheticNode(2))
	f.Add(syntheticNode(4))
	f.Add([]byte{})
	f.Fuzz(func(t *testing.T, data []byte) {
		packet, err := DecodeNodePacket(data)
		if err != nil {
			return
		}
		encoded, err := packet.Encode()
		if err != nil || !bytes.Equal(encoded, data) {
			t.Fatalf("round trip changed node packet: %v", err)
		}
	})
}
