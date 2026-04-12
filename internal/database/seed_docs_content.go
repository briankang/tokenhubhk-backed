package database

// ========== 快速入门 ==========

const contentQuickStart = `## 欢迎使用 TokenHub

TokenHub 是一个 AI 模型聚合分销平台，为您提供统一的 API 接口来访问国内外主流 AI 模型。

### 第一步：注册账号

访问平台首页，点击「免费开始」按钮，填写邮箱和密码完成注册。

### 第二步：获取 API Key

登录后进入控制台，在左侧菜单选择「API Keys」：

1. 点击「创建 Key」
2. 输入备注名称（如 "测试用"）
3. 复制生成的 Key（以 ` + "`sk-`" + ` 开头）

> **重要：** API Key 仅在创建时显示一次，请妥善保存。

### 第三步：发送第一个请求

使用 curl 测试 API：

` + "```bash" + `
curl -X POST https://your-domain.com/v1/chat/completions \
  -H "Content-Type: application/json" \
  -H "Authorization: Bearer sk-your-api-key" \
  -d '{
    "model": "gpt-4o-mini",
    "messages": [
      {"role": "user", "content": "你好，介绍一下你自己"}
    ]
  }'
` + "```" + `

使用 Python：

` + "```python" + `
from openai import OpenAI

client = OpenAI(
    api_key="sk-your-api-key",
    base_url="https://your-domain.com/v1"
)

response = client.chat.completions.create(
    model="gpt-4o-mini",
    messages=[{"role": "user", "content": "你好！"}]
)
print(response.choices[0].message.content)
` + "```" + `

### 第四步：选择模型

TokenHub 支持多种 AI 模型，进入「模型」页面查看所有可用模型和定价。

| 模型 | 供应商 | 特点 |
|------|--------|------|
| GPT-4o | OpenAI | 强大的多模态模型 |
| Claude 3.5 Sonnet | Anthropic | 擅长代码和分析 |
| DeepSeek Chat | DeepSeek | 性价比极高 |
| Qwen Plus | 阿里云 | 中文能力优秀 |

### 下一步

- 查看 [API Key 管理](/docs/api-keys) 了解安全最佳实践
- 使用 [Playground](/docs/playground) 在线体验模型
- 参考 [Chat API](/docs/chat-api) 开发您的应用
`

// ========== 平台使用指南 ==========

const contentApiKeys = `## API Key 管理

API Key 是访问 TokenHub API 的凭证，用于身份认证和用量计费。

### 创建 API Key

1. 登录控制台，进入「API Keys」页面
2. 点击「创建 Key」按钮
3. 输入一个有意义的备注名称
4. 系统生成以 ` + "`sk-`" + ` 开头的密钥

> **安全提示：** Key 仅在创建时完整显示一次。创建后仅能看到前 8 位。

### 使用方式

在 HTTP 请求头中添加：

` + "```" + `
Authorization: Bearer sk-your-api-key
` + "```" + `

或在 OpenAI SDK 中配置：

` + "```python" + `
client = OpenAI(
    api_key="sk-your-api-key",
    base_url="https://your-domain.com/v1"
)
` + "```" + `

### 安全最佳实践

- **不要在前端代码中硬编码 Key**，使用环境变量或后端代理
- **定期轮换** Key，删除不再使用的旧 Key
- **限制每个 Key 的用途**，为不同项目创建独立的 Key
- **监控用量**，发现异常及时禁用

### Key 管理操作

| 操作 | 说明 |
|------|------|
| 创建 | 生成新的 API Key |
| 查看用量 | 查看该 Key 的调用次数和 Token 消耗 |
| 删除 | 永久停用该 Key |
`

const contentPlayground = `## Playground 使用指南

Playground 是 TokenHub 提供的在线模型测试工具，无需编写代码即可体验各种 AI 模型。

### 功能介绍

- **模型切换**：下拉选择不同 AI 模型
- **参数调节**：Temperature、Max Tokens、Top P 等
- **流式输出**：实时查看模型响应
- **历史记录**：自动保存对话历史

### 使用步骤

1. 进入控制台，点击左侧「Playground」
2. 在顶部选择要测试的模型
3. 在输入框中输入消息
4. 点击发送或按 Enter
5. 实时查看模型的流式响应

### 参数说明

| 参数 | 范围 | 说明 |
|------|------|------|
| Temperature | 0-2 | 控制随机性，越高越随机 |
| Max Tokens | 1-4096 | 最大生成长度 |
| Top P | 0-1 | 核采样概率 |

### 注意事项

- Playground 使用会消耗您的余额
- 流式模式下支持随时停止生成
- 复杂对话建议使用较大的 Max Tokens
`

const contentBalance = `## 余额充值与额度说明

### 余额体系

TokenHub 采用预付费余额制：

- **余额**：通过充值获得，按使用量实时扣减
- **体验额度**：新注册用户赠送的免费额度

扣费优先级：先扣体验额度，再扣充值余额。

### 充值方式

平台支持多种充值方式：

| 方式 | 说明 | 到账时间 |
|------|------|----------|
| 微信支付 | 扫码支付 | 即时 |
| 支付宝 | 在线支付 | 即时 |
| 对公转账 | 银行转账 | 1-3 工作日 |
| Stripe | 国际信用卡 | 即时 |

### 计费规则

- 按 Token 用量计费，Input 和 Output 分别定价
- 不同模型定价不同，查看「模型」页面了解详情
- 计费精度为小数点后 6 位

### 余额查询

` + "```bash" + `
# 查询余额
curl -H "Authorization: Bearer sk-your-key" \
  https://your-domain.com/api/v1/open/balance
` + "```" + `

### 额度不足

当余额不足时，API 请求将返回 402 状态码。请及时充值以避免服务中断。
`

// ========== 编码工具 ==========

const contentCursor = `## Cursor 接入 TokenHub

Cursor 是一款强大的 AI 编码 IDE，完全支持 OpenAI 兼容 API。

### 配置步骤

1. 打开 Cursor，进入 **Settings → Models**
2. 找到 **OpenAI API Key** 配置区
3. 填写以下信息：

| 配置项 | 值 |
|--------|-----|
| API Key | ` + "`sk-your-tokenhub-key`" + ` |
| Base URL | ` + "`https://your-domain.com/v1`" + ` |

4. 在模型列表中添加需要的模型（如 ` + "`gpt-4o`" + `、` + "`claude-3-5-sonnet-20241022`" + `）
5. 保存设置后即可使用

### 验证连接

在 Cursor 中发起一次对话，确认模型可以正常响应。如果看到回复内容，说明配置成功。

### 推荐模型

| 模型 | 适用场景 |
|------|----------|
| gpt-4o | 通用编码、代码审查 |
| claude-3-5-sonnet | 复杂代码生成、重构 |
| deepseek-chat | 日常编码、性价比之选 |

### 常见问题

**Q: 连接超时？**
A: 检查 Base URL 是否正确，确保网络可以访问 TokenHub 服务器。

**Q: 模型不显示？**
A: 手动在模型列表中添加模型 ID，Cursor 会自动通过 /v1/models 获取。
`

const contentContinue = `## Continue.dev 接入指南

Continue 是一个开源的 AI 代码助手 VS Code 插件，支持自定义 OpenAI 兼容端点。

### 配置方法

编辑 Continue 配置文件 ` + "`~/.continue/config.json`" + `：

` + "```json" + `
{
  "models": [
    {
      "title": "TokenHub GPT-4o",
      "provider": "openai",
      "model": "gpt-4o",
      "apiKey": "sk-your-tokenhub-key",
      "apiBase": "https://your-domain.com/v1"
    },
    {
      "title": "TokenHub DeepSeek",
      "provider": "openai",
      "model": "deepseek-chat",
      "apiKey": "sk-your-tokenhub-key",
      "apiBase": "https://your-domain.com/v1"
    }
  ],
  "tabAutocompleteModel": {
    "title": "TokenHub Autocomplete",
    "provider": "openai",
    "model": "deepseek-chat",
    "apiKey": "sk-your-tokenhub-key",
    "apiBase": "https://your-domain.com/v1"
  }
}
` + "```" + `

### 功能支持

- **Chat**: 代码对话、问答（/v1/chat/completions）
- **Tab 补全**: 行内代码补全
- **Slash 命令**: /edit, /comment, /explain 等

### 特性

Continue 支持 FIM（Fill-in-the-Middle）补全模式，适合代码补全场景。
`

const contentCline = `## Cline 接入指南

Cline（原 Claude Dev）是 VS Code 中的 AI Agent 插件，可以自主执行编码任务。

### 配置步骤

1. 在 VS Code 中安装 **Cline** 扩展
2. 打开 Cline 设置面板
3. 选择 **OpenAI Compatible** 作为 Provider
4. 填写配置：

| 配置项 | 值 |
|--------|-----|
| Base URL | ` + "`https://your-domain.com/v1`" + ` |
| API Key | ` + "`sk-your-tokenhub-key`" + ` |
| Model | ` + "`claude-3-5-sonnet-20241022`" + ` 或其他模型 |

5. 点击保存

### 推荐模型

Cline 是 Agent 类工具，建议使用能力较强的模型：

- **claude-3-5-sonnet**: 代码理解和生成最佳
- **gpt-4o**: 通用能力强
- **qwen-max**: 中文项目首选

### 注意事项

Cline 可能会连续多次调用 API 来完成任务，请关注 Token 消耗量。建议设置合理的 Max Tokens 上限。
`

// ========== 对话客户端 ==========

const contentLobeChat = `## LobeChat 接入指南

LobeChat 是一款开源的 AI 对话前端应用，支持 OpenAI 兼容 API。

### Docker 部署方式

` + "```bash" + `
docker run -d -p 3210:3210 \
  -e OPENAI_API_KEY=sk-your-tokenhub-key \
  -e OPENAI_PROXY_URL=https://your-domain.com/v1 \
  --name lobe-chat \
  lobehub/lobe-chat
` + "```" + `

### UI 配置方式

1. 打开 LobeChat 设置
2. 进入「语言模型」→「OpenAI」
3. 配置：
   - API Key: ` + "`sk-your-tokenhub-key`" + `
   - API 代理地址: ` + "`https://your-domain.com/v1`" + `
4. 添加自定义模型列表

### 特性支持

- 流式响应 (SSE)
- 多模型切换
- 插件系统
- 助手市场
- Vision 多模态

### 推荐配置

优先使用 Docker 部署方式，环境变量配置更加安全，不会将 Key 暴露在前端。
`

const contentNextChat = `## ChatGPT-Next-Web 接入指南

NextChat（ChatGPT-Next-Web）是最流行的 ChatGPT 前端之一，支持 Web、桌面和移动端。

### Docker 部署

` + "```bash" + `
docker run -d -p 3000:3000 \
  -e OPENAI_API_KEY=sk-your-tokenhub-key \
  -e BASE_URL=https://your-domain.com \
  --name nextchat \
  yidadaa/chatgpt-next-web
` + "```" + `

### UI 配置

1. 打开 NextChat 设置
2. 填写：
   - API Key: ` + "`sk-your-tokenhub-key`" + `
   - 接口地址: ` + "`https://your-domain.com`" + `
3. 自定义模型语法: ` + "`模型名=显示名`" + `

### 自定义模型

在设置中添加自定义模型列表：

` + "```" + `
gpt-4o=GPT-4o
deepseek-chat=DeepSeek
claude-3-5-sonnet-20241022=Claude 3.5
qwen-plus=Qwen Plus
` + "```" + `

### 特性

- 轻量级，快速部署
- 支持 Vercel 一键部署
- Web/桌面/移动多端支持
`

const contentOpenWebUI = `## Open WebUI 接入指南

Open WebUI 是一个功能丰富的自托管 AI 前端，支持 Ollama 和 OpenAI 兼容 API。

### Docker 部署

` + "```bash" + `
docker run -d -p 8080:8080 \
  -e OPENAI_API_BASE_URL=https://your-domain.com/v1 \
  -e OPENAI_API_KEY=sk-your-tokenhub-key \
  -v open-webui:/app/backend/data \
  --name open-webui \
  ghcr.io/open-webui/open-webui:main
` + "```" + `

### 管理后台配置

1. 访问 Open WebUI 管理后台
2. 进入 **Settings → Connections → OpenAI**
3. 添加新连接：
   - URL: ` + "`https://your-domain.com/v1`" + `
   - API Key: ` + "`sk-your-tokenhub-key`" + `
4. 点击测试连接
5. 保存后模型列表将自动获取

### 特性支持

- 用户管理系统
- 知识库 RAG
- Web 搜索集成
- 多模型对话
- 文件上传分析
`

// ========== Coding Plan ==========

const contentCodingPlan = `## Coding Plan 产品介绍

TokenHub Coding Plan 是专为 AI 编码工具设计的模型聚合方案。

### 什么是 Coding Plan

Coding Plan 提供标准的 OpenAI 兼容 API（/v1/*），让您可以在任何支持自定义 API 的编码工具中使用 TokenHub 的模型。

### 支持的端点

| 端点 | 方法 | 功能 |
|------|------|------|
| /v1/chat/completions | POST | 聊天对话（主要） |
| /v1/completions | POST | 代码补全/FIM |
| /v1/models | GET | 模型列表 |
| /v1/embeddings | POST | 向量嵌入 |

### 支持的编码工具

| 工具 | 支持方式 | 推荐度 |
|------|----------|--------|
| Cursor | 原生支持 | ★★★★★ |
| Continue.dev | 原生支持 | ★★★★★ |
| Cline | 原生支持 | ★★★★★ |
| GitHub Copilot | BYOK 支持 | ★★★★ |
| Windsurf | 部分支持 | ★★★★ |
| Aider | 环境变量 | ★★★★ |
| JetBrains AI | 原生支持 | ★★★★ |

### 使用方法

在编码工具中配置以下信息即可：

` + "```" + `
Base URL: https://your-domain.com/v1
API Key:  sk-your-tokenhub-key
` + "```" + `

### 计费

Coding Plan 按 Token 用量计费，与普通 API 调用使用同一余额，无额外费用。
`

// ========== API 参考 ==========

const contentAuth = `## 认证方式

TokenHub 支持三种认证方式，适用于不同场景。

### 1. JWT Token（Web 前端）

用于前端页面认证，通过登录接口获取：

` + "```bash" + `
# 登录获取 Token
curl -X POST https://your-domain.com/api/v1/auth/login \
  -H "Content-Type: application/json" \
  -d '{"email": "user@example.com", "password": "your-password"}'
` + "```" + `

响应：
` + "```json" + `
{
  "code": 0,
  "data": {
    "access_token": "eyJhbGciOiJIUzI1NiIs...",
    "refresh_token": "eyJhbGciOiJIUzI1NiIs...",
    "expires_in": 86400
  }
}
` + "```" + `

使用：` + "`Authorization: Bearer <access_token>`" + `

### 2. API Key（OpenAI 兼容）

用于 AI 模型调用，通过控制台创建：

` + "```bash" + `
curl -X POST https://your-domain.com/v1/chat/completions \
  -H "Authorization: Bearer sk-your-api-key" \
  -H "Content-Type: application/json" \
  -d '{"model": "gpt-4o", "messages": [{"role": "user", "content": "hello"}]}'
` + "```" + `

### 3. Bearer Token（Open API）

用于企业级 Open API 接口，复用 API Key：

` + "```bash" + `
curl -H "Authorization: Bearer sk-your-api-key" \
  https://your-domain.com/api/v1/open/account
` + "```" + `

### 认证失败

| 状态码 | 含义 |
|--------|------|
| 401 | 未提供认证信息或 Token 已过期 |
| 403 | 权限不足（角色不匹配） |
`

const contentChatAPI = `## Chat Completions API

TokenHub 完全兼容 OpenAI Chat Completions API。

### 请求

` + "```" + `
POST /v1/chat/completions
` + "```" + `

### 请求参数

` + "```json" + `
{
  "model": "gpt-4o",
  "messages": [
    {"role": "system", "content": "You are a helpful assistant."},
    {"role": "user", "content": "Hello!"}
  ],
  "temperature": 0.7,
  "max_tokens": 2048,
  "stream": false
}
` + "```" + `

### 参数说明

| 参数 | 类型 | 必填 | 说明 |
|------|------|------|------|
| model | string | 是 | 模型 ID |
| messages | array | 是 | 消息列表 |
| temperature | float | 否 | 随机性 (0-2) |
| max_tokens | int | 否 | 最大生成 Token 数 |
| stream | bool | 否 | 是否启用流式输出 |
| top_p | float | 否 | 核采样 (0-1) |
| stop | array | 否 | 停止序列 |

### 非流式响应

` + "```json" + `
{
  "id": "chatcmpl-xxx",
  "object": "chat.completion",
  "model": "gpt-4o",
  "choices": [
    {
      "index": 0,
      "message": {"role": "assistant", "content": "Hello!"},
      "finish_reason": "stop"
    }
  ],
  "usage": {
    "prompt_tokens": 10,
    "completion_tokens": 5,
    "total_tokens": 15
  }
}
` + "```" + `

### 流式响应

设置 ` + "`stream: true`" + ` 后，响应为 SSE 格式：

` + "```" + `
data: {"id":"chatcmpl-xxx","choices":[{"delta":{"content":"Hello"}}]}
data: {"id":"chatcmpl-xxx","choices":[{"delta":{"content":"!"}}]}
data: [DONE]
` + "```" + `

### 可用模型

通过 ` + "`GET /v1/models`" + ` 获取所有可用模型列表。
`

const contentOpenAPI = `## Open API 企业接口

Open API 提供企业级的消费查询、用量统计、余额管理等接口。

### 认证

使用 API Key 的 Bearer Token 认证：
` + "```" + `
Authorization: Bearer sk-your-api-key
` + "```" + `

### 接口列表

#### 账户信息
` + "```" + `
GET /api/v1/open/account
` + "```" + `

#### 余额查询
` + "```" + `
GET /api/v1/open/balance
GET /api/v1/open/balance/recharge-records
` + "```" + `

#### 消费查询
` + "```" + `
GET /api/v1/open/consumption/summary?start_date=2026-01-01&end_date=2026-01-31
GET /api/v1/open/consumption/details?page=1&page_size=20
GET /api/v1/open/consumption/export?format=csv
` + "```" + `

#### 用量统计
` + "```" + `
GET /api/v1/open/usage/stats?period=day
GET /api/v1/open/usage/trend?days=30
` + "```" + `

#### 模型定价
` + "```" + `
GET /api/v1/open/models/pricing
` + "```" + `

#### Key 管理
` + "```" + `
GET /api/v1/open/account/keys
GET /api/v1/open/account/keys/:id/usage
` + "```" + `

### 限流

Open API 限流 60 req/min，超过限制返回 429 状态码。
`

const contentErrorCodes = `## 错误码参考

TokenHub API 使用标准 HTTP 状态码配合自定义错误码。

### HTTP 状态码

| 状态码 | 含义 |
|--------|------|
| 200 | 请求成功 |
| 400 | 请求参数错误 |
| 401 | 认证失败 |
| 402 | 余额不足 |
| 403 | 权限不足 |
| 404 | 资源不存在 |
| 429 | 请求频率超限 |
| 500 | 服务器内部错误 |

### 响应格式

成功：
` + "```json" + `
{
  "code": 0,
  "message": "success",
  "data": { ... }
}
` + "```" + `

失败：
` + "```json" + `
{
  "code": 40001,
  "message": "参数错误: model is required"
}
` + "```" + `

### 自定义错误码

| 错误码 | 含义 |
|--------|------|
| 0 | 成功 |
| 40001 | 参数校验失败 |
| 40101 | Token 无效或已过期 |
| 40102 | API Key 无效 |
| 40201 | 余额不足 |
| 40301 | 权限不足 |
| 40401 | 资源不存在 |
| 42901 | 请求频率超限 |
| 50001 | 服务器内部错误 |
| 50002 | 上游供应商错误 |
| 50003 | 模型不可用 |
`

// ========== 部署指南 ==========

const contentDockerDeploy = `## Docker Compose 一键部署

使用 Docker Compose 可以快速部署 TokenHub 平台。

### 前置条件

- Docker 20.10+
- Docker Compose v2+
- 至少 2GB 可用内存

### 部署步骤

1. **克隆项目**

` + "```bash" + `
git clone https://github.com/your-org/tokenhub.git
cd tokenhub
` + "```" + `

2. **配置环境变量**

` + "```bash" + `
cp .env.example .env
# 编辑 .env 文件，设置数据库密码、JWT 密钥等
` + "```" + `

3. **启动服务**

` + "```bash" + `
docker-compose up -d
` + "```" + `

4. **访问平台**

- 前端: http://localhost:3000
- API: http://localhost:8090
- 安装向导: http://localhost:3000/setup

### docker-compose.yml

` + "```yaml" + `
version: '3.8'
services:
  app:
    build: ./server/go-server
    ports:
      - "8090:8090"
    environment:
      - DB_HOST=mysql
      - DB_PORT=3306
      - DB_NAME=tokenhub
      - DB_USER=root
      - DB_PASSWORD=your-password
      - REDIS_ADDR=redis:6379
      - JWT_SECRET=your-jwt-secret
    depends_on:
      - mysql
      - redis

  mysql:
    image: mysql:8.0
    environment:
      MYSQL_ROOT_PASSWORD: your-password
      MYSQL_DATABASE: tokenhub
    volumes:
      - mysql_data:/var/lib/mysql
    ports:
      - "3306:3306"

  redis:
    image: redis:7-alpine
    ports:
      - "6379:6379"

volumes:
  mysql_data:
` + "```" + `

### 验证部署

` + "```bash" + `
# 检查服务状态
docker-compose ps

# 查看日志
docker-compose logs -f app

# 健康检查
curl http://localhost:8090/health
` + "```" + `
`

const contentManualDeploy = `## 手动部署与环境变量

如果不使用 Docker，可以手动部署 TokenHub。

### 环境要求

| 组件 | 最低版本 | 推荐版本 |
|------|----------|----------|
| Go | 1.21 | 1.22+ |
| MySQL | 8.0 | 8.0+ |
| Redis | 6.0 | 7.0+ |
| Node.js | 18 | 20+ |

### 后端部署

` + "```bash" + `
# 进入后端目录
cd server/go-server

# 编译
go build -o tokenhub-server ./cmd/server

# 运行
./tokenhub-server
` + "```" + `

### 前端构建

` + "```bash" + `
# 安装依赖
npm install

# 构建
npm run build

# 产物在 dist/ 目录
` + "```" + `

### 环境变量参考

| 变量名 | 必填 | 默认值 | 说明 |
|--------|------|--------|------|
| DB_HOST | 是 | localhost | MySQL 主机 |
| DB_PORT | 否 | 3306 | MySQL 端口 |
| DB_NAME | 是 | tokenhub | 数据库名 |
| DB_USER | 是 | root | 数据库用户 |
| DB_PASSWORD | 是 | - | 数据库密码 |
| REDIS_ADDR | 是 | localhost:6379 | Redis 地址 |
| REDIS_PASSWORD | 否 | - | Redis 密码 |
| JWT_SECRET | 是 | - | JWT 签名密钥 |
| SERVER_PORT | 否 | 8090 | 服务端口 |
| LOG_LEVEL | 否 | info | 日志级别 |
| PAYMENT_ENCRYPT_KEY | 否 | - | 支付配置加密密钥 |

### Nginx 配置

` + "```nginx" + `
server {
    listen 80;
    server_name your-domain.com;

    # 前端静态文件
    location / {
        root /path/to/dist;
        try_files $uri $uri/ /index.html;
    }

    # API 代理
    location /api/ {
        proxy_pass http://127.0.0.1:8090;
        proxy_set_header Host $host;
        proxy_set_header X-Real-IP $remote_addr;
    }

    # OpenAI 兼容路由代理
    location /v1/ {
        proxy_pass http://127.0.0.1:8090;
        proxy_set_header Host $host;
        proxy_buffering off;
    }
}
` + "```" + `

### 生产环境建议

- 使用 HTTPS（Let's Encrypt 免费证书）
- 设置强密码的 JWT_SECRET
- 开启 MySQL 慢查询日志
- 配置 Redis 持久化
- 使用 systemd 管理后端进程
`

// ========== 平台使用指南 - 代理分销 ==========

// contentAgentDistribution 代理商分销指南文档内容
const contentAgentDistribution = `## 代理商分销指南

TokenHub 提供三级代理分销体系，代理商可以通过推广平台获取持续佣金收入。

### 三级代理体系

| 级别 | 佣金比例 | 说明 |
|------|----------|------|
| L1 一级代理 | 15% | 直接推荐用户的消费提成 |
| L2 二级代理 | 8% | 下级推荐用户的消费提成 |
| L3 三级代理 | 3% | 三级下线用户的消费提成 |

### 代理申请流程

1. **注册账号** — 在平台完成正常注册
2. **联系管理员** — 提交代理申请，说明推广计划
3. **审核通过** — 管理员审核并升级为代理角色
4. **获取专属链接** — 进入代理面板获取推广链接和邀请码

### 代理面板功能

登录后进入「代理面板」，可以：

- **查看团队统计** — 直属下级数、团队总人数、本月佣金、累计收益
- **团队管理** — 查看下级树形结构，了解每个成员的消费和佣金
- **佣金明细** — 查看每笔佣金的来源、金额、状态
- **推广链接** — 复制专属推广链接和邀请码

### 佣金计算规则

佣金基于下级用户的实际 Token 消费金额计算：

` + "```" + `
佣金 = 下级消费金额 × 对应级别佣金比例

示例：
- 您的直属用户 A 消费了 $100
  L1 佣金 = $100 × 15% = $15
- A 推荐的用户 B 消费了 $200
  L2 佣金 = $200 × 8% = $16
- B 推荐的用户 C 消费了 $300
  L3 佣金 = $300 × 3% = $9
` + "```" + `

### 佣金结算

- **计算时机** — 每笔消费完成后异步计算
- **状态流转** — 待结算 → 已结算 → 已提现
- **提现门槛** — 累计佣金达到最低提现金额后可申请提现
- **结算周期** — 月结，每月初统一结算上月佣金

### 注意事项

- 代理商自己的消费不计入佣金
- 佣金比例可能根据平台政策调整
- 作弊行为（刷单等）将导致代理资格被取消
`

// contentPersonalReferral 个人邀请返现指南文档内容
const contentPersonalReferral = `## 个人邀请返现指南

TokenHub 为所有用户提供邀请返现机制，邀请朋友注册即可获取返现奖励。

### 获取邀请码

1. 登录 TokenHub 平台
2. 进入「控制台」→「设置」页面
3. 在「我的邀请码」区域查看您的专属邀请码
4. 或进入「推荐有奖」页面获取完整的推广链接

### 分享方式

- **邀请链接** — 复制您的专属注册链接发送给朋友
- **邀请码** — 朋友注册时填写您的邀请码（8 位字母数字）

### 返现规则

| 项目 | 说明 |
|------|------|
| 返现比例 | 被邀请用户消费金额的 5%（默认） |
| 邀请人奖励 | 被邀请用户每次消费自动返现到您的余额 |
| 被邀请人奖励 | 新用户注册时获得额外免费额度 |
| 绑定关系 | 永久绑定，不可更改 |

### 返现计算示例

` + "```" + `
您邀请了用户 D，用户 D 当月消费 $50

您的返现 = $50 × 5% = $2.50
返现自动充入您的账户余额
` + "```" + `

### 查看返现记录

进入「推荐有奖」页面，可以查看：

- **邀请统计** — 已邀请人数、活跃用户数
- **返现汇总** — 累计返现金额、本月返现
- **明细记录** — 每笔返现的来源和金额

### 与代理分销的区别

| 对比项 | 个人邀请返现 | 代理商分销 |
|--------|------------|----------|
| 适用对象 | 所有用户 | 代理商角色 |
| 层级 | 仅一级 | 三级 |
| 返现比例 | 5% | 15%/8%/3% |
| 管理面板 | 推荐有奖页面 | 专属代理面板 |
| 申请方式 | 自动开通 | 需管理员审核 |

### 常见问题

**Q: 邀请返现可以提现吗？**
A: 返现直接充入余额，用于 API 调用抵扣，不支持直接提现。

**Q: 邀请关系可以解除吗？**
A: 邀请关系一旦绑定为永久关系，无法解除。

**Q: 我可以同时是代理商和普通用户吗？**
A: 可以。升级为代理商后，您的推荐关系会自动切换为代理分销体系（更高佣金）。
`
