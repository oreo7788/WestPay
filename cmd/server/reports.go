package main

import (
	"encoding/csv"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"
)

func (s *Store) dashboard(w http.ResponseWriter, r *http.Request, u User) error {
	if err := s.expireOrders(); err != nil {
		return err
	}
	d := Dashboard{Trend: make([]float64, 12)}
	today := time.Now().Format("2006-01-02")
	yesterday := time.Now().AddDate(0, 0, -1).Format("2006-01-02")
	month := time.Now().Format("2006-01") + "-01"
	scope, scopeArgs := u.orderWhere("home", "o.")
	scope = " AND " + scope
	var amount, fee, monthAmount, yesterdayAmount int64
	if e := s.db.QueryRow(`SELECT COUNT(*),COALESCE(SUM(status='已支付'),0),COALESCE(SUM(CASE WHEN status='已支付' THEN amount END),0),COALESCE(SUM(CASE WHEN status='已支付' THEN fee-cost END),0) FROM orders o WHERE created_at>=?`+scope, append([]any{today}, scopeArgs...)...).Scan(&d.TodayOrders, &d.TodaySuccess, &amount, &fee); e != nil {
		return e
	}
	if e := s.db.QueryRow("SELECT COALESCE(SUM(amount),0) FROM orders o WHERE status='已支付' AND created_at>=?"+scope, append([]any{month}, scopeArgs...)...).Scan(&monthAmount); e != nil {
		return e
	}
	if e := s.db.QueryRow("SELECT COUNT(*),COALESCE(SUM(CASE WHEN status='已支付' THEN amount END),0) FROM orders o WHERE created_at>=? AND created_at<?"+scope, append([]any{yesterday, today}, scopeArgs...)...).Scan(&d.Yesterday, &yesterdayAmount); e != nil {
		return e
	}
	d.TodayAmount = float64(amount) / 100
	d.TodayProfit = float64(fee) / 100
	if u.merchant() {
		d.TodayProfit = 0
	}
	d.MonthAmount = float64(monthAmount) / 100
	d.YesterdayAmount = float64(yesterdayAmount) / 100
	if d.TodayOrders > 0 {
		d.SuccessRate = float64(d.TodaySuccess) * 100 / float64(d.TodayOrders)
	}
	rows, e := s.db.Query("SELECT CAST(substr(created_at,12,2) AS INTEGER)/2,SUM(amount) FROM orders o WHERE status='已支付' AND created_at>=?"+scope+" GROUP BY 1", append([]any{today}, scopeArgs...)...)
	if e != nil {
		return e
	}
	for rows.Next() {
		var index int
		var n int64
		if e = rows.Scan(&index, &n); e != nil {
			rows.Close()
			return e
		}
		if index >= 0 && index < 12 {
			d.Trend[index] = float64(n) / 100
		}
	}
	e = rows.Err()
	rows.Close()
	if e != nil {
		return e
	}
	// Homepage panels have independent grants. Never return ungranted panel data.
	out := map[string]any{}
	allowed := func(key string) bool {
		for _, p := range permissionCatalog() {
			if p.Code == "home."+key {
				return u.can("home.view") && u.can(p.Code) && (!u.merchant() || p.Merchant)
			}
		}
		return false
	}
	for _, field := range []struct {
		permission, key string
		value           any
	}{
		{"today_orders", "todayOrders", d.TodayOrders},
		{"today_success", "todaySuccess", d.TodaySuccess},
		{"today_amount", "todayAmount", d.TodayAmount},
		{"success_rate", "successRate", d.SuccessRate},
		{"profit", "todayProfit", d.TodayProfit},
		{"month_amount", "monthAmount", d.MonthAmount},
		{"yesterday_orders", "yesterdayOrders", d.Yesterday},
		{"yesterday_amount", "yesterdayAmount", d.YesterdayAmount},
		{"trend", "trend", d.Trend},
	} {
		if allowed(field.permission) {
			out[field.key] = field.value
		}
	}
	for _, count := range []struct{ permission, key, table string }{
		{"channel_count", "channelCount", "channels"},
		{"merchant_count", "merchantCount", "merchants"},
	} {
		if allowed(count.permission) {
			var n int
			merchantExpr := ""
			if count.table == "merchants" {
				merchantExpr = "id"
			}
			visible, args := u.dataWhere("home", count.table, "id", merchantExpr)
			if e := s.db.QueryRow("SELECT COUNT(*) FROM "+count.table+" WHERE "+visible, args...).Scan(&n); e != nil {
				return e
			}
			out[count.key] = n
		}
	}
	if allowed("channels") {
		items := []map[string]any{}
		channelScope, channelArgs := u.dataWhere("home", "channels", "c.id", "")
		orderScope, orderArgs := u.orderWhere("home", "o.")
		args := append([]any{today}, orderArgs...)
		args = append(args, channelArgs...)
		rows, e := s.db.Query(`SELECT c.id,c.name,c.method,c.enabled,COALESCE(SUM(CASE WHEN o.status='已支付' THEN o.amount ELSE 0 END),0) FROM channels c LEFT JOIN orders o ON o.channel_id=c.id AND o.created_at>=? AND `+orderScope+` WHERE `+channelScope+` GROUP BY c.id ORDER BY c.id`, args...)
		if e != nil {
			return e
		}
		for rows.Next() {
			var id, name, method string
			var enabled bool
			var amount int64
			if e = rows.Scan(&id, &name, &method, &enabled, &amount); e != nil {
				rows.Close()
				return e
			}
			items = append(items, map[string]any{"id": id, "name": name, "method": method, "enabled": enabled, "todayAmount": float64(amount) / 100})
		}
		e = rows.Err()
		rows.Close()
		if e != nil {
			return e
		}
		out["channels"] = items
	}
	if allowed("merchant_ranking") {
		items := []map[string]any{}
		merchantScope, merchantArgs := u.dataWhere("home", "merchants", "m.id", "m.id")
		orderScope, orderArgs := u.orderWhere("home", "o.")
		args := append([]any{today}, orderArgs...)
		args = append(args, merchantArgs...)
		rows, e := s.db.Query(`SELECT m.id,m.name,COUNT(o.id),COALESCE(SUM(o.status='已支付'),0),COALESCE(SUM(CASE WHEN o.status='已支付' THEN o.amount ELSE 0 END),0) FROM merchants m LEFT JOIN orders o ON o.merchant_id=m.id AND o.created_at>=? AND `+orderScope+` WHERE `+merchantScope+` GROUP BY m.id ORDER BY COUNT(o.id) DESC,m.id LIMIT 10`, args...)
		if e != nil {
			return e
		}
		for rows.Next() {
			var id, name string
			var count, success int
			var amount int64
			var rate float64
			if e = rows.Scan(&id, &name, &count, &success, &amount); e != nil {
				rows.Close()
				return e
			}
			if count > 0 {
				rate = float64(success) * 100 / float64(count)
			}
			items = append(items, map[string]any{"id": id, "name": name, "todayCount": count, "todayRate": rate, "todayAmount": float64(amount) / 100})
		}
		e = rows.Err()
		rows.Close()
		if e != nil {
			return e
		}
		out["merchantRanking"] = items
	}
	if allowed("latest_orders") {
		items := []PaymentOrder{}
		rows, e := s.db.Query(orderSelect+" WHERE o.created_at>=?"+scope+" ORDER BY o.created_at DESC,o.rowid DESC LIMIT 5", append([]any{today}, scopeArgs...)...)
		if e != nil {
			return e
		}
		for rows.Next() {
			item, e := scanOrder(rows)
			if e != nil {
				rows.Close()
				return e
			}
			items = append(items, redactOrder(u, item))
		}
		e = rows.Err()
		rows.Close()
		if e != nil {
			return e
		}
		out["latestOrders"] = items
	}
	writeJSON(w, 200, out)
	return nil
}
func (s *Store) finance(w http.ResponseWriter, r *http.Request, u User) error {
	kind := r.PathValue("kind")
	out := []FinanceRow{}
	where := " WHERE 1=1"
	args := []any{}
	alias := "o"
	if kind == "prepay" {
		alias = "l"
	}
	page, e := financePermissionPage(r)
	if e != nil {
		return e
	}
	resource := "orders"
	if kind == "prepay" {
		resource = "ledger"
	}
	scope, scopeArgs := u.dataWhere(page, resource, alias+".id", alias+".merchant_id")
	where += " AND " + scope
	args = append(args, scopeArgs...)
	if from := r.URL.Query().Get("from"); from != "" {
		if _, e := time.Parse("2006-01-02", from); e != nil {
			return bad("开始日期无效")
		}
		where += " AND " + alias + ".created_at>=?"
		args = append(args, from)
	}
	if to := r.URL.Query().Get("to"); to != "" {
		t, e := time.Parse("2006-01-02", to)
		if e != nil {
			return bad("结束日期无效")
		}
		where += " AND " + alias + ".created_at<?"
		args = append(args, t.AddDate(0, 0, 1).Format("2006-01-02"))
	}
	if q := strings.TrimSpace(r.URL.Query().Get("q")); q != "" {
		where += " AND m.name LIKE ?"
		args = append(args, "%"+q+"%")
	}
	if kind == "prepay" {
		rows, e := s.db.Query("SELECT m.name,l.delta,l.before_balance,l.after_balance,l.created_at,l.kind,l.reference FROM ledger l JOIN merchants m ON m.id=l.merchant_id"+where+" ORDER BY l.created_at DESC,l.rowid DESC LIMIT 1000", args...)
		if e != nil {
			return e
		}
		defer rows.Close()
		for rows.Next() {
			var f FinanceRow
			var delta, before, after int64
			if e = rows.Scan(&f.Name, &delta, &before, &after, &f.Date, &f.Type, &f.RelatedID); e != nil {
				return e
			}
			f.Amount = float64(delta) / 100
			f.Before = float64(before) / 100
			f.After = float64(after) / 100
			f.Remaining = f.After
			out = append(out, f)
		}
		if e = rows.Err(); e != nil {
			return e
		}
	} else {
		group := ""
		key := ""
		switch kind {
		case "channels":
			group = "c.name"
			key = "c.id"
		case "products":
			group = "p.name"
			key = "p.code"
		case "merchants":
			group = "m.name"
			key = "m.id"
		case "statistics":
			group = "c.method"
			key = "c.method"
		default:
			return notfound("报表类型不存在")
		}
		rows, e := s.db.Query("SELECT "+group+",COUNT(*),COALESCE(SUM(CASE WHEN o.status='已支付' THEN o.amount END),0),COALESCE(SUM(CASE WHEN o.status='已支付' THEN o.fee END),0),MAX(m.total_prepay),MAX(m.balance) FROM orders o JOIN merchants m ON m.id=o.merchant_id JOIN products p ON p.code=o.product_code JOIN channels c ON c.id=o.channel_id"+where+" GROUP BY "+key+" ORDER BY 3 DESC", args...)
		if e != nil {
			return e
		}
		defer rows.Close()
		for rows.Next() {
			var f FinanceRow
			var amount, fee, prepaid, balance int64
			if e = rows.Scan(&f.Name, &f.Orders, &amount, &fee, &prepaid, &balance); e != nil {
				return e
			}
			f.Amount = float64(amount) / 100
			f.Fee = float64(fee) / 100
			if kind == "merchants" {
				f.Prepaid = float64(prepaid) / 100
				f.Remaining = float64(balance) / 100
			}
			out = append(out, f)
		}
		if e = rows.Err(); e != nil {
			return e
		}
	}
	if r.URL.Query().Get("export") == "csv" {
		w.Header().Set("Content-Type", "text/csv; charset=utf-8")
		w.Header().Set("Content-Disposition", "attachment; filename=finance.csv")
		_, _ = w.Write([]byte("\xef\xbb\xbf"))
		cw := csv.NewWriter(w)
		_ = cw.Write([]string{"名称", "笔数", "金额", "手续费", "变更前余额", "变更后余额", "时间", "类型", "关联单号"})
		for _, f := range out {
			_ = cw.Write([]string{safeCSV(f.Name), strconv.Itoa(f.Orders), fmt.Sprintf("%.2f", f.Amount), fmt.Sprintf("%.2f", f.Fee), fmt.Sprintf("%.2f", f.Before), fmt.Sprintf("%.2f", f.After), f.Date, safeCSV(f.Type), safeCSV(f.RelatedID)})
		}
		cw.Flush()
		return cw.Error()
	}
	writeJSON(w, 200, out)
	return nil
}
