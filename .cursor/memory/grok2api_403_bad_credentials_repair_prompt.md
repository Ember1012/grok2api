# grok2api：Grok OAuth Access Token 失效 / 403 `bad-credentials` 完整维修提示词

> 用途：将本文件全文复制给 Cursor Agent / Codex / Claude Code，让其直接在当前 `Ember1012/grok2api` 工作区中完成排查、修复、测试与验证。  
> 目标错误：
>
> ```text
> HTTP 403
> {
>   "code": "unauthenticated:bad-credentials",
>   "error": "The OAuth2 access token could not be validated."
> }
> ```

---

## 一、你的角色

你现在是本项目的高级 Go 后端工程师、OAuth2 鉴权工程师和 API Gateway 维护者。

请直接在**当前打开的 `grok2api` 仓库工作区**中完成修复，不要只给建议，不要只写伪代码，不要让我手工拼接代码。

你的任务是：

1. 先完整审计当前分支真实源码；
2. 明确 `Access Token`、`Refresh Token`、`expires_at`、后台刷新、手动刷新、测试连接、主代理请求、SSE、WebSocket 的真实调用链；
3. 修复 xAI/Grok 上游返回特定 `403 unauthenticated:bad-credentials` 时未触发安全即时刷新与单次重试的问题；
4. 保留普通 `403` 的原有语义，**绝对不能把所有 403 都当成 Token 过期**；
5. 防止刷新风暴、无限重试、重复 RT 刷新、Refresh Token 轮换丢失、并发数据竞争；
6. 补齐单元测试、集成级测试或基于 `httptest` 的行为测试；
7. 最后运行完整测试并给出修改摘要。

---

# 二、问题背景

当前实际故障是：

```json
{
  "code": "unauthenticated:bad-credentials",
  "error": "The OAuth2 access token could not be validated."
}
```

HTTP 状态：

```text
403 Forbidden
```

这不是普通的套餐权限不足，也不是普通模型 entitlement 问题。

它的语义是：

```text
当前 Access Token 无法被 xAI OAuth2 鉴权系统验证
```

当前项目中已知存在 OAuth Refresh Token → Access Token 的刷新能力，因此本次维修目标不是重写 OAuth，而是把：

```text
上游明确报告 Access Token 无效
        ↓
安全识别
        ↓
同账号强制刷新一次
        ↓
持久化新 Token
        ↓
使用新 Access Token 重放原请求一次
```

正确接入到现有运行链路中。

---

# 三、当前源码基线：先核实，禁止盲改

在修改前，必须重新检查当前工作区，因为我本地分支可能与 GitHub `main` 不完全一致。

已知上游公开仓库当前版本中，至少存在以下相关结构，请以工作区真实代码为准：

## 1. `auth/token.go`

重点检查：

```text
RefreshAccessToken
TokenURL
RefreshScopes
MaxRetries
TokenData
ExpiresAt
RefreshToken 轮换处理
```

公开版本中可见 xAI OAuth Token Endpoint：

```text
https://auth.x.ai/oauth2/token
```

并使用类似 scope：

```text
openid profile email offline_access grok-cli:access api:access
```

**不要擅自修改 client_id、scope、token endpoint。**
除非当前仓库代码和现有 OAuth 流明确要求，否则本次修复不应改变 OAuth 协议参数。

---

## 2. `admin/test_connection.go`

重点检查：

```text
TestConnection
批量测试相关函数
ExecuteRequest
ExecuteOpenAIResponsesRequest
401 处理
429 处理
```

已知公开版本的单账号测试连接逻辑大致是：

```text
检查 Access Token 是否为空
        ↓
ExecuteRequest / ExecuteOpenAIResponsesRequest
        ↓
非 200 读取错误体
        ↓
switch status
    401 -> unauthorized
    429 -> rate limit
        ↓
其他错误直接返回
```

因此本次 `403 bad-credentials` 会落入“直接报错”，不会自动恢复。

---

## 3. `proxy/handler.go`

重点检查：

```text
classifyHTTPFailure
upstreamErrorKind
applyCooldownForModel
responseFailedStatusCode
classifyResponseFailedOutcome
responseFailedRetryable
SSE/stream transparent retry
WebSocket 失败与鉴权探针
所有 HTTP 非 2xx 分支
```

已知公开版本中：

```text
401 -> unauthorized
429 -> 单独处理
5xx -> server
其他 4xx -> client
```

同时 `response.failed` 的错误字符串映射中需要检查是否认识：

```text
unauthenticated:bad-credentials
```

不能只搜索：

```text
unauthorized
invalid_api_key
forbidden
```

因为：

```text
unauthenticated:bad-credentials
```

不等于：

```text
unauthorized
```

---

## 4. `proxy/executor.go`

重点检查：

```text
Authorization: Bearer <access_token>
请求构建
请求重建
payload 是否可重放
SSE 请求
Responses 请求
WebSocket 请求
```

确认 Token 是在每次重建请求时重新从账号对象读取，还是在调用前被复制到局部变量。

这是关键：

> 刷新成功后，重试请求必须真正使用新 Access Token，不能继续使用第一次失败时缓存下来的旧 Token。

---

## 5. Store / Account / Repository / Database 相关代码

不要假定函数名。

请全仓搜索：

```text
RefreshAccessToken(
refresh_token
RefreshToken
AccessToken
ExpiresAt
expires_at
StartBackground
background refresh
refresh account
manual refresh
force refresh
UpdateAccount
SaveAccount
Persist
MarkCooldown
MarkError
unauthorized
singleflight
mutex
sync.Map
```

必须找出：

1. 现有单账号强制刷新入口；
2. 现有后台定时刷新入口；
3. Token 更新数据库的唯一正确方式；
4. Token 更新内存运行时账号的正确方式；
5. Refresh Token 轮换时的保存逻辑；
6. 账号状态和 cooldown 的恢复逻辑。

**优先复用现有能力。不要平行再造第二套 Token 存储机制。**

---

# 四、参考 `sub2api` 的原则，但不要盲抄

参考项目：

```text
https://github.com/Wei-Shaw/sub2api
```

目标项目：

```text
https://github.com/Ember1012/grok2api
```

`sub2api` 当前 Grok/xAI OAuth 设计值得借鉴的原则：

```text
保存 access_token
保存 refresh_token
保存 token_type
保存 expires_at
支持 OAuth PKCE
支持 refresh-token 验证/刷新
支持单账号刷新
```

但要特别注意：

```text
sub2api 当前公开说明：
401 -> 需要重新授权
403 -> entitlement / subscription-tier
避免对所有 403 形成刷新循环
```

因此本项目修复不能简单写成：

```go
if resp.StatusCode == 403 {
    refresh()
}
```

这是明确禁止的。

本次要做的是：

```text
403
  +
错误体精确命中 bad credentials
  =
Access Token 鉴权失败
```

而普通 403 仍保留原处理。

---

# 五、核心维修目标

最终必须实现以下状态机：

```text
请求上游
   ↓
成功 2xx
   ↓
正常返回

请求上游
   ↓
401 / 403
   ↓
读取有限长度错误体
   ↓
是否精确匹配 xAI Access Token 无效？
   ├─ 否
   │   ↓
   │ 保留现有 401/403/entitlement/cooldown 逻辑
   │
   └─ 是
       ↓
账号是否有 Refresh Token？
       ├─ 否
       │   ↓
       │ 不刷新
       │ 标记为需要重新授权或按现有 AT-only 语义处理
       │ 返回明确错误
       │
       └─ 是
           ↓
同账号并发去重
           ↓
检查当前 AT 是否已被别的协程刷新
           ├─ 已变化
           │   ↓
           │ 直接使用最新 AT 重试
           │
           └─ 未变化
               ↓
使用 RT 强制刷新一次
               ↓
刷新成功？
               ├─ 否
               │   ↓
               │ 按刷新错误类型处理
               │ 不无限重试
               │
               └─ 是
                   ↓
原子更新内存 + 数据库
保存 RT 轮换
更新 expires_at
                   ↓
使用新 AT 重放原请求一次
                   ↓
成功？
   ├─ 是 -> 正常返回，不惩罚账号
   └─ 否
       ↓
       若仍为同类 bad-credentials
       -> 不再刷新第二次
       -> 标记需要重新授权/鉴权失效
       -> 进入现有失败处理
```

---

# 六、第一项修改：新增“精确的 xAI 鉴权失败分类器”

必须集中实现，不要在 5 个文件里复制字符串判断。

请选择一个不会造成循环依赖的位置，例如：

```text
internal/xaierror
internal/upstreamerror
auth/xai_error.go
proxy/xai_error.go
```

具体位置由你根据当前依赖图决定。

建议提供类似 API：

```go
type AuthFailureKind string

const (
    AuthFailureNone           AuthFailureKind = ""
    AuthFailureBadCredentials AuthFailureKind = "bad_credentials"
)

func ClassifyXAIAuthFailure(statusCode int, body []byte) AuthFailureKind

func IsXAIInvalidAccessToken(statusCode int, body []byte) bool
```

---

## 必须识别的真实错误

至少识别：

```json
{
  "code": "unauthenticated:bad-credentials",
  "error": "The OAuth2 access token could not be validated."
}
```

必须支持常见嵌套形式：

```json
{
  "error": {
    "code": "unauthenticated:bad-credentials",
    "message": "The OAuth2 access token could not be validated."
  }
}
```

也检查项目已有 `response.failed` 包装结构，例如：

```text
response.error.code
response.error.type
response.error.message

response.status_details.error.code
response.status_details.error.type
response.status_details.error.message

error.code
error.type
error.message

顶层 code
顶层 error（字符串）
顶层 message
```

---

## 推荐判定原则

### 精确命中 1

```text
normalized code == "unauthenticated:bad-credentials"
```

### 精确命中 2

错误文本包含：

```text
oauth2 access token could not be validated
```

大小写不敏感。

### 可选兼容

仅在当前真实上游样本或现有测试证明需要时，兼容：

```text
invalid access token
token could not be validated
```

但不要使用过宽判断。

---

## 明确禁止

不能因为以下条件就刷新：

```text
status == 403
status == 401
contains("forbidden")
contains("permission")
contains("entitlement")
contains("subscription")
contains("payment")
contains("model not allowed")
```

普通 403 可能表示：

```text
entitlement 不足
订阅等级不足
模型权限不足
套餐限制
账号策略限制
```

这些不能靠刷新 Token 修复。

---

# 七、第二项修改：实现“同账号安全强制刷新协调器”

必须复用现有 `RefreshAccessToken` 和现有数据库保存机制。

建议增加一个 Store 级能力，函数名按项目风格决定，例如：

```go
func (s *Store) RecoverAfterInvalidAccessToken(
    ctx context.Context,
    account *Account,
    failedAccessToken string,
) error
```

或：

```go
func (s *Store) RefreshAccountAfterAuthFailure(
    ctx context.Context,
    accountID int64,
    failedAccessToken string,
) error
```

---

## 必须满足 1：AT-only 账号不尝试刷新

如果账号：

```text
有 access_token
无 refresh_token
```

则：

```text
不能调用 token endpoint
不能伪造 refresh
不能无限重试
```

返回明确 typed error，例如：

```text
ErrRefreshTokenMissing
ErrReauthorizationRequired
```

并按当前项目已有账号状态语义处理。

---

## 必须满足 2：并发去重，防止 Refresh Storm

真实生产环境可能同时有 20 个请求命中同一失效 AT。

错误实现：

```text
20 个请求
  ↓
20 次 403
  ↓
20 次 RT refresh
```

这是禁止的。

必须实现：

```text
20 个请求
  ↓
同账号 singleflight / keyed mutex
  ↓
最多 1 次真正 RT refresh
  ↓
其他请求复用刷新结果
```

优先检查项目是否已有：

```text
singleflight.Group
per-account mutex
refresh lock
token refresh guard
sync.Map
```

有则复用。

没有再增加最小实现。

---

## 必须满足 3：锁内二次检查 AT 是否已变化

第一次失败时保存：

```text
failedAccessToken
```

进入刷新临界区后再次读取：

```text
currentAccessToken
```

如果：

```text
currentAccessToken != failedAccessToken
```

说明其他协程已经刷新成功。

此时：

```text
不要再次 refresh
直接返回成功
让调用方使用最新 AT 重试
```

这一步非常重要。

---

## 必须满足 4：正确处理 Refresh Token 轮换

刷新响应可能：

### 情况 A：返回新 RT

```json
{
  "access_token": "new_at",
  "refresh_token": "new_rt"
}
```

必须保存：

```text
new_at
new_rt
```

### 情况 B：不返回新 RT

```json
{
  "access_token": "new_at"
}
```

必须保留：

```text
old_rt
```

禁止把原 Refresh Token 更新为空。

---

## 必须满足 5：更新 expires_at

基于当前 `TokenData` 和现有项目逻辑更新：

```text
expires_at
```

不要自行创造第二种时间格式。

---

## 必须满足 6：数据库与内存运行时状态一致

必须找到项目现有正确更新路径。

修复后必须保证：

```text
数据库中的 access_token
内存 Account.AccessToken
数据库中的 refresh_token
内存 Account.RefreshToken
expires_at
```

一致。

优先使用已有 repository/store 方法。

禁止出现：

```text
只改内存，不改数据库
```

导致：

```text
重启后恢复旧 Token
```

也禁止：

```text
只改数据库，不改内存
```

导致：

```text
当前进程继续使用旧 Token
```

---

## 必须满足 7：日志绝不泄露 Token

禁止：

```go
log.Printf("access token=%s", accessToken)
log.Printf("refresh token=%s", refreshToken)
```

允许：

```text
account_id
db_id
masked email
refresh reason
status code
error kind
duration
```

如需关联 Token，只记录不可逆短哈希，例如：

```text
sha256(token) 前 8 位
```

但如果没有必要，连哈希也不要记。

---

# 八、第三项修改：修复单账号“测试连接”

目标文件优先检查：

```text
admin/test_connection.go
```

目标函数优先检查：

```text
TestConnection
```

当前错误场景：

```text
点击测试连接
  ↓
旧 AT 请求上游
  ↓
403 bad-credentials
  ↓
直接显示失败
```

修复为：

```text
点击测试连接
  ↓
请求上游
  ↓
403 bad-credentials
  ↓
若有 RT：
    强制刷新一次
    ↓
    使用新 AT 重试一次
  ↓
成功 -> 测试成功
失败 -> 返回最终错误
```

---

## 关键要求

### 1. 最多刷新一次

必须有请求级 guard，例如：

```text
refreshAttempted = true
```

绝对禁止：

```text
403 -> refresh -> 403 -> refresh -> 403 -> refresh
```

---

### 2. 刷新前不要先执行 24 小时 unauthorized cooldown

对于精确命中的：

```text
bad_credentials
```

处理顺序必须是：

```text
先尝试恢复
```

而不是：

```text
先 MarkCooldownWithError(24h)
再 refresh
```

否则可能刷新成功但账号仍被禁用。

---

### 3. 刷新成功后重建请求

不能复用包含旧 Header 的 `http.Request`。

必须重新调用现有 executor 或重新构建上游请求，让：

```http
Authorization: Bearer <new_access_token>
```

真正生效。

---

### 4. 正确关闭第一次失败响应体

读取错误体后：

```text
close body
```

避免连接泄漏。

如现有逻辑仍需后续读取，必须恢复 body：

```go
resp.Body = io.NopCloser(bytes.NewReader(body))
```

但优先设计清晰控制流，避免重复读取。

---

### 5. 错误体限制长度

不能：

```go
io.ReadAll(resp.Body)
```

无限读取未知上游错误体。

请复用项目已有大小限制；没有则增加合理上限，例如：

```text
64 KiB
256 KiB
1 MiB
```

结合项目已有 body limit 选择。

---

# 九、第四项修改：修复批量测试连接

不要只修单账号测试。

全仓搜索批量测试逻辑，例如：

```text
BatchTest
batch test
runBatch
test account
StatusUnauthorized
StatusTooManyRequests
```

批量测试也必须实现同样语义：

```text
第一次 bad-credentials
        ↓
同账号刷新一次
        ↓
同账号重试一次
        ↓
成功 -> success
仍失败 -> 最终失败
```

并且：

```text
普通 403
```

不触发 refresh。

---

# 十、第五项修改：修复真实主代理请求链路

这是最重要的部分。

只修后台“测试连接”不够。

必须覆盖真实 API：

```text
/v1/responses
/responses
/v1/chat/completions
/chat/completions
/v1/messages
其他实际共用 Grok executor 的入口
```

按当前真实路由和 handler 审计。

---

## A. 普通 HTTP / 非流式

流程：

```text
发送上游请求
  ↓
收到非 2xx
  ↓
读取有限错误体
  ↓
精确命中 bad_credentials？
  ├─ 否 -> 原逻辑
  └─ 是
       ↓
       refresh once
       ↓
       rebuild request
       ↓
       retry same account once
```

建议：

```text
优先刷新并重试原账号
```

因为这是同账号凭证恢复，不是普通“换号”。

如果第二次仍失败，再进入现有：

```text
换号
cooldown
scheduler penalty
最终错误
```

具体顺序要结合当前 handler 的 retry/scheduler 结构。

---

## B. SSE 流式请求

HTTP 连接建立后，在向下游发送任何响应头/正文前：

```text
如果上游直接返回 403 bad-credentials
```

可以：

```text
refresh once
rebuild request
retry once
```

必须保证：

```text
尚未向下游写出 body
```

一旦已向下游写出 SSE 数据：

```text
不能透明重放
```

否则会产生：

```text
重复 token
重复事件
损坏 SSE
```

---

## C. `response.failed` 事件

检查：

```text
classifyResponseFailedOutcome
responseFailedStatusCode
responseFailedRetryable
```

如果 xAI 在 HTTP 200 的 SSE 内返回：

```text
response.failed
```

且错误结构包含：

```text
unauthenticated:bad-credentials
```

必须至少做到正确分类。

透明 refresh + retry 只能在：

```text
尚未向下游写出任何正文
```

时进行。

如果已经写出：

```text
不要重放
```

只做：

```text
正确分类
账号状态处理
明确日志
```

---

## D. WebSocket

当前项目可能有：

```text
WS close
policy violation
auth probe
verifyAccountAuth
```

不要把所有 WebSocket 异常关闭都当 Token 失效。

仅当：

1. WebSocket 消息中有精确 bad-credentials；
2. 或当前已有鉴权探针明确返回同类错误；

才触发刷新。

如果无法安全重放 WebSocket 会话：

```text
不要强行透明重放
```

但应：

```text
刷新账号供后续请求使用
当前连接按现有协议失败
```

具体以当前 WS 架构为准。

---

# 十一、第六项修改：统一错误分类

当前项目中可能存在：

```text
classifyHTTPFailure
upstreamErrorKind
responseFailedStatusCode
classifyResponseFailedOutcome
applyCooldownForModel
```

需要让：

```text
403 + bad_credentials
```

被识别为：

```text
unauthorized
```

或新增更明确类型：

```text
bad_credentials
invalid_access_token
```

推荐内部类型：

```text
bad_credentials
```

对外/调度语义可映射为：

```text
unauthorized
```

但不要把普通 403 映射为 unauthorized。

---

## 例如

错误：

```json
{
  "code": "unauthenticated:bad-credentials",
  "error": "The OAuth2 access token could not be validated."
}
```

应该：

```text
failureKind = bad_credentials / unauthorized
refreshable = true（仅有 RT）
```

错误：

```json
{
  "error": {
    "code": "forbidden",
    "message": "subscription tier does not allow this model"
  }
}
```

应该：

```text
failureKind = entitlement / client / forbidden
refreshable = false
```

---

# 十二、第七项修改：刷新失败后的状态处理

必须区分至少三类。

---

## 1. Refresh Token 永久无效

例如 token endpoint 返回语义类似：

```text
invalid_grant
invalid refresh token
refresh token expired
revoked
```

处理：

```text
不继续 refresh
不继续重试原请求
标记需要重新授权
记录 sanitized error
```

优先复用项目现有：

```text
MarkError
MarkCooldownWithError
reauthorization
banned/risky
```

不要发明与调度器不兼容的新状态。

---

## 2. Refresh Token 请求临时失败

例如：

```text
timeout
connection reset
502
503
```

处理：

```text
按现有 refresh retry/backoff
```

若最终失败：

```text
不要误判为永久封禁
不要 24h unauthorized
```

除非当前已有明确策略。

---

## 3. Refresh 成功，但重试仍 bad-credentials

处理：

```text
不再第二次 refresh
标记需要重新授权
进入最终失败逻辑
```

这是防无限循环的硬要求。

---

# 十三、并发与数据竞争要求

必须运行：

```bash
go test -race ./...
```

至少保证新增代码无明显 race。

重点检查：

```text
Account.AccessToken
Account.RefreshToken
Account.ExpiresAt
Account.Status
Cooldown
并发刷新
后台刷新
请求触发刷新
管理员手动刷新
批量测试刷新
```

---

## 特别场景

后台定时刷新和请求触发刷新可能同时发生：

```text
后台线程
  ↓
Refresh RT

请求线程
  ↓
403 bad-credentials
  ↓
Refresh RT
```

最终必须：

```text
同账号只允许一个真正刷新动作
```

或至少：

```text
第二个动作在锁内发现 AT 已变化后跳过
```

否则如果 xAI 发生 RT rotation：

```text
第一次 refresh -> new_rt_1
第二次仍拿 old_rt -> invalid_grant
```

会把正常账号打坏。

---

# 十四、必须补齐的测试

不要只写 happy path。

优先使用：

```text
testing
httptest.Server
自定义 RoundTripper
mock token endpoint
mock xAI endpoint
```

根据项目现有测试风格实现。

---

## A. 分类器表驱动测试

### Case 1：真实错误

输入：

```text
status = 403
body = {
  "code":"unauthenticated:bad-credentials",
  "error":"The OAuth2 access token could not be validated."
}
```

期望：

```text
true
```

---

### Case 2：嵌套错误

```json
{
  "error": {
    "code": "unauthenticated:bad-credentials",
    "message": "The OAuth2 access token could not be validated."
  }
}
```

期望：

```text
true
```

---

### Case 3：普通 entitlement 403

```json
{
  "error": {
    "code": "forbidden",
    "message": "subscription tier does not allow this model"
  }
}
```

期望：

```text
false
```

---

### Case 4：普通 403 空 body

期望：

```text
false
```

---

### Case 5：429

期望：

```text
false
```

---

### Case 6：500

期望：

```text
false
```

---

## B. 单账号测试连接恢复

模拟：

```text
第一次上游请求
-> 403 bad-credentials

token refresh
-> 200 + new_at

第二次上游请求
-> 200
```

断言：

```text
refresh endpoint 调用 1 次
上游业务 endpoint 调用 2 次
第二次 Authorization 使用 new_at
最终测试成功
```

---

## C. 普通 403 不刷新

模拟：

```text
403 entitlement
```

断言：

```text
refresh endpoint 调用 0 次
```

---

## D. AT-only 账号

账号：

```text
access_token = old_at
refresh_token = ""
```

上游：

```text
403 bad-credentials
```

断言：

```text
refresh endpoint 调用 0 次
返回需要重新授权/缺少 RT
无无限重试
```

---

## E. Refresh Token rotation

token endpoint 返回：

```json
{
  "access_token": "new_at",
  "refresh_token": "new_rt",
  "expires_in": 3600
}
```

断言：

```text
内存 AT = new_at
DB AT = new_at
内存 RT = new_rt
DB RT = new_rt
expires_at 已更新
```

---

## F. Token endpoint 不返回新 RT

返回：

```json
{
  "access_token": "new_at",
  "expires_in": 3600
}
```

断言：

```text
旧 RT 被保留
绝不能变成空字符串
```

---

## G. 第二次仍 403

模拟：

```text
业务请求 1 -> 403 bad-credentials
refresh -> success
业务请求 2 -> 403 bad-credentials
```

断言：

```text
refresh 只调用 1 次
不存在第三次请求循环
最终进入失败/重新授权状态
```

---

## H. 并发 20 请求

模拟同账号：

```text
20 个并发请求同时收到 bad-credentials
```

断言：

```text
真正 token refresh 调用次数 = 1
```

其他请求：

```text
复用新 AT
或在锁内发现 AT 已变化
```

---

## I. 后台刷新与请求刷新并发

模拟：

```text
background refresh
request-triggered refresh
```

同时发生。

断言：

```text
无 RT rotation 覆盖
无空 RT
无旧 AT 回写覆盖新 AT
```

---

## J. 批量测试

至少验证：

```text
bad-credentials -> refresh once -> success
普通 403 -> no refresh
```

---

## K. SSE

验证：

```text
在下游尚未写 body 前：
403 bad-credentials -> refresh -> retry -> success
```

并验证：

```text
已经写出 SSE 后
禁止透明重放
```

---

# 十五、实现建议：请求级单次恢复 Guard

根据现有代码结构实现，不要求照抄。

例如：

```go
type authRecoveryState struct {
    attempted bool
}
```

或：

```go
for attempt := 0; attempt < maxAttempts; attempt++ {
    resp, err := execute()

    if err != nil {
        return err
    }

    if !IsXAIInvalidAccessToken(resp.StatusCode, body) {
        return handleNormally(resp)
    }

    if attempt > 0 {
        return handleFinalAuthFailure(resp)
    }

    err = store.RecoverAfterInvalidAccessToken(
        ctx,
        account,
        failedAccessToken,
    )
    if err != nil {
        return handleRefreshFailure(err)
    }

    // 下一轮必须 rebuild request
}
```

关键不是代码长什么样，而是：

```text
最多恢复一次
使用新 AT
不重放已输出流
```

---

# 十六、实现建议：防并发刷新

优先复用项目已有机制。

如果没有，可考虑：

```text
singleflight keyed by account ID
```

但还要做：

```text
failed AT 二次检查
```

伪代码：

```go
func (s *Store) RecoverAfterInvalidAccessToken(
    ctx context.Context,
    acc *Account,
    failedAT string,
) error {
    key := strconv.FormatInt(acc.ID(), 10)

    _, err, _ := s.refreshGroup.Do(key, func() (any, error) {
        currentAT := acc.GetAccessToken()

        if currentAT != "" && currentAT != failedAT {
            // 其他协程已刷新
            return nil, nil
        }

        rt := acc.GetRefreshToken()
        if rt == "" {
            return nil, ErrRefreshTokenMissing
        }

        // 必须调用项目现有 refresh 实现
        tokenData, accountInfo, err := RefreshAccessToken(...)
        if err != nil {
            return nil, err
        }

        // 必须走项目现有持久化/运行时更新逻辑
        if err := s.persistRefreshedCredentials(...); err != nil {
            return nil, err
        }

        return nil, nil
    })

    return err
}
```

注意：

```text
不要机械照抄此伪代码
```

必须适配当前仓库真实 API、锁、数据库和错误类型。

---

# 十七、数据库一致性要求

在写代码前检查项目支持：

```text
PostgreSQL + Redis
SQLite + Memory
```

修复不能只在一种模式工作。

测试至少确认：

```text
SQLite 代码路径能编译
PostgreSQL 代码路径能编译
```

如果 Store 抽象已统一：

```text
必须通过 Store/Repository 抽象更新
```

不要在业务代码里新写某一种数据库专用 SQL。

---

# 十八、部署兼容要求

不要破坏：

```text
docker-compose.yml
docker-compose.local.yml
docker-compose.sqlite.yml
docker-compose.sqlite.local.yml
go run .
```

除非测试证明需要，不要新增环境变量。

若确实新增配置，例如：

```text
auth_bad_credentials_refresh_enabled
auth_recovery_timeout_seconds
```

则必须：

1. 默认开启安全行为或给出合理默认值；
2. 更新 `.env.example`；
3. 更新 `.env.sqlite.example`；
4. 更新配置文档；
5. 保持旧部署不配新变量也能启动。

但本次优先：

```text
不新增配置
```

---

# 十九、安全要求

必须遵守：

1. 不在日志打印 AT；
2. 不在日志打印 RT；
3. 不在 API 错误返回 Token；
4. 不把 OAuth 上游完整敏感响应直接暴露给普通 API 用户；
5. 管理后台可显示 sanitized 错误；
6. body 读取必须有限长；
7. refresh 必须有 timeout；
8. 不允许无限 retry；
9. 不允许所有 403 自动 refresh；
10. 不允许 refresh 失败后吞错并继续拿旧 AT 请求。

---

# 二十、推荐修改范围

请先审计，再决定真实文件。

高概率涉及：

```text
auth/token.go
auth/store.go 或其他 Store 拆分文件
admin/test_connection.go
proxy/handler.go
proxy/executor.go（仅当需要确保重建请求读取新 AT）
相关 *_test.go
```

也可能存在：

```text
auth/refresh*.go
proxy/error*.go
internal/*
database/*
```

请基于当前分支实际结构修改。

---

# 二十一、不要做的事情

明确禁止：

```text
1. if status == 403 { refresh() }

2. 所有 401/403 无脑 refresh

3. refresh 失败后继续无限请求

4. 只修 UI

5. 只修 TestConnection，不修真实 API 链路

6. 只改内存 Token

7. 只改数据库 Token

8. 新 RT 返回后仍保存旧 RT

9. 新 RT 缺失时把旧 RT 清空

10. 多协程并发刷新同一个 RT

11. 复用带旧 Authorization Header 的 request

12. 已经向下游输出 SSE 后透明重放

13. 打印完整 Access Token / Refresh Token

14. 为了修复本问题顺手大规模重构整个项目

15. 擅自更改 xAI OAuth client_id / scopes / endpoint
```

---

# 二十二、执行顺序

请严格按以下顺序执行。

## Phase 1：源码审计

先输出简短审计结果：

```text
A. 当前 checkout commit
B. 当前 Token refresh 函数
C. 当前单账号刷新入口
D. 当前后台刷新入口
E. 当前数据库持久化入口
F. TestConnection 错误分支
G. Batch Test 错误分支
H. 主代理非 2xx 分支
I. SSE response.failed 分支
J. WebSocket auth failure 分支
K. 是否已有 singleflight / refresh lock
```

不要停在审计阶段。

---

## Phase 2：给出最小维修计划

列出：

```text
准备修改哪些文件
每个文件修改什么
为什么不会把普通 403 误判
如何防刷新风暴
如何保证最多重试一次
```

不要等我确认，继续执行。

---

## Phase 3：直接修改代码

完成真实代码。

要求：

```text
gofmt
无未使用 import
无伪代码
无 TODO 代替实现
```

---

## Phase 4：增加测试

至少覆盖本文件“必须补齐的测试”中的核心项。

优先保证：

```text
classifier
single test
ordinary 403 no refresh
AT-only
RT rotation
retry once
parallel refresh dedupe
main proxy
batch test
```

---

## Phase 5：运行验证

至少运行：

```bash
go test ./...
```

然后：

```bash
go test -race ./...
```

如果项目过大导致 race 测试耗时过长，至少对修改包执行：

```bash
go test -race ./auth ./admin ./proxy/...
```

具体包路径按真实项目。

如果前端未改：

```text
无需为了本次后端修复强制重构 UI
```

如果改了前端错误展示，再运行：

```bash
cd frontend
npm test
npm run build
```

按项目 package manager 真实配置执行。

---

## Phase 6：最终报告

最后输出：

### 1. 根因

明确说明为什么：

```text
403 bad-credentials
```

以前没有恢复。

### 2. 修改文件

逐文件列出。

### 3. 新状态机

说明：

```text
bad-credentials -> refresh once -> retry once
```

### 4. 普通 403

证明：

```text
不会 refresh
```

### 5. 并发保护

说明：

```text
20 并发只刷新一次
```

### 6. Token 轮换

说明：

```text
new RT 保存
无 new RT 保留 old RT
```

### 7. 测试结果

给出真实命令和结果。

### 8. 未解决风险

如有，诚实列出。

---

# 二十三、验收标准

只有同时满足以下条件才算完成。

- [ ] 精确识别 `unauthenticated:bad-credentials`
- [ ] 精确识别 `The OAuth2 access token could not be validated`
- [ ] 普通 403 不刷新
- [ ] AT-only 账号不调用 refresh endpoint
- [ ] 有 RT 的账号可即时刷新
- [ ] 刷新后使用新 AT
- [ ] 最多刷新一次
- [ ] 最多重试原请求一次
- [ ] 无无限循环
- [ ] 并发请求不会形成 refresh storm
- [ ] 后台刷新与请求刷新不会互相覆盖
- [ ] 新 RT 正确保存
- [ ] 无新 RT 时旧 RT 保留
- [ ] expires_at 正确更新
- [ ] 内存与数据库一致
- [ ] 单账号测试连接已修
- [ ] 批量测试已修
- [ ] 真实代理 API 已修
- [ ] SSE 不会在已输出后危险重放
- [ ] WebSocket 不会把任意断连误判为 Token 失效
- [ ] 日志不泄露 Token
- [ ] `go test ./...` 通过
- [ ] race 测试通过或至少修改包通过
- [ ] 给出完整修改摘要

---

# 二十四、建议的人工回归测试

代码测试通过后，再做真实环境验证。

## 测试 1：正常账号

```text
测试连接
```

期望：

```text
成功
无 refresh
```

---

## 测试 2：人为放入失效 AT，但保留有效 RT

期望：

```text
第一次业务请求 -> 上游 bad-credentials
后台 -> refresh
第二次业务请求 -> 成功
前端最终显示成功
```

日志应类似：

```text
xai auth failure detected
account=123
kind=bad_credentials
refresh_started
refresh_succeeded
request_retried
retry_succeeded
```

不能出现 Token。

---

## 测试 3：普通 403 entitlement

期望：

```text
不 refresh
按普通 403 处理
```

---

## 测试 4：无 RT

期望：

```text
不 refresh
提示需要重新授权
```

---

## 测试 5：RT 也失效

期望：

```text
refresh 一次
失败
账号进入需要重新授权状态
无循环
```

---

## 测试 6：20 并发

期望：

```text
只有 1 次实际 refresh
```

---

# 二十五、最终提醒

本次修复的核心不是：

```text
“403 就刷新”
```

而是：

```text
“只有明确证明 Access Token 无法验证的 403，才触发一次安全恢复”
```

最终正确逻辑必须是：

```text
403
  ↓
解析 body
  ↓
code == unauthenticated:bad-credentials
或 message == OAuth2 access token could not be validated
  ↓
有 RT？
  ├─ 否 -> 重新授权
  └─ 是
       ↓
同账号并发去重
       ↓
检查 AT 是否已被其他协程更新
       ↓
强制 refresh 一次
       ↓
原子保存 AT / RT / expires_at
       ↓
重建请求
       ↓
重试一次
       ↓
仍失败则停止，不再刷新
```

请现在开始：

```text
先审计当前工作区
然后直接完成修改、测试和最终报告
```

不要只回答方案。
