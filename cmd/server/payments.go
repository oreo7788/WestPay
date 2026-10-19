package main

import (
	"database/sql"
	"encoding/csv"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"
)

const orderSelect = `SELECT o.id,o.merchant_order,o.merchant_id,m.name,o.product_code,p.name,o.amount,c.name,o.channel_id,c.method,o.created_at,o.callback_at,o.status,o.provider_txn,o.query_count,c.plugin,COALESCE(w.out_trade_no,''),COALESCE(qc.url,''),COALESCE(qc.checkout_token,''),COALESCE(w.expires_at,0),COALESCE(w.description,p.name) FROM orders o JOIN merchants m ON m.id=o.merchant_id JOIN products p ON p.code=o.product_code JOIN channels c ON c.id=o.channel_id LEFT JOIN wechat_orders w ON w.order_id=o.id LEFT JOIN wechat_checkouts qc ON qc.order_id=o.id`

type scanner interface{ Scan(...any) error }

func scanOrder(row scanner) (PaymentOrder, error) {
	var o PaymentOrder
	var amount int64
	var token string
	e := row.Scan(&o.SystemID, &o.MerchantOrder, &o.MerchantID, &o.MerchantName, &o.ProductCode, &o.ProductName, &amount, &o.ChannelName, &o.ChannelID, &o.Method, &o.CreatedAt, &o.CallbackAt, &o.Status, &o.ProviderTxn, &o.QueryCount, &o.Plugin, &o.ProviderOrder, &o.PayURL, &token, &o.ExpiresAt, &o.Description)
	o.Amount = float64(amount) / 100
	o.RechargeID = o.SystemID
	if o.PayURL != "" && token != "" {
		o.QRImage = "/api/payments/" + o.SystemID + "/qrcode"
		o.QRImageURL = strings.TrimSuffix(o.PayURL, "/pay/"+token) + "/api/cashier/" + token + "/qr.png"
	}
	o.CanPay = o.PayURL != "" && (o.Status == "未支付" || o.Status == "处理中") && o.ExpiresAt > time.Now().Unix()
	return o, e
}
func orderBy(db interface{ QueryRow(string, ...any) *sql.Row }, oid string) (PaymentOrder, error) {
	o, e := scanOrder(db.QueryRow(orderSelect+" WHERE o.id=?", oid))
	if e == sql.ErrNoRows {
		return o, notfound("订单不存在")
	}
	return o, e
}
func (s *Store) transaction(fn func(*sql.Tx) error) error {
	tx, e := s.db.Begin()
	if e != nil {
		return e
	}
	defer tx.Rollback()
	if e = fn(tx); e != nil {
		return e
	}
	return tx.Commit()
}

type createPaymentInput struct {
	MerchantID    string  `json:"merchantId"`
	MerchantOrder string  `json:"merchantOrder"`
	ProductCode   string  `json:"productCode"`
	ChannelID     string  `json:"channelId"`
	Amount        float64 `json:"amount"`
	Description   string  `json:"description"`
}

func (s *Store) createPayment(w http.ResponseWriter, r *http.Request, u User) error {
	var b createPaymentInput
	if err := decode(w, r, &b); err != nil {
		return err
	}
	if !u.creationAllowed("orders", b.MerchantID) {
		return forbid()
	}
	o, _, err := s.createOrder(r, u, b, false)
	if err != nil {
		return err
	}
	writeJSON(w, 201, redactOrder(u, o))
	return nil
}
func (s *Store) createOrder(r *http.Request, u User, b createPaymentInput, idempotent bool) (PaymentOrder, bool, error) {
	b.Description = strings.TrimSpace(b.Description)
	if len(b.Description) > 127 {
		return PaymentOrder{}, false, bad("商品描述最多为 127 字节")
	}
	amount, e := cents(b.Amount)
	if e != nil {
		return PaymentOrder{}, false, e
	}
	b.MerchantOrder = strings.TrimSpace(b.MerchantOrder)
	if amount <= 0 || b.MerchantOrder == "" || len(b.MerchantOrder) > 100 {
		return PaymentOrder{}, false, bad("请填写有效金额与商户订单号")
	}
	oid := id("P-")
	created := true
	e = s.transaction(func(tx *sql.Tx) error {
		var prior, priorProduct, priorChannel, priorDescription, priorPlugin string
		var priorAmount int64
		err := tx.QueryRow("SELECT o.id,o.product_code,o.channel_id,o.amount,COALESCE(wx.description,p.name),c.plugin FROM orders o JOIN channels c ON c.id=o.channel_id JOIN products p ON p.code=o.product_code LEFT JOIN wechat_orders wx ON wx.order_id=o.id WHERE o.merchant_id=? AND o.merchant_order=?", b.MerchantID, b.MerchantOrder).Scan(&prior, &priorProduct, &priorChannel, &priorAmount, &priorDescription, &priorPlugin)
		if err == nil {
			if !idempotent {
				return conflict("该商户订单号已存在，请勿重复创建")
			}
			if priorPlugin != wechatPlugin {
				return conflict("该商户订单号已用于其他支付通道")
			}
			if priorProduct != b.ProductCode || priorAmount != amount || (b.ChannelID != "" && priorChannel != b.ChannelID) || (b.Description != "" && priorDescription != b.Description) {
				return conflict("商户订单号已存在，但金额、产品、通道或商品描述与原订单不一致")
			}
			oid = prior
			created = false
			return nil
		}
		if err != sql.ErrNoRows {
			return err
		}
		var active bool
		var fee, min, max, channelRate int64
		var method, plugin string
		if idempotent && b.ChannelID == "" {
			err = tx.QueryRow("SELECT id FROM channels WHERE enabled=1 AND plugin=? AND method='微信支付' AND min_cents<=? AND max_cents>=? ORDER BY fee_bps,id LIMIT 1", wechatPlugin, amount, amount).Scan(&b.ChannelID)
			if err == sql.ErrNoRows {
				return bad("没有支持此金额的已启用微信 JSAPI 通道")
			}
			if err != nil {
				return err
			}
		}
		if err = tx.QueryRow("SELECT enabled FROM merchants WHERE id=?", b.MerchantID).Scan(&active); err != nil {
			return bad("请选择有效商户")
		}
		if !active {
			return bad("该商户已关闭")
		}
		if err = tx.QueryRow("SELECT enabled,fee_bps FROM products WHERE code=?", b.ProductCode).Scan(&active, &fee); err != nil {
			return bad("请选择有效产品")
		}
		if !active {
			return bad("该产品已关闭")
		}
		if err = tx.QueryRow("SELECT enabled,min_cents,max_cents,method,fee_bps,plugin FROM channels WHERE id=?", b.ChannelID).Scan(&active, &min, &max, &method, &channelRate, &plugin); err != nil {
			return bad("请选择有效通道")
		}
		if idempotent && plugin != wechatPlugin {
			return bad("业务下单接口仅支持微信 JSAPI 通道")
		}
		if !active || amount < min || amount > max {
			return bad("通道已关闭或订单金额超出通道限额")
		}
		var productMethod string
		if err = tx.QueryRow("SELECT method FROM products WHERE code=?", b.ProductCode).Scan(&productMethod); err != nil {
			return err
		}
		if productMethod != method {
			return bad("产品与通道支付方式不匹配")
		}
		var minimum, maximum string
		if err = tx.QueryRow("SELECT value FROM settings WHERE key='minAmount'").Scan(&minimum); err != nil {
			return err
		}
		if err = tx.QueryRow("SELECT value FROM settings WHERE key='maxAmount'").Scan(&maximum); err != nil {
			return err
		}
		lo, _ := strconv.ParseFloat(minimum, 64)
		hi, _ := strconv.ParseFloat(maximum, 64)
		if b.Amount < lo || b.Amount > hi {
			return bad("金额超出系统收款限额")
		}
		var blocked int
		if err = tx.QueryRow("SELECT COUNT(*) FROM records WHERE kind='blacklist' AND status='生效' AND name IN (?,(SELECT name FROM merchants WHERE id=?))", b.MerchantID, b.MerchantID).Scan(&blocked); err != nil {
			return err
		}
		if blocked > 0 {
			return apiError{403, "该商户已列入黑名单"}
		}
		if _, err = tx.Exec("INSERT INTO orders(id,merchant_order,merchant_id,product_code,channel_id,amount,fee,cost,status,created_at) VALUES(?,?,?,?,?,?,?,?,?,?)", oid, b.MerchantOrder, b.MerchantID, b.ProductCode, b.ChannelID, amount, (amount*fee+5000)/10000, (amount*channelRate+5000)/10000, "未支付", timestamp()); err != nil {
			return err
		}
		if plugin == wechatPlugin {
			if err = s.reserveWechat(tx, oid, b.MerchantID, (amount*fee+5000)/10000, b.Description, u, r); err != nil {
				return err
			}
		} else if plugin != "demo" {
			return bad("不支持该通道插件")
		}
		return s.audit(tx, u, r, "创建订单", oid, b.MerchantOrder)
	})
	if e != nil {
		return PaymentOrder{}, false, e
	}
	o, e := orderBy(s.db, oid)
	if e != nil {
		return PaymentOrder{}, false, e
	}
	return o, created, nil
}
func (s *Store) listPayments(w http.ResponseWriter, r *http.Request, u User) error {
	if err := s.expireOrders(); err != nil {
		return err
	}
	clauses := []string{"1=1"}
	args := []any{}
	if u.merchant() {
		clauses = append(clauses, "o.merchant_id=?")
		args = append(args, u.MerchantID)
	}
	v := r.URL.Query()
	pageScope := "orders"
	if v.Get("archive") == "true" {
		pageScope = "history"
	}
	visibility, scopeArgs := u.orderWhere(pageScope, "o.")
	if v.Get("all") == "true" && v.Get("archive") != "true" {
		current, ca := u.orderWhere("orders", "o.")
		history, ha := u.orderWhere("history", "o.")
		visibility = "((o.created_at>=? AND " + current + ") OR (o.created_at<? AND " + history + "))"
		today := time.Now().Format("2006-01-02")
		scopeArgs = append([]any{today}, ca...)
		scopeArgs = append(scopeArgs, today)
		scopeArgs = append(scopeArgs, ha...)
	}
	clauses = append(clauses, visibility)
	args = append(args, scopeArgs...)
	if v.Get("archive") == "true" {
		clauses = append(clauses, "o.created_at < ?")
		args = append(args, time.Now().Format("2006-01-02"))
	} else if v.Get("all") != "true" {
		clauses = append(clauses, "o.created_at >= ?")
		args = append(args, time.Now().Format("2006-01-02"))
	}
	for _, f := range []struct{ key, col string }{{"systemId", "o.id"}, {"merchantOrder", "o.merchant_order"}} {
		if value := strings.TrimSpace(v.Get(f.key)); value != "" {
			clauses = append(clauses, "instr(lower("+f.col+"),lower(?))>0")
			args = append(args, value)
		}
	}
	if value := strings.TrimSpace(v.Get("merchant")); value != "" {
		clauses = append(clauses, "(instr(lower(m.name),lower(?))>0 OR instr(lower(m.id),lower(?))>0)")
		args = append(args, value, value)
	}
	for _, f := range []struct{ key, col, all string }{{"status", "o.status", "全部状态"}, {"method", "c.method", "所有支付方式"}, {"merchantId", "o.merchant_id", ""}, {"channelId", "o.channel_id", ""}} {
		if x := v.Get(f.key); x != "" && x != f.all {
			clauses = append(clauses, f.col+"=?")
			args = append(args, x)
		}
	}
	if from := v.Get("from"); from != "" {
		if _, e := time.Parse("2006-01-02", from); e != nil {
			return bad("开始日期无效")
		}
		clauses = append(clauses, "o.created_at>=?")
		args = append(args, from)
	}
	if to := v.Get("to"); to != "" {
		d, e := time.Parse("2006-01-02", to)
		if e != nil {
			return bad("结束日期无效")
		}
		clauses = append(clauses, "o.created_at<?")
		args = append(args, d.AddDate(0, 0, 1).Format("2006-01-02"))
	}
	where := " WHERE " + strings.Join(clauses, " AND ")
	page, size, total := 1, 20, 0
	paginated := v.Get("page") != "" && v.Get("export") != "csv"
	suffix := " ORDER BY o.created_at DESC,o.id DESC"
	if paginated {
		var err error
		page, err = strconv.Atoi(v.Get("page"))
		if err != nil || page < 1 {
			return bad("页码无效")
		}
		if v.Get("pageSize") != "" {
			size, err = strconv.Atoi(v.Get("pageSize"))
			if err != nil || size < 1 || size > 100 {
				return bad("每页数量须在 1–100 之间")
			}
		}
		if err = s.db.QueryRow("SELECT COUNT(*) FROM orders o JOIN merchants m ON m.id=o.merchant_id JOIN channels c ON c.id=o.channel_id"+where, args...).Scan(&total); err != nil {
			return err
		}
		suffix += " LIMIT ? OFFSET ?"
		args = append(args, size, (page-1)*size)
	} else if v.Get("export") != "csv" {
		suffix += " LIMIT 1000"
	}
	rows, e := s.db.Query(orderSelect+where+suffix, args...)
	if e != nil {
		return e
	}
	defer rows.Close()
	out := []PaymentOrder{}
	for rows.Next() {
		o, e := scanOrder(rows)
		if e != nil {
			return e
		}
		out = append(out, redactOrder(u, o))
	}
	if e = rows.Err(); e != nil {
		return e
	}
	if v.Get("export") == "csv" {
		w.Header().Set("Content-Type", "text/csv; charset=utf-8")
		w.Header().Set("Content-Disposition", "attachment; filename=payments.csv")
		_, _ = w.Write([]byte("\xef\xbb\xbf"))
		csvw := csv.NewWriter(w)
		_ = csvw.Write([]string{"系统单号", "商户单号", "商户", "产品", "金额", "通道", "状态", "创建时间"})
		for _, o := range out {
			_ = csvw.Write([]string{o.SystemID, safeCSV(o.MerchantOrder), safeCSV(o.MerchantName), safeCSV(o.ProductName), fmt.Sprintf("%.2f", o.Amount), safeCSV(o.ChannelName), o.Status, o.CreatedAt})
		}
		csvw.Flush()
		return csvw.Error()
	}
	if paginated {
		writeJSON(w, 200, map[string]any{"items": out, "total": total, "page": page, "pageSize": size})
	} else {
		writeJSON(w, 200, out)
	}
	return nil
}
func safeCSV(s string) string {
	if strings.ContainsAny(strings.TrimLeft(s, " \t\r\n")[:min(1, len(strings.TrimLeft(s, " \t\r\n")))], "=+-@") {
		return "'" + s
	}
	return s
}
func (s *Store) queryPayment(w http.ResponseWriter, r *http.Request, u User) error {
	current, err := orderBy(s.db, r.PathValue("id"))
	if err != nil {
		return err
	}
	if current.Plugin == wechatPlugin {
		if err = s.reconcileWechatOrder(r.Context(), current.SystemID, u.Name); err != nil {
			return err
		}
		if err = s.transaction(func(tx *sql.Tx) error {
			if _, err := tx.Exec("UPDATE orders SET query_count=query_count+1 WHERE id=?", current.SystemID); err != nil {
				return err
			}
			return s.audit(tx, u, r, "查询微信订单", current.SystemID, "微信支付查单")
		}); err != nil {
			return err
		}
		updated, err := orderBy(s.db, current.SystemID)
		if err != nil {
			return err
		}
		writeJSON(w, 200, redactOrder(u, updated))
		return nil
	}
	if err := s.expireOrders(); err != nil {
		return err
	}
	e := s.transaction(func(tx *sql.Tx) error {
		var plugin string
		if e := tx.QueryRow("SELECT c.plugin FROM orders o JOIN channels c ON c.id=o.channel_id WHERE o.id=?", r.PathValue("id")).Scan(&plugin); e == sql.ErrNoRows {
			return notfound("订单不存在")
		} else if e != nil {
			return e
		}
		if plugin != "demo" {
			return conflict("该通道尚未接入查询接口")
		}
		if _, e := tx.Exec("UPDATE orders SET query_count=query_count+1 WHERE id=?", r.PathValue("id")); e != nil {
			return e
		}
		return s.audit(tx, u, r, "查询订单", r.PathValue("id"), "本地模拟通道")
	})
	if e != nil {
		return e
	}
	o, e := orderBy(s.db, r.PathValue("id"))
	if e != nil {
		return e
	}
	writeJSON(w, 200, map[string]any{"message": "已读取本地模拟通道状态", "order": redactOrder(u, o)})
	return nil
}
func (s *Store) simulatePayment(w http.ResponseWriter, r *http.Request, u User) error {
	if err := s.expireOrders(); err != nil {
		return err
	}
	var b struct {
		Status string `json:"status"`
	}
	if e := decode(w, r, &b); e != nil {
		return e
	}
	if b.Status != "已支付" && b.Status != "支付失败" {
		return bad("模拟结果无效")
	}
	e := s.transaction(func(tx *sql.Tx) error {
		var merchant, status, plugin string
		var fee int64
		e := tx.QueryRow("SELECT o.merchant_id,o.status,o.fee,c.plugin FROM orders o JOIN channels c ON c.id=o.channel_id WHERE o.id=?", r.PathValue("id")).Scan(&merchant, &status, &fee, &plugin)
		if e == sql.ErrNoRows {
			return notfound("订单不存在")
		}
		if e != nil {
			return e
		}
		if plugin != "demo" {
			return conflict("只有本地模拟通道支持此操作")
		}
		if status == b.Status {
			return nil
		}
		if status != "未支付" && status != "处理中" {
			return conflict("订单已完成，不可重复变更结果")
		}
		if b.Status == "已支付" {
			var before int64
			if e = tx.QueryRow("SELECT balance FROM merchants WHERE id=?", merchant).Scan(&before); e != nil {
				return e
			}
			if before < fee {
				return conflict("商户预付余额不足，无法扣除手续费")
			}
			if _, e = tx.Exec("UPDATE merchants SET balance=balance-? WHERE id=?", fee, merchant); e != nil {
				return e
			}
			if _, e = tx.Exec("INSERT INTO ledger VALUES(?,?,?,?,?,?,?,?,?,?)", id("L-"), merchant, -fee, before, before-fee, "订单手续费", r.PathValue("id"), "本地模拟收款", u.Name, timestamp()); e != nil {
				return e
			}
		}
		provider := ""
		if b.Status == "已支付" {
			provider = id("SIM-")
		}
		if _, e = tx.Exec("UPDATE orders SET status=?,callback_at=?,provider_txn=? WHERE id=?", b.Status, timestamp(), provider, r.PathValue("id")); e != nil {
			return e
		}
		return s.audit(tx, u, r, "模拟支付结果", r.PathValue("id"), b.Status)
	})
	if e != nil {
		return e
	}
	o, e := orderBy(s.db, r.PathValue("id"))
	if e != nil {
		return e
	}
	writeJSON(w, 200, redactOrder(u, o))
	return nil
}
func (s *Store) listMerchants(w http.ResponseWriter, r *http.Request, u User) error {
	page := "merchants"
	if !u.can("merchants.view") && u.can("orders.create") {
		page = "order_lookup"
	}
	scope, args := u.dataWhere(page, "merchants", "id", "id")
	where := " WHERE " + scope
	if u.merchant() {
		where += " AND id=?"
		args = append(args, u.MerchantID)
	}
	if value := r.URL.Query().Get("enabled"); value != "" {
		if value != "true" && value != "false" {
			return bad("商户状态无效")
		}
		if where == "" {
			where = " WHERE enabled=?"
		} else {
			where += " AND enabled=?"
		}
		args = append(args, value == "true")
	}
	rows, e := s.db.Query("SELECT id,name,balance,total_prepay,enabled FROM merchants"+where+" ORDER BY created_at DESC", args...)
	if e != nil {
		return e
	}
	out := []Merchant{}
	for rows.Next() {
		var m Merchant
		var balance, total int64
		if e = rows.Scan(&m.ID, &m.Name, &balance, &total, &m.Enabled); e != nil {
			rows.Close()
			return e
		}
		m.Prepaid = float64(total) / 100
		m.TotalPrepay = m.Prepaid
		m.Remaining = float64(balance) / 100
		out = append(out, m)
	}
	e = rows.Err()
	rows.Close()
	if e != nil {
		return e
	}
	today := time.Now().Format("2006-01-02")
	yesterday := time.Now().AddDate(0, 0, -1).Format("2006-01-02")
	for i := range out {
		var success int
		if e = s.db.QueryRow("SELECT COUNT(*),COALESCE(SUM(status='已支付'),0) FROM orders WHERE merchant_id=? AND created_at>=?", out[i].ID, today).Scan(&out[i].TodayCount, &success); e != nil {
			return e
		}
		if out[i].TodayCount > 0 {
			out[i].TodayRate = float64(success) * 100 / float64(out[i].TodayCount)
		}
		if e = s.db.QueryRow("SELECT COUNT(*) FROM orders WHERE merchant_id=? AND created_at>=? AND created_at<?", out[i].ID, yesterday, today).Scan(&out[i].Yesterday); e != nil {
			return e
		}
		if e = s.db.QueryRow("SELECT COALESCE(SUM(created_at>=?),0),COALESCE(SUM(created_at>=?),0) FROM orders WHERE merchant_id=?", time.Now().Add(-30*time.Minute).Format("2006-01-02 15:04:05"), time.Now().Add(-10*time.Minute).Format("2006-01-02 15:04:05"), out[i].ID).Scan(&out[i].Recent30m, &out[i].Recent10m); e != nil {
			return e
		}
	}
	writeJSON(w, 200, filterKeyword(r, out))
	return nil
}
func (s *Store) createMerchant(w http.ResponseWriter, r *http.Request, u User) error {
	var b struct {
		Name    string  `json:"name"`
		Prepaid float64 `json:"prepaid"`
	}
	if e := decode(w, r, &b); e != nil {
		return e
	}
	b.Name = strings.TrimSpace(b.Name)
	n, e := cents(b.Prepaid)
	if e != nil {
		return e
	}
	if b.Name == "" || len(b.Name) > 100 || n < 0 {
		return bad("请填写商户名称和有效预付金额")
	}
	if n > 0 && !u.can("merchants.adjust") {
		return forbid()
	}
	mid := id("M-")
	if !u.creationAllowed("merchants", mid) {
		return forbid()
	}
	e = s.transaction(func(tx *sql.Tx) error {
		if _, e := tx.Exec("INSERT INTO merchants VALUES(?,?,?,?,?,?)", mid, b.Name, n, n, true, timestamp()); e != nil {
			return e
		}
		if n > 0 {
			if _, e := tx.Exec("INSERT INTO ledger VALUES(?,?,?,?,?,?,?,?,?,?)", id("L-"), mid, n, 0, n, "初始预付", mid, "创建商户", u.Name, timestamp()); e != nil {
				return e
			}
		}
		return s.audit(tx, u, r, "创建商户", mid, b.Name)
	})
	if e != nil {
		return e
	}
	writeJSON(w, 201, map[string]string{"id": mid})
	return nil
}
func (s *Store) editMerchant(w http.ResponseWriter, r *http.Request, u User) error {
	var b struct {
		Name    string `json:"name"`
		Enabled bool   `json:"enabled"`
	}
	if e := decode(w, r, &b); e != nil {
		return e
	}
	if strings.TrimSpace(b.Name) == "" {
		return bad("名称不能为空")
	}
	e := s.transaction(func(tx *sql.Tx) error {
		var wasEnabled bool
		if err := tx.QueryRow("SELECT enabled FROM merchants WHERE id=?", r.PathValue("id")).Scan(&wasEnabled); err == sql.ErrNoRows {
			return notfound("商户不存在")
		} else if err != nil {
			return err
		}
		if wasEnabled != b.Enabled && !u.can("merchants.toggle") {
			return forbid()
		}
		result, e := tx.Exec("UPDATE merchants SET name=?,enabled=? WHERE id=?", strings.TrimSpace(b.Name), b.Enabled, r.PathValue("id"))
		if e != nil {
			return e
		}
		n, _ := result.RowsAffected()
		if n == 0 {
			return notfound("商户不存在")
		}
		return s.audit(tx, u, r, "修改商户", r.PathValue("id"), b.Name)
	})
	if e != nil {
		return e
	}
	writeJSON(w, 200, map[string]bool{"ok": true})
	return nil
}
func (s *Store) adjustPrepay(w http.ResponseWriter, r *http.Request, u User) error {
	var b struct {
		Amount float64 `json:"amount"`
		Note   string  `json:"note"`
		Key    string  `json:"idempotencyKey"`
	}
	if e := decode(w, r, &b); e != nil {
		return e
	}
	n, e := cents(b.Amount)
	if e != nil {
		return e
	}
	if n == 0 || len(b.Key) < 8 || len(b.Key) > 100 {
		return bad("请输入非零金额并提供操作标识")
	}
	ref := "ADJ-" + b.Key
	e = s.transaction(func(tx *sql.Tx) error {
		var existingMid string
		var delta int64
		err := tx.QueryRow("SELECT merchant_id,delta FROM ledger WHERE reference=?", ref).Scan(&existingMid, &delta)
		if err == nil {
			if existingMid == r.PathValue("id") && delta == n {
				return nil
			}
			return conflict("操作标识已用于其他调整")
		}
		if err != sql.ErrNoRows {
			return err
		}
		var before int64
		err = tx.QueryRow("SELECT balance FROM merchants WHERE id=?", r.PathValue("id")).Scan(&before)
		if err == sql.ErrNoRows {
			return notfound("商户不存在")
		}
		if err != nil {
			return err
		}
		if before+n < 0 {
			return conflict("剩余预付不能小于 0")
		}
		positive := int64(0)
		if n > 0 {
			positive = n
		}
		if _, err = tx.Exec("UPDATE merchants SET balance=balance+?,total_prepay=total_prepay+? WHERE id=?", n, positive, r.PathValue("id")); err != nil {
			return err
		}
		if _, err = tx.Exec("INSERT INTO ledger VALUES(?,?,?,?,?,?,?,?,?,?)", id("L-"), r.PathValue("id"), n, before, before+n, "预付调整", ref, b.Note, u.Name, timestamp()); err != nil {
			return err
		}
		return s.audit(tx, u, r, "调整预付", r.PathValue("id"), fmt.Sprintf("变更 %.2f 元；%s", float64(n)/100, b.Note))
	})
	if e != nil {
		return e
	}
	writeJSON(w, 200, map[string]bool{"ok": true})
	return nil
}
func (s *Store) toggleMerchant(w http.ResponseWriter, r *http.Request, u User) error {
	return s.toggle(w, r, u, "merchants", "商户")
}
func (s *Store) toggleChannel(w http.ResponseWriter, r *http.Request, u User) error {
	return s.toggle(w, r, u, "channels", "通道")
}
func (s *Store) toggle(w http.ResponseWriter, r *http.Request, u User, table, label string) error {
	e := s.transaction(func(tx *sql.Tx) error {
		result, e := tx.Exec("UPDATE "+table+" SET enabled=NOT enabled WHERE id=?", r.PathValue("id"))
		if e != nil {
			return e
		}
		n, _ := result.RowsAffected()
		if n == 0 {
			return notfound(label + "不存在")
		}
		return s.audit(tx, u, r, "切换"+label+"状态", r.PathValue("id"), "")
	})
	if e != nil {
		return e
	}
	writeJSON(w, 200, map[string]bool{"ok": true})
	return nil
}
func (s *Store) listProducts(w http.ResponseWriter, r *http.Request, u User) error {
	page := "products"
	if !u.can("products.view") && u.can("orders.create") {
		page = "order_lookup"
	}
	scope, args := u.dataWhere(page, "products", "code", "")
	rows, e := s.db.Query("SELECT code,name,fee_bps,method,enabled FROM products WHERE "+scope+" ORDER BY code", args...)
	if e != nil {
		return e
	}
	defer rows.Close()
	out := []Product{}
	for rows.Next() {
		var p Product
		var rate int64
		if e = rows.Scan(&p.Code, &p.Name, &rate, &p.Method, &p.Enabled); e != nil {
			return e
		}
		p.FeeRate = float64(rate) / 100
		out = append(out, p)
	}
	if e = rows.Err(); e != nil {
		return e
	}
	writeJSON(w, 200, filterKeyword(r, out))
	return nil
}
func (s *Store) saveProduct(w http.ResponseWriter, r *http.Request, u User) error {
	var p Product
	if e := decode(w, r, &p); e != nil {
		return e
	}
	rate, e := bps(p.FeeRate)
	if e != nil {
		return e
	}
	if p.Method != "支付宝" && p.Method != "微信支付" && p.Method != "网银支付" {
		return bad("请选择产品支付方式")
	}
	if len(p.Code) > 64 {
		return bad("产品编码最多 64 位")
	}
	for _, runeValue := range p.Code {
		if !(runeValue >= 'a' && runeValue <= 'z' || runeValue >= 'A' && runeValue <= 'Z' || runeValue >= '0' && runeValue <= '9' || runeValue == '-' || runeValue == '_') {
			return bad("产品编码仅支持字母、数字、下划线和横线")
		}
	}
	if strings.TrimSpace(p.Code) == "" || strings.TrimSpace(p.Name) == "" {
		return bad("请填写产品编码与名称")
	}
	if r.Method == "PATCH" && p.Code != r.PathValue("id") {
		return bad("产品编码不可修改")
	}
	e = s.transaction(func(tx *sql.Tx) error {
		var err error
		if r.Method == "POST" {
			var n int
			if err = tx.QueryRow("SELECT COUNT(*) FROM products WHERE code=?", p.Code).Scan(&n); err != nil {
				return err
			}
			if n > 0 {
				return conflict("产品编码已存在")
			}
			_, err = tx.Exec("INSERT INTO products VALUES(?,?,?,?,?)", p.Code, p.Name, rate, p.Method, p.Enabled)
		} else {
			var previous string
			var used int
			if e := tx.QueryRow("SELECT method FROM products WHERE code=?", p.Code).Scan(&previous); e != nil {
				return notfound("产品不存在")
			}
			if e := tx.QueryRow("SELECT COUNT(*) FROM orders WHERE product_code=?", p.Code).Scan(&used); e != nil {
				return e
			}
			if used > 0 && previous != p.Method {
				return conflict("产品已有订单，支付方式不可修改")
			}
			var result sql.Result
			result, err = tx.Exec("UPDATE products SET name=?,fee_bps=?,method=?,enabled=? WHERE code=?", p.Name, rate, p.Method, p.Enabled, p.Code)
			if err == nil {
				n, _ := result.RowsAffected()
				if n == 0 {
					return notfound("产品不存在")
				}
			}
		}
		if err != nil {
			return err
		}
		if r.Method == "POST" {
			if !u.creationAllowed("products", "") {
				return forbid()
			}
		}
		return s.audit(tx, u, r, "保存产品", p.Code, p.Name)
	})
	if e != nil {
		return e
	}
	writeJSON(w, 200, p)
	return nil
}
func (s *Store) listChannels(w http.ResponseWriter, r *http.Request, u User) error {
	page := "channels"
	if !u.can("channels.view") && u.can("orders.create") {
		page = "order_lookup"
	}
	scope, args := u.dataWhere(page, "channels", "id", "")
	rows, e := s.db.Query("SELECT id,name,fee_bps,min_cents,max_cents,method,plugin,enabled FROM channels WHERE "+scope+" ORDER BY id", args...)
	if e != nil {
		return e
	}
	out := []Channel{}
	for rows.Next() {
		var c Channel
		var fee, min, max int64
		if e = rows.Scan(&c.ID, &c.Name, &fee, &min, &max, &c.Method, &c.Plugin, &c.Enabled); e != nil {
			rows.Close()
			return e
		}
		c.FeeRate = float64(fee) / 100
		c.MinAmount = float64(min) / 100
		c.MaxAmount = float64(max) / 100
		out = append(out, c)
	}
	e = rows.Err()
	rows.Close()
	if e != nil {
		return e
	}
	today := time.Now().Format("2006-01-02")
	yesterday := time.Now().AddDate(0, 0, -1).Format("2006-01-02")
	for i := range out {
		var a, b int64
		if e = s.db.QueryRow("SELECT COALESCE(SUM(CASE WHEN created_at>=? THEN amount END),0),COALESCE(SUM(CASE WHEN created_at>=? AND created_at<? THEN amount END),0) FROM orders WHERE channel_id=? AND status='已支付'", today, yesterday, today, out[i].ID).Scan(&a, &b); e != nil {
			return e
		}
		out[i].TodayAmount = float64(a) / 100
		out[i].Yesterday = float64(b) / 100
	}
	writeJSON(w, 200, filterKeyword(r, out))
	return nil
}
func (s *Store) saveChannel(w http.ResponseWriter, r *http.Request, u User) error {
	var c Channel
	if e := decode(w, r, &c); e != nil {
		return e
	}
	rate, e := bps(c.FeeRate)
	if e != nil {
		return e
	}
	min, e := cents(c.MinAmount)
	if e != nil {
		return e
	}
	max, e := cents(c.MaxAmount)
	if e != nil {
		return e
	}
	if min <= 0 || max < min || strings.TrimSpace(c.Name) == "" {
		return bad("通道名称或金额限额无效")
	}
	if c.Method != "支付宝" && c.Method != "微信支付" && c.Method != "网银支付" {
		return bad("不支持该支付方式")
	}
	if c.Plugin != "demo" && c.Plugin != wechatPlugin {
		return bad("不支持该支付插件")
	}
	if c.Plugin == wechatPlugin && c.Method != "微信支付" {
		return bad("微信 JSAPI 插件仅支持微信支付")
	}
	if r.Method == "POST" {
		c.ID = id("CH-")
	} else {
		c.ID = r.PathValue("id")
	}
	e = s.transaction(func(tx *sql.Tx) error {
		if r.Method == "PATCH" {
			var enabled bool
			if err := tx.QueryRow("SELECT enabled FROM channels WHERE id=?", r.PathValue("id")).Scan(&enabled); err == sql.ErrNoRows {
				return notfound("通道不存在")
			} else if err != nil {
				return err
			}
			if enabled != c.Enabled && !u.can("channels.toggle") {
				return forbid()
			}
		}

		var e error
		if r.Method == "POST" {
			_, e = tx.Exec("INSERT INTO channels VALUES(?,?,?,?,?,?,?,?)", c.ID, c.Name, rate, min, max, c.Method, c.Plugin, c.Enabled)
		} else {
			var previous, previousPlugin string
			var used int
			if e := tx.QueryRow("SELECT method,plugin FROM channels WHERE id=?", c.ID).Scan(&previous, &previousPlugin); e != nil {
				return notfound("通道不存在")
			}
			if e := tx.QueryRow("SELECT COUNT(*) FROM orders WHERE channel_id=?", c.ID).Scan(&used); e != nil {
				return e
			}
			if used > 0 && (previous != c.Method || previousPlugin != c.Plugin) {
				return conflict("通道已有订单，支付方式和插件不可修改")
			}
			var result sql.Result
			result, e = tx.Exec("UPDATE channels SET name=?,fee_bps=?,min_cents=?,max_cents=?,method=?,plugin=?,enabled=? WHERE id=?", c.Name, rate, min, max, c.Method, c.Plugin, c.Enabled, c.ID)
			if e == nil {
				n, _ := result.RowsAffected()
				if n == 0 {
					return notfound("通道不存在")
				}
			}
		}
		if e != nil {
			return e
		}
		if r.Method == "POST" {
			if !u.creationAllowed("channels", "") {
				return forbid()
			}
		}
		return s.audit(tx, u, r, "保存通道", c.ID, c.Name)
	})
	if e != nil {
		return e
	}
	writeJSON(w, 200, c)
	return nil
}

func (s *Store) getPayment(w http.ResponseWriter, r *http.Request, u User) error {
	o, err := orderBy(s.db, r.PathValue("id"))
	if err != nil {
		return err
	}
	writeJSON(w, 200, redactOrder(u, o))
	return nil
}
