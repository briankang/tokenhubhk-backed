package database

const contentQuickStart = `## 这篇适合谁？

如果你是第一次使用 TokenHub，可以从这里开始。跟着做一遍，你会完成三件事：注册账号、创建 API Key、发出第一条模型请求。

不用一开始就理解所有参数。先跑通，再慢慢优化。

## 第一步：注册账号

1. 打开[注册页面](/register)，或者在首页点击「免费开始」。
2. 输入邮箱、密码和验证码。密码建议使用 12 位以上，并包含字母、数字和符号。
3. 如果页面提供 Google 或 GitHub 登录，也可以直接选择第三方账号授权。
4. 注册完成后进入控制台，先确认右上角显示的是您的邮箱或用户名。

## 第二步：确认余额

1. 登录后进入[余额页面](/dashboard/balance)。
2. 查看「可用余额」和「体验额度」。
3. 如果余额不足，先按页面提示完成充值。充值完成后刷新页面确认到账。

## 第三步：创建 API Key

1. 进入 [API Keys](/dashboard/keys) 页面。
2. 点击「创建 Key」。
3. 填写便于识别的名称，例如「本地测试」或「生产服务」。
4. 创建后立即复制完整 Key。完整 Key 只展示一次，之后只能看到部分前缀。

## 第四步：在 Playground 试用模型

1. 先打开[模型市场](/models)，随便挑一个可用、价格合适的模型，复制模型 ID。
2. 进入 [Playground](/playground)。
3. 在模型选择器中选择刚才看的模型。
4. 输入一条接近真实场景的问题，例如「请用三句话介绍 TokenHub」。
5. 点击发送，观察模型回复、Request ID 和用量信息。

## 第五步：发起 API 请求

把下面示例中的域名、Key 和模型 ID 替换成自己的信息：

~~~bash
curl -X POST https://your-domain.com/v1/chat/completions \
  -H "Content-Type: application/json" \
  -H "Authorization: Bearer sk-your-api-key" \
  -d '{
    "model": "gpt-4o-mini",
    "messages": [
      {"role": "user", "content": "你好，请介绍一下你自己"}
    ]
  }'
~~~

Python 示例：

~~~python
from openai import OpenAI

client = OpenAI(
    api_key="sk-your-api-key",
    base_url="https://your-domain.com/v1",
)

response = client.chat.completions.create(
    model="gpt-4o-mini",
    messages=[{"role": "user", "content": "你好"}],
)
print(response.choices[0].message.content)
~~~

## 检查结果

调用成功后，响应中会包含 'choices' 和 'usage'。如果返回 401，请检查 API Key；如果返回 402，请检查余额；如果返回 429，请稍后重试或降低请求频率。

下一步建议继续看[查看模型与选择模型](/docs/choose-models)，先把模型怎么选搞清楚。`

const contentAccountSecurity = `## 注册方式

平台支持邮箱注册登录；如果当前站点开启了 Google 或 GitHub 登录，也可以使用第三方账号登录。第三方登录会绑定已验证邮箱，后续可继续使用同一邮箱登录。

## 登录步骤

1. 打开「登录」页面。
2. 输入邮箱和密码，或点击 Google / GitHub 登录按钮。
3. 登录成功后进入控制台。
4. 如果页面提示账号不存在，请先完成注册；如果提示密码错误，请使用找回密码或联系客服。

## 账号安全建议

- 不要与他人共用账号。
- 不要把 API Key 粘贴到截图、聊天记录或公开仓库。
- 生产环境建议为每个项目创建独立 API Key，便于停用和追踪用量。
- 如果怀疑 Key 泄露，立即删除旧 Key 并创建新 Key。

## 退出登录

在右上角账号菜单中点击「退出登录」。公共电脑使用后建议退出登录，并清除浏览器中保存的敏感信息。`

const contentApiKeys = `## API Key 的用途

API Key 就像你的模型调用通行证。调用 '/v1/chat/completions'、'/v1/models' 这类接口时，都要用它来证明「这是你的账号在调用」。

注意：网页登录状态和 API Key 是两回事。网页登录用来打开控制台，API Key 用来给程序调用模型，不要混着用。

## 创建 API Key

1. 登录控制台。
2. 进入 [API Keys](/dashboard/keys) 页面。
3. 点击「创建 Key」。
4. 输入名称。建议名称包含用途和环境，例如「crm-prod」或「local-test」。
5. 点击确认后复制完整 Key，并保存到安全位置。

## 保存 API Key

推荐把 Key 放在服务端环境变量中：

~~~bash
TOKENHUB_API_KEY=sk-your-api-key
TOKENHUB_BASE_URL=https://your-domain.com/v1
~~~

不要把 Key 写进前端代码、移动端安装包、公开仓库或可被用户查看的配置文件。

## 删除或轮换 API Key

1. 新建一个 Key，并把应用配置切换到新 Key。
2. 观察调用是否正常。
3. 回到「API Keys」页面删除旧 Key。
4. 如果有多个服务共用旧 Key，需要逐个服务替换后再删除。

## 用量查看

在 [API Keys](/dashboard/keys) 页面可以查看每个 Key 的调用次数、最近使用时间和用量。发现异常增长时，先停用或删除对应 Key，再排查调用来源。

一个小建议：生产环境、本地测试、第三方客户端最好用不同 Key。这样哪边费用异常，一眼就能看出来。`

const contentBalance = `## 余额类型

平台通常会显示两类额度：

- 充值余额：您实际充值后的可用余额。
- 体验额度：平台赠送的新用户或活动额度。

扣费时一般优先使用体验额度，再使用充值余额。具体以账单页展示为准。

## 充值步骤

1. 进入[余额页面](/dashboard/balance)。
2. 选择充值金额。
3. 选择付款方式。页面会展示当前站点已启用的在线支付或对公转账方式。
4. 完成付款后返回平台。
5. 刷新余额页，确认充值记录状态为成功。

## 查看账单

1. 进入[余额页面](/dashboard/balance)或账单记录页面。
2. 按时间筛选充值记录和消费记录。
3. 对照模型、Token 用量、扣费金额和 Request ID。
4. 如果账单和调用日志不一致，请保留 Request ID 便于客服排查。

## 余额不足

余额不足时，模型请求可能返回 402。处理方式：

1. 先到[余额页面](/dashboard/balance)确认可用余额。
2. 完成充值或联系客服补充额度。
3. 重试请求。

## 对公转账注意事项

如果使用对公转账，请在备注中填写平台要求的信息。转账后通常需要人工确认，到账时间以页面说明为准。`

const contentChooseModels = `## 先去哪里看模型？

想看平台现在能用哪些模型，请直接打开[模型市场](/models)。这里是给用户看的模型入口，不需要关心背后是谁提供能力，也不用记一堆技术名词。

进入模型市场后，建议按这个顺序看：

1. 先用搜索框输入模型名称、用途关键词或能力关键词，比如「长上下文」「图片」「代码」「低价」。
2. 看模型状态。只有显示可用的模型，才建议放进正式请求里。
3. 看价格。重点看输入价格和输出价格，输出越长通常花费越多。
4. 看上下文。上下文越大，能一次放进去的历史消息、文档内容越多。
5. 看能力标签。比如是否支持图片输入、JSON 输出、联网搜索、深度思考等。

## 不知道选哪个？先这样挑

别一上来就选最贵的模型。大多数场景可以先从便宜、稳定、响应快的模型试起，真的不够用再升级。

| 你的场景 | 优先看什么 |
| --- | --- |
| 日常聊天、客服问答 | 价格、响应速度、中文表达 |
| 总结长文章或长对话 | 上下文长度、最大输出长度 |
| 写代码、解释代码 | 代码能力、推理能力、上下文长度 |
| 识别图片或多模态输入 | 是否有图片输入能力标签 |
| 生产系统接入 | 稳定性、价格、延迟、错误率 |

## 模型 ID 很重要

API 调用时要填的是模型 ID，不是页面里的展示标题。最稳妥的做法是：

1. 在[模型市场](/models)找到目标模型。
2. 打开或展开模型详情。
3. 复制页面展示的模型 ID。
4. 把这个 ID 填到请求里的 'model' 字段。

如果你是通过接口取模型列表，也可以从 '/v1/models' 返回结果里复制模型 ID。想看某个模型具体支持哪些参数，可以去[模型 API 文档](/docs/api-models)搜索这个模型。

## 模型不可用时怎么处理

如果请求提示模型不存在、不可用或没有权限，先别急，按下面排查：

1. 回到[模型市场](/models)，确认这个模型还在列表里，并且状态可用。
2. 检查请求里的 'model' 有没有大小写、空格或版本号写错。
3. 换一个同类型模型试一下，确认是不是单个模型临时不可用。
4. 保存错误响应和 Request ID，联系客服时会省很多来回沟通。`

const contentPricingUsage = `## 先理解一个词：Token

模型不是按「一条消息」收费，而是按 Token 计算。你可以把 Token 理解成模型读和写时使用的小片段：你发出去的内容会产生输入 Token，模型回复的内容会产生输出 Token。

一般来说：

- 提示词越长，输入 Token 越多。
- 回复越长，输出 Token 越多。
- 长对话一直带历史记录，费用会慢慢变高。
- 图片、长文档、多轮上下文等场景，通常也会增加用量。

## 去哪里看价格？

请打开[模型市场](/models)，找到你准备使用的模型。每个模型都会展示价格和能力信息。看价格时，重点看这几项：

1. 输入价格：你发给模型的内容怎么计费。
2. 输出价格：模型回复给你的内容怎么计费。
3. 上下文长度：一次请求能放多少内容。
4. 最大输出：模型一次最多能回复多长。

正式接入前，建议先去 [Playground](/playground) 用几条真实业务问题试一下。这样你能看到大概会消耗多少 Token，心里会更有数。

## 流式响应会不会扣费？

会。流式只是「一边生成一边返回」，不是免费模式。模型已经生成出来的内容，通常都会进入用量统计。

如果用户中途停止了流式输出，一般会按已经生成的部分记录用量。具体账单以余额页和用量记录为准。

## 哪些请求通常不会产生模型费用？

下面这些请求通常还没真正进入模型调用：

- API Key 写错导致 401。
- 余额不足导致 402。
- JSON 格式错误导致 400。
- 模型 ID 写错导致请求被平台拦截。

如果请求已经进入模型并产生了回复，即使你的业务代码没有使用这段回复，也可能会记录用量。

## 怎么把成本控制住？

几个很实用的小习惯：

1. 给 'max_tokens' 设置合理上限，不要让模型无限写。
2. 不要每次都把整段历史聊天塞进去，长对话可以先做摘要。
3. 能用轻量模型解决的任务，不要默认上高阶模型。
4. 批量任务先拿 5 到 10 条样本测试，再扩大规模。
5. 给不同项目创建不同 API Key，这样账单更容易看清楚。`

const contentPlayground = `## Playground 是做什么的？

[Playground](/playground) 可以理解成一个网页里的模型试验台。你还没写代码之前，可以先在这里试模型、试提示词、试参数，看看回复是否符合预期。

它是真实调用模型的，所以会使用你的余额或体验额度。好处是：你能在正式接入前先看效果，避免代码写完才发现模型不合适。

## 发送第一条消息

1. 打开 [Playground](/playground)。
2. 在模型选择器里选一个模型。如果不知道选哪个，可以先去[模型市场](/models)看价格和能力。
3. 在输入框里写一个真实问题，不建议只写「你好」，可以写接近业务场景的问题。
4. 点击发送。
5. 等回复完成后，看三样东西：回复内容、Request ID、用量信息。

## 参数怎么调才不容易迷路？

刚开始建议少动参数，先用默认值。需要优化时，再按下面方向调整：

| 参数 | 适合什么时候调 | 简单建议 |
| --- | --- | --- |
| temperature | 想控制回复随机性 | 客服、分类、抽取用低一点；创作、营销文案可高一点 |
| top_p | 想控制采样范围 | 不确定就保持默认，别和 temperature 一起大幅调整 |
| max_tokens | 想限制回复长度 | 怕模型写太长、费用太高，就给一个明确上限 |
| stream | 想让回复边生成边显示 | 聊天界面建议开启，后台批处理可以不开 |

## 调好以后怎么放到代码里？

Playground 调到满意后，把下面这些信息带到你的服务端代码里：

1. 模型 ID，也就是请求里的 'model'。
2. 消息数组，也就是 'messages'。
3. 你调整过的参数，比如 'temperature'、'max_tokens'、'stream'。

API Key 不建议放在前端页面里。生产环境请放在服务端环境变量中。

## Request ID 要记一下

每次模型回复都会有 Request ID。遇到扣费疑问、响应中断、输出异常时，请把 Request ID 一起发给客服。它就像这次请求的编号，能帮助我们快速定位。`

const contentCustomParams = `## 先用标准参数，够用就别复杂化

大多数情况下，用标准 OpenAI 兼容参数就够了。建议先从下面这些参数开始：

~~~json
{
  "model": "gpt-4o-mini",
  "messages": [{"role": "user", "content": "hello"}],
  "temperature": 0.7,
  "top_p": 1,
  "max_tokens": 1024,
  "stream": false
}
~~~

## 什么时候需要自定义参数？

有些模型会有少量高级选项，比如搜索、思考、特殊输出格式等。只有当标准参数不够用，并且你已经在[模型 API 文档](/docs/api-models)确认目标模型支持时，再把这些选项放入 'extra_body' 或 'custom_params'。

平台会先处理 'model'、'messages'、'temperature' 这类标准参数，再合并这些高级选项。

~~~json
{
  "model": "your-model",
  "messages": [{"role": "user", "content": "请联网检索今天的新闻"}],
  "extra_body": {
    "enable_search": true
  }
}
~~~

## 使用建议

1. 先到[模型 API 文档](/docs/api-models)确认目标模型是否支持。
2. 自定义字段不要和 'model'、'messages'、'stream' 等标准字段重名。
3. 先在 [Playground](/playground) 测试，再放入生产环境。
4. 如果返回参数不支持，先删掉这个字段，让基础请求跑通。
5. 不要把密钥、签名、Authorization、计费字段放进这些自定义对象里。

## 记住这条安全边界

自定义参数只是给模型增加高级选项，不会绕过平台认证、余额检查和模型权限。用户请求里只需要 TokenHub API Key，不需要也不应该传其它平台的密钥或签名。`

const contentAuth = `## 两类认证

TokenHub 常见认证分为两类：

| 场景 | 凭证 | 用途 |
| --- | --- | --- |
| 网页控制台 | 登录后的访问令牌 | 访问后台页面、查看账单、管理 Key |
| 模型 API | API Key | 调用 '/v1/chat/completions'、'/v1/models' |

## 模型调用认证

所有模型调用都在 HTTP Header 中传入 API Key：

~~~http
Authorization: Bearer sk-your-api-key
Content-Type: application/json
~~~

## 网页登录认证

网页登录后，浏览器会保存登录状态。用户通常不需要手动处理 JWT。请不要把网页登录 Token 当作模型 API Key 使用。

## 认证失败排查

1. 确认 Header 名称是 'Authorization'。
2. 确认格式是 'Bearer sk-...'，中间有一个空格。
3. 确认 Key 没有多复制空格、换行或引号。
4. 确认 Key 没有被删除。
5. 确认请求地址是当前站点的 '/v1' 地址。`

const contentChatAPI = `## Endpoint

~~~http
POST /v1/chat/completions
~~~

## 非流式请求

~~~bash
curl -X POST https://your-domain.com/v1/chat/completions \
  -H "Authorization: Bearer sk-your-api-key" \
  -H "Content-Type: application/json" \
  -d '{
    "model": "gpt-4o-mini",
    "messages": [
      {"role": "system", "content": "You are a helpful assistant."},
      {"role": "user", "content": "请用三句话解释大模型 API"}
    ],
    "temperature": 0.7,
    "max_tokens": 800
  }'
~~~

## 流式请求

~~~json
{
  "model": "gpt-4o-mini",
  "messages": [
    {"role": "user", "content": "写一段产品介绍"}
  ],
  "stream": true
}
~~~

流式响应使用 SSE 格式，客户端会持续收到 'data:' 片段，直到收到 '[DONE]'。

## messages 格式

| role | 用途 |
| --- | --- |
| system | 设置助手行为和边界 |
| user | 用户输入 |
| assistant | 历史助手回复 |

## 响应字段

成功响应通常包含：

- 'id'：请求 ID。
- 'choices'：模型回复。
- 'usage'：Token 用量。
- 'model'：实际调用模型。

生产环境建议记录 'id'、模型、用量和业务用户 ID，便于账单核对。`

const contentModelsAPI = `## 这个接口是做什么的？

'/v1/models' 用来获取当前账号可以调用的模型列表。很多第三方客户端会用它自动刷新模型选择器，你自己的系统也可以用它来生成可选模型列表。

如果你只是想人工查看模型、价格和能力，直接打开[模型市场](/models)会更直观。

## Endpoint

~~~http
GET /v1/models
~~~

## 请求示例

~~~bash
curl https://your-domain.com/v1/models \
  -H "Authorization: Bearer sk-your-api-key"
~~~

## 按模型查看 API 文档

模型列表接口只返回基础信息。每个模型的参数支持、能力标签、请求示例和注意事项，请进入[模型 API 文档](/docs/api-models)查看。

进入后可以搜索模型名称，点击任意模型打开它自己的 API 文档页面。这里会比 '/v1/models' 更适合阅读，因为它会把参数、示例和注意事项整理好。

## 返回信息

不同站点配置可能不同，常见字段包括：

- 模型 ID：请求里的 'model' 就填它。
- 模型名称：页面展示用的名称。
- 上下文长度：一次请求大概能放多少内容。
- 能力标签：例如图片、JSON、搜索、思考等。
- 状态：是否可用。

调用 '/v1/chat/completions' 时，请使用模型 ID，不要手打展示名称。

## 没有模型怎么办

1. 确认 API Key 有效。
2. 确认账号余额充足。
3. 打开[模型市场](/models)，确认页面上是否有可用模型。
4. 如果网页有模型但接口返回为空，请联系客服排查账号权限。`

const contentOpenAICompatibleClients = `## 通用配置项

大多数第三方客户端只需要三项信息：

| 配置项 | 填写内容 |
| --- | --- |
| API 类型 | OpenAI Compatible 或 OpenAI |
| Base URL | 'https://your-domain.com/v1' |
| API Key | 'sk-your-api-key' |

如果客户端要求填写完整接口地址，请填写 'https://your-domain.com/v1/chat/completions'；如果要求填写 Base URL，请只填到 '/v1'。

## 配置步骤

1. 在 TokenHub 创建 API Key。
2. 打开第三方客户端的模型或接口设置。
3. 选择 OpenAI Compatible。
4. 填入 Base URL 和 API Key。
5. 刷新或手动添加模型 ID。
6. 发送一条测试消息。

## 常见字段名称

不同客户端命名不同，但含义相同：

- Base URL、API Base、Endpoint、Proxy URL：填写 '/v1' 地址。
- API Key、Token、Secret Key：填写 'sk-' 开头的 Key。
- Model、Model ID、Deployment Name：填写 TokenHub 模型 ID。

## 安全提醒

优先在服务端或本地客户端保存 API Key。公共网页、共享配置和团队截图中不要暴露完整 Key。`

const contentChatClients = `## LobeChat

1. 打开 LobeChat 设置。
2. 进入模型或 OpenAI 设置。
3. API Key 填写 TokenHub 的 'sk-' Key。
4. API 代理地址或 Base URL 填写 'https://your-domain.com/v1'。
5. 保存后刷新模型列表，或手动添加模型 ID。
6. 新建会话发送测试消息。

## Open WebUI

1. 进入 Open WebUI 管理设置。
2. 打开 Connections 或模型连接页面。
3. 新增 OpenAI 连接。
4. URL 填写 'https://your-domain.com/v1'。
5. API Key 填写 TokenHub API Key。
6. 点击测试连接并保存。

## NextChat

1. 打开设置页面。
2. 接口地址填写站点域名，或按客户端要求填写 '/v1' 地址。
3. API Key 填写 TokenHub API Key。
4. 在自定义模型中添加 TokenHub 模型 ID。
5. 保存后发起测试对话。

## 排查建议

如果客户端提示连接失败，先用 curl 测试同一个 Key 和 Base URL。curl 成功但客户端失败时，通常是 Base URL 填写层级不一致。`

const contentDevClients = `## Cursor

1. 打开 Cursor 设置。
2. 找到 Models 或 OpenAI Compatible 配置。
3. API Key 填写 TokenHub API Key。
4. Base URL 填写 'https://your-domain.com/v1'。
5. 手动添加需要使用的模型 ID。
6. 保存后在聊天窗口发送测试问题。

## Continue

在 Continue 配置中添加 OpenAI 兼容模型：

~~~json
{
  "models": [
    {
      "title": "TokenHub",
      "provider": "openai",
      "model": "gpt-4o-mini",
      "apiKey": "sk-your-api-key",
      "apiBase": "https://your-domain.com/v1"
    }
  ]
}
~~~

## 使用建议

开发客户端可能会连续发起多次请求。建议：

- 单独创建一个开发客户端专用 Key。
- 先选择成本可控的模型测试。
- 在账单页观察调用频率和 Token 消耗。
- 重要项目开启日志记录，保存 Request ID。`

const contentErrorCodes = `## 常见 HTTP 状态码

| 状态码 | 含义 | 处理方式 |
| --- | --- | --- |
| 400 | 请求参数错误 | 检查 JSON、model、messages 和参数类型 |
| 401 | 认证失败 | 检查 API Key 和 Authorization Header |
| 402 | 余额不足 | 充值或联系客服补充额度 |
| 403 | 无权限 | 确认账号或 Key 是否允许调用该模型 |
| 404 | 路径或资源不存在 | 检查 URL、模型 ID 或文档路径 |
| 429 | 请求过快 | 降低频率，稍后重试 |
| 500 | 平台内部错误 | 保留 Request ID 并联系支持 |
| 502/503 | 模型服务暂时不可用 | 稍后重试或切换模型 |

## 认证失败

检查以下内容：

1. Header 是否为 'Authorization: Bearer sk-your-api-key'。
2. Key 是否完整，没有空格和换行。
3. Key 是否已删除。
4. 请求是否发到了正确域名。

## 余额不足

1. 进入余额页确认可用余额。
2. 查看是否有未完成的充值订单。
3. 充值后重试请求。

## 模型不可用

1. 检查模型 ID。
2. 到[模型市场](/models)确认模型状态。
3. 尝试同类其他模型。
4. 保留错误响应和 Request ID。`

const contentFAQ = `## API Key 创建后还能再次查看完整内容吗？

不能。完整 API Key 只在创建时显示一次。如果忘记保存，请删除旧 Key 并创建新 Key。

## Base URL 应该填什么？

大多数 SDK 和客户端填写 'https://your-domain.com/v1'。如果工具要求填写完整接口地址，再填写 'https://your-domain.com/v1/chat/completions'。

## Playground 会扣费吗？

会。Playground 发起的是真实模型请求，会按模型用量扣减余额或体验额度。

## 为什么同一段提示词每次回复不同？

模型生成具有随机性。降低 'temperature'，并固定提示词和上下文，可以让输出更稳定。

## 如何降低费用？

选择合适模型、减少不必要上下文、设置 'max_tokens'、避免重复发送大段历史消息，并按项目拆分 API Key 观察成本。

## 需要填写其它平台的密钥吗？

不需要。用户只使用 TokenHub API Key。不要在请求里填写其它平台的密钥、签名或 Authorization。

## 遇到问题应该提供什么信息？

请提供请求时间、模型 ID、Request ID、错误码和必要的请求摘要。不要发送完整 API Key。`
