#!/bin/bash
# 部署后自检 + 自愈: 覆盖历次事故的自动检出与处理, 避免部署把服务打挂没人发现。
# 用法: post-deploy-check.sh [blue|green]   (缺省从 nginx 生效配置探测在役槽)
#
# 覆盖的事故类别:
#   1. 退役槽 stop 时 unlink 掉在役槽的 -wal/-shm → 在役槽 fd 出现 (deleted), 读写报
#      malformed。自愈: 就地重启在役槽。
#   2. 两个槽同时活着共写一个 SQLite 文件。自愈: kill 掉非在役槽。
#   3. SQLite 读路径损坏(/api/credentials/load 500)。先重启一次; 仍坏则报错退出(需人工恢复)。
#   4. 磁盘占满曾诱发真损坏。≥90% 自动 docker image prune; 清完仍高则告警。
#   5. 完整性门禁: .backup 副本跑 integrity_check + foreign_key_check。
# 退出码: 0 正常; 1 自愈后仍异常; 2 需要人工介入。
set -euo pipefail

DATA_DIR=/var/lib/docker/volumes/gpt-load_gpt-load-data/_data
DB="$DATA_DIR/gpt-load.db"
SQLITE=/tmp/sqlite3
PORTS="80 1455 54545 51121"
declare -i FAILURES=0

active_slot=${1:-}
if [ -z "$active_slot" ]; then
  active_slot=$(sed -n 's|^[[:space:]]*server 127\.0\.0\.\([0-9]*\):18081;.*|\1|p' /etc/nginx/sites-available/gpt-load)
  case "$active_slot" in
    1) active_slot=blue ;;
    10) active_slot=green ;;
    *) echo "!! cannot detect active slot from nginx"; exit 2 ;;
  esac
fi
case "$active_slot" in
  blue) SLOT_IP=127.0.0.1; OTHER=green ;;
  green) SLOT_IP=127.0.0.10; OTHER=blue ;;
  *) echo "!! unknown slot: $active_slot"; exit 2 ;;
esac
echo "==> active slot: gpt-load-$active_slot ($SLOT_IP:18081)"

wal_fd_deleted() {
  docker exec "gpt-load-$1" ls -l /proc/1/fd 2>/dev/null | grep -q 'gpt-load.db-wal (deleted)'
}

http_code() { curl -s -o /dev/null -w "%{http_code}" --noproxy "*" "$1" || true; }

echo "==> [1/7] entry ports"
for port in $PORTS; do
  code=$(http_code "http://127.0.0.1:$port/health")
  if [ "$code" = "200" ]; then
    echo "    port $port: 200"
  else
    echo "    !! port $port: $code"
    FAILURES+=1
  fi
done

echo "==> [2/7] container health"
state="missing missing"
for _ in $(seq 1 60); do
  state=$(docker inspect "gpt-load-$active_slot" --format '{{.State.Status}} {{.State.Health.Status}}' 2>/dev/null || echo "missing missing")
  [ "$state" = "running healthy" ] && break
  # healthcheck 有 start_period, starting 是正常过渡态, 等它而不是立刻重启。
  sleep 2
done
if [ "$state" = "running healthy" ]; then
  echo "    gpt-load-$active_slot: running healthy"
else
  echo "    !! gpt-load-$active_slot: $state — restarting"
  cd /opt/gpt-load && docker compose restart "gpt-load-$active_slot" >/dev/null
  sleep 5
  FAILURES+=1
fi

echo "==> [3/7] stray slot (must not share the SQLite file while alive)"
if docker ps --format '{{.Names}}' | grep -qx "gpt-load-$OTHER"; then
  echo "    !! gpt-load-$OTHER still running — killing (shared-DB writer)"
  cd /opt/gpt-load && docker compose kill "gpt-load-$OTHER" >/dev/null 2>&1 || true
  FAILURES+=1
else
  echo "    gpt-load-$OTHER not running: ok"
fi

echo "==> [4/7] active slot WAL file (deleted fd = unlinked by the retiring slot)"
if wal_fd_deleted "$active_slot"; then
  echo "    !! WAL fd deleted — restarting gpt-load-$active_slot to rebuild it"
  cd /opt/gpt-load && docker compose restart "gpt-load-$active_slot" >/dev/null
  for _ in $(seq 1 60); do
    [ "$(http_code "http://$SLOT_IP:18081/health")" = "200" ] && break
    sleep 1
  done
  if wal_fd_deleted "$active_slot"; then
    echo "    !! WAL still deleted after restart"
    FAILURES+=1
  else
    echo "    WAL rebuilt: ok"
  fi
else
  echo "    WAL fd alive: ok"
fi

echo "==> [5/7] control-plane canary (SQLite read path)"
KEY=$(cat "$DATA_DIR/auth.key")
api_code=$(curl -s -o /dev/null -w "%{http_code}" --noproxy "*" -H "Authorization: Bearer $KEY" http://127.0.0.1/api/credentials/load || true)
if [ "$api_code" = "200" ]; then
  echo "    /api/credentials/load: 200"
else
  echo "    !! /api/credentials/load: $api_code — restarting once"
  cd /opt/gpt-load && docker compose restart "gpt-load-$active_slot" >/dev/null
  sleep 8
  api_code=$(curl -s -o /dev/null -w "%{http_code}" --noproxy "*" -H "Authorization: Bearer $KEY" http://127.0.0.1/api/credentials/load || true)
  if [ "$api_code" = "200" ]; then
    echo "    recovered after restart: 200"
    FAILURES+=1
  else
    echo "    !! still $api_code after restart — DB likely corrupt, manual recovery needed"
    echo "    ==> preserve evidence: docker logs gpt-load-$active_slot; $SQLITE $DB '.backup /root/gpt-load-suspect.db'"
    exit 2
  fi
fi

echo "==> [6/7] disk headroom (disk-full has corrupted the DB before)"
usage=$(df -P / | awk 'NR==2 {gsub("%",""); print $5}')
if [ "$usage" -ge 90 ]; then
  echo "    !! disk ${usage}% — pruning dangling images"
  docker image prune -f >/dev/null
  usage=$(df -P / | awk 'NR==2 {gsub("%",""); print $5}')
fi
if [ "$usage" -ge 90 ]; then
  echo "    !! disk still ${usage}% after prune — free space manually"
  FAILURES+=1
else
  echo "    disk ${usage}%: ok"
fi

echo "==> [7/7] DB integrity gate (offline copy — NEVER open the live DB with host sqlite3)"
if [ -x "$SQLITE" ]; then
  CHECK=/tmp/gpt-load-postcheck.db
  rm -f "$CHECK" "$CHECK-wal" "$CHECK-shm"
  # 宿主 CLI 直接开活库做 .backup, 退出时会 checkpoint 并 unlink -wal/-shm ——
  # 在役进程从此攥着已删除的 inode 继续写, 这正是历次"部署后几分钟 malformed"的元凶。
  # 只 cp 三件套再查副本; sqlite 恢复/写回都发生在副本上, 活库零接触。
  if cp "$DB" "$CHECK" 2>/dev/null &&
     { [ ! -f "$DB-wal" ] || cp "$DB-wal" "$CHECK-wal"; } &&
     { [ ! -f "$DB-shm" ] || cp "$DB-shm" "$CHECK-shm"; }; then
    INTEGRITY=$("$SQLITE" "$CHECK" "PRAGMA quick_check;" 2>&1 | head -1)
    if [ "$INTEGRITY" = "ok" ]; then
      FKCOUNT=$("$SQLITE" "$CHECK" "PRAGMA foreign_key_check;" 2>&1 | wc -l)
      if [ "$FKCOUNT" -eq 0 ]; then
        echo "    integrity ok, fk violations: 0"
      else
        echo "    !! FK VIOLATIONS: $FKCOUNT rows — manual recovery needed"
        cp "$CHECK" /root/gpt-load-suspect-$(date +%Y%m%d-%H%M%S).db
        rm -f "$CHECK" "$CHECK-wal" "$CHECK-shm"
        exit 2
      fi
    else
      echo "    !! INTEGRITY FAILED: $INTEGRITY — manual recovery needed"
      cp "$CHECK" /root/gpt-load-suspect-$(date +%Y%m%d-%H%M%S).db
      rm -f "$CHECK" "$CHECK-wal" "$CHECK-shm"
      exit 2
    fi
    rm -f "$CHECK" "$CHECK-wal" "$CHECK-shm"
  else
    echo "    !! copy failed — treat as suspect"
    FAILURES+=1
  fi
else
  echo "    $SQLITE missing, skipped"
fi

if [ "$FAILURES" -gt 0 ]; then
  echo "==> post-deploy check: DEGRADED ($FAILURES issue(s), see above)"
  exit 1
fi
echo "==> post-deploy check: all clear"
