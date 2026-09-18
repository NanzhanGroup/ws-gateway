package gateway

// taskcontext.go — W4「网关同步」：把 ws-core 中枢的任务清单注入各网关系统提示
//
// ── 为什么需要它 ──
//
// taskhub.go 解决了"用户主动问"：上线播报一次 + 用户发 `#<编号> <指令>#` 才转中枢。
// 但**对话中的 LLM 自己不知道任务**：用户问"我的任务跑到哪了""有几个在跑"，
// 智能体只能答"我不知道"，必须先教会用户发井号格式——体验断层。
//
// 本文件补上另一半：把【活跃任务摘要】作为**只读引用**注入网关系统提示
// （各网关 gateway_refs.go 与 SOUL.md / MEMORY.md / USER.md 并列），
// 于是用户用自然语言问，智能体答得出。这就是 W4 里"替代记忆提炼"的落点：
// 与其每晚提炼一份会过期的记忆，不如让智能体随时看到**实时任务事实**。
//
// ── 设计要点（与清歌约定的口径）──
//
//  1. **共享层一份实现**：逻辑放 ws-gateway（所有网关都 import），
//     各网关只加一行 TaskContextRef("<渠道名>")，绝不在 6 个网关里各写一份；
//  2. **只读**：本文件只拉列表、只渲染文本，**不下发任何指令**。写操作仍走
//     taskhub.go 的显式 `#<编号> <指令>#` 定界符通道（自然语言写指令本期后置，
//     因为网关侧误判会吞掉用户正常消息）；
//  3. **配额**：最多 5 条 + 总长 600 字，**无任务时返回空串**（不污染 prompt）；
//  4. **TTL 30s**：prompt 每轮都要拼，但中枢不能每轮都打——30 秒内复用同一份
//     结果（含"空结果"也缓存，避免无任务时每轮白打一次 HTTP）；
//  5. **静默降级**：中枢不可达时 ListActive 会退到本地快照并**显式标注**（⚠️ 快照），
//     让智能体知道这不是实时状态；连快照都没有就返回空串，绝不编造任务；
//  6. **可关闭**：WS_TASKHUB_CONTEXT=0/off/false 关闭注入（与 WS_TASKHUB_ANNOUNCE 同风格）。

import (
	"fmt"
	"os"
	"sort"
	"strings"
	"sync"
	"time"
)

// ── 常量 ──

const (
	// EnvTaskContext 置 0/false/off 可关闭"任务上下文注入"（默认开启）
	EnvTaskContext = "WS_TASKHUB_CONTEXT"

	// DefaultTaskContextMaxTasks 注入的最大任务条数
	DefaultTaskContextMaxTasks = 5

	// DefaultTaskContextMaxChars 注入文本的总长上限（字符数，含标题行）
	DefaultTaskContextMaxChars = 600

	// DefaultTaskContextTTL 注入文本的缓存时长（避免每轮打中枢）
	DefaultTaskContextTTL = 30 * time.Second
)

// TaskContextSource 任务来源。*TaskHub 天然满足（ListActive 自带快照兜底）；
// 测试可注入假实现，无需起 HTTP 服务。
type TaskContextSource interface {
	ListActive() ([]HubTask, bool, error)
}

// TaskContextEnabled 注入是否启用（默认启用；WS_TASKHUB_CONTEXT=0/false/off 关闭）。
func TaskContextEnabled() bool {
	v := strings.ToLower(strings.TrimSpace(os.Getenv(EnvTaskContext)))
	return !(v == "0" || v == "false" || v == "off")
}

// ── 缓存器 ──

// TaskContext 任务上下文缓存器：按 TTL 复用渲染结果，空结果同样缓存。
//
// 并发安全（网关多协程处理不同用户消息，会并发拼 prompt）；now 字段仅为可测性
// 注入时钟，生产请勿改动。
type TaskContext struct {
	Name     string // 渠道名（决定快照文件路径，如 qq/weixin）
	TTL      time.Duration
	MaxTasks int
	MaxChars int
	Source   TaskContextSource

	now    func() time.Time
	mu     sync.Mutex
	cached string
	at     time.Time
	filled bool
}

// NewTaskContext 构造缓存器（TTL/配额取默认值，零值兜底见 Block）。
func NewTaskContext(name string, src TaskContextSource) *TaskContext {
	return &TaskContext{
		Name:     name,
		TTL:      DefaultTaskContextTTL,
		MaxTasks: DefaultTaskContextMaxTasks,
		MaxChars: DefaultTaskContextMaxChars,
		Source:   src,
		now:      time.Now,
	}
}

// Block 返回本次注入的正文（不含标题；空串 = 不注入）。
// 命中 TTL 缓存直接返回；否则拉一次中枢（失败则静默降级为空）。
func (c *TaskContext) Block() string {
	if c == nil || c.Source == nil {
		return ""
	}
	nowFn := c.now
	if nowFn == nil {
		nowFn = time.Now
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.filled && nowFn().Sub(c.at) < c.ttl() {
		return c.cached
	}
	text := ""
	if tasks, fromSnapshot, err := c.Source.ListActive(); err == nil {
		text = RenderTaskContext(tasks, fromSnapshot, c.maxTasks(), c.maxChars())
	}
	// err != nil：中枢不可达且无快照 → 保持空串（静默降级，不编造任务）
	c.cached, c.at, c.filled = text, nowFn(), true
	return text
}

// Invalidate 主动失效缓存（网关收到任务指令后调用，让下一条消息立刻看到新状态）。
func (c *TaskContext) Invalidate() {
	if c == nil {
		return
	}
	c.mu.Lock()
	c.filled = false
	c.mu.Unlock()
}

func (c *TaskContext) ttl() time.Duration {
	if c.TTL <= 0 {
		return DefaultTaskContextTTL
	}
	return c.TTL
}

func (c *TaskContext) maxTasks() int {
	if c.MaxTasks <= 0 {
		return DefaultTaskContextMaxTasks
	}
	return c.MaxTasks
}

func (c *TaskContext) maxChars() int {
	if c.MaxChars <= 0 {
		return DefaultTaskContextMaxChars
	}
	return c.MaxChars
}

// ── 渲染（纯函数，便于单测）──

// RenderTaskContext 把活跃任务渲染成注入文本；无任务返回空串。
//
// 排序：执行中/已认领/已中断 等"需要用户关注"的排前面，其余按短号升序——
// 让最要紧的任务永远在配额内，不会因为短号大而被 5 条上限挤掉。
func RenderTaskContext(tasks []HubTask, fromSnapshot bool, maxTasks, maxChars int) string {
	if len(tasks) == 0 {
		return ""
	}
	if maxTasks <= 0 {
		maxTasks = DefaultTaskContextMaxTasks
	}
	if maxChars <= 0 {
		maxChars = DefaultTaskContextMaxChars
	}
	sorted := make([]HubTask, len(tasks))
	copy(sorted, tasks)
	sort.SliceStable(sorted, func(i, j int) bool {
		pi, pj := taskContextRank(sorted[i].Status), taskContextRank(sorted[j].Status)
		if pi != pj {
			return pi < pj
		}
		return sorted[i].ShortNo < sorted[j].ShortNo
	})

	var lines []string
	if fromSnapshot {
		lines = append(lines, "（⚠️ 任务中枢暂不可达，以下为本地快照，可能不是最新）")
	}
	shown := 0
	for _, t := range sorted {
		if shown >= maxTasks {
			break
		}
		lines = append(lines, "  "+FormatTaskLine(t))
		shown++
	}
	if rest := len(sorted) - shown; rest > 0 {
		lines = append(lines, fmt.Sprintf("  …另有 %d 个任务（可用 `#<编号> 详情#` 查看）", rest))
	}
	lines = append(lines, "  （用户在对话里问任务进度时，据以上回答；操作任务需用户发 `#<编号> <指令>#`）")
	return truncateRunes(strings.Join(lines, "\n"), maxChars-1)
}

// taskContextRank 注入排序优先级（越小越靠前）。
func taskContextRank(status string) int {
	switch status {
	case "interrupted":
		return 0 // 已中断：最需要用户决定是否续跑
	case "running", "leased":
		return 1 // 正在跑：用户最常问
	case "paused":
		return 2
	case "pending":
		return 3
	default:
		return 4
	}
}

// ── 各网关接入入口 ──

// taskContextRegistry 按渠道名缓存 TaskContext（网关进程内单例）。
var taskContextRegistry sync.Map // name -> *TaskContext

// TaskContextRef 各网关一行接入：返回注入正文（空串 = 不注入）。
//
// 用法（gateway_refs.go，与 SOUL.md / MEMORY.md / USER.md 并列）：
//
//	if s := gateway.TaskContextRef("qq"); s != "" {
//		parts = append(parts, "【任务清单 · ws-core 中枢】\n"+s)
//	}
func TaskContextRef(name string) string {
	if !TaskContextEnabled() {
		return ""
	}
	holder, _ := taskContextRegistry.LoadOrStore(name, NewTaskContext(name,
		NewTaskHub(TaskHubAddrFromEnv(), "", DefaultTaskHubSnapshotPath(name))))
	return holder.(*TaskContext).Block()
}

// InvalidateTaskContext 失效某渠道的任务缓存（网关执行完任务指令后调用）。
func InvalidateTaskContext(name string) {
	if holder, ok := taskContextRegistry.Load(name); ok {
		holder.(*TaskContext).Invalidate()
	}
}
