// Package gateway 提供平台网关共享类型
//
// taskhub.go — 任务中枢（ws-core）续跑交互共享组件
//
// ── 为什么需要它 ──
//
// ws-core 是"任务中枢"：任务带 7 态状态机、4 位用户短号、指令通道，且
// 下线时任务与断点全程落盘。devie-gateway（设备侧）在 P1 期已接上这条链路，
// 但**所有对话网关（qq/weixin/wecom/feishu/telegram/email）都没有接**：
// 用户换了渠道就问不出"我的任务现在什么状态"，也无法叫停。
//
// 本文件把这段交互抽成"渠道无关"的共享组件（所有网关都依赖 ws-gateway 模块），
// 保证各渠道体验完全一致：
//
//	下线任务落盘（ws-core 侧）→ 上线网关给出【任务列表 + 状态】
//	→ 用户回复 `<4 位任务 ID> <指令>` → 网关转中枢 → 回显结果
//
// ── 设计要点 ──
//
//  1. **零新依赖**：只用标准库（net/http + encoding/json + regexp）；
//  2. **只产出文本**：各渠道 sendMsg 签名不同，共享组件不碰发送，只做
//     "拉列表 / 记快照 / 格式化 / 解析指令 / 转指令"，由网关侧挂钩子；
//  3. **快照兜底**：每次成功拉取都落盘快照，ws-core 短暂不可用时仍能列出
//     "上次已知的任务清单"（标注为快照），实现"下线落盘、上线可列"；
//  4. **解析从严**：必须"4 位数字 + 已知指令词"才拦截，绝不吞掉正常聊天
//     （例如"2024 年的合同"不会命中，因为"年的合同"不是指令词）。
package gateway

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"
)

// ── 常量 ──

const (
	// DefaultTaskHubAddr 任务中枢默认地址（ws-core HTTP API，本机回环）
	DefaultTaskHubAddr = "http://127.0.0.1:9521"

	// EnvTaskHubAddr 覆盖中枢地址的环境变量
	EnvTaskHubAddr = "WS_CORE_API"

	// EnvTaskHubAnnounce 置 0 可关闭"上线播报任务清单"（默认开启）
	EnvTaskHubAnnounce = "WS_TASKHUB_ANNOUNCE"

	// TaskHubSnapshotVersion 快照文件格式版本（不兼容变更时 +1）
	TaskHubSnapshotVersion = 1
)

// ── 数据结构 ──

// HubTask 任务中枢里的一条任务（字段与 ws-core config.go 的 Task 对齐，
// 只取网关展示/交互需要的子集；多余字段忽略）。
type HubTask struct {
	ID       int64  `json:"id"`
	TaskID   string `json:"task_id,omitempty"`
	ShortNo  int    `json:"short_no,omitempty"`
	Title    string `json:"title"`
	Goal     string `json:"goal,omitempty"`
	Status   string `json:"status"`
	Executor string `json:"executor,omitempty"`
	Owner    string `json:"owner,omitempty"`
	StepDone int    `json:"step_done"`
	// StepTotal 总步骤数
	StepTotal int `json:"step_total"`
	// ResumeEpoch 第几次续跑
	ResumeEpoch int `json:"resume_epoch"`
	// InterruptReason 中断原因（graceful-shutdown | crash-recovered | plan-changed）
	InterruptReason string `json:"interrupt_reason,omitempty"`
	Priority        int    `json:"priority"`
	CreatedAt       string `json:"created_at,omitempty"`
	UpdatedAt       string `json:"updated_at,omitempty"`
	Result          string `json:"result,omitempty"`
}

// taskHubSnapshot 快照文件结构（落盘，供 ws-core 不可用时兜底）
type taskHubSnapshot struct {
	Version   int       `json:"version"`
	Addr      string    `json:"addr"`
	FetchedAt string    `json:"fetched_at"`
	Tasks     []HubTask `json:"tasks"`
}

// TaskHub 任务中枢客户端。零值不可用，请用 NewTaskHub 构造。
type TaskHub struct {
	BaseURL      string // 中枢地址（如 http://127.0.0.1:9521）
	Owner        string // 只关心该归属的任务（空 = 全部）
	SnapshotPath string // 快照落盘路径（空 = 不落盘）
	Client       *http.Client
}

// NewTaskHub 构造任务中枢客户端。
//   - baseURL 为空时用 DefaultTaskHubAddr
//   - owner 为空表示不过滤归属
//   - snapshotPath 为空表示不落盘快照
func NewTaskHub(baseURL, owner, snapshotPath string) *TaskHub {
	baseURL = strings.TrimSpace(baseURL)
	if baseURL == "" {
		baseURL = DefaultTaskHubAddr
	}
	baseURL = strings.TrimRight(baseURL, "/")
	return &TaskHub{
		BaseURL:      baseURL,
		Owner:        strings.TrimSpace(owner),
		SnapshotPath: snapshotPath,
		Client:       &http.Client{Timeout: 8 * time.Second},
	}
}

// TaskHubAddrFromEnv 解析中枢地址：环境变量 → 默认回环地址。
func TaskHubAddrFromEnv() string {
	if v := strings.TrimSpace(os.Getenv(EnvTaskHubAddr)); v != "" {
		return strings.TrimRight(v, "/")
	}
	return DefaultTaskHubAddr
}

// TaskHubAnnounceEnabled 上线播报是否启用（默认启用，置 0 关闭）。
func TaskHubAnnounceEnabled() bool {
	v := strings.TrimSpace(os.Getenv(EnvTaskHubAnnounce))
	return !(v == "0" || v == "false" || v == "off")
}

// DefaultTaskHubSnapshotPath 生成默认快照路径：$WS_PATH/data/taskhub/<name>.snapshot.json。
// wsPath 为空时按 WS_PATH 环境变量 → /data/app/ws 兜底。
func DefaultTaskHubSnapshotPath(name string) string {
	wsPath := strings.TrimSpace(os.Getenv("WS_PATH"))
	if wsPath == "" {
		wsPath = "/data/app/ws"
	}
	if name == "" {
		name = "gateway"
	}
	name = strings.NewReplacer("/", "_", "\\", "_", "..", "_").Replace(name)
	return filepath.Join(wsPath, "data", "taskhub", name+".snapshot.json")
}

// ── HTTP ──

// List 拉取任务列表（status 为空 = 全部；limit<=0 时用 100）。
func (h *TaskHub) List(status string, limit int) ([]HubTask, error) {
	if limit <= 0 {
		limit = 100
	}
	url := fmt.Sprintf("%s/api/tasks?limit=%d", h.BaseURL, limit)
	if status != "" {
		url += "&status=" + status
	}
	body, err := h.get(url)
	if err != nil {
		return nil, err
	}
	var tasks []HubTask
	if err := json.Unmarshal(body, &tasks); err != nil {
		return nil, fmt.Errorf("解析任务列表失败: %w", err)
	}
	return tasks, nil
}

// ListActive 拉取"活跃（非终态）"任务列表，并在成功时写入快照。
// 返回的第二个值表示是否来自快照兜底（true = 中枢不可用，用本地快照）。
func (h *TaskHub) ListActive() ([]HubTask, bool, error) {
	all, err := h.List("", 100)
	if err == nil {
		active := make([]HubTask, 0, len(all))
		for _, t := range all {
			if !IsTerminalStatus(t.Status) {
				active = append(active, t)
			}
		}
		sort.SliceStable(active, func(i, j int) bool { return active[i].ShortNo < active[j].ShortNo })
		h.SaveSnapshot(active)
		return active, false, nil
	}
	// 兜底：本地快照
	if snap, serr := h.LoadSnapshot(); serr == nil && len(snap.Tasks) > 0 {
		return snap.Tasks, true, nil
	}
	return nil, false, err
}

// Get 按 4 位短号取任务详情。
func (h *TaskHub) Get(short int) (*HubTask, error) {
	if err := ValidateShortNo(short); err != nil {
		return nil, err
	}
	body, err := h.get(fmt.Sprintf("%s/api/tasks/short/%d", h.BaseURL, short))
	if err != nil {
		return nil, err
	}
	var t HubTask
	if err := json.Unmarshal(body, &t); err != nil {
		return nil, fmt.Errorf("解析任务详情失败: %w", err)
	}
	return &t, nil
}

// Command 向中枢下发指令（继续/暂停/停止/取消/删除）。
// 返回中枢给用户的可读结果文本（删除未确认时是"请二次确认"提示）。
//
// 入参 cmd **接受用户原话**（中文/英文别名均可，如 "pause"/"确认删除"/"rm"），
// 本方法内部统一归一化为中枢的标准指令词后再下发。
// 这一点是刻意做成"防呆"的：调用方（各渠道网关）会直接把用户输入透传进来，
// 若要求调用方自己归一化，早晚有人漏掉别名 → 中枢返回「未知指令」。
// 别名「确认删除」同时等价于"删除 + confirm=true"（用户已经明确二次确认）。
func (h *TaskHub) Command(short int, cmd string, confirm bool) (string, error) {
	if err := ValidateShortNo(short); err != nil {
		return "", err
	}
	raw := strings.ToLower(strings.TrimSpace(cmd))
	verb := NormalizeTaskHubVerb(raw)
	if verb == "" {
		return "", fmt.Errorf("未知指令 %q（可选：继续/暂停/停止/取消/删除）", cmd)
	}
	if verb == "详情" {
		return "", fmt.Errorf("「详情」是本地查询动作，请用 Get(short) 获取详情")
	}
	if taskHubConfirmAliases[raw] {
		confirm = true
	}
	payload, _ := json.Marshal(map[string]any{"cmd": verb, "confirm": confirm})
	url := fmt.Sprintf("%s/api/tasks/short/%d/cmd", h.BaseURL, short)
	req, err := http.NewRequest(http.MethodPost, url, bytes.NewReader(payload))
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := h.Client.Do(req)
	if err != nil {
		return "", fmt.Errorf("连接任务中枢失败: %w", err)
	}
	defer resp.Body.Close()
	body, _ := readAllLimited(resp.Body, 1<<20)
	var out struct {
		Message string `json:"message"`
		Error   string `json:"error"`
	}
	_ = json.Unmarshal(body, &out)
	if resp.StatusCode != http.StatusOK {
		if out.Error != "" {
			return "", fmt.Errorf("%s", out.Error)
		}
		return "", fmt.Errorf("任务中枢返回 HTTP %d", resp.StatusCode)
	}
	if out.Message == "" {
		return "✓ 指令已下发。", nil
	}
	return out.Message, nil
}

func (h *TaskHub) get(url string) ([]byte, error) {
	resp, err := h.Client.Get(url)
	if err != nil {
		return nil, fmt.Errorf("连接任务中枢失败: %w", err)
	}
	defer resp.Body.Close()
	body, _ := readAllLimited(resp.Body, 4<<20)
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("任务中枢返回 HTTP %d", resp.StatusCode)
	}
	return body, nil
}

// ── 快照落盘 ──

// SaveSnapshot 把活跃任务清单落盘（尽力而为，失败只返回错误不 panic）。
func (h *TaskHub) SaveSnapshot(tasks []HubTask) error {
	if h.SnapshotPath == "" {
		return nil
	}
	if err := os.MkdirAll(filepath.Dir(h.SnapshotPath), 0o755); err != nil {
		return err
	}
	snap := taskHubSnapshot{
		Version:   TaskHubSnapshotVersion,
		Addr:      h.BaseURL,
		FetchedAt: time.Now().Format(time.RFC3339),
		Tasks:     tasks,
	}
	data, err := json.MarshalIndent(snap, "", "  ")
	if err != nil {
		return err
	}
	tmp := h.SnapshotPath + ".tmp"
	if err := os.WriteFile(tmp, data, 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, h.SnapshotPath) // 原子替换，避免半截文件
}

// LoadSnapshot 读取本地快照。
func (h *TaskHub) LoadSnapshot() (*taskHubSnapshot, error) {
	if h.SnapshotPath == "" {
		return nil, fmt.Errorf("未配置快照路径")
	}
	data, err := os.ReadFile(h.SnapshotPath)
	if err != nil {
		return nil, err
	}
	var snap taskHubSnapshot
	if err := json.Unmarshal(data, &snap); err != nil {
		return nil, err
	}
	if snap.Version != TaskHubSnapshotVersion {
		return nil, fmt.Errorf("快照版本不兼容（v%d）", snap.Version)
	}
	return &snap, nil
}

// ── 指令解析 ──

// 严格匹配：4 位数字 + 空白 + 指令词（整串匹配，防止 "1000 继续吧兄弟" 之类误吞）。
var taskHubCmdRE = regexp.MustCompile(`^([0-9]{4})[\s:：,，]+(\S+)$`)

// taskHubVerbAliases 指令别名表（中文/英文）→ 中枢标准指令词。
// 值为空串表示"非指令词"，用于快速判定。
var taskHubVerbAliases = map[string]string{
	"继续": "继续", "continue": "继续", "resume": "继续", "go": "继续", "跑": "继续",
	"暂停": "暂停", "pause": "暂停", "hold": "暂停",
	"停止": "停止", "stop": "停止",
	"取消": "取消", "cancel": "取消", "abort": "取消",
	"删除": "删除", "delete": "删除", "remove": "删除", "rm": "删除",
}

// taskHubConfirmAliases 删除的二次确认别名 → "删除" + confirm=true
var taskHubConfirmAliases = map[string]bool{
	"确认删除": true, "确定删除": true, "删除确认": true,
	"confirm-delete": true, "confirm_delete": true, "yes-delete": true,
}

// taskHubDetailAliases 查看详情别名 → 详情（不是中枢指令，由网关本地渲染）
var taskHubDetailAliases = map[string]bool{
	"详情": true, "状态": true, "detail": true, "status": true, "info": true, "查看": true,
}

// NormalizeTaskHubVerb 归一化指令词；非指令词返回空串。
func NormalizeTaskHubVerb(word string) string {
	w := strings.ToLower(strings.TrimSpace(word))
	if v, ok := taskHubVerbAliases[w]; ok {
		return v
	}
	if taskHubConfirmAliases[w] {
		return "删除"
	}
	if taskHubDetailAliases[w] {
		return "详情"
	}
	return ""
}

// IsTaskHubCommandVerb 是否为已知指令词（含详情/确认删除）。
func IsTaskHubCommandVerb(word string) bool { return NormalizeTaskHubVerb(word) != "" }

// ParseTaskCommand 解析用户回复。命中返回 (短号, 归一化指令, 是否需二次确认)。
// ok=false 表示这不是任务指令，调用方应放行给正常对话流程。
func ParseTaskCommand(text string) (short int, cmd string, confirm bool, ok bool) {
	m := taskHubCmdRE.FindStringSubmatch(strings.TrimSpace(text))
	if m == nil {
		return 0, "", false, false
	}
	verb := NormalizeTaskHubVerb(m[2])
	if verb == "" {
		return 0, "", false, false // 4 位数字后面不是指令词 → 不拦截
	}
	n, err := strconv.Atoi(m[1])
	if err != nil || ValidateShortNo(n) != nil {
		return 0, "", false, false
	}
	_, confirm = taskHubConfirmAliases[strings.ToLower(strings.TrimSpace(m[2]))]
	return n, verb, confirm, true
}

// ValidateShortNo 校验 4 位短号范围（1000-9999，与 ws-core 一致）。
func ValidateShortNo(n int) error {
	if n < 1000 || n > 9999 {
		return fmt.Errorf("任务编号必须是 4 位数字（1000-9999）")
	}
	return nil
}

// ── 状态 ──

// IsTerminalStatus 是否终态（done/failed）。
func IsTerminalStatus(status string) bool {
	return status == "done" || status == "failed"
}

// TaskStatusLabelCN 状态中文标签。
func TaskStatusLabelCN(status string) string {
	switch status {
	case "pending":
		return "待办"
	case "leased":
		return "已认领"
	case "running":
		return "执行中"
	case "paused":
		return "已暂停"
	case "interrupted":
		return "已中断（可续跑）"
	case "done":
		return "已完成"
	case "failed":
		return "已失败"
	default:
		return status
	}
}

// TaskStatusIcon 状态图标（聊天窗口可读性）。
func TaskStatusIcon(status string) string {
	switch status {
	case "pending":
		return "🕐"
	case "leased", "running":
		return "▶️"
	case "paused":
		return "⏸"
	case "interrupted":
		return "⚠️"
	case "done":
		return "✅"
	case "failed":
		return "❌"
	default:
		return "•"
	}
}

// ── 文本渲染 ──

// FormatTaskList 渲染任务清单（Markdown 友好，各渠道纯文本也能读）。
// fromSnapshot=true 时标注"离线快照"。
func FormatTaskList(tasks []HubTask, fromSnapshot bool) string {
	var sb strings.Builder
	if fromSnapshot {
		sb.WriteString("📋 **任务清单**（⚠️ 任务中枢暂不可达，以下为本地快照）\n\n")
	} else {
		sb.WriteString(fmt.Sprintf("📋 **任务清单**（%d 个活跃任务）\n\n", len(tasks)))
	}
	for _, t := range tasks {
		sb.WriteString(FormatTaskLine(t))
		sb.WriteString("\n")
	}
	sb.WriteString("\n")
	sb.WriteString(TaskCommandHint(tasks))
	return sb.String()
}

// FormatTaskLine 渲染单行任务。
func FormatTaskLine(t HubTask) string {
	line := fmt.Sprintf("%s `#%d` %s %s", TaskStatusIcon(t.Status), t.ShortNo,
		TaskStatusLabelCN(t.Status), truncateRunes(t.Title, 48))
	if t.Status == "running" || t.Status == "leased" || t.Status == "paused" || t.Status == "interrupted" {
		if t.StepTotal > 0 {
			line += fmt.Sprintf("（%d/%d 步）", t.StepDone, t.StepTotal)
		}
	}
	if t.ResumeEpoch > 0 {
		line += fmt.Sprintf("（已续跑 %d 次）", t.ResumeEpoch)
	}
	return line
}

// TaskCommandHint 指令用法提示（列示例短号，便于用户直接改数字回复）。
func TaskCommandHint(tasks []HubTask) string {
	n := 1000
	if len(tasks) > 0 {
		n = tasks[0].ShortNo
	}
	var sb strings.Builder
	sb.WriteString("回复：`<编号> <指令>`，例如：\n")
	sb.WriteString(fmt.Sprintf("  `%d 继续` / `%d 暂停` / `%d 停止` / `%d 取消` / `%d 删除`\n",
		n, n, n, n, n))
	sb.WriteString(fmt.Sprintf("  `%d 详情` 查看单个任务；`%d 确认删除` 二次确认删除", n, n))
	return sb.String()
}

// FormatTaskDetail 渲染单个任务详情。
func FormatTaskDetail(t *HubTask) string {
	if t == nil {
		return "未找到该任务。"
	}
	var sb strings.Builder
	sb.WriteString(fmt.Sprintf("%s **#%d** %s\n\n", TaskStatusIcon(t.Status), t.ShortNo, truncateRunes(t.Title, 80)))
	sb.WriteString(fmt.Sprintf("- 状态：%s\n", TaskStatusLabelCN(t.Status)))
	if t.TaskID != "" {
		sb.WriteString(fmt.Sprintf("- 全局编号：%s\n", t.TaskID))
	}
	if t.Owner != "" {
		sb.WriteString(fmt.Sprintf("- 归属：%s\n", t.Owner))
	}
	if t.StepTotal > 0 {
		sb.WriteString(fmt.Sprintf("- 进度：%d/%d 步\n", t.StepDone, t.StepTotal))
	}
	if t.ResumeEpoch > 0 {
		sb.WriteString(fmt.Sprintf("- 续跑次数：%d\n", t.ResumeEpoch))
	}
	if t.InterruptReason != "" {
		sb.WriteString(fmt.Sprintf("- 中断原因：%s\n", t.InterruptReason))
	}
	if t.UpdatedAt != "" {
		sb.WriteString(fmt.Sprintf("- 更新于：%s\n", t.UpdatedAt))
	}
	if t.Result != "" {
		sb.WriteString(fmt.Sprintf("- 结果：%s\n", truncateRunes(t.Result, 200)))
	}
	if t.Goal != "" && t.Goal != t.Title {
		sb.WriteString(fmt.Sprintf("\n> 目标：%s\n", truncateRunes(t.Goal, 300)))
	}
	sb.WriteString("\n")
	sb.WriteString(TaskCommandHint([]HubTask{*t}))
	return sb.String()
}

// ── 小工具 ──

// readAllLimited 读响应体，最多 max 字节（防止异常大的响应拖垮网关）。
func readAllLimited(r io.Reader, max int64) ([]byte, error) {
	return io.ReadAll(io.LimitReader(r, max))
}

// truncateRunes 按字符数截断（避免截断出半个 UTF-8 字符）。
func truncateRunes(s string, max int) string {
	r := []rune(strings.TrimSpace(s))
	if len(r) <= max {
		return string(r)
	}
	return string(r[:max]) + "…"
}
