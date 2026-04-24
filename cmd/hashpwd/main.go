// hashpwd - 一次性工具：为指定密码生成 bcrypt(sha256(password+lowerEmail)) 哈希
// 用法: go run ./cmd/hashpwd <password> <email>
package main

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"strings"

	"golang.org/x/crypto/bcrypt"
)

func main() {
	if len(os.Args) < 3 {
		fmt.Fprintln(os.Stderr, "usage: hashpwd <password> <email>")
		os.Exit(1)
	}
	pwd := os.Args[1]
	email := strings.ToLower(strings.TrimSpace(os.Args[2]))
	sum := sha256.Sum256([]byte(pwd + email))
	clientHash := hex.EncodeToString(sum[:])
	bcryptHash, err := bcrypt.GenerateFromPassword([]byte(clientHash), 12)
	if err != nil {
		fmt.Fprintln(os.Stderr, "bcrypt failed:", err)
		os.Exit(2)
	}
	fmt.Println(string(bcryptHash))
}
