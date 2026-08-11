# syntax=docker/dockerfile:1
# ============================================================
# xh-grok-reg · Docker 一键部署（完整版：支持 Grok 自动注册）
#
# 多阶段构建：
#   Stage 1  node:20-slim   → Vite 构建前端（产物输出到 /static）
#   Stage 2  golang:1.25    → CGO_ENABLED=0 编译单二进制（go:embed static）
#   Stage 3  ubuntu:24.04   → 运行时：CloakBrowser + Chromium + Xvfb + Python helper
#
# 构建：docker build -t xh-grok-reg:latest .
# 运行：docker compose up -d
# ============================================================

# ---------- Stage 1: 构建前端 ----------
FROM node:20-slim AS frontend
WORKDIR /frontend
# 固定 pnpm 9（与 pnpm-lock.yaml v9 匹配；corepack 默认 latest 需 Node 22+）
RUN corepack enable && corepack prepare pnpm@9 --activate
COPY frontend/package.json frontend/pnpm-lock.yaml ./
RUN pnpm install --frozen-lockfile
COPY frontend/ ./
# vite build.outDir = '../static'（相对 frontend/）→ 产物落在 /static
RUN pnpm build

# ---------- Stage 2: 编译 Go 二进制 ----------
FROM golang:1.25-alpine AS builder
WORKDIR /src
# 国内网络访问 proxy.golang.org 不稳定，改用 goproxy.cn（可自行调整）
ENV GOPROXY=https://goproxy.cn,direct
COPY go.mod go.sum ./
RUN go mod download
COPY . .
# go:embed static 要求构建时 static/ 存在（由 Stage1 提供）
COPY --from=frontend /static ./static
RUN CGO_ENABLED=0 GOOS=linux go build -trimpath -ldflags="-s -w" -o /out/xh-grok-reg .

# ---------- Stage 3: 运行时 ----------
FROM ubuntu:24.04
ENV DEBIAN_FRONTEND=noninteractive \
    HOME=/root

# 系统依赖：Xvfb 虚拟显示 + Chromium/CloakBrowser 运行库
RUN apt-get update && apt-get install -y --no-install-recommends \
      ca-certificates curl xvfb python3 python3-venv python3-pip \
      libnss3 libnspr4 libatk1.0-0 libatk-bridge2.0-0 libcups2 libdrm2 \
      libxkbcommon0 libxcomposite1 libxdamage1 libxfixes3 libxrandr2 \
      libgbm1 libasound2t64 libpango-1.0-0 libcairo2 libatspi2.0-0 \
      libglib2.0-0 libdbus-1-3 libx11-xcb1 libxcb1 libxext6 libxi6 \
      libxtst6 libxss1 libcurl4 fonts-liberation \
    && rm -rf /var/lib/apt/lists/*

# Turnstile helper（turnstile_mint.py）运行环境
COPY scripts/ /usr/local/share/grok-reg/
RUN python3 -m venv /opt/cloakbrowser-venv
# 国内网络访问 PyPI 不稳定，改用清华镜像（可自行调整）
RUN /opt/cloakbrowser-venv/bin/pip install --no-cache-dir \
      -i https://pypi.tuna.tsinghua.edu.cn/simple \
      -r /usr/local/share/grok-reg/requirements-turnstile.txt

# 安装 CloakBrowser（自带 Chromium 到 ~/.cloakbrowser）
# CLOAKBROWSER_DOWNLOAD_URL = 下载 base URL，cloakbrowser 会自行拼接
#   /chromium-v{版本}/xxx.tar.gz。国内网络可用 GitHub 加速源包装官方 releases：
#   docker build --build-arg CLOAKBROWSER_DOWNLOAD_URL=https://gh.ddlc.top/https://github.com/CloakHQ/cloakbrowser/releases/download ...
# 或走代理：
#   docker build --build-arg BUILD_HTTPS_PROXY=http://代理:端口 ...
# 默认自动重试 5 次。
ARG CLOAKBROWSER_DOWNLOAD_URL=""
ARG BUILD_HTTPS_PROXY=""
RUN if [ -n "${CLOAKBROWSER_DOWNLOAD_URL}" ]; then \
      export CLOAKBROWSER_DOWNLOAD_URL="${CLOAKBROWSER_DOWNLOAD_URL}"; \
    fi; \
    if [ -n "${BUILD_HTTPS_PROXY}" ]; then \
      export https_proxy="${BUILD_HTTPS_PROXY}" http_proxy="${BUILD_HTTPS_PROXY}"; \
    fi; \
    for i in 1 2 3 4 5; do \
      echo "== cloakbrowser install 尝试 ${i}/5 =="; \
      /opt/cloakbrowser-venv/bin/python -m cloakbrowser install && break || { sleep 5; }; \
    done; \
    CHROME_BIN=$(find /root/.cloakbrowser -name chrome -type f | head -1); \
    if [ -z "${CHROME_BIN}" ]; then \
      echo "ERROR: CloakBrowser chrome 二进制未安装成功，构建中止"; \
      exit 1; \
    fi; \
    echo "CloakBrowser chrome 就位: ${CHROME_BIN}"
# 补齐 Chromium 系统依赖（playwright 官方清单）
RUN /opt/cloakbrowser-venv/bin/python -m playwright install-deps chromium || true

# 服务二进制 / 入口脚本
COPY --from=builder /out/xh-grok-reg /usr/local/bin/xh-grok-reg
COPY docker/start.sh /usr/local/bin/start.sh
RUN chmod +x /usr/local/bin/start.sh

# 服务默认配置（compose / 环境变量可覆盖）
ENV ADDR=:9000 \
    GROK_TURNSTILE_PYTHON=/opt/cloakbrowser-venv/bin/python \
    GROK_TURNSTILE_SCRIPT=/usr/local/share/grok-reg/turnstile_mint.py \
    TURNSTILE_MODE=offscreen \
    GROK_CLEARANCE_DIR=/data/clearance \
    DISPLAY=:99

WORKDIR /data
EXPOSE 9000
# /data: grok-register.db + clearance；/root/.cache: rod 浏览器缓存（避免重建重复下载）
VOLUME ["/data", "/root/.cache"]
CMD ["/usr/local/bin/start.sh"]
