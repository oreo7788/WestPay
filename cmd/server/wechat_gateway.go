package main

import (
	"context"
	"crypto/rsa"
	"crypto/x509"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/wechatpay-apiv3/wechatpay-go/core"
	"github.com/wechatpay-apiv3/wechatpay-go/core/auth/verifiers"
	"github.com/wechatpay-apiv3/wechatpay-go/core/notify"
	"github.com/wechatpay-apiv3/wechatpay-go/core/option"
	wxpayments "github.com/wechatpay-apiv3/wechatpay-go/services/payments"
	"github.com/wechatpay-apiv3/wechatpay-go/services/payments/jsapi"
	"github.com/wechatpay-apiv3/wechatpay-go/utils"
)

const wechatPlugin = "wechat_jsapi"

var errWechatOrderMissing = errors.New("wechat order not created")

type WechatConfig struct {
	AppID, MchID, Serial, APIV3Key, AppSecret, BaseURL, PublicKeyID string
	PrivateKey                                                      *rsa.PrivateKey
	PublicKey                                                       *rsa.PublicKey
	Certificate                                                     *x509.Certificate
}
type WechatGateway struct {
	config WechatConfig
	client *core.Client
	notify *notify.Handler
	http   *http.Client
}
type WechatPayParams struct {
	AppID     string `json:"appId"`
	TimeStamp string `json:"timeStamp"`
	NonceStr  string `json:"nonceStr"`
	Package   string `json:"package"`
	SignType  string `json:"signType"`
	PaySign   string `json:"paySign"`
}

func readSecretFile(env string) (string, error) {
	path := os.Getenv(env)
	if path == "" {
		return "", fmt.Errorf("缺少 %s", env)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return "", fmt.Errorf("无法读取 %s 指定的文件", env)
	}
	return strings.TrimSpace(string(data)), nil
}
func loadWechatFromEnv() (*WechatGateway, error) {
	enabled, err := strconv.ParseBool(envOr("WECHATPAY_ENABLED", "false"))
	if err != nil {
		return nil, fmt.Errorf("WECHATPAY_ENABLED 必须为 true 或 false")
	}
	if !enabled {
		return nil, nil
	}
	cfg := WechatConfig{AppID: os.Getenv("WECHATPAY_APP_ID"), MchID: os.Getenv("WECHATPAY_MCH_ID"), Serial: os.Getenv("WECHATPAY_MERCHANT_SERIAL"), PublicKeyID: os.Getenv("WECHATPAY_PUBLIC_KEY_ID"), BaseURL: strings.TrimRight(os.Getenv("PUBLIC_BASE_URL"), "/")}
	for key, value := range map[string]string{"WECHATPAY_APP_ID": cfg.AppID, "WECHATPAY_MCH_ID": cfg.MchID, "WECHATPAY_MERCHANT_SERIAL": cfg.Serial, "PUBLIC_BASE_URL": cfg.BaseURL} {
		if value == "" {
			return nil, fmt.Errorf("缺少 %s", key)
		}
	}
	if cfg.APIV3Key, err = readSecretFile("WECHATPAY_API_V3_KEY_FILE"); err != nil {
		return nil, err
	}
	if len(cfg.APIV3Key) != 32 {
		return nil, fmt.Errorf("API v3 密钥必须为 32 字节")
	}
	if cfg.AppSecret, err = readSecretFile("WECHATPAY_APP_SECRET_FILE"); err != nil {
		return nil, err
	}
	cfg.PrivateKey, err = utils.LoadPrivateKeyWithPath(os.Getenv("WECHATPAY_PRIVATE_KEY_PATH"))
	if err != nil {
		return nil, fmt.Errorf("无法读取 WECHATPAY_PRIVATE_KEY_PATH 中的商户私钥")
	}
	if cfg.PrivateKey.N.BitLen() < 2048 {
		return nil, fmt.Errorf("商户 RSA 私钥至少为 2048 位")
	}
	if path := os.Getenv("WECHATPAY_PUBLIC_KEY_PATH"); path != "" {
		if cfg.PublicKeyID == "" {
			return nil, fmt.Errorf("缺少 WECHATPAY_PUBLIC_KEY_ID")
		}
		cfg.PublicKey, err = utils.LoadPublicKeyWithPath(path)
		if err != nil {
			return nil, fmt.Errorf("无法读取微信支付公钥")
		}
	} else if path := os.Getenv("WECHATPAY_PLATFORM_CERT_PATH"); path != "" {
		cfg.Certificate, err = utils.LoadCertificateWithPath(path)
		if err != nil {
			return nil, fmt.Errorf("无法读取微信支付平台证书")
		}
	} else {
		return nil, fmt.Errorf("请配置微信支付公钥或平台证书文件")
	}
	return newWechatGateway(cfg, &http.Client{Timeout: 8 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }})
}
func newWechatGateway(cfg WechatConfig, hc *http.Client) (*WechatGateway, error) {
	parsed, err := url.Parse(cfg.BaseURL)
	if err != nil || parsed.Scheme != "https" || parsed.Host == "" || parsed.User != nil || parsed.RawQuery != "" || parsed.Fragment != "" || parsed.Path != "" {
		return nil, fmt.Errorf("PUBLIC_BASE_URL 必须为不含路径的 HTTPS 域名")
	}
	if len(cfg.APIV3Key) != 32 || cfg.PrivateKey == nil || cfg.AppID == "" || cfg.MchID == "" || cfg.AppSecret == "" || cfg.Serial == "" {
		return nil, fmt.Errorf("微信支付配置不完整")
	}
	opts := []core.ClientOption{option.WithHTTPClient(hc)}
	var handler *notify.Handler
	if cfg.PublicKey != nil {
		if cfg.PublicKeyID == "" || cfg.PublicKey.N.BitLen() < 2048 {
			return nil, fmt.Errorf("微信支付公钥 ID 无效或公钥长度不足")
		}
		opts = append(opts, option.WithWechatPayPublicKeyAuthCipher(cfg.MchID, cfg.Serial, cfg.PrivateKey, cfg.PublicKeyID, cfg.PublicKey))
		handler, err = notify.NewRSANotifyHandler(cfg.APIV3Key, verifiers.NewSHA256WithRSAPubkeyVerifier(cfg.PublicKeyID, *cfg.PublicKey))
	} else if cfg.Certificate != nil {
		if time.Now().Before(cfg.Certificate.NotBefore) || time.Now().After(cfg.Certificate.NotAfter) {
			return nil, fmt.Errorf("微信支付平台证书不在有效期内")
		}
		certs := []*x509.Certificate{cfg.Certificate}
		opts = append(opts, option.WithWechatPayAuthCipher(cfg.MchID, cfg.Serial, cfg.PrivateKey, certs))
		handler, err = notify.NewRSANotifyHandler(cfg.APIV3Key, verifiers.NewSHA256WithRSAVerifier(core.NewCertificateMapWithList(certs)))
	} else {
		return nil, fmt.Errorf("缺少微信支付验签公钥或平台证书")
	}
	if err != nil {
		return nil, fmt.Errorf("初始化通知解密失败")
	}
	client, err := core.NewClient(context.Background(), opts...)
	if err != nil {
		return nil, fmt.Errorf("初始化微信支付 SDK 失败")
	}
	return &WechatGateway{config: cfg, client: client, notify: handler, http: hc}, nil
}
func providerError(err error) error {
	var e *core.APIError
	if errors.As(err, &e) && e.Code == "ORDER_NOT_EXIST" {
		return errWechatOrderMissing
	}
	return apiError{502, "微信支付接口调用未完成，请稍后查单确认；不要重复创建商户订单号"}
}
func (w *WechatGateway) prepay(ctx context.Context, outNo, description, openid string, amount int64, expires time.Time) (string, WechatPayParams, error) {
	svc := jsapi.JsapiApiService{Client: w.client}
	resp, _, err := svc.PrepayWithRequestPayment(ctx, jsapi.PrepayRequest{Appid: core.String(w.config.AppID), Mchid: core.String(w.config.MchID), Description: core.String(description), OutTradeNo: core.String(outNo), TimeExpire: core.Time(expires), NotifyUrl: core.String(w.config.BaseURL + "/api/wechat/notify"), Amount: &jsapi.Amount{Total: core.Int64(amount), Currency: core.String("CNY")}, Payer: &jsapi.Payer{Openid: core.String(openid)}})
	if err != nil {
		return "", WechatPayParams{}, providerError(err)
	}
	if resp == nil || resp.PrepayId == nil {
		return "", WechatPayParams{}, apiError{502, "微信支付未返回预支付信息"}
	}
	return *resp.PrepayId, WechatPayParams{ptr(resp.Appid), ptr(resp.TimeStamp), ptr(resp.NonceStr), ptr(resp.Package), ptr(resp.SignType), ptr(resp.PaySign)}, nil
}
func (w *WechatGateway) signPrepay(ctx context.Context, prepayID string) (WechatPayParams, error) {
	nonce, err := utils.GenerateNonce()
	if err != nil {
		return WechatPayParams{}, err
	}
	p := WechatPayParams{AppID: w.config.AppID, TimeStamp: strconv.FormatInt(time.Now().Unix(), 10), NonceStr: nonce, Package: "prepay_id=" + prepayID, SignType: "RSA"}
	signed, err := w.client.Sign(ctx, fmt.Sprintf("%s\n%s\n%s\n%s\n", p.AppID, p.TimeStamp, p.NonceStr, p.Package))
	if err != nil {
		return p, apiError{500, "生成支付参数失败"}
	}
	p.PaySign = signed.Signature
	return p, nil
}
func (w *WechatGateway) query(ctx context.Context, outNo string) (*wxpayments.Transaction, error) {
	svc := jsapi.JsapiApiService{Client: w.client}
	t, _, err := svc.QueryOrderByOutTradeNo(ctx, jsapi.QueryOrderByOutTradeNoRequest{OutTradeNo: core.String(outNo), Mchid: core.String(w.config.MchID)})
	if err != nil {
		return nil, providerError(err)
	}
	return t, nil
}
func (w *WechatGateway) close(ctx context.Context, outNo string) error {
	svc := jsapi.JsapiApiService{Client: w.client}
	_, err := svc.CloseOrder(ctx, jsapi.CloseOrderRequest{OutTradeNo: core.String(outNo), Mchid: core.String(w.config.MchID)})
	if err != nil {
		return providerError(err)
	}
	return nil
}
func (w *WechatGateway) exchangeCode(ctx context.Context, code string) (string, error) {
	q := url.Values{"appid": {w.config.AppID}, "secret": {w.config.AppSecret}, "code": {code}, "grant_type": {"authorization_code"}}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, "https://api.weixin.qq.com/sns/oauth2/access_token?"+q.Encode(), nil)
	if err != nil {
		return "", apiError{502, "构造微信授权请求失败"}
	}
	resp, err := w.http.Do(req)
	if err != nil {
		return "", apiError{502, "微信授权请求失败，请重新扫码授权"}
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		return "", apiError{502, "微信授权服务暂不可用"}
	}
	var result struct {
		OpenID  string `json:"openid"`
		ErrCode int    `json:"errcode"`
	}
	if err = json.NewDecoder(io.LimitReader(resp.Body, 64<<10)).Decode(&result); err != nil || result.ErrCode != 0 || result.OpenID == "" || len(result.OpenID) > 128 {
		return "", apiError{502, "微信授权未完成，请重新扫码授权"}
	}
	return result.OpenID, nil
}
func ptr[T any](p *T) (zero T) {
	if p != nil {
		return *p
	}
	return zero
}
func (s *Store) requireWechat() (*WechatGateway, error) {
	if s.wechat == nil {
		return nil, apiError{503, "微信 JSAPI 尚未启用，请先完成本地配置"}
	}
	return s.wechat, nil
}
func (s *Store) wechatStatus(w http.ResponseWriter, r *http.Request, u User) error {
	ready := s.wechat != nil
	missing := missingWechatConfig()
	out := map[string]any{"plugin": wechatPlugin, "ready": ready, "mode": "普通商户 JSAPI · 网页扫码", "missing": missing}
	if ready {
		out["notifyUrl"] = s.wechat.config.BaseURL + "/api/wechat/notify"
		out["oauthCallback"] = s.wechat.config.BaseURL + "/api/wechat/oauth/callback"
	}
	writeJSON(w, 200, out)
	return nil
}

func missingWechatConfig() []string {
	fields := []string{"WECHATPAY_APP_ID", "WECHATPAY_MCH_ID", "WECHATPAY_MERCHANT_SERIAL", "WECHATPAY_PRIVATE_KEY_PATH", "WECHATPAY_API_V3_KEY_FILE", "WECHATPAY_APP_SECRET_FILE", "PUBLIC_BASE_URL"}
	missing := []string{}
	enabled, err := strconv.ParseBool(envOr("WECHATPAY_ENABLED", "false"))
	if err != nil || !enabled {
		missing = append(missing, "WECHATPAY_ENABLED=true")
	}
	for _, key := range fields {
		if strings.TrimSpace(os.Getenv(key)) == "" {
			missing = append(missing, key)
		}
	}
	if os.Getenv("WECHATPAY_PUBLIC_KEY_PATH") == "" && os.Getenv("WECHATPAY_PLATFORM_CERT_PATH") == "" {
		missing = append(missing, "WECHATPAY_PUBLIC_KEY_PATH / WECHATPAY_PLATFORM_CERT_PATH")
	} else if os.Getenv("WECHATPAY_PUBLIC_KEY_PATH") != "" && os.Getenv("WECHATPAY_PUBLIC_KEY_ID") == "" {
		missing = append(missing, "WECHATPAY_PUBLIC_KEY_ID")
	}
	return missing
}
