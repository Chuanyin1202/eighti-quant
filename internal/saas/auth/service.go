// Package auth handles JWT issuance and validation for SaaS API authentication.
//
// Tokens carry minimal claims: user ID + role. Subscription validation is done
// at request time against the User row, not embedded in the token (so revoking
// or downgrading takes effect immediately on the next request).
package auth

import (
	"errors"
	"fmt"
	"time"

	"github.com/Chuanyin1202/eighti-quant/internal/saas/config"
	"github.com/golang-jwt/jwt/v5"
)

// ErrInvalidToken indicates a token failed signature, expiry, or shape checks.
var ErrInvalidToken = errors.New("invalid token")

// Claims is the JWT payload.
type Claims struct {
	UserID uint   `json:"uid"`
	Role   string `json:"role"` // "free" / "pro" / etc — informational, not authoritative
	jwt.RegisteredClaims
}

// Service signs and parses JWTs using a shared HMAC secret.
type Service struct {
	secret []byte
	ttl    time.Duration
	issuer string
}

// New constructs a Service from JWTConfig. Secret must be non-empty
// (validated in config.Load).
func New(cfg config.JWTConfig) *Service {
	return &Service{
		secret: []byte(cfg.Secret),
		ttl:    time.Duration(cfg.TTLHours) * time.Hour,
		issuer: cfg.Issuer,
	}
}

// SignToken issues a token for the given user.
func (s *Service) SignToken(userID uint, role string) (string, error) {
	now := time.Now()
	claims := Claims{
		UserID: userID,
		Role:   role,
		RegisteredClaims: jwt.RegisteredClaims{
			Issuer:    s.issuer,
			IssuedAt:  jwt.NewNumericDate(now),
			ExpiresAt: jwt.NewNumericDate(now.Add(s.ttl)),
		},
	}
	tok := jwt.NewWithClaims(jwt.SigningMethodHS256, claims)
	signed, err := tok.SignedString(s.secret)
	if err != nil {
		return "", fmt.Errorf("auth: sign: %w", err)
	}
	return signed, nil
}

// ParseToken validates the token and returns its claims, or ErrInvalidToken
// on any failure (signature mismatch, expired, malformed, wrong issuer).
func (s *Service) ParseToken(tokenStr string) (*Claims, error) {
	parsed, err := jwt.ParseWithClaims(tokenStr, &Claims{}, func(t *jwt.Token) (any, error) {
		if _, ok := t.Method.(*jwt.SigningMethodHMAC); !ok {
			return nil, fmt.Errorf("unexpected signing method: %v", t.Header["alg"])
		}
		return s.secret, nil
	}, jwt.WithIssuer(s.issuer))
	if err != nil {
		return nil, ErrInvalidToken
	}
	claims, ok := parsed.Claims.(*Claims)
	if !ok || !parsed.Valid {
		return nil, ErrInvalidToken
	}
	return claims, nil
}
