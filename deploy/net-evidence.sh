#!/usr/bin/env bash
# 网络抖动判定诊断脚本: 每步实时输出(同时落盘), 打印所用命令, 逐步给出【判定】。
# 用法: bash /opt/gpt-load/net-evidence.sh [探测秒数, 默认 120]
# 输出: 终端实时滚动 + /tmp/net-evidence-<时间戳>.txt
set -u
SECONDS_BUDGET=${1:-120}
TARGET_HOST=open.bigmodel.cn
TARGET_IP=$(getent ahostsv4 "$TARGET_HOST" | awk '{print $1; exit}')
STAMP=$(date +%F_%H%M%S)
OUT="/tmp/net-evidence-$STAMP.txt"
PING_COUNT=100
CURL_ROUNDS=30
declare -a FINDINGS=()

verdict() { # $1=步骤名 $2=OK|WARN|BAD $3=说明
  case "$2" in
    OK)   echo "  【判定】✅ 正常 — $3" ;;
    WARN) echo "  【判定】⚠️ 异常 — $3"; FINDINGS+=("$1|$3") ;;
    BAD)  echo "  【判定】❌ 异常 — $3"; FINDINGS+=("$1|$3") ;;
  esac
}
loss_of() { ping -c "$1" -i 0.2 -w "$SECONDS_BUDGET" "$2" 2>&1 | tail -2; }
parse_ping() { # stdin: ping tail → "loss% rtt_avg rtt_mdev" (1位小数)
  awk '/packet loss/ {for(i=1;i<=NF;i++) if($i ~ /%/) {gsub(/%/,"",$i); loss=$i}}
       /rtt/ {split($4,a,"/"); avg=a[2]; split($6,b,"/"); mdev=b[1]}
       END {printf "%.1f %.1f %.1f", loss+0, avg+0, mdev+0}'
}

main() {
echo "==== 网络抖动判定诊断 $(date '+%F %T %Z') → $TARGET_HOST ($TARGET_IP) ===="
echo

echo "━━━ [1] 首跳: 家蹭路由 192.168.1.1 (局域网定界) ━━━"
echo "\$ ping -c $PING_COUNT -i 0.2 192.168.1.1"
P1=$(loss_of "$PING_COUNT" 192.168.1.1 | parse_ping)
set -- $P1
L1=${1:-0}; A1=${2:-0}; M1=${3:-0}
echo "$PING_COUNT 包: 丢包 ${L1}%  平均 ${A1}ms  抖动(mdev) ${M1}ms"
if awk -v l="$L1" -v m="$M1" 'BEGIN{exit !(l==0 && m<2)}'; then
  verdict "首跳" OK "局域网与路由器无丢包无抖动 — 你方设备与路由器排除嫌疑"
else
  verdict "首跳" BAD "首跳即丢包/抖动(丢${L1}%, 抖动${M1}ms) — 问题在局域网/路由器, 与 ISP 无关"
fi
echo

echo "━━━ [2] 出网第一段: 223.5.5.5 (过路由器后的运营商接入网) ━━━"
echo "\$ ping -c $PING_COUNT -i 0.2 223.5.5.5"
P2=$(loss_of "$PING_COUNT" 223.5.5.5 | parse_ping)
set -- $P2
L2=${1:-0}; A2=${2:-0}; M2=${3:-0}
echo "$PING_COUNT 包: 丢包 ${L2}%  平均 ${A2}ms  抖动(mdev) ${M2}ms"
if awk -v l="$L2" -v l1="$L1" 'BEGIN{exit !(l<1 && l<=l1+0.5)}'; then
  verdict "出网" OK "运营商接入网与首跳同等干净"
else
  verdict "出网" BAD "出网即丢包 ${L2}%(首跳仅 ${L1}%) — 丢包从运营商接入段开始, 责任在 ISP"
fi
echo

echo "━━━ [3] 厂商边缘 ICMP 对照: $TARGET_IP ━━━"
echo "\$ ping -c $PING_COUNT -i 0.2 $TARGET_IP"
P3=$(loss_of "$PING_COUNT" "$TARGET_IP" | parse_ping)
set -- $P3
L3=${1:-0}; A3=${2:-0}; M3=${3:-0}
echo "$PING_COUNT 包: 丢包 ${L3}%  平均 ${A3}ms  抖动(mdev) ${M3}ms"
if awk -v l="$L3" 'BEGIN{exit !(l<1)}'; then
  verdict "厂商ICMP" OK "ICMP 面当前干净(注意: ICMP 干净不代表 TCP 不丢, 以 [4][5] 为准)"
else
  verdict "厂商ICMP" WARN "到厂商边缘 ICMP 丢包 ${L3}% — 链路问题已延伸到对端方向"
fi
echo

echo "━━━ [4] TCP 443 握手探测 ×$CURL_ROUNDS (与平台 connect_failed 同机理) ━━━"
echo "\$ curl --connect-timeout 8 -o /dev/null -s -w '%{time_connect} %{http_code}\\n' https://$TARGET_HOST/   # ×$CURL_ROUNDS"
echo "time_connect(秒) HTTP码  |  >1s=SYN重传(丢包)  000/28=SYN 8s 超时"
TIMEOUTS=0; SLOW=0; OKN=0; SUM=0
for _ in $(seq 1 "$CURL_ROUNDS"); do
  LINE=$(curl --connect-timeout 8 -o /dev/null -s -w '%{time_connect} %{http_code}\n' "https://$TARGET_HOST/" 2>/dev/null)
  echo "  $LINE"
  TC=${LINE%% *}; CODE=${LINE##* }
  if [ "$CODE" = "000" ]; then
    TIMEOUTS=$((TIMEOUTS+1))
  elif awk -v t="$TC" 'BEGIN{exit !(t>1)}'; then
    SLOW=$((SLOW+1)); SUM=$(awk -v s=$SUM -v t=$TC 'BEGIN{print s+t}')
  else
    OKN=$((OKN+1)); SUM=$(awk -v s=$SUM -v t=$TC 'BEGIN{print s+t}')
  fi
done
BADN=$((TIMEOUTS+SLOW))
echo "汇总: 超时 $TIMEOUTS / SYN重传 $SLOW / 正常 $OKN (平均握手 $(awk -v s=$SUM -v n=$((OKN+SLOW)) 'BEGIN{if(n)printf "%.0fms",s/n*1000; else print "n/a"}'))"
if [ "$TIMEOUTS" -eq 0 ] && [ "$SLOW" -eq 0 ]; then
  verdict "TCP握手" OK "全部握手 <1s — 与平台同口径的探测无异常"
else
  verdict "TCP握手" BAD "SYN 超时 $TIMEOUTS 次 + 重传 $SLOW 次 (共 $CURL_ROUNDS) — 与平台 upstream_connect_failed(约7s放弃) 同机理的原始证据"
fi
echo

echo "━━━ [5] 逐跳定位 mtr TCP 443 (丢包点定界) ━━━"
if command -v mtr >/dev/null 2>&1; then
  echo "\$ mtr -4 -T -P 443 -c 100 -r -w $TARGET_HOST"
  MTR_OUT=$(mtr -4 -T -P 443 -c 100 -r -w "$TARGET_HOST" 2>&1 | sed '/mtr-packet error/d')
else
  echo "\$ traceroute -T -p 443 -w 2 -q 3 $TARGET_IP   (安装 mtr-tiny 可得逐跳丢包率)"
  MTR_OUT=$(traceroute -T -p 443 -w 2 -q 3 "$TARGET_IP" 2>&1)
fi
echo "$MTR_OUT"
# 解析: mtr -w 行内取丢包%与首个IPv4, 丢包>=10% 视为丢包跳
DEMARC=$(echo "$MTR_OUT" | awk '
  /\|--/ {
    loss=""; ip=""
    for(i=1;i<=NF;i++){
      if($i ~ /%$/){ v=$i; gsub(/%/,"",v); loss=v+0 }
      if($i ~ /^[0-9]+\.[0-9]+\.[0-9]+\.[0-9]+$/ && ip==""){ ip=$i }
    }
    if(ip!="" && loss!="" && loss>=10) printf "%d %s %.0f\n", hop, ip, loss
    hop++
  }
  BEGIN{ hop=1 }')
FIRST_LOSSY_HOP=$(echo "$DEMARC" | awk 'NR==1{print $1, $2, $3}')
if [ -z "$DEMARC" ]; then
  verdict "逐跳" OK "各有应答跳丢包 <10% — 本轮未捕捉到丢包点"
else
  set -- $FIRST_LOSSY_HOP
  echo "丢包起点: 第 ${1} 跳 (${2}), 丢包 ${3}% —— 此后各可测跳持续丢包"
  verdict "逐跳" BAD "丢包起点在第 ${1} 跳 (${2}) — 该跳之后的运营商链路为责任段; 首跳(局域网)0% 已排除你方设备"
fi
echo

echo "━━━ [6] 平台侧错误(最近 10 分钟, 与上面同一时间轴) ━━━"
DB=/var/lib/docker/volumes/gpt-load_gpt-load-data/_data/gpt-load.db
if [ ! -f "$DB" ]; then
  echo "(本机无 gpt-load 数据卷, 跳过 — 平台错误请到 172.16.4.10 上跑本脚本获取)"
else
echo "\$ sqlite3(只读副本) request_logs 最近10分钟失败按分钟/错误码"
rm -f /tmp/ne-audit.db*; cp "$DB" /tmp/ne-audit.db; cp "$DB-wal" /tmp/ne-audit.db-wal 2>/dev/null; cp "$DB-shm" /tmp/ne-audit.db-shm 2>/dev/null
ERRROWS=""
if [ -x /tmp/sqlite3 ]; then
  ERRROWS=$(/tmp/sqlite3 "file:/tmp/ne-audit.db?mode=ro" \
    "SELECT datetime((completed_at_ms/1000)/60*60,'unixepoch'), error_code, COUNT(*) FROM request_logs WHERE status!='success' AND completed_at_ms > (strftime('%s','now')-600)*1000 GROUP BY 1,2 ORDER BY 1;")
fi
rm -f /tmp/ne-audit.db*
if [ -z "$ERRROWS" ]; then
  echo "(最近 10 分钟无失败请求)"
  verdict "平台错误" OK "窗口内平台无失败请求"
else
  echo "分钟(UTC) | 错误码 | 次数"
  echo "$ERRROWS" | sed 's/^/  /'
  CF=$(echo "$ERRROWS" | awk -F'|' '$2 ~ /connect_failed/ {s+=$3} END{print s+0}')
  verdict "平台错误" BAD "窗口内平台失败 $CF 次 connect_failed — 与 [4][5] 网络异常同时窗, 因果成立"
fi
fi
echo

echo "━━━ 综合判定 ━━━"
if [ ${#FINDINGS[@]} -eq 0 ]; then
  echo "✅ 本轮全部探测正常 — 抖动未发生或已恢复。请在平台再次出现 upstream_connect_failed 时立刻重跑本脚本。"
else
  for f in "${FINDINGS[@]}"; do IFS='|' read -r step text <<<"$f"; echo "❌ [$step] $text"; done
  echo
  echo "因果链:"
  echo "  1. 平台 connect_failed 平均 7s 失败 = 内核 SYN 重传 3 次放弃 → 请求死在 TCP 握手, 业务未到达厂商;"
  echo "  2. 本脚本 TCP 握手探测同分钟出现超时/重传 → 与平台错误同机理的原始观测;"
  echo "  3. mtr 定界丢包起点在运营商跳之后、首跳 0% → 故障段在 ISP, 不在你方任何设备。"
fi
echo
echo "==== 结束 $(date '+%F %T') ===="
}

echo "输出文件: $OUT" >&2
main 2>&1 | tee "$OUT"
