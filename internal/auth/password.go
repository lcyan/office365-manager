// Package auth 提供登录密码的哈希、校验与旧格式迁移，以及登录尝试限速。
package auth

import (
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"strconv"
	"strings"

	"golang.org/x/crypto/argon2"
)

// argon2id 参数。memory 单位 KB：64 MB 是服务端合理默认值，
// 对单管理员工具的登录频率来说延迟可忽略。
const (
	argonTime    = 1
	argonMemory  = 64 * 1024
	argonThreads = 4
	argonSaltLen = 16
	argonKeyLen  = 32
)

// DefaultPassword 是首次初始化配置文件时的登录密码，与旧版保持一致。
const DefaultPassword = "admin"

// MinPasswordLength 是新密码的最小长度。
const MinPasswordLength = 8

// HashPassword 用 argon2id 生成 PHC 格式哈希字符串。
func HashPassword(password string) (string, error) {
	salt := make([]byte, argonSaltLen)
	if _, err := rand.Read(salt); err != nil {
		return "", fmt.Errorf("生成盐值失败：%w", err)
	}
	key := argon2.IDKey([]byte(password), salt, argonTime, argonMemory, argonThreads, argonKeyLen)
	return fmt.Sprintf("$argon2id$v=%d$m=%d,t=%d,p=%d$%s$%s",
		argon2.Version, argonMemory, argonTime, argonThreads,
		base64.RawStdEncoding.EncodeToString(salt),
		base64.RawStdEncoding.EncodeToString(key)), nil
}

// VerifyPassword 校验 password 是否匹配 hash。
// 支持 argon2id（PHC）与旧版无盐 SHA-256（64 位十六进制，Python 版遗留）。
func VerifyPassword(hash, password string) bool {
	if strings.HasPrefix(hash, "$argon2id$") {
		return verifyArgon2id(hash, password)
	}
	if isLegacyHexHash(hash) {
		digest := sha256.Sum256([]byte(password))
		expected := hex.EncodeToString(digest[:])
		return subtle.ConstantTimeCompare([]byte(strings.ToLower(hash)), []byte(expected)) == 1
	}
	return false
}

// NeedsRehash 报告 hash 是否为旧格式、应在下一次成功登录后升级为 argon2id。
func NeedsRehash(hash string) bool {
	return !strings.HasPrefix(hash, "$argon2id$")
}

// ValidateNewPassword 检查新密码策略：至少 8 个字符且不能为纯空白。
func ValidateNewPassword(password string) error {
	if strings.TrimSpace(password) == "" {
		return errors.New("新密码不能为空白")
	}
	if len([]rune(password)) < MinPasswordLength {
		return fmt.Errorf("新密码至少需要 %d 个字符", MinPasswordLength)
	}
	return nil
}

func verifyArgon2id(hash, password string) bool {
	parts := strings.Split(hash, "$")
	// ["", "argon2id", "v=19", "m=..,t=..,p=..", "salt", "key"]
	if len(parts) != 6 || parts[1] != "argon2id" {
		return false
	}
	var version int
	if _, err := fmt.Sscanf(parts[2], "v=%d", &version); err != nil || version != argon2.Version {
		return false
	}
	var memory, time uint32
	var threads uint8
	params := strings.Split(parts[3], ",")
	if len(params) != 3 {
		return false
	}
	for _, p := range params {
		value := ""
		switch {
		case strings.HasPrefix(p, "m="):
			value, _ = strings.CutPrefix(p, "m=")
			memory = parseUint32(value)
		case strings.HasPrefix(p, "t="):
			value, _ = strings.CutPrefix(p, "t=")
			time = parseUint32(value)
		case strings.HasPrefix(p, "p="):
			value, _ = strings.CutPrefix(p, "p=")
			threads = parseUint8(value)
		default:
			return false
		}
		if value == "" {
			return false
		}
	}
	salt, err := base64.RawStdEncoding.DecodeString(parts[4])
	if err != nil {
		return false
	}
	want, err := base64.RawStdEncoding.DecodeString(parts[5])
	if err != nil {
		return false
	}
	got := argon2.IDKey([]byte(password), salt, time, memory, threads, uint32(len(want)))
	return subtle.ConstantTimeCompare(got, want) == 1
}

func isLegacyHexHash(hash string) bool {
	if len(hash) != sha256.Size*2 {
		return false
	}
	_, err := hex.DecodeString(hash)
	return err == nil
}

func parseUint32(s string) uint32 {
	v, err := strconv.ParseUint(s, 10, 32)
	if err != nil {
		return 0
	}
	return uint32(v)
}

func parseUint8(s string) uint8 {
	v, err := strconv.ParseUint(s, 10, 8)
	if err != nil {
		return 0
	}
	return uint8(v)
}
