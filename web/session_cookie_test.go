package web

import (
	"crypto/tls"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-contrib/sessions"
	"github.com/gin-gonic/gin"
	"github.com/gorilla/securecookie"
)

func TestSessionCookieSecureOnTLS(t *testing.T) {
	gin.SetMode(gin.TestMode)
	engine := gin.New()
	engine.Use(sessions.Sessions("session", newSessionStore([]byte("test-signing-secret"))))
	engine.Use(secureSessionCookies)
	engine.GET("/set", func(c *gin.Context) {
		sessions.Default(c).Set("user", "admin")
		if err := sessions.Default(c).Save(); err != nil {
			t.Error(err)
		}
	})
	for _, secure := range []bool{false, true} {
		req := httptest.NewRequest(http.MethodGet, "/set", nil)
		if secure {
			req.TLS = &tls.ConnectionState{}
		}
		w := httptest.NewRecorder()
		engine.ServeHTTP(w, req)
		cookies := w.Result().Cookies()
		if len(cookies) != 1 || cookies[0].Secure != secure {
			t.Fatalf("TLS=%t cookie secure mismatch: %+v", secure, cookies)
		}
	}
}

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
