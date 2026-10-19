package main

import (
	"bytes"
	"encoding/json"
	"net/http/httptest"
	"testing"
)

func TestDashboardPanelPermissions(t *testing.T) {
	s, c, _ := fixture(t)
	m := request(t, s, c, "POST", "/api/merchants", map[string]any{"name": "Merchant A", "prepaid": 10}, 201)
	other := request(t, s, c, "POST", "/api/merchants", map[string]any{"name": "Merchant B", "prepaid": 10}, 201)
	// Merchant B has more orders, despite being created second.
	for i, mid := range []string{m["id"].(string), other["id"].(string), other["id"].(string)} {
		o := request(t, s, c, "POST", "/api/payments", map[string]any{"merchantId": mid, "merchantOrder": []string{"A-1", "B-1", "B-2"}[i], "productCode": "PAY-ALI", "channelId": "CH-DEMO-01", "amount": 100}, 201)
		request(t, s, c, "POST", "/api/payments/"+o["systemId"].(string)+"/simulate", map[string]string{"status": "已支付"}, 200)
	}
	all := request(t, s, c, "GET", "/api/dashboard", nil, 200)
	ranking := all["merchantRanking"].([]any)
	if ranking[0].(map[string]any)["id"] != other["id"] {
		t.Fatal("ranking not ordered by today's order count")
	}
	if _, ok := ranking[0].(map[string]any)["prepaid"]; ok {
		t.Fatal("ranking exposes merchant balance")
	}

	fields := map[string]string{"today_orders": "todayOrders", "today_success": "todaySuccess", "today_amount": "todayAmount", "success_rate": "successRate", "profit": "todayProfit", "month_amount": "monthAmount", "yesterday_orders": "yesterdayOrders", "yesterday_amount": "yesterdayAmount", "channel_count": "channelCount", "merchant_count": "merchantCount", "trend": "trend", "channels": "channels", "latest_orders": "latestOrders", "merchant_ranking": "merchantRanking"}
	// Each grant returns only its own panel, including valid zero values.
	for panel, key := range fields {
		t.Run(panel, func(t *testing.T) {
			u := User{Identity: "internal", Permissions: []string{"home.view", "home." + panel}}
			w := httptest.NewRecorder()
			if e := s.dashboard(w, httptest.NewRequest("GET", "/api/dashboard", nil), u); e != nil {
				t.Fatal(e)
			}
			var got map[string]any
			if e := json.Unmarshal(w.Body.Bytes(), &got); e != nil {
				t.Fatal(e)
			}
			if len(got) != 1 {
				t.Fatalf("ungranted data returned: %v", got)
			}
			if _, ok := got[key]; !ok {
				t.Fatalf("missing panel %s", key)
			}
		})
	}
	role := request(t, s, c, "POST", "/api/access/roles", map[string]any{"name": "Merchant dashboard", "identity": "merchant", "enabled": true, "permissions": []string{"home.view", "home.today_amount", "home.latest_orders"}}, 200)
	rid := role["id"].(string)
	request(t, s, c, "POST", "/api/users", map[string]any{"name": "merchant-home", "password": "test-only-password-789", "identity": "merchant", "merchantId": m["id"], "enabled": true, "roleIds": []string{rid}}, 200)
	login := httptest.NewRequest("POST", "/api/auth/login", bytes.NewBufferString(`{"name":"merchant-home","password":"test-only-password-789"}`))
	w := httptest.NewRecorder()
	s.handler().ServeHTTP(w, login)
	if w.Code != 200 {
		t.Fatal(w.Body.String())
	}
	mc := w.Result().Cookies()[0]
	got := request(t, s, mc, "GET", "/api/dashboard", nil, 200)
	if len(got) != 2 || got["todayAmount"] != float64(100) {
		t.Fatalf("merchant panels or totals leaked: %v", got)
	}
	orders := got["latestOrders"].([]any)
	if len(orders) != 1 || orders[0].(map[string]any)["merchantId"] != m["id"] {
		t.Fatalf("cross-merchant orders leaked: %v", orders)
	}
	request(t, s, mc, "GET", "/api/merchants", nil, 403)
	request(t, s, mc, "GET", "/api/payments", nil, 403)
	request(t, s, mc, "GET", "/api/channels", nil, 403)
	// A merchant role cannot be granted the platform ranking, even via a crafted request.
	request(t, s, c, "PATCH", "/api/access/roles/"+rid, map[string]any{"name": "Merchant dashboard", "identity": "merchant", "enabled": true, "permissions": []string{"home.view", "home.merchant_ranking"}}, 400)
	// Even an unexpected database grant must not expose an internal panel.
	if _, e := s.db.Exec("INSERT INTO role_permissions VALUES(?,?)", rid, "home.merchant_ranking"); e != nil {
		t.Fatal(e)
	}
	got = request(t, s, mc, "GET", "/api/dashboard", nil, 200)
	if _, ok := got["merchantRanking"]; ok {
		t.Fatal("merchant ranking leaked")
	}
	// Revocation applies to the same session on its very next request.
	request(t, s, c, "PATCH", "/api/access/roles/"+rid, map[string]any{"name": "Merchant dashboard", "identity": "merchant", "enabled": true, "permissions": []string{"home.view"}}, 200)
	got = request(t, s, mc, "GET", "/api/dashboard", nil, 200)
	if len(got) != 0 {
		t.Fatalf("revoked panels returned: %v", got)
	}
	request(t, s, c, "PATCH", "/api/access/roles/"+rid, map[string]any{"name": "Merchant dashboard", "identity": "merchant", "enabled": true, "permissions": []string{}}, 200)
	request(t, s, mc, "GET", "/api/dashboard", nil, 403)
	request(t, s, c, "POST", "/api/access/roles", map[string]any{"name": "Missing home access", "identity": "internal", "enabled": true, "permissions": []string{"home.trend"}}, 400)
}
