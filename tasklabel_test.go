// ws-gateway — 任务渲染契约测试
//
// 契约（与 ws-core/tasklabel.go 对齐）：short_no==0 时**绝不输出 `#0`**。
// `#0` 不是合法短号（ValidateShortNo 只认 1000-9999），显示得像编号却点不进去 = 纯误导。
package gateway

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestTaskLabelNeverPrintsZero(t *testing.T) {
	cases := []struct {
		name string
		in   HubTask
		want string
	}{
		{"正常短号", HubTask{ID: 1, ShortNo: 1000, TaskID: "T000001"}, "#1000"},
		{"无短号有全局编号", HubTask{ID: 84, ShortNo: 0, TaskID: "N000084", Title: "[t1] 节点"}, "N000084"},
		{"两者都无", HubTask{ID: 7, ShortNo: 0, Title: "脏行"}, "task:7"},
	}
	for _, c := range cases {
		if got := TaskLabel(c.in); got != c.want {
			t.Errorf("%s: TaskLabel = %q，期望 %q", c.name, got, c.want)
		}
		if strings.Contains(TaskLabel(c.in), "#0") {
			t.Errorf("%s: 渲染出现 #0（%q）", c.name, TaskLabel(c.in))
		}
	}
}

func TestFormatTaskLineHasNoZeroLabel(t *testing.T) {
	line := FormatTaskLine(HubTask{ID: 84, ShortNo: 0, TaskID: "N000084", Title: "[t1] 节点", Status: "done"})
	if strings.Contains(line, "#0") {
		t.Fatalf("清单行出现 #0：%q", line)
	}
	if !strings.Contains(line, "N000084") {
		t.Errorf("清单行应退化为全局编号：%q", line)
	}
}

func TestTaskCommandHintSkipsZeroShortNo(t *testing.T) {
	// 首个任务没有短号时，示例短号不得取 0（否则用户照着回复 `#0 详情#` 永远查不到）
	hint := TaskCommandHint([]HubTask{
		{ID: 84, ShortNo: 0, TaskID: "N000084"},
		{ID: 85, ShortNo: 1023, TaskID: "T000085"},
	})
	if strings.Contains(hint, "#0 ") {
		t.Fatalf("指令提示出现 #0：%q", hint)
	}
	if !strings.Contains(hint, "#1023") {
		t.Errorf("指令提示应取首个有效短号：%q", hint)
	}
	// 全都没有短号 → 退化默认示例（1000），也不得是 0
	hint2 := TaskCommandHint([]HubTask{{ID: 1, ShortNo: 0, TaskID: "N000001"}})
	if strings.Contains(hint2, "#0 ") {
		t.Fatalf("指令提示出现 #0：%q", hint2)
	}
	if !strings.Contains(hint2, "#1000") {
		t.Errorf("无有效短号时应退化默认示例：%q", hint2)
	}
}

// ── 详情里的「执行节点」展开（0.12.0：节点只在父任务详情下可见）──

func TestFormatTaskDetailShowsNodes(t *testing.T) {
	task := &HubTask{
		ID: 83, ShortNo: 1000, TaskID: "T000083", Title: "修复大文件传输", Status: "running",
		StepDone: 12, StepTotal: 37,
		Nodes: []HubTask{
			{ID: 1, TaskID: "N000001", Title: "[t1] 确认仓库基线", Status: "done"},
			{ID: 2, TaskID: "N000037", Title: "[t37] 写验证报告", Status: "failed"},
			{ID: 3, TaskID: "N000020", Title: "[t20] 限速 harness", Status: "running"},
		},
		NodesTotal: 37, NodesDone: 12, NodesFailed: 1,
	}
	out := FormatTaskDetail(task)
	for _, want := range []string{"执行节点", "12/37 完成", "1 失败", "N000037", "[t37] 写验证报告"} {
		if !strings.Contains(out, want) {
			t.Errorf("详情缺少 %q：\n%s", want, out)
		}
	}
	if strings.Contains(out, "#0") {
		t.Errorf("详情出现 #0：\n%s", out)
	}
	// 失败节点必须排在最前（最需要用户决策）
	if i, j := strings.Index(out, "N000037"), strings.Index(out, "N000001"); i < 0 || j < 0 || i > j {
		t.Errorf("失败节点应排在已完成节点之前：\n%s", out)
	}
	// 总数为 37 > 列出的 3 → 折叠提示
	if !strings.Contains(out, "另有 34 个节点") {
		t.Errorf("应给出折叠计数：\n%s", out)
	}
	// 无节点（普通任务）→ 不出现该小节
	if s := FormatTaskDetail(&HubTask{ShortNo: 1001, Title: "普通任务"}); strings.Contains(s, "执行节点") {
		t.Errorf("普通任务详情不应出现「执行节点」小节：\n%s", s)
	}
}

func TestGetEnrichesPlanNodesOnlyForPlans(t *testing.T) {
	var nodeHits int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/tasks/short/1000": // 计划任务（有子步骤）
			_ = json.NewEncoder(w).Encode(HubTask{ID: 83, ShortNo: 1000, Title: "计划", Status: "running", StepTotal: 37})
		case "/api/tasks/short/1001": // 普通任务
			_ = json.NewEncoder(w).Encode(HubTask{ID: 90, ShortNo: 1001, Title: "普通", Status: "pending"})
		case "/api/tasks/83/nodes":
			nodeHits++
			_ = json.NewEncoder(w).Encode(map[string]any{
				"total": 37, "done": 12, "failed": 1,
				"nodes": []HubTask{{ID: 1, TaskID: "N000001", Title: "[t1] x", Status: "done"}},
			})
		default:
			http.Error(w, "not found", http.StatusNotFound)
		}
	}))
	defer srv.Close()

	h := NewTaskHub(srv.URL, "", "")
	plan, err := h.Get(1000)
	if err != nil {
		t.Fatal(err)
	}
	if plan.NodesTotal != 37 || plan.NodesDone != 12 || plan.NodesFailed != 1 || len(plan.Nodes) != 1 {
		t.Errorf("计划详情应填充节点进度，实际 %+v", plan)
	}
	plain, err := h.Get(1001)
	if err != nil {
		t.Fatal(err)
	}
	if len(plain.Nodes) != 0 {
		t.Errorf("普通任务不应请求节点（省一次网络往返），实际 %+v", plain)
	}
	if nodeHits != 1 {
		t.Errorf("只应为计划任务取一次节点，实际 %d 次", nodeHits)
	}
}
