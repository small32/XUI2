package service

import (
	"encoding/base64"
	"encoding/json"
	"errors"
	"net/url"
	"regexp"
	"strings"
	"sync"
	"time"
	"unicode"
	"xui/database"
	"xui/database/model"

	"github.com/microcosm-cc/bluemonday"
)

const ClientGuideMaxSize = 20 * 1024 * 1024
const clientGuideSettingKey = "clientUsageGuide"

type ClientGuide struct {
	HTML      string `json:"html"`
	UpdatedAt int64  `json:"updatedAt"`
}

var clientGuideMu sync.Mutex
var clientGuidePolicy = newClientGuidePolicy()

func newClientGuidePolicy() *bluemonday.Policy {
	p := bluemonday.NewPolicy()
	p.AllowElements("p", "div", "span", "h1", "h2", "h3", "h4", "h5", "h6",
		"strong", "b", "em", "i", "u", "s", "strike", "sub", "sup", "br", "hr",
		"ul", "ol", "li", "blockquote", "pre", "code", "a", "img", "font")
	p.AllowLists()
	p.AllowTables()
	p.AllowAttrs("title").Matching(bluemonday.Paragraph).Globally()
	p.AllowAttrs("align").Matching(bluemonday.CellAlign).OnElements("p", "div", "h1", "h2", "h3", "h4", "h5", "h6")
	p.AllowAttrs("href").Matching(regexp.MustCompile(`(?i)^https?://`)).OnElements("a")
	p.AllowAttrs("src").OnElements("img")
	p.AllowAttrs("alt").Matching(bluemonday.Paragraph).OnElements("img")
	p.AllowAttrs("width", "height").Matching(bluemonday.NumberOrPercent).OnElements("img")
	// Older browser editing commands can emit font elements instead of span styles.
	p.AllowAttrs("face").Matching(regexp.MustCompile(`^[\p{L}\p{N} _,'"-]{1,120}$`)).OnElements("font")
	p.AllowAttrs("color").Matching(regexp.MustCompile(`(?i)^(#[0-9a-f]{3,8}|[a-z]{1,30})$`)).OnElements("font")
	p.AllowAttrs("size").Matching(regexp.MustCompile(`^[+-]?[1-7]$`)).OnElements("font")
	p.RequireParseableURLs(true)
	p.AllowRelativeURLs(false)
	p.AllowURLSchemes("http", "https")
	// The library's image helper also permits SVG; this guide only accepts raster data.
	p.AllowURLSchemeWithCustomPolicy("data", clientGuideRasterDataURL)
	p.RequireNoFollowOnLinks(true)
	p.RequireNoReferrerOnLinks(true)
	p.AddTargetBlankToFullyQualifiedLinks(true)
	// bluemonday parses declarations and validates each allowed CSS property.
	// Positioning, external CSS/image URLs, scripts and event attributes remain forbidden.
	p.AllowAttrs("style").Globally()
	p.AllowStyles("color", "background-color", "font-size", "font-weight", "font-style",
		"text-align", "text-decoration", "line-height", "vertical-align", "white-space",
		"margin", "margin-left", "margin-right", "margin-top", "margin-bottom",
		"padding", "padding-left", "padding-right", "padding-top", "padding-bottom").Globally()
	p.AllowStyles("font-family").MatchingHandler(clientGuideFontFamily).Globally()
	p.AllowStyles("width", "height", "max-width", "max-height").OnElements("img", "table")
	p.AllowStyles("border", "border-collapse", "border-spacing", "border-color", "border-style", "border-width").OnElements("table", "td", "th")
	return p
}

func clientGuideRasterDataURL(u *url.URL) bool {
	if u.RawQuery != "" || u.Fragment != "" {
		return false
	}
	comma := strings.IndexByte(u.Opaque, ',')
	if comma < 0 {
		return false
	}
	switch strings.ToLower(u.Opaque[:comma]) {
	case "image/png;base64", "image/jpeg;base64", "image/gif;base64", "image/webp;base64":
	default:
		return false
	}
	_, err := base64.StdEncoding.DecodeString(u.Opaque[comma+1:])
	return err == nil && len(u.Opaque) > comma+1
}

func clientGuideFontFamily(value string) bool {
	if strings.TrimSpace(value) == "" || len(value) > 256 {
		return false
	}
	// Font family names can include Chinese characters. Restrict the CSS value to
	// names and separators, excluding functions, escapes, declarations and URLs.
	for _, char := range value {
		if !unicode.IsLetter(char) && !unicode.IsNumber(char) && !unicode.IsSpace(char) && !strings.ContainsRune("_-'\",", char) {
			return false
		}
	}
	return true
}

func (s *ClientService) Guide() (*ClientGuide, error) {
	var row model.Setting
	err := database.GetDB().Where("key = ?", clientGuideSettingKey).First(&row).Error
	if database.IsNotFound(err) {
		return &ClientGuide{}, nil
	}
	if err != nil {
		return nil, err
	}
	var guide ClientGuide
	if err := json.Unmarshal([]byte(row.Value), &guide); err != nil {
		return nil, errors.New("使用说明内容无效，请重新保存")
	}
	if len(guide.HTML) > ClientGuideMaxSize {
		return nil, errors.New("使用说明内容超过 20MB")
	}
	// Sanitize reads as well, including records restored from an older backup.
	guide.HTML = clientGuidePolicy.Sanitize(guide.HTML)
	if len(guide.HTML) > ClientGuideMaxSize {
		return nil, errors.New("使用说明内容超过 20MB")
	}
	return &guide, nil
}

func (s *ClientService) SaveGuide(raw string) (*ClientGuide, error) {
	if len(raw) > ClientGuideMaxSize {
		return nil, errors.New("使用说明内容不能超过 20MB")
	}
	html := clientGuidePolicy.Sanitize(strings.TrimSpace(raw))
	if len(html) > ClientGuideMaxSize {
		return nil, errors.New("使用说明内容不能超过 20MB")
	}
	clientGuideMu.Lock()
	defer clientGuideMu.Unlock()
	guide := &ClientGuide{HTML: html, UpdatedAt: time.Now().Unix()}
	data, err := json.Marshal(guide)
	if err != nil {
		return nil, err
	}
	if err := setValue(database.GetDB(), clientGuideSettingKey, string(data)); err != nil {
		return nil, err
	}
	return guide, nil
}
