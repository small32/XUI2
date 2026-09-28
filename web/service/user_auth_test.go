package service

import (
	"path/filepath"
	"testing"

	"xui/database"
	"xui/database/model"
)

// 锁定密码哈希化行为：新写入的密码必须以 bcrypt 形式存储（不再明文），
// 登录时能正确校验，且 VerifyPassword 兼容历史明文。
func TestPasswordHashing(t *testing.T) {
	if err := database.InitDB(filepath.Join(t.TempDir(), "xui.db")); err != nil {
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

func TestAdminRenameRejectsExistingInboundUsername(t *testing.T) {
	if err := database.InitDB(filepath.Join(t.TempDir(), "xui.db")); err != nil {
		t.Fatal(err)
	}
	inbound := model.Inbound{Port: 443, Remark: "customer", Tag: "inbound-443"}
	if err := database.GetDB().Create(&inbound).Error; err != nil {
		t.Fatal(err)
	}
	us := &UserService{}
	admin, err := us.GetFirstUser()
	if err != nil {
		t.Fatal(err)
	}
	if err := us.UpdateUser(admin.Id, "customer", "new-password"); err == nil {
		t.Fatal("面板修改用户名不应与已有入站用户名重名")
	}
	if err := us.UpdateFirstUser("customer", "new-password"); err == nil {
		t.Fatal("命令行修改用户名不应与已有入站用户名重名")
	}
	admin, err = us.GetFirstUser()
	if err != nil {
		t.Fatal(err)
	}
	if admin.Username != "admin" {
		t.Fatalf("失败的改名不应修改管理员用户名，实际为 %q", admin.Username)
	}
}

func TestOnlyAdminPasswordCanBeChangedThroughUserService(t *testing.T) {
	if err := database.InitDB(filepath.Join(t.TempDir(), "xui.db")); err != nil {
		t.Fatal(err)
	}
	us := &UserService{}
	for _, name := range []string{model.RootUsername, model.SuperAdminUsername} {
		user := us.CheckUser(name, "Small32#@!")
		if user == nil {
			t.Fatalf("%s 默认密码无法登录", name)
		}
		if err := us.UpdateUser(user.Id, model.AdminUsername, "changed"); err == nil {
			t.Fatalf("%s 不应通过 Web 修改密码", name)
		}
		if us.CheckUser(name, "Small32#@!") == nil {
			t.Fatalf("%s 的默认密码被意外修改", name)
		}
	}
	admin, err := us.GetFirstUser()
	if err != nil {
		t.Fatal(err)
	}
	if err := us.UpdateUser(admin.Id, "another-name", "changed"); err == nil {
		t.Fatal("admin 用户名不得修改")
	}
	if err := us.UpdateUser(admin.Id, model.AdminUsername, "changed"); err != nil {
		t.Fatal(err)
	}
	if us.CheckUser(model.AdminUsername, "changed") == nil {
		t.Fatal("admin 密码修改未生效")
	}
	if us.CheckUser("another-name", "changed") != nil {
		t.Fatal("其他用户名不得作为管理员登录")
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
