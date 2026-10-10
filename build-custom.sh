#!/usr/bin/env bash
# 基于 Dockerfile.custom 构建覆盖官方镜像的自定义 gpt-load 镜像。
# 用法:
#   ./build-custom.sh                     # 默认 tag gpt-load:cap, 官方源
#   ./build-custom.sh gpt-load:cap-cn     # 自定义 tag
#   BASE_IMAGE=ghcr.nju.edu.cn/tbphp/gpt-load:2 ./build-custom.sh   # 走镜像代理
set -euo pipefail

TAG="${1:-gpt-load:cap}"
BASE_IMAGE="${BASE_IMAGE:-ghcr.io/tbphp/gpt-load:2}"
VERSION="${VERSION:-2.0.0-cap}"

cd "$(dirname "$0")"

echo "==> base: ${BASE_IMAGE}"
echo "==> tag:  ${TAG}"
echo "==> ver:  ${VERSION}"

docker build -f Dockerfile.custom \
    --build-arg BASE_IMAGE="${BASE_IMAGE}" \
    --build-arg VERSION="${VERSION}" \
    -t "${TAG}" .

echo "==> done: ${TAG}"
docker image ls "${TAG}"
