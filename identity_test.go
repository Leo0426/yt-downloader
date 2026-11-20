package main

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"
)

type accountClient struct {
	t      *testing.T
	s      *identityStore
	cookie *http.Cookie
}

func newAccountClient(t *testing.T) *accountClient {
	t.Helper()
	s, err := newIdentityStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	return &accountClient{t: t, s: s}
}

func (c *accountClient) call(path string, input any, code int) []byte {
	c.t.Helper()
	method := http.MethodGet
	var body []byte
	if input != nil {
		method = http.MethodPost
		var err error
		body, err = json.Marshal(input)
		if err != nil {
			c.t.Fatal(err)
		}
	}
	req := httptest.NewRequest(method, "http://127.0.0.1:40000/api/"+path, bytes.NewReader(body))
	if c.cookie != nil {
		req.AddCookie(c.cookie)
	}
	w := httptest.NewRecorder()
	c.s.serve(w, req, deployment{})
	if w.Code != code {
		c.t.Fatalf("%s: expected %d, got %d %s", path, code, w.Code, w.Body.String())
	}
	for _, cookie := range w.Result().Cookies() {
		c.cookie = cookie
	}
	return w.Body.Bytes()
}

type accountStatus struct {
	Setup bool               `json:"setupRequired"`
	User  *identityPrincipal `json:"user"`
}

func (c *accountClient) status() accountStatus {
	c.t.Helper()
	var result accountStatus
	if err := json.Unmarshal(c.call("auth/session", nil, 200), &result); err != nil {
		c.t.Fatal(err)
	}
	return result
}
func (c *accountClient) createAdmin() {
	c.t.Helper()
	c.call("auth/setup", map[string]string{"username": "Owner", "password": "bootstrap-passphrase"}, 200)
	if c.cookie == nil || !c.cookie.HttpOnly || c.cookie.Path != "/" || c.cookie.SameSite != http.SameSiteStrictMode {
		c.t.Fatal("unsafe account cookie")
	}
}
func (c *accountClient) setUser(name, password string, roles ...string) string {
	c.t.Helper()
	c.call("access/users/save", map[string]any{"username": name, "password": password, "roles": roles}, 200)
	for id, u := range c.s.data.Users {
		if u.Username == name {
			return id
		}
	}
	c.t.Fatalf("could not find user %s", name)
	return ""
}
func (c *accountClient) setRole(name string, permissions ...string) string {
	c.t.Helper()
	c.call("access/roles/save", map[string]any{"name": name, "permissions": permissions}, 200)
	for id, r := range c.s.data.CustomRoles {
		if r.Name == name {
			return id
		}
	}
	c.t.Fatal("could not find role")
	return ""
}

func TestIdentityLifecycleAndPasswordSessions(t *testing.T) {
	owner := newAccountClient(t)
	if st := owner.status(); st.User != nil || !st.Setup {
		t.Fatal("first run must request setup")
	}
	owner.call("access", nil, 401)
	owner.createAdmin()
	owner.call("auth/setup", map[string]string{"username": "other", "password": "bootstrap-passphrase"}, 409)
	admin := owner.status().User
	if admin == nil || !admin.Can("settings.manage") || !admin.Can("storage.reveal") {
		t.Fatal(admin)
	}
	owner.call("access/users/delete", map[string]string{"id": admin.ID}, 400)
	owner.call("access/users/save", map[string]any{"id": admin.ID, "username": admin.Username, "disabled": true}, 400)
	owner.call("access/users/save", map[string]any{"id": admin.ID, "username": admin.Username, "roles": []string{}}, 400)
	id := owner.setUser("Σ", "sigma-passphrase", "viewer")
	owner.setUser("ς", "final-sigma-passphrase", "operator")
	account := accountClient{t: t, s: owner.s}
	account.call("auth/login", map[string]string{"username": " σ ", "password": "sigma-passphrase"}, 200)
	if p := account.status().User; p == nil || p.ID != id || !p.Can("jobs.view") || p.Can("jobs.create") {
		t.Fatal(p)
	}
	account.call("access", nil, 403)
	another := accountClient{t: t, s: owner.s}
	another.call("auth/login", map[string]string{"username": "Σ", "password": "sigma-passphrase"}, 200)
	account.call("auth/password", map[string]string{"currentPassword": "wrong", "newPassword": "next-passphrase"}, 401)
	account.call("auth/password", map[string]string{"currentPassword": "sigma-passphrase", "newPassword": "next-passphrase"}, 200)
	if account.status().User == nil || another.status().User != nil {
		t.Fatal("changing password must rotate sessions")
	}
	another.call("auth/login", map[string]string{"username": "Σ", "password": "sigma-passphrase"}, 401)
	another.call("auth/login", map[string]string{"username": "ς", "password": "final-sigma-passphrase"}, 200)
	if another.status().User.ID == id {
		t.Fatal("case mapping must match uniqueness rules")
	}
	owner.call("access/users/save", map[string]any{"id": id, "username": "Σ", "password": "reset-passphrase"}, 200)
	if account.status().User != nil {
		t.Fatal("password reset must revoke old sessions")
	}
	account.call("auth/login", map[string]string{"username": "σ", "password": "reset-passphrase"}, 200)
	owner.call("access/users/save", map[string]any{"id": id, "username": "Σ", "disabled": true}, 200)
	owner.call("access/users/save", map[string]any{"id": id, "username": "Σ", "disabled": false}, 200)
	if account.status().User != nil {
		t.Fatal("re-enabling a user must not revive revoked sessions")
	}
	again, err := newIdentityStore(filepath.Dir(owner.s.path))
	if err != nil {
		t.Fatal(err)
	}
	account.s = again
	account.call("auth/login", map[string]string{"username": "Σ", "password": "reset-passphrase"}, 200)
	stale := account
	account.call("auth/logout", map[string]string{}, 200)
	if stale.status().User != nil {
		t.Fatal("server must revoke logged-out session")
	}
	raw, err := os.ReadFile(again.path)
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(raw, []byte("passphrase")) || bytes.Contains(raw, []byte(stale.cookie.Value)) {
		t.Fatal("secret persisted")
	}
	info, err := os.Stat(again.path)
	if err != nil {
		t.Fatal(err)
	}
	if runtime.GOOS != "windows" && info.Mode().Perm()&0077 != 0 {
		t.Fatal("account file is accessible by other OS users")
	}
	if strings.Contains(string(owner.call("access", nil, 200)), "digest") {
		t.Fatal("management directory includes credential material")
	}
}

func TestIdentityDelegationAndLiveRoleChanges(t *testing.T) {
	owner := newAccountClient(t)
	owner.createAdmin()
	consumerRole := owner.setRole("日志查看", "jobs.logs", "jobs.view")
	delegation := owner.setRole("用户与角色管理", "roles.manage", "users.manage", "jobs.view", "jobs.logs")
	consumerID := owner.setUser("测试成员", "reader-passphrase", consumerRole)
	managerID := owner.setUser("委托管理", "manager-passphrase", delegation)
	member := accountClient{t: t, s: owner.s}
	member.call("auth/login", map[string]string{"username": "测试成员", "password": "reader-passphrase"}, 200)
	manager := accountClient{t: t, s: owner.s}
	manager.call("auth/login", map[string]string{"username": "委托管理", "password": "manager-passphrase"}, 200)
	// Another tab can switch the shared cookie while an old form is still on screen.
	form := httptest.NewRequest("POST", "http://127.0.0.1:40000/api/access/users/delete", strings.NewReader(`{"id":"`+consumerID+`"}`))
	form.AddCookie(owner.cookie)
	form.Header.Set("X-YTD-User", managerID)
	rejected := httptest.NewRecorder()
	owner.s.serve(rejected, form, deployment{})
	if rejected.Code != http.StatusUnauthorized || member.status().User == nil {
		t.Fatal("stale account form was accepted")
	}
	manager.call("access/roles/save", map[string]any{"name": "高级权限", "permissions": []string{"storage.reveal"}}, 403)
	manager.call("access/users/save", map[string]any{"id": managerID, "username": "委托管理", "roles": []string{"admin"}}, 403)
	manager.call("access/users/delete", map[string]string{"id": owner.status().User.ID}, 403)
	manager.call("access/roles/save", map[string]any{"id": "admin", "name": "管理员", "permissions": []string{}}, 403)
	manager.call("access/roles/delete", map[string]string{"id": consumerRole}, 409)
	manager.call("access/roles/save", map[string]any{"id": consumerRole, "name": "仅查看", "permissions": []string{"jobs.view"}}, 200)
	if p := member.status().User; p == nil || p.Can("jobs.logs") || !p.Can("jobs.view") {
		t.Fatal("active session ignored role edit")
	}
	owner.call("access/users/save", map[string]any{"id": consumerID, "username": "测试成员", "roles": []string{"operator", consumerRole}}, 200)
	if p := member.status().User; p == nil || !p.Can("jobs.create") || !p.Can("jobs.logs") || len(p.Roles) != 2 {
		t.Fatal("roles must be unioned")
	}
	manager.call("access/users/delete", map[string]string{"id": consumerID}, 403)
	owner.call("access/users/delete", map[string]string{"id": consumerID}, 200)
	owner.call("access/roles/delete", map[string]string{"id": consumerRole}, 200)
	if member.status().User != nil {
		t.Fatal("deleted user retained session")
	}
}

func TestIdentityConcurrentSetupAndFailedCommit(t *testing.T) {
	c := newAccountClient(t)
	const payload = `{"username":"owner","password":"test-passphrase"}`
	var wg sync.WaitGroup
	codes := make(chan int, 4)
	for i := 0; i < 4; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			w := httptest.NewRecorder()
			c.s.serve(w, httptest.NewRequest("POST", "http://127.0.0.1:40000/api/auth/setup", strings.NewReader(payload)), deployment{})
			codes <- w.Code
		}()
	}
	wg.Wait()
	close(codes)
	winners := 0
	for code := range codes {
		if code == 200 {
			winners++
		} else if code != 409 {
			t.Fatal(code)
		}
	}
	if winners != 1 || len(c.s.data.Users) != 1 {
		t.Fatal("concurrent bootstrap created multiple accounts")
	}
	c.call("auth/login", map[string]string{"username": "Owner", "password": "test-passphrase"}, 200)
	c.s.path = filepath.Join(t.TempDir(), "missing", "accounts.json")
	c.call("access/users/save", map[string]any{"id": c.status().User.ID, "username": "changed", "roles": []string{"admin"}}, 400)
	if c.status().User.Username != "owner" {
		t.Fatal("failed write mutated active identity")
	}
	if err := os.WriteFile(filepath.Join(filepath.Dir(filepath.Dir(c.s.path)), "accounts.json"), []byte(`{"version":1,"users":{},"roles":{}}`), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := newIdentityStore(filepath.Dir(filepath.Dir(c.s.path))); err == nil {
		t.Fatal("damaged existing data must not reopen setup")
	}
}

func TestIdentityLoginLimitAndSessionExpiry(t *testing.T) {
	c := newAccountClient(t)
	c.createAdmin()
	for i := 0; i < 8; i++ {
		c.call("auth/login", map[string]string{"username": "owner", "password": "invalid"}, 401)
	}
	c.call("auth/login", map[string]string{"username": "Owner", "password": "bootstrap-passphrase"}, 429)
	for key, sess := range c.s.sessions {
		sess.expires = time.Now().Add(-time.Second)
		c.s.sessions[key] = sess
	}
	if c.status().User != nil {
		t.Fatal("expired session accepted")
	}
}
