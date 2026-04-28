# 中国大陆手机号账号登录注册 Spec

## 范围

第一期仅支持中国大陆手机号（`+86`）作为独立账号注册和登录方式。手机号账号与邮箱、Google、GitHub 一样都会创建统一的 `users.id`，但不会自动与已有邮箱或 OAuth 账号合并。

## 账号规则

- 手机号注册成功后必须设置账号名 `username` 和密码。
- `username` 全局唯一，长度 4-32，只允许字母、数字、下划线，不能纯数字，不能以下划线开头或结尾。
- 手机号以 E.164 格式保存到 `users.phone_e164`，如 `+8613812345678`，全局唯一。
- 手机号账号的 `users.email` 使用内部占位邮箱，避免破坏现有邮箱账号的 `NOT NULL + UNIQUE` 约束；用户侧展示优先使用手机号和账号名。
- 账号密码登录支持邮箱或账号名作为标识符。

## 公开 API

### POST `/api/v1/public/check-username`

Request:

```json
{ "username": "alice_2026" }
```

Response:

```json
{ "exists": false, "valid": true }
```

### GET `/api/v1/auth/phone/config`

Response:

```json
{
  "enabled": true,
  "country_code": "CN",
  "dial_code": "+86",
  "captcha": {
    "enabled": true,
    "region": "cn",
    "prefix": "157gnn",
    "scene_id": "k7nl3rju"
  }
}
```

### POST `/api/v1/auth/phone/precheck`

Request:

```json
{
  "phone": "13812345678",
  "fingerprint": "fp_xxx",
  "purpose": "LOGIN"
}
```

Response:

```json
{
  "allowed": true,
  "need_captcha": false,
  "retry_after": 0
}
```

### POST `/api/v1/auth/phone/send-code`

Request:

```json
{
  "phone": "13812345678",
  "fingerprint": "fp_xxx",
  "purpose": "LOGIN",
  "captcha_verify_param": "aliyun-token"
}
```

Response:

```json
{
  "sent": true,
  "expires_in": 300,
  "cooldown": 60,
  "phone": "138****5678"
}
```

限流响应:

```json
{
  "code": 42901,
  "message": "sms rate limited",
  "data": {
    "retry_after": 42,
    "limit_type": "phone_cooldown"
  }
}
```

### POST `/api/v1/auth/phone/login`

登录已注册手机号。

Request:

```json
{ "phone": "13812345678", "code": "123456" }
```

Response: 与 `/auth/login` 一致，返回 JWT。

若手机号未注册，返回 `404` 并带 `needs_register: true`。

### POST `/api/v1/auth/phone/register`

Request:

```json
{
  "phone": "13812345678",
  "code": "123456",
  "username": "alice_2026",
  "password": "sha256(client-side)",
  "invite_code": "optional",
  "referral_code": "optional"
}
```

Response: 与 `/auth/register` 一致，返回 JWT 和用户信息。

## 后台 API

- `GET/PUT /api/v1/admin/security/sms-provider`
- `POST /api/v1/admin/security/sms-provider/test`
- `GET/PUT /api/v1/admin/security/captcha-provider`
- `POST /api/v1/admin/security/captcha-provider/test`
- `GET/PUT /api/v1/admin/security/sms-risk`
- `GET /api/v1/admin/security/sms-logs`
- `GET/POST/DELETE /api/v1/admin/security/phone-risk-rules`

## 风控默认值

- 验证码有效期：300 秒
- 前端冷却：60 秒
- 后端同手机号冷却：60 秒
- 同手机号每小时最多 5 次，每日最多 10 次
- 同 IP 每小时最多 20 次，每日最多 100 次
- 同设备指纹每日最多 10 次
- 单验证码最多错误 5 次
- 连续失败后冻结 15 分钟
- 命中高风险时要求阿里云验证码 2.0

## 阿里云配置

短信默认模板：

- `TemplateCode`: `SMS_505710272`
- `TemplateParam`: `{"code":"123456"}`
- 模板内容：`TOKENHUBHK的验证码为：${code}，5分钟内有效，请勿泄露于他人！`

阿里云验证码服务端校验必须将前端返回的 `CaptchaVerifyParam` 原样传给 `VerifyIntelligentCaptcha`，不得改写。
