package announcement

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"go.uber.org/zap"
	"gorm.io/gorm"

	"tokenhub-server/internal/model"
	"tokenhub-server/internal/pkg/dbctx"
	pkgredis "tokenhub-server/internal/pkg/redis"
	"tokenhub-server/internal/service/usercache"
)

// Service 公告服务
type Service struct {
	db     *gorm.DB
	logger *zap.Logger
}

// NewService 创建公告服务实例
func NewService(db *gorm.DB) *Service {
	return &Service{
		db:     db,
		logger: zap.L(),
	}
}

// ========== 请求/响应结构体 ==========

// CreateRequest 创建公告请求
type CreateRequest struct {
	Title      string     `json:"title" binding:"required,max=255"`
	TitleEn    string     `json:"title_en" binding:"max=255"`
	Content    string     `json:"content"`
	ContentEn  string     `json:"content_en"`
	Type       string     `json:"type" binding:"required"`
	Priority   string     `json:"priority" binding:"required"`
	Status     string     `json:"status" binding:"required"`
	ShowBanner bool       `json:"show_banner"`
	StartsAt   *time.Time `json:"starts_at,omitempty"`
	ExpiresAt  *time.Time `json:"expires_at,omitempty"`
	RelatedURL string     `json:"related_url,omitempty"`
}

// UpdateRequest 更新公告请求
type UpdateRequest struct {
	Title      *string    `json:"title,omitempty"`
	TitleEn    *string    `json:"title_en,omitempty"`
	Content    *string    `json:"content,omitempty"`
	ContentEn  *string    `json:"content_en,omitempty"`
	Type       *string    `json:"type,omitempty"`
	Priority   *string    `json:"priority,omitempty"`
	Status     *string    `json:"status,omitempty"`
	ShowBanner *bool      `json:"show_banner,omitempty"`
	StartsAt   *time.Time `json:"starts_at,omitempty"`
	ExpiresAt  *time.Time `json:"expires_at,omitempty"`
	RelatedURL *string    `json:"related_url,omitempty"`
}

// AnnouncementWithReadStatus 带已读状态的公告
type AnnouncementWithReadStatus struct {
	model.Announcement
	IsRead bool       `json:"is_read"`
	ReadAt *time.Time `json:"read_at,omitempty"`
}

// Stats 公告统计
type Stats struct {
	Total    int64 `json:"total"`
	Active   int64 `json:"active"`
	Draft    int64 `json:"draft"`
	Inactive int64 `json:"inactive"`
}

// ========== 管理员操作 ==========

// List 分页获取公告列表（管理员用）
func (s *Service) List(ctx context.Context, page, pageSize int, typ, status, priority string) ([]model.Announcement, int64, error) {
	q := s.db.WithContext(ctx).Model(&model.Announcement{})
	if typ != "" {
		q = q.Where("type = ?", typ)
	}
	if status != "" {
		q = q.Where("status = ?", status)
	}
	if priority != "" {
		q = q.Where("priority = ?", priority)
	}

	var total int64
	q.Count(&total)

	var list []model.Announcement
	err := q.Order("created_at DESC").
		Offset((page - 1) * pageSize).
		Limit(pageSize).
		Find(&list).Error
	return list, total, err
}

// Create 创建公告
func (s *Service) Create(ctx context.Context, req CreateRequest, creatorID uint) (*model.Announcement, error) {
	ann := &model.Announcement{
		Title:      req.Title,
		TitleEn:    req.TitleEn,
		Content:    req.Content,
		ContentEn:  req.ContentEn,
		Type:       req.Type,
		Priority:   req.Priority,
		Status:     req.Status,
		ShowBanner: req.ShowBanner,
		StartsAt:   req.StartsAt,
		ExpiresAt:  req.ExpiresAt,
		CreatedBy:  creatorID,
		RelatedURL: req.RelatedURL,
	}
	if err := s.db.WithContext(ctx).Create(ann).Error; err != nil {
		return nil, err
	}
	// 新建或修改公告后清除 banner 缓存
	s.invalidateBannerCache()
	return ann, nil
}

// Update 更新公告
func (s *Service) Update(ctx context.Context, id uint, req UpdateRequest) (*model.Announcement, error) {
	var ann model.Announcement
	if err := s.db.WithContext(ctx).First(&ann, id).Error; err != nil {
		return nil, err
	}

	updates := map[string]interface{}{}
	if req.Title != nil {
		updates["title"] = *req.Title
	}
	if req.TitleEn != nil {
		updates["title_en"] = *req.TitleEn
	}
	if req.Content != nil {
		updates["content"] = *req.Content
	}
	if req.ContentEn != nil {
		updates["content_en"] = *req.ContentEn
	}
	if req.Type != nil {
		updates["type"] = *req.Type
	}
	if req.Priority != nil {
		updates["priority"] = *req.Priority
	}
	if req.Status != nil {
		updates["status"] = *req.Status
	}
	if req.ShowBanner != nil {
		updates["show_banner"] = *req.ShowBanner
	}
	if req.StartsAt != nil {
		updates["starts_at"] = req.StartsAt
	}
	if req.ExpiresAt != nil {
		updates["expires_at"] = req.ExpiresAt
	}
	if req.RelatedURL != nil {
		updates["related_url"] = *req.RelatedURL
	}

	if err := s.db.WithContext(ctx).Model(&model.Announcement{}).Where("id = ?", ann.ID).Updates(updates).Error; err != nil {
		return nil, err
	}
	s.invalidateBannerCache()
	return &ann, nil
}

// Delete 软删除公告
func (s *Service) Delete(ctx context.Context, id uint) error {
	err := s.db.WithContext(ctx).Delete(&model.Announcement{}, id).Error
	if err == nil {
		s.invalidateBannerCache()
	}
	return err
}

// GetStats 获取统计信息
func (s *Service) GetStats(ctx context.Context) (*Stats, error) {
	var stats Stats
	s.db.WithContext(ctx).Model(&model.Announcement{}).Count(&stats.Total)
	s.db.WithContext(ctx).Model(&model.Announcement{}).Where("status = ?", "active").Count(&stats.Active)
	s.db.WithContext(ctx).Model(&model.Announcement{}).Where("status = ?", "draft").Count(&stats.Draft)
	s.db.WithContext(ctx).Model(&model.Announcement{}).Where("status = ?", "inactive").Count(&stats.Inactive)
	return &stats, nil
}

// ========== 用户操作 ==========

// GetUserNotifications 获取用户通知列表（含已读状态）
// unreadOnly=true：只返回未读；readOnly=true：只返回已读；两者均 false：全量返回
func (s *Service) GetUserNotifications(ctx context.Context, userID uint, page, pageSize int, unreadOnly, readOnly bool) ([]AnnouncementWithReadStatus, int64, error) {
	now := time.Now()

	// DB 查询 5s 超时（含子查询 + count + order by）
	ctx, cancel := dbctx.Medium(ctx)
	defer cancel()

	// 构建基础查询：status=active，时间范围有效
	baseQ := s.db.WithContext(ctx).Model(&model.Announcement{}).
		Where("status = ?", "active").
		Where("starts_at IS NULL OR starts_at <= ?", now).
		Where("expires_at IS NULL OR expires_at >= ?", now)

	if unreadOnly {
		// 未读：未在 user_announcement_reads 中的记录（使用原生 SQL 子查询避免 GORM 子查询解析问题）
		baseQ = baseQ.Where("id NOT IN (SELECT announcement_id FROM user_announcement_reads WHERE user_id = ?)", userID)
	} else if readOnly {
		// 已读：在 user_announcement_reads 中的记录
		baseQ = baseQ.Where("id IN (SELECT announcement_id FROM user_announcement_reads WHERE user_id = ?)", userID)
	}

	var total int64
	baseQ.Count(&total)

	var anns []model.Announcement
	err := baseQ.Order("FIELD(priority, 'urgent', 'high', 'normal', 'low'), created_at DESC").
		Offset((page - 1) * pageSize).
		Limit(pageSize).
		Find(&anns).Error
	if err != nil {
		return nil, 0, err
	}

	// 批量查询已读状态
	if len(anns) == 0 {
		return []AnnouncementWithReadStatus{}, 0, nil
	}
	ids := make([]uint, len(anns))
	for i, a := range anns {
		ids[i] = a.ID
	}
	var reads []model.UserAnnouncementRead
	s.db.WithContext(ctx).
		Where("user_id = ? AND announcement_id IN ?", userID, ids).
		Find(&reads)

	readMap := map[uint]time.Time{}
	for _, r := range reads {
		readMap[r.AnnouncementID] = r.ReadAt
	}

	result := make([]AnnouncementWithReadStatus, len(anns))
	for i, a := range anns {
		item := AnnouncementWithReadStatus{Announcement: a}
		if t, ok := readMap[a.ID]; ok {
			item.IsRead = true
			item.ReadAt = &t
		}
		result[i] = item
	}
	return result, total, nil
}

// GetUnreadCount 获取用户未读通知数量（Redis 缓存 30s）
func (s *Service) GetUnreadCount(ctx context.Context, userID uint) (int64, error) {
	cacheKey := fmt.Sprintf("notif:unread:%d", userID)
	rdb := pkgredis.Client

	// 尝试从 Redis 读取
	if rdb != nil {
		val, err := rdb.Get(ctx, cacheKey).Result()
		if err == nil {
			var count int64
			if json.Unmarshal([]byte(val), &count) == nil {
				return count, nil
			}
		}
	}

	// 从数据库统计（2s 超时，该端点 Dashboard 高频调用）
	dbCtx, dbCancel := dbctx.Short(ctx)
	defer dbCancel()
	now := time.Now()
	var count int64

	// 使用 LEFT JOIN 避免子查询为空时的 SQL 错误
	err := s.db.WithContext(dbCtx).
		Table("announcements").
		Select("COUNT(DISTINCT announcements.id)").
		Joins("LEFT JOIN user_announcement_reads ON user_announcement_reads.announcement_id = announcements.id AND user_announcement_reads.user_id = ?", userID).
		Where("announcements.status = ?", "active").
		Where("announcements.starts_at IS NULL OR announcements.starts_at <= ?", now).
		Where("announcements.expires_at IS NULL OR announcements.expires_at >= ?", now).
		Where("announcements.deleted_at IS NULL").
		Where("user_announcement_reads.id IS NULL"). // 未读：没有已读记录
		Count(&count).Error
	if err != nil {
		return 0, err
	}

	// 写入缓存 30s
	if rdb != nil {
		data, _ := json.Marshal(count)
		rdb.Set(ctx, cacheKey, string(data), 30*time.Second)
	}
	return count, nil
}

// MarkAsRead 标记单条公告为已读
func (s *Service) MarkAsRead(ctx context.Context, userID, announcementID uint) error {
	// 检查公告是否存在
	var ann model.Announcement
	if err := s.db.WithContext(ctx).First(&ann, announcementID).Error; err != nil {
		return err
	}

	// 使用 upsert：若已存在记录则忽略
	read := &model.UserAnnouncementRead{
		UserID:         userID,
		AnnouncementID: announcementID,
		ReadAt:         time.Now(),
	}
	err := s.db.WithContext(ctx).
		Where(model.UserAnnouncementRead{UserID: userID, AnnouncementID: announcementID}).
		FirstOrCreate(read).Error
	if err == nil {
		s.invalidateUnreadCache(userID)
	}
	return err
}

// MarkAllAsRead 全部标记为已读
func (s *Service) MarkAllAsRead(ctx context.Context, userID uint) error {
	now := time.Now()
	// 获取所有活跃公告
	var anns []model.Announcement
	s.db.WithContext(ctx).Where("status = ?", "active").Find(&anns)

	for _, ann := range anns {
		read := &model.UserAnnouncementRead{
			UserID:         userID,
			AnnouncementID: ann.ID,
			ReadAt:         now,
		}
		s.db.WithContext(ctx).
			Where(model.UserAnnouncementRead{UserID: userID, AnnouncementID: ann.ID}).
			FirstOrCreate(read)
	}
	s.invalidateUnreadCache(userID)
	return nil
}

// GetActiveBanners 获取活跃的滚动 Banner 公告（公开接口，Redis 缓存 2min）
func (s *Service) GetActiveBanners(ctx context.Context) ([]model.Announcement, error) {
	const cacheKey = "notif:banners"
	rdb := pkgredis.Client

	if rdb != nil {
		val, err := rdb.Get(ctx, cacheKey).Result()
		if err == nil {
			var banners []model.Announcement
			if json.Unmarshal([]byte(val), &banners) == nil {
				return banners, nil
			}
		}
	}

	// 2s 超时 — 此端点是公开 landing / 顶栏 banner，必须快速失败降级
	dbCtx, dbCancel := dbctx.Short(ctx)
	defer dbCancel()
	now := time.Now()
	var banners []model.Announcement
	err := s.db.WithContext(dbCtx).
		Where("status = ?", "active").
		Where("show_banner = ?", true).
		Where("starts_at IS NULL OR starts_at <= ?", now).
		Where("expires_at IS NULL OR expires_at >= ?", now).
		Order("FIELD(priority, 'urgent', 'high', 'normal', 'low'), created_at DESC").
		Find(&banners).Error
	if err != nil {
		return nil, err
	}

	if rdb != nil {
		data, _ := json.Marshal(banners)
		rdb.Set(ctx, cacheKey, string(data), 2*time.Minute)
	}
	return banners, nil
}

// ========== 缓存失效 ==========

func (s *Service) invalidateBannerCache() {
	rdb := pkgredis.Client
	if rdb != nil {
		rdb.Del(context.Background(), "notif:banners")
	}
}

func (s *Service) invalidateUnreadCache(userID uint) {
	rdb := pkgredis.Client
	if rdb != nil {
		rdb.Del(context.Background(), fmt.Sprintf("notif:unread:%d", userID))
	}
	// 统一失效 user:notif:unread:{uid}（新缓存路径由 NotificationHandler.UnreadCount 使用）
	usercache.InvalidateNotifUnread(context.Background(), userID)
}
