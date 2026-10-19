package main

import (
	"bytes"
	"crypto/rand"
	"crypto/subtle"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/url"
	"path/filepath"
	"strconv"
	"time"

	"github.com/skip2/go-qrcode"
	wxpayments "github.com/wechatpay-apiv3/wechatpay-go/services/payments"
)

func secureToken() string {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		panic(err)
	}
	return hex.EncodeToString(b)
}
func cashierHeaders(w http.ResponseWriter) {
	w.Header().Set("Referrer-Policy", "no-referrer")
	w.Header().Set("X-Robots-Tag", "noindex, nofollow")
	w.Header().Set("Content-Security-Policy", "frame-ancestors 'none'; base-uri 'none'")
}
func (s *Store) cashierOrder(token string) (wechatOrder, error) {
	if len(token) != 64 {
		return wechatOrder{}, notfound("收款链接不存在")
	}
	var oid string
	err := s.db.QueryRow("SELECT order_id FROM checkout_tokens WHERE token_hash=? AND expires_at>?", hashToken(token), time.Now().Unix()).Scan(&oid)
	if err == sql.ErrNoRows {
		return wechatOrder{}, apiError{410, "收款链接已过期或不存在"}
	}
	if err != nil {
		return wechatOrder{}, err
	}
	return getWechatOrder(s.db, oid)
}
func (s *Store) checkout(w http.ResponseWriter, r *http.Request, u User) error {
	gw, err := s.requireWechat()
	if err != nil {
		return err
	}
	oid := r.PathValue("id")
	err = s.transaction(func(tx *sql.Tx) error {
		if err := s.ensureWechatCheckoutTx(tx, oid, gw); err != nil {
			return err
		}
		return s.audit(tx, u, r, "查看收款二维码", oid, "复用订单已保存的二维码")
	})
	if err != nil {
		return err
	}
	o, err := orderBy(s.db, oid)
	if err != nil {
		return err
	}
	cashierHeaders(w)
	writeJSON(w, 200, map[string]any{"url": o.PayURL, "qrImage": o.QRImage, "expiresAt": o.ExpiresAt, "canPay": o.CanPay})
	return nil
}
func (s *Store) cashierPage(w http.ResponseWriter, r *http.Request) {
	cashierHeaders(w)
	if _, err := s.cashierOrder(r.PathValue("token")); err != nil {
		fail(w, err)
		return
	}
	http.ServeFile(w, r, filepath.Join(envOr("WEB_ROOT", "frontend/dist"), "index.html"))
}
func (s *Store) cashierQR(w http.ResponseWriter, r *http.Request) {
	cashierHeaders(w)
	gw, err := s.requireWechat()
	if err != nil {
		fail(w, err)
		return
	}
	if _, err = s.cashierOrder(r.PathValue("token")); err != nil {
		fail(w, err)
		return
	}
	var png []byte
	err = s.db.QueryRow("SELECT qr_png FROM wechat_checkouts WHERE checkout_token=?", r.PathValue("token")).Scan(&png)
	if err == sql.ErrNoRows {
		png, err = qrcode.Encode(gw.config.BaseURL+"/pay/"+r.PathValue("token"), qrcode.Medium, 320)
	}
	if err != nil {
		fail(w, err)
		return
	}
	w.Header().Set("Content-Type", "image/png")
	_, _ = w.Write(png)
}
func (s *Store) cashierIdentity(r *http.Request, token string) (string, error) {
	c, err := r.Cookie("west_cashier")
	if err != nil {
		return "", apiError{401, "请先完成微信网页授权"}
	}
	var openid string
	err = s.db.QueryRow("SELECT openid FROM wechat_sessions WHERE session_hash=? AND token_hash=? AND expires_at>?", hashToken(c.Value), hashToken(token), time.Now().Unix()).Scan(&openid)
	if err != nil {
		return "", apiError{401, "微信授权已失效，请重新授权"}
	}
	return openid, nil
}
func (s *Store) cashierInfo(w http.ResponseWriter, r *http.Request) {
	cashierHeaders(w)
	o, err := s.cashierOrder(r.PathValue("token"))
	if err != nil {
		fail(w, err)
		return
	}
	_, authErr := s.cashierIdentity(r, r.PathValue("token"))
	writeJSON(w, 200, map[string]any{"description": o.Description, "amount": float64(o.Amount) / 100, "orderId": o.ID, "status": o.Status, "expiresAt": o.Expires, "ready": s.wechat != nil, "authenticated": authErr == nil, "canPay": o.Status != "已支付" && o.HoldState == "reserved" && o.Expires > time.Now().Unix()})
}
func (s *Store) oauthStart(w http.ResponseWriter, r *http.Request) {
	cashierHeaders(w)
	gw, err := s.requireWechat()
	if err != nil {
		fail(w, err)
		return
	}
	o, err := s.cashierOrder(r.PathValue("token"))
	if err != nil {
		fail(w, err)
		return
	}
	if o.Expires <= time.Now().Unix() || o.HoldState != "reserved" {
		fail(w, conflict("订单已完成或已超时"))
		return
	}
	if o.AppID != gw.config.AppID || o.MchID != gw.config.MchID {
		fail(w, conflict("订单绑定的微信商户与当前配置不一致"))
		return
	}
	state := secureToken()
	if _, err = s.db.Exec("INSERT INTO wechat_oauth_states VALUES(?,?,?,?)", hashToken(state), hashToken(r.PathValue("token")), r.PathValue("token"), time.Now().Add(10*time.Minute).Unix()); err != nil {
		fail(w, err)
		return
	}
	http.SetCookie(w, &http.Cookie{Name: "west_wx_oauth", Value: state, Path: "/api/wechat/oauth/callback", HttpOnly: true, Secure: true, SameSite: http.SameSiteLaxMode, MaxAge: 600})
	q := url.Values{"appid": {gw.config.AppID}, "redirect_uri": {gw.config.BaseURL + "/api/wechat/oauth/callback"}, "response_type": {"code"}, "scope": {"snsapi_base"}, "state": {state}}
	http.Redirect(w, r, "https://open.weixin.qq.com/connect/oauth2/authorize?"+q.Encode()+"#wechat_redirect", http.StatusFound)
}
func (s *Store) oauthCallback(w http.ResponseWriter, r *http.Request) {
	cashierHeaders(w)
	gw, err := s.requireWechat()
	if err != nil {
		fail(w, err)
		return
	}
	state, code := r.URL.Query().Get("state"), r.URL.Query().Get("code")
	cookie, err := r.Cookie("west_wx_oauth")
	if err != nil || len(state) != 64 || subtle.ConstantTimeCompare([]byte(cookie.Value), []byte(state)) != 1 || code == "" || len(code) > 512 {
		fail(w, apiError{403, "微信授权状态无效，请重新扫码"})
		return
	}
	var token string
	err = s.transaction(func(tx *sql.Tx) error {
		var deadline int64
		if e := tx.QueryRow("SELECT checkout_token,expires_at FROM wechat_oauth_states WHERE state_hash=?", hashToken(state)).Scan(&token, &deadline); e == sql.ErrNoRows {
			return apiError{403, "微信授权状态已使用或已过期"}
		} else if e != nil {
			return e
		}
		if deadline <= time.Now().Unix() {
			return apiError{403, "微信授权已过期"}
		}
		_, e := tx.Exec("DELETE FROM wechat_oauth_states WHERE state_hash=?", hashToken(state))
		return e
	})
	if err != nil {
		fail(w, err)
		return
	}
	http.SetCookie(w, &http.Cookie{Name: "west_wx_oauth", Path: "/api/wechat/oauth/callback", MaxAge: -1, HttpOnly: true, Secure: true, SameSite: http.SameSiteLaxMode})
	o, err := s.cashierOrder(token)
	if err != nil {
		fail(w, err)
		return
	}
	openid, err := gw.exchangeCode(r.Context(), code)
	if err != nil {
		http.Redirect(w, r, gw.config.BaseURL+"/pay/"+token+"?auth_error=1", http.StatusFound)
		return
	}
	sid := secureToken()
	if _, err = s.db.Exec("INSERT INTO wechat_sessions VALUES(?,?,?,?)", hashToken(sid), hashToken(token), openid, o.Expires+15*60); err != nil {
		fail(w, err)
		return
	}
	http.SetCookie(w, &http.Cookie{Name: "west_cashier", Value: sid, Path: "/api/cashier/", HttpOnly: true, Secure: true, SameSite: http.SameSiteLaxMode, MaxAge: int(o.Expires + 15*60 - time.Now().Unix())})
	http.Redirect(w, r, gw.config.BaseURL+"/pay/"+token, http.StatusFound)
}
func (s *Store) cashierPrepay(w http.ResponseWriter, r *http.Request) {
	cashierHeaders(w)
	gw, err := s.requireWechat()
	if err != nil {
		fail(w, err)
		return
	}
	if r.Header.Get("Origin") != gw.config.BaseURL {
		fail(w, apiError{403, "收款页面来源无效"})
		return
	}
	token := r.PathValue("token")
	o, err := s.cashierOrder(token)
	if err != nil {
		fail(w, err)
		return
	}
	openid, err := s.cashierIdentity(r, token)
	if err != nil {
		fail(w, err)
		return
	}
	unlock := s.lockWechat(o.ID)
	defer unlock()
	o, err = getWechatOrder(s.db, o.ID)
	if err != nil {
		fail(w, err)
		return
	}
	if o.Status == "已支付" || o.HoldState != "reserved" || o.Expires <= time.Now().Unix() {
		fail(w, conflict("订单已完成或已超时"))
		return
	}
	if o.AppID != gw.config.AppID || o.MchID != gw.config.MchID {
		fail(w, conflict("微信商户配置已变更，请联系商户"))
		return
	}
	if o.OpenID != "" && o.OpenID != openid {
		fail(w, conflict("该订单已绑定其他微信账号，请重新创建订单"))
		return
	}
	if o.PrepayID != "" {
		params, e := gw.signPrepay(r.Context(), o.PrepayID)
		if e != nil {
			fail(w, e)
			return
		}
		writeJSON(w, 200, params)
		return
	}
	// Bind the verified payer before contacting WeChat; never accept an OpenID from the browser.
	bound, err := s.db.Exec("UPDATE wechat_orders SET payer_openid=? WHERE order_id=? AND (payer_openid='' OR payer_openid=?) AND hold_state='reserved'", openid, o.ID, openid)
	if err != nil {
		fail(w, err)
		return
	}
	if changed, _ := bound.RowsAffected(); changed != 1 {
		fail(w, conflict("该订单的付款身份或支付状态已变更，请刷新页面"))
		return
	}
	prepayID, params, err := gw.prepay(r.Context(), o.OutNo, o.Description, openid, o.Amount, time.Unix(o.Expires, 0).UTC())
	if err != nil {
		fail(w, err)
		return
	}
	if _, err = s.db.Exec("UPDATE wechat_orders SET prepay_id=? WHERE order_id=?", prepayID, o.ID); err != nil {
		fail(w, err)
		return
	}
	writeJSON(w, 200, params)
}
func (s *Store) cashierRefresh(w http.ResponseWriter, r *http.Request) {
	gw, err := s.requireWechat()
	if err != nil {
		fail(w, err)
		return
	}
	if r.Header.Get("Origin") != gw.config.BaseURL {
		fail(w, apiError{403, "收款页面来源无效"})
		return
	}
	o, err := s.cashierOrder(r.PathValue("token"))
	if err != nil {
		fail(w, err)
		return
	}
	if _, err = s.cashierIdentity(r, r.PathValue("token")); err != nil {
		fail(w, err)
		return
	}
	result, err := s.db.Exec("UPDATE wechat_orders SET last_query_at=? WHERE order_id=? AND last_query_at<=?", time.Now().Unix(), o.ID, time.Now().Unix()-5)
	if err != nil {
		fail(w, err)
		return
	}
	changed, _ := result.RowsAffected()
	if changed > 0 {
		if err = s.reconcileWechatOrder(r.Context(), o.ID, "付款人查单"); err != nil {
			fail(w, err)
			return
		}
	}
	s.cashierInfo(w, r)
}
func (s *Store) wechatNotify(w http.ResponseWriter, r *http.Request) {
	cashierHeaders(w)
	gw, err := s.requireWechat()
	if err != nil {
		writeJSON(w, 503, map[string]string{"code": "FAIL", "message": "支付通道尚未启用"})
		return
	}
	stamp, err := strconv.ParseInt(r.Header.Get("Wechatpay-Timestamp"), 10, 64)
	now := time.Now().Unix()
	if err != nil || stamp < now-300 || stamp > now+300 {
		writeJSON(w, 401, map[string]string{"code": "FAIL", "message": "通知时间戳无效"})
		return
	}
	body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, 1<<20))
	if err != nil {
		writeJSON(w, 400, map[string]string{"code": "FAIL", "message": "通知内容无效"})
		return
	}
	var envelope struct {
		ID       string `json:"id"`
		Event    string `json:"event_type"`
		Resource *struct {
			Nonce string `json:"nonce"`
		} `json:"resource"`
	}
	if json.Unmarshal(body, &envelope) != nil || envelope.ID == "" || envelope.Resource == nil || len(envelope.Resource.Nonce) != 12 {
		writeJSON(w, 400, map[string]string{"code": "FAIL", "message": "通知内容无效"})
		return
	}
	r.Body = io.NopCloser(bytes.NewReader(body))
	var transaction wxpayments.Transaction
	notification, err := gw.notify.ParseNotifyRequest(r.Context(), r, &transaction)
	if err != nil {
		writeJSON(w, 401, map[string]string{"code": "FAIL", "message": "通知验签或解密失败"})
		return
	}
	if notification.EventType != "TRANSACTION.SUCCESS" || notification.Resource == nil || notification.Resource.OriginalType != "transaction" || notification.ResourceType != "encrypt-resource" {
		writeJSON(w, 400, map[string]string{"code": "FAIL", "message": "不支持该通知类型"})
		return
	}
	if err = s.applyWechatTransaction(&transaction, notification.ID, hashToken(string(body)), "微信支付通知"); err != nil {
		var ae apiError
		status := 500
		message := "通知处理失败，请重试"
		if errors.As(err, &ae) {
			status = ae.Status
			message = ae.Message
		}
		writeJSON(w, status, map[string]string{"code": "FAIL", "message": message})
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
