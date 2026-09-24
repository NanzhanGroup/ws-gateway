# ws-gateway — 文殊网关通信协议库

**网关通信协议库**，提供统一的 ChatRequest/ChatResponse 数据结构以及 Unix socket 通信（SocketSend）、PID 单例保护（LockPIDFile）等基础设施。不是独立二进制，被各网关项目 import 使用。

## 编译

此项目是 library 包，不产生独立二进制。被以下项目 import：

```go
import "github.com/NanzhanGroup/ws-gateway"
```

## 依赖方

| 项目               | 用途                           |
|--------------------|--------------------------------|
| `weixin-gateway`   | socket 通信、PID 保护          |
| `email-gateway`    | socket 通信、PID 保护          |
| `telegram-gateway` | Agent 客户端、文件处理          |
| `chat`             | 被引用（间接依赖）             |

## 提供的核心功能

- **AgentClient** — 通过 Unix socket 或 HTTP 调用 Agent
- **SocketSend** — 向 Unix socket 发送 ChatRequest 并接收 ChatResponse
- **LockPIDFile / UnlockPIDFile** — PID 单例保护
- **TempDir / SaveFile** — 临时文件和附件处理
- **ChatRequest / ChatResponse** — 统一网关通信协议
- **FileRef** — 文件引用结构（用于附件传递）
- **任务中枢接入**（`taskhub.go` / `taskcontext.go`）— 活跃任务清单、`#1000 继续#` 指令解析与下发、系统提示注入
- **内置命令接入**（`wscmd.go`，0.13.0）— 文殊内置命令（`wsc:version` / `wsc:upgrade-module`）的
  pre-LLM 拦截：`gateway.RunWSCCommand(content)` → `(reply, handled)`，命中即由 ws-core 直接执行、
  不经 LLM。判定口径与 ws-core 一致；`WS_WSC_CMD=0` 关闭，`WS_CORE_ADDR` 指定中枢地址。
  各渠道调用位置：紧跟「任务中枢指令（`#1000 继续#`）」之后（weixin/qq/telegram/feishu/wecom 的
  `process.go`、email 的 `main.go`、device-gateway/server 的 `brain.go`）。
