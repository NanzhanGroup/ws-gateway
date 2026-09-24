package gateway

// wscmd_test.go — 内置命令（WSC）网关侧单测
//
// 覆盖：前缀判定（与 ws-core 同口径）、正常执行、中枢不可达、中枢返回 handled=false、
// 开关关闭，以及「非命令一律不消费」（防误吞用户正常提问）。

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestIsWSCCommand(t *testing.T) {
	yes := []string{
		"wsc:version", "WSC:VERSION", "  wsc:help  ", ":wsc:version",
		"wsc：version", "wsc:upgrade-module ws-core -s",
	}
	for _, s := range yes {
		if !IsWSCCommand(s) {
			t.Errorf("IsWSCCommand(%q) 应为 true", s)
		}
	}
	no := []string{
		"", "  ", "你好", "version", "帮我看看 wsc:version", "1000 继续",
		"wsc", "wsc version",
	}
	for _, s := range no {
		if IsWSCCommand(s) {
			t.Errorf("IsWSCCommand(%q) 应为 false（不能在非开头处误判）", s)
		}
	}
}

func TestRunWSCCommand_NotACommand(t *testing.T) {
	t.Setenv(EnvWSCEnabled, "")
	t.Setenv(EnvWSCoreAddr, "http://127.0.0.1:1") // 故意指向死地址：非命令不该发起请求
	reply, handled := RunWSCCommand("今天天气不错")
	if handled || reply != "" {
		t.Fatalf("非命令不应被消费: handled=%v reply=%q", handled, reply)
	}
}

func TestRunWSCCommand_OK(t *testing.T) {
	t.Setenv(EnvWSCEnabled, "")
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/cmd" {
			t.Errorf("路径应为 /api/cmd，实际 %s", r.URL.Path)
		}
		var in struct{ Text string }
		_ = json.NewDecoder(r.Body).Decode(&in)
		if in.Text != "wsc:version" {
			t.Errorf("透传文本错误: %q", in.Text)
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"handled": true, "reply": "【文殊模块版本】ok"})
	}))
	defer srv.Close()

	reply, handled, err := RunWSCCommandWith(srv.URL, "wsc:version", 5*time.Second)
	if err != nil || !handled || !strings.Contains(reply, "文殊模块版本") {
		t.Fatalf("正常执行失败: reply=%q handled=%v err=%v", reply, handled, err)
	}
}

func TestRunWSCCommand_CoreUnreachable(t *testing.T) {
	t.Setenv(EnvWSCEnabled, "")
	// 绝不静默放给 LLM：中枢不可达也要消费并回显错误
	reply, handled, err := RunWSCCommandWith("http://127.0.0.1:1", "wsc:version", 2*time.Second)
	if err == nil {
		t.Error("中枢不可达应返回错误")
	}
	if !handled || !strings.Contains(reply, "执行失败") {
		t.Fatalf("应消费并提示失败: handled=%v reply=%q", handled, reply)
	}
}

func TestRunWSCCommand_CoreSaysNotHandled(t *testing.T) {
	t.Setenv(EnvWSCEnabled, "")
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{"handled": false, "reply": ""})
	}))
	defer srv.Close()
	if reply, handled, _ := RunWSCCommandWith(srv.URL, "wsc:xxx", 5*time.Second); handled || reply != "" {
		t.Fatalf("中枢判非命令时应放行原有流程: handled=%v reply=%q", handled, reply)
	}
}

func TestRunWSCCommand_Disabled(t *testing.T) {
	t.Setenv(EnvWSCEnabled, "0")
	if reply, handled, _ := RunWSCCommandWith("http://127.0.0.1:1", "wsc:version", time.Second); handled || reply != "" {
		t.Fatalf("关闭开关后不应拦截: handled=%v reply=%q", handled, reply)
	}
}

func TestWSCoreAddrFromEnv(t *testing.T) {
	t.Setenv(EnvWSCoreAddr, "")
	if got := WSCoreAddrFromEnv(); got != DefaultWSCoreAddr {
		t.Errorf("默认地址错误: %s", got)
	}
	t.Setenv(EnvWSCoreAddr, "http://10.0.0.1:9521/")
	if got := WSCoreAddrFromEnv(); got != "http://10.0.0.1:9521" {
		t.Errorf("应去掉尾部斜杠: %s", got)
	}
}
