package entity

import (
	"crypto/tls"
	"encoding/json"
	"net"
	"strings"
	"time"
	"xui/util/common"
	"xui/xray"
)

type Msg struct {
	Success bool        `json:"success"`
	Msg     string      `json:"msg"`
	Obj     interface{} `json:"obj"`
}

type Pager struct {
	Current  int         `json:"current"`
	PageSize int         `json:"page_size"`
	Total    int         `json:"total"`
	OrderBy  string      `json:"order_by"`
	Desc     bool        `json:"desc"`
	Key      string      `json:"key"`
	List     interface{} `json:"list"`
}

type AllSetting struct {
	ServerName         string `json:"serverName" form:"serverName"`
	WebListen          string `json:"webListen" form:"webListen"`
	WebPort            int    `json:"webPort" form:"webPort"`
	WebCertFile        string `json:"webCertFile" form:"webCertFile"`
	WebKeyFile         string `json:"webKeyFile" form:"webKeyFile"`
	WebBasePath        string `json:"webBasePath" form:"webBasePath"`
	XrayTemplateConfig string `json:"xrayTemplateConfig" form:"xrayTemplateConfig"`

	// RestrictedLoginEnable 是否允许非管理员用户（凭入站备注/用户名+入站密码）登录。
	// 关闭时禁止受限账号登录面板。
	RestrictedLoginEnable bool `json:"restrictedLoginEnable" form:"restrictedLoginEnable"`

	TimeLocation string `json:"timeLocation" form:"timeLocation"`

	// ExternalHost 被控端对外可达的主机名/域名（用于拼接 API 与订阅地址）。
	// 仅 agent 使用；留空时用监听地址回退。
	ExternalHost string `json:"externalHost" form:"externalHost"`
}

func (s *AllSetting) CheckValid() error {
	if s.WebListen != "" {
		ip := net.ParseIP(s.WebListen)
		if ip == nil {
			return common.NewError("web listen is not valid ip:", s.WebListen)
		}
	}

	if s.WebPort <= 0 || s.WebPort > 65535 {
		return common.NewError("web port is not a valid port:", s.WebPort)
	}

	if s.WebCertFile != "" || s.WebKeyFile != "" {
		_, err := tls.LoadX509KeyPair(s.WebCertFile, s.WebKeyFile)
		if err != nil {
			return common.NewErrorf("cert file <%v> or key file <%v> invalid: %v", s.WebCertFile, s.WebKeyFile, err)
		}
	}

	if !strings.HasPrefix(s.WebBasePath, "/") {
		s.WebBasePath = "/" + s.WebBasePath
	}
	if !strings.HasSuffix(s.WebBasePath, "/") {
		s.WebBasePath += "/"
	}

	xrayConfig := &xray.Config{}
	err := json.Unmarshal([]byte(s.XrayTemplateConfig), xrayConfig)
	if err != nil {
		return common.NewError("xray template config invalid:", err)
	}

	_, err = time.LoadLocation(s.TimeLocation)
	if err != nil {
		return common.NewError("time location not exist:", s.TimeLocation)
	}

	return nil
}


// AgentConnectionInfo 是被控端在自己面板设置页展示的只读连接信息。
// Token 与指纹由服务层在请求时动态计算，不落库、不来自 AllSetting。
type AgentConnectionInfo struct {
	ApiUrl      string `json:"apiUrl"`
	SubscribeUrl string `json:"subscribeUrl"`
	Port        int    `json:"port"`
	CertSha256  string `json:"certSha256"`
	Token       string `json:"token"`
}
