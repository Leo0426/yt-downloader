package main

import (
	"crypto/pbkdf2"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"time"
	"unicode"
	"unicode/utf8"
)

type permissionDefinition struct {
	Key   string `json:"key"`
	Label string `json:"label"`
}

var permissionCatalog = []permissionDefinition{
	{"jobs.view", "查看自己的下载任务"},
	{"jobs.viewAll", "查看所有用户的任务"},
	{"jobs.create", "新建下载和提交嗅探资源"},
	{"jobs.control", "取消、重试或删除自己的任务"},
	{"jobs.controlAll", "管理其他用户的任务"},
	{"jobs.logs", "查看任务日志"},
	{"jobs.files", "打开下载文件或所在目录"},
	{"jobs.ai", "启动 AI 辅助下载"},
	{"queue.manage", "暂停或恢复整个队列"},
	{"settings.manage", "管理下载偏好和 AI 服务"},
	{"storage.manage", "查看与清理缓存"},
	{"storage.reveal", "打开本机数据和全部日志目录"},
	{"tools.manage", "管理下载组件"},
	{"app.quit", "关闭下载服务"},
	{"users.manage", "管理用户和分配角色"},
	{"roles.manage", "管理角色和权限"},
}

type identityRole struct {
	ID          string   `json:"id"`
	Name        string   `json:"name"`
	Permissions []string `json:"permissions"`
	Builtin     bool     `json:"builtin"`
}

func builtinRoles() map[string]identityRole {
	all := make([]string, 0, len(permissionCatalog))
	for _, permission := range permissionCatalog {
		all = append(all, permission.Key)
	}
	return map[string]identityRole{
		"admin":    {"admin", "管理员", all, true},
		"operator": {"operator", "操作员", []string{"jobs.view", "jobs.create", "jobs.control", "jobs.logs", "jobs.files", "jobs.ai"}, true},
		"viewer":   {"viewer", "观察者", []string{"jobs.view"}, true},
	}
}

type identityPrincipal struct {
	ID          string   `json:"id"`
	Username    string   `json:"username"`
	Roles       []string `json:"roles"`
	RoleNames   []string `json:"roleNames"`
	Permissions []string `json:"permissions"`
}

func (p identityPrincipal) Can(key string) bool { return slices.Contains(p.Permissions, key) }

type publicUser struct {
	ID       string   `json:"id"`
	Username string   `json:"username"`
	Roles    []string `json:"roles"`
	Disabled bool     `json:"disabled"`
}

type passwordHash struct {
	Salt   []byte `json:"salt"`
	Digest []byte `json:"digest"`
}

const passwordIterations = 600_000 // PBKDF2-HMAC-SHA256 for identity file version 1.

func derivePassword(password string, salt []byte) ([]byte, error) {
	return pbkdf2.Key(sha256.New, password, salt, passwordIterations, sha256.Size)
}

func hashPassword(password string) (passwordHash, error) {
	if utf8.RuneCountInString(password) < 10 || len(password) > 256 {
		return passwordHash{}, errors.New("密码至少需要 10 个字符，最多 256 字节")
	}
	salt := make([]byte, 16)
	rand.Read(salt)
	digest, err := derivePassword(password, salt)
	if err != nil {
		return passwordHash{}, errors.New("无法保存密码")
	}
	return passwordHash{salt, digest}, nil
}

func (h passwordHash) matches(password string) bool {
	digest, err := derivePassword(password, h.Salt)
	return err == nil && subtle.ConstantTimeCompare(digest, h.Digest) == 1
}

type identityUser struct {
	publicUser
	Password passwordHash `json:"passwordHash"`
}

type identityData struct {
	Version     int                     `json:"version"`
	Users       map[string]identityUser `json:"users"`
	CustomRoles map[string]identityRole `json:"roles"`
}

type loginSession struct {
	userID  string
	expires time.Time
}
type authFailures struct {
	count   int
	expires time.Time
}

type identityStore struct {
	mu       sync.Mutex
	path     string
	data     identityData
	builtins map[string]identityRole
	sessions map[[sha256.Size]byte]loginSession
	failures map[string]authFailures
}

func newIdentityStore(root string) (*identityStore, error) {
	s := &identityStore{path: filepath.Join(root, "accounts.json"),
		data: identityData{1, map[string]identityUser{}, map[string]identityRole{}}, builtins: builtinRoles(),
		sessions: make(map[[sha256.Size]byte]loginSession), failures: make(map[string]authFailures)}
	b, err := os.ReadFile(s.path)
	if errors.Is(err, os.ErrNotExist) {
		return s, nil
	}
	if err != nil {
		return nil, err
	}
	var stored identityData
	if err = json.Unmarshal(b, &stored); err != nil {
		return nil, fmt.Errorf("无法读取账户数据库: %w", err)
	}
	if stored.Version != 1 || stored.Users == nil || stored.CustomRoles == nil {
		return nil, errors.New("无法读取账户数据库版本或内容")
	}
	// An existing account database can never legitimately become empty. A damaged file must
	// not reopen the first-run setup endpoint.
	if err = s.validate(stored); err != nil {
		return nil, fmt.Errorf("账户数据库无效: %w", err)
	}
	s.data = stored
	return s, nil
}

func (s *identityStore) role(data identityData, id string) (identityRole, bool) {
	if role, ok := s.builtins[id]; ok {
		return role, true
	}
	role, ok := data.CustomRoles[id]
	return role, ok
}

func identityNameKey(name string) string { return strings.ToLower(strings.TrimSpace(name)) }

func validIdentityName(name string) bool {
	return name != "" && strings.TrimSpace(name) == name && utf8.RuneCountInString(name) <= 64 && strings.IndexFunc(name, func(r rune) bool { return !unicode.IsPrint(r) }) < 0
}

// Validate before replacing the file and when loading it. Changes are committed only after
// their complete resulting authorization graph is valid.
func (s *identityStore) validate(data identityData) error {
	names := make(map[string]bool)
	for _, r := range s.builtins {
		names[identityNameKey(r.Name)] = true
	}
	for id, r := range data.CustomRoles {
		_, reserved := s.builtins[id]
		if id == "" || id != r.ID || reserved || r.Builtin || !validIdentityName(r.Name) {
			return errors.New("无效的角色")
		}
		name := identityNameKey(r.Name)
		if names[name] {
			return errors.New("角色名称已存在")
		}
		names[name] = true
		for _, key := range r.Permissions {
			if !slices.Contains(s.builtins["admin"].Permissions, key) {
				return errors.New("包含未定义的权限")
			}
		}
	}
	clear(names)
	hasAdmin := false
	for id, u := range data.Users {
		if id == "" || id != u.ID || !validIdentityName(u.Username) || len(u.Password.Salt) != 16 || len(u.Password.Digest) != sha256.Size {
			return errors.New("无效的账户或密码记录")
		}
		name := identityNameKey(u.Username)
		if names[name] {
			return errors.New("用户名已存在")
		}
		names[name] = true
		for _, roleID := range u.Roles {
			if _, ok := s.role(data, roleID); !ok {
				return errors.New("所选角色不存在，使用中的角色无法删除")
			}
		}
		if !u.Disabled && slices.Contains(u.Roles, "admin") {
			hasAdmin = true
		}
	}
	if !hasAdmin {
		return errors.New("必须保留至少一个启用的管理员账户")
	}
	return nil
}

func (s *identityStore) commit(next identityData) error {
	if err := s.validate(next); err != nil {
		return err
	}
	raw, err := json.MarshalIndent(next, "", "  ")
	if err != nil {
		return err
	}
	if err = atomicWrite(s.path, raw); err != nil {
		return errors.New("账户数据保存失败，请检查数据目录")
	}
	s.data = next
	return nil
}

func (s *identityStore) userPrincipal(u identityUser) identityPrincipal {
	p := identityPrincipal{ID: u.ID, Username: u.Username, Roles: u.Roles, RoleNames: []string{}, Permissions: []string{}}
	for _, id := range u.Roles {
		r, _ := s.role(s.data, id)
		p.RoleNames = append(p.RoleNames, r.Name)
		for _, key := range r.Permissions {
			if !p.Can(key) {
				p.Permissions = append(p.Permissions, key)
			}
		}
	}
	slices.Sort(p.Permissions)
	return p
}

// Cookie names are scoped by server port since loopback instances may use different data
// directories. A launch cookie and an authenticated account cookie serve distinct purposes.
func accountCookieName(r *http.Request) string {
	_, port, _ := net.SplitHostPort(r.Host)
	return "ytd_account_" + port
}

func (s *identityStore) principalLocked(r *http.Request) (identityPrincipal, bool) {
	c, err := r.Cookie(accountCookieName(r))
	if err != nil || c.Value == "" || len(c.Value) > 128 {
		return identityPrincipal{}, false
	}
	key := sha256.Sum256([]byte(c.Value))
	sess, ok := s.sessions[key]
	if !ok {
		return identityPrincipal{}, false
	}
	u, ok := s.data.Users[sess.userID]
	if !ok || u.Disabled || !time.Now().Before(sess.expires) {
		delete(s.sessions, key)
		return identityPrincipal{}, false
	}
	return s.userPrincipal(u), true
}
func (s *identityStore) principal(r *http.Request) (identityPrincipal, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.principalLocked(r)
}

const sessionLifetime = 12 * time.Hour

func (s *identityStore) logout(w http.ResponseWriter, r *http.Request, secure bool) {
	name := accountCookieName(r)
	if c, err := r.Cookie(name); err == nil {
		delete(s.sessions, sha256.Sum256([]byte(c.Value)))
	}
	http.SetCookie(w, &http.Cookie{Name: name, Path: "/", MaxAge: -1, HttpOnly: true, Secure: secure, SameSite: http.SameSiteStrictMode})
}

func (s *identityStore) issue(w http.ResponseWriter, r *http.Request, id string, secure bool) {
	now := time.Now()
	if old, err := r.Cookie(accountCookieName(r)); err == nil {
		delete(s.sessions, sha256.Sum256([]byte(old.Value)))
	}
	for key, session := range s.sessions {
		if !now.Before(session.expires) {
			delete(s.sessions, key)
		}
	}
	raw := rand.Text()
	expires := now.Add(sessionLifetime)
	s.sessions[sha256.Sum256([]byte(raw))] = loginSession{id, expires}
	http.SetCookie(w, &http.Cookie{Name: accountCookieName(r), Value: raw, Path: "/", Expires: expires, MaxAge: int(sessionLifetime.Seconds()), HttpOnly: true, Secure: secure, SameSite: http.SameSiteStrictMode})
}

func (s *identityStore) revokeUser(id string) {
	for key, sess := range s.sessions {
		if sess.userID == id {
			delete(s.sessions, key)
		}
	}
}

func (s *identityStore) ownerNames(ids []string) map[string]string {
	s.mu.Lock()
	defer s.mu.Unlock()
	names := make(map[string]string)
	for _, id := range ids {
		if u, ok := s.data.Users[id]; ok {
			names[id] = u.Username
		}
	}
	return names
}
