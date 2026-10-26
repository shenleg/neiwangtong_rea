// 本文件以独占创建、逐条Sync保存运行事实和发送摘要；不保存聊天正文，不覆盖旧记录或自动换名重试。
package nativeweb

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"sync"

	"neiwangtong-gateway/internal/nativeproto"
)

type journal struct {
	mu   sync.Mutex
	file *os.File
}

func newJournal(path string) (*journal, error) {
	file, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if err != nil {
		return nil, fmt.Errorf("独占创建运行记录: %w", err)
	}
	return &journal{file: file}, nil
}

func (j *journal) write(value any) error {
	j.mu.Lock()
	defer j.mu.Unlock()
	if err := json.NewEncoder(j.file).Encode(value); err != nil {
		return err
	}
	return j.file.Sync()
}

func (j *journal) close() error {
	j.mu.Lock()
	defer j.mu.Unlock()
	return j.file.Close()
}

// saveDirectory 每次上线保存一份新快照，保留历史和失败文件；没有静默覆盖或失败后回退。
func saveDirectory(path string, directory *nativeproto.Directory) error {
	file, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if err != nil {
		return err
	}
	writeErr := directory.Write(file)
	if writeErr == nil {
		writeErr = file.Sync()
	}
	return errors.Join(writeErr, file.Close())
}
