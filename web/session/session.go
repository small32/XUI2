package session

import (
	"crypto/sha256"
	"encoding/gob"
	"fmt"
	"github.com/gin-contrib/sessions"
	"github.com/gin-gonic/gin"
	"x-ui/database"
	"x-ui/database/model"
)

const (
	loginUser        = "LOGIN_USER"
	loginInboundId   = "LOGIN_INBOUND_ID"
	loginPasswordFgp = "LOGIN_INBOUND_PASSWORD_FINGERPRINT"
	loginUserHash    = "LOGIN_USER_PASSWORD_HASH"
)

func init() {
	gob.Register(model.User{})
}

func SetLoginUser(c *gin.Context, user *model.User) error {
	// 密码只用于登录校验，绝不允许进入客户端会话 Cookie。
	// 这里的 key 未加密，仅签名，gob 序列化后可被 base64 还原，因此必须清空。
	if user == nil {
		return fmt.Errorf("user cannot be nil")
	}
	if user.Password == "" {
		return fmt.Errorf("user password hash cannot be empty")
	}
	safeUser := *user
	safeUser.Password = ""
	s := sessions.Default(c)
	s.Set(loginUser, safeUser)
	s.Set(loginUserHash, user.Password)
	// Switching to an administrator session must not retain a prior
	// restricted-login binding.
	s.Delete(loginInboundId)
	s.Delete(loginPasswordFgp)
	return s.Save()
}

func GetLoginUser(c *gin.Context) *model.User {
	s := sessions.Default(c)
	obj := s.Get(loginUser)
	if obj == nil {
		return nil
	}
	user, ok := obj.(model.User)
	if !ok {
		return nil
	}
	return &user
}

// IsLogin 是否为管理员已登录（持有真实用户会话）。
// 受限登录不写入占位用户，因此受限会话下 IsLogin 为 false，
// 从而受限账号无法通过 checkLogin 进入 /server 等管理接口。
func IsLogin(c *gin.Context) bool {
	u := GetLoginUser(c)
	if u == nil {
		return false
	}
	stored, ok := sessions.Default(c).Get(loginUserHash).(string)
	if !ok || stored == "" {
		return false
	}
	var current model.User
	if database.GetDB() == nil || database.GetDB().Where("id = ?", u.Id).First(&current).Error != nil {
		return false
	}
	return stored == current.Password
}

// IsAdminLogin 是否为管理员登录。受限登录不建立用户会话，故这里等价于 IsLogin。
func IsAdminLogin(c *gin.Context) bool {
	return IsLogin(c) && !IsRestricted(c)
}

// SetLoginInboundId 记录受限登录绑定的入站 id
func SetLoginInboundId(c *gin.Context, inboundId int) error {
	s := sessions.Default(c)
	s.Set(loginInboundId, inboundId)
	return s.Save()
}

// GetLoginInboundId 取受限登录绑定的入站 id，非受限登录返回 0
func GetLoginInboundId(c *gin.Context) int {
	s := sessions.Default(c)
	obj := s.Get(loginInboundId)
	if obj == nil {
		return 0
	}
	inboundId, ok := obj.(int)
	if !ok {
		return 0
	}
	return inboundId
}

// IsRestricted 是否为受限登录（仅能查看绑定入站）
func IsRestricted(c *gin.Context) bool {
	return GetLoginInboundId(c) > 0
}

// SetRestrictedLogin 建立受限登录会话，密码校验已在 service 层完成。
// 出于安全考虑，入站密码绝不写入客户端会话 Cookie，这里仅记录密码的
// SHA-256 指纹（单向，不泄露明文），供密码轮换后使旧会话失效。
// 受限登录不写入占位用户：受限账号走独立的受限路由，而非管理员判定。
func SetRestrictedLogin(c *gin.Context, inboundId int, password string) error {
	s := sessions.Default(c)
	s.Delete(loginUser)
	s.Delete(loginUserHash)
	s.Set(loginInboundId, inboundId)
	s.Set(loginPasswordFgp, restCredFingerprint(inboundId, password))
	return s.Save()
}

// IsRestrictedCredValid 校验受限会话是否仍然有效：比对会话记录的入站密码指纹
// 与当前库内密码指纹。管理员轮换入站密码后指纹变化，旧会话随即失效，
// 需以新密码重新受限登录。
func IsRestrictedCredValid(c *gin.Context, inboundId int, currentPassword string) bool {
	expected, ok := sessions.Default(c).Get(loginPasswordFgp).(string)
	if !ok || expected == "" {
		return false
	}
	return expected == restCredFingerprint(inboundId, currentPassword)
}

// restCredFingerprint 计算入站 id 与密码的 SHA-256 指纹。
func restCredFingerprint(inboundId int, password string) string {
	h := sha256.Sum256([]byte(fmt.Sprintf("%d\x00%s", inboundId, password)))
	return fmt.Sprintf("%x", h[:])
}

func ClearSession(c *gin.Context) {
	s := sessions.Default(c)
	s.Clear()
	s.Options(sessions.Options{
		Path:   "/",
		MaxAge: -1,
	})
	s.Save()
}
