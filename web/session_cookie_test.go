package web

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-contrib/sessions"
	"github.com/gin-gonic/gin"
	"github.com/gorilla/securecookie"
)

func TestSessionCookieEncryptsSessionValues(t *testing.T) {
	gin.SetMode(gin.TestMode)
	const hash = "$2a$12$not-a-real-password-hash"
	engine := gin.New()
	engine.Use(sessions.Sessions("session", newSessionStore([]byte("test-signing-secret"))))
	engine.GET("/set", func(c *gin.Context) {
		sessions.Default(c).Set("passwordHash", hash)
		if err := sessions.Default(c).Save(); err != nil {
			t.Error(err)
		}
		c.Status(http.StatusNoContent)
	})
	engine.GET("/read", func(c *gin.Context) {
		if got := sessions.Default(c).Get("passwordHash"); got != hash {
			c.String(http.StatusBadRequest, "session value did not survive cookie round trip")
			return
		}
		c.Status(http.StatusNoContent)
	})

	first := httptest.NewRecorder()
	engine.ServeHTTP(first, httptest.NewRequest(http.MethodGet, "/set", nil))
	cookie := first.Result().Cookies()[0]
	if strings.Contains(cookie.Value, hash) {
		t.Fatal("session cookie exposes the password hash")
	}
	var plaintextPayload map[interface{}]interface{}
	legacyDecoder := securecookie.New([]byte("test-signing-secret"), nil)
	if err := legacyDecoder.Decode("session", cookie.Value, &plaintextPayload); err == nil {
		t.Fatal("encrypted session cookie was readable with the signing key only")
	}
	second := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodGet, "/read", nil)
	request.AddCookie(cookie)
	engine.ServeHTTP(second, request)
	if second.Code != http.StatusNoContent {
		t.Fatalf("encrypted session cookie could not be read: status=%d body=%s", second.Code, second.Body.String())
	}
}
