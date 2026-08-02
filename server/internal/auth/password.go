package auth

import "golang.org/x/crypto/bcrypt"

// HashPassword 生成 bcrypt 密码哈希（默认成本）。
func HashPassword(password string) (string, error) {
	hash, err := bcrypt.GenerateFromPassword([]byte(password), bcrypt.DefaultCost)
	if err != nil {
		return "", err
	}
	return string(hash), nil
}

// CheckPassword 校验明文密码与 bcrypt 哈希是否匹配。
func CheckPassword(hash, password string) bool {
	return bcrypt.CompareHashAndPassword([]byte(hash), []byte(password)) == nil
}
