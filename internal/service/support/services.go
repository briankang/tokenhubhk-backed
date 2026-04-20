package support

import (
	"context"
	"fmt"
	"strings"
	"time"

	"go.uber.org/zap"
	"gorm.io/gorm"

	"tokenhub-server/internal/model"
	"tokenhub-server/internal/pkg/logger"
)

// ================= SessionService =================

type SessionService struct {
	db *gorm.DB
}

func NewSessionService(db *gorm.DB) *SessionService {
	return &SessionService{db: db}
}

// GetOrCreate 根据 sessionID 加载；为 0 则新建
func (s *SessionService) GetOrCreate(ctx context.Context, userID uint, sessionID uint, locale string) (*model.SupportSession, error) {
	if sessionID > 0 {
		var sess model.SupportSession
		if err := s.db.WithContext(ctx).Where("id = ? AND user_id = ?", sessionID, userID).First(&sess).Error; err == nil {
			return &sess, nil
		}
	}
	sess := &model.SupportSession{
		UserID:    userID,
		Locale:    locale,
		Status:    "active",
		LastMsgAt: time.Now(),
	}
	if err := s.db.WithContext(ctx).Create(sess).Error; err != nil {
		return nil, err
	}
	return sess, nil
}

// List 我的会话列表
func (s *SessionService) List(ctx context.Context, userID uint, page, pageSize int) ([]model.SupportSession, int64, error) {
	if pageSize <= 0 || pageSize > 100 {
		pageSize = 20
	}
	if page < 1 {
		page = 1
	}
	q := s.db.WithContext(ctx).Where("user_id = ?", userID)
	var total int64
	q.Model(&model.SupportSession{}).Count(&total)
	var out []model.SupportSession
	err := q.Order("last_msg_at DESC").Offset((page - 1) * pageSize).Limit(pageSize).Find(&out).Error
	return out, total, err
}

// Close 标记会话已关闭
func (s *SessionService) Close(ctx context.Context, userID, sessionID uint) error {
	now := time.Now()
	return s.db.WithContext(ctx).Model(&model.SupportSession{}).
		Where("id = ? AND user_id = ?", sessionID, userID).
		Updates(map[string]any{"status": "closed", "closed_at": &now}).Error
}

// ScheduleMemoryExtract 触发异步记忆提取（预留 hook，Phase 1 可仅 log 不做）
func (s *SessionService) ScheduleMemoryExtract(ctx context.Context, sessionID uint) {
	logger.L.Debug("scheduled memory extract", zap.Uint("session_id", sessionID))
	// TODO(Phase2): taskqueue.Publish(TaskMemoryExtract, {session_id})
}

// ================= MessageService =================

type MessageService struct {
	db *gorm.DB
}

func NewMessageService(db *gorm.DB) *MessageService {
	return &MessageService{db: db}
}

// SaveUser 保存用户消息（content=原语言；contentZh=翻译版仅当 orig != zh）
func (ms *MessageService) SaveUser(ctx context.Context, sessionID uint, content, contentZh string) error {
	m := &model.SupportMessage{
		SessionID: sessionID,
		Role:      "user",
		Content:   content,
		ContentZh: contentZh,
	}
	if err := ms.db.WithContext(ctx).Create(m).Error; err != nil {
		return err
	}
	// 更新 session 的 msg_count 与 last_msg_at
	ms.db.WithContext(ctx).Model(&model.SupportSession{}).Where("id = ?", sessionID).
		Updates(map[string]any{
			"msg_count":   gorm.Expr("msg_count + 1"),
			"last_msg_at": time.Now(),
		})
	return nil
}

// SaveAssistant 保存 AI 回复
func (ms *MessageService) SaveAssistant(ctx context.Context, sessionID uint, content, modelID string, chunkIDs []uint, urls []string, needHuman bool) (uint, error) {
	docRefs := ""
	if len(chunkIDs) > 0 {
		parts := make([]string, len(chunkIDs))
		for i, id := range chunkIDs {
			parts[i] = fmt.Sprintf("%d", id)
		}
		docRefs = strings.Join(parts, ",")
	}
	urlsStr := strings.Join(urls, ",")
	m := &model.SupportMessage{
		SessionID: sessionID,
		Role:      "assistant",
		Content:   content,
		ModelID:   modelID,
		DocRefs:   docRefs,
		ExternalUrls: urlsStr,
		NeedHuman: needHuman,
	}
	if err := ms.db.WithContext(ctx).Create(m).Error; err != nil {
		return 0, err
	}
	ms.db.WithContext(ctx).Model(&model.SupportSession{}).Where("id = ?", sessionID).
		Updates(map[string]any{
			"msg_count":   gorm.Expr("msg_count + 1"),
			"last_msg_at": time.Now(),
		})
	if needHuman {
		ms.db.WithContext(ctx).Model(&model.SupportSession{}).Where("id = ?", sessionID).
			Update("status", "escalated")
	}
	return m.ID, nil
}

// UpdateTokens 回填实际 tokens 消耗
func (ms *MessageService) UpdateTokens(ctx context.Context, msgID uint, tokensIn, tokensOut int) {
	ms.db.WithContext(ctx).Model(&model.SupportMessage{}).Where("id = ?", msgID).Updates(map[string]any{
		"tokens_in":  tokensIn,
		"tokens_out": tokensOut,
	})
}

// RecentForPrompt 拉最近 N 轮对话用于 prompt 注入（返回 OpenAI 格式）
func (ms *MessageService) RecentForPrompt(ctx context.Context, sessionID uint, maxTurns int) []map[string]string {
	var msgs []model.SupportMessage
	if err := ms.db.WithContext(ctx).Where("session_id = ?", sessionID).
		Order("id DESC").Limit(maxTurns * 2).Find(&msgs).Error; err != nil {
		return nil
	}
	// 反转为时间升序
	for i, j := 0, len(msgs)-1; i < j; i, j = i+1, j-1 {
		msgs[i], msgs[j] = msgs[j], msgs[i]
	}
	out := make([]map[string]string, 0, len(msgs))
	for _, m := range msgs {
		out = append(out, map[string]string{"role": m.Role, "content": m.Content})
	}
	return out
}

// ListMessages 会话完整消息（按时间升序）
func (ms *MessageService) ListMessages(ctx context.Context, sessionID uint) ([]model.SupportMessage, error) {
	var out []model.SupportMessage
	err := ms.db.WithContext(ctx).Where("session_id = ?", sessionID).Order("id ASC").Find(&out).Error
	return out, err
}

// MarkAccepted 用户采纳标记（保证幂等）
func (ms *MessageService) MarkAccepted(ctx context.Context, sessionID, messageID uint) error {
	now := time.Now()
	return ms.db.WithContext(ctx).Model(&model.SupportMessage{}).
		Where("id = ? AND session_id = ? AND role = ?", messageID, sessionID, "assistant").
		Updates(map[string]any{"is_accepted": true, "accepted_at": &now}).Error
}

// ================= MemoryService =================

type MemoryService struct {
	db *gorm.DB
}

func NewMemoryService(db *gorm.DB) *MemoryService {
	return &MemoryService{db: db}
}

// Top 返回用户最相关的 N 条记忆
func (m *MemoryService) Top(ctx context.Context, userID uint, limit int) []model.UserSupportMemory {
	var out []model.UserSupportMemory
	if err := m.db.WithContext(ctx).
		Where("user_id = ? AND is_active = ?", userID, true).
		Where("expires_at IS NULL OR expires_at > NOW()").
		Order("relevance DESC, last_used_at DESC").
		Limit(limit).Find(&out).Error; err != nil {
		return nil
	}
	// 异步更新 last_used_at
	if len(out) > 0 {
		go func() {
			ids := make([]uint, 0, len(out))
			for _, mm := range out {
				ids = append(ids, mm.ID)
			}
			now := time.Now()
			m.db.Model(&model.UserSupportMemory{}).Where("id IN ?", ids).Update("last_used_at", &now)
		}()
	}
	return out
}

// CleanupExpired 清理过期记忆（cron 调用）
func (m *MemoryService) CleanupExpired(ctx context.Context) (int64, error) {
	res := m.db.WithContext(ctx).Where("expires_at IS NOT NULL AND expires_at < NOW()").Delete(&model.UserSupportMemory{})
	return res.RowsAffected, res.Error
}

// Deactivate60Days 60 天未用自动失活（不删除，便于恢复）
func (m *MemoryService) Deactivate60Days(ctx context.Context) (int64, error) {
	res := m.db.WithContext(ctx).Model(&model.UserSupportMemory{}).
		Where("is_active = ? AND (last_used_at IS NULL OR last_used_at < DATE_SUB(NOW(), INTERVAL 60 DAY))", true).
		Update("is_active", false)
	return res.RowsAffected, res.Error
}

// ================= ProviderDocService =================

type ProviderDocService struct {
	db *gorm.DB
}

func NewProviderDocService(db *gorm.DB) *ProviderDocService {
	return &ProviderDocService{db: db}
}

// MatchByQuery 基于 Keywords 字段做关键词匹配
// 简单实现：关键词命中数 + priority 加权排序
func (p *ProviderDocService) MatchByQuery(ctx context.Context, query string, limit int) []model.ProviderDocReference {
	if strings.TrimSpace(query) == "" {
		return nil
	}
	var all []model.ProviderDocReference
	if err := p.db.WithContext(ctx).Where("is_active = ?", true).Find(&all).Error; err != nil {
		return nil
	}
	lowerQ := strings.ToLower(query)

	type scored struct {
		ref   model.ProviderDocReference
		hits  int
		score int
	}
	var matched []scored
	for _, r := range all {
		hits := 0
		for _, kw := range strings.Split(r.Keywords, ",") {
			kw = strings.TrimSpace(strings.ToLower(kw))
			if kw == "" {
				continue
			}
			if strings.Contains(lowerQ, kw) {
				hits++
			}
		}
		if hits > 0 {
			matched = append(matched, scored{ref: r, hits: hits, score: hits*10 + r.Priority})
		}
	}
	// 按 score 降序
	for i := 0; i < len(matched); i++ {
		for j := i + 1; j < len(matched); j++ {
			if matched[j].score > matched[i].score {
				matched[i], matched[j] = matched[j], matched[i]
			}
		}
	}
	if limit > 0 && len(matched) > limit {
		matched = matched[:limit]
	}
	out := make([]model.ProviderDocReference, len(matched))
	for i, m := range matched {
		out[i] = m.ref
	}
	return out
}

// List 管理员列表（全部）
func (p *ProviderDocService) List(ctx context.Context) ([]model.ProviderDocReference, error) {
	var out []model.ProviderDocReference
	err := p.db.WithContext(ctx).Order("supplier_code ASC, priority DESC").Find(&out).Error
	return out, err
}

// Create/Update/Delete 管理操作
func (p *ProviderDocService) Create(ctx context.Context, r *model.ProviderDocReference) error {
	return p.db.WithContext(ctx).Create(r).Error
}

func (p *ProviderDocService) Update(ctx context.Context, r *model.ProviderDocReference) error {
	return p.db.WithContext(ctx).Save(r).Error
}

func (p *ProviderDocService) Delete(ctx context.Context, id uint) error {
	return p.db.WithContext(ctx).Delete(&model.ProviderDocReference{}, id).Error
}

// ================= HotQuestionService =================

type HotQuestionService struct {
	db *gorm.DB
}

func NewHotQuestionService(db *gorm.DB) *HotQuestionService {
	return &HotQuestionService{db: db}
}

// ListPublished 公开列表：仅返回 is_published=true 的问题；可选 category 过滤
func (h *HotQuestionService) ListPublished(ctx context.Context, category string) ([]model.HotQuestion, error) {
	q := h.db.WithContext(ctx).Where("is_published = ?", true)
	if strings.TrimSpace(category) != "" {
		q = q.Where("category = ?", category)
	}
	var out []model.HotQuestion
	if err := q.Order("priority DESC, hit_count DESC, id ASC").Limit(50).Find(&out).Error; err != nil {
		return nil, err
	}
	return out, nil
}

// Get 获取单条（仅已发布的）
func (h *HotQuestionService) Get(ctx context.Context, id uint) (*model.HotQuestion, error) {
	var hq model.HotQuestion
	err := h.db.WithContext(ctx).Where("id = ? AND is_published = ?", id, true).First(&hq).Error
	if err != nil {
		return nil, err
	}
	// 异步计数
	go h.db.Model(&model.HotQuestion{}).Where("id = ?", id).UpdateColumn("hit_count", gorm.Expr("hit_count + 1"))
	return &hq, nil
}

// ================= TicketService =================

type TicketService struct {
	db *gorm.DB
}

func NewTicketService(db *gorm.DB) *TicketService {
	return &TicketService{db: db}
}

// Create 新建工单
func (t *TicketService) Create(ctx context.Context, ticket *model.SupportTicket) error {
	if ticket.TicketNo == "" {
		ticket.TicketNo = generateTicketNo()
	}
	if ticket.Status == "" {
		ticket.Status = "pending"
	}
	if ticket.DueAt.IsZero() {
		ticket.DueAt = time.Now().Add(24 * time.Hour)
	}
	return t.db.WithContext(ctx).Create(ticket).Error
}

// ListMine 用户自己的工单
func (t *TicketService) ListMine(ctx context.Context, userID uint, status string, page, pageSize int) ([]model.SupportTicket, int64, error) {
	if pageSize <= 0 || pageSize > 100 {
		pageSize = 20
	}
	if page < 1 {
		page = 1
	}
	q := t.db.WithContext(ctx).Where("user_id = ?", userID)
	if status != "" {
		q = q.Where("status = ?", status)
	}
	var total int64
	q.Model(&model.SupportTicket{}).Count(&total)
	var out []model.SupportTicket
	err := q.Order("created_at DESC").Offset((page - 1) * pageSize).Limit(pageSize).Find(&out).Error
	return out, total, err
}

// GetByTicketNo 按单号查询（限用户）
func (t *TicketService) GetByTicketNo(ctx context.Context, userID uint, ticketNo string) (*model.SupportTicket, []model.SupportTicketReply, error) {
	var ticket model.SupportTicket
	if err := t.db.WithContext(ctx).Where("ticket_no = ? AND user_id = ?", ticketNo, userID).First(&ticket).Error; err != nil {
		return nil, nil, err
	}
	var replies []model.SupportTicketReply
	t.db.WithContext(ctx).Where("ticket_id = ? AND is_internal = ?", ticket.ID, false).
		Order("id ASC").Find(&replies)
	// 清除未读标记
	if ticket.UnreadByUser {
		t.db.WithContext(ctx).Model(&model.SupportTicket{}).Where("id = ?", ticket.ID).Update("unread_by_user", false)
	}
	return &ticket, replies, nil
}

// AppendReply 用户追加留言
func (t *TicketService) AppendReply(ctx context.Context, userID, ticketID uint, content string) error {
	// 确认所有权
	var ticket model.SupportTicket
	if err := t.db.WithContext(ctx).Where("id = ? AND user_id = ?", ticketID, userID).First(&ticket).Error; err != nil {
		return err
	}
	reply := &model.SupportTicketReply{
		TicketID:   ticketID,
		AuthorID:   userID,
		AuthorType: "user",
		Content:    content,
	}
	if err := t.db.WithContext(ctx).Create(reply).Error; err != nil {
		return err
	}
	// 状态更新：用户回复后 status→pending（等待管理员响应）
	t.db.WithContext(ctx).Model(&model.SupportTicket{}).Where("id = ?", ticketID).
		Updates(map[string]any{"status": "pending", "read_by_admin_at": nil})
	return nil
}

// ConfirmResolved 用户确认解决
func (t *TicketService) ConfirmResolved(ctx context.Context, userID, ticketID uint) error {
	now := time.Now()
	return t.db.WithContext(ctx).Model(&model.SupportTicket{}).
		Where("id = ? AND user_id = ?", ticketID, userID).
		Updates(map[string]any{"status": "closed", "resolved_at": &now}).Error
}

// Reopen 重新打开
func (t *TicketService) Reopen(ctx context.Context, userID, ticketID uint) error {
	return t.db.WithContext(ctx).Model(&model.SupportTicket{}).
		Where("id = ? AND user_id = ?", ticketID, userID).
		Updates(map[string]any{"status": "reopened", "resolved_at": nil}).Error
}

func generateTicketNo() string {
	now := time.Now()
	return fmt.Sprintf("T%04d%02d%02d%06d", now.Year(), now.Month(), now.Day(), now.UnixNano()%1000000)
}
