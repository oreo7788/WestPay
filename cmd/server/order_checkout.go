package main

import (
	"database/sql"
	"net/http"
	"time"

	"github.com/skip2/go-qrcode"
)

// The checkout is created in the order transaction, so a successful intake
// always returns the same QR code, including after a retry or server restart.
func (s *Store) ensureWechatCheckoutTx(tx *sql.Tx, oid string, gw *WechatGateway) error {
	var exists int
	if err := tx.QueryRow("SELECT COUNT(*) FROM wechat_checkouts WHERE order_id=?", oid).Scan(&exists); err != nil {
		return err
	}
	if exists > 0 {
		return nil
	}
	o, err := getWechatOrder(tx, oid)
	if err != nil {
		return err
	}
	if o.HoldState != "reserved" || o.Expires <= time.Now().Unix() {
		return conflict("订单已完成或已超时，不能生成新的收款二维码")
	}
	if o.AppID != gw.config.AppID || o.MchID != gw.config.MchID {
		return conflict("订单绑定的微信商户与当前配置不一致")
	}
	token := secureToken()
	payURL := gw.config.BaseURL + "/pay/" + token
	png, err := qrcode.Encode(payURL, qrcode.Medium, 320)
	if err != nil {
		return err
	}
	if _, err = tx.Exec("INSERT INTO checkout_tokens VALUES(?,?,?)", hashToken(token), oid, o.Expires+15*60); err != nil {
		return err
	}
	_, err = tx.Exec("INSERT INTO wechat_checkouts(order_id,checkout_token,url,qr_png) VALUES(?,?,?,?)", oid, token, payURL, png)
	return err
}
func (s *Store) orderQRCode(w http.ResponseWriter, r *http.Request, u User) error {
	var png []byte
	err := s.db.QueryRow("SELECT qr_png FROM wechat_checkouts WHERE order_id=?", r.PathValue("id")).Scan(&png)
	if err == sql.ErrNoRows {
		return notfound("该订单没有已生成的收款二维码")
	}
	if err != nil {
		return err
	}
	cashierHeaders(w)
	w.Header().Set("Content-Type", "image/png")
	_, err = w.Write(png)
	return err
}
