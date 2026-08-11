# Docker 一键部署（完整版）

支持 **Grok 自动注册** 的完整容器化方案。镜像内含 CloakBrowser、Chromium、Xvfb 与 Turnstile helper，启动即用。

## 1. 前置要求

- Docker Engine 20.10+（含 BuildKit，Docker Desktop / Linux 均可）
- 服务器能访问 `x.ai`、邮箱服务（Outlook Graph / Gmail / IMAP）及代理出口
- 建议 ≥ 4 核 / 4GB 内存 / 20GB 磁盘（完整镜像 + CloakBrowser 体积较大）

## 2. 构建并启动

```bash
# 在项目根目录（含 Dockerfile 处）
docker compose up -d --build
```

> **国内网络必读**：构建需从 GitHub 下载 CloakBrowser（~206MB），直连常中断。
> 先设置加速源（任选可达者，实测 gh.ddlc.top / ghfast.top / ghproxy.net 可用）：
>
> ```bash
> # PowerShell
> $env:CLOAKBROWSER_DOWNLOAD_URL = "https://gh.ddlc.top/https://github.com/CloakHQ/cloakbrowser/releases/download"
> docker compose up -d --build
> ```
>
> 或与 `docker-compose.yml` 同目录建 `.env` 文件：
> ```
> CLOAKBROWSER_DOWNLOAD_URL=https://gh.ddlc.top/https://github.com/CloakHQ/cloakbrowser/releases/download
> ```
> 也可用代理：`BUILD_HTTPS_PROXY=http://代理:端口`。

首次构建较慢（需下载 CloakBrowser + Chromium 运行库，约 5-15 分钟）。之后启动秒级。

## 3. 访问

| 项 | 值 |
| --- | --- |
| 地址 | `http://localhost:9000/`（或服务器 IP:9000） |
| 默认账号 | `admin / admin123` |
| 健康检查 | `curl http://localhost:9000/healthz` → `{"ok":true}` |

> ⚠️ 首次登录后**立即修改默认密码**。生产环境请加 HTTPS 反向代理与访问控制，勿将管理台直接暴露公网。

## 4. 数据持久化

| Volume | 内容 |
| --- | --- |
| `grok-data` | `grok-register.db`（账号/邮箱/代理/任务）、`clearance` 目录 |
| `grok-cache` | rod 浏览器缓存（`browser` / `browser-grok` 两个 Chromium），避免重建容器重复下载 |

`docker compose down` 不会删数据；`docker compose down -v` 会清空。

## 5. 环境变量

| 变量 | 默认值 | 说明 |
| --- | --- | --- |
| `ADDR` | `:9000` | HTTP 监听地址 |
| `TURNSTILE_MODE` | `offscreen` | 有 Xvfb 时 headed 移屏；无显示环境自动降级 headless |
| `GROK_TURNSTILE_PYTHON` | `/opt/cloakbrowser-venv/bin/python` | helper 解释器 |
| `GROK_TURNSTILE_SCRIPT` | `/usr/local/share/grok-reg/turnstile_mint.py` | 单次签发脚本 |
| `GROK_CLEARANCE_DIR` | `/data/clearance` | clearance 编排目录（可选） |

修改方式：编辑 `docker-compose.yml` 的 `environment` 后 `docker compose up -d` 重建。

## 6. 运维命令

```bash
docker compose logs -f xh-grok-reg   # 实时日志
docker compose restart xh-grok-reg   # 重启
docker compose down                  # 停止（保留数据）
docker compose down -v               # 停止并清空数据卷（慎用）
docker compose pull                  # 使用已推送镜像时拉取
```

## 7. 常见问题

| 现象 | 处理 |
| --- | --- |
| 首次启动仪表盘显示"浏览器下载中" | rod 在下载 Chromium 到 `/root/.cache/rod`，耐心等待，进度会实时显示 |
| 注册任务失败、Turnstile 签发报错 | `docker compose logs` 查看 stderr 尾部；确认容器能出网访问 `x.ai` |
| 邮箱/代理连不上 | 检查宿主机网络与防火墙；如出口 IP 需与宿主机一致，将 compose 中 `network_mode` 改为 `host` |
| Chrome 崩溃 | 确认 compose 已配 `shm_size: "2gb"`（/dev/shm 过小导致） |
| 镜像很大 | 完整版需要 CloakBrowser（约 +1.5GB），属正常；仅需管理台/邮箱可另出瘦身版 |

## 8. 验证清单（部署后）

- [ ] `curl http://localhost:9000/healthz` 返回 `{"ok":true}`
- [ ] 浏览器打开 `http://localhost:9000/`，能登录管理台
- [ ] 管理台「浏览器状态」显示已就绪（rod Chromium 下载完成）
- [ ] 添加一个邮箱并验证，确认邮箱池可用
- [ ] 发起一次 Grok 注册，确认 Turnstile helper 正常签发（首次会触发 CloakBrowser 启动）
