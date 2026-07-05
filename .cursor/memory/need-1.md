---
name: 处理运行告警
overview: 当前页面能正常请求管理端接口，主要问题不是前端崩溃，而是后端账号用量探针持续返回 404。计划优先收敛 Grok quota probe 的运行时行为，避免无效探针反复打日志。
todos:
  - id: locate-usage-probe
    content: 定位 usageProbe 注册点和实际上游 endpoint，确认 404 来源
    status: pending
  - id: grok-quota-probe
    content: 将主动用量探针收敛为 Grok /responses 最小探针，并分类处理 404
    status: pending
  - id: plan-metadata
    content: 检查 plan_type 告警是否应迁移为 Grok subscription_tier / entitlement_status 语义
    status: pending
  - id: verify-runtime
    content: 运行服务并验证管理端接口正常、用量探针不再重复 404
    status: pending
isProject: false
---

# 处理当前运行告警

## 结论

- 需要处理：后端日志里的 `[账号 1] 用量探针失败: 探针返回状态 404`。
  - 这说明当前用量探针调用的上游路径或请求形态不被 Grok 接受。
  - 管理端接口本身是正常的，日志里 `/api/admin/*` 基本都是 `200`。
- 暂时不需要处理：浏览器控制台里的表单 `autocomplete/id/name` 之类黄色警告。
  - 这些是 Chrome 对表单可用性/自动填充的提示，不会导致当前页面功能失败。
  - 可后续顺手优化，但优先级低。
- 需要确认但不一定立刻修：`刷新后 plan_type 为空，无法识别套餐类型`。
  - 刷新后账号仍显示 `刷新成功`，说明 token 刷新链路可用。
  - 这更像套餐元数据识别不足，不是认证失败。

## 处理范围

- 重点检查并调整 [auth/store.go](e:/project/GitHub/Ember1012/grok2api/auth/store.go) 中用量探针触发与错误记录边界。
- 定位实际注册的 `usageProbe` 实现，检查它是否仍在使用旧 OpenAI/Codex 语义，或调用了 Grok 不支持的 quota/reset endpoint。
- 按项目规则收敛为 Grok provider 语义：主动 quota probe 应使用最小 `/responses` 请求，而不是依赖不可用的 quota reset 或旧接口。

## 拟处理方式

1. 定位 `usageProbe` 注册点和上游请求路径。
   - 确认 404 来自哪个 Grok/xAI endpoint。
   - 区分是 base URL 归一化问题、endpoint 错误，还是账号没有对应 capability。

2. 调整探针策略。
   - 将主动探针约束为最小 `/responses` 请求：`input: "."`、`max_output_tokens: 1`、`store: false`。
   - 只解析允许的 quota/header 信息，例如 `x-ratelimit-*`、`retry-after`、`xai-subscription-tier`、`xai-entitlement-status`。
   - 不把 404 当成普通噪声无限刷屏，应分类为 endpoint/config 类错误或禁用当前无效探针路径。

3. 处理套餐识别告警。
   - 检查刷新返回的账号信息来源。
   - 如果 Grok OAuth 不稳定返回 `plan_type`，应改为使用 `subscription_tier` / `entitlement_status` 这类 Grok 语义，而不是继续沿用旧 `plan_type` 判断。

4. 验证。
   - 启动服务后观察管理端轮询仍为 `200`。
   - 手动触发一次账号刷新/用量探针。
   - 确认不再重复出现 `用量探针失败: 探针返回状态 404`。

## 暂不处理项

- 前端 `autocomplete`、`id/name`、资源 sourcemap 类浏览器提示：不影响当前功能，可后续统一做前端可访问性清理。