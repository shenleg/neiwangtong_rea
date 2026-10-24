// 本文件兼容换行输入并按已证明的正文片段与转义开销检查序列化容量，不重建客户端JSON或识别URL。
package ipc

import (
	"fmt"
	"strings"
	"unicode/utf8"
)

const (
	// 6d06c31的固定正文构造与两个XML模板共同预算，不含外层XML及结尾NUL。
	escapedJSONBudget = 261950
	// 固定根字段/排版和普通/链接片段开销，已包含引号、缩进及外层转义。
	messageRootOverhead  = 501
	textFragmentOverhead = 446
	linkFragmentOverhead = 328
)

// 只解释这两种字面换行，长模式优先；不做通用反转义或双反斜杠保护。
var messageNewlines = strings.NewReplacer(`\r\n`, "\r\n", `\n`, "\n")

func prepareMessage(message string) (string, error) {
	message = messageNewlines.Replace(message)
	if err := validateMessage(message); err != nil {
		return "", err
	}
	return message, nil
}

func validateMessage(message string) error {
	if message == "" || !utf8.ValidString(message) || strings.IndexByte(message, 0) >= 0 {
		return fmt.Errorf("正文必须非空、为有效UTF-8且不含NUL；未发送")
	}
	units, dots, content := 0, 0, 0
	for _, c := range message {
		width := 1
		if c > 0xffff {
			width = 2
		}
		units += width
		switch c {
		case '"':
			content += 7
		case '\\', '\b', '\t', '\n', '\f', '\r':
			content += 2
		case '\'', ' ':
			content += 6
		case '&':
			content += 5
		case '<', '>':
			content += 4
		default:
			if c < 0x20 {
				content += 6
			} else {
				content += width
			}
		}
		if c == '.' {
			dots++
		}
		// 内容成本单调增加且不小于units，提前拒绝使后续整数乘法在386上也有界。
		if content > escapedJSONBudget {
			return fmt.Errorf("正文序列化预算上界超过客户端容量；未发送")
		}
	}

	// 每个URL至少消耗4个UTF-16单元及1个ASCII点号；普通片段数T<=min(L+1,U-4L)。
	// 两条线的交点为(U-1)/5，结构成本先增后减，只需检查交点附近两个整数。
	// 完整推导见docs/research/reports/ipc-message-content/refined-body-bound.md。
	maxLinks := min(dots, units/4)
	left := min(maxLinks, (units-1)/5)
	right := min(maxLinks, left+1)
	shape := max(messageShapeOverhead(units, left), messageShapeOverhead(units, right))
	bound := content + messageRootOverhead + shape
	if bound > escapedJSONBudget {
		return fmt.Errorf("正文序列化预算上界%d超过%d；未发送", bound, escapedJSONBudget)
	}
	return nil
}

func messageShapeOverhead(units, links int) int {
	texts := min(links+1, units-4*links)
	return textFragmentOverhead*texts + linkFragmentOverhead*links
}
