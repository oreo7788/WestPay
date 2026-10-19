# 支付通道与订单管理平台

Go + SQLite 后端，Rust + Leptos/WebAssembly 前端。本地管理功能已接入数据库，默认仅监听 `127.0.0.1:8081`。

## 启动

环境：Go 1.23+、C 编译器（SQLite 驱动需要 CGO）、Rust、WASM target、Trunk。

```sh
rustup target add wasm32-unknown-unknown
cargo install trunk
make run
```

打开 http://127.0.0.1:8081，首次使用创建超级管理员。账号 2–40 个字符，密码 10–72 个字符，不存在默认密码。超管在“系统设置 → 角色与权限”配置菜单和操作权限，再在“用户管理”创建内部/商户用户并分配角色。商户账号需绑定商户主体。升级、备份及旧管理员引导见 [docs/ACCESS_CONTROL.md](docs/ACCESS_CONTROL.md)。

SQLite 数据保存在 `data/west.db`；服务重启保留账号、配置、订单、余额流水与日志。可通过 `DB_PATH` 指定其他数据库，通过 `ADDR` 改端口，通过 `WEB_ROOT` 指定静态资源目录。备份应停止服务后复制整个 `data` 文件夹，避免漏掉 WAL 文件。

## 当前进度与接入检查

当前先完善本地业务系统，上游订单及微信实时接入后置。最新方案只有内部、商户两类身份：内部维护支持的供应商目录，商户填写资料，经内部审核后启用通道。通道支持商户直收和平台代收，详见 [docs/IDENTITY_CHANNEL_MODEL.md](docs/IDENTITY_CHANNEL_MODEL.md)。统一资金账户、结算和提现/付款审批的要求与待明确规则见 [docs/LOCAL_SYSTEM_REVIEW.md](docs/LOCAL_SYSTEM_REVIEW.md)。这些属于下一阶段开发范围，尚未全部实现。

最新本机环境状态与下一步见 [docs/STATUS.md](docs/STATUS.md)。可在项目根目录执行：

```sh
./bin/west-platform check
# 或使用源码构建检查命令
make check
```

检查不会启动服务、调用微信接口或创建业务数据；缺少真实收款条件时退出码为 `1`。命令和服务器都不会自动加载 `.env`，需先在本机导入环境变量。检查通过只表示本地前置条件齐备，仍需真实联调。

## 已实现

- 登录、退出、12 小时会话、密码哈希、超管用户管理、自定义角色、多角色授权、菜单/页面/操作权限、内部/商户数据隔离、停用、修改/重置密码、来源检查和登录频率限制。权限收回在后续请求生效，停用或重置密码使既有会话失效。
- 商户新增、编辑、启停；初始预付和后续预付调整生成不可变流水。金额以整数分保存，禁止负余额；重复调整标识不会重复记账。
- 产品新增、编辑、费率和支付方式；通道新增、编辑、费率、限额、插件和启停。
- 收款订单创建、唯一商户订单号、方式与限额校验、列表查询、详情、CSV 导出、模拟查单和模拟支付成功/失败。
- 成功订单与手续费扣减在同一事务中完成，重复结果不会重复扣费；订单保留创建时的费率与通道成本，历史统计不受后续费率调整影响。
- 首页、通道/产品/商户报表、订单统计及预付明细使用数据库数据，支持日期、商户筛选和 CSV 导出。
- 系统收款金额上下限、未支付订单超时关闭、商户黑名单拦截。
- 投诉处理状态、付款记录登记、收钱吧商户资料、公众号小程序资料、用户标识登记的新增和编辑。
- 登录和操作审计日志。

## 外部业务系统接单与二维码

外部系统使用商户 API 密钥调用 `POST /api/v1/orders`，提交单号、金额和商品描述，自动生成并保存订单二维码。相同单号的相同请求重试会返回原订单，不重复创建和预留手续费；密钥只可访问所属商户的订单。

拥有“商户列表：管理接口密钥”权限的内部用户或超管，在“商户列表 → 接口接入”生成/撤销密钥。订单列表可直接查看二维码、支付状态，默认每 5 秒刷新，支持主动向微信查单。接口支持读取订单状态，具体参数及响应见 [docs/BUSINESS_API.md](docs/BUSINESS_API.md)。

## 微信 JSAPI 接入

已加入 `wechat_jsapi` 普通商户适配器：订单二维码、独立 Rust 收款页、微信网页授权、JSAPI 下单/调起支付、支付通知验签解密、查单补单、超时关单及手续费预留/结算/释放。默认禁用，需自行配置商户号、公众号 AppID、密钥文件及可访问本机的 HTTPS 域名。

配置模板：[.env.example](.env.example)。流程、账号权限、域名、接口及联调说明：[docs/WECHAT_JSAPI.md](docs/WECHAT_JSAPI.md)。本轮已构建前后端，本地服务仍保持停止，尚未进行真实微信支付联调。

## 外部集成边界

已实现 `demo` 本地模拟与 `wechat_jsapi` 微信支付两种适配器。模拟通道不会访问支付机构；微信通道仅在启用并配置后访问官方接口。微信接入目前只支持一组普通商户凭据，不含服务商子商户、退款、分账、资金账单或自动对账。付款记录登记不会执行转账。收钱吧授权和机器人消息等仍需单独接入；资料管理页面可保存登记信息，不代表已连接外部系统。

功能版首次启动没有演示商户、假订单或虚构统计；只初始化三个常用支付产品与三个模拟通道。可先新增商户并预付，再创建订单，使用“模拟支付成功”检查账务流程。

## 验证

```sh
go test -race ./cmd/server
cd frontend
trunk build --release
```

现有早期测试用例覆盖未登录与旧只读权限、跨来源写入拦截、预付调整去重、负余额回滚、重启持久化、并发支付结果去重、配置限额、黑名单、超时关闭、费率快照与平台净利润、CSV 导出。新权限模型尚未进行运行验收，本轮未新增或执行测试。

## 代码

- `cmd/server/main.go`：数据库结构、认证、路由和权限
- `cmd/server/payments.go`：订单、账务、商户、产品和通道
- `cmd/server/reports.go`：首页、财务报表与导出
- `cmd/server/admin.go`：配置、资料记录与审计
- `cmd/server/access.go`：权限迁移、用户/角色管理、权限目录、鉴权及商户归属检查
- `cmd/server/tasks.go`：订单超时任务
- `cmd/server/readiness.go`：不启动服务的本地接入条件检查
- `frontend/src/main.rs`：控制台布局、订单与商户页面
- `frontend/src/features.rs`：登录、编辑表单、订单操作、报表和系统配置
- `frontend/src/access.rs`：用户/角色页面、菜单和操作授权、登录权限刷新、修改密码

## API

`/api/auth/status`、`/api/auth/setup`、`/api/auth/login`、`/api/auth/logout` 用于后台登录流程。下面列出的管理接口需要登录会话。

外部业务接口 `/api/v1/orders*` 使用所属商户的 Bearer API 密钥；收款页及 `/api/cashier/*` 使用限时收款令牌，付款下单还需要微信授权身份；`/api/wechat/notify` 通过微信签名验证，不依赖后台登录。

- `GET, POST /api/payments`；`POST /api/payments/{id}/query`；`POST /api/payments/{id}/simulate`
- `GET, POST /api/merchants`；`PATCH /api/merchants/{id}`；`POST /api/merchants/{id}/prepay`、`toggle`
- `GET, POST /api/products`；`PATCH /api/products/{code}`
- `GET, POST /api/channels`；`PATCH /api/channels/{id}`；`POST /api/channels/{id}/toggle`
- `GET /api/finance/{channels|products|merchants|prepay|statistics}`
- `GET, PATCH /api/settings`；`GET /api/audit`
- 超管专属：`GET, POST /api/users`；`PATCH /api/users/{id}`；`POST /api/users/{id}/password`
- 超管专属：`GET /api/access/permissions`；`GET, POST /api/access/roles`；`PATCH /api/access/roles/{id}`
- 已登录用户：`POST /api/auth/password`（校验原密码后修改自身密码）
- `GET, POST /api/records/{kind}`；`PATCH /api/records/{kind}/{id}`

预付调整请求需要 `amount`、`note` 和 `idempotencyKey`。流水只增不改。查询和财务导出可带 `export=csv`。
