package gateway

// startup_interrupts.go — 上线播报：上次重启时中断的对话（渠道无关）
//
// 2026-11-05：任务中枢（ws-core /api/tasks、#短号 指令、任务清单注入）已整体移除，
// 原 taskhub.go 里的 InterruptedSession / FormatStartupTasks 一并下线。
// 但「网关重启时中断的对话」是渠道本地 processing 队列的事实，与任务体系无关，
// 用户需要知道「上次那条还没处理完」以及怎么恢复 —— 故把这一小块独立保留在这里。
//
// 与任务体系彻底脱钩：本文件不访问任何 HTTP、不读快照、不含任务清单。

import (
	"fmt"
	"strings"
)

// InterruptedSession 一条「网关重启时中断的对话处理」的渠道无关视图。
//
// 各网关从本地 processing 队列转换而来，共享同一份渲染逻辑，保证 6 个渠道体验一致。
type InterruptedSession struct {
	Summary  string // 用户消息摘要
	TaskID   string // 会话任务 ID（可空）
	Step     int    // 当前步骤序号（1 起；0 = 无断点）
	StepName string // 当前步骤名
}

// Line 渲染一条中断对话的摘要行（含断点步骤）。
func (it InterruptedSession) Line() string {
	s := strings.TrimSpace(it.Summary)
	if s == "" {
		s = "（消息内容为空）"
	}
	if it.StepName != "" {
		s += fmt.Sprintf("（第 %d 步：%s）", it.Step, it.StepName)
	} else if it.Step > 0 {
		s += fmt.Sprintf("（第 %d 步）", it.Step)
	}
	return s
}

// FormatInterruptedSessions 渲染「上次重启中断的对话」播报正文。
//
// autoResume=true：网关会自行恢复（wecom/feishu/telegram）；
// autoResume=false：需用户回复「继续」/「取消」确认（qq/weixin）。
//
// 返回空串表示无内容可播报，调用方据此跳过发送（网关重启频繁，空播报=骚扰）。
func FormatInterruptedSessions(interrupted []InterruptedSession, autoResume bool) string {
	if len(interrupted) == 0 {
		return ""
	}
	var sb strings.Builder
	if autoResume {
		sb.WriteString(fmt.Sprintf("🔄 **有 %d 条对话在上次重启时中断，正在自动恢复**：\n", len(interrupted)))
	} else {
		sb.WriteString(fmt.Sprintf("🔄 **有 %d 条对话在上次重启时中断**（回复「继续」恢复处理 / 「取消」放弃）：\n", len(interrupted)))
	}
	for _, it := range interrupted {
		sb.WriteString("  · ")
		sb.WriteString(it.Line())
		sb.WriteString("\n")
	}
	return strings.TrimRight(sb.String(), "\n")
}
