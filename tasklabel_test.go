// ws-gateway — 任务渲染契约测试
//
// 契约（与 ws-core/tasklabel.go 对齐）：short_no==0 时**绝不输出 `#0`**。
// `#0` 不是合法短号（ValidateShortNo 只认 1000-9999），显示得像编号却点不进去 = 纯误导。
package gateway

import (
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
