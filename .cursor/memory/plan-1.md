---
name: 切换 Grok 授权
overview: 把“添加账号”中的 ChatGPT/Codex OAuth 垂直替换为 Grok/xAI OAuth，并同步接通凭证落库、刷新和运行时账号状态，避免只换界面后账号无法使用。保留当前账号管理框架，但隔离并停止从该入口暴露 OpenAI 专属授权语义。
todos:
  - id: grok-oauth-core
    content: 建立 xAI OAuth 配置、PKCE 会话、code 兑换与 token 刷新模块
    status: pending
  - id: grok-account-persistence
    content: 改造管理路由、Grok 账号凭证落库与平台去重语义
    status: pending
  - id: grok-account-ui
    content: 将添加账号和重新授权界面完整切换为 Grok 流程
    status: pending
  - id: grok-runtime
    content: 按平台接通 Grok token 刷新、请求上游和运行时状态
    status: pending
  - id: grok-verification
    content: 补充授权、持久化、前端与端到端回归验证
    status: pending
isProject: false
---

# Grok OAuth 授权切换

## 目标架构

```mermaid
flowchart LR
  UI[添加/重新授权 Grok 账号] --> Session[PKCE 会话]
  Session --> XAI[auth.x.ai 授权]
  XAI --> Paste[粘贴 callback URL]
  Paste --> Exchange[校验 state 并兑换 token]
  Exchange --> Account[Grok OAuth 账号凭证]
  Account --> Refresh[到期自动刷新]
  Refresh --> Runtime[运行时可用性与错误状态]
```

## 实施步骤

1. **建立独立的 xAI OAuth 边界**
   - 参考 [参考项目/sub2api-main/backend/internal/pkg/xai/oauth.go](参考项目/sub2api-main/backend/internal/pkg/xai/oauth.go)、[grok_oauth_service.go](参考项目/sub2api-main/backend/internal/service/grok_oauth_service.go) 和 [grok_oauth_client.go](参考项目/sub2api-main/backend/internal/repository/grok_oauth_client.go)，在当前项目中抽出 Grok 授权配置与协议层，而不是继续扩展 OpenAI 常量。
   - 使用 `https://auth.x.ai/oauth2/authorize`、`https://auth.x.ai/oauth2/token`、PKCE、`openid profile email offline_access grok-cli:access api:access` 和 `http://127.0.0.1:56121/callback`；允许通过环境变量覆盖，并限制可接受的 xAI 上游地址。
   - 会话保存 `state / code_verifier / redirect_uri / proxy_url / client_id`，30 分钟失效；兑换时接受完整 callback URL，并严格校验 `state`。

2. **替换管理 API 与账号写入语义**
   - 将 [admin/oauth.go](admin/oauth.go) 从 OpenAI/Codex 兑换逻辑改为 Grok 授权编排，并在 [admin/handler.go](admin/handler.go) 保持清晰的 Grok OAuth 路由职责：生成授权链接、兑换并创建账号、重新授权已有账号、刷新账号凭证。
   - Grok 账号统一写入 `platform=grok`、`type=oauth`；凭证结构收敛为 `access_token / refresh_token / id_token / token_type / expires_at / client_id / scope / email / base_url`，其中 `base_url` 默认 `https://api.x.ai/v1`。
   - 调整 [admin/token_credentials.go](admin/token_credentials.go)、[database/postgres.go](database/postgres.go) 与 [database/sqlite.go](database/sqlite.go)，去除 OAuth 写入时强制 `platform=openai` 的行为；Grok 去重优先使用稳定身份信息，不再套用 OpenAI 的 `account_id/user_id` 假设。

3. **把添加账号和重新授权界面切到 Grok**
   - 修改 [frontend/src/pages/Accounts.tsx](frontend/src/pages/Accounts.tsx)、[frontend/src/api.ts](frontend/src/api.ts)、[frontend/src/types.ts](frontend/src/types.ts) 及中英文文案：OAuth 标签、步骤说明、授权域名、成功提示全部改为 Grok/xAI。
   - 保留现有“两步式”体验：生成并打开授权链接 → 用户粘贴跳转失败页面的完整 URL → 系统解析 `code/state` 并创建账号；编辑账号时复用同一流程更新凭证。
   - 从“添加账号”主流程移除 `ChatGPT Session`、Codex 邀请等 OpenAI 专属入口；其他历史导入能力暂不物理删除，避免扩大本次变更和破坏旧数据迁移。

4. **接通刷新与运行时状态闭环**
   - 修改 [auth/token.go](auth/token.go) 与 [auth/store.go](auth/store.go)，按账号平台选择 xAI refresh 流程，正确处理 refresh token 轮换、缺失 `expires_in` 时的默认有效期，以及 401/403/429 的不同状态。
   - Grok 账号不再触发 ChatGPT `wham` 用量探针；将账号可用性先建立在 token 有效性和最小 xAI 请求探针上，避免 OpenAI 的 5h/7d 配额字段误判 Grok 账号。
   - 若当前代理请求仍指向 OpenAI/Codex 上游，则同步按 `platform=grok` 使用 `base_url + Bearer access_token`，确保新增账号不只是“能授权”，而是能进入实际请求链路。

5. **验证与回归**
   - 扩充 [admin/oauth_test.go](admin/oauth_test.go)：授权 URL/PKCE、完整 callback URL 解析、state 不匹配、会话过期、兑换成功、重新授权、refresh token 保留与轮换、403 entitlement 错误脱敏。
   - 覆盖 PostgreSQL/SQLite 的 Grok 账号写入和更新，确认不会被改写为 OpenAI；覆盖前端添加与重新授权状态切换。
   - 运行 Go 测试、前端类型检查/构建，并用可替换的 xAI 测试端点完成一次“生成链接 → 兑换 → 落库 → 刷新 → 运行时加载”的端到端验证，测试中不记录真实 token。