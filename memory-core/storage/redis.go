package storage

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/agent-memoryos/memory-core/types"
	"github.com/go-redis/redis/v8"
)

// RedisHotCache implements HotCache using Redis.
type RedisHotCache struct {
	client *redis.Client
}

// NewRedisHotCache creates a new Redis-backed hot cache.
func NewRedisHotCache(addr, password string, db int) (*RedisHotCache, error) {
	client := redis.NewClient(&redis.Options{
		Addr:         addr,
		Password:     password,
		DB:           db,
		MaxRetries:   3,
		PoolSize:     20,
		MinIdleConns: 5,
		DialTimeout:  5 * time.Second,
		ReadTimeout:  3 * time.Second,
		WriteTimeout: 3 * time.Second,
	})

	if err := client.Ping(context.Background()).Err(); err != nil {
		return nil, fmt.Errorf("redis: ping: %w", err)
	}

	return &RedisHotCache{client: client}, nil
}

func (r *RedisHotCache) GetMemory(ctx context.Context, memoryID string) (*types.Memory, error) {
	key := fmt.Sprintf("memory:%s", memoryID)
	data, err := r.client.Get(ctx, key).Bytes()
	if err != nil {
		return nil, err
	}

	var m types.Memory
	if err := json.Unmarshal(data, &m); err != nil {
		return nil, err
	}
	return &m, nil
}

func (r *RedisHotCache) SetMemory(ctx context.Context, memory *types.Memory, ttlSeconds int) error {
	key := fmt.Sprintf("memory:%s", memory.ID)
	data, err := json.Marshal(memory)
	if err != nil {
		return err
	}
	return r.client.Set(ctx, key, data, time.Duration(ttlSeconds)*time.Second).Err()
}

func (r *RedisHotCache) Invalidate(ctx context.Context, memoryID string) error {
	key := fmt.Sprintf("memory:%s", memoryID)
	return r.client.Del(ctx, key).Err()
}

func (r *RedisHotCache) CacheEmbedding(ctx context.Context, key string, embedding []float32) error {
	data, err := json.Marshal(embedding)
	if err != nil {
		return err
	}
	return r.client.Set(ctx, fmt.Sprintf("emb:%s", key), data, 1*time.Hour).Err()
}

func (r *RedisHotCache) GetEmbedding(ctx context.Context, key string) ([]float32, error) {
	data, err := r.client.Get(ctx, fmt.Sprintf("emb:%s", key)).Bytes()
	if err != nil {
		return nil, err
	}
	var emb []float32
	if err := json.Unmarshal(data, &emb); err != nil {
		return nil, err
	}
	return emb, nil
}

func (r *RedisHotCache) AddToRecent(ctx context.Context, userID, memoryID string, score float64) error {
	return r.client.ZAdd(ctx, fmt.Sprintf("recent:%s", userID), &redis.Z{
		Score:  score,
		Member: memoryID,
	}).Err()
}

func (r *RedisHotCache) GetRecent(ctx context.Context, userID string, limit int) ([]string, error) {
	return r.client.ZRevRange(ctx, fmt.Sprintf("recent:%s", userID), 0, int64(limit-1)).Result()
}

func (r *RedisHotCache) SetSession(ctx context.Context, sessionID, key string, value interface{}, ttlSeconds int) error {
	data, err := json.Marshal(value)
	if err != nil {
		return err
	}
	return r.client.HSet(ctx, fmt.Sprintf("session:%s", sessionID), key, data).Err()
}

func (r *RedisHotCache) GetSession(ctx context.Context, sessionID, key string, target interface{}) error {
	data, err := r.client.HGet(ctx, fmt.Sprintf("session:%s", sessionID), key).Bytes()
	if err != nil {
		return err
	}
	return json.Unmarshal(data, target)
}

// Close closes the Redis connection.
func (r *RedisHotCache) Close() error {
	return r.client.Close()
}
