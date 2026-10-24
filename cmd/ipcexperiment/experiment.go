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
	RecordObservation(string, ipc.Observation) (ipc.Delivery, error)
}

func runExperiment(exePath string, config ipc.Config, send bool, in io.Reader, out io.Writer) error {
	adapter, err := ipc.NewAdapter(exePath)
	if err != nil {
		return err
	}
	return runWithAdapter(config.AuthorizedGroupID, send, in, out, adapter)
}

func runWithAdapter(groupID string, send bool, in io.Reader, out io.Writer, adapter experimentAdapter) error {
	if send && groupID == "" {
		return fmt.Errorf("尚未提供实验group_id；未发送")
	}
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
	phrase := "SEND ONCE " + groupID + " " + ipc.Message
	if _, err := fmt.Fprintf(out, "仅限正常授权客户端和已确认的实验原生群；本次请求不得重试。\ngroup_id=%s\ntext=%s\n请确认独立接收方已就绪；输入以下完整一行才调用一次：\n%s\n", groupID, ipc.Message, phrase); err != nil {
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

	// 确认等待不进入Adapter；SendGroup会重新核对构造时绑定的实例，再占用日志并调用一次。
	result, sendErr := adapter.SendGroup(groupID, ipc.Message)
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
		{ipc.SenderObservation, "发送方正确测试群是否按本次时间新增一条固定文本（one/none/multiple/wrong_target/unknown）："},
		{ipc.RecipientObservation, "独立接收方是否在正确原生群看见正确发送者的一条本次新增固定文本（one/none/multiple/wrong_target/unknown）："},
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
	_, err = fmt.Fprintf(out, "独立接收观察=%s；不代表全群送达。\n", delivery)
	return err
}

func readLine(reader *bufio.Reader) (string, error) {
	line, err := reader.ReadString('\n')
	if err != nil {
		return "", err
	}
	return strings.TrimSuffix(strings.TrimSuffix(line, "\n"), "\r"), nil
}
