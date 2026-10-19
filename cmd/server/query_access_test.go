package main

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"
)

func queryActor(t *testing.T, s *Store, admin *http.Cookie, name, identity, mid string, permissions []string) (string, *http.Cookie) {
	t.Helper()
	role := request(t, s, admin, "POST", "/api/access/roles", map[string]any{"name": name, "identity": identity, "enabled": true, "permissions": permissions}, 200)
	rid := role["id"].(string)
	request(t, s, admin, "POST", "/api/users", map[string]any{"name": name, "identity": identity, "merchantId": mid, "enabled": true, "password": "query-test-password-123", "roleIds": []string{rid}}, 200)
	body, _ := json.Marshal(map[string]string{"name": name, "password": "query-test-password-123"})
	r := httptest.NewRequest("POST", "/api/auth/login", bytes.NewReader(body))
	w := httptest.NewRecorder()
	s.handler().ServeHTTP(w, r)
	if w.Code != 200 {
		t.Fatal(w.Body.String())
	}
	return rid, w.Result().Cookies()[0]
}

func TestQueryPermissionsAcrossPages(t *testing.T) {
	s, c, _ := fixture(t)
	pages := map[string]string{
		"orders": "/api/payments?", "history": "/api/payments?archive=true&",
		"merchants": "/api/merchants?", "products": "/api/products?", "channels": "/api/channels?",
		"merchant_settlement": "/api/finance/merchants?reportPage=merchant_settlement&",
		"finance_channels":    "/api/finance/channels?", "finance_products": "/api/finance/products?",
		"finance_merchants": "/api/finance/merchants?", "prepay": "/api/finance/prepay?", "statistics": "/api/finance/statistics?",
		"receivers": "/api/records/receivers?", "complaints": "/api/records/complaints?", "official": "/api/records/official?",
		"blacklist": "/api/records/blacklist?", "transfers": "/api/records/transfers?", "user_ids": "/api/records/user_ids?",
	}
	grants := []string{}
	for page := range pages {
		grants = append(grants, page+".view")
	}
	grants = append(grants, "orders.export", "history.export", "finance_merchants.export", "merchant_settlement.export")
	rid, actor := queryActor(t, s, c, "query-internal", "internal", "", grants)
	for page, path := range pages {
		for _, f := range queryFilters(page) {
			t.Run(page+"/"+f.Key, func(t *testing.T) { request(t, s, actor, "GET", path+f.Param+"=test", nil, 403) })
		}
	}
	request(t, s, actor, "GET", "/api/payments?export=csv&systemId=test", nil, 403)
	request(t, s, actor, "GET", "/api/payments?q=test", nil, 400)
	request(t, s, actor, "GET", "/api/payments?systemId=test&systemId=other", nil, 400)
	request(t, s, actor, "GET", "/api/payments?system_id=test", nil, 400)
	// Explicitly grant one query. No current/history, report alias or field fallback.
	grants = append(grants, "orders.filter_system_id", "finance_merchants.filter_from")
	request(t, s, c, "PATCH", "/api/access/roles/"+rid, map[string]any{"name": "query-internal", "identity": "internal", "enabled": true, "permissions": grants}, 200)
	request(t, s, actor, "GET", "/api/payments?systemId=test&page=1", nil, 200)
	request(t, s, actor, "GET", "/api/payments?archive=true&systemId=test", nil, 403)
	request(t, s, actor, "GET", "/api/payments?all=true&systemId=test", nil, 403)
	request(t, s, actor, "GET", "/api/payments?systemId=test&status=已支付", nil, 403)
	request(t, s, actor, "GET", "/api/finance/merchants?from=2026-01-01", nil, 200)
	request(t, s, actor, "GET", "/api/finance/merchants?reportPage=merchant_settlement&from=2026-01-01", nil, 403)
	request(t, s, actor, "GET", "/api/finance/merchants?reportPage=statistics", nil, 400)
	request(t, s, actor, "GET", "/api/finance/merchants?reportPage=merchant_settlement&export=csv&from=2026-01-01", nil, 403)
	// Revoke in place; the same session is immediately denied.
	request(t, s, c, "PATCH", "/api/access/roles/"+rid, map[string]any{"name": "query-internal", "identity": "internal", "enabled": true, "permissions": []string{"orders.view"}}, 200)
	request(t, s, actor, "GET", "/api/payments?systemId=test", nil, 403)
}

func TestSeparateOrderFiltersAndMerchantIsolation(t *testing.T) {
	s, c, _ := fixture(t)
	a := request(t, s, c, "POST", "/api/merchants", map[string]any{"name": "Alpha merchant", "prepaid": 0}, 201)
	b := request(t, s, c, "POST", "/api/merchants", map[string]any{"name": "Beta merchant", "prepaid": 0}, 201)
	create := func(mid, number string) map[string]any {
		return request(t, s, c, "POST", "/api/payments", map[string]any{"merchantId": mid, "merchantOrder": number, "productCode": "PAY-ALI", "channelId": "CH-DEMO-01", "amount": 100}, 201)
	}
	first := create(a["id"].(string), "ORDER-A")
	second := create(b["id"].(string), first["systemId"].(string))
	check := func(cookie *http.Cookie, query string, want int, id string) {
		t.Helper()
		got := request(t, s, cookie, "GET", "/api/payments?page=1&"+query, nil, 200)
		if got["total"] != float64(want) {
			t.Fatalf("filter %s: %v", query, got)
		}
		if want == 1 && got["items"].([]any)[0].(map[string]any)["systemId"] != id {
			t.Fatalf("wrong match for %s: %v", query, got)
		}
	}
	check(c, "systemId="+first["systemId"].(string), 1, first["systemId"].(string))
	check(c, "merchantOrder="+first["systemId"].(string), 1, second["systemId"].(string))
	check(c, "merchant="+url.QueryEscape("Alpha merchant"), 1, first["systemId"].(string))
	check(c, "systemId="+first["systemId"].(string)+"&merchantOrder=ORDER-A&merchant="+a["id"].(string), 1, first["systemId"].(string))
	check(c, "systemId="+first["systemId"].(string)+"&merchantOrder="+first["systemId"].(string), 0, "")
	check(c, "systemId=%25", 0, "")
	grants := []string{"orders.view", "orders.export", "orders.filter_system_id", "orders.filter_merchant_order"}
	rid, merchant := queryActor(t, s, c, "query-merchant", "merchant", a["id"].(string), grants)
	check(merchant, "systemId="+second["systemId"].(string), 0, "")
	check(merchant, "merchantOrder="+first["systemId"].(string), 0, "")
	request(t, s, merchant, "GET", "/api/payments?merchantId="+b["id"].(string), nil, 403)
	request(t, s, merchant, "GET", "/api/payments?merchant=Beta", nil, 403)
	request(t, s, merchant, "GET", "/api/payments?export=csv&method=支付宝", nil, 403)
	request(t, s, c, "PATCH", "/api/access/roles/"+rid, map[string]any{"name": "query-merchant", "identity": "merchant", "enabled": true, "permissions": append(grants, "orders.filter_merchant", "orders.filter_merchant_id")}, 200)
	check(merchant, "merchant=Beta", 0, "")
	check(merchant, "merchantId="+b["id"].(string), 0, "")
	// Field permissions also apply to CSV, while allowed queries work normally.
	r := httptest.NewRequest("GET", "/api/payments?export=csv&systemId="+first["systemId"].(string), nil)
	r.AddCookie(merchant)
	w := httptest.NewRecorder()
	s.handler().ServeHTTP(w, r)
	if w.Code != 200 || !bytes.Contains(w.Body.Bytes(), []byte("ORDER-A")) || bytes.Contains(w.Body.Bytes(), []byte("Beta merchant")) {
		t.Fatalf("CSV query/isolation failure: %d %s", w.Code, w.Body.String())
	}
}
