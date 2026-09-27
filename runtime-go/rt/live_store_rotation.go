//go:build !js

// live_store_rotation.go — the session-store half of session-id rotation
// (live_session_rotation.go): re-keying a live session object without
// tearing it down, and the alias records that say what became of a retired
// id. Every backend keeps its aliases in the same place as its sessions, so
// a multi-replica deploy (sqlite / postgres / redis shared by the replicas)
// resolves an alias written by any replica.
//
// An alias lives as long as a session would (the store TTL): long enough
// that a retired id is never re-adopted as a fresh session while a browser
// could still present it.

package rt

import (
	"errors"
	"log"
	"time"

	"github.com/redis/go-redis/v9"
)

// ── DDL + queries ───────────────────────────────────────────────────

const (
	qSqliteCreateAliases = `CREATE TABLE IF NOT EXISTS sky_session_aliases (
		old_sid    TEXT PRIMARY KEY,
		alias      TEXT NOT NULL,
		expires_at INTEGER NOT NULL
	)`
	qPostgresCreateAliases = `CREATE TABLE IF NOT EXISTS sky_session_aliases (
		old_sid    TEXT PRIMARY KEY,
		alias      TEXT NOT NULL,
		expires_at BIGINT NOT NULL
	)`
	qSqlitePutAlias = `INSERT INTO sky_session_aliases (old_sid, alias, expires_at) VALUES (?, ?, ?)
		ON CONFLICT(old_sid) DO UPDATE SET alias = excluded.alias, expires_at = excluded.expires_at`
	qPostgresPutAlias = `INSERT INTO sky_session_aliases (old_sid, alias, expires_at) VALUES ($1, $2, $3)
		ON CONFLICT (old_sid) DO UPDATE SET alias = EXCLUDED.alias, expires_at = EXCLUDED.expires_at`
	qSqliteGetAlias       = `SELECT alias FROM sky_session_aliases WHERE old_sid = ? AND expires_at >= ?`
	qPostgresGetAlias     = `SELECT alias FROM sky_session_aliases WHERE old_sid = $1 AND expires_at >= $2`
	qSqliteReapAliases    = `DELETE FROM sky_session_aliases WHERE expires_at < ?`
	qPostgresReapAliases  = `DELETE FROM sky_session_aliases WHERE expires_at < $1`
	qSqliteDeleteSession  = `DELETE FROM sky_sessions WHERE sid = ?`
	qPostgresDeleteSess   = `DELETE FROM sky_sessions WHERE sid = $1`
	redisAliasKeyPrefix   = "sky:alias:"
	aliasMinimumRetention = time.Hour
)

// aliasTTL is how long an alias record is kept: the store TTL, and never
// less than an hour.
func aliasTTL(ttl time.Duration) time.Duration {
	if ttl < aliasMinimumRetention {
		return aliasMinimumRetention
	}
	return ttl
}

// ── memory ──────────────────────────────────────────────────────────

type memAlias struct {
	a   sessionAlias
	exp time.Time
}

func (s *memoryStore) rekeySession(oldSid, newSid string, sess *liveSession) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if cur, ok := s.sessions[oldSid]; ok && cur == sess {
		delete(s.sessions, oldSid)
	}
	if sess.evicted.Load() {
		return
	}
	sess.touchLastSeen()
	s.sessions[newSid] = sess
}

func (s *memoryStore) putAlias(oldSid string, a sessionAlias) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.aliases == nil {
		s.aliases = map[string]memAlias{}
	}
	s.aliases[oldSid] = memAlias{a: a, exp: time.Now().Add(aliasTTL(s.ttl))}
}

func (s *memoryStore) getAlias(oldSid string) (sessionAlias, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	e, ok := s.aliases[oldSid]
	if !ok || time.Now().After(e.exp) {
		return sessionAlias{}, false
	}
	return e.a, true
}

// reapAliases drops expired alias records. Caller holds s.mu.
func (s *memoryStore) reapAliasesLocked(now time.Time) {
	for k, e := range s.aliases {
		if now.After(e.exp) {
			delete(s.aliases, k)
		}
	}
}

// ── sqlite / postgres (shared shape) ────────────────────────────────

// rekeyDurable moves a session in a DB-backed store: the live pointer moves
// in memCache, the blob is written under the new id and the old row is
// deleted. The old row goes even when the blob cannot be encoded (the
// in-memory fallback), so the retired id is never readable from disk.
func rekeyDurable(memMu interface {
	Lock()
	Unlock()
}, memCache map[string]*liveSession, oldSid, newSid string, sess *liveSession,
	set func(sid string, sess *liveSession), deleteRow func(sid string)) {
	memMu.Lock()
	if cur, ok := memCache[oldSid]; ok && cur == sess {
		delete(memCache, oldSid)
	}
	memMu.Unlock()
	if !sess.evicted.Load() {
		set(newSid, sess)
	}
	deleteRow(oldSid)
}

func (s *sqliteStore) rekeySession(oldSid, newSid string, sess *liveSession) {
	rekeyDurable(&s.memMu, s.memCache, oldSid, newSid, sess, s.Set, func(sid string) {
		reportSessionWriteError("live.session-rekey.sqlite", sid,
			execIgnoringRows(s.db, qSqliteDeleteSession, sid))
	})
}

func (s *sqliteStore) putAlias(oldSid string, a sessionAlias) {
	exp := time.Now().Add(aliasTTL(s.ttl)).Unix()
	reportSessionWriteError("live.session-alias.sqlite", oldSid,
		execIgnoringRows(s.db, qSqlitePutAlias, oldSid, encodeAlias(a), exp))
}

func (s *sqliteStore) getAlias(oldSid string) (sessionAlias, bool) {
	var raw string
	if err := s.db.QueryRow(qSqliteGetAlias, oldSid, time.Now().Unix()).Scan(&raw); err != nil {
		return sessionAlias{}, false
	}
	return decodeAlias(raw)
}

func (s *postgresStore) rekeySession(oldSid, newSid string, sess *liveSession) {
	rekeyDurable(&s.memMu, s.memCache, oldSid, newSid, sess, s.Set, func(sid string) {
		reportSessionWriteError("live.session-rekey.postgres", sid,
			execIgnoringRows(s.db, qPostgresDeleteSess, sid))
	})
}

func (s *postgresStore) putAlias(oldSid string, a sessionAlias) {
	exp := time.Now().Add(aliasTTL(s.ttl)).Unix()
	reportSessionWriteError("live.session-alias.postgres", oldSid,
		execIgnoringRows(s.db, qPostgresPutAlias, oldSid, encodeAlias(a), exp))
}

func (s *postgresStore) getAlias(oldSid string) (sessionAlias, bool) {
	var raw string
	if err := s.db.QueryRow(qPostgresGetAlias, oldSid, time.Now().Unix()).Scan(&raw); err != nil {
		return sessionAlias{}, false
	}
	return decodeAlias(raw)
}

// ── redis ───────────────────────────────────────────────────────────

func redisAliasKey(sid string) string { return redisAliasKeyPrefix + sid }

func (s *redisStore) rekeySession(oldSid, newSid string, sess *liveSession) {
	rekeyDurable(&s.memMu, s.memCache, oldSid, newSid, sess, s.Set, func(sid string) {
		if err := s.client.Del(s.ctx, redisKey(sid)).Err(); err != nil {
			reportSessionWriteError("live.session-rekey.redis", sid, err)
		}
	})
}

func (s *redisStore) putAlias(oldSid string, a sessionAlias) {
	if err := s.client.Set(s.ctx, redisAliasKey(oldSid), encodeAlias(a), aliasTTL(s.ttl)).Err(); err != nil {
		reportSessionWriteError("live.session-alias.redis", oldSid, err)
	}
}

func (s *redisStore) getAlias(oldSid string) (sessionAlias, bool) {
	raw, err := s.client.Get(s.ctx, redisAliasKey(oldSid)).Result()
	if err != nil {
		if !errors.Is(err, redis.Nil) {
			log.Printf("[sky.live] redis: get session alias %s: %v", oldSid, err)
		}
		return sessionAlias{}, false
	}
	return decodeAlias(raw)
}
