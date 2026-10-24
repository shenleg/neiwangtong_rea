// 本程序提供联系人姓名到 user_id 的只读查询命令入口：解密离线 nr 副本并输出 JSON，
// 不发送消息、不修改配置、不写数据库文件。口令从标准输入读精确字节直到 EOF。
//
// 退出码：0 唯一命中或列表成功；1 文件、口令、身份或查询错误；2 参数错误；
// 3 名称未命中；4 同名歧义，候选已全部输出，不能自动选择。
package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"

	"neiwangtong-gateway/internal/contacts"
)

func main() {
	os.Exit(run(os.Args[1:], os.Stdin, os.Stdout, os.Stderr))
}

// run 解析参数并执行一次查询，返回进程退出码；输出 JSON 写入 stdout，诊断写入 stderr。
func run(args []string, stdin io.Reader, stdout, stderr io.Writer) int {
	flags := flag.NewFlagSet("contactlookup", flag.ContinueOnError)
	flags.SetOutput(stderr)
	nrPath := flags.String("nr", "", "完整离线nr副本的绝对路径")
	table := flags.String("table", "", "身份表名；库中只有一张时可省略，多张时必须指定")
	name := flags.String("name", "", "联系人原始名称的精确值；缺省时输出全部用户联系人")
	if err := flags.Parse(args); err != nil {
		return 2
	}
	if flags.NArg() != 0 {
		fmt.Fprintln(stderr, "不接受位置参数")
		return 2
	}
	if *nrPath == "" {
		fmt.Fprintln(stderr, "必须用 -nr 指定离线副本路径")
		return 2
	}
	password, err := io.ReadAll(stdin)
	if err != nil {
		fmt.Fprintf(stderr, "读取口令: %v\n", err)
		return 1
	}
	db, err := contacts.Open(*nrPath, password)
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
		users, err := db.LoadUsers(target)
		if err != nil {
			fmt.Fprintf(stderr, "读取联系人: %v\n", err)
			return 1
		}
		writeResult(stdout, result{Status: "listed", Table: target, PageSize: db.PageSize(),
			SHA256: db.SHA256(), Candidates: users})
		return 0
	}
	matches, err := db.LookupName(target, *name)
	if err != nil {
		fmt.Fprintf(stderr, "查询联系人: %v\n", err)
		return 1
	}
	resultTemplate := result{Name: *name, Table: target, PageSize: db.PageSize(), SHA256: db.SHA256()}
	switch len(matches) {
	case 0:
		resultTemplate.Status = "not_found"
		writeResult(stdout, resultTemplate)
		return 3
	case 1:
		resultTemplate.Status = "unique"
		resultTemplate.Candidates = matches
		writeResult(stdout, resultTemplate)
		return 0
	default:
		resultTemplate.Status = "ambiguous"
		resultTemplate.Candidates = matches
		writeResult(stdout, resultTemplate)
		return 4
	}
}

// result 是命令的 JSON 输出结构；候选仅含原始名称与 user_id，不含其他个人资料。
type result struct {
	Status     string             `json:"status"`
	Table      string             `json:"table"`
	Name       string             `json:"name,omitempty"`
	PageSize   int                `json:"page_size"`
	SHA256     string             `json:"source_sha256"`
	Candidates []contacts.Contact `json:"candidates"`
}

func writeResult(stdout io.Writer, value result) {
	encoder := json.NewEncoder(stdout)
	encoder.SetEscapeHTML(false)
	if err := encoder.Encode(value); err != nil {
		fmt.Fprintf(os.Stderr, "输出 JSON: %v\n", err)
		os.Exit(1)
	}
}
