package main

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"errors"
	"net/http"
	"strconv"
	"time"

	wxpayments "github.com/wechatpay-apiv3/wechatpay-go/services/payments"
)

const wechatSchema = `
CREATE TABLE IF NOT EXISTS wechat_orders(order_id TEXT PRIMARY KEY REFERENCES orders(id),out_trade_no TEXT NOT NULL UNIQUE,appid TEXT NOT NULL,mchid TEXT NOT NULL,description TEXT NOT NULL,payer_openid TEXT NOT NULL DEFAULT '',prepay_id TEXT NOT NULL DEFAULT '',expires_at INTEGER NOT NULL,last_query_at INTEGER NOT NULL DEFAULT 0,hold_state TEXT NOT NULL CHECK(hold_state IN ('reserved','charged','released')));
CREATE TABLE IF NOT EXISTS checkout_tokens(token_hash TEXT PRIMARY KEY,order_id TEXT NOT NULL REFERENCES wechat_orders(order_id),expires_at INTEGER NOT NULL);
CREATE TABLE IF NOT EXISTS wechat_oauth_states(state_hash TEXT PRIMARY KEY,token_hash TEXT NOT NULL REFERENCES checkout_tokens(token_hash),checkout_token TEXT NOT NULL,expires_at INTEGER NOT NULL);
CREATE TABLE IF NOT EXISTS wechat_sessions(session_hash TEXT PRIMARY KEY,token_hash TEXT NOT NULL REFERENCES checkout_tokens(token_hash),openid TEXT NOT NULL,expires_at INTEGER NOT NULL);
CREATE TABLE IF NOT EXISTS wechat_events(event_id TEXT PRIMARY KEY,order_id TEXT NOT NULL REFERENCES orders(id),transaction_id TEXT NOT NULL,body_hash TEXT NOT NULL,created_at TEXT NOT NULL);
CREATE TABLE IF NOT EXISTS wechat_checkouts(order_id TEXT PRIMARY KEY REFERENCES wechat_orders(order_id),checkout_token TEXT NOT NULL UNIQUE,url TEXT NOT NULL,qr_png BLOB NOT NULL);
CREATE TABLE IF NOT EXISTS merchant_api_keys(merchant_id TEXT PRIMARY KEY REFERENCES merchants(id),token_hash TEXT NOT NULL UNIQUE,prefix TEXT NOT NULL,created_at TEXT NOT NULL,last_used_at TEXT NOT NULL DEFAULT '');
CREATE UNIQUE INDEX IF NOT EXISTS orders_provider_unique ON orders(provider_txn) WHERE provider_txn<>'';
`

type wechatOrder struct {
	ID, OutNo, AppID, MchID, OpenID, PrepayID, HoldState, Status, MerchantID, Description string
	Amount, Fee, Expires                                                                  int64
}

func hashToken(token string) string {
	sum := sha256.Sum256([]byte(token))
	return hex.EncodeToString(sum[:])
}
func (s *Store) lockWechat(oid string) func() {
	key := sha256.Sum256([]byte(oid))
	mu := &s.wxLocks[int(key[0])%len(s.wxLocks)]
	mu.Lock()
	return mu.Unlock
}
func getWechatOrder(db interface{ QueryRow(string, ...any) *sql.Row }, oid string) (wechatOrder, error) {
	var o wechatOrder
	err := db.QueryRow("SELECT w.order_id,w.out_trade_no,w.appid,w.mchid,w.payer_openid,w.prepay_id,w.expires_at,w.hold_state,o.status,o.merchant_id,o.amount,o.fee,w.description FROM wechat_orders w JOIN orders o ON o.id=w.order_id WHERE w.order_id=?", oid).Scan(&o.ID, &o.OutNo, &o.AppID, &o.MchID, &o.OpenID, &o.PrepayID, &o.Expires, &o.HoldState, &o.Status, &o.MerchantID, &o.Amount, &o.Fee, &o.Description)
	if err == sql.ErrNoRows {
		return o, notfound("微信支付订单不存在")
	}
	return o, err
}
func (s *Store) reserveWechat(tx *sql.Tx, oid, mid string, fee int64, description string, u User, r *http.Request) error {
	gw, err := s.requireWechat()
	if err != nil {
		return err
	}
	var timeout string
	if err = tx.QueryRow("SELECT value FROM settings WHERE key='orderTimeoutMinutes'").Scan(&timeout); err != nil {
		return err
	}
	minutes, err := strconv.Atoi(timeout)
	if err != nil {
		return err
	}
	if minutes < 1 {
		return bad("订单超时时间必须至少为 1 分钟")
	}
	if minutes > 120 {
		minutes = 120
	}
	var before int64
	if err = tx.QueryRow("SELECT balance FROM merchants WHERE id=?", mid).Scan(&before); err != nil {
		return err
	}
	if before < fee {
		return conflict("预付余额不足，无法预留订单手续费")
	}
	if description == "" {
		if err = tx.QueryRow("SELECT p.name FROM products p JOIN orders o ON o.product_code=p.code WHERE o.id=?", oid).Scan(&description); err != nil {
			return err
		}
	}
	if len(description) == 0 || len(description) > 127 {
		return bad("微信支付商品描述须为 1–127 字节，请缩短产品名称")
	}
	outNo := "WX" + hashToken(oid)[:30]
	if _, err = tx.Exec("INSERT INTO wechat_orders(order_id,out_trade_no,appid,mchid,description,expires_at,hold_state) VALUES(?,?,?,?,?,?,?)", oid, outNo, gw.config.AppID, gw.config.MchID, description, time.Now().Add(time.Duration(minutes)*time.Minute).Unix(), "reserved"); err != nil {
		return err
	}
	if _, err = tx.Exec("UPDATE merchants SET balance=balance-? WHERE id=?", fee, mid); err != nil {
		return err
	}
	if _, err = tx.Exec("INSERT INTO ledger VALUES(?,?,?,?,?,?,?,?,?,?)", id("L-"), mid, -fee, before, before-fee, "订单手续费预留", "RESERVE-"+oid, "微信 JSAPI 下单前预留", u.Name, timestamp()); err != nil {
		return err
	}
	return s.ensureWechatCheckoutTx(tx, oid, gw)
}
func wechatAudit(tx *sql.Tx, actor, action, oid, detail string) error {
	return auditEvent(tx, User{Name: actor}, action, oid, detail, "wechat")
}
func (s *Store) applyWechatTransaction(t *wxpayments.Transaction, eventID, bodyHash, actor string) error {
	if t == nil || ptr(t.OutTradeNo) == "" || ptr(t.Appid) == "" || ptr(t.Mchid) == "" || ptr(t.TradeState) == "" {
		return bad("微信交易信息不完整")
	}
	return s.transaction(func(tx *sql.Tx) error {
		var oid string
		err := tx.QueryRow("SELECT order_id FROM wechat_orders WHERE out_trade_no=?", ptr(t.OutTradeNo)).Scan(&oid)
		if err == sql.ErrNoRows {
			return notfound("通知未匹配到本地订单")
		}
		if err != nil {
			return err
		}
		o, err := getWechatOrder(tx, oid)
		if err != nil {
			return err
		}
		if o.AppID != ptr(t.Appid) || o.MchID != ptr(t.Mchid) {
			return bad("微信交易的商户号或 AppID 不匹配")
		}
		if t.Amount == nil || t.Amount.Total == nil || *t.Amount.Total != o.Amount || ptr(t.Amount.Currency) != "CNY" {
			return bad("微信交易金额或币种与本地订单不匹配")
		}
		state := ptr(t.TradeState)
		if state == "SUCCESS" {
			tid := ptr(t.TransactionId)
			if tid == "" || ptr(t.TradeType) != "JSAPI" || t.Payer == nil || o.OpenID == "" || ptr(t.Payer.Openid) != o.OpenID {
				return bad("微信交易流水、支付类型或付款人不匹配")
			}
			paidAt, err := time.Parse(time.RFC3339, ptr(t.SuccessTime))
			if err != nil {
				return bad("微信支付成功时间无效")
			}
			var usedBy string
			err = tx.QueryRow("SELECT id FROM orders WHERE provider_txn=? AND id<>?", tid, oid).Scan(&usedBy)
			if err == nil {
				return conflict("微信支付流水已关联其他订单")
			}
			if err != sql.ErrNoRows {
				return err
			}
			if eventID != "" {
				var priorOrder, priorTxn string
				err = tx.QueryRow("SELECT order_id,transaction_id FROM wechat_events WHERE event_id=?", eventID).Scan(&priorOrder, &priorTxn)
				if err == nil {
					if priorOrder != oid || priorTxn != tid {
						return conflict("通知编号与已有交易不一致")
					}
					return nil
				}
				if err != sql.ErrNoRows {
					return err
				}
			}
			if o.Status == "已支付" {
				var currentTxn string
				if err = tx.QueryRow("SELECT provider_txn FROM orders WHERE id=?", oid).Scan(&currentTxn); err != nil {
					return err
				}
				if currentTxn != tid {
					return conflict("订单已有不同的支付流水")
				}
			} else {
				if o.HoldState != "reserved" {
					return conflict("订单手续费预留已释放，需要人工核对微信交易")
				}
				var balance int64
				if err = tx.QueryRow("SELECT balance FROM merchants WHERE id=?", o.MerchantID).Scan(&balance); err != nil {
					return err
				}
				if _, err = tx.Exec("UPDATE wechat_orders SET hold_state='charged' WHERE order_id=?", oid); err != nil {
					return err
				}
				if _, err = tx.Exec("INSERT INTO ledger VALUES(?,?,?,?,?,?,?,?,?,?)", id("L-"), o.MerchantID, 0, balance, balance, "订单手续费结算", oid, "预留手续费已结算，未再次扣减", actor, timestamp()); err != nil {
					return err
				}
				if _, err = tx.Exec("UPDATE orders SET status='已支付',callback_at=?,provider_txn=? WHERE id=?", paidAt.In(time.Local).Format("2006-01-02 15:04:05"), tid, oid); err != nil {
					return err
				}
				if err = wechatAudit(tx, actor, "确认微信支付成功", oid, "验签/查单确认；预留手续费结算"); err != nil {
					return err
				}
			}
			if eventID != "" {
				_, err = tx.Exec("INSERT INTO wechat_events VALUES(?,?,?,?,?)", eventID, oid, tid, bodyHash, timestamp())
				return err
			}
			return nil
		}
		if eventID != "" {
			return bad("本通知入口只处理支付成功事件")
		}
		switch state {
		case "CLOSED", "REVOKED", "PAYERROR":
			return releaseWechatTx(tx, o, actor, ifClosedState(state))
		case "NOTPAY", "USERPAYING":
			return nil
		case "REFUND":
			return conflict("微信订单显示退款状态，请人工核对退款账务")
		default:
			return bad("微信返回了未支持的交易状态")
		}
	})
}
func ifClosedState(state string) string {
	if state == "PAYERROR" {
		return "支付失败"
	}
	return "已关闭"
}
func releaseWechatTx(tx *sql.Tx, o wechatOrder, actor, status string) error {
	if o.Status == "已支付" || o.HoldState == "charged" {
		return nil
	}
	if o.HoldState == "released" {
		return nil
	}
	var before int64
	if err := tx.QueryRow("SELECT balance FROM merchants WHERE id=?", o.MerchantID).Scan(&before); err != nil {
		return err
	}
	if _, err := tx.Exec("UPDATE merchants SET balance=balance+? WHERE id=?", o.Fee, o.MerchantID); err != nil {
		return err
	}
	if _, err := tx.Exec("UPDATE wechat_orders SET hold_state='released' WHERE order_id=?", o.ID); err != nil {
		return err
	}
	if _, err := tx.Exec("INSERT INTO ledger VALUES(?,?,?,?,?,?,?,?,?,?)", id("L-"), o.MerchantID, o.Fee, before, before+o.Fee, "手续费预留释放", "RELEASE-"+o.ID, "未支付订单确认关闭", actor, timestamp()); err != nil {
		return err
	}
	if _, err := tx.Exec("UPDATE orders SET status=? WHERE id=?", status, o.ID); err != nil {
		return err
	}
	return wechatAudit(tx, actor, "关闭微信订单", o.ID, "确认未支付后释放手续费预留")
}
func (s *Store) releaseWechat(oid, actor string) error {
	return s.transaction(func(tx *sql.Tx) error {
		o, err := getWechatOrder(tx, oid)
		if err != nil {
			return err
		}
		return releaseWechatTx(tx, o, actor, "已关闭")
	})
}
func (s *Store) reconcileWechatOrder(ctx context.Context, oid, actor string) error {
	gw, err := s.requireWechat()
	if err != nil {
		return err
	}
	unlock := s.lockWechat(oid)
	defer unlock()
	o, err := getWechatOrder(s.db, oid)
	if err != nil {
		return err
	}
	if o.Status == "已支付" || o.HoldState == "released" {
		return nil
	}
	if o.AppID != gw.config.AppID || o.MchID != gw.config.MchID {
		return conflict("订单绑定的微信商户与当前配置不一致")
	}
	expired := time.Now().Unix() >= o.Expires
	if o.OpenID == "" {
		if expired {
			return s.releaseWechat(oid, actor)
		}
		return nil
	}
	t, err := gw.query(ctx, o.OutNo)
	if errors.Is(err, errWechatOrderMissing) {
		if expired {
			return s.releaseWechat(oid, actor)
		}
		return nil
	}
	if err != nil {
		return err
	}
	if t == nil || ptr(t.OutTradeNo) != o.OutNo {
		return bad("微信查单返回的订单号不匹配")
	}
	if err = s.applyWechatTransaction(t, "", "", actor); err != nil {
		return err
	}
	if expired && ptr(t.TradeState) == "NOTPAY" {
		if err = gw.close(ctx, o.OutNo); err != nil {
			return err
		}
		return s.releaseWechat(oid, actor)
	}
	return nil
}
func (s *Store) reconcileWechatOrders(ctx context.Context) error {
	if s.wechat == nil {
		return nil
	}
	rows, err := s.db.Query("SELECT w.order_id FROM wechat_orders w JOIN orders o ON o.id=w.order_id WHERE w.hold_state='reserved' AND o.status<>'已支付' ORDER BY w.last_query_at,o.created_at LIMIT 30")
	if err != nil {
		return err
	}
	ids := []string{}
	for rows.Next() {
		var oid string
		if err = rows.Scan(&oid); err != nil {
			rows.Close()
			return err
		}
		ids = append(ids, oid)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return err
	}
	var first error
	for _, oid := range ids {
		if err = ctx.Err(); err != nil {
			return err
		}
		if _, err = s.db.Exec("UPDATE wechat_orders SET last_query_at=? WHERE order_id=?", time.Now().Unix(), oid); err != nil {
			return err
		}
		if err = s.reconcileWechatOrder(ctx, oid, "系统补单"); err != nil && first == nil {
			first = err
		}
	}
	cleanup := s.transaction(func(tx *sql.Tx) error {
		cutoff := time.Now().Unix()
		for _, table := range []string{"wechat_oauth_states", "wechat_sessions", "checkout_tokens"} {
			// Delete dependent expiring records before checkout tokens.
			if _, e := tx.Exec("DELETE FROM "+table+" WHERE expires_at<?", cutoff); e != nil {
				return e
			}
		}
		return nil
	})
	if first != nil {
		return first
	}
	return cleanup
}
