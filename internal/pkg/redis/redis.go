package redis

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"time"

	"github.com/google/uuid"
	goredis "github.com/redis/go-redis/v9"
)

// Client 全局Redis客户端实例
var Client *goredis.Client

// Config Redis连接配置
// Username 可选：阿里云 Tair 推荐使用 ACL 账号密码（实例名作为默认账号名）
// 留空时走 AUTH password 模式
type Config struct {
	Addr     string
	Username string
	Password string
	DB       int
}

// Init 初始化全局Redis客户端
// 分步诊断日志（输出到 stdout 便于 kubectl logs 排障）：
//  1. 打印目标 addr + username 是否填充
//  2. TCP 预检（5s 超时，避免 Dial 阶段无日志阻塞）
//  3. NewClient（Ping 会触发实际建连 + AUTH）
//  4. Ping 验证 username/password（5s 超时）
func Init(cfg Config) error {
	if cfg.Addr == "" {
		return fmt.Errorf("redis addr is empty")
	}

	authMode := "AUTH password"
	if cfg.Username != "" {
		authMode = "ACL (username+password)"
	}
	fmt.Printf("redis: starting init addr=%s auth=%s db=%d\n", cfg.Addr, authMode, cfg.DB)

	// Step 1: TCP 预检
	fmt.Printf("redis: tcp preflight addr=%s\n", cfg.Addr)
	tcpStart := time.Now()
	conn, tcpErr := net.DialTimeout("tcp", cfg.Addr, 5*time.Second)
	if tcpErr != nil {
		fmt.Printf("redis: tcp preflight FAILED addr=%s err=%v (hint: 检查 Tair 白名单是否放通 Pod IP 段)\n", cfg.Addr, tcpErr)
		return fmt.Errorf("redis tcp preflight failed (%s): %w", cfg.Addr, tcpErr)
	}
	_ = conn.Close()
	fmt.Printf("redis: tcp preflight OK addr=%s cost=%v\n", cfg.Addr, time.Since(tcpStart))

	// Step 2: NewClient
	fmt.Println("redis: goredis.NewClient")
	Client = goredis.NewClient(&goredis.Options{
		Addr:     cfg.Addr,
		Username: cfg.Username,
		Password: cfg.Password,
		DB:       cfg.DB,
	})

	// Step 3: Ping（触发实际建连 + AUTH）
	fmt.Println("redis: ping (auth check)")
	pingStart := time.Now()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := Client.Ping(ctx).Err(); err != nil {
		fmt.Printf("redis: ping FAILED err=%v (hint: TCP 通但 ping 失败多半是账号或密码错，或未设置 ACL 账号)\n", err)
		return fmt.Errorf("redis ping failed: %w", err)
	}
	fmt.Printf("redis: ping OK cost=%v\n", time.Since(pingStart))
	return nil
}

// Close 关闭Redis客户端连接
func Close() error {
	if Client != nil {
		return Client.Close()
	}
	return nil
}

// Get 从Redis获取指定键的值
func Get(ctx context.Context, key string) (string, error) {
	return Client.Get(ctx, key).Result()
}

// Set 将键值对存储到Redis，带TTL过期时间
func Set(ctx context.Context, key string, value interface{}, ttl time.Duration) error {
	return Client.Set(ctx, key, value, ttl).Err()
}

// Del 从Redis删除指定的键
func Del(ctx context.Context, keys ...string) error {
	return Client.Del(ctx, keys...).Err()
}

// SetJSON 将值序列化为JSON后存储到Redis
func SetJSON(ctx context.Context, key string, value interface{}, ttl time.Duration) error {
	data, err := json.Marshal(value)
	if err != nil {
		return fmt.Errorf("failed to marshal value: %w", err)
	}
	return Set(ctx, key, string(data), ttl)
}

// GetJSON 从Redis获取值并反序列化为目标结构体
func GetJSON(ctx context.Context, key string, dest interface{}) error {
	val, err := Get(ctx, key)
	if err != nil {
		return err
	}
	return json.Unmarshal([]byte(val), dest)
}

// DistributedLock 基于Redis的分布式锁
type DistributedLock struct {
	key   string
	token string
}

// Lock 获取分布式锁，返回锁实例用于释放，若锁已被持有则返回错误
func Lock(ctx context.Context, key string, ttl time.Duration) (*DistributedLock, error) {
	if key == "" {
		return nil, fmt.Errorf("lock key is empty")
	}
	if ttl <= 0 {
		ttl = 10 * time.Second
	}
	token := uuid.New().String()
	lockKey := "lock:" + key

	ok, err := Client.SetNX(ctx, lockKey, token, ttl).Result()
	if err != nil {
		return nil, fmt.Errorf("failed to acquire lock: %w", err)
	}
	if !ok {
		return nil, fmt.Errorf("lock %s is already held", key)
	}
	return &DistributedLock{key: lockKey, token: token}, nil
}

// Unlock 释放分布式锁（仅当仍由当前实例持有时才释放）
func (dl *DistributedLock) Unlock(ctx context.Context) error {
	if dl == nil {
		return nil
	}
	// Lua脚本实现原子性检查并删除
	script := `
		if redis.call("get", KEYS[1]) == ARGV[1] then
			return redis.call("del", KEYS[1])
		else
			return 0
		end
	`
	_, err := Client.Eval(ctx, script, []string{dl.key}, dl.token).Result()
	return err
}
