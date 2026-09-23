package service

import (
	"path/filepath"
	"testing"

	"x-ui/database"
)

// 锁定密码哈希化行为：新写入的密码必须以 bcrypt 形式存储（不再明文），
// 登录时能正确校验，且 VerifyPassword 兼容历史明文。
func TestPasswordHashing(t *testing.T) {
	if err := database.InitDB(filepath.Join(t.TempDir(), "x-ui.db")); err != nil {
		t.Fatal(err)
	}
	us := &UserService{}
	if err := us.UpdateFirstUser("admin", "secret123"); err != nil {
		t.Fatal(err)
	}

	user, err := us.GetFirstUser()
	if err != nil {
		t.Fatal(err)
	}
	if user.Password == "secret123" {
		t.Fatal("密码必须以哈希形式存储，不能是明文")
	}
	if !hashLike(user.Password) {
		t.Fatalf("密码应保存为 bcrypt 哈希，实际: %q", user.Password)
	}

	// 正确密码能登录。
	if u := us.CheckUser("admin", "secret123"); u == nil {
		t.Fatal("正确密码应能登录")
	}
	// 错误密码被拒绝。
	if u := us.CheckUser("admin", "wrong"); u != nil {
		t.Fatal("错误密码不应登录")
	}
}

// VerifyPassword 应同时兼容历史明文与 bcrypt 哈希。
func TestVerifyPasswordCompat(t *testing.T) {
	// bcrypt 哈希路径。
	hashed, err := HashPassword("abc")
	if err != nil {
		t.Fatal(err)
	}
	if ok, isPlain := VerifyPassword(hashed, "abc"); !ok || isPlain {
		t.Fatalf("bcrypt 匹配应 ok=true,isPlain=false，得到 ok=%v isPlain=%v", ok, isPlain)
	}
	if ok, _ := VerifyPassword(hashed, "xyz"); ok {
		t.Fatal("错误密码不应匹配哈希")
	}
	// 历史明文路径。
	if ok, isPlain := VerifyPassword("legacy-plain", "legacy-plain"); !ok || !isPlain {
		t.Fatalf("明文匹配应 ok=true,isPlain=true，得到 ok=%v isPlain=%v", ok, isPlain)
	}
}

func hashLike(s string) bool {
	return len(s) == 60 && s[:4] == "$2a$"
}