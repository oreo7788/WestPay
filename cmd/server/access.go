package main

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"sort"
	"strings"
	"time"

	"golang.org/x/crypto/bcrypt"
)

// Permission definitions are server-owned. Granting a menu never grants writes.
type Permission struct {
	Code     string   `json:"code"`
	Name     string   `json:"name"`
	Page     string   `json:"page"`
	Merchant bool     `json:"merchant"`
	Requires []string `json:"requires"`
}

func permissionCatalog() []Permission {
	out := []Permission{}
	add := func(page, name string, merchant bool, actions ...string) {
		out = append(out, Permission{page + ".view", name + "：查看", page, merchant, []string{}})
		labels := map[string]string{"create": "新增", "edit": "编辑", "toggle": "启停", "adjust": "资金调整", "export": "导出", "query": "主动查单", "qr": "查看二维码", "simulate": "模拟支付结果", "apikey": "管理接口密钥"}
		for _, a := range actions {
			out = append(out, Permission{page + "." + a, name + "：" + labels[a], page, merchant, []string{page + ".view"}})
		}
	}
	add("home", "首页", true)
	for _, panel := range []struct {
		key, name string
		merchant  bool
	}{
		{"notice", "运行提示", true},
		{"today_orders", "今日充值", true},
		{"today_success", "成交笔数", true},
		{"today_amount", "成交金额", true},
		{"success_rate", "成功率", true},
		{"profit", "平台利润", false},
		{"month_amount", "当月成交金额", true},
		{"yesterday_orders", "昨日下单笔数", true},
		{"yesterday_amount", "昨日成交金额", true},
		{"channel_count", "今日通道数", false},
		{"merchant_count", "商户数量", false},
		{"trend", "近 12 时段成交趋势", true},
		{"channels", "当天通道统计", false},
		{"latest_orders", "最新收款订单", true},
		{"merchant_ranking", "当天商户排行", false},
	} {
		out = append(out, Permission{"home." + panel.key, "首页：" + panel.name, "home", panel.merchant, []string{"home.view"}})
	}
	add("orders", "收款订单", true, "qr", "query", "export")
	// Creating orders remains internal until merchant-owned channel onboarding is implemented.
	out = append(out, Permission{"orders.create", "收款订单：新增", "orders", false, []string{"orders.view"}}, Permission{"orders.simulate", "收款订单：模拟支付结果", "orders", false, []string{"orders.view"}})
	add("history", "历史订单", true, "qr", "query", "export")
	add("merchants", "商户列表", false, "create", "edit", "toggle", "adjust", "apikey")
	add("products", "产品设置", false, "create", "edit")
	add("channels", "全局通道配置", false, "create", "edit", "toggle")
	for _, x := range []struct{ key, name string }{{"merchant_settlement", "商户对账（汇总）"}, {"finance_channels", "通道数据"}, {"finance_products", "产品数据"}, {"finance_merchants", "商户数据"}, {"prepay", "资金流水"}, {"statistics", "订单统计"}} {
		add(x.key, x.name, true, "export")
	}
	for _, x := range []struct{ key, name string }{{"receivers", "收钱吧资料"}, {"complaints", "投诉登记"}, {"official", "公众号小程序资料"}, {"blacklist", "黑名单"}, {"transfers", "付款记录登记"}, {"user_ids", "用户标识"}} {
		add(x.key, x.name, false, "create", "edit")
	}
	for _, x := range []struct{ key, name string }{{"site_settings", "网站配置"}, {"pay_settings", "支付配置"}, {"quick_login", "快捷登录配置"}} {
		add(x.key, x.name, false, "edit")
	}
	for _, x := range []struct{ key, name string }{{"methods", "支付方式"}, {"plugins", "支付插件"}, {"robot", "机器人说明"}, {"logs", "操作日志"}} {
		add(x.key, x.name, false)
	}
	pages := map[string]string{}
	for _, p := range out {
		if strings.HasSuffix(p.Code, ".view") {
			pages[p.Page] = strings.TrimSuffix(p.Name, "：查看")
		}
	}
	for _, page := range []string{"orders", "history", "merchants", "products", "channels", "merchant_settlement", "finance_channels", "finance_products", "finance_merchants", "prepay", "statistics", "receivers", "complaints", "official", "blacklist", "transfers", "user_ids"} {
		for _, f := range queryFilters(page) {
			out = append(out, Permission{page + "." + f.Key, pages[page] + "：" + f.Name, page, f.Merchant, []string{page + ".view"}})
		}
	}
	return out
}
func (u User) can(code string) bool {
	if u.Super {
		return true
	}
	for _, p := range u.Permissions {
		if p == code {
			return true
		}
	}
	return false
}
func (u User) any(codes ...string) bool {
	for _, c := range codes {
		if u.can(c) {
			return true
		}
	}
	return false
}
func (u User) merchant() bool { return u.Identity == "merchant" }

const accessSchema = `
CREATE TABLE IF NOT EXISTS access_migrations(version INTEGER PRIMARY KEY,applied_at TEXT NOT NULL);
CREATE TABLE IF NOT EXISTS user_access(user_id TEXT PRIMARY KEY REFERENCES users(id),identity TEXT NOT NULL CHECK(identity IN ('internal','merchant')),merchant_id TEXT REFERENCES merchants(id),enabled INTEGER NOT NULL CHECK(enabled IN (0,1)),is_super INTEGER NOT NULL CHECK(is_super IN (0,1)),CHECK((identity='internal' AND merchant_id IS NULL) OR (identity='merchant' AND merchant_id IS NOT NULL AND is_super=0)));
CREATE TABLE IF NOT EXISTS access_roles(id TEXT PRIMARY KEY,name TEXT NOT NULL UNIQUE,identity TEXT NOT NULL CHECK(identity IN ('internal','merchant')),enabled INTEGER NOT NULL CHECK(enabled IN (0,1)));
CREATE TABLE IF NOT EXISTS role_permissions(role_id TEXT NOT NULL REFERENCES access_roles(id),permission TEXT NOT NULL,PRIMARY KEY(role_id,permission));
CREATE TABLE IF NOT EXISTS user_roles(user_id TEXT NOT NULL REFERENCES users(id),role_id TEXT NOT NULL REFERENCES access_roles(id),PRIMARY KEY(user_id,role_id));
`

func (s *Store) migrateAccess(path string) error {
	var exists int
	if e := s.db.QueryRow("SELECT COUNT(*) FROM sqlite_master WHERE type='table' AND name='access_migrations'").Scan(&exists); e != nil {
		return e
	}
	if exists > 0 {
		return nil
	} // The complete version is committed atomically below.
	var n int
	if e := s.db.QueryRow("SELECT COUNT(*) FROM users").Scan(&n); e != nil {
		return e
	}
	if n > 0 {
		backup := path + ".before-access-" + id("") + ".bak"
		file, e := os.OpenFile(backup, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
		if e != nil {
			return e
		}
		if e = file.Close(); e != nil {
			return e
		}
		if _, e = s.db.Exec("VACUUM INTO ?", backup); e != nil {
			return fmt.Errorf("权限迁移备份失败: %w", e)
		}
	}
	return s.transaction(func(tx *sql.Tx) error {
		if _, e := tx.Exec(accessSchema); e != nil {
			return e
		}
		for _, role := range []struct{ id, name, identity string }{{"legacy-admin", "原管理员", "internal"}, {"legacy-operator", "原操作员", "internal"}, {"legacy-viewer", "原只读账号", "internal"}, {"merchant-reader", "商户查看", "merchant"}} {
			if _, e := tx.Exec("INSERT INTO access_roles VALUES(?,?,?,1)", role.id, role.name, role.identity); e != nil {
				return e
			}
			for _, p := range permissionCatalog() {
				grant := role.id == "legacy-admin" || role.id == "legacy-viewer" && strings.HasSuffix(p.Code, ".view") || role.id == "legacy-operator" && !strings.HasPrefix(p.Code, "site_settings.edit") && !strings.HasPrefix(p.Code, "pay_settings.edit") && !strings.HasPrefix(p.Code, "quick_login.edit") && !strings.HasSuffix(p.Code, ".apikey") || role.id == "merchant-reader" && p.Merchant && strings.HasSuffix(p.Code, ".view")
				if grant {
					if _, e := tx.Exec("INSERT INTO role_permissions VALUES(?,?)", role.id, p.Code); e != nil {
						return e
					}
				}
			}
		}
		if _, e := tx.Exec("INSERT INTO user_access SELECT id,'internal',NULL,1,0 FROM users"); e != nil {
			return e
		}
		if _, e := tx.Exec("INSERT INTO user_roles SELECT id,'legacy-'||role FROM users"); e != nil {
			return e
		}
		// Only the sole original administrator is unambiguous. Multiple old accounts require an explicit offline selection.
		if n == 1 {
			if _, e := tx.Exec("UPDATE user_access SET is_super=1 WHERE user_id IN (SELECT id FROM users WHERE role='admin')"); e != nil {
				return e
			}
		}
		if _, e := tx.Exec("DELETE FROM sessions"); e != nil {
			return e
		}
		if _, e := tx.Exec("INSERT INTO audit(actor,action,target,detail,ip,created_at) VALUES('系统迁移','权限结构升级','access-v1','保留原密码及角色映射；仅唯一旧管理员自动成为超管；旧会话失效','',?)", timestamp()); e != nil {
			return e
		}
		_, e := tx.Exec("INSERT INTO access_migrations VALUES(1,?)", timestamp())
		return e
	})
}
func (s *Store) loadAccess(u *User) error {
	e := s.db.QueryRow("SELECT identity,COALESCE(merchant_id,''),enabled,is_super FROM user_access WHERE user_id=?", u.ID).Scan(&u.Identity, &u.MerchantID, &u.Enabled, &u.Super)
	if e != nil {
		return e
	}
	if !u.Enabled {
		return apiError{401, "账号已停用"}
	}
	if u.merchant() {
		var enabled bool
		if e = s.db.QueryRow("SELECT enabled FROM merchants WHERE id=?", u.MerchantID).Scan(&enabled); e != nil {
			return e
		}
		if !enabled {
			return apiError{401, "商户已停用"}
		}
	}
	u.Permissions = []string{}
	u.RoleIDs = []string{}
	rows, e := s.db.Query("SELECT r.id FROM user_roles ur JOIN access_roles r ON r.id=ur.role_id WHERE ur.user_id=? AND r.enabled=1 AND r.identity=? ORDER BY r.id", u.ID, u.Identity)
	if e != nil {
		return e
	}
	for rows.Next() {
		var v string
		if e = rows.Scan(&v); e != nil {
			rows.Close()
			return e
		}
		u.RoleIDs = append(u.RoleIDs, v)
	}
	e = rows.Err()
	rows.Close()
	if e != nil {
		return e
	}
	rows, e = s.db.Query("SELECT DISTINCT rp.permission FROM user_roles ur JOIN access_roles r ON r.id=ur.role_id JOIN role_permissions rp ON rp.role_id=r.id WHERE ur.user_id=? AND r.enabled=1 AND r.identity=? ORDER BY rp.permission", u.ID, u.Identity)
	if e != nil {
		return e
	}
	allowed := map[string]bool{}
	for _, p := range permissionCatalog() {
		if !u.merchant() || p.Merchant {
			allowed[p.Code] = true
		}
	}
	for rows.Next() {
		var p string
		if e = rows.Scan(&p); e != nil {
			rows.Close()
			return e
		}
		if allowed[p] {
			u.Permissions = append(u.Permissions, p)
		}
	}
	e = rows.Err()
	rows.Close()
	if e != nil {
		return e
	}
	return s.loadUserScopes(u)
}
func forbid() error { return apiError{403, "没有执行此操作的权限"} }
func (s *Store) authorize(r *http.Request, u User) error {
	path := r.URL.Path
	read := r.Method == "GET" || r.Method == "HEAD"
	need := []string{}
	queryPage := ""
	switch {
	case strings.HasPrefix(path, "/api/access/"), strings.HasPrefix(path, "/api/users"):
		if !u.Super {
			return forbid()
		}
		return nil
	case path == "/api/auth/password":
		return nil
	case path == "/api/dashboard":
		need = []string{"home.view"}
	case path == "/api/payments":
		if read {
			queryPage = "orders"
			if r.URL.Query().Get("archive") == "true" {
				need = []string{"history.view"}
				queryPage = "history"
			} else {
				need = []string{"orders.view"}
			}
			if r.URL.Query().Get("all") == "true" {
				if !u.can("orders.view") || !u.can("history.view") {
					return forbid()
				}
				if r.URL.Query().Get("export") == "csv" && (!u.can("orders.export") || !u.can("history.export")) {
					return forbid()
				}
			}
		} else {
			need = []string{"orders.create"}
		}
	case strings.HasPrefix(path, "/api/payments/"):
		if !u.any("orders.view", "history.view") {
			return forbid()
		}
		var mid, created string
		e := s.db.QueryRow("SELECT merchant_id,created_at FROM orders WHERE id=?", r.PathValue("id")).Scan(&mid, &created)
		if e == sql.ErrNoRows || e == nil && u.merchant() && mid != u.MerchantID {
			return notfound("订单不存在")
		}
		if e != nil {
			return e
		}
		prefix := "orders"
		if created < time.Now().Format("2006-01-02") {
			prefix = "history"
		}
		if !u.can(prefix + ".view") {
			return forbid()
		}
		if e := s.requireData(u, prefix, "orders", "orders", "id", r.PathValue("id"), "merchant_id"); e != nil {
			return e
		}
		switch {
		case strings.HasSuffix(path, "/qrcode"), strings.HasSuffix(path, "/checkout"):
			need = []string{prefix + ".qr"}
		case strings.HasSuffix(path, "/query"):
			need = []string{prefix + ".query"}
		case strings.HasSuffix(path, "/simulate"):
			need = []string{"orders.simulate"}
		default:
			need = []string{prefix + ".view"}
		}

	case path == "/api/merchants" && read:
		need = []string{"merchants.view", "orders.create"}
		queryPage = "merchants"
	case strings.HasPrefix(path, "/api/merchants"):
		switch {
		case strings.Contains(path, "/api-key"):
			need = []string{"merchants.apikey"}
		case strings.HasSuffix(path, "/prepay"):
			need = []string{"merchants.adjust"}
		case strings.HasSuffix(path, "/toggle"):
			need = []string{"merchants.toggle"}
		case r.Method == "POST":
			need = []string{"merchants.create"}
		default:
			need = []string{"merchants.edit"}
		}
	case path == "/api/products" && read:
		need = []string{"products.view", "orders.create"}
		queryPage = "products"
	case strings.HasPrefix(path, "/api/products"):
		if r.Method == "POST" {
			need = []string{"products.create"}
		} else {
			need = []string{"products.edit"}
		}
	case path == "/api/channels" && read:
		need = []string{"channels.view", "orders.create"}
		queryPage = "channels"
	case strings.HasPrefix(path, "/api/channels"):
		if strings.HasSuffix(path, "/toggle") {
			need = []string{"channels.toggle"}
		} else if r.Method == "POST" {
			need = []string{"channels.create"}
		} else {
			need = []string{"channels.edit"}
		}
	case strings.HasPrefix(path, "/api/finance/"):
		page, e := financePermissionPage(r)
		if e != nil {
			return e
		}
		need = []string{page + ".view"}
		queryPage = page

	case path == "/api/settings":
		suffix := ".view"
		if !read {
			suffix = ".edit"
		}
		need = []string{"site_settings" + suffix, "pay_settings" + suffix, "quick_login" + suffix}
	case path == "/api/audit":
		need = []string{"logs.view"}
	case path == "/api/integrations/wechat":
		need = []string{"plugins.view", "channels.view"}
	case strings.HasPrefix(path, "/api/records/"):
		kind := r.PathValue("kind")
		if _, ok := recordKinds[kind]; !ok {
			return notfound("记录不存在")
		}
		suffix := ".view"
		if !read {
			if r.Method == "POST" {
				suffix = ".create"
			} else {
				suffix = ".edit"
			}
		}
		need = []string{kind + suffix}
		if read {
			queryPage = kind
		}
	default:
		return forbid()
	}
	// Every grant is intersected with the identity applicability in the server-owned catalog.
	allowed := false
	for _, p := range permissionCatalog() {
		for _, n := range need {
			if p.Code == n && (!u.merchant() || p.Merchant) && u.can(n) {
				allowed = true
			}
		}
	}
	if !allowed {
		return forbid()
	}
	if r.URL.Query().Get("export") == "csv" {
		exports := []string{}
		for _, n := range need {
			exports = append(exports, strings.TrimSuffix(n, ".view")+".export")
		}
		if !u.any(exports...) {
			return forbid()
		}
	}
	return authorizeQuery(r, u, queryPage)
}
func redactOrder(u User, o PaymentOrder) PaymentOrder {
	prefix := "orders"
	if o.CreatedAt < time.Now().Format("2006-01-02") {
		prefix = "history"
	}
	if !u.can(prefix + ".qr") {
		o.QRImage = ""
		o.QRImageURL = ""
		o.PayURL = ""
	}
	return o
}

type AccessRole struct {
	ID          string   `json:"id"`
	Name        string   `json:"name"`
	Identity    string   `json:"identity"`
	Enabled     bool     `json:"enabled"`
	Permissions []string `json:"permissions"`
	DataScope   string   `json:"dataScope"`
	MerchantIDs []string `json:"merchantIds"`
}

func (s *Store) accessCatalog(w http.ResponseWriter, r *http.Request, u User) error {
	writeJSON(w, 200, permissionCatalog())
	return nil
}
func (s *Store) listRoles(w http.ResponseWriter, r *http.Request, u User) error {
	out := []AccessRole{}
	rows, e := s.db.Query("SELECT id,name,identity,enabled FROM access_roles ORDER BY identity,name")
	if e != nil {
		return e
	}
	for rows.Next() {
		var x AccessRole
		x.Permissions = []string{}
		if e = rows.Scan(&x.ID, &x.Name, &x.Identity, &x.Enabled); e != nil {
			rows.Close()
			return e
		}
		out = append(out, x)
	}
	e = rows.Err()
	rows.Close()
	if e != nil {
		return e
	}
	for i := range out {
		if e := s.loadRoleScope(&out[i]); e != nil {
			return e
		}
		rows, e := s.db.Query("SELECT permission FROM role_permissions WHERE role_id=? ORDER BY permission", out[i].ID)
		if e != nil {
			return e
		}
		for rows.Next() {
			var p string
			if e = rows.Scan(&p); e != nil {
				rows.Close()
				return e
			}
			out[i].Permissions = append(out[i].Permissions, p)
		}
		e = rows.Err()
		rows.Close()
		if e != nil {
			return e
		}
	}
	writeJSON(w, 200, out)
	return nil
}
func (s *Store) saveRole(w http.ResponseWriter, r *http.Request, u User) error {
	var b AccessRole
	if e := decode(w, r, &b); e != nil {
		return e
	}
	b.Name = strings.TrimSpace(b.Name)
	if b.Name == "" || len(b.Name) > 100 || (b.Identity != "internal" && b.Identity != "merchant") {
		return bad("请填写角色名称及有效身份")
	}
	known := map[string]Permission{}
	for _, p := range permissionCatalog() {
		known[p.Code] = p
	}
	grants := map[string]bool{}
	for _, code := range b.Permissions {
		p, ok := known[code]
		if !ok || b.Identity == "merchant" && !p.Merchant {
			return bad("角色含无效或不适用的权限")
		}
		grants[code] = true
	}
	for code := range grants {
		for _, dep := range known[code].Requires {
			if !grants[dep] {
				return bad("操作权限必须同时授予对应页面查看权限")
			}
		}
	}
	b.Permissions = []string{}
	for code := range grants {
		b.Permissions = append(b.Permissions, code)
	}
	sort.Strings(b.Permissions)
	creating := r.Method == "POST"
	b.ID = r.PathValue("id")
	if creating {
		b.ID = id("R-")
	}
	e := s.transaction(func(tx *sql.Tx) error {
		if e := s.validateRoleScope(tx, &b); e != nil {
			return e
		}
		var old any
		if !creating {
			var x AccessRole
			if e := tx.QueryRow("SELECT id,name,identity,enabled,COALESCE((SELECT mode FROM role_data_scope WHERE role_id=access_roles.id),'own_merchant') FROM access_roles WHERE id=?", b.ID).Scan(&x.ID, &x.Name, &x.Identity, &x.Enabled, &x.DataScope); e == sql.ErrNoRows {
				return notfound("角色不存在")
			} else if e != nil {
				return e
			}
			if x.Identity != b.Identity {
				return bad("角色身份不可修改，请新建角色")
			}
			x.Permissions = []string{}
			rows, e := tx.Query("SELECT permission FROM role_permissions WHERE role_id=?", b.ID)
			if e != nil {
				return e
			}
			for rows.Next() {
				var p string
				if e = rows.Scan(&p); e != nil {
					rows.Close()
					return e
				}
				x.Permissions = append(x.Permissions, p)
			}
			e = rows.Err()
			rows.Close()
			if e != nil {
				return e
			}
			x.MerchantIDs = []string{}
			mids, e := tx.Query("SELECT merchant_id FROM role_scope_merchants WHERE role_id=? ORDER BY merchant_id", b.ID)
			if e != nil {
				return e
			}
			for mids.Next() {
				var mid string
				if e = mids.Scan(&mid); e != nil {
					mids.Close()
					return e
				}
				x.MerchantIDs = append(x.MerchantIDs, mid)
			}
			e = mids.Err()
			mids.Close()
			if e != nil {
				return e
			}
			old = x
		}
		var duplicate int
		if e := tx.QueryRow("SELECT COUNT(*) FROM access_roles WHERE name=? AND id<>?", b.Name, b.ID).Scan(&duplicate); e != nil {
			return e
		}
		if duplicate > 0 {
			return conflict("角色名称已存在")
		}
		if creating {
			if _, e := tx.Exec("INSERT INTO access_roles VALUES(?,?,?,?)", b.ID, b.Name, b.Identity, b.Enabled); e != nil {
				return e
			}
		} else {
			if _, e := tx.Exec("UPDATE access_roles SET name=?,enabled=? WHERE id=?", b.Name, b.Enabled, b.ID); e != nil {
				return e
			}
		}
		if _, e := tx.Exec("DELETE FROM role_permissions WHERE role_id=?", b.ID); e != nil {
			return e
		}
		for _, code := range b.Permissions {
			if _, e := tx.Exec("INSERT INTO role_permissions VALUES(?,?)", b.ID, code); e != nil {
				return e
			}
		}
		if _, e := tx.Exec("INSERT INTO role_data_scope VALUES(?,?) ON CONFLICT(role_id) DO UPDATE SET mode=excluded.mode", b.ID, b.DataScope); e != nil {
			return e
		}
		if _, e := tx.Exec("DELETE FROM role_scope_merchants WHERE role_id=?", b.ID); e != nil {
			return e
		}
		for _, mid := range b.MerchantIDs {
			if _, e := tx.Exec("INSERT INTO role_scope_merchants VALUES(?,?)", b.ID, mid); e != nil {
				return e
			}
		}
		detail, _ := json.Marshal(map[string]any{"before": old, "after": b})
		return s.audit(tx, u, r, "保存角色权限", b.ID, string(detail))
	})
	if e != nil {
		return e
	}
	writeJSON(w, 200, b)
	return nil
}

type UserInput struct {
	Name       string   `json:"name"`
	Password   string   `json:"password"`
	Identity   string   `json:"identity"`
	MerchantID string   `json:"merchantId"`
	RoleIDs    []string `json:"roleIds"`
	Enabled    bool     `json:"enabled"`
}

func validateUserBinding(tx *sql.Tx, b UserInput) error {
	if b.Identity != "internal" && b.Identity != "merchant" {
		return bad("请选择内部或商户身份")
	}
	if b.Identity == "internal" && b.MerchantID != "" {
		return bad("内部账号不能绑定商户")
	}
	if b.Identity == "merchant" {
		var n int
		if e := tx.QueryRow("SELECT COUNT(*) FROM merchants WHERE id=?", b.MerchantID).Scan(&n); e != nil {
			return e
		}
		if n != 1 {
			return bad("请选择有效商户")
		}
	}
	seen := map[string]bool{}
	for _, rid := range b.RoleIDs {
		if seen[rid] {
			return bad("不能重复分配角色")
		}
		seen[rid] = true
		var identity string
		var enabled bool
		if e := tx.QueryRow("SELECT identity,enabled FROM access_roles WHERE id=?", rid).Scan(&identity, &enabled); e == sql.ErrNoRows {
			return bad("角色不存在")
		} else if e != nil {
			return e
		}
		if identity != b.Identity || !enabled {
			return bad("只能分配同身份且启用的角色")
		}
	}
	return nil
}
func replaceUserRoles(tx *sql.Tx, uid string, roles []string) error {
	if _, e := tx.Exec("DELETE FROM user_roles WHERE user_id=?", uid); e != nil {
		return e
	}
	for _, rid := range roles {
		if _, e := tx.Exec("INSERT INTO user_roles VALUES(?,?)", uid, rid); e != nil {
			return e
		}
	}
	return nil
}
func (s *Store) listAccessUsers(w http.ResponseWriter, r *http.Request, u User) error {
	rows, e := s.db.Query("SELECT u.id,u.name,u.role,a.identity,COALESCE(a.merchant_id,''),a.enabled,a.is_super FROM users u JOIN user_access a ON a.user_id=u.id ORDER BY u.name")
	if e != nil {
		return e
	}
	out := []User{}
	for rows.Next() {
		var x User
		x.Permissions = []string{}
		x.RoleIDs = []string{}
		if e = rows.Scan(&x.ID, &x.Name, &x.Role, &x.Identity, &x.MerchantID, &x.Enabled, &x.Super); e != nil {
			rows.Close()
			return e
		}
		out = append(out, x)
	}
	e = rows.Err()
	rows.Close()
	if e != nil {
		return e
	}
	for i := range out {
		rows, e := s.db.Query("SELECT role_id FROM user_roles WHERE user_id=? ORDER BY role_id", out[i].ID)
		if e != nil {
			return e
		}
		for rows.Next() {
			var v string
			if e = rows.Scan(&v); e != nil {
				rows.Close()
				return e
			}
			out[i].RoleIDs = append(out[i].RoleIDs, v)
		}
		e = rows.Err()
		rows.Close()
		if e != nil {
			return e
		}
	}
	writeJSON(w, 200, out)
	return nil
}
func (s *Store) saveAccessUser(w http.ResponseWriter, r *http.Request, u User) error {
	var b UserInput
	if e := decode(w, r, &b); e != nil {
		return e
	}
	b.Name = strings.TrimSpace(b.Name)
	create := r.Method == "POST"
	uid := r.PathValue("id")
	if create {
		uid = id("U-")
	}
	if len(b.Name) < 2 || len(b.Name) > 40 {
		return bad("账号须为 2–40 个字符")
	}
	if create && (len(b.Password) < 10 || len(b.Password) > 72) {
		return bad("密码须为 10–72 个字符")
	}
	if !create && b.Password != "" {
		return bad("请使用重置密码操作")
	}
	hash := ""
	if create {
		v, e := bcrypt.GenerateFromPassword([]byte(b.Password), bcrypt.DefaultCost)
		if e != nil {
			return e
		}
		hash = string(v)
	}
	e := s.transaction(func(tx *sql.Tx) error {
		if e := validateUserBinding(tx, b); e != nil {
			return e
		}
		var old any
		if !create {
			var x User
			if e := tx.QueryRow("SELECT u.name,a.identity,COALESCE(a.merchant_id,''),a.enabled,a.is_super FROM users u JOIN user_access a ON a.user_id=u.id WHERE u.id=?", uid).Scan(&x.Name, &x.Identity, &x.MerchantID, &x.Enabled, &x.Super); e == sql.ErrNoRows {
				return notfound("账号不存在")
			} else if e != nil {
				return e
			}
			if x.Identity != b.Identity || x.MerchantID != b.MerchantID {
				return bad("账号身份和商户归属不可修改，请另建账号")
			}
			if x.Super && !b.Enabled {
				var n int
				if e := tx.QueryRow("SELECT COUNT(*) FROM user_access WHERE is_super=1 AND enabled=1").Scan(&n); e != nil {
					return e
				}
				if n <= 1 {
					return conflict("至少保留一个启用的超级管理员")
				}
			}
			x.RoleIDs = []string{}
			rows, e := tx.Query("SELECT role_id FROM user_roles WHERE user_id=?", uid)
			if e != nil {
				return e
			}
			for rows.Next() {
				var rid string
				if e = rows.Scan(&rid); e != nil {
					rows.Close()
					return e
				}
				x.RoleIDs = append(x.RoleIDs, rid)
			}
			e = rows.Err()
			rows.Close()
			if e != nil {
				return e
			}
			old = x
		}
		var dup int
		if e := tx.QueryRow("SELECT COUNT(*) FROM users WHERE name=? AND id<>?", b.Name, uid).Scan(&dup); e != nil {
			return e
		}
		if dup > 0 {
			return conflict("账号名称已存在")
		}
		if create {
			if _, e := tx.Exec("INSERT INTO users(id,name,password_hash,role) VALUES(?,?,?,'viewer')", uid, b.Name, hash); e != nil {
				return e
			}
			var mid any
			if b.MerchantID != "" {
				mid = b.MerchantID
			}
			if _, e := tx.Exec("INSERT INTO user_access VALUES(?,?,?,?,0)", uid, b.Identity, mid, b.Enabled); e != nil {
				return e
			}
		} else {
			if _, e := tx.Exec("UPDATE users SET name=? WHERE id=?", b.Name, uid); e != nil {
				return e
			}
			if _, e := tx.Exec("UPDATE user_access SET enabled=? WHERE user_id=?", b.Enabled, uid); e != nil {
				return e
			}
		}
		if e := replaceUserRoles(tx, uid, b.RoleIDs); e != nil {
			return e
		}
		if !b.Enabled {
			if _, e := tx.Exec("DELETE FROM sessions WHERE user_id=?", uid); e != nil {
				return e
			}
		}
		detail, _ := json.Marshal(map[string]any{"before": old, "after": map[string]any{"name": b.Name, "identity": b.Identity, "merchantId": b.MerchantID, "enabled": b.Enabled, "roleIds": b.RoleIDs}})
		return s.audit(tx, u, r, "保存用户授权", uid, string(detail))
	})
	if e != nil {
		return e
	}
	writeJSON(w, 200, map[string]string{"id": uid})
	return nil
}
func (s *Store) resetUserPassword(w http.ResponseWriter, r *http.Request, u User) error {
	var b struct {
		Password string `json:"password"`
	}
	if e := decode(w, r, &b); e != nil {
		return e
	}
	return s.setPassword(w, r, u, r.PathValue("id"), b.Password)
}
func (s *Store) changeOwnPassword(w http.ResponseWriter, r *http.Request, u User) error {
	var b struct {
		Current  string `json:"currentPassword"`
		Password string `json:"password"`
	}
	if e := decode(w, r, &b); e != nil {
		return e
	}
	var hash string
	if e := s.db.QueryRow("SELECT password_hash FROM users WHERE id=?", u.ID).Scan(&hash); e != nil {
		return e
	}
	if bcrypt.CompareHashAndPassword([]byte(hash), []byte(b.Current)) != nil {
		return bad("原密码不正确")
	}
	return s.setPassword(w, r, u, u.ID, b.Password)
}
func (s *Store) setPassword(w http.ResponseWriter, r *http.Request, u User, uid, password string) error {
	if len(password) < 10 || len(password) > 72 {
		return bad("密码须为 10–72 个字符")
	}
	hash, e := bcrypt.GenerateFromPassword([]byte(password), bcrypt.DefaultCost)
	if e != nil {
		return e
	}
	e = s.transaction(func(tx *sql.Tx) error {
		result, e := tx.Exec("UPDATE users SET password_hash=? WHERE id=?", string(hash), uid)
		if e != nil {
			return e
		}
		n, _ := result.RowsAffected()
		if n == 0 {
			return notfound("账号不存在")
		}
		if _, e = tx.Exec("DELETE FROM sessions WHERE user_id=?", uid); e != nil {
			return e
		}
		return s.audit(tx, u, r, "修改或重置密码", uid, "该账号既有会话已失效")
	})
	if e != nil {
		return e
	}
	writeJSON(w, 200, map[string]bool{"ok": true})
	return nil
}
