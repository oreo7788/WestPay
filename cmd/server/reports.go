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
	scope := ""
	scopeArgs := []any{}
	if u.merchant() {
		scope = " AND merchant_id=?"
		scopeArgs = append(scopeArgs, u.MerchantID)
	}
	var amount, fee, monthAmount, yesterdayAmount int64
	if e := s.db.QueryRow(`SELECT COUNT(*),COALESCE(SUM(status='已支付'),0),COALESCE(SUM(CASE WHEN status='已支付' THEN amount END),0),COALESCE(SUM(CASE WHEN status='已支付' THEN fee-cost END),0) FROM orders WHERE created_at>=?`+scope, append([]any{today}, scopeArgs...)...).Scan(&d.TodayOrders, &d.TodaySuccess, &amount, &fee); e != nil {
		return e
	}
	if e := s.db.QueryRow("SELECT COALESCE(SUM(amount),0) FROM orders WHERE status='已支付' AND created_at>=?"+scope, append([]any{month}, scopeArgs...)...).Scan(&monthAmount); e != nil {
		return e
	}
	if e := s.db.QueryRow("SELECT COUNT(*),COALESCE(SUM(CASE WHEN status='已支付' THEN amount END),0) FROM orders WHERE created_at>=? AND created_at<?"+scope, append([]any{yesterday, today}, scopeArgs...)...).Scan(&d.Yesterday, &yesterdayAmount); e != nil {
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
	rows, e := s.db.Query("SELECT CAST(substr(created_at,12,2) AS INTEGER)/2,SUM(amount) FROM orders WHERE status='已支付' AND created_at>=?"+scope+" GROUP BY 1", append([]any{today}, scopeArgs...)...)
	if e != nil {
		return e
	}
	defer rows.Close()
	for rows.Next() {
		var index int
		var n int64
		if e = rows.Scan(&index, &n); e != nil {
			return e
		}
		if index >= 0 && index < 12 {
			d.Trend[index] = float64(n) / 100
		}
	}
	if e = rows.Err(); e != nil {
		return e
	}
	writeJSON(w, 200, d)
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
	if u.merchant() {
		where += " AND " + alias + ".merchant_id=?"
		args = append(args, u.MerchantID)
	}
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
