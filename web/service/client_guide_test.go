package service

import (
	"encoding/json"
	"strings"
	"testing"
	"time"
	"xui/database"
	"xui/database/model"
)

func TestClientGuidePreservesRichTextAndRasterImages(t *testing.T) {
	clientTestDB(t)
	s := new(ClientService)
	png := "data:image/png;base64,iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAQAAAC1HAwCAAAAC0lEQVR42mP8/x8AAwMCAO+jc1kAAAAASUVORK5CYII="
	gif := "data:image/gif;base64,R0lGODlhAQABAIAAAAAAAP///ywAAAAAAQABAAACAUwAOw=="
	webp := "data:image/webp;base64,UklGRh4AAABXRUJQVlA4TBEAAAAvAAAAAAfQ//73v/+BiOh/AAA="
	raw := `<h1>客户端使用说明</h1><div><p style="color:#ff0000;background-color:rgb(255, 255, 0);font-size:18px;text-align:center;font-family:'微软雅黑', Arial;">步骤<strong>一</strong><b>二</b><em>三</em><i>四</i><u>五</u></p>` +
		`<ul><li>安装</li></ul><ol><li>登录</li></ol><blockquote>提示</blockquote><pre><code>example</code></pre>` +
		`<table style="border-collapse:collapse;"><tbody><tr><th>系统</th><td colspan="2">Windows</td></tr></tbody></table>` +
		`<font face="Microsoft YaHei" color="#ff0000" size="4">兼容字体</font>` +
		`<a href="https://example.com/manual" target="_blank">教程</a>` +
		`<img src="` + png + `" width="640" height="480" style="width:80%;max-width:100%;height:auto;" alt="说明图片">` +
		`<img src="` + gif + `"><img src="` + webp + `"><img src="data:image/jpeg;base64,/9j/2Q=="><img src="http://example.com/screenshot.jpg"></div>`
	guide, err := s.SaveGuide(raw)
	if err != nil {
		t.Fatal(err)
	}
	for _, expected := range []string{"<h1>", "<strong>", "<ul>", "<ol>", "<blockquote>", "<pre>", "<code>", "<table", "<tbody>", `colspan="2"`, `face="Microsoft YaHei"`, `color="#ff0000"`, `size="4"`, "color: #ff0000", "background-color: rgb(255, 255, 0)", "font-size: 18px", "text-align: center", "微软雅黑", "max-width: 100%", png, gif, webp, "data:image/jpeg;base64,/9j/2Q==", "http://example.com/screenshot.jpg", "https://example.com/manual", "noopener", "noreferrer"} {
		if !strings.Contains(guide.HTML, expected) {
			t.Errorf("rich text lost %q: %s", expected, guide.HTML)
		}
	}
	if guide.UpdatedAt < time.Now().Add(-time.Minute).Unix() || guide.UpdatedAt > time.Now().Unix() {
		t.Fatal("guide save time is not Unix seconds")
	}
	loaded, err := new(ClientService).Guide()
	if err != nil || loaded.HTML != guide.HTML || loaded.UpdatedAt != guide.UpdatedAt {
		t.Fatalf("saved guide changed on read: %v", err)
	}
}

func TestClientGuideStripsStoredXSSAndUnsafeURLs(t *testing.T) {
	clientTestDB(t)
	s := new(ClientService)
	raw := `<script>alert('secret')</script><style>body{display:none}</style>` +
		`<div onclick="alert(1)" id="app" class="ant-modal"><p>正常内容</p></div>` +
		`<a href="javascript:alert(1)">bad</a><a href="jav&#x61;script:alert(1)">encoded</a><a href="data:text/html;base64,PHNjcmlwdD4=">html</a>` +
		`<a href="file:///etc/passwd">file</a><a href="//example.com/path">relative</a>` +
		`<img src="javascript:alert(1)" onerror="alert(1)"><img src="data:image/svg+xml;base64,PHN2ZyBvbmxvYWQ9ImFsZXJ0KDEpIj48L3N2Zz4=">` +
		`<img src="data:image/png;base64,INVALID!"><img src="https://example.com/a.png" srcset="https://bad.example/x 2x" onload="alert(1)">` +
		`<iframe src="https://bad.example/"></iframe><svg onload="alert(1)"><circle></circle></svg><math><mtext>x</mtext></math>` +
		`<form action="https://bad.example/"><input name="password"><button>发送</button></form><object data="https://bad.example/"></object>` +
		`<span style="color:expression(alert(1));background-image:url(javascript:alert(1));position:fixed;behavior:url(x);font-family:url(javascript:alert(1));">style</span>`
	guide, err := s.SaveGuide(raw)
	if err != nil {
		t.Fatal(err)
	}
	lower := strings.ToLower(guide.HTML)
	for _, forbidden := range []string{"<script", "<style", "onclick", "onerror", "onload", "javascript:", "data:text/html", "data:image/svg", "invalid!", "file:", `href="//`, "srcset", "<iframe", "<svg", "<math", "<form", "<input", "<button", "<object", "expression(", "background-image", "position:", "behavior:", `id="app"`, `class="ant-modal"`} {
		if strings.Contains(lower, forbidden) {
			t.Errorf("unsafe content survived %q: %s", forbidden, guide.HTML)
		}
	}
	if !strings.Contains(guide.HTML, "正常内容") || !strings.Contains(guide.HTML, "https://example.com/a.png") {
		t.Fatal("safe content was lost while removing attacks")
	}
	var stored model.Setting
	if err := database.GetDB().Where("key = ?", clientGuideSettingKey).First(&stored).Error; err != nil {
		t.Fatal(err)
	}
	var persisted ClientGuide
	if json.Unmarshal([]byte(stored.Value), &persisted) != nil || persisted.HTML != guide.HTML {
		t.Fatal("database did not persist the sanitized HTML")
	}
	// A restored record is also sanitized when read, regardless of its origin.
	restored, _ := json.Marshal(&ClientGuide{HTML: raw, UpdatedAt: 123})
	if err := database.GetDB().Model(&stored).Update("value", string(restored)).Error; err != nil {
		t.Fatal(err)
	}
	loaded, err := s.Guide()
	if err != nil || loaded.HTML != guide.HTML || loaded.UpdatedAt != 123 {
		t.Fatal("reading a restored record bypassed HTML sanitization")
	}
}

func TestClientGuidePreservesDocumentAnchors(t *testing.T) {
	clientTestDB(t)
	s := new(ClientService)
	raw := `<p><a href="#install" target="_blank">安装步骤</a> <a href="#%E5%B8%B8%E8%A7%81%E9%97%AE%E9%A2%98">常见问题</a> <a href="#legacy">旧式锚点</a></p>` +
		`<h2 id="install">安装步骤</h2><h2 id="常见问题">常见问题</h2><a name="legacy"></a>` +
		`<p data-guide-anchor="existing">已保存的锚点</p><h3 id="app">同名标记</h3>` +
		`<a href="https://example.com/manual?a=1&amp;b=2">外部教程</a>`
	guide, err := s.SaveGuide(raw)
	if err != nil {
		t.Fatal(err)
	}
	for _, expected := range []string{`href="#install"`, `href="#%E5%B8%B8%E8%A7%81%E9%97%AE%E9%A2%98"`, `href="#legacy"`, `data-guide-anchor="install"`, `data-guide-anchor="常见问题"`, `data-guide-anchor="legacy"`, `data-guide-anchor="existing"`, `data-guide-anchor="app"`, `href="https://example.com/manual?a=1&amp;b=2"`} {
		if !strings.Contains(guide.HTML, expected) {
			t.Errorf("document anchor lost %q: %s", expected, guide.HTML)
		}
	}
	for _, forbidden := range []string{` id=`, ` name=`} {
		if strings.Contains(guide.HTML, forbidden) {
			t.Errorf("guide anchor leaked into panel DOM namespace: %s", guide.HTML)
		}
	}
	loaded, err := new(ClientService).Guide()
	if err != nil || loaded.HTML != guide.HTML {
		t.Fatalf("anchors changed when reading the saved guide: %v", err)
	}
	// Raw HTML restored from a backup uses the same anchor normalization.
	restored, _ := json.Marshal(&ClientGuide{HTML: raw, UpdatedAt: 123})
	if err := database.GetDB().Model(&model.Setting{}).Where("key = ?", clientGuideSettingKey).Update("value", string(restored)).Error; err != nil {
		t.Fatal(err)
	}
	loaded, err = s.Guide()
	if err != nil || loaded.HTML != guide.HTML || loaded.UpdatedAt != 123 {
		t.Fatalf("restored document anchors were not normalized: %v", err)
	}
}

func TestClientGuideAnchorsDoNotAllowOtherRelativeURLs(t *testing.T) {
	raw := `<a href="//bad.example/path">protocol-relative</a><a href="/xui/settings">absolute path</a>` +
		`<a href="../settings">relative path</a><a href="?action=delete">query</a><a href="javascript:alert(1)">script</a>` +
		`<img src="//bad.example/x.png"><img src="/xui/settings"><img src="#install"><img src="data:text/html;base64,PHNjcmlwdD4=">` +
		`<h2 id="bad name" data-guide-anchor="bad name">invalid</h2><a name="bad/name"></a>` +
		`<a href="#install" onclick="alert(1)">valid</a><h2 id="install">标题</h2><img src="https://example.com/image.png">`
	clean := sanitizeClientGuide(raw)
	for _, forbidden := range []string{`href="//`, `href="/`, `href="..`, `href="?`, "javascript:", "onclick", `src="//`, `src="/`, `src="#`, "data:text/html", `data-guide-anchor="bad`, ` id=`, ` name=`} {
		if strings.Contains(clean, forbidden) {
			t.Errorf("unsafe attribute survived %q: %s", forbidden, clean)
		}
	}
	for _, expected := range []string{`href="#install"`, `data-guide-anchor="install"`, `src="https://example.com/image.png"`} {
		if !strings.Contains(clean, expected) {
			t.Errorf("safe content lost %q: %s", expected, clean)
		}
	}
}

func TestClientGuideEmptyLimitsAndIndependentSettings(t *testing.T) {
	clientTestDB(t)
	s := new(ClientService)
	guide, err := s.Guide()
	if err != nil || guide.HTML != "" || guide.UpdatedAt != 0 {
		t.Fatal("new installation should have an empty guide")
	}
	guide, err = s.SaveGuide("<p>独立说明</p>")
	if err != nil {
		t.Fatal(err)
	}
	if err := s.SaveSettings(&ClientSettings{Enabled: false, PublicURL: "https://example.com/panel/"}); err != nil {
		t.Fatal(err)
	}
	loaded, err := s.Guide()
	if err != nil || loaded.HTML != guide.HTML || loaded.UpdatedAt != guide.UpdatedAt {
		t.Fatal("saving client settings overwrote the usage guide")
	}
	if _, err := s.SaveGuide(strings.Repeat("a", ClientGuideMaxSize+1)); err == nil {
		t.Fatal("oversized HTML was accepted")
	}
	loaded, _ = s.Guide()
	if loaded.HTML != guide.HTML {
		t.Fatal("rejected oversized save changed the existing guide")
	}
	boundary := strings.Repeat("a", ClientGuideMaxSize)
	if saved, err := s.SaveGuide(boundary); err != nil || len(saved.HTML) != ClientGuideMaxSize {
		t.Fatalf("20MB boundary should be accepted: %v", err)
	}
	guide, err = s.SaveGuide(" \n\t ")
	if err != nil || guide.HTML != "" || guide.UpdatedAt <= 0 {
		t.Fatal("clearing the guide did not take effect")
	}
	loaded, err = s.Guide()
	if err != nil || loaded.HTML != "" {
		t.Fatal("cleared guide was not empty on the next request")
	}
	cfg, err := s.Settings()
	if err != nil || cfg.Enabled || cfg.PublicURL != "https://example.com/panel/" {
		t.Fatal("saving the guide changed client settings")
	}
}
