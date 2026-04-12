package skylive_rt

import (
	"context"
	"encoding/json"
	"log"
	"time"

	"github.com/redis/go-redis/v9"
)

// RedisStore is a session store backed by Redis.
// Sessions are stored as JSON with TTL-based expiration handled by Redis.
type RedisStore struct {
	client *redis.Client
	ttl    time.Duration
	ctx    context.Context
	cancel context.CancelFunc
}

// NewRedisStore connects to Redis at the given address and returns a store.
// addr can be "localhost:6379" or a full Redis URL "redis://:password@host:port/db".
func NewRedisStore(addr string, ttl time.Duration) (*RedisStore, error) {
	opt, err := redis.ParseURL(addr)
	if err != nil {
		// Not a URL — treat as host:port
		opt = &redis.Options{Addr: addr}
	}

	client := redis.NewClient(opt)
	ctx := context.Background()

	// Verify connection
	if err := client.Ping(ctx).Err(); err != nil {
		return nil, err
	}

	// Create a cancellable context for the store
	ctx, cancel := context.WithCancel(ctx)

	return &RedisStore{client: client, ttl: ttl, ctx: ctx, cancel: cancel}, nil
}

type redisSession struct {
	ModelJSON    string `json:"model"`
	PrevViewHTML string `json:"prev_view"`
	CreatedAt    int64  `json:"created_at"`
	LastSeen     int64  `json:"last_seen"`
	Version      int64  `json:"version"`
}

func (s *RedisStore) Get(sid string) (*Session, bool) {
	val, err := s.client.Get(s.ctx, "sky:sess:"+sid).Result()
	if err != nil {
		return nil, false
	}

	var rs redisSession
	if err := json.Unmarshal([]byte(val), &rs); err != nil {
		return nil, false
	}

	// Deserialize model
	var model map[string]any
	if err := json.Unmarshal([]byte(rs.ModelJSON), &model); err != nil {
		return nil, false
	}
	fixJSONNumbers(model)

	// Deserialize previous view tree
	var prevView *VNode
	if rs.PrevViewHTML != "" {
		prevView = ParseHTML(rs.PrevViewHTML)
	}

	sess := &Session{
		Model:    model,
		PrevView: prevView,
		Created:  time.Unix(rs.CreatedAt, 0),
		LastSeen: time.Now(),
		Version:  rs.Version,
	}

	// Refresh TTL
	s.client.Expire(s.ctx, "sky:sess:"+sid, s.ttl)

	return sess, true
}

func (s *RedisStore) Set(sid string, sess *Session) bool {
	now := time.Now()
	sess.LastSeen = now

	modelBytes, err := json.Marshal(sess.Model)
	if err != nil {
		log.Printf("skylive_rt: RedisStore.Set: failed to marshal model: %v", err)
		return false
	}

	var prevViewHTML string
	if sess.PrevView != nil {
		prevViewHTML = RenderToString(sess.PrevView)
	}

	createdAt := sess.Created.Unix()
	if createdAt == 0 {
		createdAt = now.Unix()
	}

	newVersion := sess.Version + 1
	rs := redisSession{
		ModelJSON:    string(modelBytes),
		PrevViewHTML: prevViewHTML,
		CreatedAt:    createdAt,
		LastSeen:     now.Unix(),
		Version:      newVersion,
	}

	data, err := json.Marshal(rs)
	if err != nil {
		log.Printf("skylive_rt: RedisStore.Set: failed to marshal session: %v", err)
		return false
	}

	key := "sky:sess:" + sid

	// Use Redis WATCH for optimistic concurrency: if another writer changed
	// the key between our GET and this SET, the transaction aborts.
	err = s.client.Watch(s.ctx, func(tx *redis.Tx) error {
		// Check current version
		if sess.Version > 0 {
			val, err := tx.Get(s.ctx, key).Result()
			if err == nil {
				var current redisSession
				if json.Unmarshal([]byte(val), &current) == nil {
					if current.Version != sess.Version {
						return redis.TxFailedErr // version mismatch
					}
				}
			}
		}
		_, err := tx.TxPipelined(s.ctx, func(pipe redis.Pipeliner) error {
			pipe.Set(s.ctx, key, string(data), s.ttl)
			return nil
		})
		return err
	}, key)

	if err != nil {
		if err == redis.TxFailedErr {
			return false // version conflict
		}
		log.Printf("skylive_rt: RedisStore.Set: failed: %v", err)
		return false
	}
	sess.Version = newVersion
	return true
}

func (s *RedisStore) Delete(sid string) {
	s.client.Del(s.ctx, "sky:sess:"+sid)
}

func (s *RedisStore) NewID() string {
	return generateSessionID()
}

// Close closes the Redis client connection.
func (s *RedisStore) Close() error {
	s.cancel()
	return s.client.Close()
}
