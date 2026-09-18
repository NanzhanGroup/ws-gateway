// Package gateway 提供平台网关共享类型
//
// buscontext.go — 未读节点总线消息注入共享层（BusUnreadRef）
//
// ── 为什么需要它（2026-09-19）──
//
// ws-core 的节点总线投递正常，但**没有任何一方把它交给 LLM**：智能体是
// "请求-响应"的无状态模型，只在有人跟它说话时才醒，于是"消息落库了，但想不起
// 去看"。可靠修法只有一条：**把未读摘要塞进每次对话的系统提示**，不依赖模型
// 主动想起。本文件与 taskcontext.go 同构（同一套 TTL 缓存/静默降级/可关开关），
// 各网关只需一行 `gateway.BusUnreadRef("<渠道>")` 接入。
//
// 与 ws-core buswake.go 的分工：
//   - 本文件（注入）：让智能体**看得见**（被动，100% 可靠）
//   - buswake（唤醒）：超时未处理时**主动叫醒**（带三道闸）
package gateway

import (
	"encoding/json"
	"net/http"
	"os"
	"strings"
	"sync"
	"time"
)

const (
	// EnvBusInject 置 0 关闭"未读总线消息注入"（默认开启）
	EnvBusInject = "WS_BUS_INJECT"

	// busUnreadTTL 注入块缓存时长（系统提示每轮都拼，不能每轮打 HTTP）
	busUnreadTTL = 10 * time.Second

	// busUnreadMax 摘要最多列几条
	busUnreadMax = 5

	// busUnreadBodyMax 单条正文截断（字符）
	busUnreadBodyMax = 120
)

// BusContextEnabled 未读总线注入是否启用（默认启用）
func BusContextEnabled() bool {
	return strings.TrimSpace(os.Getenv(EnvBusInject)) != "0"
}

type busUnreadMsg struct {
	ID        string `json:"id"`
	From      string `json:"from"`
	Kind      string `json:"kind"`
	Body      string `json:"body"`
	RefFile   string `json:"ref_file"`
	CreatedAt int64  `json:"created_at"`
}

type busUnreadCache struct {
	mu  sync.Mutex
	val string
	at  time.Time
}

var busUnreadCaches sync.Map // name -> *busUnreadCache

// BusUnreadRef 返回"未读节点总线消息摘要"文本块；无未读 / 不可达 / 已关闭时返回空串。
// name 仅用于区分缓存（渠道名），不影响请求内容。
func BusUnreadRef(name string) string {
	if !BusContextEnabled() {
		return ""
	}
	holder, _ := busUnreadCaches.LoadOrStore(name, &busUnreadCache{})
	c := holder.(*busUnreadCache)
	c.mu.Lock()
	defer c.mu.Unlock()
	if time.Since(c.at) < busUnreadTTL {
		return c.val
	}
	c.at = time.Now()
	c.val = fetchBusUnread()
	return c.val
}

// InvalidateBusUnread 失效缓存（网关处理完总线消息后调用，令下次对话立即更新）
func InvalidateBusUnread(name string) {
	if holder, ok := busUnreadCaches.Load(name); ok {
		holder.(*busUnreadCache).mu.Lock()
		holder.(*busUnreadCache).at = time.Time{}
		holder.(*busUnreadCache).mu.Unlock()
	}
}

func fetchBusUnread() string {
	client := &http.Client{Timeout: 800 * time.Millisecond}
	resp, err := client.Get(TaskHubAddrFromEnv() + "/api/bus/inbox?unread_only=1&limit=20")
	if err != nil {
		return ""
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return ""
	}
	var env struct {
		Count int            `json:"count"`
		Msgs  []busUnreadMsg `json:"msgs"`
	}
	if json.NewDecoder(resp.Body).Decode(&env) != nil || len(env.Msgs) == 0 {
		return ""
	}

	byFrom := map[string]int{}
	var sb strings.Builder
	for i, m := range env.Msgs {
		byFrom[m.From]++
		if i >= busUnreadMax {
			continue
		}
		body := strings.Join(strings.Fields(m.Body), " ")
		if r := []rune(body); len(r) > busUnreadBodyMax {
			body = string(r[:busUnreadBodyMax]) + "…"
		}
		sb.WriteString("  · [" + m.Kind + "] " + m.From + "：" + body + "\n")
		if m.RefFile != "" {
			sb.WriteString("    附件 file_id=" + m.RefFile + "（用 ws-core -bus-get 取回）\n")
		}
	}
	var who []string
	for k, v := range byFrom {
		who = append(who, k+"("+itoa(v)+")")
	}
	return "【未读节点总线消息 · " + itoa(env.Count) + " 条】来自：" + strings.Join(who, "、") + "\n" +
		sb.String() +
		"处理纪律：先读正文，能办就办；办完用 `ws-core -bus-ack <id>` 标记已读，需回执则 `-bus-send` 回信。"
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	neg := n < 0
	if neg {
		n = -n
	}
	var b [20]byte
	i := len(b)
	for n > 0 {
		i--
		b[i] = byte('0' + n%10)
		n /= 10
	}
	if neg {
		i--
		b[i] = '-'
	}
	return string(b[i:])
}
