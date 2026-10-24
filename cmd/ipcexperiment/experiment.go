// 本文件仅处理实验确认和外部观察；配置、定位、载荷、单次日志与Win32调用全部交给ipc.Adapter。
package main

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"strings"

	"neiwangtong-gateway/internal/ipc"
)

type experimentAdapter interface {
	Preflight() (ipc.Preflight, error)
	SendGroup(string, string) (ipc.SendResult, error)
	SendUser(string, string) (ipc.SendResult, error)
	RecordObservation(string, ipc.Observation) (ipc.Delivery, error)
}

// targetKind 区分实验发送的两类显式目标。
type targetKind uint8

const (
	targetGroup targetKind = iota
	targetUser
)

// sendTarget 是一次实验发送的显式目标：kind 决定调用SendGroup或SendUser，id为原始目标标识。
type sendTarget struct {
	kind targetKind
	id   string
}

// targetLabel 返回载荷选择器名与场景描述，用于确认提示。
func (t sendTarget) targetLabel() (selector, scene string) {
	if t.kind == targetUser {
		return "user_id", "已确认的实验单聊用户"
	}
	return "group_id", "已确认的实验原生群"
}

// runExperiment 按配置解析唯一发送目标并委托给runWithAdapter；text为空时调用方应先解析为固定实验正文。
func runExperiment(exePath string, config ipc.Config, send bool, text string, in io.Reader, out io.Writer) error {
	adapter, err := ipc.NewAdapter(exePath)
	if err != nil {
		return err
	}
	var target sendTarget
	switch {
	case config.AuthorizedGroupID != "":
		target = sendTarget{targetGroup, config.AuthorizedGroupID}
	case config.AuthorizedUserID != "":
		target = sendTarget{targetUser, config.AuthorizedUserID}
	}
	return runWithAdapter(target, text, send, in, out, adapter)
}

// runWithAdapter 管理确认与观察；确认短语包含完整目标ID与正文，确认什么就发送什么。
func runWithAdapter(target sendTarget, text string, send bool, in io.Reader, out io.Writer, adapter experimentAdapter) error {
	if send && target.id == "" {
		return fmt.Errorf("尚未提供实验目标ID；未发送")
	}
	selector, scene := target.targetLabel()
	info, err := adapter.Preflight()
	if err != nil {
		return err
	}
	if err := json.NewEncoder(out).Encode(info); err != nil {
		return err
	}
	if !send {
		return nil
	}
	phrase := "SEND ONCE " + target.id + " " + text
	if _, err := fmt.Fprintf(out, "仅限正常授权客户端和%s；本次请求不得重试。\n%s=%s\ntext=%s\n请确认独立接收方已就绪；输入以下完整一行才调用一次：\n%s\n", scene, selector, target.id, text, phrase); err != nil {
		return err
	}
	reader := bufio.NewReader(in)
	answer, err := readLine(reader)
	if err != nil {
		return fmt.Errorf("发送确认: %w", err)
	}
	if answer != phrase {
		return fmt.Errorf("未收到完整确认，未发送")
	}

	// 确认等待不进入Adapter；发送方法会重新核对构造时绑定的实例，再占用日志并调用一次。
	var result ipc.SendResult
	var sendErr error
	if target.kind == targetUser {
		result, sendErr = adapter.SendUser(target.id, text)
	} else {
		result, sendErr = adapter.SendGroup(target.id, text)
	}
	if result.AttemptReserved {
		if err := json.NewEncoder(out).Encode(result); err != nil {
			return err
		}
	}
	if sendErr != nil {
		return sendErr
	}
	prompts := []struct {
		kind ipc.ObservationKind
		text string
	}{
		{ipc.ClientObservation, "调用结束，不会重试。客户端状态（stable/crashed/abnormal/unknown）："},
		{ipc.SenderObservation, "发送方正确测试窗口是否按本次时间新增一条固定文本（one/none/multiple/wrong_target/unknown）："},
		{ipc.RecipientObservation, "独立接收方是否在正确窗口看见正确发送者的一条本次新增固定文本（one/none/multiple/wrong_target/unknown）："},
	}
	delivery := ipc.Unknown
	for _, prompt := range prompts {
		if _, err := fmt.Fprintln(out, prompt.text); err != nil {
			return err
		}
		state, err := readLine(reader)
		if err != nil {
			return fmt.Errorf("外部观察未完成，送达仍为unknown: %w", err)
		}
		delivery, err = adapter.RecordObservation(result.AttemptID, ipc.Observation{Kind: prompt.kind, State: state})
		if err != nil {
			return err
		}
	}
	_, err = fmt.Fprintf(out, "独立接收观察=%s；不代表全量送达。\n", delivery)
	return err
}

// observationKind 解析观察类别参数；未知类别返回错误。
func observationKind(name string) (ipc.ObservationKind, error) {
	switch name {
	case "client":
		return ipc.ClientObservation, nil
	case "sender":
		return ipc.SenderObservation, nil
	case "recipient":
		return ipc.RecipientObservation, nil
	default:
		return "", fmt.Errorf("观察类别必须是 client、sender 或 recipient")
	}
}

// recordObservation 只向既有发送记录追加一条外部观察，不发送；适配已核实的记录标识与类别。
func recordObservation(adapter experimentAdapter, attemptID string, kind ipc.ObservationKind, state string, out io.Writer) error {
	delivery, err := adapter.RecordObservation(attemptID, ipc.Observation{Kind: kind, State: state})
	if err != nil {
		return err
	}
	_, err = fmt.Fprintf(out, "观察已记录，本条送达判定=%s\n", delivery)
	return err
}

func readLine(reader *bufio.Reader) (string, error) {
	line, err := reader.ReadString('\n')
	if err != nil {
		return "", err
	}
	return strings.TrimSuffix(strings.TrimSuffix(line, "\n"), "\r"), nil
}
