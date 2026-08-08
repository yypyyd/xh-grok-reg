# 设计说明

## 系统边界

`xh-grok-reg` 是单实例 Go 服务：Gin 提供鉴权 API 和嵌入式前端，GORM/SQLite 保存 Grok 账号、邮箱和设置，后台生产器负责浏览器注册与验证码取件。前端是独立的 React/Vite 工程，构建后输出到 `static/`。

## 平台与运行时边界

- **Windows 不支持 Grok 自动注册**。Windows 仅作为前端开发、管理台/API、邮箱池认证、代理管理和已部署账号测活/导出的开发与运维平台；`start.ps1` 不会安装也不检查 Linux Turnstile 运行时。
- Linux（推荐 Ubuntu/Debian x86_64）是完整 Grok 自动注册的目标平台。注册链路依赖 `/opt/cloakbrowser-venv/bin/python`、CloakBrowser Chromium 和 `scripts/turnstile_mint.py`；可选的并发池使用 `scripts/turnstile_pool.py`。
- 即使在 Windows 上手动安装同名 Python/CloakBrowser 路径，也不属于项目的兼容性保证范围；生产注册应将服务部署到 Linux，通过浏览器访问 Linux 实例的管理台。
- Go 代码通过 `GROK_TURNSTILE_PYTHON`、`GROK_TURNSTILE_SCRIPT` 和 `TURNSTILE_MODE` 定位、配置外部 helper。`turnstile_pool.py` 仅作为可选的外部调度脚本，不会被 Go 服务自动调用。helper 不嵌入 Go 二进制，也不把浏览器缓存、Python 虚拟环境或凭据提交到仓库。
- helper 或 Python 路径缺失时，注册任务应明确失败并写入日志；邮箱池、账号列表和其他公共 API 不受该前置依赖影响。
- 可选的 clearance 兜底依赖用户自行提供 Docker/FlareSolverr/WARP/Privoxy compose 栈，并通过 `GROK_CLEARANCE_DIR` 指定目录；默认协议路径不要求这套第三方容器。

## 分层

- `internal/grokreg`：浏览器注册、Turnstile、协议注册、清除验证和代理会话。
- `internal/grokproducer`：单任务/批量任务、并发控制、停止、日志和截图落库。
- `internal/livecheck`：Console DPoP 测活和 OAuth refresh 回退，区分 alive/dead/unknown。
- `internal/mailfetch`、`internal/mailverify`：Outlook Graph/IMAP、Gmail/IMAP 取件和后台凭据验证。
- `internal/handlers`：登录、邮箱、设置、代理和 Grok API。
- `internal/db`、`internal/models`：SQLite 初始化、迁移、孤儿任务回收和邮箱关联。
- `frontend`：全新浅色 SaaS 控制台，不复用源项目静态资源。

## 数据与任务

Grok 账号存储在 `grok_registrations`，邮箱存储在 `mailboxes`。启动时残留的 `registering`/`waiting_code` 账号会标为 `register_failed`。注册成功后会话数据仅通过导出接口返回，列表和日志接口不返回完整凭据。

批量生产从已验证邮箱中领取未使用地址；每个任务使用独立上下文和代理配置。测活只在用户主动触发时运行，`unknown` 不会覆盖为 `dead` 的业务语义。

## 前端边界

前端只依赖 `/api` 接口，统一处理 Bearer token、自动续期响应头、401 退出、加载/错误/空状态。生产构建使用 Vite 输出 `static/`，Go 服务通过 `go:embed` 提供 `/` 和静态资源。

## 非目标

本项目只提供 Grok 注册、测活、导出及公共管理能力；不提交数据库、可执行文件、浏览器缓存和凭据。

## 部署决策

Turnstile helper 保留为 `scripts/` 下的独立 Linux 部署资产，而不是在 Go 启动时动态下载或在 Windows 上静默回退。这样可以让管理台在 Windows 上正常开发和运维，同时避免把平台相关的浏览器二进制、Python 环境和敏感配置混入仓库。

## 变更历史

### 2026-08-08 - 发布前敏感信息与旧产品引用扫描

**变更内容**：移除启动脚本中的本地工作区专用路径，测试代理改为 example 域名，并扫描生产代码中的无关产品引用。

**变更理由**：开源仓库不应暴露本地开发环境路径、服务器部署信息或旧产品闭包。

**影响范围**：`start.ps1`、`internal/proxyutil/session_test.go` 和发布前扫描；运行时功能不变。

### 2026-08-08 - README 改为项目说明型布局

**变更内容**：README 按“核心能力、平台边界、注册流程、技术亮点、架构、快速开始、部署、功能、API、FAQ”的顺序重排，采用开源项目常见的表格和代码块风格。

**变更理由**：让首次访问仓库的用户先理解 Grok-only 定位和 Windows/Linux 支持差异，再按环境选择部署方式。

**影响范围**：仅文档结构和表达方式，代码、API 和数据格式不变。

### 2026-08-08 - 补齐开源部署边界与外部依赖说明

**变更内容**：补充全新部署所需的前端嵌入构建、Linux 专用用户、CloakBrowser 缓存权限、可选 clearance/Docker 栈和仓库排除项说明。

**变更理由**：源码、helper 和 Go 依赖虽然已迁移，但 Python/Chromium、数据库、凭据及第三方 clearance 容器不应伪装成仓库内置内容。

**影响范围**：README、DESIGN、`.env.example` 和 Linux 部署流程；业务 API 不变。

### 2026-08-08 - Grok 列表状态自动刷新

**变更内容**：Grok 账号页在页面可见时每 2.5 秒重新读取账号列表，同时轮询生产进度；后台注册完成后，列表会自动从“注册中/待验证码”更新为“已注册”，并保留当前勾选项。

**变更理由**：原页面只刷新生产进度，没有刷新分页列表，导致数据库状态已变更但用户仍看到旧状态。

**影响范围**：`frontend/src/GrokPage.jsx`；后端状态接口和数据格式不变。

### 2026-08-08 - 明确 Windows/Linux 平台边界

**变更内容**：README、DESIGN 和 `.env.example` 明确 Windows 仅支持开发、管理台/API、邮箱池和公共运维；Grok 自动注册固定以 Linux + CloakBrowser/Chromium/Turnstile helper 为支持目标，并补充 Linux systemd 部署、跨平台工作流和常见错误排查。

**变更理由**：Windows 缺少项目注册链路依赖的 Linux 浏览器运行时，继续把 Windows 启动成功描述成“支持注册”会导致 `fork/exec ... no such file or directory` 等误判。

**影响范围**：文档、示例配置和部署流程；Go API 与核心注册逻辑不变。

### 2026-08-08 - 邮箱认证失败原因可见

**变更内容**：邮箱认证 worker 将安全的失败原因写入 `mailboxes.verify_error`，列表直接展示最近一次失败原因；重新认证时清空旧原因。

**变更理由**：仅显示“验证失败”无法判断是缺少 `client_id`、refresh token 失效、IMAP 权限问题还是邮箱服务临时不可用。

**影响范围**：`internal/models`、`internal/mailverify`、`internal/mailfetch`、邮箱页状态展示。

### 2026-08-08 - 完整迁移邮箱池工作流

**变更内容**：前端邮箱页接回源项目已有的分页、搜索、状态筛选、跨页勾选、所选/失败/全部重新认证、批量删除、全部删除、完整凭据编辑、注册用量展示，以及已验证邮箱的收件箱轮询、邮件合并和正文缓存。

**变更理由**：新 UI 不复用源项目视觉布局，但必须保留邮箱池的业务语义和后台认证队列交互，避免只迁移按钮而丢失实际运维能力。

**影响范围**：`frontend/src/MailboxPage.jsx`、邮箱页样式、邮箱 API 的全部前端调用；Go API 契约保持不变。
