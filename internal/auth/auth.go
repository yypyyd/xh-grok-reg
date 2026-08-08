// Package auth 管理员登录 + JWT 鉴权。
//
// 设计要点：
//   - token 唯一：每个管理员同一时刻只有一个有效 token，签发新的立即作废旧的
//   - token 落库：数据库存当前有效 token；内存缓存优先，进程重启后缓存为空再读库
//   - 自动续期：token 有效期 24h，签发超过 2h（剩余 <22h）后请求会自动换发新 token，
//     通过响应头 X-New-Token 下发，旧 token 同时作废
package auth

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"strings"
	"sync"
	"time"

	"github.com/golang-jwt/jwt/v5"
	"golang.org/x/crypto/bcrypt"
	"gorm.io/gorm"

	"xh-grok-reg/internal/models"
)

const (
	TokenTTL       = 24 * time.Hour
	RenewAfter     = 2 * time.Hour
	DefaultUser    = "admin"
	DefaultPass    = "admin123"
	MinPasswordLen = 7 // 密码长度要求大于 6 位
)

var (
	ErrBadCredentials = errors.New("用户名或密码错误")
	ErrInvalidToken   = errors.New("token 无效或已过期")
	ErrWeakPassword   = errors.New("密码长度必须大于 6 位")
)

type Claims struct {
	AdminID uint `json:"aid"`
	jwt.RegisteredClaims
}

type cacheEntry struct {
	token    string
	issuedAt time.Time
}

// Service 鉴权服务。tokens 是内存缓存（adminID → 当前有效 token），
// 重启后缓存为空，Validate 会回读数据库再填缓存。
type Service struct {
	db     *gorm.DB
	secret []byte

	mu     sync.Mutex
	tokens map[uint]cacheEntry
}

func New(db *gorm.DB) (*Service, error) {
	s := &Service{db: db, tokens: map[uint]cacheEntry{}}
	if err := s.loadSecret(); err != nil {
		return nil, err
	}
	if err := s.ensureAdmin(); err != nil {
		return nil, err
	}
	return s, nil
}

// loadSecret JWT 签名密钥持久化在 settings 表（首启随机生成），重启后 token 仍可验签。
func (s *Service) loadSecret() error {
	var st models.Setting
	err := s.db.Where("key = ?", "jwt_secret").First(&st).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		buf := make([]byte, 32)
		if _, err := rand.Read(buf); err != nil {
			return err
		}
		st = models.Setting{Key: "jwt_secret", Value: hex.EncodeToString(buf)}
		if err := s.db.Create(&st).Error; err != nil {
			return err
		}
	} else if err != nil {
		return err
	}
	s.secret = []byte(st.Value)
	return nil
}

// ensureAdmin 首启创建默认管理员 admin / admin123。
func (s *Service) ensureAdmin() error {
	var count int64
	if err := s.db.Model(&models.Admin{}).Count(&count).Error; err != nil {
		return err
	}
	if count > 0 {
		return nil
	}
	hash, err := bcrypt.GenerateFromPassword([]byte(DefaultPass), bcrypt.DefaultCost)
	if err != nil {
		return err
	}
	return s.db.Create(&models.Admin{Username: DefaultUser, PasswordHash: string(hash)}).Error
}

// Login 校验密码，签发新 token（旧 token 立即作废）。
func (s *Service) Login(username, password string) (string, *models.Admin, error) {
	var a models.Admin
	if err := s.db.Where("username = ?", username).First(&a).Error; err != nil {
		return "", nil, ErrBadCredentials
	}
	if bcrypt.CompareHashAndPassword([]byte(a.PasswordHash), []byte(password)) != nil {
		return "", nil, ErrBadCredentials
	}
	tok, err := s.issueToken(&a)
	if err != nil {
		return "", nil, err
	}
	return tok, &a, nil
}

// issueToken 签发新 JWT 并写库 + 写缓存；换掉老的（老 token 之后校验不过）。
func (s *Service) issueToken(a *models.Admin) (string, error) {
	now := time.Now()
	jti := make([]byte, 8)
	if _, err := rand.Read(jti); err != nil {
		return "", err
	}
	claims := Claims{
		AdminID: a.ID,
		RegisteredClaims: jwt.RegisteredClaims{
			ID:        hex.EncodeToString(jti),
			IssuedAt:  jwt.NewNumericDate(now),
			ExpiresAt: jwt.NewNumericDate(now.Add(TokenTTL)),
			Subject:   a.Username,
		},
	}
	tok, err := jwt.NewWithClaims(jwt.SigningMethodHS256, claims).SignedString(s.secret)
	if err != nil {
		return "", err
	}
	if err := s.db.Model(&models.Admin{}).Where("id = ?", a.ID).
		Updates(map[string]any{"token": tok, "token_issued_at": now}).Error; err != nil {
		return "", err
	}
	s.mu.Lock()
	s.tokens[a.ID] = cacheEntry{token: tok, issuedAt: now}
	s.mu.Unlock()
	return tok, nil
}

// Validate 校验 token；命中缓存直接比对，缓存缺失（如重启后）回读数据库。
// 若 token 签发已超过 2h，自动签发新 token 返回（newToken 非空表示已续期，旧的作废）。
func (s *Service) Validate(tokenStr string) (admin *models.Admin, newToken string, err error) {
	var claims Claims
	t, err := jwt.ParseWithClaims(tokenStr, &claims, func(t *jwt.Token) (any, error) {
		if _, ok := t.Method.(*jwt.SigningMethodHMAC); !ok {
			return nil, ErrInvalidToken
		}
		return s.secret, nil
	})
	if err != nil || !t.Valid {
		return nil, "", ErrInvalidToken
	}

	s.mu.Lock()
	entry, cached := s.tokens[claims.AdminID]
	s.mu.Unlock()

	var a models.Admin
	if err := s.db.First(&a, claims.AdminID).Error; err != nil {
		return nil, "", ErrInvalidToken
	}
	if !cached {
		// 重启后缓存丢了：从数据库读回当前有效 token
		entry = cacheEntry{token: a.Token, issuedAt: a.TokenIssuedAt}
		s.mu.Lock()
		s.tokens[a.ID] = entry
		s.mu.Unlock()
	}
	if entry.token == "" || entry.token != tokenStr {
		return nil, "", ErrInvalidToken // 已被新 token 顶掉
	}

	if time.Since(entry.issuedAt) > RenewAfter {
		nt, ierr := s.issueToken(&a)
		if ierr == nil {
			newToken = nt
		}
	}
	return &a, newToken, nil
}

// ChangePassword 修改密码（长度 >6），成功后强制签发新 token（旧的作废）。
func (s *Service) ChangePassword(adminID uint, oldPass, newPass string) (string, error) {
	if len(strings.TrimSpace(newPass)) < MinPasswordLen {
		return "", ErrWeakPassword
	}
	var a models.Admin
	if err := s.db.First(&a, adminID).Error; err != nil {
		return "", ErrBadCredentials
	}
	if bcrypt.CompareHashAndPassword([]byte(a.PasswordHash), []byte(oldPass)) != nil {
		return "", errors.New("原密码错误")
	}
	hash, err := bcrypt.GenerateFromPassword([]byte(newPass), bcrypt.DefaultCost)
	if err != nil {
		return "", err
	}
	if err := s.db.Model(&a).Update("password_hash", string(hash)).Error; err != nil {
		return "", err
	}
	return s.issueToken(&a)
}
