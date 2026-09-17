package gateway

import (
	"strings"
	"testing"
)

// 上线统一播报：任务清单 + 中断对话合并为一条。
func TestFormatStartupTasks_Merged(t *testing.T) {
	tasks := []HubTask{
		{ShortNo: 1000, Title: "ws-win 追平 WorkBuddy", Status: "running", StepDone: 2, StepTotal: 10},
	}
	interrupted := []InterruptedSession{
		{Summary: "1000 重试", TaskID: "T457323996415", Step: 3, StepName: "执行子任务"},
	}
	out := FormatStartupTasks(tasks, false, interrupted, false)

	if !strings.Contains(out, "任务清单") || !strings.Contains(out, "1 个活跃任务") {
		t.Fatalf("缺少任务清单表头: %q", out)
	}
	if !strings.Contains(out, "#1000") {
		t.Fatalf("缺少任务行: %q", out)
	}
	if !strings.Contains(out, "中断") || !strings.Contains(out, "1000 重试") {
		t.Fatalf("缺少中断对话小节: %q", out)
	}
	if !strings.Contains(out, "第 3 步：执行子任务") {
		t.Fatalf("缺少断点步骤: %q", out)
	}
	// 手动确认（autoResume=false）应给出「继续」/「取消」指引
	if !strings.Contains(out, "「继续」") || !strings.Contains(out, "「取消」") {
		t.Fatalf("缺少继续/取消指引: %q", out)
	}
	// 必须只有一条消息（无重复表头）
	if strings.Count(out, "任务清单") != 1 {
		t.Fatalf("任务清单表头应只出现一次: %q", out)
	}
}

// 自动恢复（wecom/feishu/telegram）：不应出现「继续」/「取消」指引。
func TestFormatStartupTasks_AutoResume(t *testing.T) {
	out := FormatStartupTasks(nil, false, []InterruptedSession{{Summary: "早上好"}}, true)
	if !strings.Contains(out, "当前无活跃任务") {
		t.Fatalf("无活跃任务时表头不正确: %q", out)
	}
	if !strings.Contains(out, "自动恢复") {
		t.Fatalf("缺少自动恢复说明: %q", out)
	}
	if strings.Contains(out, "「继续」") {
		t.Fatalf("自动恢复模式不应出现「继续」指引: %q", out)
	}
}

// 只有中枢任务、无中断对话：与旧 FormatTaskList 等价（不含中断小节）。
func TestFormatStartupTasks_TasksOnly(t *testing.T) {
	tasks := []HubTask{{ShortNo: 1000, Title: "T", Status: "paused"}}
	out := FormatStartupTasks(tasks, false, nil, false)
	if strings.Contains(out, "中断") {
		t.Fatalf("无中断对话时不应出现中断小节: %q", out)
	}
	if !strings.Contains(out, "1000 继续") {
		t.Fatalf("缺少指令用法提示: %q", out)
	}
}

// 既无任务也无中断：返回空串，调用方跳过发送。
func TestFormatStartupTasks_Empty(t *testing.T) {
	if got := FormatStartupTasks(nil, false, nil, false); got != "" {
		t.Fatalf("空输入应返回空串，得到 %q", got)
	}
	if got := FormatStartupTasks([]HubTask{}, false, []InterruptedSession{}, true); got != "" {
		t.Fatalf("空切片应返回空串，得到 %q", got)
	}
}

// 快照兜底：fromSnapshot=true 时标注"本地快照"。
func TestFormatStartupTasks_Snapshot(t *testing.T) {
	out := FormatStartupTasks([]HubTask{{ShortNo: 1000, Title: "T", Status: "running"}}, true, nil, false)
	if !strings.Contains(out, "本地快照") {
		t.Fatalf("快照模式应标注: %q", out)
	}
}
