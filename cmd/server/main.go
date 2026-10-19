package main

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"math"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	_ "github.com/mattn/go-sqlite3"
	"golang.org/x/crypto/bcrypt"
)

type User struct {
	ID          string   `json:"id"`
	Name        string   `json:"name"`
	Role        string   `json:"role"`
	Identity    string   `json:"identity"`
	MerchantID  string   `json:"merchantId"`
	Enabled     bool     `json:"enabled"`
	Super       bool     `json:"super"`
	RoleIDs     []string `json:"roleIds"`
	Permissions []string `json:"permissions"`
}
type Store struct {
	db       *sql.DB
	wechat   *WechatGateway
	wxLocks  [64]sync.Mutex
	loginMu  sync.Mutex
	attempts map[string]attempt
}
type attempt struct {
	Count int
	Start time.Time
}
type apiError struct {
	Status  int
	Message string
}

func (e apiError) Error() string { return e.Message }
func bad(s string) error         { return apiError{400, s} }
func notfound(s string) error    { return apiError{404, s} }
func conflict(s string) error    { return apiError{409, s} }
func id(prefix string) string {
	b := make([]byte, 12)
	if _, err := rand.Read(b); err != nil {
		panic(err)
	}
	return prefix + hex.EncodeToString(b)
}
func timestamp() string { return time.Now().Format("2006-01-02 15:04:05") }
func cents(v float64) (int64, error) {
	if math.IsNaN(v) || math.IsInf(v, 0) || math.Abs(v) > 100000000 {
		return 0, bad("金额超出范围")
	}
	n := math.Round(v * 100)
	if math.Abs(v*100-n) > 0.00001 {
		return 0, bad("金额最多支持两位小数")
	}
	return int64(n), nil
}
func bps(v float64) (int64, error) {
	if v < 0 || v > 100 || math.IsNaN(v) || math.IsInf(v, 0) {
		return 0, bad("费率应在 0 至 100% 之间")
	}
	return cents(v)
}
func writeJSON(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(value)
}
func fail(w http.ResponseWriter, err error) {
	var e apiError
	if errors.As(err, &e) {
		writeJSON(w, e.Status, map[string]string{"error": e.Message})
		return
	}
	log.Printf("request error: %v", err)
	writeJSON(w, 500, map[string]string{"error": "保存失败，请稍后重试"})
}
func decode(w http.ResponseWriter, r *http.Request, v any) error {
	d := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20))
	d.DisallowUnknownFields()
	if err := d.Decode(v); err != nil {
		return bad("提交内容格式无效")
	}
	if err := d.Decode(&struct{}{}); err != io.EOF {
		return bad("请求中只能包含一个 JSON 对象")
	}
	return nil
}
func envOr(k, d string) string {
	if s := os.Getenv(k); s != "" {
		return s
	}
	return d
}

func openStore(path string) (*Store, error) {
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		return nil, err
	}
	db, err := sql.Open("sqlite3", path+"?_foreign_keys=on&_busy_timeout=5000&_journal_mode=WAL&_txlock=immediate")
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(1)
	if _, err = db.Exec(schema + wechatSchema); err != nil {
		db.Close()
		return nil, err
	}
	s := &Store{db: db, attempts: map[string]attempt{}}
	if err = s.migrateAccess(path); err != nil {
		db.Close()
		return nil, err
	}
	if err = s.seed(); err != nil {
		db.Close()
		return nil, err
	}
	_ = os.Chmod(path, 0600)
	return s, nil
}

const schema = `
CREATE TABLE IF NOT EXISTS users(id TEXT PRIMARY KEY,name TEXT NOT NULL UNIQUE,password_hash TEXT NOT NULL,role TEXT NOT NULL CHECK(role IN ('admin','operator','viewer')));
CREATE TABLE IF NOT EXISTS sessions(token_hash TEXT PRIMARY KEY,user_id TEXT NOT NULL REFERENCES users(id),expires INTEGER NOT NULL);
CREATE TABLE IF NOT EXISTS merchants(id TEXT PRIMARY KEY,name TEXT NOT NULL,balance INTEGER NOT NULL CHECK(balance>=0),total_prepay INTEGER NOT NULL DEFAULT 0,enabled INTEGER NOT NULL,created_at TEXT NOT NULL);
CREATE TABLE IF NOT EXISTS products(code TEXT PRIMARY KEY,name TEXT NOT NULL,fee_bps INTEGER NOT NULL CHECK(fee_bps BETWEEN 0 AND 10000),method TEXT NOT NULL,enabled INTEGER NOT NULL);
CREATE TABLE IF NOT EXISTS channels(id TEXT PRIMARY KEY,name TEXT NOT NULL,fee_bps INTEGER NOT NULL,min_cents INTEGER NOT NULL,max_cents INTEGER NOT NULL,method TEXT NOT NULL,plugin TEXT NOT NULL,enabled INTEGER NOT NULL);
CREATE TABLE IF NOT EXISTS orders(id TEXT PRIMARY KEY,merchant_order TEXT NOT NULL,merchant_id TEXT NOT NULL REFERENCES merchants(id),product_code TEXT NOT NULL REFERENCES products(code),channel_id TEXT NOT NULL REFERENCES channels(id),amount INTEGER NOT NULL CHECK(amount>0),fee INTEGER NOT NULL CHECK(fee>=0),cost INTEGER NOT NULL CHECK(cost>=0),status TEXT NOT NULL,created_at TEXT NOT NULL,callback_at TEXT NOT NULL DEFAULT '',provider_txn TEXT NOT NULL DEFAULT '',query_count INTEGER NOT NULL DEFAULT 0,UNIQUE(merchant_id,merchant_order));
CREATE INDEX IF NOT EXISTS orders_date ON orders(created_at);
CREATE TABLE IF NOT EXISTS ledger(id TEXT PRIMARY KEY,merchant_id TEXT NOT NULL REFERENCES merchants(id),delta INTEGER NOT NULL,before_balance INTEGER NOT NULL,after_balance INTEGER NOT NULL,kind TEXT NOT NULL,reference TEXT NOT NULL UNIQUE,note TEXT NOT NULL,actor TEXT NOT NULL,created_at TEXT NOT NULL);
CREATE TABLE IF NOT EXISTS audit(id INTEGER PRIMARY KEY AUTOINCREMENT,actor TEXT NOT NULL,action TEXT NOT NULL,target TEXT NOT NULL,detail TEXT NOT NULL,ip TEXT NOT NULL,created_at TEXT NOT NULL);
CREATE TABLE IF NOT EXISTS records(id TEXT PRIMARY KEY,kind TEXT NOT NULL,name TEXT NOT NULL,reference TEXT NOT NULL DEFAULT '',amount INTEGER NOT NULL DEFAULT 0,status TEXT NOT NULL,note TEXT NOT NULL,created_at TEXT NOT NULL,updated_at TEXT NOT NULL);
CREATE TABLE IF NOT EXISTS settings(key TEXT PRIMARY KEY,value TEXT NOT NULL);
`

func (s *Store) seed() error {
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	for _, p := range []Product{{Code: "PAY-ALI", Name: "支付宝", Method: "支付宝", FeeRate: 1, Enabled: true}, {Code: "PAY-WX", Name: "微信", Method: "微信支付", FeeRate: 1, Enabled: true}, {Code: "PAY-UNION", Name: "云闪付", Method: "网银支付", FeeRate: 1, Enabled: true}} {
		if _, err = tx.Exec("INSERT OR IGNORE INTO products VALUES(?,?,?,?,?)", p.Code, p.Name, int64(p.FeeRate*100), p.Method, p.Enabled); err != nil {
			return err
		}
	}
	for i, m := range []string{"支付宝", "微信支付", "网银支付"} {
		if _, err = tx.Exec("INSERT OR IGNORE INTO channels VALUES(?,?,?,?,?,?,?,?)", fmt.Sprintf("CH-DEMO-%02d", i+1), "本地模拟通道-"+m, 100, 100, 200000, m, "demo", true); err != nil {
			return err
		}
	}
	for k, v := range map[string]string{"siteName": "支付通道管理平台", "minAmount": "1.00", "maxAmount": "2000.00", "orderTimeoutMinutes": "30", "supportContact": "", "notificationEnabled": "false"} {
		if _, err = tx.Exec("INSERT OR IGNORE INTO settings VALUES(?,?)", k, v); err != nil {
			return err
		}
	}
	return tx.Commit()
}
func (s *Store) audit(tx *sql.Tx, u User, r *http.Request, action, target, detail string) error {
	ip, _, _ := net.SplitHostPort(r.RemoteAddr)
	_, err := tx.Exec("INSERT INTO audit(actor,action,target,detail,ip,created_at) VALUES(?,?,?,?,?,?)", u.Name, action, target, detail, ip, timestamp())
	return err
}
func (s *Store) current(r *http.Request) (User, error) {
	c, e := r.Cookie("west_session")
	if e != nil {
		return User{}, apiError{401, "请先登录"}
	}
	sum := sha256.Sum256([]byte(c.Value))
	var u User
	e = s.db.QueryRow("SELECT u.id,u.name,u.role FROM sessions s JOIN users u ON s.user_id=u.id WHERE s.token_hash=? AND s.expires>?", hex.EncodeToString(sum[:]), time.Now().Unix()).Scan(&u.ID, &u.Name, &u.Role)
	if e != nil {
		return u, apiError{401, "登录已失效，请重新登录"}
	}
	if e = s.loadAccess(&u); e != nil {
		return User{}, e
	}
	return u, nil
}
func (s *Store) protect(fn func(http.ResponseWriter, *http.Request, User) error) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		u, e := s.current(r)
		if e != nil {
			fail(w, e)
			return
		}
		if e = s.authorize(r, u); e != nil {
			fail(w, e)
			return
		}

		if e = fn(w, r, u); e != nil {
			fail(w, e)
		}
	}
}
func (s *Store) handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, r *http.Request) {
		if e := s.db.Ping(); e != nil {
			fail(w, e)
			return
		}
		writeJSON(w, 200, map[string]string{"status": "ok"})
	})
	mux.HandleFunc("GET /pay/{token}", s.cashierPage)
	mux.HandleFunc("GET /api/cashier/{token}", s.cashierInfo)
	mux.HandleFunc("GET /api/cashier/{token}/qr.png", s.cashierQR)
	mux.HandleFunc("GET /api/cashier/{token}/oauth/start", s.oauthStart)
	mux.HandleFunc("POST /api/cashier/{token}/prepay", s.cashierPrepay)
	mux.HandleFunc("POST /api/cashier/{token}/refresh", s.cashierRefresh)
	mux.HandleFunc("GET /api/wechat/oauth/callback", s.oauthCallback)
	mux.HandleFunc("POST /api/wechat/notify", s.wechatNotify)
	mux.HandleFunc("POST /api/v1/orders", s.businessCreateOrder)
	mux.HandleFunc("GET /api/v1/orders/{merchantOrder}", s.businessGetOrder)
	mux.HandleFunc("POST /api/v1/orders/{merchantOrder}/query", s.businessGetOrder)
	mux.HandleFunc("GET /api/auth/status", s.authStatus)
	mux.HandleFunc("POST /api/auth/setup", s.setup)
	mux.HandleFunc("POST /api/auth/login", s.login)
	mux.HandleFunc("POST /api/auth/logout", s.logout)
	routes := map[string]func(http.ResponseWriter, *http.Request, User) error{
		"GET /api/payments/{id}":                  s.getPayment,
		"GET /api/payments/{id}/qrcode":           s.orderQRCode,
		"GET /api/merchants/{id}/api-key":         s.merchantAPIStatus,
		"POST /api/merchants/{id}/api-key":        s.saveMerchantAPIKey,
		"POST /api/merchants/{id}/api-key/revoke": s.saveMerchantAPIKey,
		"GET /api/integrations/wechat":            s.wechatStatus,
		"POST /api/payments/{id}/checkout":        s.checkout,
		"GET /api/dashboard":                      s.dashboard, "GET /api/payments": s.listPayments, "POST /api/payments": s.createPayment, "POST /api/payments/{id}/query": s.queryPayment, "POST /api/payments/{id}/simulate": s.simulatePayment,
		"GET /api/merchants": s.listMerchants, "POST /api/merchants": s.createMerchant, "PATCH /api/merchants/{id}": s.editMerchant, "POST /api/merchants/{id}/prepay": s.adjustPrepay, "POST /api/merchants/{id}/toggle": s.toggleMerchant,
		"GET /api/products": s.listProducts, "POST /api/products": s.saveProduct, "PATCH /api/products/{id}": s.saveProduct,
		"GET /api/channels": s.listChannels, "POST /api/channels": s.saveChannel, "PATCH /api/channels/{id}": s.saveChannel, "POST /api/channels/{id}/toggle": s.toggleChannel,
		"GET /api/finance/{kind}": s.finance, "GET /api/settings": s.getSettings, "PATCH /api/settings": s.saveSettings, "GET /api/audit": s.listAudit,
		"GET /api/records/{kind}": s.listRecords, "POST /api/records/{kind}": s.saveRecord, "PATCH /api/records/{kind}/{id}": s.saveRecord,
		"GET /api/users": s.listAccessUsers, "POST /api/users": s.saveAccessUser,
		"PATCH /api/users/{id}": s.saveAccessUser, "POST /api/users/{id}/password": s.resetUserPassword,
		"GET /api/access/permissions": s.accessCatalog, "GET /api/access/roles": s.listRoles,
		"POST /api/access/roles": s.saveRole, "PATCH /api/access/roles/{id}": s.saveRole,
		"POST /api/auth/password": s.changeOwnPassword,
	}
	for pattern, fn := range routes {
		mux.HandleFunc(pattern, s.protect(fn))
	}
	mux.HandleFunc("/api/", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, 404, map[string]string{"error": "接口不存在"})
	})
	mux.Handle("/", http.FileServer(http.Dir(envOr("WEB_ROOT", "frontend/dist"))))
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("Referrer-Policy", "same-origin")
		w.Header().Set("Cache-Control", "no-store")
		if r.Method != "GET" && r.Method != "HEAD" {
			origin := r.Header.Get("Origin")
			if origin != "" {
				u, e := url.Parse(origin)
				if e != nil || u.Host != r.Host {
					fail(w, apiError{403, "请求来源不允许"})
					return
				}
			}
		}
		mux.ServeHTTP(w, r)
	})
}
func (s *Store) authStatus(w http.ResponseWriter, r *http.Request) {
	var count int
	if err := s.db.QueryRow("SELECT COUNT(*) FROM users").Scan(&count); err != nil {
		fail(w, err)
		return
	}
	u, e := s.current(r)
	if e != nil {
		u = User{Permissions: []string{}, RoleIDs: []string{}}
	}
	writeJSON(w, 200, map[string]any{"needsSetup": count == 0, "authenticated": e == nil, "user": u})
}
func (s *Store) setSession(w http.ResponseWriter, r *http.Request, u User) error {
	token := id("")
	sum := sha256.Sum256([]byte(token))
	_, err := s.db.Exec("INSERT INTO sessions VALUES(?,?,?)", hex.EncodeToString(sum[:]), u.ID, time.Now().Add(12*time.Hour).Unix())
	if err != nil {
		return err
	}
	http.SetCookie(w, &http.Cookie{Name: "west_session", Value: token, Path: "/", HttpOnly: true, SameSite: http.SameSiteStrictMode, Secure: r.TLS != nil, MaxAge: 43200})
	return nil
}
func credentials(w http.ResponseWriter, r *http.Request) (string, string, error) {
	var b struct {
		Name     string `json:"name"`
		Password string `json:"password"`
	}
	if e := decode(w, r, &b); e != nil {
		return "", "", e
	}
	b.Name = strings.TrimSpace(b.Name)
	if len(b.Name) < 2 || len(b.Name) > 40 || len(b.Password) < 10 || len(b.Password) > 72 {
		return "", "", bad("账号须为 2–40 个字符，密码须为 10–72 个字符")
	}
	return b.Name, b.Password, nil
}
func (s *Store) setup(w http.ResponseWriter, r *http.Request) {
	name, p, e := credentials(w, r)
	if e != nil {
		fail(w, e)
		return
	}
	hash, e := bcrypt.GenerateFromPassword([]byte(p), bcrypt.DefaultCost)
	if e != nil {
		fail(w, e)
		return
	}
	tx, e := s.db.Begin()
	if e != nil {
		fail(w, e)
		return
	}
	defer tx.Rollback()
	var n int
	if e = tx.QueryRow("SELECT COUNT(*) FROM users").Scan(&n); e != nil {
		fail(w, e)
		return
	}
	if n > 0 {
		fail(w, conflict("管理员已创建，请登录"))
		return
	}
	u := User{ID: id("U-"), Name: name, Role: "admin", Identity: "internal", Enabled: true, Super: true, Permissions: []string{}, RoleIDs: []string{}}
	if _, e = tx.Exec("INSERT INTO users VALUES(?,?,?,?)", u.ID, u.Name, string(hash), u.Role); e == nil {
		e = s.audit(tx, u, r, "创建管理员", u.ID, "")
	}
	if e == nil {
		_, e = tx.Exec("INSERT INTO user_access VALUES(?,'internal',NULL,1,1)", u.ID)
	}
	if e == nil {
		e = tx.Commit()
	}
	if e != nil {
		fail(w, e)
		return
	}
	if e = s.setSession(w, r, u); e != nil {
		fail(w, e)
		return
	}
	writeJSON(w, 201, u)
}
func (s *Store) login(w http.ResponseWriter, r *http.Request) {
	ip, _, _ := net.SplitHostPort(r.RemoteAddr)
	s.loginMu.Lock()
	a := s.attempts[ip]
	if time.Since(a.Start) > 15*time.Minute {
		a = attempt{Start: time.Now()}
	}
	a.Count++
	s.attempts[ip] = a
	s.loginMu.Unlock()
	if a.Count > 20 {
		fail(w, apiError{429, "登录尝试过多，请 15 分钟后再试"})
		return
	}
	name, p, e := credentials(w, r)
	if e != nil {
		fail(w, e)
		return
	}
	var u User
	var hash string
	e = s.db.QueryRow("SELECT id,name,role,password_hash FROM users WHERE name=?", name).Scan(&u.ID, &u.Name, &u.Role, &hash)
	if e != nil || bcrypt.CompareHashAndPassword([]byte(hash), []byte(p)) != nil {
		fail(w, apiError{401, "账号或密码错误"})
		return
	}
	if e = s.loadAccess(&u); e != nil {
		fail(w, apiError{401, "账号或商户已停用，无法登录"})
		return
	}
	if e = s.setSession(w, r, u); e != nil {
		fail(w, e)
		return
	}
	tx, e := s.db.Begin()
	if e == nil {
		defer tx.Rollback()
		e = s.audit(tx, u, r, "登录", u.ID, "")
		if e == nil {
			e = tx.Commit()
		}
	}
	if e != nil {
		fail(w, e)
		return
	}
	s.loginMu.Lock()
	delete(s.attempts, ip)
	s.loginMu.Unlock()
	writeJSON(w, 200, u)
}
func (s *Store) logout(w http.ResponseWriter, r *http.Request) {
	if c, e := r.Cookie("west_session"); e == nil {
		sum := sha256.Sum256([]byte(c.Value))
		if _, e = s.db.Exec("DELETE FROM sessions WHERE token_hash=?", hex.EncodeToString(sum[:])); e != nil {
			fail(w, e)
			return
		}
	}
	http.SetCookie(w, &http.Cookie{Name: "west_session", Path: "/", MaxAge: -1, HttpOnly: true, SameSite: http.SameSiteStrictMode})
	writeJSON(w, 200, map[string]bool{"ok": true})
}
func main() {
	if len(os.Args) == 3 && os.Args[1] == "bootstrap-super" {
		st, err := openStore(envOr("DB_PATH", "data/west.db"))
		if err != nil {
			log.Fatal(err)
		}
		defer st.db.Close()
		err = st.transaction(func(tx *sql.Tx) error {
			var count int
			if e := tx.QueryRow("SELECT COUNT(*) FROM user_access WHERE is_super=1").Scan(&count); e != nil {
				return e
			}
			if count > 0 {
				return conflict("已经存在超管，不能再次引导")
			}
			var uid string
			if e := tx.QueryRow("SELECT u.id FROM users u JOIN user_access a ON a.user_id=u.id WHERE u.name=? AND u.role='admin' AND a.identity='internal' AND a.enabled=1", os.Args[2]).Scan(&uid); e != nil {
				return fmt.Errorf("请选择已存在且启用的旧管理员")
			}
			if _, e := tx.Exec("UPDATE user_access SET is_super=1 WHERE user_id=?", uid); e != nil {
				return e
			}
			_, e := tx.Exec("INSERT INTO audit(actor,action,target,detail,ip,created_at) VALUES('本机命令','指定初始超管',?,'旧管理员权限引导','',?)", uid, timestamp())
			return e
		})
		if err != nil {
			log.Fatal(err)
		}
		log.Print("初始超管已指定；服务未启动")
		return
	}
	if len(os.Args) > 1 {
		if len(os.Args) == 2 && os.Args[1] == "check" {
			if !checkReadiness(os.Stdout) {
				os.Exit(1)
			}
			return
		}
		log.Fatal("用法：west-platform [check | bootstrap-super 旧管理员账号]")
	}
	s, e := openStore(envOr("DB_PATH", "data/west.db"))
	if e != nil {
		log.Fatal(e)
	}
	defer s.db.Close()
	s.wechat, e = loadWechatFromEnv()
	if e != nil {
		log.Fatal(e)
	}
	addr := envOr("ADDR", "127.0.0.1:8081")
	srv := &http.Server{Addr: addr, Handler: s.handler(), ReadHeaderTimeout: 5 * time.Second, ReadTimeout: 15 * time.Second, WriteTimeout: 30 * time.Second, IdleTimeout: 60 * time.Second}
	go func() {
		ticker := time.NewTicker(time.Minute)
		defer ticker.Stop()
		for range ticker.C {
			if e := s.expireOrders(); e != nil {
				log.Printf("expiry task: %v", e)
			}
			ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
			if e := s.reconcileWechatOrders(ctx); e != nil {
				log.Printf("wechat reconciliation: %v", e)
			}
			cancel()
		}
	}()
	log.Printf("payment platform: http://%s", addr)
	log.Fatal(srv.ListenAndServe())
}
