package auth

import (
	"errors"
	"time"

	"github.com/golang-jwt/jwt/v5"
)

// TokenService 负责 JWT 会话令牌的签发与校验（HS256，无状态）。
type TokenService struct {
	secret []byte
	ttl    time.Duration
}

// NewTokenService 创建令牌服务。secret 为 HMAC 密钥，ttl 为令牌有效期。
func NewTokenService(secret string, ttl time.Duration) *TokenService {
	return &TokenService{secret: []byte(secret), ttl: ttl}
}

// TTL 返回令牌有效期（用于 cookie MaxAge）。
func (t *TokenService) TTL() time.Duration { return t.ttl }

// Issue 为用户签发 JWT：sub 为用户 ID。
func (t *TokenService) Issue(userID string) (string, error) {
	now := time.Now()
	claims := jwt.RegisteredClaims{
		Subject:   userID,
		IssuedAt:  jwt.NewNumericDate(now),
		ExpiresAt: jwt.NewNumericDate(now.Add(t.ttl)),
	}
	return jwt.NewWithClaims(jwt.SigningMethodHS256, claims).SignedString(t.secret)
}

// Parse 校验 JWT 并返回用户 ID；过期、签名不符、算法不符均报错。
func (t *TokenService) Parse(tokenStr string) (string, error) {
	token, err := jwt.Parse(tokenStr, func(_ *jwt.Token) (any, error) {
		return t.secret, nil
	}, jwt.WithValidMethods([]string{jwt.SigningMethodHS256.Alg()}))
	if err != nil {
		return "", err
	}
	sub, err := token.Claims.GetSubject()
	if err != nil || sub == "" {
		return "", errors.New("令牌缺少 sub")
	}
	return sub, nil
}
