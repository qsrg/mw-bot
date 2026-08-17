// File security_test.go: 密码哈希与 JWT 签发/解析单元测试，对齐 Python test_auth.py 中的 security 部分。
package common

import (
	"strings"
	"testing"
	"time"
)

// testSettings 构造测试用 Settings，JWT 密钥固定便于断言。
func testSettings() Settings {
	return Settings{
		JWTSecret:          "test-secret-for-unit-test-only",
		JWTAlgorithm:       "HS256",
		AccessTokenMinutes: 120,
	}
}

// TestHashPasswordAndVerify 验证 bcrypt 哈希与校验闭环。
// 同一明文哈希两次结果不同（盐随机），但校验均通过；错误密码校验失败。
func TestHashPasswordAndVerify(t *testing.T) {
	password := "correct horse battery staple"
	hash1, err := HashPassword(password)
	if err != nil {
		t.Fatalf("HashPassword 失败: %v", err)
	}
	hash2, err := HashPassword(password)
	if err != nil {
		t.Fatalf("HashPassword 第二次失败: %v", err)
	}
	// 盐随机，两次哈希结果应不同
	if hash1 == hash2 {
		t.Error("两次哈希结果相同，盐未随机化")
	}
	// 两次哈希都能校验通过
	if err := VerifyPassword(password, hash1); err != nil {
		t.Errorf("校验 hash1 失败: %v", err)
	}
	if err := VerifyPassword(password, hash2); err != nil {
		t.Errorf("校验 hash2 失败: %v", err)
	}
	// 错误密码校验失败
	if err := VerifyPassword("wrong-password", hash1); err == nil {
		t.Error("错误密码校验应失败，但通过了")
	}
}

// TestHashPasswordTruncatesAt72Bytes 验证密码超过 72 字节时截断处理，不报错。
// bcrypt 限制密码最长 72 字节，超出部分需截断而非报错。
func TestHashPasswordTruncatesAt72Bytes(t *testing.T) {
	// 100 字节密码，前 72 字节相同
	longPwd := strings.Repeat("a", 100)
	shortPwd := strings.Repeat("a", 72)
	hashLong, err := HashPassword(longPwd)
	if err != nil {
		t.Fatalf("长密码哈希失败: %v", err)
	}
	hashShort, err := HashPassword(shortPwd)
	if err != nil {
		t.Fatalf("短密码哈希失败: %v", err)
	}
	// 截断后两者应能互相校验
	if err := VerifyPassword(shortPwd, hashLong); err != nil {
		t.Errorf("长密码哈希校验短密码失败: %v", err)
	}
	if err := VerifyPassword(longPwd, hashShort); err != nil {
		t.Errorf("短密码哈希校验长密码失败: %v", err)
	}
}

// TestIssueAndParseTokenRoundtrip 验证 JWT 签发与解析闭环。
// claims 中 sub/username/role 应能正确回读。
func TestIssueAndParseTokenRoundtrip(t *testing.T) {
	settings := testSettings()
	token, err := IssueToken(1, "admin", "admin", settings)
	if err != nil {
		t.Fatalf("IssueToken 失败: %v", err)
	}
	if token == "" {
		t.Fatal("token 为空")
	}
	claims, err := ParseToken(token, settings)
	if err != nil {
		t.Fatalf("ParseToken 失败: %v", err)
	}
	if claims.Subject != "1" {
		t.Errorf("sub 期望 1, 实际 %s", claims.Subject)
	}
	if claims.Username != "admin" {
		t.Errorf("username 期望 admin, 实际 %s", claims.Username)
	}
	if claims.Role != "admin" {
		t.Errorf("role 期望 admin, 实际 %s", claims.Role)
	}
	if claims.ExpiresAt == nil {
		t.Fatal("exp 为空")
	}
	if claims.ExpiresAt.Time.Before(time.Now()) {
		t.Error("token 已过期")
	}
}

// TestParseTokenRejectsWrongSecret 验证用错误密钥解析 token 失败。
func TestParseTokenRejectsWrongSecret(t *testing.T) {
	settings := testSettings()
	token, err := IssueToken(1, "admin", "admin", settings)
	if err != nil {
		t.Fatalf("IssueToken 失败: %v", err)
	}
	// 用不同密钥解析应失败
	wrongSettings := settings
	wrongSettings.JWTSecret = "different-secret"
	if _, err := ParseToken(token, wrongSettings); err == nil {
		t.Error("用错误密钥解析应失败，但通过了")
	}
}

// TestParseTokenRejectsExpired 验证过期 token 解析失败。
func TestParseTokenRejectsExpired(t *testing.T) {
	settings := testSettings()
	settings.AccessTokenMinutes = -1 // 立即过期
	token, err := IssueToken(1, "admin", "admin", settings)
	if err != nil {
		t.Fatalf("IssueToken 失败: %v", err)
	}
	if _, err := ParseToken(token, settings); err == nil {
		t.Error("过期 token 解析应失败，但通过了")
	}
}

// TestRolePermissionsDistinct 验证 admin 与 user 角色权限区分。
// admin 拥有 7 项权限含 document.upload，user 拥有 3 项权限不含 document.upload。
func TestRolePermissionsDistinct(t *testing.T) {
	adminPerms := PermissionsForRole("admin")
	userPerms := PermissionsForRole("user")
	if len(adminPerms) != 7 {
		t.Errorf("admin 权限数期望 7, 实际 %d", len(adminPerms))
	}
	if len(userPerms) != 3 {
		t.Errorf("user 权限数期望 3, 实际 %d", len(userPerms))
	}
	// admin 应含 document.upload，user 不应含
	if !contains(adminPerms, "document.upload") {
		t.Error("admin 应包含 document.upload 权限")
	}
	if contains(userPerms, "document.upload") {
		t.Error("user 不应包含 document.upload 权限")
	}
	// 两个角色都应能聊天
	if !contains(adminPerms, "chat.ask") {
		t.Error("admin 应包含 chat.ask 权限")
	}
	if !contains(userPerms, "chat.ask") {
		t.Error("user 应包含 chat.ask 权限")
	}
}

// TestPermissionsForUnknownRole 验证未知角色返回 nil。
func TestPermissionsForUnknownRole(t *testing.T) {
	if perms := PermissionsForRole("unknown"); perms != nil {
		t.Errorf("未知角色应返回 nil, 实际 %v", perms)
	}
}

// contains 检查字符串切片是否包含指定值。
func contains(slice []string, val string) bool {
	for _, s := range slice {
		if s == val {
			return true
		}
	}
	return false
}
