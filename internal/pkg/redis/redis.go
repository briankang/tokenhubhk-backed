package redis

import (
	"context"
	"encoding/json"
	"fmt"
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
func Init(cfg Config) error {
	if cfg.Addr == "" {
		return fmt.Errorf("redis addr is empty")
	}
	Client = goredis.NewClient(&goredis.Options{
		Addr:     cfg.Addr,
		Username: cfg.Username,
		Password: cfg.Password,
		DB:       cfg.DB,
	})
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := Client.Ping(ctx).Err(); err != nil {
		return fmt.Errorf("redis ping failed: %w", err)
	}
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
