// Package gateway 提供平台网关共享类型
// 每个渠道一个独立二进制，通过 Unix socket 与核心智能体通信
// 文件通过共享文件系统传递，不经过 socket body
package gateway

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"time"
)

// DefaultSocketAddr 默认 Unix socket 地址
const DefaultSocketAddr = "/tmp/ws.sock"

// ── 文件描述 ──

// FileRef 文件引用（共享文件系统路径）
type FileRef struct {
	Path     string `json:"path"`               // 文件绝对路径
	MimeType string `json:"mime_type,omitempty"` // 如 image/png, audio/ogg, video/mp4
	Name     string `json:"name,omitempty"`      // 显示名称（含扩展名）
	Size     int64  `json:"size,omitempty"`      // 文件大小（字节）
}

// ── Agent HTTP API 类型 ──

// ChatRequest 发送给智能体的聊天请求
type ChatRequest struct {
	Platform  string    `json:"platform"`
	UserID    string    `json:"user_id"`
	NickName  string    `json:"nick_name"`
	Content   string    `json:"content"`
	AccountID string    `json:"account_id"`
	Files     []FileRef `json:"files,omitempty"` // 网关下载好的输入文件路径
}

// ChatResponse 智能体返回的聊天响应
type ChatResponse struct {
	Reply    string    `json:"reply,omitempty"`     // 文本回复
	Typing   bool      `json:"typing,omitempty"`    // 是否需要"正在输入"状态
	Files    []FileRef `json:"files,omitempty"`     // 智能体生成的输出文件路径
	Finished bool      `json:"finished"`            // 会话本轮是否已结束
}

// AgentClient 智能体 HTTP 客户端
type AgentClient struct {
	BaseURL    string
	HTTPClient *http.Client
}

// NewAgentClient 创建智能体客户端
func NewAgentClient(baseURL string) *AgentClient {
	return &AgentClient{
		BaseURL: baseURL,
		HTTPClient: &http.Client{Timeout: 300 * time.Second},
	}
}

// Chat 发送消息到智能体并返回回复
func (c *AgentClient) Chat(req ChatRequest) (*ChatResponse, error) {
	body, err := json.Marshal(req)
	if err != nil {
		return nil, fmt.Errorf("序列化请求失败: %w", err)
	}

	httpReq, err := http.NewRequest("POST", c.BaseURL+"/api/v1/chat", bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("创建请求失败: %w", err)
	}
	httpReq.Header.Set("Content-Type", "application/json")

	resp, err := c.HTTPClient.Do(httpReq)
	if err != nil {
		return nil, fmt.Errorf("请求智能体失败: %w", err)
	}
	defer resp.Body.Close()

	var chatResp ChatResponse
	if err := json.NewDecoder(resp.Body).Decode(&chatResp); err != nil {
		return nil, fmt.Errorf("解析响应失败: %w", err)
	}
	return &chatResp, nil
}

// Health 检查智能体健康状态
func (c *AgentClient) Health() error {
	resp, err := c.HTTPClient.Get(c.BaseURL + "/api/v1/chat")
	if err != nil {
		return err
	}
	resp.Body.Close()
	return nil
}

// ── 平台适配器接口 ──

// MessageEvent 从平台接收到的消息事件
type MessageEvent struct {
	Platform  string
	UserID    string
	NickName  string
	Content   string
	AccountID string
	Files     []FileRef // 平台附带的文件（已下载到本地）
}

// PlatformAdapter 平台适配器接口
type PlatformAdapter interface {
	// Name 返回平台标识
	Name() string
	// Start 启动平台连接
	Start(ctx context.Context, handler func(MessageEvent)) error
	// Send 向用户发送文本消息
	Send(to, text string) error
	// SendFile 向用户发送文件
	SendFile(to string, file FileRef) error
	// SendTyping 发送"正在输入"状态
	SendTyping(to string) error
}

// ── 文件工具 ──

// TempDir 创建文件中转临时目录（网关用）
// 返回：目录路径 + 清理函数（结束后调用）
func TempDir() (string, func(), error) {
	dir, err := os.MkdirTemp("", "ws-gateway-*")
	if err != nil {
		return "", nil, err
	}
	return dir, func() { os.RemoveAll(dir) }, nil
}

// SaveFile 将 io.Reader 保存到中转目录
func SaveFile(dir, name string, src io.Reader) (*FileRef, error) {
	path := filepath.Join(dir, name)
	f, err := os.Create(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	size, err := io.Copy(f, src)
	if err != nil {
		return nil, err
	}
	return &FileRef{
		Path: path,
		Name: name,
		Size: size,
	}, nil
}

// CopyFile 将源文件复制到中转目录
func CopyFile(dir, srcPath string) (*FileRef, error) {
	src, err := os.Open(srcPath)
	if err != nil {
		return nil, err
	}
	defer src.Close()
	return SaveFile(dir, filepath.Base(srcPath), src)
}

// SendFileReply 向智能体发送带文件的请求
// 便捷方法：网关收到平台文件后调用此函数
func (c *AgentClient) SendFileReply(req ChatRequest) (*ChatResponse, error) {
	return c.Chat(req)
}

// SocketSend 通过 Unix socket 发送聊天请求并接收响应
func SocketSend(socketPath string, req ChatRequest) (*ChatResponse, error) {
	conn, err := net.DialTimeout("unix", socketPath, 10*time.Second)
	if err != nil {
		return nil, fmt.Errorf("连接 agent socket 失败: %w", err)
	}
	defer conn.Close()

	// 发送请求
	body, err := json.Marshal(req)
	if err != nil {
		return nil, fmt.Errorf("序列化失败: %w", err)
	}
	if _, err := conn.Write(body); err != nil {
		return nil, fmt.Errorf("发送失败: %w", err)
	}

	// 读取响应
	conn.SetReadDeadline(time.Now().Add(120 * time.Second))
	respBody, err := io.ReadAll(conn)
	if err != nil {
		return nil, fmt.Errorf("读取响应失败: %w", err)
	}

	var resp ChatResponse
	if err := json.Unmarshal(respBody, &resp); err != nil {
		return nil, fmt.Errorf("解析响应失败: %w", err)
	}
	return &resp, nil
}
