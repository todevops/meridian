// 凭据加解密单测：密文无明文、往返一致、防篡改、密钥隔离。
package credentials

import (
	"strings"
	"testing"
)

func TestEncryptDecryptRoundTrip(t *testing.T) {
	c, err := NewCipher("test-master-key")
	if err != nil {
		t.Fatalf("创建加解密器失败: %v", err)
	}
	secret := `{"username":"root","password":"s3cret-plain"}`
	ct, err := c.Encrypt([]byte(secret))
	if err != nil {
		t.Fatalf("加密失败: %v", err)
	}
	if ct == secret {
		t.Fatal("密文不应等于明文")
	}
	if strings.Contains(ct, "s3cret-plain") {
		t.Fatal("密文不应包含明文片段")
	}
	// 同一明文两次加密密文应不同（随机 nonce）。
	ct2, err := c.Encrypt([]byte(secret))
	if err != nil {
		t.Fatalf("二次加密失败: %v", err)
	}
	if ct == ct2 {
		t.Fatal("相同明文两次加密密文应不同（随机 nonce）")
	}
	plain, err := c.Decrypt(ct)
	if err != nil {
		t.Fatalf("解密失败: %v", err)
	}
	if string(plain) != secret {
		t.Fatalf("往返不一致: %q", string(plain))
	}
}

func TestDecryptRejectsTampering(t *testing.T) {
	c, err := NewCipher("test-master-key")
	if err != nil {
		t.Fatalf("创建加解密器失败: %v", err)
	}
	ct, err := c.Encrypt([]byte("hello"))
	if err != nil {
		t.Fatalf("加密失败: %v", err)
	}
	// 篡改密文尾字符。
	tampered := ct[:len(ct)-2] + "AA"
	if _, err := c.Decrypt(tampered); err == nil {
		t.Fatal("篡改密文应解密失败")
	}
	// 非 base64 / 过短密文。
	if _, err := c.Decrypt("!!!not-base64!!!"); err == nil {
		t.Fatal("非法 base64 应解密失败")
	}
	if _, err := c.Decrypt("aGVsbG8="); err == nil {
		t.Fatal("过短密文应解密失败")
	}
}

func TestDifferentKeysCannotDecrypt(t *testing.T) {
	c1, _ := NewCipher("key-1")
	c2, _ := NewCipher("key-2")
	ct, err := c1.Encrypt([]byte("secret"))
	if err != nil {
		t.Fatalf("加密失败: %v", err)
	}
	if _, err := c2.Decrypt(ct); err == nil {
		t.Fatal("不同密钥不应解密成功")
	}
}

func TestLoadCipherDefaultFallback(t *testing.T) {
	t.Setenv(MasterKeyEnv, "")
	c, usingDefault, err := LoadCipher()
	if err != nil {
		t.Fatalf("加载失败: %v", err)
	}
	if !usingDefault {
		t.Fatal("未配置 CMDB_MASTER_KEY 时应标记使用内置开发键")
	}
	if _, err := c.Encrypt([]byte("x")); err != nil {
		t.Fatalf("开发键应可用: %v", err)
	}

	t.Setenv(MasterKeyEnv, "explicit-key")
	_, usingDefault, err = LoadCipher()
	if err != nil || usingDefault {
		t.Fatal("显式配置时不应使用内置开发键")
	}
}
