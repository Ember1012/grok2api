# Grok Web SSO Token 直输登录逻辑

## 结论

参考项目中所谓“直接输入 SSO 登录”，本质是管理端**导入已获取的 xAI `sso` Cookie/Token**。它不是将 xAI 用户名、密码或 MFA 交给服务端代登录。

该能力的合理定位是“受控凭据导入”：管理员粘贴短期或已有的 SSO 凭据，由服务端校验、脱敏持久化，并将其作为后续授权链路的输入。

## 数据流

```mermaid
flowchart LR
  A[管理员粘贴每行 SSO Token] --> B[前端仅在内存中创建临时 txt]
  B --> C[统一账号导入接口]
  C --> D[规范化、限长、去重]
  D --> E[Token 哈希生成账号身份]
  E --> F[加密保存凭据]
  F --> G[SSO 账号可用]
  G --> H[可选：转换为 OAuth]
  H --> I[xAI Device Flow]
  I --> J[保存 access / refresh token]
```

## 输入边界

- 支持纯文本：每行一个 SSO Token。
- 兼容 `sso=<token>` 形式；若输入的是完整 Cookie 串，只取 `sso` 的值，忽略其余 Cookie 字段。
- 可支持 JSON 批量导入，单项字段使用 `sso_token` 或 `token`。
- 规范化前后均不应把原 token 写入日志、错误信息或响应。
- 忽略空行；按规范化后的 token 去重。
- 需要限制单 token 大小和单次导入总数，避免异常请求占用内存或存储。

## 凭据生命周期

1. **导入**：前端不新增敏感 JSON API，而是把粘贴文本临时封装成文本文件，复用既有 multipart 导入边界。
2. **身份**：以 `sha256(token)` 生成稳定 `source_key`；展示名称只能使用哈希短前缀，不使用 token 原文。
3. **持久化**：原 token 必须通过项目既有加密机制保存；数据库、审计、任务进度和接口返回均不得包含原 token。
4. **使用**：仅在 Grok 上游请求边界，将 token 注入对应 Cookie 或授权请求。
5. **失效**：认证失败属于不可重试的凭据失效，应标记账号不健康并要求重新导入；网络、代理和上游临时故障只应使账号暂时不可调度。

## SSO 转 OAuth 的参考流程

参考项目支持把已导入的 Web SSO 凭据转换为 Build OAuth 凭据，流程为：

1. 使用 SSO Cookie 访问 xAI 账号页，先确认该 SSO 凭据仍然有效。
2. 启动 xAI OAuth Device Flow，取得 `device_code`、`user_code`、验证地址、轮询间隔和有效期。
3. 在已认证的 SSO 会话中完成 Device Flow 的验证与授权确认。
4. 按服务端给出的间隔轮询 token endpoint，成功后得到 `access_token`、可选 `refresh_token`、`id_token` 和过期时间。
5. 从声明中提取可选账号元数据；持久化长期 OAuth 凭据，并保留 Web SSO 与 OAuth 账号的关联关系。

当前项目若实现这条能力，应收敛到唯一 `GrokProvider` 内部的 `oauth`、`token` 和 `errors` 职责中。不要照搬参考项目的 Web / Build / Console 多 provider 运行时结构。

## 安全约束

- 不接受账号密码、MFA 验证码或浏览器完整会话导出作为“直输登录”输入。
- 不在前端状态、浏览器存储、日志、审计记录、错误消息、SSE 进度或 API 响应中暴露 token、Cookie 或 Authorization 值。
- 上游 OAuth 地址必须使用 HTTPS，且仅允许 `x.ai` 与其子域名；拒绝带 userinfo 的 URL、非 HTTPS URL 与跳转到非 allowlist 域名的重定向。
- 需要限制重定向次数、响应体大小、整个转换流程的超时与 Device Flow 轮询窗口。
- 获取 OAuth token 后，refresh token 未轮换时必须保留旧值；永久刷新失败与临时上游错误必须有不同的账号状态处理。

## 异常语义

| 场景 | 处理原则 |
|---|---|
| 空输入、格式错误、超长 token | 拒绝导入，返回不含原 token 的输入错误。 |
| 重复 token | 幂等跳过或更新已有账号，不创建重复凭据。 |
| SSO 已失效 | 标记凭据需要重新认证，不将其伪装为网络错误。 |
| Device Flow 被拒绝或过期 | 停止轮询，返回授权失败。 |
| 网络、代理、xAI 临时错误 | 保持凭据不变，仅临时不可调度，并按策略重试。 |
| 非受信重定向或 URL | 立即拒绝，避免 Cookie/token 外泄。 |

## 参考证据

- 粘贴文本并封装临时文件：`参考项目/grok2api-main/grok2api-main/frontend/src/features/accounts/accounts-page.tsx`
- 统一导入调用：`参考项目/grok2api-main/grok2api-main/frontend/src/features/accounts/accounts-api.ts`
- SSO token 解析、清理、去重与哈希身份：`参考项目/grok2api-main/grok2api-main/backend/internal/infra/provider/web/import.go`
- SSO 驱动的 Device Flow 转换及 URL 安全校验：`参考项目/grok2api-main/grok2api-main/backend/internal/infra/provider/web/sso_build.go`
- Token 清理和可信重定向的测试：`参考项目/grok2api-main/grok2api-main/backend/internal/infra/provider/web/sso_build_test.go`

> 本文仅整理参考行为和安全边界。参考项目是只读设计依据；当前项目若实现，应重新实现，不可导入、依赖或复制其源码。