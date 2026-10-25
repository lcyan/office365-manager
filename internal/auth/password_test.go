package auth

import (
	"crypto/sha256"
	"encoding/hex"
	"strings"
	"testing"
	"time"
)

func TestHashVerifyRoundTrip(t *testing.T) {
	hash, err := HashPassword("correct horse battery staple")
	if err != nil {
		t.Fatalf("HashPassword: %v", err)
	}
	if !strings.HasPrefix(hash, "$argon2id$") {
		t.Fatalf("hash = %q, want argon2id PHC format", hash)
	}
	if !VerifyPassword(hash, "correct horse battery staple") {
		t.Error("correct password rejected")
	}
	if VerifyPassword(hash, "wrong password") {
		t.Error("wrong password accepted")
	}
	// 相同密码两次哈希应产生不同盐值。
	hash2, _ := HashPassword("correct horse battery staple")
	if hash == hash2 {
		t.Error("salt reused between hashes")
	}
}

func TestVerifyLegacySHA256(t *testing.T) {
	digest := sha256.Sum256([]byte("admin"))
	legacy := hex.EncodeToString(digest[:])
	if !VerifyPassword(legacy, "admin") {
		t.Error("legacy hash rejected")
	}
	if VerifyPassword(legacy, "Admin") {
		t.Error("legacy hash wrong password accepted")
	}
	if !NeedsRehash(legacy) {
		t.Error("legacy hash should need rehash")
	}
	upgraded, _ := HashPassword("admin")
	if NeedsRehash(upgraded) {
		t.Error("argon2id hash should not need rehash")
	}
}

func TestVerifyMalformedHashes(t *testing.T) {
	for _, hash := range []string{
		"", "plaintext", "$argon2id$broken",
		"$argon2id$v=19$m=65536$t=1$p=4$c2FsdA$aGFzaA",                      // 参数段格式错误
		"$argon2id$v=1$m=65536,t=1,p=4$c2FsdA$aGFzaA",                       // 不支持的版本
		"$argon2id$v=19$m=65536,t=1,p=4$!!!$aGFzaA",                         // salt 非法 base64
		"$argon2id$v=19$m=65536,t=1,p=4$c2FsdA$!!!",                         // key 非法 base64
		"0123456789gabcdef0123456789abcdef0123456789abcdef0123456789abcdef", // 非十六进制
	} {
		if VerifyPassword(hash, "anything") {
			t.Errorf("malformed hash %q accepted a password", hash)
		}
	}
}

func TestValidateNewPasswordPolicy(t *testing.T) {
	cases := []struct {
		password string
		wantErr  bool
	}{
		{"", true},
		{"   ", true},
		{"1234567", true},
		{"12345678", false},
		{"这是一个足够长的密码", false},
	}
	for _, tc := range cases {
		err := ValidateNewPassword(tc.password)
		if (err != nil) != tc.wantErr {
			t.Errorf("ValidateNewPassword(%q) error = %v, wantErr %v", tc.password, err, tc.wantErr)
		}
	}
}

func TestLimiterBurstAndRefill(t *testing.T) {
	l := NewLimiter(3, 10*time.Millisecond)
	for i := 0; i < 3; i++ {
		if !l.Allow("1.2.3.4") {
			t.Fatalf("attempt %d within burst rejected", i+1)
		}
	}
	if l.Allow("1.2.3.4") {
		t.Error("4th immediate attempt allowed")
	}
	if !l.Allow("other-ip") {
		t.Error("other key affected by 1.2.3.4 bucket")
	}
	time.Sleep(25 * time.Millisecond) // 恢复约 2 个令牌
	if !l.Allow("1.2.3.4") {
		t.Error("attempt after refill rejected")
	}
	if !l.Allow("1.2.3.4") {
		t.Error("second attempt after refill rejected")
	}
	if l.Allow("1.2.3.4") {
		t.Error("more than refilled tokens allowed")
	}
}
