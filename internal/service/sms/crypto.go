package sms

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"encoding/base64"
	"fmt"
	"io"
	"os"
)

const defaultSMSEncryptKey = "tokenhub-sms-secret-key-32bytes!!"

func smsEncryptKey() []byte {
	key := os.Getenv("SMS_ENCRYPT_KEY")
	if key == "" {
		key = defaultSMSEncryptKey
	}
	keyBytes := []byte(key)
	if len(keyBytes) < 32 {
		padded := make([]byte, 32)
		copy(padded, keyBytes)
		return padded
	}
	if len(keyBytes) > 32 {
		return keyBytes[:32]
	}
	return keyBytes
}

func encryptSecret(plain string) (string, error) {
	block, err := aes.NewCipher(smsEncryptKey())
	if err != nil {
		return "", err
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return "", err
	}
	nonce := make([]byte, gcm.NonceSize())
	if _, err := io.ReadFull(rand.Reader, nonce); err != nil {
		return "", err
	}
	out := append(nonce, gcm.Seal(nil, nonce, []byte(plain), nil)...)
	return base64.StdEncoding.EncodeToString(out), nil
}

func decryptSecret(cipherText string) (string, error) {
	if cipherText == "" {
		return "", nil
	}
	raw, err := base64.StdEncoding.DecodeString(cipherText)
	if err != nil {
		return "", err
	}
	block, err := aes.NewCipher(smsEncryptKey())
	if err != nil {
		return "", err
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return "", err
	}
	if len(raw) < gcm.NonceSize() {
		return "", fmt.Errorf("invalid encrypted secret")
	}
	nonce := raw[:gcm.NonceSize()]
	data := raw[gcm.NonceSize():]
	plain, err := gcm.Open(nil, nonce, data, nil)
	if err != nil {
		return "", err
	}
	return string(plain), nil
}
