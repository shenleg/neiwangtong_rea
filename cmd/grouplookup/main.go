// 本程序提供群名称到 group_id 的只读查询命令入口：读取离线 qrp 副本并输出 JSON，
// 不发送消息、不修改配置、不写数据库文件。qrp 是明文 SQLite，不需要口令。
//
// 退出码：0 唯一命中或列表成功；1 文件、身份、schema 或查询错误；2 参数错误；
// 3 群名未命中；4 同名群歧义，候选已全部输出，不能自动选择。
package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"

	"neiwangtong-gateway/internal/groups"
)

func main() {
	os.Exit(run(os.Args[1:], os.Stdout, os.Stderr))
}

// run 解析参数并执行一次查询，返回进程退出码；输出 JSON 写入 stdout，诊断写入 stderr。
func run(args []string, stdout, stderr io.Writer) int {
	flags := flag.NewFlagSet("grouplookup", flag.ContinueOnError)
	flags.SetOutput(stderr)
	qrpPath := flags.String("qrp", "", "完整离线qrp副本的绝对路径（明文SQLite，无需口令）")
	table := flags.String("table", "", "身份表名；库中只有一张时可省略，多张时必须指定")
	name := flags.String("name", "", "群名称的精确值；缺省时输出全部群")
	if err := flags.Parse(args); err != nil {
		return 2
	}
	if flags.NArg() != 0 {
		fmt.Fprintln(stderr, "不接受位置参数")
		return 2
	}
	if *qrpPath == "" {
		fmt.Fprintln(stderr, "必须用 -qrp 指定离线副本路径")
		return 2
	}
	db, err := groups.Open(*qrpPath)
	if err != nil {
		fmt.Fprintf(stderr, "打开副本: %v\n", err)
		return 1
	}
	target := *table
	if target == "" {
		tables := db.Tables()
		if len(tables) != 1 {
			fmt.Fprintf(stderr, "库中有 %d 张身份表，必须用 -table 指定其一: %v\n", len(tables), tables)
			return 1
		}
		target = tables[0]
	}
	if *name == "" {
		list, err := db.LoadGroups(target)
		if err != nil {
			fmt.Fprintf(stderr, "读取群列表: %v\n", err)
			return 1
		}
		writeResult(stdout, result{Status: "listed", Table: target, PageSize: db.PageSize(),
			SHA256: db.SHA256(), Candidates: list})
		return 0
	}
	matches, err := db.LookupName(target, *name)
	if err != nil {
		fmt.Fprintf(stderr, "查询群: %v\n", err)
		return 1
	}
	template := result{Name: *name, Table: target, PageSize: db.PageSize(), SHA256: db.SHA256()}
	switch len(matches) {
	case 0:
		template.Status = "not_found"
		writeResult(stdout, template)
		return 3
	case 1:
		template.Status = "unique"
		template.Candidates = matches
		writeResult(stdout, template)
		return 0
	default:
		template.Status = "ambiguous"
		template.Candidates = matches
		writeResult(stdout, template)
		return 4
	}
}

// result 是命令的 JSON 输出结构；候选仅含群名称与 group_id，不含群简介、公告等其他字段。
type result struct {
	Status     string          `json:"status"`
	Table      string          `json:"table"`
	Name       string          `json:"name,omitempty"`
	PageSize   int             `json:"page_size"`
	SHA256     string          `json:"source_sha256"`
	Candidates []groups.Group  `json:"candidates"`
}

func writeResult(stdout io.Writer, value result) {
	encoder := json.NewEncoder(stdout)
	encoder.SetEscapeHTML(false)
	if err := encoder.Encode(value); err != nil {
		fmt.Fprintf(os.Stderr, "输出 JSON: %v\n", err)
		os.Exit(1)
	}
}
