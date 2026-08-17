// File security.go: 密码哈希（bcrypt）与 JWT 签发/解析（HS256），对齐 Python security.py。
package common

import (
	"fmt"
	"strconv"
	"time"

	"github.com/golang-jwt/jwt/v5"
	"golang.org/x/crypto/bcrypt"
)

// bcrypt 限制密码最长 72 字节，统一在此处截断，与 Python 互验兼容。
const bcryptMaxBytes = 72

// bcryptCost bcrypt 计算成本，与 Python bcrypt.gensalt() 默认 cost=12 对齐（L1）。
const bcryptCost = 12

// HashPassword 对明文密码进行 bcrypt 哈希。
// 密码编码为 UTF-8 后截断至 72 字节再哈希，确保与 Python bcrypt 互验兼容。
func HashPassword(password string) (string, error) {
	pwBytes := []byte(password)
	if len(pwBytes) > bcryptMaxBytes {
		pwBytes = pwBytes[:bcryptMaxBytes]
	}
	hash, err := bcrypt.GenerateFromPassword(pwBytes, bcryptCost)
	if err != nil {
		return "", fmt.Errorf("hash password: %w", err)
	}
	return string(hash), nil
}

// VerifyPassword 校验明文密码与 bcrypt 哈希是否匹配。
// 密码同样截断至 72 字节后比对，匹配返回 nil，不匹配返回非 nil 错误。
func VerifyPassword(password, hash string) error {
	pwBytes := []byte(password)
	if len(pwBytes) > bcryptMaxBytes {
		pwBytes = pwBytes[:bcryptMaxBytes]
	}
	return bcrypt.CompareHashAndPassword([]byte(hash), pwBytes)
}

// Claims JWT claims 结构体，包含 sub/username/role 与标准注册 claims。
// Subject 字段（json:"sub"）覆盖 RegisteredClaims.Subject，序列化时以本字段为准。
type Claims struct {
	Subject  string `json:"sub"`
	Username string `json:"username"`
	Role     string `json:"role"`
	jwt.RegisteredClaims
}

// IssueToken 签发 JWT access token，HS256 签名，过期时间为 AccessTokenMinutes 分钟。
// claims 包含 sub（user_id 字符串）、username、role、exp、iat。
func IssueToken(userID int64, username, role string, settings Settings) (string, error) {
	now := time.Now()
	exp := now.Add(time.Duration(settings.AccessTokenMinutes) * time.Minute)
	sub := strconv.FormatInt(userID, 10)
	claims := Claims{
		Subject:  sub,
		Username: username,
		Role:     role,
		RegisteredClaims: jwt.RegisteredClaims{
			ExpiresAt: jwt.NewNumericDate(exp),
			IssuedAt:  jwt.NewNumericDate(now),
		},
	}
	token := jwt.NewWithClaims(jwt.SigningMethodHS256, claims)
	signed, err := token.SignedString([]byte(settings.JWTSecret))
	if err != nil {
		return "", fmt.Errorf("sign token: %w", err)
	}
	return signed, nil
}

// ParseToken 解析并校验 JWT，返回 claims。
// 签名方法必须为 HMAC（HS256/HS384/HS512），且算法名须匹配 settings.JWTAlgorithm。
func ParseToken(tokenStr string, settings Settings) (*Claims, error) {
	claims := &Claims{}
	_, err := jwt.ParseWithClaims(tokenStr, claims, func(t *jwt.Token) (interface{}, error) {
		if _, ok := t.Method.(*jwt.SigningMethodHMAC); !ok {
			return nil, fmt.Errorf("unexpected signing method: %v", t.Header["alg"])
		}
		return []byte(settings.JWTSecret), nil
	}, jwt.WithValidMethods([]string{settings.JWTAlgorithm}))
	if err != nil {
		return nil, fmt.Errorf("parse token: %w", err)
	}
	return claims, nil
}

// RolePermissions 角色到权限集合的映射，与 Python security.py 对齐。
// admin 拥有 7 项权限，user 拥有 3 项权限。
var RolePermissions = map[string][]string{
	"admin": {
		"document.upload",
		"document.delete",
		"mcp.server.register",
		"mcp.tool.manage",
		"mcp.tool.call",
		"chat.ask",
		"memory.manage",
	},
	"user": {
		"chat.ask",
		"mcp.tool.call",
		"memory.manage",
	},
}

// PermissionsForRole 返回角色对应的权限列表，未知角色返回 nil。
func PermissionsForRole(role string) []string {
	return RolePermissions[role]
}
