package gateway

// wscmd.go — 文殊内置命令（WSC = WenShu Command）网关侧接入
//
// ── 为什么要有这一层 ──
//
// 用户发一条 `wsc:version`，正确行为是**立即执行并回结果**，而不是先唤醒 LLM 让它
// 猜"用户想干嘛"再调工具——那样又慢又烧 token，还可能猜错。指令必须在**进 LLM 之前**
// 就被拦下。这是各网关 pre-LLM 拦截链的第 5.8 步（紧随任务中枢指令 `#1000 继续#`）。
//
// ── 分工 ──
//
//	网关侧（本文件）：只做「识别前缀 → 转中枢 → 原文回显」，不实现任何业务逻辑；
//	中枢侧（ws-core）：唯一实现（HandleWSC，HTTP POST /api/cmd）——版本信息、
//	模块升级、系统重启这些能力只有核心那一层才有。
//
// 一份实现服务全部渠道：weixin / qq / telegram / feishu / wecom / email 调同一个函数，
// 绝不在 6 个网关里各写一遍（与 taskhub.go 的共享口径一致）。
//
// ── 从严解析 ──
//
// 只认「**开头**就是 wsc:」的文本（容忍前导冒号与全角冒号）。像「帮我看看 wsc:version」
// 这种不开头的句子一律放行给 LLM——拦截发生在 LLM 之前，误判就会吞掉用户正常提问。
// 判定口径与 ws-core 的 wscParse 保持一致（两侧都有单测）。
//
// ── 关闭开关 ──
//
//	WS_WSC_CMD=0/false/off   关闭本渠道的内置命令拦截（默认开启）
//	WS_CORE_ADDR=host:port   指定中枢地址（默认 http://127.0.0.1:9521）

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"strings"
	"time"
)

const (
	// EnvWSCEnabled 置 0/false/off 可关闭内置命令拦截（默认开启）
	EnvWSCEnabled = "WS_WSC_CMD"
	// EnvWSCoreAddr 中枢（ws-core）HTTP API 基址
	EnvWSCoreAddr = "WS_CORE_ADDR"
	// DefaultWSCoreAddr 中枢默认地址（与 ws-core 的 -http-addr 一致，仅回环）
	DefaultWSCoreAddr = "http://127.0.0.1:9521"
	// DefaultWSCTimeout 单次内置命令超时。
	// 取值理由：version 要哈希 53 个模块（约 1~2s）+ 拉清单；升级类命令在 ws-core 侧
	// 会走「下载→解包→校验」，最坏十几秒，故给 90s 上限，避免用户长时间无响应。
	DefaultWSCTimeout = 90 * time.Second
)

// WSCEnabled 内置命令拦截是否启用（默认启用；WS_WSC_CMD=0/false/off 关闭）。
func WSCEnabled() bool {
	v := strings.ToLower(strings.TrimSpace(os.Getenv(EnvWSCEnabled)))
	return !(v == "0" || v == "false" || v == "off")
}

// WSCoreAddrFromEnv 中枢地址：WS_CORE_ADDR 优先，默认 http://127.0.0.1:9521（去掉尾部斜杠）。
func WSCoreAddrFromEnv() string {
	a := strings.TrimRight(strings.TrimSpace(os.Getenv(EnvWSCoreAddr)), "/")
	if a == "" {
		return DefaultWSCoreAddr
	}
	return a
}

// IsWSCCommand 判定文本是否为文殊内置命令（与 ws-core 的 wscParse 同口径）。
//
// 容忍：前导空白、前导冒号（:wsc:version）、全角冒号（wsc：version）、大小写。
// 拒绝：空串、前缀不在开头（"讲一下 wsc:version"→放行给 LLM）。
func IsWSCCommand(text string) bool {
	s := strings.TrimSpace(text)
	if s == "" {
		return false
	}
	s = strings.ReplaceAll(s, "：", ":")
	s = strings.TrimLeft(s, ":")
	return strings.HasPrefix(strings.ToLower(s), "wsc:")
}

// RunWSCCommand 把一条用户消息交给中枢执行（内置命令）。
//
// 返回 (reply, handled)：
//
//	handled=false → 不是内置命令，调用方**继续**走原有 LLM 流程；
//	handled=true  → 已消费该消息，reply 为要回显给用户的文本（含中枢不可达等错误提示），
//	                调用方**立即 return**，不再进 LLM。
//
// 注意：只要是内置命令（前缀命中）就一定 handled=true —— 即使中枢不可达也回显错误，
// 而不是"悄悄放给 LLM"。用户既然打了前缀，就是明确在跟系统说话。
func RunWSCCommand(text string) (string, bool) {
	reply, handled, _ := RunWSCCommandWith(WSCoreAddrFromEnv(), text, DefaultWSCTimeout)
	return reply, handled
}

// RunWSCCommandWith 与 RunWSCCommand 相同，但可指定中枢基址与超时（便于单测注入）。
func RunWSCCommandWith(baseURL, text string, timeout time.Duration) (string, bool, error) {
	if !WSCEnabled() || !IsWSCCommand(text) {
		return "", false, nil
	}
	addr := strings.TrimRight(strings.TrimSpace(baseURL), "/")
	if addr == "" {
		addr = DefaultWSCoreAddr
	}
	payload, _ := json.Marshal(map[string]string{"text": strings.TrimSpace(text)})
	client := &http.Client{Timeout: timeout}
	resp, err := client.Post(addr+"/api/cmd", "application/json", bytes.NewReader(payload))
	if err != nil {
		return fmt.Sprintf("⚠ 文殊内置命令执行失败：任务中枢不可达（%s）\n命令：%s",
			err.Error(), strings.TrimSpace(text)), true, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Sprintf("⚠ 文殊内置命令执行失败：任务中枢返回 HTTP %d", resp.StatusCode), true,
			fmt.Errorf("ws-core /api/cmd HTTP %d", resp.StatusCode)
	}
	var out struct {
		Handled bool   `json:"handled"`
		Reply   string `json:"reply"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return fmt.Sprintf("⚠ 文殊内置命令回执解析失败：%v", err), true, err
	}
	if !out.Handled {
		// 双保险：中枢认为不是内置命令 → 交回给原有流程（两侧口径不一致时不吞用户消息）
		return "", false, nil
	}
	return out.Reply, true, nil
}
