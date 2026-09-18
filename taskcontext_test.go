package gateway

// taskcontext_test.go — W4 任务上下文注入单测
//
// 覆盖：无任务不注入、配额（条数/字数）、快照标注、静默降级、TTL 缓存、
// 主动失效、开关、排序（最要紧的排前面）、中文截断不破字。

import (
	"strings"
	"testing"
	"time"
	"unicode/utf8"
)

// fakeSource 假任务来源：可指定返回内容/快照标记/错误，并记录被调用次数。
type fakeSource struct {
	tasks []HubTask
	snap  bool
	err   error
	calls int
}

func (f *fakeSource) ListActive() ([]HubTask, bool, error) {
	f.calls++
	return f.tasks, f.snap, f.err
}

func mkTask(short int, status, title string) HubTask {
	return HubTask{ShortNo: short, Status: status, Title: title}
}

// ── 渲染 ──

func TestRenderTaskContextEmpty(t *testing.T) {
	if got := RenderTaskContext(nil, false, 0, 0); got != "" {
		t.Fatalf("无任务应返回空串，得到 %q", got)
	}
	if got := RenderTaskContext([]HubTask{}, false, 5, 600); got != "" {
		t.Fatalf("空列表应返回空串，得到 %q", got)
	}
}

func TestRenderTaskContextSnapshotMarker(t *testing.T) {
	tasks := []HubTask{mkTask(1000, "running", "任务A")}
	live := RenderTaskContext(tasks, false, 5, 600)
	snap := RenderTaskContext(tasks, true, 5, 600)
	if strings.Contains(live, "快照") {
		t.Fatalf("实时结果不应出现快照标注: %q", live)
	}
	if !strings.Contains(snap, "快照") {
		t.Fatalf("快照兜底必须显式标注，得到 %q", snap)
	}
	if !strings.Contains(snap, "#1000") {
		t.Fatalf("快照也应含任务短号: %q", snap)
	}
}

func TestRenderTaskContextOrder(t *testing.T) {
	tasks := []HubTask{
		mkTask(1003, "pending", "待办"),
		mkTask(1002, "running", "在跑"),
		mkTask(1001, "interrupted", "中断"),
		mkTask(1004, "paused", "暂停"),
	}
	got := RenderTaskContext(tasks, false, 5, 600)
	iInt, iRun, iPau, iPend := strings.Index(got, "#1001"), strings.Index(got, "#1002"),
		strings.Index(got, "#1004"), strings.Index(got, "#1003")
	if !(iInt < iRun && iRun < iPau && iPau < iPend) {
		t.Fatalf("排序应为 中断 < 执行中 < 暂停 < 待办，得到 %q", got)
	}
}

func TestRenderTaskContextMaxTasks(t *testing.T) {
	var tasks []HubTask
	for i := 0; i < 7; i++ {
		tasks = append(tasks, mkTask(1000+i, "pending", "任务"))
	}
	got := RenderTaskContext(tasks, false, 5, 600)
	if n := strings.Count(got, "`#1"); n != 5 {
		t.Fatalf("应只渲染 5 条任务，得到 %d 条: %q", n, got)
	}
	if !strings.Contains(got, "另有 2 个") {
		t.Fatalf("超出配额应有剩余提示: %q", got)
	}
	// 边界：恰好等于配额时不应出现"另有"
	got5 := RenderTaskContext(tasks[:5], false, 5, 600)
	if strings.Contains(got5, "另有") {
		t.Fatalf("恰好 5 条不应有剩余提示: %q", got5)
	}
	// 边界：配额 1 条
	got1 := RenderTaskContext(tasks, false, 1, 600)
	if n := strings.Count(got1, "`#1"); n != 1 {
		t.Fatalf("配额 1 应只渲染 1 条，得到 %d 条: %q", n, got1)
	}
}

func TestRenderTaskContextRuneSafeTruncation(t *testing.T) {
	// ① 单条超长标题：FormatTaskLine 内层已按 48 字截断，整体不应超限
	long := strings.Repeat("中", 1000)
	one := RenderTaskContext([]HubTask{mkTask(1000, "running", long)}, false, 5, 600)
	if n := utf8.RuneCountInString(one); n > 600 {
		t.Fatalf("总长应 ≤600 字符，得到 %d: %q", n, one)
	}
	if !utf8.ValidString(one) {
		t.Fatal("截断后必须是合法 UTF-8（不能切出半个汉字）")
	}
	if !strings.Contains(one, "…") {
		t.Fatalf("超长标题应被截断并带省略号: %q", one)
	}

	// ② 多条拼起来超过总长上限：应在字符边界处硬截断，不破字
	//    （默认配额下 5 条 ≈ 370 字符，触不到 600；这里把上限压到 200 逼出截断分支）
	var many []HubTask
	for i := 0; i < 5; i++ {
		many = append(many, mkTask(1000+i, "running", strings.Repeat("文", 200)))
	}
	got := RenderTaskContext(many, false, 5, 200)
	if n := utf8.RuneCountInString(got); n > 200 {
		t.Fatalf("总长应 ≤200 字符，得到 %d", n)
	}
	if !utf8.ValidString(got) {
		t.Fatal("硬截断后必须是合法 UTF-8")
	}
	if !strings.HasSuffix(got, "…") {
		t.Fatalf("触顶应以省略号收尾: %q", got)
	}
	// 默认配额下的最坏情况必须落在 600 字预算内（5 条 × 48 字标题 + 页脚 + 快照标注）
	var worst []HubTask
	for i := 0; i < 5; i++ {
		worst = append(worst, mkTask(1000+i, "interrupted", strings.Repeat("满", 500)))
	}
	if n := utf8.RuneCountInString(RenderTaskContext(worst, true, 0, 0)); n > DefaultTaskContextMaxChars {
		t.Fatalf("默认配额最坏情况应 ≤%d 字符，得到 %d", DefaultTaskContextMaxChars, n)
	}

	// ③ 未超限不得截断
	short := RenderTaskContext([]HubTask{mkTask(1000, "running", "短标题")}, false, 5, 600)
	if strings.HasSuffix(short, "…") {
		t.Fatalf("未超限不应截断: %q", short)
	}
}

// ── 缓存 / 降级 ──

func TestTaskContextTTLCache(t *testing.T) {
	src := &fakeSource{tasks: []HubTask{mkTask(1000, "running", "任务A")}}
	c := NewTaskContext("qq", src)
	base := time.Unix(1789760000, 0)
	c.now = func() time.Time { return base }

	first := c.Block()
	if src.calls != 1 {
		t.Fatalf("首次应拉取 1 次，得到 %d", src.calls)
	}
	// TTL 内复用（含时间前进 29s）
	base = base.Add(29 * time.Second)
	second := c.Block()
	if src.calls != 1 {
		t.Fatalf("TTL 内不应重复拉取，得到 %d 次", src.calls)
	}
	if first != second {
		t.Fatal("TTL 内应返回同一份文本")
	}
	// 超过 TTL 后刷新
	base = base.Add(2 * time.Second)
	c.Block()
	if src.calls != 2 {
		t.Fatalf("超过 TTL 应重新拉取，得到 %d 次", src.calls)
	}
}

func TestTaskContextCachesEmptyResult(t *testing.T) {
	src := &fakeSource{}
	c := NewTaskContext("qq", src)
	base := time.Unix(1789760000, 0)
	c.now = func() time.Time { return base }

	if got := c.Block(); got != "" {
		t.Fatalf("无任务应返回空串，得到 %q", got)
	}
	c.Block()
	if src.calls != 1 {
		t.Fatalf("空结果也应缓存（避免每轮白打中枢），得到 %d 次拉取", src.calls)
	}
	// 新任务出现后，TTL 到期即可见
	src.tasks = []HubTask{mkTask(1000, "running", "任务A")}
	base = base.Add(31 * time.Second)
	if got := c.Block(); !strings.Contains(got, "#1000") {
		t.Fatalf("TTL 到期应看到新任务，得到 %q", got)
	}
}

func TestTaskContextSilentDegrade(t *testing.T) {
	src := &fakeSource{err: errFake}
	c := NewTaskContext("qq", src)
	if got := c.Block(); got != "" {
		t.Fatalf("中枢不可达且无快照应静默降级为空串，得到 %q", got)
	}
}

func TestTaskContextInvalidate(t *testing.T) {
	src := &fakeSource{tasks: []HubTask{mkTask(1000, "running", "任务A")}}
	c := NewTaskContext("qq", src)
	base := time.Unix(1789760000, 0)
	c.now = func() time.Time { return base }

	c.Block()
	c.Invalidate() // 网关执行完指令后主动失效
	c.Block()
	if src.calls != 2 {
		t.Fatalf("失效后应重新拉取，得到 %d 次", src.calls)
	}
}

func TestTaskContextNilSafe(t *testing.T) {
	var c *TaskContext
	if got := c.Block(); got != "" {
		t.Fatalf("nil 缓存器应返回空串，得到 %q", got)
	}
	c2 := NewTaskContext("qq", nil)
	if got := c2.Block(); got != "" {
		t.Fatalf("无来源应返回空串，得到 %q", got)
	}
}

// ── 开关 ──

func TestTaskContextEnabledEnv(t *testing.T) {
	for _, v := range []string{"", "1", "on", "true"} {
		t.Setenv(EnvTaskContext, v)
		if !TaskContextEnabled() {
			t.Fatalf("%q 应视为开启", v)
		}
	}
	for _, v := range []string{"0", "false", "off", "OFF", " off "} {
		t.Setenv(EnvTaskContext, v)
		if TaskContextEnabled() {
			t.Fatalf("%q 应视为关闭", v)
		}
	}
}

func TestTaskContextRefDisabledSkipsHub(t *testing.T) {
	t.Setenv(EnvTaskContext, "0")
	if got := TaskContextRef("unit-test-disabled"); got != "" {
		t.Fatalf("关闭注入时应返回空串，得到 %q", got)
	}
	if _, ok := taskContextRegistry.Load("unit-test-disabled"); ok {
		t.Fatal("关闭时不应创建缓存器")
	}
}

// errFake 测试用错误。
var errFake = errFakeType("中枢不可达")

type errFakeType string

func (e errFakeType) Error() string { return string(e) }
