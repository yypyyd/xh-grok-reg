# mailverify

`mailverify` 是邮箱 OAuth 凭据的持久化后台认证调度器。它解决浏览器刷新或服务重启后批量认证中断、邮箱长期停留在“验证中”的问题。

## 核心职责

- 将 `mailboxes.status=unverified` 视为数据库中的待处理任务。
- 使用固定数量的 worker 并发认证，默认并发数为 10。
- 启动时把遗留的 `verifying` 恢复为 `unverified` 并继续处理。
- 支持重新认证指定邮箱、仅失败邮箱或全部邮箱。
- 防止同一邮箱在进程内被多个 worker 同时处理。

本模块不负责 HTTP 鉴权、邮箱 CRUD、Microsoft OAuth 协议细节或前端进度展示。

## 依赖关系

- 依赖 GORM 和 `models.Mailbox` 读写持久状态。
- 通过小型 `Verifier` 接口调用 `mailfetch.Client`。
- 由 `handlers.New` 创建并启动；邮箱导入和重新认证接口负责唤醒 worker。

## 快速使用

```go
mailClient := mailfetch.New()
service := mailverify.New(db, mailClient, mailverify.DefaultConcurrency)
if err := service.Start(); err != nil {
    return err
}
defer service.Stop()

service.Wake()                    // 新增 unverified 邮箱后唤醒
service.Reauthenticate([]uint{1}) // 重新认证指定邮箱
service.ReauthenticateFailed()    // 仅重新认证失败邮箱
service.ReauthenticateAll()       // 重新认证全部邮箱
```

## 文件结构

```text
mailverify/
├── service.go       # 调度、任务领取、worker 生命周期
├── service_test.go  # 恢复与重新认证测试
├── README.md
└── DESIGN.md
```

## 相关文档

- [设计文档](DESIGN.md)
