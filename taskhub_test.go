package gateway

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// ── 指令解析 ──

func TestParseTaskCommand_Hits(t *testing.T) {
	cases := []struct {
		in      string
		short   int
		cmd     string
		confirm bool
	}{
		{"1000 继续", 1000, "继续", false},
		{"1000 continue", 1000, "继续", false},
		{"1000 暂停", 1000, "暂停", false},
		{"1000 pause", 1000, "暂停", false},
		{"1000 停止", 1000, "停止", false},
		{"1000 取消", 1000, "取消", false},
		{"1000 删除", 1000, "删除", false},
		{"1000 确认删除", 1000, "删除", true},
		{"1000 详情", 1000, "详情", false},
		{"1000 status", 1000, "详情", false},
		{"  1000   继续  ", 1000, "继续", false}, // 前后空白容忍
		{"1000：暂停", 1000, "暂停", false},       // 全角冒号
		{"9999 取消", 9999, "取消", false},
	}
	for _, c := range cases {
		short, cmd, confirm, ok := ParseTaskCommand(c.in)
		if !ok {
			t.Errorf("ParseTaskCommand(%q) 未命中，期望命中", c.in)
			continue
		}
		if short != c.short || cmd != c.cmd || confirm != c.confirm {
			t.Errorf("ParseTaskCommand(%q) = (%d,%q,%v)，期望 (%d,%q,%v)",
				c.in, short, cmd, confirm, c.short, c.cmd, c.confirm)
		}
	}
}

// 关键：正常聊天内容绝不能被误吞。
func TestParseTaskCommand_NoFalsePositive(t *testing.T) {
	cases := []string{
		"2024 年的合同还没签",
		"1000",
		"1000 继续吧兄弟",     // 指令词后有内容 → 不匹配整串
		"任务 1000 继续",     // 前面有字 → 不匹配
		"100 继续",         // 3 位 → 不匹配
		"10000 继续",       // 5 位 → 不匹配
		"0999 继续",        // 超出 1000-9999
		"帮我把 1000 继续跑起来", // 中文包夹
		"",
		"1000 你好",
		"2024-09-18 报告",
	}
	for _, c := range cases {
		if _, _, _, ok := ParseTaskCommand(c); ok {
			t.Errorf("ParseTaskCommand(%q) 误命中，应放行给正常对话", c)
		}
	}
}

func TestValidateShortNo(t *testing.T) {
	if err := ValidateShortNo(1000); err != nil {
		t.Errorf("1000 应合法: %v", err)
	}
	if err := ValidateShortNo(9999); err != nil {
		t.Errorf("9999 应合法: %v", err)
	}
	if err := ValidateShortNo(999); err == nil {
		t.Error("999 应非法")
	}
	if err := ValidateShortNo(10000); err == nil {
		t.Error("10000 应非法")
	}
}

// ── 状态渲染 ──

func TestStatusLabelAndIcon(t *testing.T) {
	for _, st := range []string{"pending", "leased", "running", "paused", "interrupted", "done", "failed"} {
		if TaskStatusLabelCN(st) == "" || TaskStatusLabelCN(st) == st {
			t.Errorf("状态 %s 缺少中文标签", st)
		}
		if TaskStatusIcon(st) == "" {
			t.Errorf("状态 %s 缺少图标", st)
		}
	}
	if !IsTerminalStatus("done") || !IsTerminalStatus("failed") || IsTerminalStatus("running") {
		t.Error("终态判定错误")
	}
}

func TestFormatTaskList_MentionsShortNoAndHint(t *testing.T) {
	tasks := []HubTask{
		{ShortNo: 1000, Title: "ws-win 追平 WorkBuddy", Status: "paused", StepDone: 3, StepTotal: 12},
		{ShortNo: 1001, Title: "数据库备份", Status: "running"},
	}
	out := FormatTaskList(tasks, false)
	for _, want := range []string{"#1000", "#1001", "已暂停", "执行中", "3/12", "1000 继续", "1000 确认删除"} {
		if !strings.Contains(out, want) {
			t.Errorf("任务清单缺少 %q，实际：\n%s", want, out)
		}
	}
	// 快照兜底必须显式标注，避免用户误判为实时状态
	if snap := FormatTaskList(tasks, true); !strings.Contains(snap, "快照") {
		t.Errorf("快照模式未标注，实际：\n%s", snap)
	}
}

func TestFormatTaskDetail(t *testing.T) {
	out := FormatTaskDetail(&HubTask{
		ShortNo: 1000, Title: "ws-win 追平 WorkBuddy", Status: "interrupted",
		StepDone: 3, StepTotal: 12, ResumeEpoch: 2, InterruptReason: "crash-recovered",
		Goal: "实现续跑交互",
	})
	for _, want := range []string{"#1000", "已中断", "3/12", "crash-recovered", "续跑次数：2", "实现续跑交互"} {
		if !strings.Contains(out, want) {
			t.Errorf("任务详情缺少 %q，实际：\n%s", want, out)
		}
	}
	if got := FormatTaskDetail(nil); !strings.Contains(got, "未找到") {
		t.Errorf("nil 任务应提示未找到，实际 %q", got)
	}
}

// ── HTTP 客户端 ──

func TestListAndFilterActive(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/tasks" {
			t.Errorf("路径错误: %s", r.URL.Path)
		}
		_ = json.NewEncoder(w).Encode([]HubTask{
			{ShortNo: 1001, Status: "running", Title: "a"},
			{ShortNo: 1000, Status: "done", Title: "b"},
			{ShortNo: 1002, Status: "paused", Title: "c"},
			{ShortNo: 1003, Status: "failed", Title: "d"},
		})
	}))
	defer srv.Close()

	dir := t.TempDir()
	snap := filepath.Join(dir, "sub", "x.snapshot.json")
	h := NewTaskHub(srv.URL, "", snap)
	active, fromSnap, err := h.ListActive()
	if err != nil {
		t.Fatalf("ListActive 失败: %v", err)
	}
	if fromSnap {
		t.Error("中枢可用时不应走快照")
	}
	if len(active) != 2 {
		t.Fatalf("活跃任务应 2 条（done/failed 被过滤），实际 %d: %+v", len(active), active)
	}
	// 按短号升序
	if active[0].ShortNo != 1001 || active[1].ShortNo != 1002 {
		t.Errorf("未按短号排序: %+v", active)
	}
	// 快照已落盘
	if _, err := os.Stat(snap); err != nil {
		t.Fatalf("快照未落盘: %v", err)
	}
}

func TestListActive_FallbackToSnapshot(t *testing.T) {
	dir := t.TempDir()
	snap := filepath.Join(dir, "x.snapshot.json")
	// 先手工写一份快照
	h := NewTaskHub("http://127.0.0.1:1", "", snap) // 不可达端口
	if err := h.SaveSnapshot([]HubTask{{ShortNo: 1000, Status: "paused", Title: "离线任务"}}); err != nil {
		t.Fatalf("SaveSnapshot: %v", err)
	}
	active, fromSnap, err := h.ListActive()
	if err != nil {
		t.Fatalf("中枢不可达时应回退快照，实际 err=%v", err)
	}
	if !fromSnap {
		t.Error("应从快照兜底")
	}
	if len(active) != 1 || active[0].ShortNo != 1000 {
		t.Errorf("快照内容不对: %+v", active)
	}
}

func TestListActive_NoSnapshotReturnsError(t *testing.T) {
	h := NewTaskHub("http://127.0.0.1:1", "", filepath.Join(t.TempDir(), "none.json"))
	if _, _, err := h.ListActive(); err == nil {
		t.Error("中枢不可达且无快照时应返回错误")
	}
}

func TestCommand_OKAndError(t *testing.T) {
	var gotCmd string
	var gotConfirm bool
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/tasks/short/1000/cmd" {
			t.Errorf("路径错误: %s", r.URL.Path)
		}
		var req struct {
			Cmd     string `json:"cmd"`
			Confirm bool   `json:"confirm"`
		}
		_ = json.NewDecoder(r.Body).Decode(&req)
		gotCmd, gotConfirm = req.Cmd, req.Confirm
		_ = json.NewEncoder(w).Encode(map[string]any{"short_no": 1000, "cmd": req.Cmd, "message": "✓ 已暂停"})
	}))
	defer srv.Close()

	h := NewTaskHub(srv.URL, "", "")
	msg, err := h.Command(1000, "暂停", false)
	if err != nil {
		t.Fatalf("Command 失败: %v", err)
	}
	if msg != "✓ 已暂停" {
		t.Errorf("消息透传错误: %q", msg)
	}
	if gotCmd != "暂停" || gotConfirm {
		t.Errorf("请求体错误 cmd=%q confirm=%v", gotCmd, gotConfirm)
	}

	// 中枢返回业务错误（400 + error）应转成 error
	srv2 := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
		_ = json.NewEncoder(w).Encode(map[string]any{"error": "短号 #1000 没有对应任务"})
	}))
	defer srv2.Close()
	h2 := NewTaskHub(srv2.URL, "", "")
	if _, err := h2.Command(1000, "继续", false); err == nil {
		t.Error("中枢 400 应返回 error")
	}
}

// 真机回归（观音 ws-core 0.5.0）：用户原话「确认删除」必须被归一化成
// 「删除 + confirm=true」，否则中枢回「未知指令」——这正是首次真机验证
// 抓到的缺陷，务必回归住。
func TestCommand_NormalizesUserVerbs(t *testing.T) {
	cases := []struct {
		in       string
		wantCmd  string
		wantConf bool
	}{
		{"pause", "暂停", false},
		{"continue", "继续", false},
		{"rm", "删除", false},
		{"确认删除", "删除", true},
		{"confirm-delete", "删除", true},
		{"PAUSE", "暂停", false}, // 大小写
		{" 停止 ", "停止", false},  // 前后空白
	}
	for _, c := range cases {
		var gotCmd string
		var gotConfirm bool
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			var req struct {
				Cmd     string `json:"cmd"`
				Confirm bool   `json:"confirm"`
			}
			_ = json.NewDecoder(r.Body).Decode(&req)
			gotCmd, gotConfirm = req.Cmd, req.Confirm
			_ = json.NewEncoder(w).Encode(map[string]any{"message": "ok"})
		}))
		h := NewTaskHub(srv.URL, "", "")
		if _, err := h.Command(1000, c.in, false); err != nil {
			t.Errorf("Command(%q) 失败: %v", c.in, err)
		}
		if gotCmd != c.wantCmd || gotConfirm != c.wantConf {
			t.Errorf("Command(%q) 下发 (%q,confirm=%v)，期望 (%q,confirm=%v)",
				c.in, gotCmd, gotConfirm, c.wantCmd, c.wantConf)
		}
		srv.Close()
	}

	// 未知指令必须在本地就被拒绝（不打网络）
	h := NewTaskHub("http://127.0.0.1:1", "", "")
	if _, err := h.Command(1000, "重启地球", false); err == nil {
		t.Error("未知指令应本地拒绝")
	}
	if _, err := h.Command(1000, "详情", false); err == nil {
		t.Error("详情不是中枢指令，应本地拒绝并提示用 Get")
	}
}

func TestCommand_RejectsBadShortNo(t *testing.T) {
	h := NewTaskHub("http://127.0.0.1:1", "", "")
	if _, err := h.Command(99, "继续", false); err == nil {
		t.Error("非法短号应在本地就被拒绝（不打网络）")
	}
}

func TestGetDetail(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/tasks/short/1000" {
			t.Errorf("路径错误: %s", r.URL.Path)
		}
		_ = json.NewEncoder(w).Encode(HubTask{ShortNo: 1000, Title: "t", Status: "running"})
	}))
	defer srv.Close()
	t2, err := NewTaskHub(srv.URL, "", "").Get(1000)
	if err != nil {
		t.Fatalf("Get 失败: %v", err)
	}
	if t2.ShortNo != 1000 || t2.Status != "running" {
		t.Errorf("详情解析错误: %+v", t2)
	}
}

func TestTaskHubAddrFromEnv(t *testing.T) {
	t.Setenv(EnvTaskHubAddr, "")
	if TaskHubAddrFromEnv() != DefaultTaskHubAddr {
		t.Error("默认地址错误")
	}
	t.Setenv(EnvTaskHubAddr, "http://10.0.0.1:9521/")
	if TaskHubAddrFromEnv() != "http://10.0.0.1:9521" {
		t.Errorf("环境变量地址未生效: %s", TaskHubAddrFromEnv())
	}
}

func TestTaskHubAnnounceEnabled(t *testing.T) {
	t.Setenv(EnvTaskHubAnnounce, "")
	if !TaskHubAnnounceEnabled() {
		t.Error("默认应开启")
	}
	for _, off := range []string{"0", "false", "off"} {
		t.Setenv(EnvTaskHubAnnounce, off)
		if TaskHubAnnounceEnabled() {
			t.Errorf("%q 应关闭播报", off)
		}
	}
}

func TestSnapshotPathUsesWSPath(t *testing.T) {
	t.Setenv("WS_PATH", "/tmp/wsx")
	p := DefaultTaskHubSnapshotPath("wecom")
	if p != "/tmp/wsx/data/taskhub/wecom.snapshot.json" {
		t.Errorf("快照路径错误: %s", p)
	}
	// 渠道名里的路径分隔符要被消毒，防止越目录写
	if strings.Contains(DefaultTaskHubSnapshotPath("../evil"), "..") {
		t.Error("渠道名未消毒")
	}
}
