package main

import (
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/json"
	"io"
	"maps"
	"net/http"
	"slices"
	"sort"
	"strings"
	"time"
)

type identityInput struct {
	ID              string   `json:"id"`
	Username        string   `json:"username"`
	Password        string   `json:"password"`
	NewPassword     string   `json:"newPassword"`
	CurrentPassword string   `json:"currentPassword"`
	Roles           []string `json:"roles"`
	Disabled        *bool    `json:"disabled"`
	Name            string   `json:"name"`
	Permissions     []string `json:"permissions"`
}

func identityReply(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(value)
}

func identityError(w http.ResponseWriter, status int, message string) {
	identityReply(w, status, map[string]string{"error": message})
}

func (s *identityStore) sessionReply(w http.ResponseWriter, p *identityPrincipal, mode deployment) {
	empty := len(s.data.Users) == 0
	identityReply(w, 200, map[string]any{"setupRequired": empty, "setupTokenRequired": mode.web && empty, "deployment": mode.name(), "user": p})
}

func (s *identityStore) serve(w http.ResponseWriter, r *http.Request, mode deployment) {
	var in identityInput
	if r.Method == http.MethodPost {
		r.Body = http.MaxBytesReader(w, r.Body, 32<<10)
		decoder := json.NewDecoder(r.Body)
		decoder.DisallowUnknownFields()
		if decoder.Decode(&in) != nil || decoder.Decode(new(any)) != io.EOF {
			identityError(w, 400, "无效的账户请求")
			return
		}
	} else if r.Method != http.MethodGet {
		identityError(w, 405, "不支持的请求")
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	p, signedIn := s.principalLocked(r)
	// Recheck under the same lock that commits account mutations. A tab may have a
	// fresh browser cookie by the time it submits an old account form.
	checkAccount := r.URL.Path != "/api/auth/setup" && r.URL.Path != "/api/auth/login" && r.URL.Path != "/api/auth/session"
	if expected := r.Header.Get("X-YTD-User"); checkAccount && expected != "" && (!signedIn || expected != p.ID) {
		identityError(w, 401, "账户已切换，请重新打开")
		return
	}
	switch r.Method + " " + r.URL.Path {
	case "GET /api/auth/session":
		if signedIn {
			s.sessionReply(w, &p, mode)
		} else {
			s.sessionReply(w, nil, mode)
		}
		return
	case "POST /api/auth/setup":
		if len(s.data.Users) != 0 {
			identityError(w, 409, "管理员已经创建，请登录")
			return
		}
		if mode.web && (mode.setupToken == "" || subtle.ConstantTimeCompare([]byte(r.Header.Get("X-YTD-Setup-Token")), []byte(mode.setupToken)) != 1) {
			identityError(w, 403, "初始化码无效，请使用服务启动时提供的管理员初始化码")
			return
		}
		name := strings.TrimSpace(in.Username)
		if !validIdentityName(name) {
			identityError(w, 400, "用户名需为 1 至 64 个字符")
			return
		}
		password, err := hashPassword(in.Password)
		if err != nil {
			identityError(w, 400, err.Error())
			return
		}
		u := identityUser{publicUser: publicUser{ID: rand.Text(), Username: name, Roles: []string{"admin"}}, Password: password}
		next := s.data
		next.Users = map[string]identityUser{u.ID: u}
		if err = s.commit(next); err != nil {
			identityError(w, 400, err.Error())
			return
		}
		s.issue(w, r, u.ID, mode.secure)
		p = s.userPrincipal(u)
		s.sessionReply(w, &p, mode)
		return
	case "POST /api/auth/login":
		var u identityUser
		key := identityNameKey(in.Username)
		for _, candidate := range s.data.Users {
			if identityNameKey(candidate.Username) == key {
				u = candidate
				break
			}
		}
		if !s.verifyCredential(w, u, in.Password) {
			return
		}
		s.issue(w, r, u.ID, mode.secure)
		p = s.userPrincipal(u)
		s.sessionReply(w, &p, mode)
		return
	case "POST /api/auth/logout":
		s.logout(w, r, mode.secure)
		s.sessionReply(w, nil, mode)
		return
	}
	if !signedIn {
		identityError(w, 401, "请先登录")
		return
	}
	switch r.Method + " " + r.URL.Path {
	case "POST /api/auth/password":
		u := s.data.Users[p.ID]
		if !s.verifyCredential(w, u, in.CurrentPassword) {
			return
		}
		password, err := hashPassword(in.NewPassword)
		if err != nil {
			identityError(w, 400, err.Error())
			return
		}
		next := s.data
		next.Users = maps.Clone(next.Users)
		u.Password = password
		next.Users[p.ID] = u
		if err = s.commit(next); err != nil {
			identityError(w, 400, err.Error())
			return
		}
		s.revokeUser(u.ID)
		s.issue(w, r, u.ID, mode.secure)
		s.sessionReply(w, &p, mode)
	case "GET /api/access":
		if !p.Can("users.manage") && !p.Can("roles.manage") {
			identityError(w, 403, "无权查看账户管理")
			return
		}
		s.accessView(w, p)
	case "POST /api/access/users/save", "POST /api/access/users/delete":
		if !p.Can("users.manage") {
			identityError(w, 403, "无权管理用户")
			return
		}
		s.mutateUser(w, p, in, strings.HasSuffix(r.URL.Path, "/delete"))
	case "POST /api/access/roles/save", "POST /api/access/roles/delete":
		if !p.Can("roles.manage") {
			identityError(w, 403, "无权管理角色")
			return
		}
		s.mutateRole(w, p, in, strings.HasSuffix(r.URL.Path, "/delete"))
	default:
		identityError(w, 404, "找不到账户功能")
	}
}

const authAttemptWindow = 5 * time.Minute

func (s *identityStore) verifyCredential(w http.ResponseWriter, u identityUser, password string) bool {
	now := time.Now()
	accountScope := "user/" + u.ID // Unknown names share a bounded bucket.
	for key, attempt := range s.failures {
		if !now.Before(attempt.expires) {
			delete(s.failures, key)
		}
	}
	if s.failures[accountScope].count >= 8 || s.failures["global"].count >= 40 {
		w.Header().Set("Retry-After", "300")
		identityError(w, 429, "尝试次数过多，请在 5 分钟后重试")
		return false
	}
	hash := u.Password
	if u.ID == "" {
		hash = passwordHash{Salt: make([]byte, 16), Digest: make([]byte, sha256.Size)}
	}
	if len(password) <= 256 && hash.matches(password) && u.ID != "" && !u.Disabled {
		delete(s.failures, accountScope)
		return true
	}
	for _, key := range []string{accountScope, "global"} {
		attempt := s.failures[key]
		if attempt.count == 0 {
			attempt.expires = now.Add(authAttemptWindow)
		}
		attempt.count++
		s.failures[key] = attempt
	}
	identityError(w, 401, "用户名或密码不正确")
	return false
}

func (p identityPrincipal) covers(permissions []string) bool {
	for _, key := range permissions {
		if !p.Can(key) {
			return false
		}
	}
	return true
}

func (s *identityStore) mayAssign(p identityPrincipal, roleIDs []string) bool {
	for _, id := range roleIDs {
		r, ok := s.role(s.data, id)
		if !ok || (id == "admin" && !slices.Contains(p.Roles, "admin")) || !p.covers(r.Permissions) {
			return false
		}
	}
	return true
}

func (s *identityStore) accessView(w http.ResponseWriter, p identityPrincipal) {
	type listedUser struct {
		publicUser
		Manageable bool `json:"manageable"`
	}
	type listedRole struct {
		identityRole
		Manageable bool `json:"manageable"`
		Assignable bool `json:"assignable"`
	}
	users := []listedUser{}
	if p.Can("users.manage") {
		for _, u := range s.data.Users {
			users = append(users, listedUser{u.publicUser, s.mayAssign(p, u.Roles)})
		}
		sort.Slice(users, func(i, j int) bool { return users[i].Username < users[j].Username })
	}
	roles := []listedRole{}
	for _, group := range []map[string]identityRole{s.builtins, s.data.CustomRoles} {
		for _, r := range group {
			manageable := !r.Builtin && p.Can("roles.manage") && p.covers(r.Permissions)
			roles = append(roles, listedRole{r, manageable, p.Can("users.manage") && s.mayAssign(p, []string{r.ID})})
		}
	}
	sort.Slice(roles, func(i, j int) bool {
		if roles[i].Builtin != roles[j].Builtin {
			return roles[i].Builtin
		}
		return roles[i].Name < roles[j].Name
	})
	identityReply(w, 200, map[string]any{"users": users, "roles": roles, "permissions": permissionCatalog})
}

func sortedIDs(values []string) []string {
	result := append([]string{}, values...)
	slices.Sort(result)
	return slices.Compact(result)
}

func (s *identityStore) mutateUser(w http.ResponseWriter, p identityPrincipal, in identityInput, remove bool) {
	next := s.data
	next.Users = maps.Clone(next.Users)
	u, found := next.Users[in.ID]
	if !found && (remove || in.ID != "") {
		identityError(w, 404, "找不到用户")
		return
	}
	if found && !s.mayAssign(p, u.Roles) {
		identityError(w, 403, "权限不足以管理此用户")
		return
	}
	if remove {
		delete(next.Users, u.ID)
	} else {
		if !found {
			u.ID = rand.Text()
			if in.Password == "" {
				identityError(w, 400, "新建账户需要初始密码")
				return
			}
		}
		if in.Roles != nil || !found {
			if !s.mayAssign(p, in.Roles) {
				identityError(w, 403, "不能授予所选角色")
				return
			}
			u.Roles = sortedIDs(in.Roles)
		}
		u.Username = strings.TrimSpace(in.Username)
		if !validIdentityName(u.Username) {
			identityError(w, 400, "用户名需为 1 至 64 个字符")
			return
		}
		if in.Disabled != nil {
			u.Disabled = *in.Disabled
		}
		if in.Password != "" {
			hash, err := hashPassword(in.Password)
			if err != nil {
				identityError(w, 400, err.Error())
				return
			}
			u.Password = hash
		}
		next.Users[u.ID] = u
	}
	if err := s.commit(next); err != nil {
		identityError(w, 400, err.Error())
		return
	}
	if remove || u.Disabled || in.Password != "" {
		s.revokeUser(u.ID)
		delete(s.failures, "user/"+u.ID)
	}
	identityReply(w, 200, struct{}{})
}

func (s *identityStore) mutateRole(w http.ResponseWriter, p identityPrincipal, in identityInput, remove bool) {
	next := s.data
	next.CustomRoles = maps.Clone(next.CustomRoles)
	r, found := s.role(s.data, in.ID)
	if !found && (remove || in.ID != "") {
		identityError(w, 404, "找不到角色")
		return
	}
	if r.Builtin {
		identityError(w, 403, "内置角色为只读")
		return
	}
	if found && !p.covers(r.Permissions) {
		identityError(w, 403, "权限不足以管理该角色")
		return
	}
	if remove {
		for _, u := range next.Users {
			if slices.Contains(u.Roles, r.ID) {
				identityError(w, 409, "角色仍分配给用户，请先解除分配")
				return
			}
		}
		delete(next.CustomRoles, r.ID)
	} else {
		if !found {
			r.ID = rand.Text()
		}
		r.Name = strings.TrimSpace(in.Name)
		if !validIdentityName(r.Name) {
			identityError(w, 400, "角色名称需为 1 至 64 个字符")
			return
		}
		if in.Permissions == nil {
			identityError(w, 400, "缺少权限列表")
			return
		}
		if !p.covers(in.Permissions) {
			identityError(w, 403, "不能授予自己未持有的权限")
			return
		}
		r.Permissions = sortedIDs(in.Permissions)
		next.CustomRoles[r.ID] = r
	}
	if err := s.commit(next); err != nil {
		identityError(w, 400, err.Error())
		return
	}
	identityReply(w, 200, struct{}{})
}
