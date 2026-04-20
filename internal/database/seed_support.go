package database

import (
	"fmt"

	"go.uber.org/zap"
	"gorm.io/gorm"

	"tokenhub-server/internal/model"
	"tokenhub-server/internal/pkg/logger"
)

// RunSeedSupport 填充 AI 客服系统种子数据
// 幂等：分别检查各张表，非空则跳过对应部分
// 覆盖：SupportModelProfile (4) + HotQuestion (5) + ProviderDocReference (8) + FAQ -> KnowledgeChunk 不在此种（由首次重建任务生成）
//
// 所有种子内容支持动态占位符 {{ref:namespace.key}}，由 DynamicValueResolver 在 RAG 召回后运行时解析
// 例如 {{ref:referral.commission_rate_pct}} -> "10" (从 referral_configs 表实时读取)
func RunSeedSupport(db *gorm.DB) {
	logger.L.Info("seed_support: 开始填充 AI 客服系统种子数据...")

	if err := db.Transaction(func(tx *gorm.DB) error {
		if err := seedSupportModelProfiles(tx); err != nil {
			return fmt.Errorf("模型配置: %w", err)
		}
		if err := seedSupportProviderDocs(tx); err != nil {
			return fmt.Errorf("供应商文档: %w", err)
		}
		if err := seedSupportHotQuestions(tx); err != nil {
			return fmt.Errorf("热门问题: %w", err)
		}
		return nil
	}); err != nil {
		logger.L.Error("seed_support: 事务失败", zap.Error(err))
		return
	}

	logger.L.Info("seed_support: AI 客服系统种子数据填充完成")
}

// seedSupportModelProfiles 种子客服候选模型
// 用户选择 glm-4 作主力，qwen-plus 降级，kimi-k2 紧急兜底
func seedSupportModelProfiles(tx *gorm.DB) error {
	var count int64
	if err := tx.Model(&model.SupportModelProfile{}).Count(&count).Error; err != nil {
		return err
	}
	if count > 0 {
		logger.L.Info("seed_support: support_model_profiles 非空，跳过")
		return nil
	}

	profiles := []model.SupportModelProfile{
		{
			ModelKey:       "glm-4",
			DisplayName:    "智谱 GLM-4（主力）",
			Priority:       100,
			IsActive:       true,
			MaxTokens:      1024,
			Temperature:    0.3,
			EnableSearch:   true,
			EnableThinking: false,
			BudgetLevel:    "normal",
			Notes:          "主力客服模型，128K 上下文，原生联网搜索（tools 嵌套）",
		},
		{
			ModelKey:       "qwen-plus",
			DisplayName:    "通义千问 Plus（降级）",
			Priority:       80,
			IsActive:       true,
			MaxTokens:      1024,
			Temperature:    0.3,
			EnableSearch:   true,
			EnableThinking: false,
			BudgetLevel:    "economy",
			Notes:          "预算吃紧时降级使用，¥0.76/M，enable_search 直通",
		},
		{
			ModelKey:       "kimi-k2",
			DisplayName:    "Kimi K2（备用）",
			Priority:       60,
			IsActive:       true,
			MaxTokens:      1024,
			Temperature:    0.3,
			EnableSearch:   true,
			EnableThinking: false,
			BudgetLevel:    "economy",
			Notes:          "200K 超长上下文备用，use_search 参数",
		},
		{
			ModelKey:       "qwen-turbo",
			DisplayName:    "通义千问 Turbo（紧急/翻译）",
			Priority:       40,
			IsActive:       true,
			MaxTokens:      512,
			Temperature:    0.2,
			EnableSearch:   false,
			EnableThinking: false,
			BudgetLevel:    "emergency",
			Notes:          "紧急兜底 + translator 服务内部使用",
		},
	}
	if err := tx.Create(&profiles).Error; err != nil {
		return err
	}
	logger.L.Info("seed_support: 创建 support_model_profiles", zap.Int("count", len(profiles)))
	return nil
}

// seedSupportProviderDocs 种子供应商官方文档 URL 引用
// AI 回答涉及特定供应商细节时，在答案中引用这些 URL
func seedSupportProviderDocs(tx *gorm.DB) error {
	var count int64
	if err := tx.Model(&model.ProviderDocReference{}).Count(&count).Error; err != nil {
		return err
	}
	if count > 0 {
		logger.L.Info("seed_support: provider_doc_references 非空，跳过")
		return nil
	}

	// 查询主要供应商 ID（若不存在则跳过对应条目）
	type supLite struct {
		ID   uint
		Code string
	}
	var sups []supLite
	if err := tx.Raw("SELECT id, code FROM suppliers WHERE code IN (?, ?, ?, ?, ?) AND is_active = 1",
		"aliyun_dashscope", "volcengine", "qianfan", "hunyuan", "zhipu").Scan(&sups).Error; err != nil {
		return err
	}
	supMap := make(map[string]uint)
	for _, s := range sups {
		supMap[s.Code] = s.ID
	}

	type docDef struct {
		Code, DocType, Title, URL, Desc, Keywords string
		Priority                                  int
	}
	defs := []docDef{
		// 阿里云百炼
		{"aliyun_dashscope", "api_reference", "阿里云百炼 API 参考",
			"https://help.aliyun.com/zh/model-studio/developer-reference/",
			"通义千问系列 API 参数、错误码、流式调用细节",
			"qwen,通义千问,百炼,dashscope,qwen-plus,qwen-turbo,qwen-max", 10},
		{"aliyun_dashscope", "pricing", "阿里云百炼定价页",
			"https://help.aliyun.com/zh/model-studio/models#5c44fdcee4b87",
			"通义千问模型按 tokens 计费、缓存折扣规则",
			"qwen价格,通义千问计费,百炼定价,dashscope价格", 10},
		// 火山引擎
		{"volcengine", "api_reference", "火山引擎方舟 API 参考",
			"https://www.volcengine.com/docs/82379/1099455",
			"豆包系列 API 参数、流式响应、Function Calling",
			"doubao,豆包,火山引擎,方舟,volcengine,seed", 10},
		{"volcengine", "pricing", "火山引擎方舟计费说明",
			"https://www.volcengine.com/docs/82379/1099320",
			"豆包系列计费、阶梯价格、缓存命中折扣",
			"doubao价格,豆包计费,方舟定价,火山价格", 10},
		// 百度千帆
		{"qianfan", "api_reference", "百度千帆 ERNIE API 参考",
			"https://cloud.baidu.com/doc/WENXINWORKSHOP/s/klqdb51pi",
			"文心一言系列 API 参数、错误码",
			"ernie,文心一言,千帆,百度,qianfan", 5},
		// 腾讯混元
		{"hunyuan", "api_reference", "腾讯混元 API 参考",
			"https://cloud.tencent.com/document/product/1729/101848",
			"混元系列 API 参数、鉴权、流式响应",
			"hunyuan,混元,腾讯", 5},
		// 智谱 GLM
		{"zhipu", "api_reference", "智谱 BigModel API 参考",
			"https://bigmodel.cn/dev/api/normal-model/glm-4",
			"GLM-4 系列 API 参数、tools 嵌套联网搜索",
			"glm,glm-4,智谱,bigmodel,zhipu,chatglm", 10},
		{"zhipu", "pricing", "智谱 BigModel 价格页",
			"https://bigmodel.cn/pricing",
			"GLM 系列模型价格、阶梯与缓存定价",
			"glm价格,智谱计费,bigmodel定价", 5},
	}

	rows := make([]model.ProviderDocReference, 0, len(defs))
	for _, d := range defs {
		supID, ok := supMap[d.Code]
		if !ok {
			continue // 该供应商尚未入库，跳过
		}
		rows = append(rows, model.ProviderDocReference{
			SupplierID:   supID,
			SupplierCode: d.Code,
			DocType:      d.DocType,
			Title:        d.Title,
			URL:          d.URL,
			Description:  d.Desc,
			Keywords:     d.Keywords,
			Locale:       "zh",
			Priority:     d.Priority,
			IsActive:     true,
		})
	}
	if len(rows) == 0 {
		logger.L.Info("seed_support: 无匹配的供应商，provider_doc_references 跳过")
		return nil
	}
	if err := tx.Create(&rows).Error; err != nil {
		return err
	}
	logger.L.Info("seed_support: 创建 provider_doc_references", zap.Int("count", len(rows)))
	return nil
}

// seedSupportHotQuestions 种子 5 条热门问题 + 标准答案
// 使用 {{ref:xxx}} 占位符，RAG 召回后由 DynamicValueResolver 实时替换为当前配置值
// 管理员可在「热门问题管理」中修改 / 新增
func seedSupportHotQuestions(tx *gorm.DB) error {
	var count int64
	if err := tx.Model(&model.HotQuestion{}).Count(&count).Error; err != nil {
		return err
	}
	if count > 0 {
		logger.L.Info("seed_support: hot_questions 非空，跳过")
		return nil
	}

	// AuthorID = 1（默认 admin 用户，seed.go 已保证存在）
	const adminID = uint(1)

	hq := []model.HotQuestion{
		{
			Title: "如何获取 API Key？",
			QuestionBody: "怎么创建 API Key、在哪里查看 API Key、API Key 怎么使用、API Key 是什么、" +
				"如何申请 API Key、新建密钥、查看密钥、复制密钥。",
			CuratedAnswer: `## 获取 API Key 的步骤

1. 登录 TokenHub 控制台
2. 进入「Dashboard → API Keys」
3. 点击「新建密钥」按钮
4. 输入密钥名称（例如 "生产环境"）并选择权限范围
5. 创建成功后在列表中点击「眼睛」图标查看完整密钥

> ⚠️ **请妥善保管密钥**：密钥使用 AES-256-GCM 加密存储，仅本人可解密查看。

### 调用示例
` + "```bash\n" +
				`curl -X POST https://www.tokenhubhk.com/v1/chat/completions \
  -H "Authorization: Bearer YOUR_API_KEY" \
  -H "Content-Type: application/json" \
  -d '{"model":"glm-4","messages":[{"role":"user","content":"你好"}]}'` + "\n```",
			Category:    "account",
			Tags:        "api_key,密钥,创建,查看",
			Priority:    15,
			IsPublished: true,
			AuthorID:    adminID,
		},
		{
			Title: "计费规则和积分换算？",
			QuestionBody: "积分怎么算、多少钱一个 token、人民币怎么换算积分、费用怎么计算、" +
				"怎么收费、按什么计费、成本怎么算、价格单位。",
			CuratedAnswer: `## 计费规则

### 积分体系
- **1 RMB = 10,000 积分**
- 所有模型按 tokens 计费（输入 + 输出分开计价）
- 精度：6 位小数（每笔费用向下取整到整数积分）

### 价格示例（{{ref:pricing.model.glm-4.display}}）

具体模型价格请访问 [定价页面](/pricing) 查看。

### 缓存命中折扣
支持缓存的模型在命中缓存时享受折扣（0.1~0.5 倍原价），具体机制分为三种：
- **auto**：自动缓存（OpenAI / DeepSeek / 豆包）
- **explicit**：显式声明（Claude）
- **both**：两种机制都支持（阿里云 / 千帆）

### 会员折扣
{{ref:member.levels_summary}}

如对账单有疑问，请[提交工单](/dashboard/support/tickets)由人工核对。`,
			Category:    "billing",
			Tags:        "计费,积分,价格,RMB,tokens",
			Priority:    15,
			IsPublished: true,
			AuthorID:    adminID,
		},
		{
			Title: "邀请返佣规则？",
			QuestionBody: "邀请别人注册能赚多少、返佣比例、提现门槛、邀请奖励、" +
				"推荐返利、被邀请人充值怎么返、多久结算、最低提现。",
			CuratedAnswer: `## 邀请返佣规则

| 项目 | 当前值 |
|------|-------|
| 佣金比例 | **{{ref:referral.commission_rate_pct}}%** |
| 归因窗口 | {{ref:referral.attribution_days}} 天 |
| 解锁门槛（被邀者首充） | ≥ ¥{{ref:referral.min_paid_rmb}} |
| 终身佣金上限（单被邀者） | ¥{{ref:referral.lifetime_cap_rmb}} |
| 最低提现金额 | ¥{{ref:referral.min_withdraw_rmb}} |
| 结算周期 | {{ref:referral.settle_days}} 天（PENDING → SETTLED） |

### 三层注册奖励
- **注册基础赠送**：¥{{ref:quota.default_free_rmb}}（即时到账，不可提现）
- **被邀者首充奖励**：充值达 ¥{{ref:quota.invitee_unlock_rmb}} 后，发放 ¥{{ref:quota.invitee_bonus_rmb}}
- **邀请人奖励**：被邀者累计付费达 ¥{{ref:quota.inviter_unlock_rmb}} 后，邀请人获得 ¥{{ref:quota.inviter_bonus_rmb}}（可提现）

### 如何获取邀请链接
在「Dashboard → 推荐计划」页复制专属链接或邀请码。

⚠️ 邀请人月度领奖上限：{{ref:quota.inviter_monthly_cap}} 人次。`,
			Category:    "billing",
			Tags:        "邀请,返佣,佣金,推荐,提现",
			Priority:    20,
			IsPublished: true,
			AuthorID:    adminID,
		},
		{
			Title: "支持哪些 AI 模型？如何选择？",
			QuestionBody: "有什么模型、模型列表、推荐哪个模型、模型对比、哪个便宜、" +
				"哪个好用、中文最好、英文最好、代码能力、推理能力。",
			CuratedAnswer: `## 平台支持的主流模型

### 顶级对话
- **claude-3-5-sonnet** / **claude-3-opus**（Anthropic）— 综合能力标杆
- **gpt-4o** / **gpt-4o-mini**（OpenAI）— 全能型
- **gemini-2.0-pro**（Google）— 多模态强

### 高性价比中文
- **glm-4**（智谱）— 128K 上下文 + 联网搜索
- **qwen-plus** / **qwen-max**（阿里云）— 中文优化
- **doubao-pro**（火山引擎）— 便宜稳定
- **deepseek-chat**（深度求索）— 推理能力强

### 专业领域
- **deepseek-coder** — 代码生成
- **moonshot-v1-128k**（Kimi）— 超长文档处理

完整列表参考 [模型市场](/models)，每个模型标注了**输入/输出价格**、**上下文长度**、**是否支持缓存/联网搜索/函数调用**等标签。

### 选型建议
- **日常对话**：qwen-plus / doubao-pro（便宜稳定）
- **代码生成**：deepseek-coder / claude-3-5-sonnet
- **长文档**：kimi-k2 / glm-4
- **多模态**：gpt-4o / gemini

平台统一 OpenAI 兼容协议，一个 API Key 调用所有模型。`,
			Category:    "api",
			Tags:        "模型,选择,对比,推荐",
			Priority:    10,
			IsPublished: true,
			AuthorID:    adminID,
		},
		{
			Title: "充值到账后如何发票？退款政策？",
			QuestionBody: "充值、发票、退款、开发票、怎么开票、能不能退款、充错了、到账时间、" +
				"支付方式、微信支付、支付宝、Stripe、PayPal。",
			CuratedAnswer: `## 充值与到账

### 支持的支付方式
- **国内**：微信支付 V3、支付宝 RSA2
- **海外**：Stripe、PayPal

充值成功后积分**立即到账**，订单回调异常时 1-2 分钟内重试。

### 发票
个人版/企业版均可开具增值税电子发票。
**申请入口**：`+"`Dashboard → 余额 → 充值记录 → 申请发票`"+`

资料所需：
- 个人：抬头 + 邮箱
- 企业：抬头 + 税号 + 邮箱（一般 3 个工作日送达）

### 退款政策
- 充值错误 / 重复充值：48 小时内可申请全额退款
- 已消费部分不可退
- 账户违规冻结的不退款

**退款流程**：
1. 提交工单（category=billing，附订单号）
2. 客服审核（1-2 个工作日）
3. 原路退回支付渠道（微信/支付宝 1-3 天，Stripe/PayPal 3-7 天）

如有特殊情况请[提交工单](/dashboard/support/tickets)。`,
			Category:    "billing",
			Tags:        "充值,发票,退款,支付",
			Priority:    12,
			IsPublished: true,
			AuthorID:    adminID,
		},
	}

	if err := tx.Create(&hq).Error; err != nil {
		return err
	}
	logger.L.Info("seed_support: 创建 hot_questions", zap.Int("count", len(hq)))
	return nil
}
