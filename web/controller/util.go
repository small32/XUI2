package controller

import (
	"github.com/gin-gonic/gin"
	"github.com/nicksnyder/go-i18n/v2/i18n"
	"net"
	"net/http"
	"x-ui/config"
	"x-ui/logger"
	"x-ui/util/common"
	"x-ui/web/entity"
	"x-ui/web/session"
)

func getUriId(c *gin.Context) int64 {
	s := struct {
		Id int64 `uri:"id"`
	}{}

	_ = c.BindUri(&s)
	return s.Id
}

func getRemoteIp(c *gin.Context) string {
	// Do not trust a client supplied forwarding header for login throttling.
	// If a trusted reverse proxy is added later, its network must be validated
	// before using X-Forwarded-For.
	addr := c.Request.RemoteAddr
	ip, _, _ := net.SplitHostPort(addr)
	if ip == "" {
		return addr
	}
	return ip
}

func jsonMsg(c *gin.Context, msg string, err error) {
	jsonMsgObj(c, msg, nil, err)
}

func jsonObj(c *gin.Context, obj interface{}, err error) {
	jsonMsgObj(c, "", obj, err)
}

func jsonMsgObj(c *gin.Context, msg string, obj interface{}, err error) {
	m := entity.Msg{
		Obj: obj,
	}
	if err == nil {
		m.Success = true
		if msg != "" {
			m.Msg = msg + "成功"
		}
	} else {
		m.Success = false
		m.Msg = msg + "失败: " + err.Error()
		logger.Warning(msg+"失败: ", err)
	}
	c.JSON(http.StatusOK, m)
}

func pureJsonMsg(c *gin.Context, success bool, msg string) {
	if success {
		c.JSON(http.StatusOK, entity.Msg{
			Success: true,
			Msg:     msg,
		})
	} else {
		c.JSON(http.StatusOK, entity.Msg{
			Success: false,
			Msg:     msg,
		})
	}
}

func html(c *gin.Context, name string, title string, data gin.H) {
	if data == nil {
		data = gin.H{}
	}
	data["title"] = title
	data["request_uri"] = c.Request.RequestURI
	data["base_path"] = c.GetString("base_path")
	data["restricted"] = session.IsRestricted(c)
	data["role"] = config.Role()
	// 把当前请求的 localizer 注入模板：每个请求有自己独立的翻译器，
	// 避免多个并发请求共享同一变量的数据竞争与语言串台。
	data["i18n"] = i18nFunc(c)
	c.HTML(http.StatusOK, name, getContext(data))
}

// i18nFunc 返回一个绑定到当前请求 localizer 的翻译闭包，
// 供模板以 {{ .i18n "key" }} 形式调用。参数按 key 里的 {{name}} 占位符对齐。
func i18nFunc(c *gin.Context) func(key string, params ...string) (interface{}, error) {
	loc, _ := c.Get("localizer")
	l, _ := loc.(*i18n.Localizer)
	return func(key string, params ...string) (interface{}, error) {
		if l == nil {
			return key, nil
		}
		names := findI18nParamNames(key)
		if len(names) != len(params) {
			return "", common.NewError("find names:", names, "---------- params:", params, "---------- num not equal")
		}
		templateData := map[string]interface{}{}
		for i := range names {
			templateData[names[i]] = params[i]
		}
		return l.Localize(&i18n.LocalizeConfig{
			MessageID:    key,
			TemplateData: templateData,
		})
	}
}

// findI18nParamNames 解析翻译 key 里 {{name}} 形式的占位符名。
func findI18nParamNames(key string) []string {
	names := make([]string, 0)
	keyLen := len(key)
	for i := 0; i < keyLen-1; i++ {
		if key[i:i+2] == "{{" {
			j := i + 2
			isFind := false
			for ; j < keyLen-1; j++ {
				if key[j:j+2] == "}}" {
					isFind = true
					break
				}
			}
			if isFind {
				// "{{" 之后即占位符名，从 i+2 开始取，到 "}}" 的起始位置 j。
				names = append(names, key[i+2:j])
			}
		}
	}
	return names
}

func getContext(h gin.H) gin.H {
	a := gin.H{
		"cur_ver": config.GetVersion(),
	}
	if h != nil {
		for key, value := range h {
			a[key] = value
		}
	}
	return a
}

func isAjax(c *gin.Context) bool {
	return c.GetHeader("X-Requested-With") == "XMLHttpRequest"
}
