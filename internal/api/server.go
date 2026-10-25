// 本文件提供 API 服务的启动/停止/换地址生命周期管理，供托盘入口使用。
package api

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"os"
	"sync"
	"time"
)

// ErrAlreadyRunning 表示服务已在监听，不能重复启动。
var ErrAlreadyRunning = errors.New("API 服务已在运行")

// Server 管理统一发送 API 的监听与关闭；Start/Stop 可多次交替调用。
type Server struct {
	addr string
	handle API

	mu       sync.Mutex
	listener net.Listener
	server   *http.Server
}

// NewServer 创建服务；addr 传空串或 :0 时由系统分配端口，实际地址用 Addr 查询。
func NewServer(addr string, handle API) *Server {
	return &Server{addr: addr, handle: handle}
}

// Start 开始监听并异步提供服务；已在运行时返回 ErrAlreadyRunning。
func (s *Server) Start() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.server != nil {
		return ErrAlreadyRunning
	}
	listener, err := net.Listen("tcp", s.addr)
	if err != nil {
		return err
	}
	// 局部捕获，避免 SetAddr 的停启间隙里 goroutine 读到 nil 或新对象。
	server := &http.Server{Handler: NewMux(s.handle)}
	s.listener = listener
	s.server = server
	go func() {
		// Serve 返回即服务已停止；关闭错误除 ErrServerClosed 外不静默吞掉。
		if err := server.Serve(listener); err != nil && !errors.Is(err, http.ErrServerClosed) {
			fmt.Fprintf(os.Stderr, "API 服务异常退出: %v\n", err)
		}
	}()
	return nil
}

// Stop 停止监听并等待在途请求结束；未运行时是空操作。
func (s *Server) Stop() error {
	s.mu.Lock()
	server := s.server
	s.server = nil
	s.listener = nil
	s.mu.Unlock()
	if server == nil {
		return nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	return server.Shutdown(ctx)
}

// SetAddr 更换监听地址并保持既定运行状态：运行中先停再按新地址启动，
// 未运行时只记录地址供下次 Start 使用。新地址启动失败时服务保持停止并返回错误。
func (s *Server) SetAddr(addr string) error {
	s.mu.Lock()
	s.addr = addr
	running := s.server != nil
	s.mu.Unlock()
	if !running {
		return nil
	}
	if err := s.Stop(); err != nil {
		return err
	}
	return s.Start()
}

// Running 报告服务当前是否在监听。
func (s *Server) Running() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.server != nil
}

// Addr 返回实际监听地址；未运行时返回空串。
func (s *Server) Addr() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.listener == nil {
		return ""
	}
	return s.listener.Addr().String()
}
