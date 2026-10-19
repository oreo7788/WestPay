mod access;
mod features;
mod orders;
mod wechat;
use features::*;
use leptos::prelude::*;
use serde::{Deserialize, Serialize};
use wasm_bindgen::JsValue;
use wasm_bindgen_futures::spawn_local;

#[derive(Clone, Debug, Deserialize)]
#[serde(rename_all = "camelCase")]
struct Dashboard {
    today_orders: i32,
    today_success: i32,
    today_amount: f64,
    success_rate: f64,
    today_profit: f64,
    month_amount: f64,
    yesterday_orders: i32,
    yesterday_amount: f64,
    trend: Vec<f64>,
}
#[derive(Clone, Debug, Deserialize, Serialize, PartialEq)]
#[serde(rename_all = "camelCase")]
struct PaymentOrder {
    system_id: String,
    recharge_id: String,
    merchant_order: String,
    merchant_id: String,
    merchant_name: String,
    product_code: String,
    product_name: String,
    amount: f64,
    channel_name: String,
    channel_id: String,
    method: String,
    created_at: String,
    callback_at: String,
    status: String,
    provider_txn: String,
    query_count: i32,
    plugin: String,
    provider_order: String,
    pay_url: String,
    qr_image: String,
    expires_at: i64,
    can_pay: bool,
    description: String,
}
#[derive(Clone, Debug, Deserialize, Serialize)]
#[serde(rename_all = "camelCase")]
struct Merchant {
    id: String,
    name: String,
    today_count: i32,
    today_rate: f64,
    yesterday_count: i32,
    recent30m: i32,
    recent10m: i32,
    prepaid: f64,
    remaining: f64,
    total_prepay: f64,
    enabled: bool,
}
#[derive(Clone, Debug, Deserialize)]
#[serde(rename_all = "camelCase")]
struct Product {
    method: String,
    code: String,
    name: String,
    enabled: bool,
}
#[derive(Clone, Debug, Deserialize)]
#[serde(rename_all = "camelCase")]
struct Channel {
    id: String,
    name: String,
    fee_rate: f64,
    plugin: String,
    method: String,
    today_amount: f64,
    enabled: bool,
}
#[derive(Clone, Debug, Deserialize)]
#[serde(rename_all = "camelCase")]
struct FinanceRow {
    name: String,
    orders: i32,
    amount: f64,
    fee: f64,
    remaining: f64,
    date: String,
    #[serde(rename = "type")]
    kind: String,
    before: f64,
    after: f64,
    related_id: String,
}

#[component]
fn Console(user: features::User, on_logout: Callback<()>) -> impl IntoView {
    let site_name = RwSignal::new("支付通道管理平台".to_string());
    let access = access::install(user);
    let readonly = false;
    let initial = access::PAGES
        .iter()
        .find(|p| access.page(p))
        .copied()
        .unwrap_or("no_access");
    let (page, set_page) = signal(initial.to_string());
    let (open_group, set_open_group) = signal(String::new());
    let (dashboard, set_dashboard) = signal(None::<Dashboard>);
    let (payments, set_payments) = signal(Vec::<PaymentOrder>::new());
    let (merchants, set_merchants) = signal(Vec::<Merchant>::new());
    let (_products, set_products) = signal(Vec::<Product>::new());
    let (channels, set_channels) = signal(Vec::<Channel>::new());
    let (detail, set_detail) = signal(None::<PaymentOrder>);
    let (notice, set_notice) = signal(String::new());
    let (q, set_q) = signal(String::new());
    let (status, set_status) = signal("全部状态".to_string());
    let (method, set_method) = signal("所有支付方式".to_string());
    let (prepay_merchant, set_prepay_merchant) = signal(None::<Merchant>);
    let (prepay_amount, set_prepay_amount) = signal(String::new());
    let (new_merchant, set_new_merchant) = signal(false);
    let (merchant_name, set_merchant_name) = signal(String::new());
    let (merchant_prepay, set_merchant_prepay) = signal(String::new());
    let prepay_key = RwSignal::new(String::new());
    let prepay_busy = RwSignal::new(false);
    let merchant_busy = RwSignal::new(false);

    Effect::new(move |_| {
        let current_page = page.get();
        let _grants = access.0.get();
        set_detail.set(None);
        if !access.page(&current_page) {
            let next = access::PAGES
                .iter()
                .find(|p| access.page(p))
                .copied()
                .unwrap_or("no_access");
            if current_page != next {
                set_page.set(next.into());
            }
        }
        if access.can("home.view") {
            fetch_dashboard(set_dashboard);
        } else {
            set_dashboard.set(None);
        }
        if current_page == "home" && access.can("orders.view") {
            fetch_payments(
                set_payments,
                "".into(),
                "全部状态".into(),
                "所有支付方式".into(),
                false,
            );
        }
        if access.any(&["home.view", "merchants.view", "orders.create"]) {
            fetch_merchants(set_merchants);
        } else {
            set_merchants.set(Vec::new());
        }
        if access.any(&["products.view", "orders.create"]) {
            fetch_products(set_products);
        }
        if access.any(&["channels.view", "orders.create"]) {
            fetch_channels(set_channels);
        } else {
            set_channels.set(Vec::new());
        }
    });

    view! {
           <div class="app-shell">
               <aside class="sidebar">
                   <div class="brand"><span class="brand-mark">"西"</span><span class="brand-name">{move||site_name.get()}</span></div>
                   <nav class="nav-list">
                       <NavItem page="home" current=page set_page=set_page icon="⌂" label="平台首页" />
                       <NavItem page="orders" current=page set_page=set_page icon="▤" label="收款订单" />
                       <NavItem page="history" current=page set_page=set_page icon="▣" label="历史订单" />
                       <NavGroup name="merchant" label="商户管理" icon="▧" open_group=open_group set_open_group=set_open_group>
                           <NavItem page="products" current=page set_page=set_page icon="·" label="产品设置" />
                           <NavItem page="merchants" current=page set_page=set_page icon="·" label="商户列表" />
                           <NavItem page="merchant_settlement" current=page set_page=set_page icon="·" label="商户对账" />
                       </NavGroup>
                       <NavGroup name="channels" label="通道管理" icon="☷" open_group=open_group set_open_group=set_open_group>
                           <NavItem page="channels" current=page set_page=set_page icon="·" label="通道配置" />
                           <NavItem page="receivers" current=page set_page=set_page icon="·" label="收钱吧管理" />
                           <NavItem page="complaints" current=page set_page=set_page icon="·" label="投诉管理" />
                           <NavItem page="methods" current=page set_page=set_page icon="·" label="支付方式" />
                           <NavItem page="plugins" current=page set_page=set_page icon="·" label="支付插件" />
                           <NavItem page="official" current=page set_page=set_page icon="·" label="公众号小程序" />
                       </NavGroup>
                       <NavGroup name="finance" label="财务对账" icon="▤" open_group=open_group set_open_group=set_open_group>
                           <NavItem page="finance_channels" current=page set_page=set_page icon="·" label="通道数据" />
                           <NavItem page="finance_products" current=page set_page=set_page icon="·" label="产品数据" />
                           <NavItem page="finance_merchants" current=page set_page=set_page icon="·" label="商户数据" />
                           <NavItem page="prepay" current=page set_page=set_page icon="·" label="预付明细" />
                           <NavItem page="statistics" current=page set_page=set_page icon="·" label="订单统计" />
                       </NavGroup>
                       <NavItem page="robot" current=page set_page=set_page icon="⌘" label="机器人命令" />
                       <NavGroup name="settings" label="系统设置" icon="⚙" open_group=open_group set_open_group=set_open_group>
                           <NavItem page="site_settings" current=page set_page=set_page icon="·" label="网站信息配置" />
                           <NavItem page="blacklist" current=page set_page=set_page icon="·" label="黑名单管理" />
                           <NavItem page="pay_settings" current=page set_page=set_page icon="·" label="支付相关配置" />
                           <NavItem page="quick_login" current=page set_page=set_page icon="·" label="快捷登录配置" />
                           <NavItem page="accounts" current=page set_page=set_page icon="·" label="用户管理" />
    <NavItem page="roles" current=page set_page=set_page icon="·" label="角色与权限" />
                       </NavGroup>
                       <NavGroup name="other" label="其他功能" icon="▣" open_group=open_group set_open_group=set_open_group>
                           <NavItem page="transfers" current=page set_page=set_page icon="·" label="付款记录" />
                           <NavItem page="logs" current=page set_page=set_page icon="·" label="登录日志" />
                           <NavItem page="user_ids" current=page set_page=set_page icon="·" label="用户标识" />
                       </NavGroup>
                   </nav>
                   <div class="sidebar-user"><span class="avatar">"A"</span><span><strong>{move||access.0.get().name}</strong><small>{move||if access.0.get().super_user{"超级管理员"}else if access.internal(){"内部用户"}else{"商户用户"}}</small></span><span class="user-more">"···"</span></div>
               </aside>
               <main class="main-area">
                   <header class="topbar"><div class="crumb"><span>"支付管理"</span><b>"/"</b><strong>{move || page_title(&page.get())}</strong></div><div class="top-actions"><span class="system-pill"><i></i>"系统运行正常"</span><span class="top-avatar">"A"</span><span>{move||access.0.get().name}</span><access::OwnPassword/><button class="text-button" on:click=move |_|on_logout.run(())>"退出"</button></div></header>
                   <div class="content-area">
                       {move || if !access.page(&page.get()){view!{<section class="panel empty-state"><h2>"暂无页面访问权限"</h2><p>"请联系超级管理员分配角色，或从左侧选择已授权菜单。"</p></section>}.into_any()}else{match page.get().as_str() {
                           "orders" | "history" => view! { <PaymentPage archive=page.get() == "history" payments=payments set_payments=set_payments q=q set_q=set_q status=status set_status=set_status method=method set_method=set_method set_detail=set_detail set_notice=set_notice readonly=readonly /> }.into_any(),
                           "merchants" => view! { <MerchantPage merchants=merchants set_merchants=set_merchants set_prepay_merchant=set_prepay_merchant set_prepay_amount=set_prepay_amount set_new_merchant=set_new_merchant set_notice=set_notice prepay_key=prepay_key readonly=readonly /> }.into_any(),
                           "products" => view! { <ManagedPage kind="products" readonly=readonly /> }.into_any(),
                           "merchant_settlement" => view! { <ReportsPage title="商户对账" endpoint="merchants" page_permission="merchant_settlement" /> }.into_any(),
                           "channels" => view! { <ManagedPage kind="channels" readonly=readonly /> }.into_any(),
                           "finance_channels" => view! { <ReportsPage title="通道数据" endpoint="channels" page_permission="finance_channels" /> }.into_any(),
                           "finance_products" => view! { <ReportsPage title="产品数据" endpoint="products" page_permission="finance_products" /> }.into_any(),
                           "finance_merchants" => view! { <ReportsPage title="商户数据" endpoint="merchants" page_permission="finance_merchants" /> }.into_any(),
                           "prepay" => view! { <ReportsPage title="预付明细" endpoint="prepay" page_permission="prepay" /> }.into_any(),
                           "statistics" => view! { <ReportsPage title="订单统计" endpoint="statistics" page_permission="statistics" /> }.into_any(),
                           "home" => view! { <Home dashboard=dashboard orders=payments channels=channels merchants=merchants set_page=set_page set_detail=set_detail /> }.into_any(),
                           "robot" => view! { <RobotPage /> }.into_any(),
                           "methods" => view! { <PaymentMethods /> }.into_any(),
                           "plugins" => view! { <AvailablePlugins /> }.into_any(),
                           "receivers" => view! { <ManagedPage kind="receivers" readonly=readonly /> }.into_any(),
                           "complaints" => view! { <ManagedPage kind="complaints" readonly=readonly /> }.into_any(),
                           "official" => view! { <ManagedPage kind="official" readonly=readonly /> }.into_any(),
                           "blacklist" => view! { <ManagedPage kind="blacklist" readonly=readonly /> }.into_any(),
                           "transfers" => view! { <ManagedPage kind="transfers" readonly=readonly /> }.into_any(),
                           "logs" => view! { <AuditPage /> }.into_any(),
                           "user_ids" => view! { <ManagedPage kind="user_ids" readonly=readonly /> }.into_any(),
                           "accounts" => view! { <access::UsersPage /> }.into_any(),
    "roles" => view! { <access::RolesPage /> }.into_any(),
                           _ => view! { <SettingsEditor readonly=!access.can(&format!("{}.edit",page.get())) on_saved=Callback::new(move |v:serde_json::Value|{if let Some(name)=v["siteName"].as_str(){site_name.set(name.into());}}) /> }.into_any(),
                       }}}
                       <Show when=move || !notice.get().is_empty()><div class="toast" role="status">{move || notice.get()}</div></Show>
                   </div>
               </main>
               <Show when=move || detail.get().is_some()>
                   <div class="modal-backdrop" on:click=move |_| set_detail.set(None)>
                       <div class="modal" on:click=|ev| ev.stop_propagation()>
                           <div class="modal-head"><div><h2>"收款订单详情"</h2><p>"订单信息与通道状态"</p></div><button class="close" on:click=move |_| set_detail.set(None)>"×"</button></div>
                           {move || detail.get().map(|o| view! { <OrderDetails readonly=readonly order=o on_update=Callback::new(move |updated:PaymentOrder|{set_detail.set(Some(updated.clone()));set_payments.update(|list|{if let Some(row)=list.iter_mut().find(|row|row.system_id==updated.system_id){*row=updated;}});fetch_dashboard(set_dashboard);fetch_merchants(set_merchants);fetch_channels(set_channels);})/> })}
                       </div>
                   </div>
               </Show>
               <Show when=move || prepay_merchant.get().is_some()>
                   <div class="modal-backdrop" on:click=move |_| {if !prepay_busy.get(){set_prepay_merchant.set(None)}}><div class="modal small-modal" on:click=|ev| ev.stop_propagation()>
                       <div class="modal-head"><div><h2>"调整商户预付"</h2><p>{move || prepay_merchant.get().map(|m|format!("商户 {} · {}",m.id,m.name)).unwrap_or_default()}</p></div><button class="close" on:click=move |_| {if !prepay_busy.get(){set_prepay_merchant.set(None)}}>"×"</button></div>
                       <form on:submit=move |ev| { ev.prevent_default(); adjust_prepay(prepay_merchant.get(), prepay_amount.get(), prepay_key.get(), set_merchants, set_prepay_merchant, prepay_busy, set_notice); }><label>"变更金额（正数增加，负数减少）"<input type="number" step="0.01" prop:value=move || prepay_amount.get() on:input=move |ev| set_prepay_amount.set(event_target_value(&ev)) placeholder="例如 500 或 -200" /></label><div class="form-actions"><button type="button" class="button secondary" on:click=move |_| {if !prepay_busy.get(){set_prepay_merchant.set(None)}}>"取消"</button><button class="button primary" type="submit" disabled=move||prepay_busy.get()>"确认调整"</button></div></form>
                   </div></div>
               </Show>
               <Show when=move || new_merchant.get()>
                   <div class="modal-backdrop" on:click=move |_| {if !merchant_busy.get(){set_new_merchant.set(false)}}><div class="modal small-modal" on:click=|ev| ev.stop_propagation()>
                       <div class="modal-head"><div><h2>"添加商户"</h2><p>"资料与初始预付将保存到本地数据库"</p></div><button class="close" on:click=move |_| {if !merchant_busy.get(){set_new_merchant.set(false)}}>"×"</button></div>
                       <form on:submit=move |ev| { ev.prevent_default(); create_merchant(merchant_name.get(), merchant_prepay.get(), set_merchants, set_new_merchant, merchant_busy, set_notice); }><label>"商户名称"<input required prop:value=move || merchant_name.get() on:input=move |ev| set_merchant_name.set(event_target_value(&ev)) placeholder="请输入商户名称" /></label><label hidden=move||!access.can("merchants.adjust")>"初始预付金额"<input type="number" min="0" step="0.01" prop:value=move || merchant_prepay.get() on:input=move |ev| set_merchant_prepay.set(event_target_value(&ev)) placeholder="0.00" /></label><div class="form-actions"><button type="button" class="button secondary" on:click=move |_| {if !merchant_busy.get(){set_new_merchant.set(false)}}>"取消"</button><button class="button primary" type="submit" disabled=move||merchant_busy.get()>"保存商户"</button></div></form>
                   </div></div>
               </Show>
           </div>
       }
}

#[component]
fn NavItem(
    page: &'static str,
    current: ReadSignal<String>,
    set_page: WriteSignal<String>,
    icon: &'static str,
    label: &'static str,
) -> impl IntoView {
    let access = access::access();
    view! { <button hidden=move||!access.page(page) class:active=move || current.get()==page class="nav-item" on:click=move |_| set_page.set(page.into())><span class="nav-icon">{icon}</span><span>{label}</span></button> }
}
#[component]
fn NavGroup(
    name: &'static str,
    label: &'static str,
    icon: &'static str,
    open_group: ReadSignal<String>,
    set_open_group: WriteSignal<String>,
    children: Children,
) -> impl IntoView {
    let access = access::access();
    let pages: &[&str] = match name {
        "merchant" => &["products", "merchants", "merchant_settlement"],
        "channels" => &[
            "channels",
            "receivers",
            "complaints",
            "methods",
            "plugins",
            "official",
        ],
        "finance" => &[
            "finance_channels",
            "finance_products",
            "finance_merchants",
            "prepay",
            "statistics",
        ],
        "settings" => &[
            "site_settings",
            "blacklist",
            "pay_settings",
            "quick_login",
            "accounts",
            "roles",
        ],
        "other" => &["transfers", "logs", "user_ids"],
        _ => &[],
    };
    let children = children();
    view! { <div class="nav-group" hidden=move||!pages.iter().any(|p|access.page(p))><button class:group-open=move || open_group.get()==name class="nav-group-title" on:click=move |_| set_open_group.update(|v|*v=if v==name {String::new()} else {name.into()})><span class="nav-icon">{icon}</span><span>{label}</span><span class="chevron">"⌄"</span></button><div class:collapsed=move || open_group.get()!=name class="nav-children">{children}</div></div> }
}

#[component]
fn Home(
    dashboard: ReadSignal<Option<Dashboard>>,
    orders: ReadSignal<Vec<PaymentOrder>>,
    channels: ReadSignal<Vec<Channel>>,
    merchants: ReadSignal<Vec<Merchant>>,
    set_page: WriteSignal<String>,
    set_detail: WriteSignal<Option<PaymentOrder>>,
) -> impl IntoView {
    let access = access::access();
    view! { <div class="page-heading"><div><h1>"后台管理首页"</h1><p>"支付通道与收付款订单运营概况"</p></div><span class="date-chip">{today_label()}</span></div>
        <div class="notice-banner"><span class="notice-icon">"!"</span><strong>"已连接本地数据库。当前支付适配器为本地模拟通道。"</strong></div>
        <div class="metric-grid">
            <Metric label="今日充值" value=move || dashboard.get().map(|d|d.today_orders.to_string()).unwrap_or("—".into()) hint="订单数" icon="▤" color="blue" />
            <Metric label="成交笔数" value=move || dashboard.get().map(|d|d.today_success.to_string()).unwrap_or("—".into()) hint="成功订单" icon="✓" color="green" />
            <Metric label="成交金额（元）" value=move || dashboard.get().map(|d|format!("¥ {:.2}",d.today_amount)).unwrap_or("—".into()) hint="今日成功金额" icon="￥" color="purple" />
            <Metric label="成功率" value=move || dashboard.get().map(|d|format!("{:.1}%",d.success_rate)).unwrap_or("—".into()) hint="成功 / 总订单" icon="↗" color="orange" />
            <Show when=move||access.internal()><Metric label="平台利润（元）" value=move || dashboard.get().map(|d|format!("¥ {:.2}",d.today_profit)).unwrap_or("—".into()) hint="手续费收入 − 通道成本" icon="◆" color="cyan" /></Show>
        </div>
        <div class="summary-strip"><div><span>"当月成交金额"</span><strong>{move || format!("¥ {:.2}",dashboard.get().map(|d|d.month_amount).unwrap_or(0.0))}</strong></div><div><span>"昨日下单笔数"</span><strong>{move || dashboard.get().map(|d|d.yesterday_orders.to_string()).unwrap_or("0".into())}</strong></div><div><span>"昨日成交金额"</span><strong>{move||format!("¥ {:.2}",dashboard.get().map(|d|d.yesterday_amount).unwrap_or(0.0))}</strong></div><div><span>"今日通道数"</span><strong>{move || channels.get().len().to_string()}</strong></div><div><span>"商户数量"</span><strong>{move || merchants.get().len().to_string()}</strong></div></div>
        <div class="dashboard-grid"><section class="panel chart-panel"><div class="section-title"><div><h2>"近 12 时段成交趋势"</h2><p>"支付订单金额统计"</p></div><span class="subtle-pill">"今日"</span></div><div class="bar-chart">{move || { let vals=dashboard.get().map(|d|d.trend).unwrap_or_default(); vals.into_iter().enumerate().map(|(i,v)| { let maximum=dashboard.get().map(|d|d.trend.into_iter().fold(1.0_f64,f64::max)).unwrap_or(1.0);let h=(v/maximum*85.0).max(1.0); view! { <div class="bar-col"><span class="bar-value">{format!("{v:.0}")}</span><i style=format!("height:{h}%")></i><small>{format!("{:02}:00",i*2)}</small></div> } }).collect_view() }}</div></section>
            <section class="panel channel-panel" hidden=move||!access.can("channels.view")><div class="section-title"><div><h2>"当天通道统计"</h2><p>"各支付通道收款概况"</p></div><button class="text-button" on:click=move |_| set_page.set("channels".into())>"通道管理 →"</button></div><For each=move || channels.get() key=|c|format!("{:?}",c) let:channel><div class="channel-row"><span class="channel-logo">{method_icon(&channel.method)}</span><div class="grow"><strong>{channel.name}</strong><small>{format!("通道 {} · 费率 {:.2}%",channel.id,channel.fee_rate)}</small></div><strong>{format!("¥ {:.2}",channel.today_amount)}</strong><span class:badge-on=channel.enabled class="status-badge">{if channel.enabled{"已开启"}else{"已关闭"}}</span></div></For></section></div>
        <div class="dashboard-grid lower-grid"><section class="panel" hidden=move||!access.can("orders.view")><div class="section-title"><div><h2>"最新收款订单"</h2><p>"最近创建的交易"</p></div><button class="text-button" on:click=move |_| set_page.set("orders".into())>"查看全部 →"</button></div><OrderTable orders=orders set_detail=set_detail /></section>
            <section class="panel ranking-panel"><div class="section-title"><div><h2>"当天商户排行"</h2><p>"按订单数量排序"</p></div></div><For each=move || merchants.get() key=|m|format!("{:?}",m) let:merchant><div class="rank-row"><span class="rank-num">{merchant.id}</span><div class="grow"><strong>{merchant.name}</strong><small>{format!("今日 {} 笔 · 成功率 {:.1}%",merchant.today_count,merchant.today_rate)}</small></div><span>{format!("¥ {:.2}",merchant.prepaid)}</span></div></For></section>
        </div>
    }
}

#[component]
fn Metric(
    label: &'static str,
    value: impl Fn() -> String + Send + Sync + 'static,
    hint: &'static str,
    icon: &'static str,
    color: &'static str,
) -> impl IntoView {
    view! { <article class="metric-card"><div class="metric-top"><span>{label}</span><i class=format!("metric-icon {color}")>{icon}</i></div><strong>{value}</strong><small>{hint}</small></article> }
}

#[component]
fn PaymentPage(
    archive: bool,
    payments: ReadSignal<Vec<PaymentOrder>>,
    set_payments: WriteSignal<Vec<PaymentOrder>>,
    q: ReadSignal<String>,
    set_q: WriteSignal<String>,
    status: ReadSignal<String>,
    set_status: WriteSignal<String>,
    method: ReadSignal<String>,
    set_method: WriteSignal<String>,
    set_detail: WriteSignal<Option<PaymentOrder>>,
    set_notice: WriteSignal<String>,
    readonly: bool,
) -> impl IntoView {
    let access = access::access();
    let permission_page = if archive { "history" } else { "orders" };
    let qr_order = RwSignal::new(None::<PaymentOrder>);
    let auto_refresh = RwSignal::new(true);
    let current_page = RwSignal::new(1_i32);
    let total = RwSignal::new(0_i32);
    let generation = RwSignal::new(0_u64);
    let loading = RwSignal::new(false);
    let error = RwSignal::new(String::new());
    let reload = move || {
        loading.set(true);
        let version = generation.get_untracked() + 1;
        generation.set(version);
        let path = format!(
            "/api/payments?archive={archive}&q={}&status={}&method={}&page={}&pageSize=20",
            url_encode(&q.get()),
            url_encode(&status.get()),
            url_encode(&method.get()),
            current_page.get()
        );
        spawn_local(async move {
            match api("GET", &path, None).await {
                Ok(v) => {
                    if generation.try_get_untracked() == Some(version) {
                        set_payments
                            .set(serde_json::from_value(v["items"].clone()).unwrap_or_default());
                        total.set(v["total"].as_i64().unwrap_or(0) as i32);
                        error.set(String::new());
                        loading.set(false);
                    }
                }
                Err(e) => {
                    if generation.try_get_untracked() == Some(version) {
                        error.set(e);
                        loading.set(false);
                    }
                }
            }
        });
    };
    Effect::new(move |_| {
        let _ = (q.get(), status.get(), method.get());
        current_page.set(1);
    });
    Effect::new(move |_| reload());
    let apply_update = Callback::new(move |updated: PaymentOrder| {
        if generation.try_get_untracked().is_none() {
            return;
        }
        let changed = payments
            .get_untracked()
            .iter()
            .any(|row| row.system_id == updated.system_id && row != &updated);
        if !changed {
            return;
        }
        generation.update(|version| *version += 1);
        loading.set(false);
        set_payments.update(|list| {
            if let Some(row) = list
                .iter_mut()
                .find(|row| row.system_id == updated.system_id)
            {
                *row = updated;
            }
        });
    });
    spawn_local(async move {
        loop {
            crate::wechat::delay(5000).await;
            if generation.try_get_untracked().is_none() {
                return;
            }
            if auto_refresh.get_untracked() && !loading.get_untracked() {
                reload()
            }
        }
    });
    let title = if archive {
        "历史订单"
    } else {
        "收款订单"
    };
    view! { <div class="page-heading"><div><h1>{title}</h1><p>{if archive{"已归档的收款交易记录"}else{"查询、跟踪和核对平台收款订单"}}</p></div><div class="heading-actions"><OrderCreator readonly=readonly on_created=Callback::new(move |_|reload())/><a class="button secondary export-link" hidden=move||!access.can(&format!("{permission_page}.export")) href=move||format!("/api/payments?archive={archive}&export=csv&q={}&status={}&method={}",url_encode(&q.get()),url_encode(&status.get()),url_encode(&method.get()))>"导出 CSV"</a></div></div>
        <section class="panel filter-panel"><div class="filters"><label class="search-field"><span>"系统单号 / 商户单号 / 商户"</span><input prop:value=move || q.get() on:input=move |ev| set_q.set(event_target_value(&ev)) placeholder="输入关键字" /></label><label><span>"支付方式"</span><select prop:value=move || method.get() on:change=move |ev| set_method.set(event_target_value(&ev))><option>"所有支付方式"</option><option>"支付宝"</option><option>"微信支付"</option><option>"网银支付"</option></select></label><label><span>"订单状态"</span><select prop:value=move || status.get() on:change=move |ev| set_status.set(event_target_value(&ev))><option>"全部状态"</option><option>"未支付"</option><option>"处理中"</option><option>"已支付"</option><option>"支付失败"</option><option>"已关闭"</option></select></label><button class="button primary" on:click=move |_| reload()>"搜索"</button><button class="button secondary" on:click=move |_| {set_q.set(String::new());set_status.set("全部状态".into());set_method.set("所有支付方式".into());reload();}>"重置"</button></div></section>
        <p class="form-error" role="alert">{move||error.get()}</p><section class="panel table-panel"><div class="table-toolbar"><div><strong>{title}</strong><small>{move || format!("共 {} 条记录",total.get())}</small><label class="auto-refresh-toggle"><input type="checkbox" prop:checked=move||auto_refresh.get() on:change=move|ev|auto_refresh.set(event_target_checked(&ev))/><span>"每5秒刷新状态"</span></label></div><button class="button secondary small-button" on:click=move |_| reload()>"↻ 刷新"</button></div><div class="table-scroll"><table><thead><tr><th>"系统单号"<small>"充值单号"</small></th><th>"商户单号"<small>"商户名称"</small></th><th>"产品名称"<small>"订单金额"</small></th><th>"通道名称"<small>"通道 ID"</small></th><th>"充值时间"<small>"回调时间"</small></th><th>"支付状态"</th><th>"收款二维码"</th><th>"操作"</th></tr></thead><tbody>
            <For each=move || payments.get() key=|o|format!("{:?}",o) let:order><tr><td><button class="id-link" on:click={let order=order.clone();move |_| set_detail.set(Some(order.clone()))}>{order.system_id.clone()}</button><small>{order.recharge_id.clone()}</small></td><td>{order.merchant_order.clone()}<small>{format!("{} · {}",order.merchant_id,order.merchant_name)}</small></td><td>{order.product_name.clone()}<small>{format!("¥ {:.2}",order.amount)}</small></td><td>{order.channel_name.clone()}<small>{format!("通道 {} · {}",order.channel_id,order.method)}</small></td><td>{order.created_at.clone()}<small>{if order.callback_at.is_empty(){"—".to_string()}else{order.callback_at.clone()}}</small></td><td><span class=status_class(order.status.as_str())>{order.status.clone()}</span><small>{if order.plugin=="wechat_jsapi"&&!order.can_pay&&order.status=="未支付"{"支付期限已过，等待确认"}else{""}}</small></td><td>{if access.can(&format!("{permission_page}.qr"))&&!order.qr_image.is_empty(){let qr=order.clone();view!{<button class="order-qr-button" on:click=move |_|qr_order.set(Some(qr.clone()))><img src=order.qr_image.clone() loading="lazy" width="44" height="44" alt="订单收款二维码预览"/><span>"查看二维码"</span></button>}.into_any()}else{view!{<small>{if order.plugin=="demo"{"模拟通道"}else{"暂无二维码"}}</small>}.into_any()}}</td><td><button class="text-button" hidden=move||!access.can(&format!("{permission_page}.query")) disabled=readonly on:click={let id=order.system_id.clone();move |_| {let id=id.clone();spawn_local(async move {match api("POST",&format!("/api/payments/{id}/query"),None).await{Ok(v)=>{match serde_json::from_value::<PaymentOrder>(v.get("order").cloned().unwrap_or(v)){Ok(latest)=>{set_notice.set(format!("订单 {}：{}",latest.merchant_order,latest.status));apply_update.run(latest)},Err(_)=>set_notice.set("订单响应格式无效".into())}},Err(e)=>set_notice.set(e)}});}} >"查单"</button><button class="text-button" on:click={let order=order.clone();move |_| set_detail.set(Some(order.clone()))}>"详情"</button></td></tr></For>
        </tbody></table><Show when=move || payments.get().is_empty()><div class="empty-state">"没有找到匹配的订单"</div></Show></div><div class="pagination"><span>{move || format!("显示 {} 条",payments.get().len())}</span><button disabled={move||current_page.get()<=1} on:click=move |_|current_page.update(|p|*p-=1)>"‹"</button><button class="page-current">{move||current_page.get()}</button><button disabled={move||current_page.get()*20>=total.get()} on:click=move |_|current_page.update(|p|*p+=1)>"›"</button><span>"本地数据库"</span></div></section>
        {move||qr_order.get().map(|order|view!{<crate::orders::OrderQRCodeDialog order=order readonly=readonly on_close=Callback::new(move |_|qr_order.set(None)) on_update=apply_update/>})}
    }
}

#[component]
fn OrderTable(
    orders: ReadSignal<Vec<PaymentOrder>>,
    set_detail: WriteSignal<Option<PaymentOrder>>,
) -> impl IntoView {
    view! { <div class="table-scroll compact-table"><table><thead><tr><th>"系统单号"</th><th>"商户"</th><th>"产品 / 金额"</th><th>"状态"</th><th>"时间"</th></tr></thead><tbody><For each={move || orders.get().into_iter().take(5).collect::<Vec<_>>()} key=|o|format!("{:?}",o) let:order><tr><td><button class="id-link" on:click={let order=order.clone();move |_| set_detail.set(Some(order.clone()))}>{order.system_id.clone()}</button></td><td>{format!("{} · {}",order.merchant_id,order.merchant_name)}</td><td>{format!("{} · ¥ {:.2}",order.product_name,order.amount)}</td><td><span class=status_class(order.status.as_str())>{order.status.clone()}</span></td><td>{order.created_at.clone()}</td></tr></For></tbody></table></div> }
}

#[component]
fn MerchantPage(
    prepay_key: RwSignal<String>,
    readonly: bool,
    merchants: ReadSignal<Vec<Merchant>>,
    set_merchants: WriteSignal<Vec<Merchant>>,
    set_prepay_merchant: WriteSignal<Option<Merchant>>,
    set_prepay_amount: WriteSignal<String>,
    set_new_merchant: WriteSignal<bool>,
    set_notice: WriteSignal<String>,
) -> impl IntoView {
    let access = access::access();
    let (merchant_q, set_merchant_q) = signal(String::new());
    let (merchant_state, set_merchant_state) = signal("全部用户".to_string());
    view! { <div class="page-heading"><div><h1>"商户列表"</h1><p>"管理接入商户、预付余额和交易权限。"</p></div><button class="button primary" hidden=move||!access.can("merchants.create") disabled=readonly on:click=move |_| set_new_merchant.set(true)>"＋ 添加商户"</button></div>
        <section class="panel filter-panel"><div class="filters"><label><span>"商户名称 / 商户号"</span><input prop:value=move || merchant_q.get() on:input=move |ev|set_merchant_q.set(event_target_value(&ev)) placeholder="搜索商户" /></label><label><span>"用户状态"</span><select prop:value=move || merchant_state.get() on:change=move |ev|set_merchant_state.set(event_target_value(&ev))><option>"全部用户"</option><option>"正常"</option><option>"已关闭"</option></select></label><button class="button primary" on:click=move |_| {let _=merchant_q.get();} >"搜索"</button><button class="button secondary" on:click=move |_| {set_merchant_q.set(String::new());set_merchant_state.set("全部用户".into());} >"重置"</button></div></section>
        <section class="panel table-panel"><div class="table-toolbar"><div><strong>"商户列表"</strong><small>{move || format!("共 {} 个商户",merchants.get().len())}</small></div><button class="button secondary small-button" on:click=move |_|fetch_merchants(set_merchants)>"刷新"</button></div><div class="table-scroll"><table><thead><tr><th>"商户号 / 商户名"</th><th>"今量 / 成率"</th><th>"昨量 / 成率"</th><th>"30m量"</th><th>"10m量"</th><th>"预付"</th><th>"剩余预付"</th><th>"总预付"</th><th>"状态"</th><th>"操作"</th></tr></thead><tbody>
            <For each={move || {let query=merchant_q.get().to_lowercase();let state=merchant_state.get();merchants.get().into_iter().filter(|m|(query.is_empty()||m.name.to_lowercase().contains(&query)||m.id.to_lowercase().contains(&query))&&(state=="全部用户"||(state=="正常"&&m.enabled)||(state=="已关闭"&&!m.enabled))).collect::<Vec<_>>()}} key=|m|format!("{:?}",m) let:merchant><tr><td><strong>{merchant.id.clone()}</strong><small>{merchant.name.clone()}</small></td><td>{merchant.today_count}<small>{format!("成功率 {:.1}%",merchant.today_rate)}</small></td><td>{merchant.yesterday_count}<small>"昨日"</small></td><td>{merchant.recent30m}</td><td>{merchant.recent10m}</td><td>{format!("¥ {:.2}",merchant.prepaid)}</td><td><strong>{format!("¥ {:.2}",merchant.remaining)}</strong></td><td>{format!("¥ {:.2}",merchant.total_prepay)}</td><td><span class:badge-on=merchant.enabled class="status-badge">{if merchant.enabled{"正常"}else{"已关闭"}}</span></td><td><button class="text-button" hidden=move||!access.can("merchants.adjust") disabled=readonly on:click={let merchant=merchant.clone();move |_| {prepay_key.set(format!("{:.0}-{}",js_sys::Date::now(),js_sys::Math::random()));set_prepay_amount.set(String::new());set_prepay_merchant.set(Some(merchant.clone()));}}>"增减预付"</button><button class="text-button" hidden=move||!access.can("merchants.toggle") disabled=readonly on:click={let id=merchant.id.clone();move |_| toggle_merchant(id.clone(),set_merchants,set_notice)}>"切换状态"</button><MerchantEditor merchant=merchant.clone() readonly=!access.can("merchants.edit") on_saved=Callback::new(move |_|fetch_merchants(set_merchants))/>{if access.can("merchants.apikey"){view!{<crate::orders::MerchantApiAccess merchant_id=merchant.id.clone()/>}.into_any()}else{view!{<span></span>}.into_any()}}</td></tr></For>
        </tbody></table></div></section>
    }
}

#[component]
fn Detail(label: &'static str, value: String) -> impl IntoView {
    view! { <div class="detail-item"><small>{label}</small><strong>{value}</strong></div> }
}

#[component]
fn RobotPage() -> impl IntoView {
    let command_rows = vec![
        (
            "U价命令",
            "mz / mw / my",
            "查询欧意买价；支付宝、微信、银行卡",
            "所有人",
        ),
        (
            "预付命令",
            "+金额 / -金额",
            "为商户或供应商增加、减少预付",
            "管理员",
        ),
        ("查单", "查单 + 订单号", "查询支付订单状态", "所有人"),
        (
            "查产品",
            "查产品 + 编号",
            "查询产品费率和启用状态",
            "所有人",
        ),
        (
            "查通道",
            "查通道 + 编号",
            "查询通道费率和启用状态",
            "所有人",
        ),
        (
            "跑量",
            "跑量 / 昨日跑量",
            "查询当天或昨日交易数据",
            "所有人",
        ),
        (
            "绑定群",
            "绑定商户 + 名称",
            "将机器人群组关联到商户",
            "管理员",
        ),
        (
            "广播",
            "广播至分组 + 名称",
            "向指定商户群广播消息",
            "管理员",
        ),
    ];
    view! { <div class="page-heading"><div><h1>"机器人命令"</h1><p>"商户和运营群可使用的机器人指令说明。"</p></div></div><section class="panel table-panel"><div class="table-scroll"><table><thead><tr><th>"命令类型"</th><th>"指令"</th><th>"说明"</th><th>"权限"</th></tr></thead><tbody>{command_rows.into_iter().map(|(a,b,c,d)|view!{<tr><td>{a}</td><td><code>{b}</code></td><td>{c}</td><td><span class="status-badge">{d}</span></td></tr>}).collect_view()}</tbody></table></div></section> }
}

fn fetch_dashboard(set: WriteSignal<Option<Dashboard>>) {
    spawn_local(async move {
        if let Ok(v) = api("GET", "/api/dashboard", None).await {
            if let Ok(data) = serde_json::from_value(v) {
                set.set(Some(data))
            }
        }
    });
}
fn fetch_payments(
    set: WriteSignal<Vec<PaymentOrder>>,
    q: String,
    status: String,
    method: String,
    archive: bool,
) {
    spawn_local(async move {
        let url = format!(
            "/api/payments?archive={archive}&q={}&status={}&method={}",
            url_encode(&q),
            url_encode(&status),
            url_encode(&method)
        );
        if let Ok(v) = api("GET", &url, None).await {
            if let Ok(data) = serde_json::from_value(v) {
                set.set(data)
            }
        }
    });
}
fn fetch_merchants(set: WriteSignal<Vec<Merchant>>) {
    spawn_local(async move {
        if let Ok(v) = api("GET", "/api/merchants", None).await {
            if let Ok(data) = serde_json::from_value(v) {
                set.set(data)
            }
        }
    });
}
fn fetch_products(set: WriteSignal<Vec<Product>>) {
    spawn_local(async move {
        if let Ok(v) = api("GET", "/api/products", None).await {
            if let Ok(data) = serde_json::from_value(v) {
                set.set(data)
            }
        }
    });
}
fn fetch_channels(set: WriteSignal<Vec<Channel>>) {
    spawn_local(async move {
        if let Ok(v) = api("GET", "/api/channels", None).await {
            if let Ok(data) = serde_json::from_value(v) {
                set.set(data)
            }
        }
    });
}
fn toggle_merchant(id: String, set: WriteSignal<Vec<Merchant>>, notice: WriteSignal<String>) {
    spawn_local(async move {
        match api("POST", &format!("/api/merchants/{id}/toggle"), None).await {
            Ok(_) => {
                fetch_merchants(set);
                notice.set("商户状态已保存".into());
            }
            Err(e) => notice.set(e),
        }
    });
}
fn adjust_prepay(
    merchant: Option<Merchant>,
    amount: String,
    key: String,
    set: WriteSignal<Vec<Merchant>>,
    close: WriteSignal<Option<Merchant>>,
    busy: RwSignal<bool>,
    notice: WriteSignal<String>,
) {
    if busy.get() {
        return;
    }
    let Some(m) = merchant else { return };
    let Ok(value) = amount.parse::<f64>() else {
        notice.set("请输入有效金额".into());
        return;
    };
    busy.set(true);
    let body = serde_json::json!({"amount":value,"idempotencyKey":key,"note":"后台预付调整"});
    spawn_local(async move {
        match api(
            "POST",
            &format!("/api/merchants/{}/prepay", m.id),
            Some(body),
        )
        .await
        {
            Ok(_) => {
                fetch_merchants(set);
                close.set(None);
                notice.set("预付调整已保存，流水已生成".into());
            }
            Err(e) => notice.set(e),
        }
        busy.set(false);
    });
}
fn create_merchant(
    name: String,
    prepaid: String,
    set: WriteSignal<Vec<Merchant>>,
    close: WriteSignal<bool>,
    busy: RwSignal<bool>,
    notice: WriteSignal<String>,
) {
    if busy.get() {
        return;
    }
    let amount = match prepaid.parse::<f64>() {
        Ok(v) => v,
        Err(_) if prepaid.trim().is_empty() => 0.0,
        Err(_) => {
            notice.set("请输入有效预付金额".into());
            return;
        }
    };
    busy.set(true);
    let body = serde_json::json!({"name":name,"prepaid":amount});
    spawn_local(async move {
        match api("POST", "/api/merchants", Some(body)).await {
            Ok(_) => {
                fetch_merchants(set);
                close.set(false);
                notice.set("商户已保存".into());
            }
            Err(e) => notice.set(e),
        }
        busy.set(false);
    });
}
fn url_encode(s: &str) -> String {
    js_sys::encode_uri_component(s)
        .as_string()
        .unwrap_or_default()
}
fn page_title(p: &str) -> &'static str {
    match p {
        "home" => "后台管理首页",
        "orders" => "收款订单",
        "history" => "历史订单",
        "products" => "产品设置",
        "merchants" => "商户列表",
        "merchant_settlement" => "商户对账",
        "channels" => "通道配置",
        "receivers" => "收钱吧管理",
        "complaints" => "投诉管理",
        "methods" => "支付方式",
        "plugins" => "支付插件",
        "official" => "公众号小程序",
        "finance_channels" => "通道数据",
        "finance_products" => "产品数据",
        "finance_merchants" => "商户数据",
        "prepay" => "预付明细",
        "statistics" => "订单统计",
        "robot" => "机器人命令",
        "blacklist" => "黑名单管理",
        "transfers" => "付款记录",
        "logs" => "登录日志",
        "user_ids" => "用户标识",
        "site_settings" => "网站信息配置",
        "pay_settings" => "支付相关配置",
        "quick_login" => "快捷登录配置",
        "accounts" => "用户管理",
        "roles" => "角色与权限",
        _ => "支付管理",
    }
}
fn status_class(s: &str) -> String {
    match s {
        "已支付" => "state-tag state-success".into(),
        "支付失败" => "state-tag state-danger".into(),
        "处理中" => "state-tag state-warn".into(),
        _ => "state-tag state-muted".into(),
    }
}
fn method_icon(s: &str) -> &'static str {
    if s.contains("支付宝") {
        "支"
    } else if s.contains("微信") {
        "微"
    } else {
        "银"
    }
}
fn today_label() -> String {
    js_sys::Date::new_0()
        .to_locale_date_string("zh-CN", &JsValue::UNDEFINED)
        .as_string()
        .unwrap_or_default()
}
fn main() {
    leptos::mount::mount_to_body(wechat::Root);
}
