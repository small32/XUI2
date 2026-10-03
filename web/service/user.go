package service

import (
	"errors"
	"strings"

	"golang.org/x/crypto/bcrypt"
	"xui/database"
	"xui/database/model"
	"xui/logger"

	"gorm.io/gorm"
)

// bcrypt 开销因子。过高会拖慢登录，过低不安全；12 是常见折中。
const bcryptCost = 12

// HashPassword 返回密码的 bcrypt 哈希。失败时返回 error。
func HashPassword(password string) (string, error) {
	b, err := bcrypt.GenerateFromPassword([]byte(password), bcryptCost)
	if err != nil {
		return "", err
	}
	return string(b), nil
}

// VerifyPassword 校验明文密码是否匹配存储的（可能是 bcrypt 哈希或历史明文）密码。
// 返回 ok 表示匹配；返回 isPlain 表示存储的是明文（需升级），此时 ok 为 true 且由调用方回写哈希。
func VerifyPassword(stored, password string) (ok, isPlain bool) {
	// bcrypt 哈希固定以 $2a$/$2b$/$2y$ 开头。
	if strings.HasPrefix(stored, "$2a$") || strings.HasPrefix(stored, "$2b$") || strings.HasPrefix(stored, "$2y$") {
		err := bcrypt.CompareHashAndPassword([]byte(stored), []byte(password))
		return err == nil, false
	}
	// 历史明文（老库）：直接比对。
	return stored == password, true
}

type UserService struct {
}

func (s *UserService) GetFirstUser() (*model.User, error) {
	db := database.GetDB()

	user := &model.User{}
	err := db.Model(model.User{}).
		Where("username = ?", model.AdminUsername).First(user).
		Error
	if err != nil {
		return nil, err
	}
	return user, nil
}

func (s *UserService) CheckUser(username string, password string) *model.User {
	if !model.IsPanelAdminUsername(username) {
		return nil
	}
	db := database.GetDB()

	user := &model.User{}
	err := db.Model(model.User{}).
		Where("username = ?", username).
		First(user).
		Error
	if err == gorm.ErrRecordNotFound {
		return nil
	} else if err != nil {
		logger.Warning("check user err:", err)
		return nil
	}
	ok, isPlain := VerifyPassword(user.Password, password)
	if !ok {
		return nil
	}
	if isPlain {
		// 老库存的是明文：登录成功后原地升级为 bcrypt 哈希。
		if hashed, hErr := HashPassword(password); hErr == nil {
			_ = db.Model(model.User{}).Where("id = ?", user.Id).Update("password", hashed).Error
			user.Password = hashed
		}
	}
	return user
}

func (s *UserService) UpdateUser(id int, username string, password string) error {
	if username != model.AdminUsername {
		return errors.New("管理员用户名固定为 admin")
	}
	if password == "" {
		return errors.New("password can not be empty")
	}
	db := database.GetDB()
	hashed, err := HashPassword(password)
	if err != nil {
		return err
	}
	result := db.Model(model.User{}).Where("id = ? AND username = ?", id, model.AdminUsername).
		Update("password", hashed)
	if result.Error != nil {
		return result.Error
	}
	if result.RowsAffected != 1 {
		return errors.New("只有 admin 账号可在面板修改密码")
	}
	return nil
}

// MustChangeReservedPassword reads the current database flag, so an existing
// session cannot bypass a newly required password change.
func (s *UserService) MustChangeReservedPassword(id int) (bool, error) {
	var user model.User
	if err := database.GetDB().First(&user, id).Error; err != nil {
		return false, err
	}
	return (user.Username == model.RootUsername || user.Username == model.SuperAdminUsername) && user.MustChangePassword, nil
}

func (s *UserService) ChangeReservedPassword(id int, oldPassword, newPassword string) error {
	var user model.User
	if err := database.GetDB().First(&user, id).Error; err != nil {
		return err
	}
	if (user.Username != model.RootUsername && user.Username != model.SuperAdminUsername) || !user.MustChangePassword {
		return errors.New("该账号不需要首次修改密码")
	}
	if ok, _ := VerifyPassword(user.Password, oldPassword); !ok {
		return errors.New("原密码错误")
	}
	if len(newPassword) < 12 {
		return errors.New("新密码至少需要 12 个字符")
	}
	if newPassword == oldPassword {
		return errors.New("新密码不能与原密码相同")
	}
	hashed, err := HashPassword(newPassword)
	if err != nil {
		return err
	}
	result := database.GetDB().Model(&model.User{}).
		Where("id = ? AND password = ? AND must_change_password = ?", user.Id, user.Password, true).
		Updates(map[string]interface{}{"password": hashed, "must_change_password": false})
	if result.Error != nil {
		return result.Error
	}
	if result.RowsAffected != 1 {
		return errors.New("密码已变化，请重新登录")
	}
	return nil
}

func (s *UserService) UpdateFirstUser(username string, password string) error {
	if username != model.AdminUsername {
		return errors.New("管理员用户名固定为 admin")
	} else if password == "" {
		return errors.New("password can not be empty")
	}
	hashed, err := HashPassword(password)
	if err != nil {
		return err
	}
	db := database.GetDB()
	user := &model.User{}
	err = db.Model(model.User{}).Where("username = ?", model.AdminUsername).First(user).Error
	if database.IsNotFound(err) {
		user.Username = model.AdminUsername
		user.Password = hashed
		return db.Model(model.User{}).Create(user).Error
	} else if err != nil {
		return err
	}
	user.Password = hashed
	return db.Save(user).Error
}
