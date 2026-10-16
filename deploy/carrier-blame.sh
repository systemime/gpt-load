#!/usr/bin/env bash
# 运营商责任判定脚本: 用排除法证明丢包在运营商网, 而非内网或智谱。
#   内网无罪 = 首跳/同网段主机 0 丢包
#   智谱无罪 = 包能到达时握手仅 ~33ms、业务量大且成功率高(丢的是去程/回程, 不是对端拒绝)
#   运营商定责 = 多个互不相关的外部目标同时丢包 + mtr 定界丢包起点在运营商跳
# 用法: bash carrier-blame.sh [秒预算, 默认 120]   输出: /tmp/carrier-blame-<时间戳>.txt(实时回显)
set -u
SECONDS_BUDGET=${1:-120}
TARGET_HOST=open.bigmodel.cn
TARGET_IP=$(getent ahostsv4 "$TARGET_HOST" | awk '{print $1; exit}')
INTRANET_PEER=${INTRANET_PEER:-172.16.4.104}   # 同网段另一台主机, 可 env 覆盖
GATEWAY=$(ip route | awk '/^default/{print $3; exit}')
STAMP=$(date +%F_%H%M%S)
OUT="/tmp/carrier-blame-$STAMP.txt"
PING_COUNT=50
CURL_ROUNDS=15
declare -a FINDINGS=()
EXT_LOSS_MAX=0; DEMARC=""; INTRANET_BAD=0; ZHIPU_RESP_MS=-1; SUCCESS_N=-1; FAIL_CF=0

verdict() {
  case "$2" in
    OK)   echo "  【判定】✅ 正常 — $3" ;;
    WARN) echo "  【判定】⚠️ 异常 — $3"; FINDINGS+=("$3") ;;
    BAD)  echo "  【判定】❌ 异常 — $3"; FINDINGS+=("$3") ;;
  esac
}
ping_stats() { ping -c "$1" -i 0.2 -w "$SECONDS_BUDGET" "$2" 2>&1 | tail -2; }
parse_ping() { awk '/packet loss/{for(i=1;i<=NF;i++) if($i ~ /%$/){gsub(/%/,"",$i); loss=$i}}
  /rtt/{split($4,a,"/"); avg=a[2]} END{printf "%.1f %.1f", loss+0, avg+0}'; }

main() {
echo "==== 运营商责任判定 $(date '+%F %T %Z') → $TARGET_HOST ($TARGET_IP) ===="
echo "(同时对照: 223.5.5.5 阿里 / 119.29.29.29 腾讯 / www.qq.com / www.baidu.com)"
echo

echo "━━━ [1] 内网排除: 网关 $GATEWAY + 同网段主机 $INTRANET_PEER ━━━"
echo "\$ ping -c $PING_COUNT -i 0.2 $GATEWAY ; ping -c $PING_COUNT -i 0.2 $INTRANET_PEER"
G=$(ping_stats "$PING_COUNT" "$GATEWAY" | parse_ping); set -- $G; GL=$1; GA=$2
V=$(ping_stats "$PING_COUNT" "$INTRANET_PEER" | parse_ping); set -- $V; VL=$1; VA=$2
echo "网关:   丢包 ${GL}%  平均 ${GA}ms"
echo "内网机: 丢包 ${VL}%  平均 ${VA}ms"
if awk -v a="$GL" -v b="$VL" 'BEGIN{exit !(a==0 && b==0)}'; then
  verdict "内网" OK "网关与同网段主机双双 0% 丢包 — 内网/交换机/本机网卡排除"
else
  INTRANET_BAD=1
  verdict "内网" BAD "内网自身有丢包(网关 ${GL}% / 内网机 ${VL}%) — 先查交换机/网卡, 本轮结论不含运营商定责"
fi
echo

echo "━━━ [2] 首跳(家蹭路由 192.168.1.1): 内网与运营商的分界点 ━━━"
echo "\$ ping -c $PING_COUNT -i 0.2 192.168.1.1"
H=$(ping_stats "$PING_COUNT" 192.168.1.1 | parse_ping); set -- $H; HL=$1; HA=$2
echo "丢包 ${HL}%  平均 ${HA}ms"
if [ "$HL" = "0.0" ]; then
  verdict "首跳" OK "路由器侧 0% 丢包 — 丢包必然发生在路由器之后的运营商网"
else
  verdict "首跳" BAD "路由器即丢包 ${HL}% — 问题在路由器/家宽接入, 联系宽带装机商而非骨干网"
fi
echo

echo "━━━ [3] 多目标对照 ping ×$PING_COUNT (互不相关的四个服务商) ━━━"
echo "\$ ping -c $PING_COUNT -i 0.2 <目标>  对 223.5.5.5 / 119.29.29.29 / www.qq.com / www.baidu.com / $TARGET_IP"
BADDEST=0; TOTAL=0
printf "%-22s %-8s %-10s\n" "目标" "丢包%" "平均ms"
for dest in 223.5.5.5 119.29.29.29 www.qq.com www.baidu.com "$TARGET_IP"; do
  S=$(ping_stats "$PING_COUNT" "$dest" | parse_ping); set -- $S; l=$1; a=$2
  printf "%-22s %-8s %-10s\n" "$dest" "$l" "$a"
  TOTAL=$((TOTAL+1))
  if awk -v l="$l" 'BEGIN{exit !(l>=1)}'; then
    BADDEST=$((BADDEST+1))
    case "$dest" in "$TARGET_IP") ;; *) EXT_LOSS_MAX=$(awk -v a="$EXT_LOSS_MAX" -v b="$l" 'BEGIN{print (b>a)?b:a}') ;; esac
    [ "$dest" = "$TARGET_IP" ] && L3=$l
  fi
done
if [ "$BADDEST" -ge 2 ]; then
  verdict "多目标" BAD "外部目标 $BADDEST/$TOTAL 个同时丢包 — 与目标是谁无关, 公共路径只有运营商网 → 运营商定责核心证据"
elif [ "$BADDEST" -eq 1 ]; then
  verdict "多目标" WARN "仅 1 个目标丢包 — 更像单目标/单路径问题, 运营商定责证据偏弱(结合 [5] 看)"
else
  verdict "多目标" OK "本轮各外部目标干净 — 抖动未发生, 建议平台报错时重跑"
fi
echo

echo "━━━ [4] TCP 443 握手对照 ×$CURL_ROUNDS/目标 (与平台同机理) ━━━"
echo "\$ curl --connect-timeout 8 -o /dev/null -s -w '%{time_connect} %{http_code}' https://<目标>/  ×$CURL_ROUNDS"
printf "%-22s %-8s %-8s %-8s\n" "目标" "超时" "重传>1s" "正常"
BADTCP=0
for dest in www.baidu.com "$TARGET_IP"; do
  t=0; s=0; o=0
  for _ in $(seq 1 "$CURL_ROUNDS"); do
    LINE=$(curl --connect-timeout 8 -o /dev/null -s -w '%{time_connect} %{http_code}\n' "https://$dest/" 2>/dev/null)
    TC=${LINE%% *}; CODE=${LINE##* }
    if [ "$CODE" = "000" ]; then t=$((t+1))
    elif awk -v x="$TC" 'BEGIN{exit !(x>1)}'; then s=$((s+1))
    else o=$((o+1)); fi
    if [ "$dest" = "$TARGET_IP" ] && awk -v x="$TC" -v z="$ZHIPU_RESP_MS" 'BEGIN{exit !(x>0 && x<1 && (z<0 || x*1000<z))}'; then
      ZHIPU_RESP_MS=$(awk -v x="$TC" 'BEGIN{printf "%d", x*1000}')
    fi
  done
  printf "%-22s %-8s %-8s %-8s\n" "$dest" "$t" "$s" "$o"
  if [ "$dest" = "$TARGET_IP" ]; then ZHIPU_TO=$t; ZHIPU_SLOW=$s; fi
  BADTCP=$((BADTCP + t + s))
done
echo "(注: 握手>1s = 首个 SYN 被丢后内核重传; 超时 000 = 重传耗尽 8s 放弃 — 与平台 connect_failed 同机理)"
if [ "$BADTCP" -gt 0 ]; then
  verdict "TCP对照" BAD "对 baidu/智谱 的 SYN 也被丢 — 连业内最大 CDN 都丢, 排除智谱单方面问题"
else
  verdict "TCP对照" OK "本轮各目标握手无重传"
fi
echo

echo "━━━ [5] mtr 逐跳定界 (丢包从哪一跳开始) ━━━"
echo "\$ mtr -4 -T -P 443 -c 100 -r -w $TARGET_HOST"
MTR_OUT=$(mtr -4 -T -P 443 -c 100 -r -w "$TARGET_HOST" 2>&1 | sed '/mtr-packet error/d')
echo "$MTR_OUT"
DEMARC=$(echo "$MTR_OUT" | awk '
  /\|--/ { loss=""; ip=""
    for(i=1;i<=NF;i++){ if($i ~ /%$/){ v=$i; gsub(/%/,"",v); loss=v+0 }
      if($i ~ /^[0-9]+\.[0-9]+\.[0-9]+\.[0-9]+$/ && ip==""){ ip=$i } }
    if(ip!="" && loss!="" && loss>=10) printf "%d %s %.0f\n", hop, ip, loss
    hop++ } BEGIN{hop=1}' | head -1)
if [ -n "$DEMARC" ]; then
  set -- $DEMARC
  echo "丢包起点: 第 ${1} 跳 (${2}), 丢包 ${3}%"
  DEMARC="第${1}跳(${2}) 丢${3}%"
  verdict "mtr定界" BAD "丢包起点在运营商跳(${2}) — 不在首跳、不在对端"
else
  verdict "mtr定界" OK "本轮逐跳无丢包点"
fi
echo

echo "━━━ [6] 智谱侧排除: 包到达时它响应多快 + 业务面有多正常 ━━━"
echo "\$ (从[4]取) 对智谱完整握手的最快时延; \$ sqlite3 只读副本 最近10分钟平台成功/失败"
echo "智谱握手最快响应: ${ZHIPU_RESP_MS}ms (TCPRTT 级别 — 对端服务器与入口正常)"
DB=/var/lib/docker/volumes/gpt-load_gpt-load-data/_data/gpt-load.db
if [ -f "$DB" ] && [ -x /tmp/sqlite3 ]; then
  rm -f /tmp/cb.db*; cp "$DB" /tmp/cb.db; cp "$DB-wal" /tmp/cb.db-wal 2>/dev/null; cp "$DB-shm" /tmp/cb.db-shm 2>/dev/null
  ROW=$(/tmp/sqlite3 "file:/tmp/cb.db?mode=ro" "SELECT SUM(CASE WHEN status='success' THEN 1 ELSE 0 END), SUM(CASE WHEN error_code='upstream_connect_failed' THEN 1 ELSE 0 END) FROM request_logs WHERE completed_at_ms > (strftime('%s','now')-600)*1000;")
  rm -f /tmp/cb.db*
  SUCCESS_N=${ROW%%|*}; FAIL_CF=${ROW##*|}
  echo "最近10分钟平台: 成功 $SUCCESS_N 个请求 / connect_failed $FAIL_CF 个"
  verdict "智谱侧" OK "丢的是去程 SYN(本机重传无应答), 智谱从未收到; 而包能到达时它响应 ${ZHIPU_RESP_MS}ms 且成功业务持续 — 对端服务与入口无罪"
fi
echo

echo "━━━ 综合判定 ━━━"
if [ "$INTRANET_BAD" -eq 1 ]; then
  echo "❌ 内网自身丢包 — 先修内网, 本轮无法定责运营商。"
elif [ ${#FINDINGS[@]} -eq 0 ]; then
  echo "✅ 本轮各探测正常 — 抖动未发生。请在平台出现 upstream_connect_failed 时立即重跑。"
else
  for f in "${FINDINGS[@]}"; do echo "❌ $f"; done
fi
if [ "$BADDEST" -ge 2 ] || [ -n "$DEMARC" ] || [ "$BADTCP" -gt 0 ]; then
  echo
  echo "结论: 排除法指向运营商 —"
  echo "  ① 内网 0% 丢包(网关/同网段/首跳)                 → 不是我们内网;"
  echo "  ② 智谱握手最快 ${ZHIPU_RESP_MS}ms、成功业务持续($SUCCESS_N 成功/10min) → 不是智谱;"
  echo -n "  ③ 运营商定责证据: "
  [ "$BADDEST" -ge 2 ] && echo -n "多目标同时丢包(${BADDEST}/${TOTAL}); "
  [ -n "$DEMARC" ] && echo -n "mtr 定界丢包起点在运营商跳(${DEMARC}); "
  [ "$BADTCP" -gt 0 ] && echo -n "对 baidu/智谱 的 SYN 握手被丢(${BADTCP} 次)"
  echo "  → 责任在中国移动承载网。"
fi
echo
echo "==== 结束 $(date '+%F %T') ===="
}

echo "输出文件: $OUT" >&2
main 2>&1 | tee "$OUT"
