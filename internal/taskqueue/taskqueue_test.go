package taskqueue

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"testing"
	"time"
)

const testSignKey = "test-secret-key-32bytes-long!!"

func TestPublisher_Sign(t *testing.T) {
	p := &Publisher{signingKey: testSignKey}

	taskID := "test-task-123"
	taskType := TaskBatchCheck
	timestamp := time.Now().Unix()
	payload := []byte(`{"supplier_id":0}`)

	sig := p.sign(taskID, taskType, timestamp, payload)

	// 签名应该非空
	if sig == "" {
		t.Fatal("signature should not be empty")
	}

	// 相同输入应产生相同签名
	sig2 := p.sign(taskID, taskType, timestamp, payload)
	if sig != sig2 {
		t.Error("same input should produce same signature")
	}

	// 不同 payload 应产生不同签名
	sig3 := p.sign(taskID, taskType, timestamp, []byte(`{"supplier_id":1}`))
	if sig == sig3 {
		t.Error("different payload should produce different signature")
	}
}

func TestConsumer_VerifySignature(t *testing.T) {
	c := &Consumer{signingKey: testSignKey}

	taskID := "test-task-456"
	taskType := TaskModelSync
	timestamp := time.Now().Unix()
	payload := `{"channel_id":10}`

	// 生成正确签名
	mac := hmac.New(sha256.New, []byte(testSignKey))
	mac.Write([]byte(fmt.Sprintf("%s:%s:%d:", taskID, taskType, timestamp)))
	mac.Write([]byte(payload))
	validSig := hex.EncodeToString(mac.Sum(nil))

	task := TaskRequest{
		TaskID:    taskID,
		TaskType:  taskType,
		Payload:   payload,
		Timestamp: timestamp,
		Signature: validSig,
	}

	// 正确签名应通过验证
	if !c.verifySignature(task) {
		t.Error("valid signature should pass verification")
	}

	// 篡改 payload 后签名应失败
	task.Payload = `{"channel_id":99}`
	if c.verifySignature(task) {
		t.Error("tampered payload should fail verification")
	}

	// 伪造签名应失败
	task.Payload = payload
	task.Signature = "deadbeef"
	if c.verifySignature(task) {
		t.Error("forged signature should fail verification")
	}

	// 不同 key 的签名应失败
	wrongKeyConsumer := &Consumer{signingKey: "wrong-key"}
	task.Signature = validSig
	if wrongKeyConsumer.verifySignature(task) {
		t.Error("wrong signing key should fail verification")
	}
}

func TestTaskConstants(t *testing.T) {
	// 验证任务类型常量不为空
	types := []string{
		TaskBatchCheck, TaskModelSync, TaskModelSyncAll,
		TaskPriceScrape, TaskRouteRefresh, TaskScanOffline,
	}
	for _, tt := range types {
		if tt == "" {
			t.Error("task type constant should not be empty")
		}
	}

	// 验证状态常量
	statuses := []string{StatusPending, StatusRunning, StatusCompleted, StatusFailed}
	for _, s := range statuses {
		if s == "" {
			t.Error("status constant should not be empty")
		}
	}
}

func TestStreamConfig(t *testing.T) {
	if StreamKey == "" {
		t.Error("StreamKey should not be empty")
	}
	if ConsumerGroup == "" {
		t.Error("ConsumerGroup should not be empty")
	}
	if DeduplicationTTL <= 0 {
		t.Error("DeduplicationTTL should be positive")
	}
	if SignatureWindow <= 0 {
		t.Error("SignatureWindow should be positive")
	}
}

func TestGetString(t *testing.T) {
	m := map[string]interface{}{
		"key1": "value1",
		"key2": 123,
		"key3": nil,
	}

	if getString(m, "key1") != "value1" {
		t.Error("should return string value")
	}
	if getString(m, "key2") != "" {
		t.Error("non-string should return empty")
	}
	if getString(m, "missing") != "" {
		t.Error("missing key should return empty")
	}
}
