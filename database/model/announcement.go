package model

// Announcement 是管理员发布的通知公告，分别投递给面板登录用户和 SClash 客户端。
//
// Content 只保存纯文本：面板弹窗与客户端都按文本渲染，不解析 HTML，
// 从根上避免存储型 XSS，也不需要为公告额外引入 HTML 过滤器。
// 时间字段统一用 Unix 秒，0 表示长期有效。
//
// 时间字段刻意命名为 Created/Updated 而不是 CreatedAt/UpdatedAt：
// GORM 会把后者按字段名当成自动时间戳，保存时用当前秒覆盖，
// 导致同一秒内的二次修改无法推进版本号，客户端也就不会重新提醒。
type Announcement struct {
	Id       int    `json:"id" gorm:"primaryKey"`
	Title    string `json:"title"`
	Content  string `json:"content"`
	Level    string `json:"level"`    // info | warning | important
	Audience string `json:"audience"` // panel | client | all
	Pinned   bool   `json:"pinned"`
	Enabled  bool   `json:"enabled"`
	ExpireAt int64  `json:"expireAt"`
	Created  int64  `json:"createdAt" gorm:"column:created_at"`
	Updated  int64  `json:"updatedAt" gorm:"column:updated_at"`

	// ReadCount 只在管理端列表里回填，不入库。
	ReadCount int64 `json:"readCount" gorm:"-"`
}

// AnnouncementReceipt 记录某个账号在哪一侧、第一次读到某条公告的时间。
//
// Actor 是稳定的账号标识：面板管理员为 panel:user:<id>，端口账号为
// panel:port:<端口>，SClash 为 client:port:<端口>。同一条公告对同一账号
// 只保留首次阅读记录，(announcement_id, actor) 上有唯一索引兜底，
// 重复上报不会产生重复行，也不会刷新首次阅读时间。
type AnnouncementReceipt struct {
	Id             int    `json:"id" gorm:"primaryKey"`
	AnnouncementId int    `json:"announcementId" gorm:"uniqueIndex:idx_announcement_actor"`
	Actor          string `json:"actor" gorm:"uniqueIndex:idx_announcement_actor"`
	Channel        string `json:"channel"` // panel | client
	ReadAt         int64  `json:"readAt"`
}
