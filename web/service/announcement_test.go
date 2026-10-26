package service

import (
	"path/filepath"
	"strings"
	"testing"
	"time"

	"xui/database"
	"xui/database/model"
)

func setupAnnouncementDB(t *testing.T) *AnnouncementService {
	t.Helper()
	if err := database.InitDB(filepath.Join(t.TempDir(), "announcement.db")); err != nil {
		t.Fatal(err)
	}
	// Windows 上 SQLite 不关连接会占住临时目录，显式关闭避免清理失败。
	db, err := database.GetDB().DB()
	if err == nil {
		t.Cleanup(func() { db.Close() })
	}
	return &AnnouncementService{}
}

// 受众过滤：面板公告只出现在面板，客户端公告只出现在客户端，all 两边都下发。
func TestAnnouncementAudience(t *testing.T) {
	s := setupAnnouncementDB(t)
	if _, err := s.Save(&AnnouncementInput{Title: "面板公告", Content: "仅面板", Audience: AnnouncementAudiencePanel, Enabled: true}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Save(&AnnouncementInput{Title: "客户端公告", Content: "仅客户端", Audience: AnnouncementAudienceClient, Enabled: true}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Save(&AnnouncementInput{Title: "公共公告", Content: "两边都有", Audience: AnnouncementAudienceAll, Enabled: true}); err != nil {
		t.Fatal(err)
	}

	panel, err := s.Active(AnnouncementAudiencePanel)
	if err != nil {
		t.Fatal(err)
	}
	if len(panel) != 2 {
		t.Fatalf("面板应看到 2 条公告，实际 %d", len(panel))
	}
	for _, row := range panel {
		if row.Title == "客户端公告" {
			t.Fatal("客户端公告不应出现在面板列表")
		}
	}

	feed, err := s.ClientFeed()
	if err != nil {
		t.Fatal(err)
	}
	if len(feed.Announcements) != 2 {
		t.Fatalf("客户端应收到 2 条公告，实际 %d", len(feed.Announcements))
	}
	for _, item := range feed.Announcements {
		if item.Title == "面板公告" {
			t.Fatal("面板公告不应下发给客户端")
		}
	}
}

// 停用与过期的公告不下发。
func TestAnnouncementHiddenWhenDisabledOrExpired(t *testing.T) {
	s := setupAnnouncementDB(t)
	if _, err := s.Save(&AnnouncementInput{Title: "停用的", Content: "内容", Enabled: false}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Save(&AnnouncementInput{Title: "过期的", Content: "内容", Enabled: true, ExpireAt: time.Now().Add(-time.Minute).Unix()}); err != nil {
		t.Fatal(err)
	}
	rows, err := s.Active(AnnouncementAudiencePanel)
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 0 {
		t.Fatalf("停用与过期的公告都不应下发，实际 %d 条", len(rows))
	}
}

// 置顶公告排在前面，其余按更新时间倒序。
func TestAnnouncementPinnedFirst(t *testing.T) {
	s := setupAnnouncementDB(t)
	if _, err := s.Save(&AnnouncementInput{Title: "普通", Content: "内容", Enabled: true}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Save(&AnnouncementInput{Title: "置顶", Content: "内容", Enabled: true, Pinned: true}); err != nil {
		t.Fatal(err)
	}
	rows, err := s.Active(AnnouncementAudiencePanel)
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 2 || rows[0].Title != "置顶" {
		t.Fatalf("置顶公告应排在最前，实际顺序: %+v", rows)
	}
}

// 修改同一公告必须让 updatedAt 变化，客户端据此重新提醒。
func TestAnnouncementUpdateBumpsVersion(t *testing.T) {
	s := setupAnnouncementDB(t)
	created, err := s.Save(&AnnouncementInput{Title: "标题", Content: "第一版", Enabled: true, Audience: AnnouncementAudienceClient})
	if err != nil {
		t.Fatal(err)
	}
	first, err := s.ClientFeed()
	if err != nil {
		t.Fatal(err)
	}
	time.Sleep(1100 * time.Millisecond)
	updated, err := s.Save(&AnnouncementInput{Id: created.Id, Title: "标题", Content: "第二版", Enabled: true, Audience: AnnouncementAudienceClient})
	if err != nil {
		t.Fatal(err)
	}
	if updated.Updated <= created.Updated {
		t.Fatalf("更新时间必须递增: %d -> %d", created.Updated, updated.Updated)
	}
	second, err := s.ClientFeed()
	if err != nil {
		t.Fatal(err)
	}
	if first.Version == second.Version {
		t.Fatal("内容变化后版本号必须变化")
	}
	if len(second.Announcements) != 1 || second.Announcements[0].Content != "第二版" {
		t.Fatalf("客户端应拿到最新内容，实际 %+v", second.Announcements)
	}
}

// 同一秒内连续保存两次，更新时间也必须单调递增。
func TestAnnouncementUpdateWithinSameSecond(t *testing.T) {
	s := setupAnnouncementDB(t)
	created, err := s.Save(&AnnouncementInput{Title: "标题", Content: "第一版", Enabled: true, Audience: AnnouncementAudienceClient})
	if err != nil {
		t.Fatal(err)
	}
	updated, err := s.Save(&AnnouncementInput{Id: created.Id, Title: "标题", Content: "第二版", Enabled: true, Audience: AnnouncementAudienceClient})
	if err != nil {
		t.Fatal(err)
	}
	if updated.Updated <= created.Updated {
		t.Fatalf("同一秒内保存也必须推进更新时间: %d -> %d", created.Updated, updated.Updated)
	}
}

// 内容会被裁剪与规范化，并且拒绝非法级别、范围和超长文本。
func TestAnnouncementValidation(t *testing.T) {
	s := setupAnnouncementDB(t)
	if _, err := s.Save(&AnnouncementInput{Title: "  ", Content: "内容"}); err == nil {
		t.Fatal("空标题必须被拒绝")
	}
	if _, err := s.Save(&AnnouncementInput{Title: "标题", Content: "  "}); err == nil {
		t.Fatal("空内容必须被拒绝")
	}
	if _, err := s.Save(&AnnouncementInput{Title: strings.Repeat("标", announcementTitleMaxRunes+1), Content: "内容"}); err == nil {
		t.Fatal("超长标题必须被拒绝")
	}
	if _, err := s.Save(&AnnouncementInput{Title: "标题", Content: strings.Repeat("文", announcementContentMaxRunes+1)}); err == nil {
		t.Fatal("超长内容必须被拒绝")
	}
	if _, err := s.Save(&AnnouncementInput{Title: "标题", Content: "内容", Level: "urgent"}); err == nil {
		t.Fatal("非法级别必须被拒绝")
	}
	if _, err := s.Save(&AnnouncementInput{Title: "标题", Content: "内容", Audience: "web"}); err == nil {
		t.Fatal("非法范围必须被拒绝")
	}
	if _, err := s.Save(&AnnouncementInput{Title: "标题", Content: "内容", ExpireAt: -1}); err == nil {
		t.Fatal("负数过期时间必须被拒绝")
	}

	row, err := s.Save(&AnnouncementInput{Title: "  标题  ", Content: "第一行\r\n第二行", Enabled: true})
	if err != nil {
		t.Fatal(err)
	}
	if row.Title != "标题" {
		t.Fatalf("标题应去掉首尾空白，实际 %q", row.Title)
	}
	if row.Content != "第一行\n第二行" {
		t.Fatalf("内容应统一换行符，实际 %q", row.Content)
	}
	if row.Level != AnnouncementLevelInfo {
		t.Fatalf("默认级别应为 info，实际 %q", row.Level)
	}
	if row.Audience != AnnouncementAudiencePanel {
		t.Fatalf("默认范围应为 panel，实际 %q", row.Audience)
	}
}

// 删除后不再下发；重复删除返回错误。
func TestAnnouncementDelete(t *testing.T) {
	s := setupAnnouncementDB(t)
	row, err := s.Save(&AnnouncementInput{Title: "标题", Content: "内容", Enabled: true})
	if err != nil {
		t.Fatal(err)
	}
	// 先制造两条阅读记录，删除后不应残留孤儿行。
	for _, actor := range []string{PanelUserActor(1), ClientPortActor(18888)} {
		if err := s.MarkRead(actor, AnnouncementAudiencePanel, []int{row.Id}); err != nil {
			t.Fatal(err)
		}
	}
	if views, err := s.Receipts(row.Id); err != nil || len(views) != 2 {
		t.Fatalf("删除前应有 2 条阅读记录: %+v %v", views, err)
	}
	if err := s.Delete(row.Id); err != nil {
		t.Fatal(err)
	}
	if views, err := s.Receipts(row.Id); err != nil || len(views) != 0 {
		t.Fatalf("删除公告应同时清理阅读记录，实际 %+v %v", views, err)
	}
	rows, err := s.Active(AnnouncementAudiencePanel)
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 0 {
		t.Fatal("删除后不应再下发")
	}
	if err := s.Delete(row.Id); err == nil {
		t.Fatal("重复删除应返回错误")
	}
}

// 停用公告即使带未来的过期时间也不能下发，受众不符的公告也不能串到另一侧：
// 「未过期」这个条件必须自成一组，否则 OR 会绕过 enabled 与 audience 过滤。
func TestAnnouncementFilterIsNotBypassedByExpiry(t *testing.T) {
	s := setupAnnouncementDB(t)
	future := time.Now().Add(time.Hour).Unix()
	if _, err := s.Save(&AnnouncementInput{Title: "停用但未过期", Content: "内容", Audience: AnnouncementAudienceAll, Enabled: false, ExpireAt: future}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Save(&AnnouncementInput{Title: "仅客户端", Content: "内容", Audience: AnnouncementAudienceClient, Enabled: true, ExpireAt: future}); err != nil {
		t.Fatal(err)
	}

	rows, err := s.Active(AnnouncementAudiencePanel)
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 0 {
		t.Fatalf("面板不应看到停用或仅客户端公告，实际 %d 条: %+v", len(rows), rows)
	}

	feed, err := s.ClientFeed()
	if err != nil {
		t.Fatal(err)
	}
	if len(feed.Announcements) != 1 || feed.Announcements[0].Title != "仅客户端" {
		t.Fatalf("客户端只应看到启用的客户端公告，实际 %+v", feed.Announcements)
	}
}

// 空列表也要有稳定的版本号，客户端可以安全比较。
func TestAnnouncementEmptyFeedVersion(t *testing.T) {
	s := setupAnnouncementDB(t)
	feed, err := s.ClientFeed()
	if err != nil {
		t.Fatal(err)
	}
	if len(feed.Announcements) != 0 {
		t.Fatalf("初始不应有公告，实际 %d 条", len(feed.Announcements))
	}
	if feed.Version == "" {
		t.Fatal("空列表也应返回版本号")
	}
	again, err := s.ClientFeed()
	if err != nil {
		t.Fatal(err)
	}
	if again.Version != feed.Version {
		t.Fatal("内容不变时版本号必须稳定")
	}
}

// 数据库里应能查到迁移后的公告表，避免升级后首次发布公告失败。
func TestAnnouncementTableMigrated(t *testing.T) {
	setupAnnouncementDB(t)
	if !database.GetDB().Migrator().HasTable(&model.Announcement{}) {
		t.Fatal("公告表未被迁移")
	}
	for _, column := range []string{"title", "content", "level", "audience", "pinned", "enabled", "expire_at", "created_at", "updated_at"} {
		if !database.GetDB().Migrator().HasColumn(&model.Announcement{}, column) {
			t.Fatalf("公告表缺少列 %s", column)
		}
	}
}

// 历史公告：只含启用中的面板公告，已过期保留，停用与仅客户端的不出现；分页正确。
func TestAnnouncementHistoryPaging(t *testing.T) {
	s := setupAnnouncementDB(t)
	past := time.Now().Add(-time.Hour).Unix()
	first, err := s.Save(&AnnouncementInput{Title: "第一条", Content: "内容", Audience: AnnouncementAudiencePanel, Enabled: true})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.Save(&AnnouncementInput{Title: "第二条", Content: "内容", Audience: AnnouncementAudienceAll, Enabled: true}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Save(&AnnouncementInput{Title: "已过期", Content: "内容", Audience: AnnouncementAudiencePanel, Enabled: true, ExpireAt: past}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Save(&AnnouncementInput{Title: "已停用", Content: "内容", Audience: AnnouncementAudiencePanel, Enabled: false}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Save(&AnnouncementInput{Title: "仅客户端", Content: "内容", Audience: AnnouncementAudienceClient, Enabled: true}); err != nil {
		t.Fatal(err)
	}

	page, err := s.History(1, 2)
	if err != nil {
		t.Fatal(err)
	}
	if page.Total != 3 {
		t.Fatalf("历史公告应为 3 条，实际 %d", page.Total)
	}
	if len(page.Items) != 2 {
		t.Fatalf("第一页应为 2 条，实际 %d", len(page.Items))
	}
	for _, row := range page.Items {
		if row.Title == "已停用" || row.Title == "仅客户端" {
			t.Fatalf("停用或仅客户端公告不应出现在历史里: %s", row.Title)
		}
	}
	second, err := s.History(2, 2)
	if err != nil {
		t.Fatal(err)
	}
	if len(second.Items) != 1 || second.Items[0].Id != first.Id {
		t.Fatalf("第二页应只剩最早的一条，实际 %+v", second.Items)
	}
	if empty, err := s.History(0, 0); err != nil || empty.Page != 1 || empty.Size != 20 {
		t.Fatalf("非法分页参数应回落到第 1 页 20 条: %+v %v", empty, err)
	}
}

// 已读上报：去重、保留首次时间、受众不匹配的公告不计入。
func TestAnnouncementMarkRead(t *testing.T) {
	s := setupAnnouncementDB(t)
	shared, err := s.Save(&AnnouncementInput{Title: "公共公告", Content: "内容", Audience: AnnouncementAudienceAll, Enabled: true})
	if err != nil {
		t.Fatal(err)
	}
	panelOnly, err := s.Save(&AnnouncementInput{Title: "仅面板", Content: "内容", Audience: AnnouncementAudiencePanel, Enabled: true})
	if err != nil {
		t.Fatal(err)
	}

	if err := s.MarkRead(PanelUserActor(1), AnnouncementAudiencePanel, []int{shared.Id}); err != nil {
		t.Fatal(err)
	}
	if err := s.MarkRead(PanelUserActor(1), AnnouncementAudiencePanel, []int{shared.Id, shared.Id}); err != nil {
		t.Fatal(err)
	}
	rows, err := s.Receipts(shared.Id)
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 1 {
		t.Fatalf("同一账号重复上报只应有一条记录，实际 %d", len(rows))
	}
	if rows[0].Label != "面板管理员" {
		t.Fatalf("管理员标识应显示为面板管理员，实际 %q", rows[0].Label)
	}

	// 把首次时间改成固定值，再上报一次，验证不会被刷新。
	if err := database.GetDB().Model(&model.AnnouncementReceipt{}).
		Where("announcement_id = ? AND actor = ?", shared.Id, PanelUserActor(1)).
		Update("read_at", 1000).Error; err != nil {
		t.Fatal(err)
	}
	if err := s.MarkRead(PanelUserActor(1), AnnouncementAudiencePanel, []int{shared.Id}); err != nil {
		t.Fatal(err)
	}
	rows, err = s.Receipts(shared.Id)
	if err != nil {
		t.Fatal(err)
	}
	if rows[0].ReadAt != 1000 {
		t.Fatalf("重复上报不应刷新首次阅读时间，实际 %d", rows[0].ReadAt)
	}

	// 客户端通道不能把面板专属公告记成已读。
	if err := s.MarkRead(ClientPortActor(18888), AnnouncementAudienceClient, []int{panelOnly.Id}); err != nil {
		t.Fatal(err)
	}
	if rows, err = s.Receipts(panelOnly.Id); err != nil || len(rows) != 0 {
		t.Fatalf("客户端不应记录仅面板公告: %+v %v", rows, err)
	}

	if err := s.MarkRead("", AnnouncementAudiencePanel, []int{shared.Id}); err == nil {
		t.Fatal("缺少账号标识必须报错")
	}
	if err := s.MarkRead(PanelUserActor(1), "web", []int{shared.Id}); err == nil {
		t.Fatal("非法渠道必须报错")
	}
	if err := s.MarkRead(PanelUserActor(1), AnnouncementAudiencePanel, []int{0, -1}); err != nil {
		t.Fatalf("无效编号应被忽略而不是报错: %v", err)
	}
	if err := s.MarkRead(PanelUserActor(1), AnnouncementAudiencePanel, nil); err != nil {
		t.Fatalf("空列表应直接返回: %v", err)
	}
}

// 管理端列表要回填已读人数，标识要能反解成可读名称。
func TestAnnouncementReceiptsAndReadCount(t *testing.T) {
	s := setupAnnouncementDB(t)
	row, err := s.Save(&AnnouncementInput{Title: "公告", Content: "内容", Audience: AnnouncementAudienceAll, Enabled: true})
	if err != nil {
		t.Fatal(err)
	}
	for _, actor := range []string{
		PanelUserActor(1),
		PanelPortActor(18888),
		ClientPortActor(18888),
	} {
		if err := s.MarkRead(actor, AnnouncementAudienceClient, []int{row.Id}); err != nil {
			t.Fatal(err)
		}
	}
	rows, err := s.List()
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 1 || rows[0].ReadCount != 3 {
		t.Fatalf("已读人数应为 3，实际 %+v", rows)
	}
	views, err := s.Receipts(row.Id)
	if err != nil {
		t.Fatal(err)
	}
	labels := map[string]bool{}
	for _, view := range views {
		labels[view.Label] = true
	}
	for _, want := range []string{"面板管理员", "面板账号 18888", "SClash 18888"} {
		if !labels[want] {
			t.Fatalf("缺少可读名称 %q，实际 %+v", want, labels)
		}
	}
	if _, err := s.Receipts(0); err == nil {
		t.Fatal("非法公告编号必须报错")
	}
}
