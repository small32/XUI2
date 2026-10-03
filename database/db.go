package database

import (
	"crypto/rand"
	"errors"
	"fmt"
	"math/big"
	"os"
	"path"

	"golang.org/x/crypto/bcrypt"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
	"xui/config"
	"xui/database/model"
)

var db *gorm.DB

func initUser() error {
	if err := db.AutoMigrate(&model.User{}); err != nil {
		return err
	}
	var admin model.User
	err := db.Where("username = ?", model.AdminUsername).First(&admin).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		// Upgrade an existing administrator in place so inbound ownership and
		// the existing password survive the fixed-username migration.
		err = db.Where("username NOT IN ?", []string{model.RootUsername, model.SuperAdminUsername}).First(&admin).Error
		if err == nil {
			if err := db.Model(&admin).Update("username", model.AdminUsername).Error; err != nil {
				return err
			}
		} else if !errors.Is(err, gorm.ErrRecordNotFound) {
			return err
		} else {
			pass := randomPassword(14)
			hashed, err := bcrypt.GenerateFromPassword([]byte(pass), 12)
			if err != nil {
				return err
			}
			if err := db.Create(&model.User{Username: model.AdminUsername, Password: string(hashed)}).Error; err != nil {
				return err
			}
			fmt.Printf("首次初始化面板：默认用户名 admin，初始密码 %s（请尽快登录后修改）\n", pass)
		}
	} else if err != nil {
		return err
	}
	// These two accounts are provisioned once. Reinitialization preserves
	// existing password hashes and never prints their default credential.
	for _, username := range []string{model.RootUsername, model.SuperAdminUsername} {
		var user model.User
		err := db.Where("username = ?", username).First(&user).Error
		if err == nil {
			// Existing installations using the shared initial credential must
			// change it before accessing the management panel.
			plain, err := defaultAdminPassword()
			if err != nil {
				return err
			}
			if bcrypt.CompareHashAndPassword([]byte(user.Password), []byte(plain)) == nil || user.Password == plain {
				if err := db.Model(&user).Update("must_change_password", true).Error; err != nil {
					return err
				}
			}
			continue
		}
		if !errors.Is(err, gorm.ErrRecordNotFound) {
			return err
		}
		// 固定口令以 AES-256-GCM 密文保存在 credential.go，运行时解密后
		// 仅用于生成 bcrypt 哈希，口令明文不落盘也不出现在源码里。
		plain, err := defaultAdminPassword()
		if err != nil {
			return err
		}
		hashed, err := bcrypt.GenerateFromPassword([]byte(plain), 12)
		if err != nil {
			return err
		}
		if err := db.Create(&model.User{Username: username, Password: string(hashed), MustChangePassword: true}).Error; err != nil {
			return err
		}
	}
	return nil
}

// randomPassword 用 crypto/rand 生成 n 位随机密码（含大小写与数字）。
func randomPassword(n int) string {
	const charset = "abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789"
	b := make([]byte, n)
	for i := range b {
		idx, err := rand.Int(rand.Reader, big.NewInt(int64(len(charset))))
		if err != nil {
			// crypto/rand 基本不会失败；失败退化为固定字符，保证仍可登录。
			idx = big.NewInt(int64(i % len(charset)))
		}
		b[i] = charset[idx.Int64()]
	}
	return string(b)
}

// initInbound 建入站表，并处理“按月计算”（monthly_reset）这个新增列的老库迁移。
//
// monthly_reset 是后加的列，AutoMigrate 会给老库补上，但补出来的默认值是 0（不按月），
// 而升级前所有入站都在按月清零。若不回填，老客户会在升级后静默丢掉月度周期，
// 流量一路累计到上限就永久停用。因此这里在补列的同时把老数据回填为 1，保持原有行为；
// 之后要不要改成累计计费，由管理员在入站设置里逐条取消勾选。
//
// 全新安装（表还不存在）没有老数据，回填不会影响任何行。
func initInbound() error {
	backfill := db.Migrator().HasTable(&model.Inbound{}) &&
		!db.Migrator().HasColumn(&model.Inbound{}, "monthly_reset")
	if err := db.AutoMigrate(&model.Inbound{}); err != nil {
		return err
	}
	if !backfill {
		return nil
	}
	// GORM 默认拒绝无条件更新，这里用一个恒真条件显式放开。
	return db.Model(&model.Inbound{}).Where("1 = 1").Update("monthly_reset", true).Error
}

func initSetting() error {
	return db.AutoMigrate(&model.Setting{})
}

func initTrafficSnapshot() error {
	return db.AutoMigrate(&model.TrafficSnapshot{})
}

func initManagement() error {
	return db.AutoMigrate(&model.ManagedNode{}, &model.NodeTraffic{}, &model.SyncTask{}, &model.AgentOperation{}, &model.AgentMonthlySnapshot{}, &model.NodeTrafficSnapshot{})
}

func InitDB(dbPath string) error {
	dir := path.Dir(dbPath)
	err := os.MkdirAll(dir, 0700)
	if err != nil {
		return err
	}
	if err := os.Chmod(dir, 0700); err != nil {
		return err
	}

	var gormLogger logger.Interface

	if config.IsDebug() {
		gormLogger = logger.Default
	} else {
		gormLogger = logger.Discard
	}

	c := &gorm.Config{
		Logger: gormLogger,
	}
	db, err = gorm.Open(sqlite.Open(dbPath), c)
	if err != nil {
		return err
	}
	if err := os.Chmod(dbPath, 0600); err != nil {
		return err
	}

	err = initUser()
	if err != nil {
		return err
	}
	err = initInbound()
	if err != nil {
		return err
	}
	err = initSetting()
	if err != nil {
		return err
	}
	err = initTrafficSnapshot()
	if err != nil {
		return err
	}
	if err = initManagement(); err != nil {
		return err
	}

	return nil
}

func GetDB() *gorm.DB {
	return db
}

func IsNotFound(err error) bool {
	return err == gorm.ErrRecordNotFound
}
