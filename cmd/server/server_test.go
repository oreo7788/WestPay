package main

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"sync"
	"testing"
)

func request(t *testing.T, s *Store, cookie *http.Cookie, method, path string, body any, want int) map[string]any {
	t.Helper()
	b, _ := json.Marshal(body)
	r := httptest.NewRequest(method, path, bytes.NewReader(b))
	if cookie != nil {
		r.AddCookie(cookie)
	}
	w := httptest.NewRecorder()
	s.handler().ServeHTTP(w, r)
	if w.Code != want {
		t.Fatalf("%s %s: got %d want %d: %s", method, path, w.Code, want, w.Body.String())
	}
	var out map[string]any
	_ = json.Unmarshal(w.Body.Bytes(), &out)
	return out
}
func fixture(t *testing.T) (*Store, *http.Cookie, string) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "west.db")
	s, e := openStore(path)
	if e != nil {
		t.Fatal(e)
	}
	t.Cleanup(func() { s.db.Close() })
	r := httptest.NewRequest("POST", "/api/auth/setup", bytes.NewBufferString(`{"name":"test-admin","password":"test-only-password-123"}`))
	w := httptest.NewRecorder()
	s.handler().ServeHTTP(w, r)
	if w.Code != 201 {
		t.Fatal(w.Body.String())
	}
	cookies := w.Result().Cookies()
	if len(cookies) == 0 || !cookies[0].HttpOnly {
		t.Fatal("missing secure session cookie")
	}
	return s, cookies[0], path
}
func TestPrepayTransactionReplayAndPersistence(t *testing.T) {
	s, c, path := fixture(t)
	request(t, s, nil, "POST", "/api/merchants", map[string]any{"name": "unauth", "prepaid": 0}, 401)
	m := request(t, s, c, "POST", "/api/merchants", map[string]any{"name": "Merchant A", "prepaid": 10}, 201)
	mid := m["id"].(string)
	body := map[string]any{"amount": 5.25, "idempotencyKey": "replay-key-001", "note": "topup"}
	request(t, s, c, "POST", "/api/merchants/"+mid+"/prepay", body, 200)
	request(t, s, c, "POST", "/api/merchants/"+mid+"/prepay", body, 200)
	request(t, s, c, "POST", "/api/merchants/"+mid+"/prepay", map[string]any{"amount": -20, "idempotencyKey": "negative-key-001", "note": "invalid"}, 409)
	var balance, count int
	if e := s.db.QueryRow("SELECT balance FROM merchants WHERE id=?", mid).Scan(&balance); e != nil {
		t.Fatal(e)
	}
	_ = s.db.QueryRow("SELECT COUNT(*) FROM ledger WHERE merchant_id=?", mid).Scan(&count)
	if balance != 1525 || count != 2 {
		t.Fatalf("balance=%d ledger=%d", balance, count)
	}
	s.db.Close()
	reopened, e := openStore(path)
	if e != nil {
		t.Fatal(e)
	}
	defer reopened.db.Close()
	if e = reopened.db.QueryRow("SELECT balance FROM merchants WHERE id=?", mid).Scan(&balance); e != nil || balance != 1525 {
		t.Fatalf("persistence failed %d %v", balance, e)
	}
}
func TestConcurrentSettlementDebitsOnce(t *testing.T) {
	s, c, _ := fixture(t)
	m := request(t, s, c, "POST", "/api/merchants", map[string]any{"name": "Merchant A", "prepaid": 10}, 201)
	mid := m["id"].(string)
	body := map[string]any{"merchantId": mid, "merchantOrder": "MO-001", "productCode": "PAY-ALI", "channelId": "CH-DEMO-01", "amount": 100}
	order := request(t, s, c, "POST", "/api/payments", body, 201)
	oid := order["systemId"].(string)
	request(t, s, c, "PATCH", "/api/products/PAY-ALI", map[string]any{"code": "PAY-ALI", "name": "支付宝", "method": "支付宝", "feeRate": 2, "enabled": true}, 200)
	request(t, s, c, "POST", "/api/payments", body, 409)
	var wg sync.WaitGroup
	for i := 0; i < 6; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			request(t, s, c, "POST", "/api/payments/"+oid+"/simulate", map[string]string{"status": "已支付"}, 200)
		}()
	}
	wg.Wait()
	var balance, count int
	_ = s.db.QueryRow("SELECT balance FROM merchants WHERE id=?", mid).Scan(&balance)
	_ = s.db.QueryRow("SELECT COUNT(*) FROM ledger WHERE reference=?", oid).Scan(&count)
	if balance != 900 || count != 1 {
		t.Fatalf("double debit: balance=%d ledger=%d", balance, count)
	}
	d := request(t, s, c, "GET", "/api/dashboard", nil, 200)
	if d["todayAmount"] != float64(100) || d["todayProfit"] != float64(0) {
		t.Fatalf("report mismatch: %v", d)
	}
	request(t, s, c, "POST", "/api/payments/"+oid+"/simulate", map[string]string{"status": "支付失败"}, 409)
}
func TestPermissionsAndOrigin(t *testing.T) {
	s, c, _ := fixture(t)
	request(t, s, c, "POST", "/api/users", map[string]any{"name": "viewer", "password": "test-only-password-456", "role": "viewer"}, 201)
	r := httptest.NewRequest("POST", "/api/auth/login", bytes.NewBufferString(`{"name":"viewer","password":"test-only-password-456"}`))
	w := httptest.NewRecorder()
	s.handler().ServeHTTP(w, r)
	if w.Code != 200 {
		t.Fatal(w.Body.String())
	}
	viewer := w.Result().Cookies()[0]
	request(t, s, viewer, "POST", "/api/merchants", map[string]any{"name": "blocked", "prepaid": 0}, 403)
	request(t, s, c, "PATCH", "/api/settings", map[string]string{"minAmount": "3000", "maxAmount": "2000"}, 400)
	r = httptest.NewRequest("POST", "/api/merchants", bytes.NewBufferString(`{"name":"blocked","prepaid":0}`))
	r.Header.Set("Origin", "https://other.example")
	r.AddCookie(c)
	w = httptest.NewRecorder()
	s.handler().ServeHTTP(w, r)
	if w.Code != 403 {
		t.Fatal("foreign-origin write allowed")
	}
}
func TestConfigurationBlacklistAndTimeout(t *testing.T) {
	s, c, _ := fixture(t)
	m := request(t, s, c, "POST", "/api/merchants", map[string]any{"name": "Config merchant", "prepaid": 5}, 201)
	mid := m["id"].(string)
	request(t, s, c, "PATCH", "/api/products/PAY-ALI", map[string]any{"code": "PAY-ALI", "name": "自定义支付宝产品", "method": "支付宝", "feeRate": 2, "enabled": true}, 200)
	body := map[string]any{"merchantId": mid, "merchantOrder": "CONFIG-001", "productCode": "PAY-ALI", "channelId": "CH-DEMO-01", "amount": 100}
	request(t, s, c, "PATCH", "/api/settings", map[string]string{"maxAmount": "10"}, 200)
	request(t, s, c, "POST", "/api/payments", body, 400)
	request(t, s, c, "PATCH", "/api/settings", map[string]string{"maxAmount": "2000"}, 200)
	blocked := request(t, s, c, "POST", "/api/records/blacklist", map[string]any{"name": mid, "reference": "", "amount": 0, "status": "生效", "note": "test"}, 200)
	request(t, s, c, "POST", "/api/payments", body, 403)
	request(t, s, c, "PATCH", "/api/records/blacklist/"+blocked["id"].(string), map[string]any{"name": mid, "reference": "", "amount": 0, "status": "停用", "note": "test"}, 200)
	o := request(t, s, c, "POST", "/api/payments", body, 201)
	oid := o["systemId"].(string)
	request(t, s, c, "POST", "/api/payments/"+oid+"/simulate", map[string]string{"status": "已支付"}, 200)
	d := request(t, s, c, "GET", "/api/dashboard", nil, 200)
	if d["todayProfit"] != float64(1) {
		t.Fatalf("profit must deduct channel cost: %v", d)
	}
	body["merchantOrder"] = "EXPIRE-002"
	expired := request(t, s, c, "POST", "/api/payments", body, 201)
	eid := expired["systemId"].(string)
	if _, e := s.db.Exec("UPDATE orders SET created_at='2020-01-01 00:00:00' WHERE id=?", eid); e != nil {
		t.Fatal(e)
	}
	request(t, s, c, "POST", "/api/payments/"+eid+"/simulate", map[string]string{"status": "已支付"}, 409)
	page := request(t, s, c, "GET", "/api/payments?all=true&page=2&pageSize=1", nil, 200)
	if page["total"] != float64(2) || len(page["items"].([]any)) != 1 {
		t.Fatalf("pagination mismatch: %v", page)
	}
	var status string
	_ = s.db.QueryRow("SELECT status FROM orders WHERE id=?", eid).Scan(&status)
	if status != "已关闭" {
		t.Fatalf("expiry status %s", status)
	}
	request(t, s, c, "POST", "/api/records/complaints", map[string]any{"name": "Test complaint", "reference": oid, "amount": 100, "status": "待处理", "note": "local QA"}, 200)
	r := httptest.NewRequest("GET", "/api/finance/prepay?export=csv", nil)
	r.AddCookie(c)
	w := httptest.NewRecorder()
	s.handler().ServeHTTP(w, r)
	if w.Code != 200 || w.Header().Get("Content-Type") != "text/csv; charset=utf-8" {
		t.Fatalf("CSV export failed %s", w.Body.String())
	}
}
