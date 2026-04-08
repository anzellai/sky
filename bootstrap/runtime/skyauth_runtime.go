package main

// Std.Auth — Built-in authentication for Sky.
// Provides password hashing (bcrypt), session management, and user storage.
// Reads [auth] from sky.toml for configuration.

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"

	"golang.org/x/crypto/bcrypt"
)

// skyAuthConfig holds the auth configuration from sky.toml.
type skyAuthConfig struct {
	Method            string
	Secret            string
	PreviousSecrets   []string
	BcryptCost        int
	SessionTTL        time.Duration
	EmailVerification bool
}

var skyAuthCfg *skyAuthConfig
var skyAuthTablesCreated bool

// Compile-time config vars (set by the Sky compiler from sky.toml [auth]).
// These are overridden at package init if present.
var _skyAuthCfgMethod = ""
var _skyAuthCfgSecret = ""
var _skyAuthCfgBcryptCost = 0
var _skyAuthCfgSessionTTL = 0
var _skyAuthCfgEmailVer = false

// init auto-detects auth config from sky.toml [auth] section or environment variables.
func init() {
	// Try compile-time constants first
	secret := _skyAuthCfgSecret
	method := _skyAuthCfgMethod
	cost := _skyAuthCfgBcryptCost
	ttlSecs := _skyAuthCfgSessionTTL
	emailVer := _skyAuthCfgEmailVer

	// Then try sky.toml [auth] section
	if secret == "" {
		if data, err := os.ReadFile("sky.toml"); err == nil {
			content := string(data)
			if s := skyAuthExtractToml(content, "[auth]", "secret"); s != "" {
				secret = s
			}
			if m := skyAuthExtractToml(content, "[auth]", "method"); m != "" {
				method = m
			}
			if c := skyAuthExtractToml(content, "[auth]", "bcrypt_cost"); c != "" {
				if n, err := strconv.Atoi(c); err == nil { cost = n }
			}
			if t := skyAuthExtractToml(content, "[auth]", "session_ttl"); t != "" {
				ttlSecs = skyAuthParseTTL(t)
			}
			if skyAuthExtractToml(content, "[auth]", "email_verification") == "true" {
				emailVer = true
			}
		}
	}

	// Read previous secrets for key rotation
	var prevSecrets []string
	if data, err := os.ReadFile("sky.toml"); err == nil {
		if ps := skyAuthExtractToml(string(data), "[auth]", "previous_secrets"); ps != "" {
			for _, s := range strings.Split(ps, ",") {
				if trimmed := strings.TrimSpace(s); trimmed != "" {
					prevSecrets = append(prevSecrets, trimmed)
				}
			}
		}
	}

	// Finally override with env vars
	if v := os.Getenv("SKY_AUTH_SECRET"); v != "" { secret = v }
	if v := os.Getenv("SKY_AUTH_METHOD"); v != "" { method = v }
	if v := os.Getenv("SKY_AUTH_PREVIOUS_SECRETS"); v != "" {
		prevSecrets = nil
		for _, s := range strings.Split(v, ",") {
			if trimmed := strings.TrimSpace(s); trimmed != "" {
				prevSecrets = append(prevSecrets, trimmed)
			}
		}
	}
	if v := os.Getenv("SKY_AUTH_BCRYPT_COST"); v != "" {
		if n, err := strconv.Atoi(v); err == nil { cost = n }
	}
	if v := os.Getenv("SKY_AUTH_SESSION_TTL"); v != "" {
		if n, err := strconv.Atoi(v); err == nil { ttlSecs = n }
	}
	if os.Getenv("SKY_AUTH_EMAIL_VERIFICATION") == "true" { emailVer = true }

	if secret != "" {
		if method == "" { method = "password" }
		if cost == 0 { cost = 12 }
		if ttlSecs == 0 { ttlSecs = 86400 }
		// Also init database if not already connected
		if skyDbGetDefault() == nil {
			if data, err := os.ReadFile("sky.toml"); err == nil {
				content := string(data)
				driver := skyAuthExtractToml(content, "[database]", "driver")
				path := skyAuthExtractToml(content, "[database]", "path")
				if path == "" { path = skyAuthExtractToml(content, "[database]", "url") }
				if driver != "" && path != "" {
					skyDbAutoConnect(driver, path)
				}
			}
		}
		skyAuthCfg = &skyAuthConfig{
			Method:            method,
			Secret:            secret,
			PreviousSecrets:   prevSecrets,
			BcryptCost:        cost,
			SessionTTL:        time.Duration(ttlSecs) * time.Second,
			EmailVerification: emailVer,
		}
	}
}

func skyAuthExtractToml(content, section, key string) string {
	inSection := false
	for _, line := range strings.Split(content, "\n") {
		trimmed := strings.TrimSpace(line)
		if trimmed == section {
			inSection = true
			continue
		}
		if strings.HasPrefix(trimmed, "[") && inSection {
			break
		}
		if inSection && strings.Contains(trimmed, key) {
			parts := strings.SplitN(trimmed, "=", 2)
			if len(parts) == 2 && strings.TrimSpace(parts[0]) == key {
				return strings.Trim(strings.TrimSpace(parts[1]), "\"")
			}
		}
	}
	return ""
}

func skyAuthParseTTL(ttl string) int {
	if strings.HasSuffix(ttl, "h") {
		if n, err := strconv.Atoi(strings.TrimSuffix(ttl, "h")); err == nil { return n * 3600 }
	}
	if strings.HasSuffix(ttl, "m") {
		if n, err := strconv.Atoi(strings.TrimSuffix(ttl, "m")); err == nil { return n * 60 }
	}
	if n, err := strconv.Atoi(ttl); err == nil { return n }
	return 86400
}

// skyAuthInit initialises the auth system from sky.toml [auth] config.
// Called from generated init code or from the auto-detect init().
func skyAuthInit(method, secret string, bcryptCost int, sessionTTLSecs int, emailVerification bool) {
	if secret == "" {
		if v := os.Getenv("SKY_AUTH_SECRET"); v != "" {
			secret = v
		}
	}
	if method == "" {
		method = "password"
	}
	if bcryptCost == 0 {
		bcryptCost = 12
	}
	ttl := time.Duration(sessionTTLSecs) * time.Second
	if ttl == 0 {
		ttl = 24 * time.Hour
	}
	skyAuthCfg = &skyAuthConfig{
		Method:            method,
		Secret:            secret,
		BcryptCost:        bcryptCost,
		SessionTTL:        ttl,
		EmailVerification: emailVerification,
	}
}

// skyAuthEnsureTables creates the sky_users and sky_sessions tables if they don't exist.
// Called lazily on first auth operation to ensure the database is connected.
func skyAuthEnsureTables() {
	if skyAuthTablesCreated {
		return
	}
	conn := skyDbGetDefault()
	if conn == nil {
		return
	}
	skyAuthTablesCreated = true
	conn.DB.Exec(`CREATE TABLE IF NOT EXISTS sky_users (
		id TEXT PRIMARY KEY,
		email TEXT NOT NULL UNIQUE,
		password_hash TEXT NOT NULL DEFAULT '',
		role TEXT NOT NULL DEFAULT 'user',
		verified INTEGER NOT NULL DEFAULT 0,
		verification_token TEXT DEFAULT '',
		provider TEXT NOT NULL DEFAULT 'password',
		provider_id TEXT DEFAULT '',
		name TEXT DEFAULT '',
		avatar_url TEXT DEFAULT '',
		created_at TEXT DEFAULT (datetime('now')),
		updated_at TEXT DEFAULT (datetime('now'))
	)`)
	conn.DB.Exec(`CREATE TABLE IF NOT EXISTS sky_sessions (
		id TEXT PRIMARY KEY,
		user_id TEXT NOT NULL,
		token TEXT NOT NULL UNIQUE,
		expires_at TEXT NOT NULL,
		created_at TEXT DEFAULT (datetime('now'))
	)`)
	conn.DB.Exec(`CREATE INDEX IF NOT EXISTS idx_sky_users_email ON sky_users (email)`)
	conn.DB.Exec(`CREATE INDEX IF NOT EXISTS idx_sky_sessions_token ON sky_sessions (token)`)
}

// --- Password Hashing ---

func sky_authHashPassword(password any) any {
	pw := sky_asString(password)
	hash, err := bcrypt.GenerateFromPassword([]byte(pw), skyAuthCfg.BcryptCost)
	if err != nil {
		return SkyErr(fmt.Sprintf("Failed to hash password: %v", err))
	}
	return SkyOk(string(hash))
}

func sky_authVerifyPassword(password, hash any) any {
	err := bcrypt.CompareHashAndPassword([]byte(sky_asString(hash)), []byte(sky_asString(password)))
	return err == nil
}

// --- User Registration ---

func sky_authRegister(email, password any) any {
	if skyAuthCfg == nil {
		return SkyErr("Auth not initialised — add [auth] to sky.toml")
	}
	skyAuthEnsureTables()
	conn := skyDbGetDefault()
	if conn == nil {
		return SkyErr("No database connection — add [database] to sky.toml")
	}
	emailStr := strings.TrimSpace(strings.ToLower(sky_asString(email)))
	pwStr := sky_asString(password)
	if emailStr == "" {
		return SkyErr("Email is required")
	}
	if len(pwStr) < 6 {
		return SkyErr("Password must be at least 6 characters")
	}

	// Check if user exists
	var existing string
	err := conn.DB.QueryRow("SELECT id FROM sky_users WHERE email = ?", emailStr).Scan(&existing)
	if err == nil {
		return SkyErr("Email already registered")
	}

	// Hash password
	hash, err := bcrypt.GenerateFromPassword([]byte(pwStr), skyAuthCfg.BcryptCost)
	if err != nil {
		return SkyErr(fmt.Sprintf("Failed to hash password: %v", err))
	}

	// Generate user ID
	userID := skyAuthGenerateID()
	verified := 1
	verifyToken := ""
	if skyAuthCfg.EmailVerification {
		verified = 0
		verifyToken = skyAuthGenerateToken()
	}

	_, err = conn.DB.Exec(
		"INSERT INTO sky_users (id, email, password_hash, role, verified, verification_token, provider) VALUES (?, ?, ?, 'user', ?, ?, 'password')",
		userID, emailStr, string(hash), verified, verifyToken,
	)
	if err != nil {
		return SkyErr(fmt.Sprintf("Failed to create user: %v", err))
	}

	result := map[string]any{
		"id":       userID,
		"email":    emailStr,
		"role":     "user",
		"verified": verified == 1,
	}
	if verifyToken != "" {
		result["verificationToken"] = verifyToken
	}
	return SkyOk(result)
}

// --- Login ---

func sky_authLogin(email, password any) any {
	if skyAuthCfg == nil {
		return SkyErr("Auth not initialised — add [auth] to sky.toml")
	}
	skyAuthEnsureTables()
	conn := skyDbGetDefault()
	if conn == nil {
		return SkyErr("No database connection — add [database] to sky.toml")
	}
	emailStr := strings.TrimSpace(strings.ToLower(sky_asString(email)))
	pwStr := sky_asString(password)

	var userID, pwHash, role, name, avatarUrl string
	var verified int
	err := conn.DB.QueryRow(
		"SELECT id, password_hash, role, verified, name, avatar_url FROM sky_users WHERE email = ?",
		emailStr,
	).Scan(&userID, &pwHash, &role, &verified, &name, &avatarUrl)
	if err != nil {
		return SkyErr("Invalid email or password")
	}

	if err := bcrypt.CompareHashAndPassword([]byte(pwHash), []byte(pwStr)); err != nil {
		return SkyErr("Invalid email or password")
	}

	if skyAuthCfg.EmailVerification && verified == 0 {
		return SkyErr("Email not verified")
	}

	// Create session
	token := skyAuthGenerateToken()
	expiresAt := time.Now().Add(skyAuthCfg.SessionTTL).UTC().Format(time.RFC3339)
	conn.DB.Exec(
		"INSERT INTO sky_sessions (id, user_id, token, expires_at) VALUES (?, ?, ?, ?)",
		skyAuthGenerateID(), userID, token, expiresAt,
	)

	return SkyOk(map[string]any{
		"token": token,
		"user": map[string]any{
			"id":        userID,
			"email":     emailStr,
			"role":      role,
			"name":      name,
			"avatarUrl": avatarUrl,
			"verified":  verified == 1,
		},
	})
}

// --- Session Verification ---

func sky_authVerify(token any) any {
	if skyAuthCfg == nil {
		return SkyErr("Auth not initialised — add [auth] to sky.toml")
	}
	skyAuthEnsureTables()
	conn := skyDbGetDefault()
	if conn == nil {
		return SkyErr("No database connection — add [database] to sky.toml")
	}
	tokenStr := sky_asString(token)

	var userID, expiresAt string
	err := conn.DB.QueryRow(
		"SELECT user_id, expires_at FROM sky_sessions WHERE token = ?",
		tokenStr,
	).Scan(&userID, &expiresAt)
	if err != nil {
		return SkyErr("Invalid session")
	}

	// Check expiry
	expires, err := time.Parse(time.RFC3339, expiresAt)
	if err == nil && time.Now().After(expires) {
		conn.DB.Exec("DELETE FROM sky_sessions WHERE token = ?", tokenStr)
		return SkyErr("Session expired")
	}

	// Get user
	var email, role, name, avatarUrl string
	var verified int
	err = conn.DB.QueryRow(
		"SELECT email, role, verified, name, avatar_url FROM sky_users WHERE id = ?",
		userID,
	).Scan(&email, &role, &verified, &name, &avatarUrl)
	if err != nil {
		return SkyErr("User not found")
	}

	return SkyOk(map[string]any{
		"id":        userID,
		"email":     email,
		"role":      role,
		"name":      name,
		"avatarUrl": avatarUrl,
		"verified":  verified == 1,
	})
}

// --- Logout ---

func sky_authLogout(token any) any {
	conn := skyDbGetDefault()
	if conn == nil {
		return SkyErr("No database connection")
	}
	conn.DB.Exec("DELETE FROM sky_sessions WHERE token = ?", sky_asString(token))
	return SkyOk(struct{}{})
}

// --- Email Verification ---

func sky_authVerifyEmail(verificationToken any) any {
	conn := skyDbGetDefault()
	if conn == nil {
		return SkyErr("No database connection")
	}
	tokenStr := sky_asString(verificationToken)

	result, err := conn.DB.Exec(
		"UPDATE sky_users SET verified = 1, verification_token = '' WHERE verification_token = ? AND verified = 0",
		tokenStr,
	)
	if err != nil {
		return SkyErr(fmt.Sprintf("Verification failed: %v", err))
	}
	rows, _ := result.RowsAffected()
	if rows == 0 {
		return SkyErr("Invalid or expired verification token")
	}
	return SkyOk("Email verified")
}

// --- Role Management ---

func sky_authSetRole(userID, role any) any {
	conn := skyDbGetDefault()
	if conn == nil {
		return SkyErr("No database connection")
	}
	_, err := conn.DB.Exec("UPDATE sky_users SET role = ? WHERE id = ?", sky_asString(role), sky_asString(userID))
	if err != nil {
		return SkyErr(fmt.Sprintf("Failed to set role: %v", err))
	}
	return SkyOk(struct{}{})
}

// --- HMAC Token Signing ---

func sky_authSignToken(payload any) any {
	if skyAuthCfg == nil || skyAuthCfg.Secret == "" {
		return SkyErr("Auth secret not configured")
	}
	mac := hmac.New(sha256.New, []byte(skyAuthCfg.Secret))
	mac.Write([]byte(sky_asString(payload)))
	return SkyOk(hex.EncodeToString(mac.Sum(nil)))
}

// sky_authVerifyToken verifies an HMAC signature against the current secret
// and all previous secrets (for key rotation). Returns Ok payload if valid.
func sky_authVerifyToken(payload, signature any) any {
	if skyAuthCfg == nil || skyAuthCfg.Secret == "" {
		return SkyErr("Auth secret not configured")
	}
	payloadStr := sky_asString(payload)
	sigStr := sky_asString(signature)

	// Check current secret first
	if skyAuthCheckHMAC(payloadStr, sigStr, skyAuthCfg.Secret) {
		return SkyOk(payloadStr)
	}
	// Fall back to previous secrets (key rotation)
	for _, prevSecret := range skyAuthCfg.PreviousSecrets {
		if skyAuthCheckHMAC(payloadStr, sigStr, prevSecret) {
			return SkyOk(payloadStr)
		}
	}
	return SkyErr("Invalid signature")
}

func skyAuthCheckHMAC(payload, signature, secret string) bool {
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write([]byte(payload))
	expected := hex.EncodeToString(mac.Sum(nil))
	return hmac.Equal([]byte(expected), []byte(signature))
}

// --- Helpers ---

func skyAuthGenerateID() string {
	b := make([]byte, 16)
	rand.Read(b)
	return hex.EncodeToString(b)
}

func skyAuthGenerateToken() string {
	b := make([]byte, 32)
	rand.Read(b)
	return hex.EncodeToString(b)
}

// skyAuthCleanupExpired removes expired sessions. Called periodically.
func skyAuthCleanupExpired() {
	conn := skyDbGetDefault()
	if conn == nil {
		return
	}
	conn.DB.Exec("DELETE FROM sky_sessions WHERE expires_at < ?", time.Now().UTC().Format(time.RFC3339))
}

// Placeholder for sql import (needed for QueryRow error check)
var _ = sql.ErrNoRows
