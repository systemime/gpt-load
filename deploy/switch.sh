#!/bin/bash
# 蓝绿切换: ./switch.sh blue|green  (槽 IP: blue=127.0.0.1, green=127.0.0.10, 端口 18081)
set -euo pipefail
TARGET=${1:?usage: switch.sh blue|green}
[[ "$TARGET" == "blue" ]] && IP=127.0.0.1 || IP=127.0.0.10
OTHER=$([[ "$TARGET" == "blue" ]] && echo green || echo blue)
cd /opt/gpt-load
echo "==> start gpt-load-$TARGET ($IP:18081)"
docker compose up -d gpt-load-$TARGET
echo -n "==> wait /health"
for i in $(seq 1 120); do
  code=$(curl -s -o /dev/null -w "%{http_code}" --noproxy "*" http://$IP:18081/health || true)
  [ "$code" = "200" ] && break
  echo -n "."; sleep 1
done
echo
[ "${code:-}" = "200" ] || { echo "target not ready"; exit 1; }
sed -i "s|server 127\.0\.0\.[0-9]*:18081;|server $IP:18081;|" /etc/nginx/sites-available/gpt-load
# sites-enabled 必须是指向 sites-available 的符号链接(曾为独立副本导致 sed 不生效)
[ -L /etc/nginx/sites-enabled/gpt-load ] || { echo "sites-enabled/gpt-load is not a symlink"; exit 1; }
nginx -t >/dev/null && systemctl reload nginx
# 防呆: 确认生效配置已指向目标槽
grep -q "server $IP:18081;" <(nginx -T 2>/dev/null) || { echo "effective nginx config not pointed at $IP"; exit 1; }
echo "==> nginx switched to $IP:18081"
# 退役槽必须 kill 而不是 stop: 两个槽共用同一 SQLite 文件, 干净关闭会做 checkpoint 并按
# 文件名 unlink -wal/-shm, 而该名字此刻已属于新槽 —— 新槽的 WAL 被抽走, 后续读写报
# "database disk image is malformed (11)"(2026-10-14 事故)。SIGKILL 不做清理, WAL 由
# 新槽按崩溃恢复语义接管。nginx 已切走, 退役槽只剩在途流, 本就会被 stop 超时切断。
docker compose kill gpt-load-$OTHER 2>/dev/null || true
echo "==> active: gpt-load-$TARGET"
# 防呆 2: 确认在役槽的 WAL 仍是有效文件(未被退役槽 unlink); 出现 (deleted) 就地重启。
# WAL 名字被抽走后进程仍持有已删除的 inode, 直到崩溃前都"看起来正常", 必须显式检出。
if docker exec gpt-load-$TARGET ls -l /proc/1/fd 2>/dev/null | grep -q 'gpt-load.db-wal (deleted)'; then
  echo "==> !!! active slot lost its WAL (unlinked by the retiring slot); restarting it"
  docker compose restart gpt-load-$TARGET
  for i in $(seq 1 60); do
    code=$(curl -s -o /dev/null -w "%{http_code}" --noproxy "*" http://$IP:18081/health || true)
    [ "$code" = "200" ] && break
    sleep 1
  done
  [ "${code:-}" = "200" ] || { echo "active slot unhealthy after WAL recovery restart"; exit 1; }
  echo "==> active slot restarted with a fresh WAL"
fi

# 部署后自检 + 自愈: 端口/容器/WAL/共写槽/SQLite 读路径/磁盘/完整性, 详见该脚本头部。
if [ -f /opt/gpt-load/post-deploy-check.sh ]; then
  /opt/gpt-load/post-deploy-check.sh "$TARGET" || exit $?
else
  echo "==> post-deploy-check.sh missing, skipped"
fi
