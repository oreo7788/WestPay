package main

import (
	"database/sql"
	"encoding/json"
	"net/http"
	"strconv"
	"strings"
)

type Record struct {
	ID         string  `json:"id"`
	Kind       string  `json:"kind"`
	MerchantID string  `json:"merchantId"`
	Name       string  `json:"name"`
	Reference  string  `json:"reference"`
	Amount     float64 `json:"amount"`
	Status     string  `json:"status"`
	Note       string  `json:"note"`
	CreatedAt  string  `json:"createdAt"`
	UpdatedAt  string  `json:"updatedAt"`
}

var recordKinds = map[string][]string{"complaints": {"待处理", "处理中", "已解决", "已撤销"}, "transfers": {"待付款", "已付款", "失败", "已取消"}, "receivers": {"启用", "停用"}, "official": {"启用", "停用"}, "blacklist": {"生效", "停用"}, "user_ids": {"有效", "停用"}}

func (s *Store) listRecords(w http.ResponseWriter, r *http.Request, u User) error {
	kind := r.PathValue("kind")
	if _, ok := recordKinds[kind]; !ok {
		return notfound("记录类型不存在")
	}
	scope, args := u.dataWhere(kind, kind, "id", "")
	rows, e := s.db.Query("SELECT id,kind,name,reference,amount,status,note,created_at,updated_at,COALESCE((SELECT merchant_id FROM data_merchant_links ds WHERE ds.resource=records.kind AND ds.record_id=records.id),'') FROM records WHERE kind=? AND "+scope+" ORDER BY created_at DESC", append([]any{kind}, args...)...)
	if e != nil {
		return e
	}
	defer rows.Close()
	out := []Record{}
	for rows.Next() {
		var b Record
		var n int64
		if e = rows.Scan(&b.ID, &b.Kind, &b.Name, &b.Reference, &n, &b.Status, &b.Note, &b.CreatedAt, &b.UpdatedAt, &b.MerchantID); e != nil {
			return e
		}
		b.Amount = float64(n) / 100
		out = append(out, b)
	}
	if e = rows.Err(); e != nil {
		return e
	}
	writeJSON(w, 200, filterKeyword(r, out))
	return nil
}
func (s *Store) saveRecord(w http.ResponseWriter, r *http.Request, u User) error {
	kind := r.PathValue("kind")
	statuses, ok := recordKinds[kind]
	if !ok {
		return notfound("记录类型不存在")
	}
	var b Record
	if e := decode(w, r, &b); e != nil {
		return e
	}
	n, e := cents(b.Amount)
	if e != nil {
		return e
	}
	if n < 0 || strings.TrimSpace(b.Name) == "" || len(b.Name) > 200 || len(b.Note) > 3000 {
		return bad("名称、金额或备注无效")
	}
	ok = false
	for _, status := range statuses {
		if b.Status == status {
			ok = true
		}
	}
	if !ok {
		return bad("该模块不支持此状态")
	}
	b.ID = r.PathValue("id")
	if r.Method == "POST" {
		b.ID = id("R-")
	}
	b.Kind = kind
	e = s.transaction(func(tx *sql.Tx) error {
		if e := s.recordMerchant(tx, &b, r.Method == "POST"); e != nil {
			return e
		}
		if r.Method == "POST" && !u.creationAllowed(kind, b.MerchantID) {
			return forbid()
		}
		var result sql.Result
		var err error
		if r.Method == "POST" {
			b.CreatedAt = timestamp()
			b.UpdatedAt = b.CreatedAt
			result, err = tx.Exec("INSERT INTO records VALUES(?,?,?,?,?,?,?,?,?)", b.ID, kind, b.Name, b.Reference, n, b.Status, b.Note, b.CreatedAt, b.UpdatedAt)
		} else {
			b.UpdatedAt = timestamp()
			result, err = tx.Exec("UPDATE records SET name=?,reference=?,amount=?,status=?,note=?,updated_at=? WHERE id=? AND kind=?", b.Name, b.Reference, n, b.Status, b.Note, b.UpdatedAt, b.ID, kind)
		}
		if err != nil {
			return err
		}
		count, _ := result.RowsAffected()
		if count == 0 {
			return notfound("记录不存在")
		}
		if r.Method == "POST" {
			if e := linkMerchant(tx, kind, b.ID, b.MerchantID); e != nil {
				return e
			}
		}
		return s.audit(tx, u, r, "保存"+kind, b.ID, b.Status)
	})
	if e != nil {
		return e
	}
	writeJSON(w, 200, b)
	return nil
}
func (s *Store) getSettings(w http.ResponseWriter, r *http.Request, u User) error {
	rows, e := s.db.Query("SELECT key,value FROM settings")
	if e != nil {
		return e
	}
	defer rows.Close()
	out := map[string]string{}
	for rows.Next() {
		var k, v string
		if e = rows.Scan(&k, &v); e != nil {
			return e
		}
		out[k] = v
	}
	if e = rows.Err(); e != nil {
		return e
	}
	writeJSON(w, 200, out)
	return nil
}
func (s *Store) saveSettings(w http.ResponseWriter, r *http.Request, u User) error {
	var values map[string]string
	if e := decode(w, r, &values); e != nil {
		return e
	}
	for k, v := range values {
		switch k {
		case "siteName":
			if strings.TrimSpace(v) == "" || len(v) > 100 {
				return bad("站点名称无效")
			}
		case "minAmount", "maxAmount":
			n, e := strconv.ParseFloat(v, 64)
			if e != nil || n <= 0 {
				return bad("金额限制无效")
			}
			if _, e = cents(n); e != nil {
				return e
			}
		case "orderTimeoutMinutes":
			n, e := strconv.Atoi(v)
			if e != nil || n < 1 || n > 1440 {
				return bad("订单超时须在 1–1440 分钟之间")
			}
		case "notificationEnabled":
			if v != "true" && v != "false" {
				return bad("提醒开关无效")
			}
		case "supportContact":
			if len(v) > 200 {
				return bad("联系方式过长")
			}
		default:
			return bad("不支持该配置项")
		}
	}
	e := s.transaction(func(tx *sql.Tx) error {
		var minS, maxS string
		if e := tx.QueryRow("SELECT value FROM settings WHERE key='minAmount'").Scan(&minS); e != nil {
			return e
		}
		if e := tx.QueryRow("SELECT value FROM settings WHERE key='maxAmount'").Scan(&maxS); e != nil {
			return e
		}
		if v, ok := values["minAmount"]; ok {
			minS = v
		}
		if v, ok := values["maxAmount"]; ok {
			maxS = v
		}
		min, _ := strconv.ParseFloat(minS, 64)
		max, _ := strconv.ParseFloat(maxS, 64)
		if min > max {
			return bad("最小金额不能大于最大金额")
		}
		for k, v := range values {
			if _, e := tx.Exec("UPDATE settings SET value=? WHERE key=?", v, k); e != nil {
				return e
			}
		}
		b, _ := json.Marshal(values)
		return s.audit(tx, u, r, "保存系统配置", "settings", string(b))
	})
	if e != nil {
		return e
	}
	writeJSON(w, 200, values)
	return nil
}
func (s *Store) listAudit(w http.ResponseWriter, r *http.Request, u User) error {
	scope, args := u.dataWhere("logs", "audit", "CAST(id AS TEXT)", "")
	rows, e := s.db.Query("SELECT id,actor,action,target,detail,ip,created_at FROM audit WHERE "+scope+" ORDER BY id DESC LIMIT 500", args...)
	if e != nil {
		return e
	}
	defer rows.Close()
	out := []map[string]any{}
	for rows.Next() {
		var i int
		var actor, action, target, detail, ip, date string
		if e = rows.Scan(&i, &actor, &action, &target, &detail, &ip, &date); e != nil {
			return e
		}
		out = append(out, map[string]any{"id": i, "actor": actor, "action": action, "target": target, "detail": detail, "ip": ip, "createdAt": date})
	}
	if e = rows.Err(); e != nil {
		return e
	}
	writeJSON(w, 200, out)
	return nil
}
