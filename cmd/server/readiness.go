package main

import (
	"database/sql"
	"fmt"
	"io"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

type readinessCheck struct{ state, name, detail string }

// checkReadiness deliberately bypasses openStore: it does not create a DB,
// migrate tables, seed records, bind a port, or call a remote payment API.
func checkReadiness(out io.Writer) bool {
	checks := []readinessCheck{}
	add := func(state, name, detail string) { checks = append(checks, readinessCheck{state, name, detail}) }
	root := envOr("WEB_ROOT", "frontend/dist")
	if info, err := os.Stat(filepath.Join(root, "index.html")); err != nil || info.IsDir() {
		add("blocked", "Rust 前端资源", "缺少构建后的 index.html，请先执行 make build")
	} else {
		complete := true
		for _, pattern := range []string{"*.js", "*.wasm", "*.css"} {
			matches, err := filepath.Glob(filepath.Join(root, pattern))
			if err != nil || len(matches) == 0 {
				complete = false
			}
		}
		if complete {
			add("ok", "Rust 前端资源", "HTML / JS / WASM / CSS 构建产物齐备")
		} else {
			add("blocked", "Rust 前端资源", "构建资源不齐，请重新构建前端")
		}
	}
	enabled, parseErr := strconv.ParseBool(envOr("WECHATPAY_ENABLED", "false"))
	if parseErr != nil {
		add("blocked", "微信配置", "WECHATPAY_ENABLED 必须为 true 或 false")
	} else if !enabled {
		add("blocked", "微信配置", "真实支付未启用；check 不会自动读取 .env，请先在本机加载配置")
	} else if _, err := loadWechatFromEnv(); err != nil {
		add("blocked", "微信配置", err.Error())
	} else {
		add("ok", "微信配置", "本地参数和密钥已加载；未请求微信接口，商户权限及域名连通性仍需联调")
	}
	if missing := missingWechatConfig(); len(missing) > 0 {
		add("info", "待配置项", strings.Join(missing, "、"))
	}
	path := envOr("DB_PATH", "data/west.db")
	if info, err := os.Stat(path); err != nil || info.IsDir() {
		add("blocked", "本地数据库", "尚未初始化；后续启动服务后创建管理员、商户和真实通道")
	} else {
		absolute, err := filepath.Abs(path)
		if err != nil {
			add("blocked", "本地数据库", "无法解析数据库路径")
		} else {
			address := url.URL{Scheme: "file", Path: absolute, RawQuery: "mode=ro&_query_only=true&_busy_timeout=1000"}
			db, err := sql.Open("sqlite3", address.String())
			if err != nil {
				add("blocked", "本地数据库", "无法只读打开数据库")
			} else {
				db.SetMaxOpenConns(1)
				if err = db.Ping(); err != nil {
					add("blocked", "本地数据库", "无法只读读取数据库，请检查文件及权限")
				} else {
					inspectReadinessDB(db, add)
				}
				_ = db.Close()
			}
		}
	}
	ready := true
	fmt.Fprintln(out, "本地接入检查（不启动服务，不调用微信接口）")
	for _, c := range checks {
		label := map[string]string{"ok": "已就绪", "blocked": "待处理", "info": "说明"}[c.state]
		if c.state == "blocked" {
			ready = false
		}
		fmt.Fprintf(out, "[%s] %s：%s\n", label, c.name, c.detail)
	}
	if ready {
		fmt.Fprintln(out, "本地接入条件齐备；真实收款仍需微信扫码及外部业务系统联调。")
	} else {
		fmt.Fprintln(out, "尚未满足真实接单收款条件；请先完成以上待处理项。")
	}
	return ready
}
func inspectReadinessDB(db *sql.DB, add func(string, string, string)) {
	tables := map[string]bool{}
	rows, err := db.Query("SELECT name FROM sqlite_master WHERE type='table'")
	if err != nil {
		add("blocked", "数据库结构", "无法读取数据库结构")
		return
	}
	for rows.Next() {
		var name string
		if err = rows.Scan(&name); err != nil {
			break
		}
		tables[name] = true
	}
	readErr := rows.Err()
	_ = rows.Close()
	if err != nil || readErr != nil {
		add("blocked", "数据库结构", "数据库结构读取不完整")
		return
	}
	count := func(query string) (int, error) { var n int; err := db.QueryRow(query).Scan(&n); return n, err }
	checks := []struct{ table, name, query, missing, empty string }{
		{"users", "管理员账号", "SELECT COUNT(*) FROM users WHERE role='admin'", "等待首次启动初始化", "尚未创建管理员"},
		{"merchants", "可用商户", "SELECT COUNT(*) FROM merchants WHERE enabled=1", "等待首次启动初始化", "尚未创建并启用商户"},
		{"products", "微信支付产品", "SELECT COUNT(*) FROM products WHERE enabled=1 AND method='微信支付'", "等待首次启动初始化", "没有已启用的微信产品"},
		{"channels", "微信 JSAPI 通道", "SELECT COUNT(*) FROM channels WHERE enabled=1 AND plugin='wechat_jsapi' AND method='微信支付'", "等待首次启动初始化", "只有模拟通道或尚无已启用的微信 JSAPI 通道"},
		{"merchant_api_keys", "外部接单密钥", "SELECT COUNT(*) FROM merchant_api_keys k JOIN merchants m ON m.id=k.merchant_id WHERE m.enabled=1", "新增表尚未初始化；下一次正常启动时自动创建，再由管理员签发密钥", "尚未为可用商户签发 API 密钥"},
	}
	for _, c := range checks {
		if !tables[c.table] {
			add("blocked", c.name, c.missing)
			continue
		}
		n, err := count(c.query)
		if err != nil {
			add("blocked", c.name, "无法读取当前配置记录，请检查数据库结构")
		} else if n == 0 {
			add("blocked", c.name, c.empty)
		} else {
			add("ok", c.name, fmt.Sprintf("%d 项可用", n))
		}
	}
	pending := []string{}
	for _, table := range []string{"wechat_orders", "checkout_tokens", "wechat_oauth_states", "wechat_sessions", "wechat_events", "wechat_checkouts"} {
		if !tables[table] {
			pending = append(pending, table)
		}
	}
	if len(pending) > 0 {
		add("info", "支付数据表", "新增表等待下次正常启动自动初始化；check 不执行迁移")
	} else {
		add("ok", "支付数据表", "支付及二维码相关表已存在；未进行写入验证")
	}
	if tables["orders"] {
		if n, err := count("SELECT COUNT(*) FROM orders"); err == nil {
			add("info", "已保存订单", fmt.Sprintf("%d 笔", n))
		}
	}
}
