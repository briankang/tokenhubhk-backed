package sms

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"encoding/base64"
	"fmt"
	"io"
	"strconv"
	"strings"
	"time"
)

// buildEncryptedSceneID implements Aliyun Captcha 2.0 encrypted mode.
// Plain text format: sceneId&timestampSeconds&expireSeconds.
func buildEncryptedSceneID(sceneID, ekey string, ttlSeconds int, now time.Time) (string, error) {
	sceneID = strings.TrimSpace(sceneID)
	ekey = strings.TrimSpace(ekey)
	if sceneID == "" || ekey == "" {
		return "", nil
	}
	if ttlSeconds <= 0 {
		ttlSeconds = 3600
	}
	key, err := base64.StdEncoding.DecodeString(ekey)
	if err != nil {
		return "", fmt.Errorf("decode captcha ekey: %w", err)
	}
	if len(key) != 32 {
		return "", fmt.Errorf("captcha ekey must decode to 32 bytes")
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return "", err
	}
	iv := make([]byte, aes.BlockSize)
	if _, err := io.ReadFull(rand.Reader, iv); err != nil {
		return "", err
	}
	plain := []byte(sceneID + "&" + strconv.FormatInt(now.Unix(), 10) + "&" + strconv.Itoa(ttlSeconds))
	plain = pkcs7Pad(plain, aes.BlockSize)
	encrypted := make([]byte, len(plain))
	cipher.NewCBCEncrypter(block, iv).CryptBlocks(encrypted, plain)
	out := append(iv, encrypted...)
	return base64.StdEncoding.EncodeToString(out), nil
}

func pkcs7Pad(src []byte, blockSize int) []byte {
	padding := blockSize - len(src)%blockSize
	out := make([]byte, 0, len(src)+padding)
	out = append(out, src...)
	for i := 0; i < padding; i++ {
		out = append(out, byte(padding))
	}
	return out
}
