// 本文件用合成正文验证换行兼容、序列化容量、可变载荷及观察记录，不访问真实客户端。
package ipc

import (
	"bytes"
	"encoding/json"
	"os"
	"strings"
	"testing"

	"neiwangtong-gateway/internal/windowprobe"
)

// TestPrepareMessageNewlines 验证实际换行保留、字面换行兼容且不做通用反转义或trim。
func TestPrepareMessageNewlines(t *testing.T) {
	for _, tc := range []struct{ name, input, want string }{
		{"actual-lf", "第一行\n第二行", "第一行\n第二行"},
		{"literal-lf", `第一行\n第二行`, "第一行\n第二行"},
		{"actual-crlf", "第一行\r\n第二行", "第一行\r\n第二行"},
		{"literal-crlf", `第一行\r\n第二行`, "第一行\r\n第二行"},
		{"mixed", "首行\\n中间\r\n末行\\r\\n", "首行\n中间\r\n末行\r\n"},
		{"single-cr", "a\rb", "a\rb"},
		{"other-escapes", `\t中\r`, `\t中\r`},
		{"double-backslash", `a\\nb`, "a\\\nb"},
		{"spaces", "  正文  \n ", "  正文  \n "},
		{"only-newlines", `\n\r\n`, "\n\r\n"},
		{"specials", `<>&'" ]]> 😀`, `<>&'" ]]> 😀`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, err := prepareMessage(tc.input)
			if err != nil || got != tc.want {
				t.Fatalf("正文转换=%q，预期=%q，错误=%v", got, tc.want, err)
			}
			again, err := prepareMessage(got)
			if err != nil || again != got {
				t.Fatal("重复校验改变了已经转换的正文")
			}
		})
	}
}

// TestMessageCapacity 验证长中文不受512字节限制，按真实字符开销和保守URL片段预算拒绝边界外输入。
func TestMessageCapacity(t *testing.T) {
	for _, tc := range []struct {
		name, input string
		valid       bool
	}{
		{"empty", "", false},
		{"nul", "a\x00b", false},
		{"invalid-utf8", "bad\xff", false},
		{"500-chinese", strings.Repeat("中", 500), true},
		{"5000-chinese", strings.Repeat("中", 5000), true},
		{"500-emoji", strings.Repeat("😀", 500), true},
		{"16k-quotes", strings.Repeat(`"`, 16384), true},
		// 无点号时只有一个普通片段，固定开销为446+501=947。
		{"exact-budget", strings.Repeat("中", escapedJSONBudget-947), true},
		{"over-budget", strings.Repeat("中", escapedJSONBudget-946), false},
		{"ascii-boundary", strings.Repeat("x", 261003), true},
		{"ascii-over", strings.Repeat("x", 261004), false},
		{"emoji-boundary", strings.Repeat("😀", 130501), true},
		{"emoji-over", strings.Repeat("😀", 130502), false},
		{"content-over-budget", strings.Repeat("x", escapedJSONBudget+1), false},
		{"quotes-boundary", strings.Repeat(`"`, 37286), true},
		{"quotes-over", strings.Repeat(`"`, 37287), false},
		{"spaces-boundary", strings.Repeat(" ", 43500), true},
		{"spaces-over", strings.Repeat(" ", 43501), false},
		{"short-urls-fit", strings.Repeat("a.aa ", 333), true},
		{"short-urls-over", strings.Repeat("a.aa ", 334), false},
		{"normalized-newlines", strings.Repeat(`\n`, 5000), true},
		{"normalize-before-budget", strings.Repeat(`\r\n`, 65000), true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, err := prepareMessage(tc.input)
			if (err == nil) != tc.valid {
				t.Fatalf("容量判断=%v，预期通过=%v", err, tc.valid)
			}
		})
	}
}

// TestMessageShapeMaximum 验证交点附近两候选值与完整整数枚举相同，避免链接数取整造成漏算。
func TestMessageShapeMaximum(t *testing.T) {
	for units := 1; units <= 256; units++ {
		for dots := 0; dots <= units; dots++ {
			maxLinks := min(dots, units/4)
			left := min(maxLinks, (units-1)/5)
			got := max(messageShapeOverhead(units, left), messageShapeOverhead(units, min(maxLinks, left+1)))
			want := 0
			for links := 0; links <= maxLinks; links++ {
				want = max(want, 446*min(links+1, units-4*links)+328*links)
			}
			if got != want {
				t.Fatalf("U=%d D=%d 上界=%d，枚举=%d", units, dots, got, want)
			}
		}
	}
}

// TestMessageCharacterCost 核对转义成本与JSON字符串和六项外层替换一致，不模拟客户端排版或URL识别。
func TestMessageCharacterCost(t *testing.T) {
	// 每个用例以纯汉字填满预算余量，在最后一个字符处恰好通过，再多一个汉字必须失败。
	for _, char := range []string{"a", "中", "😀", `"`, `\`, "'", " ", "&", "<", ">", "\b", "\t", "\n", "\f", "\r", "\x01", "\x1f"} {
		var encoded bytes.Buffer
		encoder := json.NewEncoder(&encoded)
		// 关闭Go默认的HTML字符转义，与客户端保留<>&的JSON规则比较。
		encoder.SetEscapeHTML(false)
		if err := encoder.Encode(char); err != nil {
			t.Fatal(err)
		}
		value := bytes.TrimSpace(encoded.Bytes())
		valueText := string(value[1 : len(value)-1])
		cost := 0
		for _, c := range valueText {
			switch c {
			case '&':
				cost += 5
			case '"', '\'', ' ':
				cost += 6
			case '<', '>':
				cost += 4
			default:
				cost++
				if c > 0xffff {
					cost++
				}
			}
		}
		body := strings.Repeat("中", escapedJSONBudget-947-cost) + char
		if err := validateMessage(body); err != nil {
			t.Fatalf("字符%q的恰好预算被拒绝: %v", char, err)
		}
		if err := validateMessage(body + "中"); err == nil {
			t.Fatalf("字符%q的越界正文被接受", char)
		}
	}
}

// TestObservationRejectsDamagedUnicode 防止放开自定义正文后JSON解码器静默替换损坏字符并接受观察。
func TestObservationRejectsDamagedUnicode(t *testing.T) {
	for _, raw := range [][]byte{
		{'"', 0xff, '"'},
		{'"', '\\', 'u', 'd', '8', '0', '0', '"'},
		{'"', '\\', 'u', 'd', 'c', '0', '0', '"'},
	} {
		d, path, calls := fixture(t)
		prefix := []byte(`{"event":"attempt_reserved","attempt_id":"` + testAttemptID + `","group_id":"` + testGroupID + `","text":`)
		data := append(prefix, raw...)
		data = append(data, []byte("}\n{\"event\":\"call_result\",\"result\":{\"invoked\":true}}\n")...)
		if err := os.WriteFile(path, data, 0600); err != nil {
			t.Fatal(err)
		}
		delivery, err := adapterFixture(t, d).RecordObservation(testAttemptID, Observation{RecipientObservation, "one"})
		if err == nil || delivery != Unknown || *calls != 0 {
			t.Fatal("损坏正文被接受或触发发送")
		}
		after, err := os.ReadFile(path)
		if err != nil || !bytes.Equal(after, data) {
			t.Fatal("损坏的原始记录被修改")
		}
	}
}

// TestCustomMessageSendAndObservation 验证两个接口把兼容后的多行正文写入载荷及日志，重建后仍可追加观察。
func TestCustomMessageSendAndObservation(t *testing.T) {
	for _, selector := range []string{"group_id", "user_id"} {
		for _, tc := range []struct{ name, input, want string }{
			{"custom", "自定义消息 😀 <>&", "自定义消息 😀 <>&"},
			{"literal-lf", `第一行\n第二行`, "第一行\n第二行"},
			{"literal-crlf", `第一行\r\n第二行`, "第一行\r\n第二行"},
			{"500-chinese", strings.Repeat("中", 500), strings.Repeat("中", 500)},
		} {
			t.Run(selector+"/"+tc.name, func(t *testing.T) {
				d, path, calls := fixture(t)
				originalSend := d.send
				d.send = func(target windowprobe.Target, payload []byte) (CallResult, error) {
					if len(payload) != bodyOffset+len(tc.want)+1 || payload[len(payload)-1] != 0 || string(payload[bodyOffset:len(payload)-1]) != tc.want {
						t.Fatal("正文、动态长度或终止NUL错误")
					}
					return originalSend(target, payload)
				}
				a := adapterFixture(t, d)
				send, id := a.SendGroup, testGroupID
				if selector == "user_id" {
					send, id = a.SendUser, testUserID
				}
				result, err := send(id, tc.input)
				if err != nil || !result.Call.Invoked || result.Delivery != Unknown || *calls != 1 {
					t.Fatalf("自定义发送事实错误: %+v %v", result, err)
				}
				data, err := os.ReadFile(path)
				if err != nil {
					t.Fatal(err)
				}
				var reserved struct {
					Text   string `json:"text"`
					CBData int    `json:"cbData"`
				}
				if err := json.NewDecoder(bytes.NewReader(data)).Decode(&reserved); err != nil || reserved.Text != tc.want || reserved.CBData != bodyOffset+len(tc.want)+1 {
					t.Fatalf("日志未保存实际发送正文: %+v %v", reserved, err)
				}
				delivery, err := adapterFixture(t, d).RecordObservation(result.AttemptID, Observation{RecipientObservation, "one"})
				if err != nil || delivery != RecipientObserved || *calls != 1 {
					t.Fatalf("自定义正文观察失败或触发重发: %s %v", delivery, err)
				}
			})
		}
	}
}
