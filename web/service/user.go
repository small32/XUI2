package service

import (
	"errors"
	"strings"

	"golang.org/x/crypto/bcrypt"
	"x-ui/database"
	"x-ui/database/model"
	"x-ui/logger"

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
		First(user).
		Error
	if err != nil {
		return nil, err
	}
	return user, nil
}

func (s *UserService) CheckUser(username string, password string) *model.User {
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
	db := database.GetDB()
	hashed, err := HashPassword(password)
	if err != nil {
		return err
	}
	return db.Model(model.User{}).
		Where("id = ?", id).
		Update("username", username).
		Update("password", hashed).
		Error
}

func (s *UserService) UpdateFirstUser(username string, password string) error {
	if username == "" {
		return errors.New("username can not be empty")
	} else if password == "" {
		return errors.New("password can not be empty")
	}
	hashed, err := HashPassword(password)
	if err != nil {
		return err
	}
	db := database.GetDB()
	user := &model.User{}
	err = db.Model(model.User{}).First(user).Error
	if database.IsNotFound(err) {
		user.Username = username
		user.Password = hashed
		return db.Model(model.User{}).Create(user).Error
	} else if err != nil {
		return err
	}
	user.Username = username
	user.Password = hashed
	return db.Save(user).Error
}
