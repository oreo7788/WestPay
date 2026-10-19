# 微信 JSAPI 网页扫码支付接入

## 接入范围

采用普通商户 API v3 的 JSAPI 支付：商户生成订单二维码，付款人用微信扫码打开收款网页，完成网页授权后点击“确认支付”，调起微信收银台。二维码内容是本系统的 HTTPS 收款页链接。

本版本只读取一组普通商户 AppID / 商户号。多个内部商户、多个微信 JSAPI 通道仍使用这一组微信支付账号；尚不支持服务商模式的子商户进件、独立商户凭据路由、分账或代付。

官方资料：

- [JSAPI 支付产品介绍及扫码场景](https://pay.weixin.qq.com/doc/v3/merchant/4012062524)
- [JSAPI 开发指引](https://pay.weixin.qq.com/doc/v3/merchant/4012791870)
- [微信网页授权](https://developers.weixin.qq.com/doc/service/guide/h5/auth.html)
- [微信支付官方 Go SDK](https://github.com/wechatpay-apiv3/wechatpay-go)

## 已实现的流程

1. 外部业务系统通过商户密钥调用下单接口，或操作员选择微信支付产品及 `wechat_jsapi` 通道创建收款订单。金额、费率和通道成本在后端校验，微信商户号、AppID、商品描述及到期时间保留快照。
2. 预留商户预付余额中的订单手续费，记录余额流水。余额不足时整个创建事务回滚。
3. 创建订单时自动生成并保存带随机令牌的收款页链接和二维码，接口直接返回地址，订单列表可查看。相同单号重试复用原订单及二维码。收款页无需管理员登录。
4. 微信中通过 `snsapi_base` 网页授权获取当前 AppID 下的 OpenID。后端以一次性 state 和安全 Cookie 校验回跳，再向微信换取身份；不会将 AppSecret 或授权 token 返回前端。
5. 付款人点击“确认支付”。后端从数据库读取金额和身份，用官方 SDK 签名请求 `/v3/pay/transactions/jsapi`，Rust 页面调用 `WeixinJSBridge`。
6. 用户完成或取消支付后，前端请求后端查单。后端也接收微信支付通知，完成 RSA 验签、AES-GCM 解密，以及订单号、商户号、AppID、金额、币种、支付类型、付款人的匹配检查。
7. 只有已验证的微信成功通知或查单成功结果才能将订单标为“已支付”。支付状态、流水和手续费结算在一个数据库事务中完成；重复通知和查单不会重复扣费。
8. 服务运行时每分钟轮询一批待确认订单补查。过期订单先确认微信状态；仍未支付则调用微信关单，成功后释放预留手续费。接口超时或结果不明时保留待确认状态和预留，不直接认定支付失败。

手续费预留减少的是本地商户的**可用预付余额**，不是调用微信扣款。成功时将预留转为结算，不再扣第二次；确认未支付关闭后释放。实际收款金额由微信直接处理，本系统没有资金划转功能。

订单支付期限取系统“订单超时时间”，最多 120 分钟，并传给微信 `time_expire`。缓存的 `prepay_id` 只在这一期限内复用。没有向微信尝试下单的订单可在本地到期关闭；已尝试下单的订单必须经过微信查单/关单确认。退款、交易/资金账单下载和自动对账尚未实现。

## 本地配置

项目根目录提供 `.env.example`，其中只有占位值，默认禁用微信支付。复制为 `.env` 并在本机填写。真实密钥放在项目的 `secrets/` 文件夹或其他受控位置，`.env` 和 `secrets/` 已加入忽略规则。

| 配置 | 内容 |
| --- | --- |
| `WECHATPAY_ENABLED` | 配齐后设为 `true`；`false` 时不初始化真实通道 |
| `WECHATPAY_APP_ID` | 已与该普通商户号绑定、具备网页授权能力的公众号 AppID |
| `WECHATPAY_MCH_ID` | 普通商户号，需已开通对应支付权限 |
| `WECHATPAY_MERCHANT_SERIAL` | 商户 API 证书序列号 |
| `WECHATPAY_PRIVATE_KEY_PATH` | 与商户证书对应的 RSA 私钥 PEM 文件路径 |
| `WECHATPAY_API_V3_KEY_FILE` | 存放 32 字节 API v3 密钥的文本文件路径 |
| `WECHATPAY_APP_SECRET_FILE` | 存放公众号 AppSecret 的文本文件路径 |
| `WECHATPAY_PUBLIC_KEY_ID` | 微信支付公钥 ID，非商户证书序列号 |
| `WECHATPAY_PUBLIC_KEY_PATH` | 微信支付公钥 PEM 文件路径 |
| `WECHATPAY_PLATFORM_CERT_PATH` | 使用平台证书模式时，替代上面两个公钥配置 |
| `PUBLIC_BASE_URL` | 收款页的 HTTPS 域名，例如 `https://pay.example.com`，不带路径 |

相对文件路径均相对启动时的工作目录。文本密钥文件允许末尾换行。不要将实际私钥、AppSecret 或 API v3 密钥提交到代码或发在聊天里。建议 `.env` 与密钥文件权限设为 `600`。

公钥模式与证书模式二选一。有公钥路径时优先使用公钥；平台证书不自动下载/更新，应在有效期内从可信渠道准备并在更换后重启。当前凭据轮换应先处理旧账号的未结订单；不能直接把仍有未结订单的 AppID/商户号替换成另一账号。

服务只加载本地配置，不在启动时自动请求微信证书。启用后配置不完整将拒绝启动，避免静默回退到模拟通道。“支付插件”页显示配置状态和回调地址，不展示密钥。配置成功只代表文件和参数已加载，不代表微信授权权限已实测。

## 域名与本机运行

当前 SQLite 服务按单进程运行，不要多实例同时操作同一数据库。后端继续监听 `127.0.0.1:8081`。管理后台可在本机访问，但真实微信扫码、网页授权回跳和通知需要一个能路由到本机的 HTTPS 域名。仅有 `127.0.0.1` 无法让付款人的手机及微信服务器访问。

可由已有 HTTPS 反向代理或受控域名转发以下入口至本机，并保留原始 `Host`，避免来源校验失败：

- `/api/v1/*`：带商户密钥认证的外部业务系统下单和查单
- `/pay/*`：收款页面
- `/api/cashier/*`：二维码、付款身份、下单与查单
- `/api/wechat/oauth/callback`：网页授权回跳
- `/api/wechat/notify`：微信支付成功通知
- 构建产物中的根路径 CSS、JS、WASM 静态资源：收款页使用这些文件

管理接口 `/api/auth/*`、`/api/merchants*`、`/api/payments*` 等保留本机访问即可。代理不要记录 OAuth 回跳查询串、收款令牌、Cookie、AppSecret 或请求/通知正文。

微信平台还需要分别配置：

- 公众平台的**网页授权域名**：对应 `PUBLIC_BASE_URL` 的域名。
- 商户平台的 **JSAPI 支付授权目录**：例如 `https://pay.example.com/pay/`，以 `/` 结尾。
- 公众号 AppID 与商户号的绑定、JSAPI 产品权限，以及平台要求的域名校验。域名验证文件需由你的 HTTPS 入口按微信提供的路径返回。

本项目使用的实际授权回跳：`https://pay.example.com/api/wechat/oauth/callback`。下单时传入通知地址：`https://pay.example.com/api/wechat/notify`。

准备好后，在项目根目录手动加载环境并启动：

```sh
set -a
source .env
set +a
make run
```

以上命令只供后续联调使用，本轮开发没有启动本地服务，也没有发布到托管站点。

## 后台使用

1. 在“支付插件”查看本地配置状态。
2. 在“通道管理”新增通道，支付方式选择“微信支付”，插件选择“微信 JSAPI”。填入实际通道费率和允许金额。
3. 商户准备足够的预付手续费余额；使用微信产品及该通道创建订单。
4. 在订单列表的“收款二维码”列查看自动生成的二维码。微信扫码、授权并确认支付；列表及弹窗每 5 秒刷新支付状态。
5. 在后台查询支付结果，核对微信商户订单号、微信流水、订单状态、预付流水和报表。

真实通道不提供“模拟支付成功/失败”按钮，后端也拒绝模拟接口。`demo` 模拟通道仍可用于本地演示。

外部业务系统的密钥、下单、重试及状态查询方式详见 [BUSINESS_API.md](BUSINESS_API.md)。

## 接口及代码位置

| 接口 | 权限 / 作用 |
| --- | --- |
| `GET /api/integrations/wechat` | 已登录账号读取非敏感接入状态 |
| `POST /api/payments/{id}/checkout` | 复用已保存二维码；旧的未结订单可补生成 |
| `GET /api/payments/{id}` | 已登录账号读取单笔订单及二维码元数据 |
| `GET /api/payments/{id}/qrcode` | 已登录账号查看已保存的二维码 PNG |
| `POST /api/payments/{id}/query` | 非只读账号请求微信查单 |
| `GET /pay/{token}` | 带有效收款令牌访问 Rust 收款页 |
| `GET /api/cashier/{token}` | 有效令牌读取收款金额及本地状态 |
| `GET /api/cashier/{token}/qr.png` | 收款页二维码 |
| `GET /api/cashier/{token}/oauth/start` | 发起微信网页授权 |
| `GET /api/wechat/oauth/callback` | 一次性 state 与 Cookie 校验，换取付款身份 |
| `POST /api/cashier/{token}/prepay` | 同源且已授权的付款人下单/复用预支付信息 |
| `POST /api/cashier/{token}/refresh` | 同源且已授权的付款人查单，有短时间节流 |
| `POST /api/wechat/notify` | 微信验签及解密通过后的支付成功通知 |

Go：`cmd/server/wechat_gateway.go`、`wechat_store.go`、`wechat_cashier.go`。Rust：`frontend/src/wechat.rs`。订单、已保存二维码、商户 API 密钥等数据表在服务下次启动时以新增表和索引的方式创建，不清空已有数据。

## 当前验证状态

Go 后端与 Rust/WASM 前端已完成发布构建，并新增 `./bin/west-platform check` 只读检查接入准备情况；最新结果见 [STATUS.md](STATUS.md)。当前未配置真实商户凭据，未发起真实支付、授权或通知联调。本次未新增或执行自动化测试；正式收款前仍需使用你的账号完成小额支付、取消、重复通知、通知延迟、查单补单和超时关单的联调验证。
