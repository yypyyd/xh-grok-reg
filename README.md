# xh-grok-reg

> **Grok 账号注册与邮箱池管理台** · Linux 自动注册 · Turnstile 签发 · 邮箱验证码自动取件 · React/Vite SaaS 控制台

---

👥 **QQ 交流群：** [点击加入群聊【XH AI交流群】](https://qun.qq.com/universal-share/share?ac=1&authKey=gwCDI5eBBiWEE2pJPUjp1BTdgOmxHa4fLfQ2PyKe2ElLvqMqREXfE2B4g%2Fwbl6hU&busi_data=eyJncm91cENvZGUiOiIxMTA1NDUyNDg0IiwidG9rZW4iOiJyTm41VUJlUDBuWUJlY09mSjA2dWd4RS9hTStMOG5HdU9aVG5tclRUWUlsclNPRzVmOEVicVpvOFFwK3NkWG14IiwidWluIjoiMjU3MzMwMjQ5NiJ9&data=jvTP5SQMRcBfkUVI34FgGfweIgFMj3xbYeT2ccCsAD9CjIdN5sdNIFgNwbHgDnPkcKW38enqkbOJRrtLtctQ6w&svctype=4&tempid=h5_group_info)

---

## Important

本项目仅用于自动化流程研究、测试环境验证和个人学习。使用者应自行遵守目标网站服务条款、当地法律法规和第三方服务限制。请勿将本项目用于滥用、绕过平台限制或未经授权的商业用途。

## ✨ 核心能力

| 🚀 Grok 自动注册 | 📬 邮箱池自动取件 | 📊 可视化管理台 |
|:---:|:---:|:---:|
| 支持单个注册、批量生产、停止任务和验证码提交 | Outlook/Gmail/IMAP 邮箱认证、批量导入和验证码轮询 | 总览、账号、邮箱、代理和系统设置集中管理 |

| 🛡️ Turnstile 支持 | 🔁 测活与额度 | 📦 两种格式导出 |
|:---:|:---:|:---:|
| Linux CloakBrowser helper 签发注册所需令牌 | Console 测活、OAuth refresh 回退和额度写回 | Console 与 Sub2API 凭据导出 |

> 项目只提供 Grok 注册和公共管理能力，并使用独立设计的 React/Vite 管理台。

## ⚠️ 平台边界（部署前必读）

| 运行环境 | 前端开发 | 管理台/API | 邮箱池、代理、已注册账号 | Grok 自动注册 |
| --- | --- | --- | --- | --- |
| Windows 10/11 | ✅ | ✅ | ✅ | **❌ 不支持** |
| Linux（推荐 Ubuntu/Debian x86_64） | ✅ | ✅ | ✅ | **✅ 支持，需安装外部运行时** |
| macOS/其他系统 | 未做发布验证 | 未做发布验证 | 未做发布验证 | 不在支持范围内 |

Windows 可以启动管理台、API、邮箱池、代理测试以及已存在账号的管理/测活/导出；但 Grok 自动注册需要 Linux 上的 CloakBrowser、Chromium 和 Turnstile helper。即使手动在 Windows 配置同名路径，也不属于项目的兼容性保证范围。

**要执行 Grok 注册，请把 Go 服务部署到 Linux，再用任意电脑浏览器访问 Linux 服务地址。** Windows 的 start.ps1 只用于开发和公共运维，不是完整注册环境。

## 🤖 Grok 注册流程

~~~
已验证邮箱进入生产队列
        ↓
Linux CloakBrowser/Chromium 签发 Turnstile 令牌
        ↓
Grok 协议客户端抓取注册配置并提交注册请求
        ↓
邮箱池轮询验证码（或人工提交验证码）
        ↓
注册成功 → 保存会话 → 状态更新为「已注册」
        ↓
按需测活、探测额度、导出 Console/Sub2API
~~~

### 关键技术点

| 特性 | 说明 |
|------|------|
| **协议优先** | 默认使用 HTTP/gRPC 协议注册，浏览器只负责签发 Turnstile 令牌；可切换旧的全浏览器流程 |
| **邮箱自动取件** | Outlook Graph、Gmail/IMAP 和通用 IMAP，后台认证队列会保存可读的失败原因 |
| **代理一致性** | Turnstile 签发、注册请求和账号网络会话使用同一代理出口 |
| **Chromium 管理** | Go 服务启动时检查 rod Chromium，缺失时显示下载进度并自动准备 |
| **失败存证** | 注册日志和失败截图落库，可从管理台实时查看 |
| **并发安全** | 每个注册任务独立上下文，生产器负责并发闸门、停止和孤儿任务回收 |
| **自动刷新** | Grok 列表和生产进度在页面可见时自动轮询，注册成功后状态自动更新 |

## 🏗️ 项目架构

~~~
xh-grok-reg/
├── main.go                    # Gin 入口、API 路由和 go:embed 静态服务
├── go.mod / go.sum            # Go 模块和依赖锁定
├── internal/
│   ├── auth/                  # JWT 管理员鉴权和改密
│   ├── browserboot/           # rod Chromium 检查、下载和状态
│   ├── db/                    # SQLite 初始化、迁移和任务恢复
│   ├── grokreg/               # Grok 浏览器/协议注册、Turnstile、代理桥
│   ├── grokproducer/          # 单个/批量生产、并发、停止、日志和截图
│   ├── livecheck/             # Grok 测活、OAuth refresh 和额度探测
│   ├── mailfetch/             # 邮箱验证码取件
│   ├── mailverify/            # 邮箱后台认证队列
│   ├── models/                # Grok、Mailbox、Setting、Admin 数据模型
│   └── handlers/              # 登录、Grok、邮箱、设置、代理 API
├── frontend/                  # React + Vite 前端源码
├── static/                    # Vite 构建产物（由 Go embed）
├── scripts/
│   ├── requirements-turnstile.txt
│   ├── turnstile_mint.py      # Linux 单次 Turnstile 签发 helper
│   └── turnstile_pool.py      # 可选的外部并发签发脚本
├── .env.example               # 配置示例（不含凭据）
└── start.ps1                  # Windows 开发/运维启动命令
~~~

**技术栈：** Go · Gin · GORM · SQLite · go-rod · JWT · React · Vite · Playwright/CloakBrowser（Linux helper）

## 🚀 快速开始

### Windows：开发和公共运维

需要 Go 1.25+、Node.js 18+ 以及 npm 或 pnpm：

~~~powershell
.\start.ps1
~~~

首次运行会安装前端依赖、构建 static/ 并启动 Go 服务，默认地址为 http://localhost:9000。只构建前端：

~~~powershell
.\start.ps1 -BuildOnly
~~~

Windows 不能执行完整 Grok 自动注册；注册任务请使用下面的 Linux 部署方式。

### 源码运行

~~~bash
go run .
~~~

服务默认监听 :9000，数据库为当前工作目录的 grok-register.db。如前端尚未构建，先执行：

~~~bash
cd frontend
npm install
npm run build
cd ..
~~~

### 自行编译

~~~bash
# Windows
go build -o xh-grok-reg.exe .

# Linux amd64
GOOS=linux GOARCH=amd64 go build -o xh-grok-reg .
~~~

go:embed 会在编译时嵌入根目录 static/，所以源码部署必须先构建前端，或使用发布包中与源码匹配的 static/。

## 🐧 Linux 完整部署（支持 Grok 自动注册）

以下命令以 Ubuntu/Debian 为例。服务器需要能访问 x.ai、邮箱服务和代理出口。

### 1. 安装系统依赖并构建

~~~bash
sudo apt-get update
sudo apt-get install -y ca-certificates curl git build-essential python3 python3-venv xvfb nodejs npm
sudo useradd --system --create-home --shell /usr/sbin/nologin grok || true

cd frontend
npm install
npm run build
cd ..
go build -o xh-grok-reg .
~~~

### 2. 安装 CloakBrowser、Chromium 和 helper

~~~bash
python3 -m venv /opt/cloakbrowser-venv
/opt/cloakbrowser-venv/bin/pip install -r scripts/requirements-turnstile.txt

# 浏览器缓存安装给 systemd 使用的 grok 用户
sudo -u grok HOME=/home/grok /opt/cloakbrowser-venv/bin/python -m cloakbrowser install
HOME=/root /opt/cloakbrowser-venv/bin/playwright install-deps chromium

sudo install -d /usr/local/share/grok-reg
sudo install -m 755 scripts/turnstile_mint.py /usr/local/share/grok-reg/turnstile_mint.py
sudo install -m 755 scripts/turnstile_pool.py /usr/local/share/grok-reg/turnstile_pool.py
sudo chown -R grok:grok /opt/Grok-Register
~~~

默认路径：

~~~
Python: /opt/cloakbrowser-venv/bin/python
Helper: /usr/local/share/grok-reg/turnstile_mint.py
~~~

也可以通过 GROK_TURNSTILE_PYTHON、GROK_TURNSTILE_SCRIPT 和 TURNSTILE_MODE 覆盖。

### 3. 使用 systemd 托管

创建 /etc/systemd/system/xh-grok-reg.service：

~~~ini
[Unit]
Description=xh-grok-reg Grok registration service
After=network-online.target
Wants=network-online.target

[Service]
Type=simple
User=grok
WorkingDirectory=/opt/Grok-Register
ExecStart=/opt/Grok-Register/xh-grok-reg
Restart=on-failure
RestartSec=5
Environment=ADDR=:9000
Environment=HOME=/home/grok
Environment=GROK_TURNSTILE_PYTHON=/opt/cloakbrowser-venv/bin/python
Environment=GROK_TURNSTILE_SCRIPT=/usr/local/share/grok-reg/turnstile_mint.py
Environment=TURNSTILE_MODE=offscreen

[Install]
WantedBy=multi-user.target
~~~

启用服务并查看日志：

~~~bash
sudo systemctl daemon-reload
sudo systemctl enable --now xh-grok-reg
systemctl status xh-grok-reg
journalctl -u xh-grok-reg -f
~~~

访问 http://服务器IP:9000/。生产环境建议配置 HTTPS 反向代理、防火墙和访问控制，不要直接把管理台暴露到公网。

## 🔐 登录与安全

- 默认账号：admin / admin123
- 首次登录后立即修改密码
- 所有受保护 API 使用 Authorization: Bearer <token>
- JWT 只保留当前有效 token，重新登录或改密会使旧 token 失效
- grok-register.db、邮箱密码、refresh token、代理凭据和截图不得提交到 Git

常用环境变量：

| 变量 | 作用 | 默认值 |
| --- | --- | --- |
| ADDR | HTTP 监听地址 | :9000 |
| GROK_TURNSTILE_PYTHON | Turnstile helper 的 Python | /opt/cloakbrowser-venv/bin/python |
| GROK_TURNSTILE_SCRIPT | Turnstile 单次签发脚本 | /usr/local/share/grok-reg/turnstile_mint.py |
| TURNSTILE_MODE | helper 浏览器模式 | offscreen |

scripts/turnstile_pool.py 是可选外部脚本，Go 服务不会自动调用它。

## 📋 功能说明

### Grok 账号

- 分页、搜索、状态筛选和批量勾选
- 单个注册、批量生产、停止任务和手动提交验证码
- 实时日志、失败截图、测活、Console 额度展示
- 单个/批量导出 Console 与 Sub2API

### 邮箱池

- 手动添加、批量导入、编辑、删除和批量删除
- Outlook Graph、Gmail/IMAP 和通用 IMAP
- 后台认证队列、重新认证、验证失败原因展示
- 已验证邮箱收件箱轮询、验证码提取和邮件正文查看

### 系统设置

- 并发数、全局代理开关和代理列表
- 代理连通性测试
- Chromium 无头模式和 Grok 注册引擎参数

## 📥 邮箱批量导入格式

进入「邮箱池」→「批量导入」，每行一条邮箱记录。具体字段按页面提示填写；常见格式为：

~~~
email----password----provider
~~~

Outlook/Gmail OAuth 邮箱还需要对应的 client_id 和 refresh_token。refresh token 失效或 scope 不足时，请在页面执行“重新认证”。不要把真实邮箱凭据粘贴到公开 issue。

## 🔌 API 概览

登录：POST /api/login

主要接口：

- /api/stats
- /api/grok/*
- /api/mailboxes/*
- /api/settings
- /api/proxy/test
- /api/browser/status

健康检查：GET /healthz。前端收到 401 会清除 token 并回到登录页，生产任务和测活任务通过状态接口轮询。

## ❓ 常见问题

**Q：Windows 能不能注册 Grok？**

**A：不能。** Windows 只支持开发、管理台/API、邮箱池和公共运维；完整注册请使用 Linux。

**Q：报 fork/exec /opt/cloakbrowser-venv/bin/python: no such file or directory？**

**A：**Linux Python 虚拟环境不存在或路径没配置。按上面的 CloakBrowser 安装步骤执行，或设置 GROK_TURNSTILE_PYTHON。如果是在 Windows 看到，这是平台限制，应迁移服务到 Linux。

**Q：报 turnstile_mint.py: no such file？**

**A：**helper 没有复制到共享目录，检查 GROK_TURNSTILE_SCRIPT 和文件权限。

**Q：报 OAuth scope unsupported by refresh token？**

**A：**邮箱 refresh token 已撤销、过期或不含当前取件 scope。重新认证邮箱，并确认 OAuth 应用的 client ID、secret 和 redirect URI 正确。

**Q：必须安装 FlareSolverr 吗？**

**A：不是。**默认协议路径不需要；只有日志提示 Cloudflare 拦截且你在设置中启用了 clearance 兜底时才需要自行准备 Docker 栈。

**Q：浏览器第一次启动很慢？**

**A：**首次会下载 Chromium。等待 /api/browser/status 变为 ready，并确保服务器网络和磁盘空间正常。

**Q：项目是否包含数据库和账号？**

**A：不包含。**数据库、邮箱凭据、代理和浏览器缓存都属于部署者自己的运行数据，并已在 .gitignore 中排除。

<details>
<summary>参考项目</summary>

感谢以下项目作者愿意开源相关工作，为本项目提供了宝贵的业务流程、工程结构和问题排查参考：

- [cyi-cc](https://github.com/cyi-cc/chatgpt-register)
- [AaronL725/grok-register](https://github.com/AaronL725/grok-register)

本项目在参考公开资料和实现的基础上，进行了 Grok-only 迁移、后端整理和全新 UI 重做，不复用源项目的旧版页面布局。使用或再分发涉及第三方代码的部分时，请同时遵守对应项目的许可证和版权声明。

</details>

## 📄 许可证与贡献

当前仓库未额外指定开源许可证。公开发布前请根据你的分发需求补充许可证、第三方依赖声明和安全披露渠道。
