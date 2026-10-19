package main

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func TestMerchantDataScopesAcrossLists(t *testing.T) {
	s, admin, _ := fixture(t)
	createMerchant := func(name string) string {
		return request(t, s, admin, "POST", "/api/merchants", map[string]any{"name": name, "prepaid": 10}, 201)["id"].(string)
	}
	a, b := createMerchant("Scope merchant A"), createMerchant("Scope merchant B")
	createOrder := func(mid, number string, amount int) string {
		o := request(t, s, admin, "POST", "/api/payments", map[string]any{"merchantId": mid, "merchantOrder": number, "amount": amount, "productCode": "PAY-ALI", "channelId": "CH-DEMO-01"}, 201)
		id := o["systemId"].(string)
		request(t, s, admin, "POST", "/api/payments/"+id+"/simulate", map[string]string{"status": "已支付"}, 200)
		return id
	}
	currentA, historicA, currentB := createOrder(a, "SCOPE-A", 100), createOrder(a, "SCOPE-A-OLD", 50), createOrder(b, "SCOPE-B", 200)
	if _, e := s.db.Exec("UPDATE orders SET created_at=? WHERE id=?", time.Now().AddDate(0, 0, -1).Format("2006-01-02 15:04:05"), historicA); e != nil {
		t.Fatal(e)
	}
	recordA := request(t, s, admin, "POST", "/api/records/complaints", map[string]any{"name": "Record A", "merchantId": a, "status": "待处理"}, 200)["id"].(string)
	recordB := request(t, s, admin, "POST", "/api/records/complaints", map[string]any{"name": "Record B", "merchantId": b, "status": "待处理"}, 200)["id"].(string)
	request(t, s, admin, "POST", "/api/records/complaints", map[string]any{"name": "Global record", "status": "待处理"}, 200)
	grants := []string{"orders.view", "orders.qr", "orders.query", "orders.export", "history.view", "history.qr", "history.export", "home.view", "home.today_amount", "home.latest_orders", "home.merchant_count", "home.merchant_ranking", "home.channels", "home.channel_count", "merchants.view", "merchants.apikey", "merchants.edit", "products.view", "channels.view", "complaints.view", "complaints.edit", "complaints.create", "finance_merchants.view", "finance_merchants.export", "prepay.view", "prepay.export", "logs.view"}
	rid, actor := queryActor(t, s, admin, "scope-internal", "internal", "", grants)
	save := func(mode string, mids []string, enabled bool) {
		request(t, s, admin, "PATCH", "/api/access/roles/"+rid, map[string]any{"name": "scope-internal", "identity": "internal", "enabled": enabled, "permissions": grants, "dataScope": mode, "merchantIds": mids}, 200)
	}
	save("selected_merchants", []string{a}, true)
	checkOrders := func(path string, count int, want string) {
		t.Helper()
		got := request(t, s, actor, "GET", path, nil, 200)
		if got["total"] != float64(count) {
			t.Fatalf("wrong order count: %v", got)
		}
		if want != "" && got["items"].([]any)[0].(map[string]any)["systemId"] != want {
			t.Fatalf("wrong order: %v", got)
		}
	}
	checkOrders("/api/payments?page=1", 1, currentA)
	checkOrders("/api/payments?archive=true&page=1", 1, historicA)
	checkOrders("/api/payments?all=true&page=1", 2, "")
	request(t, s, actor, "GET", "/api/payments/"+currentA, nil, 200)
	for _, suffix := range []string{"", "/qrcode", "/checkout"} {
		request(t, s, actor, "GET", "/api/payments/"+currentB+suffix, nil, 404)
	}
	request(t, s, actor, "POST", "/api/payments/"+currentB+"/query", nil, 404)
	request(t, s, actor, "GET", "/api/merchants/"+b+"/api-key", nil, 404)
	request(t, s, actor, "PATCH", "/api/merchants/"+b, map[string]any{"name": "unauthorized", "enabled": true}, 404)
	dashboard := request(t, s, actor, "GET", "/api/dashboard", nil, 200)
	if dashboard["todayAmount"] != float64(100) || dashboard["merchantCount"] != float64(1) || dashboard["channelCount"] != float64(0) {
		t.Fatalf("home leaked other merchants: %v", dashboard)
	}
	if rows := dashboard["merchantRanking"].([]any); len(rows) != 1 || rows[0].(map[string]any)["id"] != a {
		t.Fatalf("ranking scope: %v", rows)
	}
	// Read array responses, including management records and accounting summaries.
	list := func(path string) []map[string]any {
		t.Helper()
		r := httptest.NewRequest("GET", path, nil)
		r.AddCookie(actor)
		w := httptest.NewRecorder()
		s.handler().ServeHTTP(w, r)
		if w.Code != 200 {
			t.Fatalf("%s: %d %s", path, w.Code, w.Body.String())
		}
		var rows []map[string]any
		if e := json.Unmarshal(w.Body.Bytes(), &rows); e != nil {
			t.Fatal(e)
		}
		return rows
	}
	for _, path := range []string{"/api/merchants", "/api/finance/merchants", "/api/finance/prepay"} {
		rows := list(path)
		if len(rows) == 0 {
			t.Fatalf("missing scoped rows at %s", path)
		}
		for _, row := range rows {
			if row["name"] != "Scope merchant A" {
				t.Fatalf("foreign data in %s: %v", path, row)
			}
		}
	}
	for _, path := range []string{"/api/products", "/api/channels"} {
		if rows := list(path); len(rows) != 0 {
			t.Fatalf("unassigned global config leaked: %s %v", path, rows)
		}
	}
	if rows := list("/api/records/complaints"); len(rows) != 1 || rows[0]["id"] != recordA {
		t.Fatalf("records scope: %v", rows)
	}
	for _, row := range list("/api/audit") {
		if row["target"] == b || row["target"] == currentB || row["target"] == recordB {
			t.Fatalf("audit scope: %v", row)
		}
	}
	request(t, s, actor, "PATCH", "/api/records/complaints/"+recordB, map[string]any{"name": "cross", "merchantId": b, "status": "待处理"}, 404)
	request(t, s, actor, "POST", "/api/records/complaints", map[string]any{"name": "cross", "merchantId": b, "status": "待处理"}, 403)
	request(t, s, actor, "PATCH", "/api/records/complaints/"+recordA, map[string]any{"name": "move", "merchantId": b, "status": "待处理"}, 400)
	for _, path := range []string{"/api/payments?all=true&export=csv", "/api/finance/merchants?export=csv", "/api/finance/prepay?export=csv"} {
		r := httptest.NewRequest("GET", path, nil)
		r.AddCookie(actor)
		w := httptest.NewRecorder()
		s.handler().ServeHTTP(w, r)
		if w.Code != 200 || bytes.Contains(w.Body.Bytes(), []byte("Scope merchant B")) || !bytes.Contains(w.Body.Bytes(), []byte("Scope merchant A")) {
			t.Fatalf("CSV scope: %s %d %s", path, w.Code, w.Body.String())
		}
	}
	// A broad role without order page permission must not widen order visibility.
	other := request(t, s, admin, "POST", "/api/access/roles", map[string]any{"name": "unrelated-all", "identity": "internal", "enabled": true, "permissions": []string{"products.view"}, "dataScope": "all"}, 200)["id"].(string)
	var uid string
	if e := s.db.QueryRow("SELECT id FROM users WHERE name='scope-internal'").Scan(&uid); e != nil {
		t.Fatal(e)
	}
	request(t, s, admin, "PATCH", "/api/users/"+uid, map[string]any{"name": "scope-internal", "identity": "internal", "enabled": true, "roleIds": []string{rid, other}}, 200)
	checkOrders("/api/payments?page=1", 1, currentA)
	// Scope union is per page; the second role can widen orders without widening home.
	request(t, s, admin, "PATCH", "/api/access/roles/"+other, map[string]any{"name": "unrelated-all", "identity": "internal", "enabled": true, "permissions": []string{"orders.view"}, "dataScope": "selected_merchants", "merchantIds": []string{b}}, 200)
	checkOrders("/api/payments?page=1", 2, "")
	if got := request(t, s, actor, "GET", "/api/dashboard", nil, 200); got["todayAmount"] != float64(100) {
		t.Fatalf("unrelated role widened home: %v", got)
	}
	request(t, s, admin, "PATCH", "/api/access/roles/"+other, map[string]any{"name": "unrelated-all", "identity": "internal", "enabled": false, "permissions": []string{"orders.view"}, "dataScope": "selected_merchants", "merchantIds": []string{b}}, 200)
	checkOrders("/api/payments?page=1", 1, currentA)
	save("selected_merchants", []string{b}, true)
	checkOrders("/api/payments?page=1", 1, currentB)
	request(t, s, actor, "GET", "/api/payments/"+currentA, nil, 404)
	save("all", nil, true)
	checkOrders("/api/payments?page=1", 2, "")
	if _, e := s.db.Exec("DELETE FROM role_data_scope WHERE role_id=?", rid); e != nil {
		t.Fatal(e)
	}
	checkOrders("/api/payments?page=1", 0, "")
}

func TestMerchantOwnScopeDoesNotDependOnCreator(t *testing.T) {
	s, c, _ := fixture(t)
	a := request(t, s, c, "POST", "/api/merchants", map[string]any{"name": "Tenant A", "prepaid": 0}, 201)["id"].(string)
	b := request(t, s, c, "POST", "/api/merchants", map[string]any{"name": "Tenant B", "prepaid": 0}, 201)["id"].(string)
	for _, mid := range []string{a, b} {
		request(t, s, c, "POST", "/api/payments", map[string]any{"merchantId": mid, "merchantOrder": "UPSTREAM-ASSIGNED", "amount": 100, "productCode": "PAY-ALI", "channelId": "CH-DEMO-01"}, 201)
	}
	grants := []string{"orders.view"}
	rid, first := queryActor(t, s, c, "tenant-first", "merchant", a, grants)
	_, second := queryActor(t, s, c, "tenant-second", "merchant", a, grants)
	check := func(cookie *http.Cookie) {
		got := request(t, s, cookie, "GET", "/api/payments?page=1", nil, 200)
		if got["total"] != float64(1) || got["items"].([]any)[0].(map[string]any)["merchantId"] != a {
			t.Fatalf("tenant scope: %v", got)
		}
	}
	check(first)
	check(second)
	for _, scope := range []map[string]any{{"dataScope": "all"}, {"dataScope": "selected_merchants", "merchantIds": []string{b}}, {"dataScope": "self"}} {
		body := map[string]any{"name": "tenant-first", "identity": "merchant", "enabled": true, "permissions": grants}
		for k, v := range scope {
			body[k] = v
		}
		request(t, s, c, "PATCH", "/api/access/roles/"+rid, body, 400)
	}
	// Even an invalid wide DB rule is intersected with the account merchant.
	if _, e := s.db.Exec("UPDATE role_data_scope SET mode='all' WHERE role_id=?", rid); e != nil {
		t.Fatal(e)
	}
	check(first)
	for _, scope := range []map[string]any{{"dataScope": "selected_merchants", "merchantIds": []string{}}, {"dataScope": "selected_merchants", "merchantIds": []string{"missing"}}, {"dataScope": "selected_merchants", "merchantIds": []string{a, a}}, {"dataScope": "own_merchant"}, {"dataScope": "all", "merchantIds": []string{a}}, {"dataScope": ""}} {
		body := map[string]any{"name": "invalid-scope", "identity": "internal", "enabled": true, "permissions": grants}
		for k, v := range scope {
			body[k] = v
		}
		request(t, s, c, "POST", "/api/access/roles", body, 400)
	}
}
