package service

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"
	"xui/database"
	"xui/database/model"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

const (
	AnnouncementAudiencePanel  = "panel"
	AnnouncementAudienceClient = "client"
	AnnouncementAudienceAll    = "all"

	AnnouncementLevelInfo      = "info"
	AnnouncementLevelWarning   = "warning"
	AnnouncementLevelImportant = "important"
)

// 公告长度上限：面板弹窗和客户端通知都只适合短文本，超长由管理员改用使用说明。
const (
	announcementTitleMaxRunes   = 100
	announcementContentMaxRunes = 4000
	announcementActiveMax       = 20
)

// AnnouncementInput 是管理员新建/编辑公告的入参，Id 为 0 表示新建。
type AnnouncementInput struct {
	Id       int    `json:"id"`
	Title    string `json:"title"`
	Content  string `json:"content"`
	Level    string `json:"level"`
	Audience string `json:"audience"`
	Pinned   bool   `json:"pinned"`
	Enabled  bool   `json:"enabled"`
	ExpireAt int64  `json:"expireAt"`
}

// ClientAnnouncement 是下发给 SClash 的精简结构：
// 客户端只需要展示与去重所需字段，不暴露启停、受众等管理信息。
type ClientAnnouncement struct {
	Id        int    `json:"id"`
	Title     string `json:"title"`
	Content   string `json:"content"`
	Level     string `json:"level"`
	Pinned    bool   `json:"pinned"`
	UpdatedAt int64  `json:"updatedAt"`
}

// ClientAnnouncementFeed 带一个内容版本号，客户端可用它判断是否有更新，
// 也便于 ETag 缓存协商。
type ClientAnnouncementFeed struct {
	Version       string               `json:"version"`
	Announcements []ClientAnnouncement `json:"announcements"`
}

type AnnouncementService struct{}

func normalizeAnnouncementLevel(level string) (string, error) {
	switch strings.TrimSpace(level) {
	case "", AnnouncementLevelInfo:
		return AnnouncementLevelInfo, nil
	case AnnouncementLevelWarning:
		return AnnouncementLevelWarning, nil
	case AnnouncementLevelImportant:
		return AnnouncementLevelImportant, nil
	default:
		return "", errors.New("公告级别无效")
	}
}

func normalizeAnnouncementAudience(audience string) (string, error) {
	switch strings.TrimSpace(audience) {
	case "", AnnouncementAudiencePanel:
		return AnnouncementAudiencePanel, nil
	case AnnouncementAudienceClient:
		return AnnouncementAudienceClient, nil
	case AnnouncementAudienceAll:
		return AnnouncementAudienceAll, nil
	default:
		return "", errors.New("公告范围无效")
	}
}

// List 返回全部公告，置顶优先，其余按最近更新排序。
// 同时回填每条公告的已读人数，供管理端表格展示。
func (s *AnnouncementService) List() ([]model.Announcement, error) {
	rows := make([]model.Announcement, 0)
	err := database.GetDB().
		Order("pinned DESC").
		Order("updated_at DESC").
		Order("id DESC").
		Find(&rows).Error
	if err != nil {
		return nil, err
	}
	counts, err := announcementReadCounts()
	if err != nil {
		return nil, err
	}
	for i := range rows {
		rows[i].ReadCount = counts[rows[i].Id]
	}
	return rows, nil
}

// Active 返回指定受众当前可见的公告：已启用、未过期，且受众匹配或为 all。
func (s *AnnouncementService) Active(audience string) ([]model.Announcement, error) {
	now := time.Now().Unix()
	rows := make([]model.Announcement, 0)
	err := database.GetDB().
		Where("enabled = ?", true).
		Where("audience IN ?", []string{audience, AnnouncementAudienceAll}).
		Where("expire_at = 0 OR expire_at > ?", now).
		Order("pinned DESC").
		Order("updated_at DESC").
		Order("id DESC").
		Limit(announcementActiveMax).
		Find(&rows).Error
	return rows, err
}

// Save 新建或更新公告。保存后 UpdatedAt 一定变化，客户端据此重新提醒同一公告的修订内容。
func (s *AnnouncementService) Save(input *AnnouncementInput) (*model.Announcement, error) {
	if input == nil {
		return nil, errors.New("公告内容不能为空")
	}
	title := strings.TrimSpace(input.Title)
	if title == "" {
		return nil, errors.New("请填写公告标题")
	}
	if utf8.RuneCountInString(title) > announcementTitleMaxRunes {
		return nil, fmt.Errorf("公告标题不能超过 %d 个字符", announcementTitleMaxRunes)
	}
	content := strings.TrimSpace(strings.ReplaceAll(input.Content, "\r\n", "\n"))
	if content == "" {
		return nil, errors.New("请填写公告内容")
	}
	if utf8.RuneCountInString(content) > announcementContentMaxRunes {
		return nil, fmt.Errorf("公告内容不能超过 %d 个字符", announcementContentMaxRunes)
	}
	level, err := normalizeAnnouncementLevel(input.Level)
	if err != nil {
		return nil, err
	}
	audience, err := normalizeAnnouncementAudience(input.Audience)
	if err != nil {
		return nil, err
	}
	if input.ExpireAt < 0 {
		return nil, errors.New("过期时间无效")
	}

	db := database.GetDB()
	now := time.Now().Unix()
	row := &model.Announcement{
		Title:    title,
		Content:  content,
		Level:    level,
		Audience: audience,
		Pinned:   input.Pinned,
		Enabled:  input.Enabled,
		ExpireAt: input.ExpireAt,
	}
	if input.Id > 0 {
		if err := db.First(row, input.Id).Error; err != nil {
			if database.IsNotFound(err) {
				return nil, errors.New("公告不存在或已被删除")
			}
			return nil, err
		}
		row.Title = title
		row.Content = content
		row.Level = level
		row.Audience = audience
		row.Pinned = input.Pinned
		row.Enabled = input.Enabled
		row.ExpireAt = input.ExpireAt
		if row.Updated >= now {
			// 同一秒内的连续修改也要让版本号单调递增，否则客户端会认为内容没变。
			row.Updated = row.Updated + 1
		} else {
			row.Updated = now
		}
		if err := db.Save(row).Error; err != nil {
			return nil, err
		}
		return row, nil
	}
	row.Created = now
	row.Updated = now
	if err := db.Create(row).Error; err != nil {
		return nil, err
	}
	return row, nil
}

// Delete 删除公告，同时清理它的阅读记录，避免留下孤儿行。
func (s *AnnouncementService) Delete(id int) error {
	if id <= 0 {
		return errors.New("公告编号无效")
	}
	return database.GetDB().Transaction(func(tx *gorm.DB) error {
		result := tx.Delete(&model.Announcement{}, id)
		if result.Error != nil {
			return result.Error
		}
		if result.RowsAffected == 0 {
			return errors.New("公告不存在或已被删除")
		}
		return tx.Where("announcement_id = ?", id).
			Delete(&model.AnnouncementReceipt{}).Error
	})
}

// ClientFeed 生成下发给 SClash 的公告列表与内容版本号。
func (s *AnnouncementService) ClientFeed() (*ClientAnnouncementFeed, error) {
	rows, err := s.Active(AnnouncementAudienceClient)
	if err != nil {
		return nil, err
	}
	feed := &ClientAnnouncementFeed{Announcements: make([]ClientAnnouncement, 0, len(rows))}
	parts := make([]string, 0, len(rows))
	for _, row := range rows {
		feed.Announcements = append(feed.Announcements, ClientAnnouncement{
			Id:        row.Id,
			Title:     row.Title,
			Content:   row.Content,
			Level:     row.Level,
			Pinned:    row.Pinned,
			UpdatedAt: row.Updated,
		})
		parts = append(parts, strconv.Itoa(row.Id)+":"+strconv.FormatInt(row.Updated, 10))
	}
	// 排序后再哈希，避免数据库返回顺序抖动导致版本号无故变化。
	sort.Strings(parts)
	sum := sha256.Sum256([]byte(strings.Join(parts, ",")))
	feed.Version = hex.EncodeToString(sum[:])[:16]
	return feed, nil
}

// 阅读记录的账号标识前缀。格式固定，便于展示时反解出可读名称。
const (
	announcementActorPanelUser  = "panel:user:"
	announcementActorPanelPort  = "panel:port:"
	announcementActorClientPort = "client:port:"
)

// PanelUserActor 是面板管理员账号的阅读记录标识。
func PanelUserActor(userId int) string { return announcementActorPanelUser + strconv.Itoa(userId) }

// PanelPortActor 是端口账号（受限登录）的阅读记录标识。
func PanelPortActor(port int) string { return announcementActorPanelPort + strconv.Itoa(port) }

// ClientPortActor 是 SClash 客户端账号的阅读记录标识。
func ClientPortActor(port int) string { return announcementActorClientPort + strconv.Itoa(port) }

// announcementActorLabel 把标识转成管理端可读的名称。
func announcementActorLabel(actor string) string {
	switch {
	case strings.HasPrefix(actor, announcementActorPanelUser):
		return "面板管理员"
	case strings.HasPrefix(actor, announcementActorPanelPort):
		return "面板账号 " + strings.TrimPrefix(actor, announcementActorPanelPort)
	case strings.HasPrefix(actor, announcementActorClientPort):
		return "SClash " + strings.TrimPrefix(actor, announcementActorClientPort)
	default:
		return actor
	}
}

// AnnouncementPage 是面板历史公告的分页结果。
type AnnouncementPage struct {
	Total int                  `json:"total"`
	Page  int                  `json:"page"`
	Size  int                  `json:"size"`
	Items []model.Announcement `json:"items"`
}

// AnnouncementReceiptView 是阅读记录的展示结构，比数据库行多一个可读名称。
type AnnouncementReceiptView struct {
	Actor   string `json:"actor"`
	Label   string `json:"label"`
	Channel string `json:"channel"`
	ReadAt  int64  `json:"readAt"`
}

// panelHistoryQuery 是面板用户可见的历史公告范围：
// 只含启用中的公告（管理员停用等于撤回，不再出现在用户侧），
// 已过期仍保留，作为历史记录。
func panelHistoryQuery() *gorm.DB {
	return database.GetDB().
		Model(&model.Announcement{}).
		Where("enabled = ?", true).
		Where("audience IN ?", []string{AnnouncementAudiencePanel, AnnouncementAudienceAll})
}

// History 按发布时间倒序返回面板用户可见的历史公告，分页。
func (s *AnnouncementService) History(page, size int) (*AnnouncementPage, error) {
	if page < 1 {
		page = 1
	}
	if size < 1 || size > 50 {
		size = 20
	}
	var total int64
	if err := panelHistoryQuery().Count(&total).Error; err != nil {
		return nil, err
	}
	rows := make([]model.Announcement, 0, size)
	if err := panelHistoryQuery().
		Order("created_at DESC").
		Order("id DESC").
		Offset((page - 1) * size).
		Limit(size).
		Find(&rows).Error; err != nil {
		return nil, err
	}
	return &AnnouncementPage{Total: int(total), Page: page, Size: size, Items: rows}, nil
}

// MarkRead 记录 actor 已读。重复上报由唯一索引忽略，保留首次阅读时间；
// 只接受与 channel 受众匹配的公告，避免把面板公告记到客户端头上。
func (s *AnnouncementService) MarkRead(actor, channel string, ids []int) error {
	actor = strings.TrimSpace(actor)
	if actor == "" {
		return errors.New("缺少阅读者标识")
	}
	switch channel {
	case AnnouncementAudiencePanel, AnnouncementAudienceClient:
	default:
		return errors.New("阅读渠道无效")
	}
	clean := make([]int, 0, len(ids))
	seen := make(map[int]bool, len(ids))
	for _, id := range ids {
		if id <= 0 || seen[id] {
			continue
		}
		seen[id] = true
		clean = append(clean, id)
		if len(clean) >= 50 {
			break
		}
	}
	if len(clean) == 0 {
		return nil
	}
	rows := make([]model.Announcement, 0, len(clean))
	if err := database.GetDB().
		Model(&model.Announcement{}).
		Where("id IN ?", clean).
		Where("audience IN ?", []string{channel, AnnouncementAudienceAll}).
		Find(&rows).Error; err != nil {
		return err
	}
	if len(rows) == 0 {
		return nil
	}
	now := time.Now().Unix()
	receipts := make([]model.AnnouncementReceipt, 0, len(rows))
	for _, row := range rows {
		receipts = append(receipts, model.AnnouncementReceipt{
			AnnouncementId: row.Id,
			Actor:          actor,
			Channel:        channel,
			ReadAt:         now,
		})
	}
	return database.GetDB().Clauses(clause.OnConflict{DoNothing: true}).Create(&receipts).Error
}

// Receipts 返回某条公告的阅读记录，最近的在前。
func (s *AnnouncementService) Receipts(announcementId int) ([]AnnouncementReceiptView, error) {
	if announcementId <= 0 {
		return nil, errors.New("公告编号无效")
	}
	rows := make([]model.AnnouncementReceipt, 0)
	if err := database.GetDB().
		Where("announcement_id = ?", announcementId).
		Order("read_at DESC").
		Order("id DESC").
		Limit(500).
		Find(&rows).Error; err != nil {
		return nil, err
	}
	views := make([]AnnouncementReceiptView, 0, len(rows))
	for _, row := range rows {
		views = append(views, AnnouncementReceiptView{
			Actor:   row.Actor,
			Label:   announcementActorLabel(row.Actor),
			Channel: row.Channel,
			ReadAt:  row.ReadAt,
		})
	}
	return views, nil
}

// announcementReadCounts 统计每条公告的已读人数。
func announcementReadCounts() (map[int]int64, error) {
	type receiptCount struct {
		AnnouncementId int   `gorm:"column:announcement_id"`
		Total          int64 `gorm:"column:total"`
	}
	var counts []receiptCount
	if err := database.GetDB().
		Model(&model.AnnouncementReceipt{}).
		Select("announcement_id, COUNT(*) AS total").
		Group("announcement_id").
		Scan(&counts).Error; err != nil {
		return nil, err
	}
	result := make(map[int]int64, len(counts))
	for _, c := range counts {
		result[c.AnnouncementId] = c.Total
	}
	return result, nil
}
