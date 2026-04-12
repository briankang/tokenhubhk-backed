package payment

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"encoding/base64"
	"fmt"
	"math/big"
	"strings"
	"time"
)

const nonceChars = "abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789"

// generateNonce 生成指定长度的随机字母数字字符串
func generateNonce(length int) string {
	var sb strings.Builder
	sb.Grow(length)
	for i := 0; i < length; i++ {
		n, _ := rand.Int(rand.Reader, big.NewInt(int64(len(nonceChars))))
		sb.WriteByte(nonceChars[n.Int64()])
	}
	return sb.String()
}

// GenerateOrderNo 生成唯一订单号：TH + yyyyMMddHHmmss + 6位随机数字
func GenerateOrderNo() string {
	ts := time.Now().Format("20060102150405")
	suffix := generateNonce(6)
	// Ensure suffix is digits only
	digits := "0123456789"
	var sb strings.Builder
	sb.Grow(6)
	for i := 0; i < 6; i++ {
		n, _ := rand.Int(rand.Reader, big.NewInt(int64(len(digits))))
		sb.WriteByte(digits[n.Int64()])
	}
	_ = suffix
	return "TH" + ts + sb.String()
}

// decryptAES256GCM 解密微信支付 V3 AEAD_AES_256_GCM 加密资源
func decryptAES256GCM(apiKey, nonce, ciphertext, associatedData string) ([]byte, error) {
	keyBytes := []byte(apiKey)
	if len(keyBytes) != 32 {
		return nil, fmt.Errorf("aes key must be 32 bytes, got %d", len(keyBytes))
	}

	ciphertextBytes, err := base64.StdEncoding.DecodeString(ciphertext)
	if err != nil {
		return nil, fmt.Errorf("decode ciphertext: %w", err)
	}

	block, err := aes.NewCipher(keyBytes)
	if err != nil {
		return nil, fmt.Errorf("new cipher: %w", err)
	}

	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return nil, fmt.Errorf("new gcm: %w", err)
	}

	plaintext, err := gcm.Open(nil, []byte(nonce), ciphertextBytes, []byte(associatedData))
	if err != nil {
		return nil, fmt.Errorf("gcm open: %w", err)
	}

	return plaintext, nil
}
