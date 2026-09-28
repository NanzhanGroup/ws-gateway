# ws-gateway — 文殊网关通信协议库（已不再是构建依赖 · 规范源保留）

> ## ⚠️ 本仓已不再是任何组件的构建期依赖，但**暂不可删除**
>
> - 标记日期：**2026-09-29**
> - 判定依据：全 `/data/code` 内
>   `grep -rn "NanzhanGroup/ws-gateway" --include=*.go --include=go.mod` → **0 处**。
>   6 个对话网关 + device-gateway 已改为**内联副本** `<repo>/internal/wsgw/`
>   （包名保持 `gateway`，故调用点零改动，仅 import 路径字符串替换）。
>
> **本仓仍是这些内联副本的规范源（source of truth），并托管分发脚本 `scripts/wsgw-client-sync.sh`：**
>
> - 改代码 / 改协议：**改本仓** → 跑 `scripts/wsgw-client-sync.sh` 同步 → 各消费仓各自提交。
> - 一致性巡检：`scripts/wsgw-client-sync.sh --check`（漂移 rc=1，建议挂监控）。
> - 若确要删除本仓：需**先把规范源 + 脚本迁移到别处**（如某消费仓的 `internal/wsgw/`，
>   或一个独立的私有仓），否则 7 处内联副本将失去单一起源。
>
> 另：本仓为**公开仓**（GitHub API 200）。仓内 `wscmd.go` / `taskhub.go` 含内部指令与任务中枢协议 ——
> 内联只斩断了「依赖」，**源码本身仍对外可见**。是否转私有 / 归档由所有者决定（本次不处理）。

## 编译

此项目是 library 包，不产生独立二进制。

```bash
go build ./... && go test ./...
```

## 历史依赖方（均已改为内联副本 `<repo>/internal/wsgw/`）

| 项目 | 现状态 |
|---|---|
| `qq` / `weixin` / `wecom` / `feishu` / `telegram-gateway` | 已内联 `internal/wsgw/` |
| `email-gateway` | 已内联 `internal/wsgw/` |
| `device-gateway/server` | 已内联 `internal/wsgw/` |
| `chat` | 经网关间接引用，已无直接依赖 |

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

## 变更记录

- **2026-09-29**：新增 `scripts/wsgw-client-sync.sh`，共享包内联分发至 7 处消费方；
  本仓自此不再是任何组件的构建期依赖，转为「规范源 + 分发脚本」角色。
- **2026-09-25**：内置命令 WSC 接入层（`wscmd.go`）—— pre-LLM 拦截，不经 LLM。
