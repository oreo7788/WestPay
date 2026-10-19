package main

import (
	"database/sql"
	"fmt"
	"net/http"
	"sort"
	"strings"
)

const dataScopeSchema = `
CREATE TABLE IF NOT EXISTS role_data_scope(role_id TEXT PRIMARY KEY REFERENCES access_roles(id),mode TEXT NOT NULL CHECK(mode IN ('all','own_merchant','selected_merchants')));
CREATE TABLE IF NOT EXISTS role_scope_merchants(role_id TEXT NOT NULL REFERENCES access_roles(id),merchant_id TEXT NOT NULL REFERENCES merchants(id),PRIMARY KEY(role_id,merchant_id));
CREATE TABLE IF NOT EXISTS data_merchant_links(resource TEXT NOT NULL,record_id TEXT NOT NULL,merchant_id TEXT NOT NULL REFERENCES merchants(id),PRIMARY KEY(resource,record_id));
CREATE INDEX IF NOT EXISTS data_merchant_idx ON data_merchant_links(resource,merchant_id,record_id);
`

type DataScope struct {
	Mode        string   `json:"mode"`
	MerchantIDs []string `json:"merchantIds"`
}

func (s *Store) loadRoleScope(role *AccessRole) error {
	// Unconfigured roles never inherit platform-wide data access.
	role.DataScope = "own_merchant"
	role.MerchantIDs = []string{}
	e := s.db.QueryRow("SELECT mode FROM role_data_scope WHERE role_id=?", role.ID).Scan(&role.DataScope)
	if e != nil && e != sql.ErrNoRows {
		return e
	}
	rows, e := s.db.Query("SELECT merchant_id FROM role_scope_merchants WHERE role_id=? ORDER BY merchant_id", role.ID)
	if e != nil {
		return e
	}
	defer rows.Close()
	for rows.Next() {
		var mid string
		if e = rows.Scan(&mid); e != nil {
			return e
		}
		role.MerchantIDs = append(role.MerchantIDs, mid)
	}
	return rows.Err()
}
func (s *Store) loadUserScopes(u *User) error {
	u.DataScopes = map[string][]DataScope{}
	for _, rid := range u.RoleIDs {
		role := AccessRole{ID: rid, Identity: u.Identity}
		if e := s.loadRoleScope(&role); e != nil {
			return e
		}
		rows, e := s.db.Query("SELECT permission FROM role_permissions WHERE role_id=?", rid)
		if e != nil {
			return e
		}
		for rows.Next() {
			var p string
			if e = rows.Scan(&p); e != nil {
				rows.Close()
				return e
			}
			if strings.HasSuffix(p, ".view") && u.can(p) {
				page := strings.TrimSuffix(p, ".view")
				u.DataScopes[page] = append(u.DataScopes[page], DataScope{role.DataScope, role.MerchantIDs})
			}
			if p == "orders.create" && u.can(p) {
				u.DataScopes["order_lookup"] = append(u.DataScopes["order_lookup"], DataScope{role.DataScope, role.MerchantIDs})
			}
		}
		e = rows.Err()
		rows.Close()
		if e != nil {
			return e
		}
	}
	return nil
}

// Scope is based solely on business merchant ownership, never creator identity.
// Union scopes only for roles granting the requested page, then enforce tenant.
func (u User) dataWhere(page, resource, idExpr, merchantExpr string) (string, []any) {
	if u.Super {
		return "1=1", nil
	}
	if merchantExpr == "" {
		merchantExpr = "COALESCE((SELECT ds.merchant_id FROM data_merchant_links ds WHERE ds.resource='" + resource + "' AND ds.record_id=" + idExpr + "),'')"
	}
	rules := u.DataScopes[page]
	if u.DataScopes == nil {
		rules = []DataScope{{Mode: "own_merchant"}}
	}
	parts := []string{}
	args := []any{}
	for _, rule := range rules {
		switch rule.Mode {
		case "all":
			parts = append(parts, "1=1")
		case "own_merchant":
			if u.MerchantID != "" {
				parts = append(parts, merchantExpr+"=?")
				args = append(args, u.MerchantID)
			}
		case "selected_merchants":
			if len(rule.MerchantIDs) == 0 {
				continue
			}
			parts = append(parts, merchantExpr+" IN ("+strings.TrimRight(strings.Repeat("?,", len(rule.MerchantIDs)), ",")+")")
			for _, mid := range rule.MerchantIDs {
				args = append(args, mid)
			}
		}
	}
	if len(parts) == 0 {
		return "0=1", nil
	}
	where := "(" + strings.Join(parts, " OR ") + ")"
	if u.merchant() {
		where += " AND " + merchantExpr + "=?"
		args = append(args, u.MerchantID)
	}
	return "(" + where + ")", args
}
func (s *Store) requireData(u User, page, resource, table, key, rid, merchantExpr string) error {
	where, args := u.dataWhere(page, resource, key, merchantExpr)
	var n int
	e := s.db.QueryRow("SELECT COUNT(*) FROM "+table+" WHERE "+key+"=? AND "+where, append([]any{rid}, args...)...).Scan(&n)
	if e != nil {
		return e
	}
	if n == 0 {
		return notfound("记录不存在或不在可查看范围内")
	}
	return nil
}
func (s *Store) validateRoleScope(tx *sql.Tx, b *AccessRole) error {
	if b.DataScope != "all" && b.DataScope != "own_merchant" && b.DataScope != "selected_merchants" {
		return bad("请选择数据查看范围")
	}
	if b.Identity == "merchant" && (b.DataScope != "own_merchant" || len(b.MerchantIDs) > 0) {
		return bad("商户角色只能查看当前账号所属商户的数据")
	}
	if b.Identity == "internal" && b.DataScope == "own_merchant" {
		return bad("内部角色请选择全部数据或指定商户")
	}
	if b.DataScope != "selected_merchants" && len(b.MerchantIDs) > 0 {
		return bad("仅指定商户范围可以设置商户列表")
	}
	if b.DataScope == "selected_merchants" && len(b.MerchantIDs) == 0 {
		return bad("请至少选择一个可查看商户")
	}
	seen := map[string]bool{}
	for _, mid := range b.MerchantIDs {
		if seen[mid] {
			return bad("可查看商户不能重复")
		}
		seen[mid] = true
		var n int
		if e := tx.QueryRow("SELECT COUNT(*) FROM merchants WHERE id=?", mid).Scan(&n); e != nil {
			return e
		}
		if n != 1 {
			return bad("可查看商户不存在")
		}
	}
	sort.Strings(b.MerchantIDs)
	if b.MerchantIDs == nil {
		b.MerchantIDs = []string{}
	}
	return nil
}
func (s *Store) recordMerchant(tx *sql.Tx, b *Record, create bool) error {
	if !create {
		var mid string
		if e := tx.QueryRow("SELECT COALESCE((SELECT merchant_id FROM data_merchant_links WHERE resource=? AND record_id=?),'')", b.Kind, b.ID).Scan(&mid); e != nil {
			return e
		}
		if b.MerchantID != mid {
			return bad("归属商户不可修改，请新建记录")
		}
	}
	if b.MerchantID != "" {
		var n int
		if e := tx.QueryRow("SELECT COUNT(*) FROM merchants WHERE id=?", b.MerchantID).Scan(&n); e != nil {
			return e
		}
		if n != 1 {
			return bad("归属商户不存在")
		}
	}
	return nil
}
func linkMerchant(tx *sql.Tx, resource, rid, mid string) error {
	if mid == "" {
		return nil
	}
	_, e := tx.Exec("INSERT INTO data_merchant_links VALUES(?,?,?)", resource, rid, mid)
	return e
}
func auditMerchant(tx *sql.Tx, aid int64, u User, target string) error {
	mid := u.MerchantID
	if mid == "" {
		e := tx.QueryRow("SELECT merchant_id FROM orders WHERE id=? UNION ALL SELECT id FROM merchants WHERE id=? UNION ALL SELECT merchant_id FROM data_merchant_links WHERE record_id=? LIMIT 1", target, target, target).Scan(&mid)
		if e != nil && e != sql.ErrNoRows {
			return e
		}
	}
	return linkMerchant(tx, "audit", fmt.Sprint(aid), mid)
}
func (s *Store) scopeObject(r *http.Request, u User) error {
	path := r.URL.Path
	rid := r.PathValue("id")
	if rid == "" {
		return nil
	}
	switch {
	case strings.HasPrefix(path, "/api/merchants/"):
		return s.requireData(u, "merchants", "merchants", "merchants", "id", rid, "id")
	case strings.HasPrefix(path, "/api/products/"):
		return s.requireData(u, "products", "products", "products", "code", rid, "")
	case strings.HasPrefix(path, "/api/channels/"):
		return s.requireData(u, "channels", "channels", "channels", "id", rid, "")
	case strings.HasPrefix(path, "/api/records/"):
		return s.requireData(u, r.PathValue("kind"), r.PathValue("kind"), "records", "id", rid, "")
	}
	return nil
}
func (u User) orderWhere(page, alias string) (string, []any) {
	return u.dataWhere(page, "orders", alias+"id", alias+"merchant_id")
}
func (u User) creationAllowed(page, mid string) bool {
	if u.Super {
		return true
	}
	if u.merchant() && mid != u.MerchantID {
		return false
	}
	for _, rule := range u.DataScopes[page] {
		if rule.Mode == "all" {
			return true
		}
		if rule.Mode == "own_merchant" && mid != "" && mid == u.MerchantID {
			return true
		}
		if rule.Mode == "selected_merchants" {
			for _, m := range rule.MerchantIDs {
				if m == mid {
					return true
				}
			}
		}
	}
	return false
}

func auditEvent(tx *sql.Tx, u User, action, target, detail, ip string) error {
	result, e := tx.Exec("INSERT INTO audit(actor,action,target,detail,ip,created_at) VALUES(?,?,?,?,?,?)", u.Name, action, target, detail, ip, timestamp())
	if e != nil {
		return e
	}
	aid, e := result.LastInsertId()
	if e != nil {
		return e
	}
	return auditMerchant(tx, aid, u, target)
}
