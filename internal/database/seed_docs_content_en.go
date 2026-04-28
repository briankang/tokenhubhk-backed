package database

var userDocArticlesEN = []docArticleDef{
	{CatSlug: "getting-started", Locale: "en", Title: "Quick Start", Slug: "quick-start", Summary: "Register, top up, create an API key, and make your first model request.", Tags: "getting started,register,API Key,first call", Sort: 10, Content: contentQuickStartEN},
	{CatSlug: "account-billing", Locale: "en", Title: "Registration, Login, and Account Security", Slug: "account-security", Summary: "How to create an account, sign in, and keep your credentials safe.", Tags: "account,login,security,OAuth", Sort: 10, Content: contentAccountSecurityEN},
	{CatSlug: "account-billing", Locale: "en", Title: "Create and Manage API Keys", Slug: "api-keys", Summary: "Create, store, rotate, and delete API keys safely.", Tags: "API Key,secret,security", Sort: 20, Content: contentApiKeysEN},
	{CatSlug: "account-billing", Locale: "en", Title: "Top-ups, Balance, and Bills", Slug: "balance", Summary: "Understand credit top-ups, balance deductions, trial credits, and billing records.", Tags: "top up,balance,billing,credits", Sort: 30, Content: contentBalanceEN},
	{CatSlug: "models-pricing", Locale: "en", Title: "Browse and Choose Models", Slug: "choose-models", Summary: "Filter models by use case, context window, capability, and price.", Tags: "models,pricing,capabilities,context", Sort: 10, Content: contentChooseModelsEN},
	{CatSlug: "models-pricing", Locale: "en", Title: "Pricing and Usage Rules", Slug: "pricing-usage", Summary: "How input tokens, output tokens, streaming, failed requests, and balance deductions work.", Tags: "pricing,usage,tokens,billing", Sort: 20, Content: contentPricingUsageEN},
	{CatSlug: "playground", Locale: "en", Title: "Debug Models in Playground", Slug: "playground", Summary: "Select models, adjust parameters, send messages, and inspect request results in the browser.", Tags: "Playground,debug,parameters,request", Sort: 10, Content: contentPlaygroundEN},
	{CatSlug: "playground", Locale: "en", Title: "Advanced Parameters and Custom Passthrough", Slug: "custom-params", Summary: "Use temperature, top_p, extra_body, and custom_params safely.", Tags: "advanced parameters,extra_body,custom_params", Sort: 20, Content: contentCustomParamsEN},
	{CatSlug: "api-usage", Locale: "en", Title: "Authentication", Slug: "authentication", Summary: "The difference between web login tokens and model-call API keys.", Tags: "auth,Bearer,JWT,API Key", Sort: 10, Content: contentAuthEN},
	{CatSlug: "api-usage", Locale: "en", Title: "Chat Completions API", Slug: "chat-api", Summary: "Call /v1/chat/completions with regular or streaming responses.", Tags: "Chat API,OpenAI,stream", Sort: 20, Content: contentChatAPIEN},
	{CatSlug: "api-usage", Locale: "en", Title: "List Models", Slug: "models-api", Summary: "Use /v1/models to get the models available to your account.", Tags: "models,/v1/models", Sort: 30, Content: contentModelsAPIEN},
	{CatSlug: "client-integration", Locale: "en", Title: "Generic OpenAI-Compatible Setup", Slug: "openai-compatible-clients", Summary: "Configure TokenHub as an OpenAI-compatible service in third-party clients.", Tags: "clients,OpenAI compatible,Base URL", Sort: 10, Content: contentOpenAICompatibleClientsEN},
	{CatSlug: "client-integration", Locale: "en", Title: "LobeChat / Open WebUI Setup", Slug: "chat-clients", Summary: "Base URL, API key, and model configuration steps for common chat clients.", Tags: "LobeChat,Open WebUI,NextChat", Sort: 20, Content: contentChatClientsEN},
	{CatSlug: "client-integration", Locale: "en", Title: "Cursor / Continue Setup", Slug: "dev-clients", Summary: "Use TokenHub's unified chat API from development clients.", Tags: "Cursor,Continue,developer clients", Sort: 30, Content: contentDevClientsEN},
	{CatSlug: "help", Locale: "en", Title: "Error Codes and Troubleshooting", Slug: "error-codes", Summary: "Troubleshoot authentication, insufficient balance, rate limits, and unavailable models.", Tags: "errors,troubleshooting,401,402,429", Sort: 10, Content: contentErrorCodesEN},
	{CatSlug: "help", Locale: "en", Title: "FAQ", Slug: "faq", Summary: "Answers to common user questions.", Tags: "FAQ,help,questions", Sort: 20, Content: contentFAQEN},
}

const contentQuickStartEN = `## Who this is for

Use this guide if you are new to TokenHub. After these steps, you can test a model in the browser or call models from your own application.

## 1. Register an account

1. Open the home page and click Get Started or Register.
2. Enter your email, password, and verification code.
3. If Google or GitHub login is enabled, you can also authorize with a third-party account.
4. After registration, open the console and confirm your account name or email.

## 2. Check your balance

1. Open Balance or Billing in the console.
2. Check available balance and trial credits.
3. If balance is low, complete a top-up before making API calls.

## 3. Create an API key

1. Open API Keys.
2. Click Create Key.
3. Use a recognizable name such as local-test or production.
4. Copy the full key immediately. It is only shown once.

## 4. Test in Playground

1. Open Playground.
2. Select a model.
3. Enter a short message.
4. Send it and review the response, request ID, and usage.

## 5. Make an API request

~~~bash
curl -X POST https://your-domain.com/v1/chat/completions \
  -H "Content-Type: application/json" \
  -H "Authorization: Bearer sk-your-api-key" \
  -d '{
    "model": "gpt-4o-mini",
    "messages": [{"role": "user", "content": "Hello"}]
  }'
~~~

If you get 401, check the API key. If you get 402, check balance. If you get 429, reduce request frequency and retry later.`

const contentAccountSecurityEN = `## Login methods

TokenHub supports email registration and login. If the site enables Google or GitHub login, you can use those providers as well.

## Security recommendations

- Do not share accounts.
- Do not paste API keys into screenshots, chats, or public repositories.
- Create separate API keys for different projects or environments.
- If a key may have leaked, delete it and create a new one immediately.

## Sign out

Use the account menu in the top-right corner. On shared computers, sign out and clear sensitive browser data after use.`

const contentApiKeysEN = `## What API keys are for

API keys authenticate model calls such as '/v1/chat/completions' and '/v1/models'. Web login tokens are separate and should not be used as model API keys.

## Create a key

1. Sign in to the console.
2. Open API Keys.
3. Click Create Key.
4. Name it after the project and environment.
5. Copy the full key and store it securely.

## Store a key

~~~bash
TOKENHUB_API_KEY=sk-your-api-key
TOKENHUB_BASE_URL=https://your-domain.com/v1
~~~

Do not put keys in frontend code, mobile apps, public repositories, or user-visible configuration files.

## Rotate a key

1. Create a new key.
2. Update your application configuration.
3. Confirm calls succeed.
4. Delete the old key.`

const contentBalanceEN = `## Balance types

The platform may show paid balance and trial credits. Trial credits are usually consumed first, then paid balance.

## Top up

1. Open Billing or Balance.
2. Select an amount.
3. Choose an enabled payment method.
4. Complete payment and return to the platform.
5. Refresh the balance page and check the record status.

## View bills

Use the billing page to compare model, token usage, amount, and request ID. Keep the request ID when asking support to investigate a billing question.

## Insufficient balance

Model calls may return 402 when balance is insufficient. Top up or contact support, then retry.`

const contentChooseModelsEN = `## Where should I look first?

Open the [model market](/models). This is the user-facing place to see what you can call right now. You do not need to care how the model is routed behind the scenes.

Once you are there, read the page in this order:

1. Search by model name, use case, or capability, such as "long context", "image", "code", or "low cost".
2. Check the model status. Use available models for production requests.
3. Check price. Input and output are usually priced separately, and long answers cost more.
4. Check context length. Larger context means the request can carry more conversation history or document text.
5. Check capability tags, such as image input, JSON output, web search, or reasoning.

## Not sure which model to choose?

Start with a stable, affordable model. If the output quality is not enough, then move up. You do not need to start with the most expensive model on day one.

| Your scenario | What to check first |
| --- | --- |
| Support chat or daily Q&A | Cost, response speed, language quality |
| Long article or conversation summary | Context length and max output |
| Coding tasks | Code ability, reasoning ability, context length |
| Image or multimodal input | Image input capability tag |
| Production integration | Availability, price, latency, and failure rate |

## The model ID matters

API calls use the model ID, not only the display name. The safest flow is:

1. Find the model in the [model market](/models).
2. Open or expand the model detail.
3. Copy the model ID shown on the page.
4. Put that ID in the 'model' field of your API request.

If you fetch models through the API, you can also copy the model ID from '/v1/models'. To check model-specific parameters, open [Model API Docs](/docs/api-models), search the model, and read that model's own document.

## What if a model is unavailable?

If a request says the model does not exist, is unavailable, or is not allowed, go through this quick checklist:

1. Open the [model market](/models) and confirm the model is still listed and available.
2. Check the 'model' field for capitalization, spaces, or version typos.
3. Try another model of the same type to see whether the issue is limited to one model.
4. Keep the error response and Request ID. Support can investigate much faster with that ID.`

const contentPricingUsageEN = `## First: what is a token?

Text models are usually billed by tokens. You can think of tokens as small pieces the model reads and writes. Your prompt creates input tokens, and the model answer creates output tokens.

In practice:

- Longer prompts use more input tokens.
- Longer answers use more output tokens.
- Carrying a full chat history every time increases cost.
- Images, documents, and long context usually increase usage too.

## Where do I check price?

Open the [model market](/models), then find the model you plan to use. Pay attention to:

1. Input price: how the content you send is billed.
2. Output price: how the answer is billed.
3. Context length: how much content one request can carry.
4. Max output: how long one answer can be.

Before production, test a few real prompts in [Playground](/playground). It gives you a much better feel for response quality and rough token usage than a tiny "hello" test.

## Does streaming cost money?

Yes. Streaming only changes how the answer is delivered. The model is still generating tokens, so usage is still recorded.

If a user stops a stream early, usage is usually recorded for the part already generated. Your balance page and usage records are the final source of truth.

## What usually does not create model cost?

These requests normally stop before a real model call:

- Wrong API key causing 401.
- Insufficient balance causing 402.
- Invalid JSON causing 400.
- Wrong model ID rejected by the platform.

If the model has already generated an answer, usage may be recorded even if your application later ignores that answer.

## How to keep cost under control

1. Set a reasonable 'max_tokens' instead of letting the model write forever.
2. Send only the context needed for the current task.
3. Summarize old conversation turns instead of resending everything.
4. Use separate API keys for production, testing, and third-party clients.
5. Start with lower-cost models, then upgrade only when quality requires it.`

const contentPlaygroundEN = `## What Playground does

[Playground](/playground) is the quickest place to test a model before writing code. It uses your account balance and makes real model calls, so the result is close to what your API integration will see.

## Send a message

1. Open [Playground](/playground).
2. Select a model. If you are not sure which one to use, check the [model market](/models) first.
3. Enter a realistic user message. A real task is more useful than just typing "hello".
4. Send it.
5. Review the answer, Request ID, and usage.

## Adjust parameters

| Parameter | Purpose | Suggestion |
| --- | --- | --- |
| temperature | Randomness | Use lower values for precise tasks |
| top_p | Sampling range | Keep default unless you know why to change it |
| max_tokens | Maximum output length | Set a task-appropriate limit |
| stream | Streaming response | Recommended for chat interfaces |

## After testing

Copy the model ID, messages, and useful parameters into your server code. Keep the Request ID when something looks wrong; it makes troubleshooting much faster.`

const contentCustomParamsEN = `## Standard parameters first

Prefer standard OpenAI-compatible parameters:

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

## Custom passthrough

When a model supports extra options, put them in 'extra_body' or 'custom_params'. TokenHub processes standard parameters first, then merges custom fields.

~~~json
{
  "model": "your-model",
  "messages": [{"role": "user", "content": "Search today's news"}],
  "extra_body": {
    "enable_search": true
  }
}
~~~

Do not override standard fields such as 'model', 'messages', or 'stream'. Never pass keys, signatures, or Authorization headers from any other platform in user requests.`

const contentAuthEN = `## Two authentication types

| Scenario | Credential | Purpose |
| --- | --- | --- |
| Web console | Login session token | View pages, bills, and manage keys |
| Model API | API key | Call '/v1/chat/completions' and '/v1/models' |

## Model-call authentication

~~~http
Authorization: Bearer sk-your-api-key
Content-Type: application/json
~~~

## Troubleshooting

1. Header name must be 'Authorization'.
2. Format must be 'Bearer sk-...' with one space.
3. Remove accidental spaces, line breaks, or quotes.
4. Confirm the key has not been deleted.
5. Confirm the request uses the current site's '/v1' base URL.`

const contentChatAPIEN = `## Endpoint

~~~http
POST /v1/chat/completions
~~~

## Non-streaming request

~~~bash
curl -X POST https://your-domain.com/v1/chat/completions \
  -H "Authorization: Bearer sk-your-api-key" \
  -H "Content-Type: application/json" \
  -d '{
    "model": "gpt-4o-mini",
    "messages": [
      {"role": "system", "content": "You are a helpful assistant."},
      {"role": "user", "content": "Explain model APIs in three sentences."}
    ],
    "temperature": 0.7,
    "max_tokens": 800
  }'
~~~

## Streaming

Set 'stream' to true. The response uses SSE chunks until '[DONE]'.

## Response fields

Successful responses usually include 'id', 'choices', 'usage', and 'model'. In production, record request ID, model, usage, and your business user ID for reconciliation.`

const contentModelsAPIEN = `## List Models Endpoint

~~~http
GET /v1/models
~~~

## Example

~~~bash
curl https://your-domain.com/v1/models \
  -H "Authorization: Bearer sk-your-api-key"
~~~

## Purpose

'/v1/models' returns models available to your account. Third-party clients often use it to refresh their model selector.

If you just want to browse models, prices, and capabilities by hand, the [model market](/models) is easier to read.

## Per-model API docs

The model list only returns callable models and basic metadata. For parameter support, capability tags, request examples, and model-specific notes, open the [Model API Docs](/docs/api-models). Search for a model and click it to open that model's dedicated API document.

## No models returned

1. Confirm the API key is valid.
2. Confirm your balance is sufficient.
3. Check the [model market](/models) for available models.
4. If the web page has models but the API returns none, contact support to check account permissions.`

const contentOpenAICompatibleClientsEN = `## Common settings

| Setting | Value |
| --- | --- |
| Service type | OpenAI Compatible or OpenAI |
| Base URL | 'https://your-domain.com/v1' |
| API Key | 'sk-your-api-key' |

If a client asks for a full endpoint, use 'https://your-domain.com/v1/chat/completions'. If it asks for Base URL, stop at '/v1'.

## Steps

1. Create a TokenHub API key.
2. Open model or API settings in the client.
3. Choose OpenAI Compatible.
4. Enter Base URL and API key.
5. Refresh models or manually add model IDs.
6. Send a test message.`

const contentChatClientsEN = `## LobeChat

1. Open LobeChat settings.
2. Open model or OpenAI settings.
3. Use your TokenHub 'sk-' key.
4. Set API proxy or Base URL to 'https://your-domain.com/v1'.
5. Save, refresh models, or manually add a model ID.
6. Start a test chat.

## Open WebUI

1. Open admin settings.
2. Open Connections or model connection settings.
3. Add an OpenAI connection.
4. Set URL to 'https://your-domain.com/v1'.
5. Use your TokenHub API key.
6. Test and save.

If curl works but a client fails, the Base URL level is usually incorrect.`

const contentDevClientsEN = `## Cursor

1. Open Cursor settings.
2. Find Models or OpenAI Compatible settings.
3. Enter your TokenHub API key.
4. Set Base URL to 'https://your-domain.com/v1'.
5. Add the model IDs you want to use.
6. Save and send a test prompt.

## Continue

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

Development clients can make many background requests. Use a dedicated key and monitor billing after setup.`

const contentErrorCodesEN = `## Common HTTP status codes

| Status | Meaning | What to do |
| --- | --- | --- |
| 400 | Bad request | Check JSON, model, messages, and parameter types |
| 401 | Authentication failed | Check API key and Authorization header |
| 402 | Insufficient balance | Top up or contact support |
| 403 | No permission | Confirm the account can access this model |
| 404 | Wrong endpoint or model | Check Base URL and model ID |
| 429 | Rate limited | Lower request frequency and retry later |
| 500/502/503 | Service error | Retry later and keep request ID |

Always keep the request ID when asking support to investigate.`

const contentFAQEN = `## Can I use the official OpenAI SDK?

Yes. Set the API key to your TokenHub key and set base_url to 'https://your-domain.com/v1'.

## Should I use web login token for model calls?

No. Model calls use API keys beginning with 'sk-'.

## Why is the charge different from my estimate?

Final cost depends on actual input tokens, generated output tokens, model price, and any platform pricing rules.

## Do I need keys from another platform?

No. User requests only need a TokenHub API key. Do not put keys, signatures, or Authorization headers from another platform into your request.

## Where do I find model-specific parameters?

Open [Model API Docs](/docs/api-models), search for the target model, and view its dedicated API page.`
