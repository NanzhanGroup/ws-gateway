#!/usr/bin/env bash
# wsgw-client-sync.sh —— 把 ws-gateway 共享包（任务中枢 / 任务上下文 / 总线上下文 /
# 单实例锁 / 内置命令 WSC 接入层）从**本仓（规范源）**同步为各消费仓的**内联副本**。
#
# 背景（2026-09-29）：
#   ws-gateway 是**公开仓**（GitHub API HTTP 200），而它是 6 个对话网关 + device-gateway
#   的共享依赖。内联到各消费仓（网关仓均为**私有仓**）后：
#     ① 消除对**第二个公开仓**的构建期依赖，是网关 PuXian（普贤）化的前置；
#     ② 各网关退化为「协议 + 自包含代码」，可逐文件 .px 重写。
#
# 规范源：本仓 /data/code/ws-gateway（6 文件，纯 stdlib，无第三方依赖）
#   buscontext.go lock.go taskcontext.go taskhub.go types.go wscmd.go
#
# 消费方 → <repo>/internal/wsgw/（包名保持 gateway，故**调用点零改动**，
#          仅 import 路径字符串替换 + go.mod 删 require/replace）
#
# 用法：
#   scripts/wsgw-client-sync.sh            # 同步全部消费方
#   scripts/wsgw-client-sync.sh --check    # 只校验副本与源是否一致（CI/巡检用，漂移 rc=1）
#   WSGW_SRC=/path/to/src scripts/wsgw-client-sync.sh
set -euo pipefail

SRC="${WSGW_SRC:-/data/code/ws-gateway}"
CODE_DIR="${WSGW_CODE_DIR:-/data/code}"
CHECK=0
[ "${1:-}" = "--check" ] && CHECK=1

FILES=(buscontext.go lock.go taskcontext.go taskhub.go types.go wscmd.go)

# 消费方：<仓目录>  <- 6 个对话网关 + 设备网关（server 子模块）
CONSUMERS=(
  "qq-gateway|qq-gateway"
  "weixin-gateway|weixin-gateway"
  "wecom-gateway|wecom-gateway"
  "feishu-gateway|feishu-gateway"
  "telegram-gateway|telegram-gateway"
  "email-gateway|email-gateway"
  "device-gateway/server|device-gateway/server"
)

[ -d "$SRC" ] || { echo "错误：规范源不存在 $SRC" >&2; exit 1; }

banner() {
  local f="$1" imp="$2"
  printf '// ⚠ 本文件为**内联副本**（DO NOT EDIT）。\n// 规范源：ws-gateway/%s\n// 同步：%s/internal/wsgw\n// 包名保持 gateway（与原模块一致），调用点无需改动。\n// 修改请改源文件，再执行 scripts/wsgw-client-sync.sh 同步。\n\n' "$f" "$imp"
}

write_copy() { # $1=目标目录 $2=文件名 $3=消费方模块路径
  local dst="$1" f="$2" imp="$3" tmp
  tmp="$(mktemp)"
  { banner "$f" "$imp"; cat "$SRC/$f"; } > "$tmp"
  if [ "$CHECK" = "1" ]; then
    if [ ! -f "$dst/$f" ] || ! diff -q <(tail -n +7 "$tmp") <(tail -n +7 "$dst/$f") >/dev/null 2>&1; then
      echo "  ✗ 漂移: $dst/$f"; rm -f "$tmp"; return 1
    fi
    rm -f "$tmp"; echo "  ✓ $dst/$f"; return 0
  fi
  mv "$tmp" "$dst/$f"
  echo "  → $dst/$f"
}

rc=0
echo "规范源: $SRC"
echo "$([ $CHECK = 1 ] && echo '模式: 校验' || echo '模式: 同步')"

for entry in "${CONSUMERS[@]}"; do
  dir="${entry%%|*}"; modpath="${entry##*|}"
  echo "[$dir]"
  dst="$CODE_DIR/$dir/internal/wsgw"
  if [ "$CHECK" = 1 ]; then
    for f in "${FILES[@]}"; do write_copy "$dst" "$f" "$modpath" || rc=1; done
  else
    mkdir -p "$dst"
    for f in "${FILES[@]}"; do write_copy "$dst" "$f" "$modpath"; done
    # 改写 import 路径（含测试文件）
    hits="$(grep -rl 'github.com/NanzhanGroup/ws-gateway' "$CODE_DIR/$dir" --include='*.go' 2>/dev/null || true)"
    if [ -n "$hits" ]; then
      sed -i "s|github.com/NanzhanGroup/ws-gateway|github.com/NanzhanGroup/$modpath/internal/wsgw|g" $hits
      echo "  import 改写: $(echo "$hits" | wc -l) 个 .go 文件"
    fi
    # go.mod 删 require/replace
    if [ -f "$CODE_DIR/$dir/go.mod" ]; then
      sed -i '/NanzhanGroup\/ws-gateway/d' "$CODE_DIR/$dir/go.mod"
      echo "  go.mod: 已删 ws-gateway require/replace"
    fi
  fi
done
exit $rc
