// Package credentials 实现凭据纳管（F-005）的核心能力：
// secret JSON 经 AES-256-GCM 加密后落库（密文列），密钥来自环境变量
// CMDB_MASTER_KEY（经 SHA-256 派生为 32 字节密钥）；缺省使用内置开发键，
// 调用方须在启动日志大字告警（见 LoadCipher 返回值）。
package credentials

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"fmt"
	"os"
)

// MasterKeyEnv 是主密钥环境变量名。
const MasterKeyEnv = "CMDB_MASTER_KEY"

// devMasterKey 是内置开发键（仅本地开发兜底，生产必须显式配置 CMDB_MASTER_KEY）。
const devMasterKey = "cmdb-dev-master-key-change-me"

// Cipher 提供 secret 的加解密能力。
type Cipher struct {
	aead cipher.AEAD
}

// NewCipher 以给定密钥材料创建加解密器（材料经 SHA-256 派生为 32 字节密钥）。
func NewCipher(keyMaterial string) (*Cipher, error) {
	if keyMaterial == "" {
		return nil, fmt.Errorf("密钥材料为空")
	}
	sum := sha256.Sum256([]byte(keyMaterial))
	block, err := aes.NewCipher(sum[:])
	if err != nil {
		return nil, fmt.Errorf("创建 AES 分组失败: %w", err)
	}
	aead, err := cipher.NewGCM(block)
	if err != nil {
		return nil, fmt.Errorf("创建 GCM 失败: %w", err)
	}
	return &Cipher{aead: aead}, nil
}

// LoadCipher 从环境变量 CMDB_MASTER_KEY 加载加解密器；
// 未配置时退回内置开发键并返回 usingDefault=true（调用方须大字告警）。
func LoadCipher() (*Cipher, bool, error) {
	key := os.Getenv(MasterKeyEnv)
	if key == "" {
		c, err := NewCipher(devMasterKey)
		return c, true, err
	}
	c, err := NewCipher(key)
	return c, false, err
}

// Encrypt 加密明文，返回 base64(nonce|ciphertext) 密文串。
func (c *Cipher) Encrypt(plaintext []byte) (string, error) {
	nonce := make([]byte, c.aead.NonceSize())
	if _, err := rand.Read(nonce); err != nil {
		return "", fmt.Errorf("生成随机 nonce 失败: %w", err)
	}
	sealed := c.aead.Seal(nonce, nonce, plaintext, nil)
	return base64.StdEncoding.EncodeToString(sealed), nil
}

// Decrypt 解密密文串，返回明文；密文损坏或密钥不符时报错。
func (c *Cipher) Decrypt(ciphertext string) ([]byte, error) {
	raw, err := base64.StdEncoding.DecodeString(ciphertext)
	if err != nil {
		return nil, fmt.Errorf("密文 base64 解码失败: %w", err)
	}
	if len(raw) < c.aead.NonceSize() {
		return nil, fmt.Errorf("密文长度不足（小于 nonce 长度）")
	}
	nonce, body := raw[:c.aead.NonceSize()], raw[c.aead.NonceSize():]
	plain, err := c.aead.Open(nil, nonce, body, nil)
	if err != nil {
		return nil, fmt.Errorf("解密失败（密文损坏或密钥不符）: %w", err)
	}
	return plain, nil
}
