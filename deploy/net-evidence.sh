#!/usr/bin/env bash
# 网络抖动取证脚本: 分段定位丢包点, 并给出与平台错误的关联证据。
# 用法: bash /opt/gpt-load/net-evidence.sh [探测秒数, 默认 120]
# 输出: /tmp/net-evidence-<时间戳>.txt —— 报障时把整个文件发给运维。
set -u
SECONDS_BUDGET=${1:-120}
TARGET_HOST=open.bigmodel.cn
TARGET_IP=$(getent ahostsv4 "$TARGET_HOST" | awk '{print $1; exit}')
STAMP=$(date +%F_%H%M%S)
OUT="/tmp/net-evidence-$STAMP.txt"
PING_COUNT=100
CURL_ROUNDS=30

{
echo "==== 网络取证 $(date '+%F %T %Z') 预算 ${SECONDS_BUDGET}s ===="
echo "[0] 环境"
echo "target=$TARGET_HOST ($TARGET_IP)"
ip -4 -brief addr show | sed 's/^/  /'
ip route | sed 's/^/  /'

probe_ping() {
  local label=$1 host=$2
  echo "[$label] ping $host ×$PING_COUNT"
  ping -c "$PING_COUNT" -i 0.2 -w "$SECONDS_BUDGET" "$host" 2>&1 | tail -3
  echo
}

echo "==== 分段丢包/抖动(ICMP 对照) ===="
probe_ping "1 首跳=家蹭路由" 192.168.1.1
probe_ping "2 出网(阿里DNS)" 223.5.5.5
probe_ping "3 厂商边缘" "$TARGET_IP"

echo "==== TCP 443 握手时延(与应用同口径) ×$CURL_ROUNDS ===="
echo "格式: time_connect(秒)  HTTP码  (000/28=SYN超时, >1s=SYN重传丢包)"
for _ in $(seq 1 "$CURL_ROUNDS"); do
  curl --connect-timeout 8 -o /dev/null -s -w '%{time_connect} %{http_code}\n' \
    "https://$TARGET_HOST/" || true
done

echo "==== 逐跳定位(mtr TCP 443, 强制 IPv4) ===="
if command -v mtr >/dev/null 2>&1; then
  mtr -4 -T -P 443 -c 100 -r -w "$TARGET_HOST"
else
  echo "mtr 未安装: apt-get install -y mtr-tiny 后重跑; 先给 traceroute:"
  traceroute -T -p 443 -w 2 -q 3 "$TARGET_HOST" 2>&1 || traceroute -w 2 -q 3 "$TARGET_IP" 2>&1
fi

echo "==== 平台侧错误(最近10分钟, 与上面对照时间对齐) ===="
echo "connect_failed 分钟分布(request_logs 副本):"
DB=/var/lib/docker/volumes/gpt-load_gpt-load-data/_data/gpt-load.db
rm -f /tmp/ne-audit.db*; cp "$DB" /tmp/ne-audit.db; cp "$DB-wal" /tmp/ne-audit.db-wal 2>/dev/null; cp "$DB-shm" /tmp/ne-audit.db-shm 2>/dev/null
if [ -x /tmp/sqlite3 ]; then
  /tmp/sqlite3 "file:/tmp/ne-audit.db?mode=ro" \
    "SELECT datetime((completed_at_ms/1000)/60*60,'unixepoch'), error_code, COUNT(*) FROM request_logs WHERE status!='success' AND completed_at_ms > (strftime('%s','now')-600)*1000 GROUP BY 1,2 ORDER BY 1;"
fi
rm -f /tmp/ne-audit.db*
echo "==== 结束 $(date '+%F %T') → $OUT ===="
} > "$OUT" 2>&1
echo "$OUT"
