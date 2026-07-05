你是一个资深后端工程师，请设计一个“用户使用量/额度系统（Usage & Billing System）”。

## 背景说明
系统中的使用量数据来自一个内部 gRPC 服务：

Service:
grok_api_v2.GrokBuildBilling

Method:
GetGrokCreditsConfig

该接口用于获取用户的：
- 每周额度（weekly credits limit）
- 已使用额度（used credits）
- 使用百分比（used / limit）
- 重置时间（reset_at）
- 订阅等级对应的 quota（SuperGrok / Free）

⚠️ 注意：
该接口是 gRPC-web 调用，不是普通 REST API。

---

## 系统目标
请设计一个完整的 usage system，包括：

- 用户每周 usage 统计
- quota limit 控制
- usage percentage 计算
- reset cycle（weekly reset）
- rate limit（短窗口限流）

---

## API 设计要求

### 1. 获取使用量
GET /api/usage

返回：
{
  "user_id": "xxx",
  "source": "GrokBuildBilling.GetGrokCreditsConfig",
  "period": "weekly",
  "used": 2000,
  "limit": 10000,
  "percent": 20,
  "remaining": 8000,
  "reset_at": "2026-07-08T18:48:00Z"
}

---

### 2. 增加使用量
POST /api/usage/increment

---

### 3. 获取限流状态（rate limit）
GET /api/rate-limit

window:
- windowSizeSeconds = 7200
- remainingRequests
- totalRequests

---

## 数据来源说明

系统真实数据来自：

1. gRPC Billing Service
   grok_api_v2.GrokBuildBilling/GetGrokCreditsConfig

2. Rate limit gateway
   window-based request limiter (2h window)

---

## 架构要求

请设计：
- API layer（REST）
- Billing service（gRPC client wrapper）
- Redis（实时 usage）
- PostgreSQL（持久化）
- Cron job（weekly reset）

---

## 输出要求

请输出：
1. 系统架构图（文字描述）
2. 数据库设计
3. Redis key 设计
4. usage 计算逻辑
5. gRPC 调用封装方式
6. 完整代码示例（Node.js 或 Python FastAPI）