package main

import (
	"database/sql"
	"net/http"
	"strings"
	"time"
)

// Business API credentials are scoped to one internal merchant. A credential
// cannot choose a merchantId or access administration endpoints.
func (s *Store) businessAuth(r *http.Request) (string, User, error) {
	fields := strings.Fields(r.Header.Get("Authorization"))
	if len(fields) != 2 || !strings.EqualFold(fields[0], "Bearer") || len(fields[1]) != 69 || !strings.HasPrefix(fields[1], "west_") {
		return "", User{}, apiError{401, "缺少有效的商户 API 密钥"}
	}
	var mid string
	err := s.db.QueryRow("SELECT k.merchant_id FROM merchant_api_keys k JOIN merchants m ON m.id=k.merchant_id WHERE k.token_hash=? AND m.enabled=1", hashToken(fields[1])).Scan(&mid)
	if err == sql.ErrNoRows {
		return "", User{}, apiError{401, "商户 API 密钥无效或商户已停用"}
	}
	if err != nil {
		return "", User{}, err
	}
	if _, err = s.db.Exec("UPDATE merchant_api_keys SET last_used_at=? WHERE token_hash=?", timestamp(), hashToken(fields[1])); err != nil {
		return "", User{}, err
	}
	return mid, User{ID: "api:" + mid, Name: "商户接口:" + mid, Role: "operator"}, nil
}
func (s *Store) businessCreateOrder(w http.ResponseWriter, r *http.Request) {
	mid, u, err := s.businessAuth(r)
	if err != nil {
		fail(w, err)
		return
	}
	var body struct {
		MerchantOrder string  `json:"merchantOrder"`
		Amount        float64 `json:"amount"`
		Description   string  `json:"description"`
		ProductCode   string  `json:"productCode"`
		ChannelID     string  `json:"channelId"`
	}
	if err = decode(w, r, &body); err != nil {
		fail(w, err)
		return
	}
	if body.ProductCode == "" {
		body.ProductCode = "PAY-WX"
	}
	o, created, err := s.createOrder(r, u, createPaymentInput{MerchantID: mid, MerchantOrder: body.MerchantOrder, Amount: body.Amount, Description: body.Description, ProductCode: body.ProductCode, ChannelID: body.ChannelID}, true)
	if err != nil {
		fail(w, err)
		return
	}
	status := http.StatusOK
	if created {
		status = http.StatusCreated
	}
	cashierHeaders(w)
	writeJSON(w, status, o)
}
func (s *Store) businessGetOrder(w http.ResponseWriter, r *http.Request) {
	mid, u, err := s.businessAuth(r)
	if err != nil {
		fail(w, err)
		return
	}
	var oid string
	err = s.db.QueryRow("SELECT id FROM orders WHERE merchant_id=? AND merchant_order=?", mid, strings.TrimSpace(r.PathValue("merchantOrder"))).Scan(&oid)
	if err == sql.ErrNoRows {
		fail(w, notfound("订单不存在"))
		return
	}
	if err != nil {
		fail(w, err)
		return
	}
	if r.Method == "POST" {
		if err = s.reconcileWechatOrder(r.Context(), oid, u.Name); err != nil {
			fail(w, err)
			return
		}
		err = s.transaction(func(tx *sql.Tx) error {
			if _, e := tx.Exec("UPDATE orders SET query_count=query_count+1 WHERE id=?", oid); e != nil {
				return e
			}
			return s.audit(tx, u, r, "业务接口查单", oid, "微信支付查询")
		})
		if err != nil {
			fail(w, err)
			return
		}
	}
	o, err := orderBy(s.db, oid)
	if err != nil {
		fail(w, err)
		return
	}
	cashierHeaders(w)
	writeJSON(w, 200, o)
}
func (s *Store) merchantAPIStatus(w http.ResponseWriter, r *http.Request, u User) error {
	if !u.can("merchants.apikey") {
		return apiError{403, "缺少管理商户 API 密钥权限"}
	}
	mid := r.PathValue("id")
	var exists int
	if err := s.db.QueryRow("SELECT COUNT(*) FROM merchants WHERE id=?", mid).Scan(&exists); err != nil {
		return err
	}
	if exists == 0 {
		return notfound("商户不存在")
	}
	var prefix, created, last string
	err := s.db.QueryRow("SELECT prefix,created_at,last_used_at FROM merchant_api_keys WHERE merchant_id=?", mid).Scan(&prefix, &created, &last)
	if err != nil && err != sql.ErrNoRows {
		return err
	}
	writeJSON(w, 200, map[string]any{"enabled": err == nil, "prefix": prefix, "createdAt": created, "lastUsedAt": last})
	return nil
}
func (s *Store) saveMerchantAPIKey(w http.ResponseWriter, r *http.Request, u User) error {
	if !u.can("merchants.apikey") {
		return apiError{403, "缺少管理商户 API 密钥权限"}
	}
	mid := r.PathValue("id")
	token := "west_" + secureToken()
	revoke := strings.HasSuffix(r.URL.Path, "/revoke")
	err := s.transaction(func(tx *sql.Tx) error {
		var count int
		if err := tx.QueryRow("SELECT COUNT(*) FROM merchants WHERE id=?", mid).Scan(&count); err != nil {
			return err
		}
		if count == 0 {
			return notfound("商户不存在")
		}
		if revoke {
			if _, err := tx.Exec("DELETE FROM merchant_api_keys WHERE merchant_id=?", mid); err != nil {
				return err
			}
			return s.audit(tx, u, r, "撤销商户 API 密钥", mid, "")
		}
		if _, err := tx.Exec("INSERT INTO merchant_api_keys(merchant_id,token_hash,prefix,created_at) VALUES(?,?,?,?) ON CONFLICT(merchant_id) DO UPDATE SET token_hash=excluded.token_hash,prefix=excluded.prefix,created_at=excluded.created_at,last_used_at=''", mid, hashToken(token), token[:13], time.Now().Format("2006-01-02 15:04:05")); err != nil {
			return err
		}
		return s.audit(tx, u, r, "生成商户 API 密钥", mid, "旧密钥即刻失效；密钥仅展示一次")
	})
	if err != nil {
		return err
	}
	if revoke {
		writeJSON(w, 200, map[string]bool{"enabled": false})
		return nil
	}
	writeJSON(w, 200, map[string]string{"apiKey": token, "merchantId": mid})
	return nil
}
