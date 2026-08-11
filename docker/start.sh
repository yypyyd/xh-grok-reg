#!/usr/bin/env bash
# xh-grok-reg 容器入口：先拉起 Xvfb 虚拟显示（供 Turnstile offscreen 模式），再启动服务。
set -e

# 首次启动 rod 会自动下载 Chromium（browser / browser-grok）到 ~/.cache/rod，
# 已通过 volume 挂载 /root/.cache 持久化，避免容器重建后重复下载。
if ! pgrep -x Xvfb >/dev/null 2>&1; then
  Xvfb :99 -screen 0 1280x1024x24 >/tmp/xvfb.log 2>&1 &
  sleep 1
fi

export DISPLAY=:99
exec /usr/local/bin/xh-grok-reg
