use crate::{Channel, Detail, FinanceRow, Merchant, PaymentOrder, Product};
use gloo_net::http::Request;
use leptos::prelude::*;
use serde::Deserialize;
use serde_json::{Value, json};
use wasm_bindgen_futures::spawn_local;

#[derive(Clone, Debug, Deserialize, Default, PartialEq)]
#[serde(default, rename_all = "camelCase")]
pub struct User {
    pub id: String,
    pub identity: String,
    pub merchant_id: String,
    pub enabled: bool,
    #[serde(rename = "super")]
    pub super_user: bool,
    pub role_ids: Vec<String>,
    pub permissions: Vec<String>,
    pub name: String,
    pub role: String,
}
#[derive(Clone, Debug, Deserialize)]
#[serde(rename_all = "camelCase")]
pub struct AuthStatus {
    pub needs_setup: bool,
    pub authenticated: bool,
    pub user: User,
}
pub async fn api(method: &str, path: &str, body: Option<Value>) -> Result<Value, String> {
    let req = match method {
        "POST" => Request::post(path),
        "PATCH" => Request::patch(path),
        _ => Request::get(path),
    };
    let response = if let Some(value) = body {
        req.json(&value)
            .map_err(|_| "无法提交数据".to_string())?
            .send()
            .await
    } else {
        req.send().await
    }
    .map_err(|_| "无法连接本地服务器".to_string())?;
    if response.status() == 401
        && !path.starts_with("/api/auth/")
        && !path.starts_with("/api/cashier/")
    {
        if let Some(window) = web_sys::window() {
            let _ = window.location().reload();
        }
    }
    let ok = response.ok();
    let value = response
        .json::<Value>()
        .await
        .map_err(|_| "服务器响应无效".to_string())?;
    if !ok {
        return Err(value
            .get("error")
            .and_then(Value::as_str)
            .unwrap_or("操作失败")
            .to_string());
    };
    Ok(value)
}
fn auth_refresh(auth: RwSignal<Option<AuthStatus>>) {
    spawn_local(async move {
        if let Ok(v) = api("GET", "/api/auth/status", None).await {
            if let Ok(s) = serde_json::from_value(v) {
                auth.set(Some(s))
            }
        }
    });
}
#[component]
pub fn App() -> impl IntoView {
    let auth = RwSignal::new(None::<AuthStatus>);
    Effect::new(move |_| auth_refresh(auth));
    view! { {move ||match auth.get(){None=>view!{<div class="auth-shell"><p>"正在连接本地数据库…"</p></div>}.into_any(),Some(s) if s.authenticated=>view!{<crate::Console user=s.user on_logout=Callback::new(move |_|{spawn_local(async move{let _=api("POST","/api/auth/logout",None).await;auth_refresh(auth);});})/>}.into_any(),Some(s)=>view!{<Login needs_setup=s.needs_setup auth=auth/>}.into_any()}} }
}
#[component]
fn Login(needs_setup: bool, auth: RwSignal<Option<AuthStatus>>) -> impl IntoView {
    let name = RwSignal::new("admin".to_string());
    let password = RwSignal::new(String::new());
    let error = RwSignal::new(String::new());
    let busy = RwSignal::new(false);
    view! {<div class="auth-shell"><form class="panel auth-card" on:submit=move |ev|{ev.prevent_default();if busy.get(){return}busy.set(true);error.set(String::new());let body=json!({"name":name.get(),"password":password.get()});spawn_local(async move{match api("POST",if needs_setup{"/api/auth/setup"}else{"/api/auth/login"},Some(body)).await{Ok(_)=>{password.set(String::new());auth_refresh(auth)},Err(e)=>error.set(e)}busy.set(false);});}>
    <span class="auth-logo">"西"</span><h1>"支付通道管理平台"</h1><p>{if needs_setup{"首次使用，请创建超级管理员账号。"}else{"登录本地管理平台"}}</p><label>"账号"<input required autocomplete="username" prop:value=move||name.get() on:input=move|ev|name.set(event_target_value(&ev))/></label><label>"密码"<input required type="password" minlength="10" maxlength="72" autocomplete=if needs_setup{"new-password"}else{"current-password"} prop:value=move||password.get() on:input=move|ev|password.set(event_target_value(&ev)) placeholder="至少 10 个字符"/></label><Show when=move||!error.get().is_empty()><p class="form-error" role="alert">{move||error.get()}</p></Show><button class="button primary" disabled=move||busy.get()>{move||if busy.get(){"正在提交…"}else if needs_setup{"创建超管并进入"}else{"登录"}}</button><small>"数据保存在本机；真实支付需另行接入通道。"</small></form></div>}
}
#[derive(Clone)]
struct Field {
    key: &'static str,
    label: &'static str,
    kind: &'static str,
    options: &'static str,
}
fn f(key: &'static str, label: &'static str, kind: &'static str) -> Field {
    Field {
        key,
        label,
        kind,
        options: "",
    }
}
fn select(key: &'static str, label: &'static str, options: &'static str) -> Field {
    Field {
        key,
        label,
        kind: "select",
        options,
    }
}
fn fields(kind: &str) -> Vec<Field> {
    match kind {
        "products" => vec![
            f("code", "产品编码", "text"),
            f("name", "产品名称", "text"),
            f("feeRate", "商户手续费率（%）", "number"),
            select("method", "支付方式", "支付宝|微信支付|网银支付"),
            select("enabled", "状态", "true|false"),
        ],
        "channels" => vec![
            f("name", "通道名称", "text"),
            f("feeRate", "通道费率（%）", "number"),
            f("minAmount", "最小金额（元）", "number"),
            f("maxAmount", "最大金额（元）", "number"),
            select("method", "支付方式", "支付宝|微信支付|网银支付"),
            select("plugin", "插件", "demo|wechat_jsapi"),
            select("enabled", "状态", "true|false"),
        ],
        "blacklist" => vec![
            f("name", "商户号或商户名称", "text"),
            select("status", "状态", "生效|停用"),
            f("note", "原因", "text"),
        ],
        "official" | "receivers" | "user_ids" => vec![
            f("name", "名称", "text"),
            f("reference", "登记标识", "text"),
            select("status", "状态", statuses(kind)),
            f("note", "备注", "text"),
        ],
        _ => vec![
            f("name", "名称 / 标题", "text"),
            f("reference", "关联订单 / 标识", "text"),
            f("amount", "登记金额（元）", "number"),
            select("status", "状态", statuses(kind)),
            f("note", "备注 / 处理说明", "text"),
        ],
    }
}
fn statuses(kind: &str) -> &'static str {
    match kind {
        "complaints" => "待处理|处理中|已解决|已撤销",
        "transfers" => "待付款|已付款|失败|已取消",
        "blacklist" => "生效|停用",
        "user_ids" => "有效|停用",
        _ => "启用|停用",
    }
}
fn title(kind: &str) -> &'static str {
    match kind {
        "products" => "产品设置",
        "channels" => "通道配置",
        "complaints" => "投诉管理",
        "transfers" => "付款记录登记",
        "receivers" => "收钱吧商户资料",
        "official" => "公众号小程序资料",
        "blacklist" => "黑名单管理",
        "user_ids" => "用户标识登记",
        "users" => "账号权限管理",
        _ => "管理记录",
    }
}
fn endpoint(kind: &str) -> String {
    match kind {
        "products" | "channels" | "users" => format!("/api/{kind}"),
        _ => format!("/api/records/{kind}"),
    }
}
fn text(v: &Value, key: &str) -> String {
    match v.get(key) {
        Some(Value::String(s)) => s.clone(),
        Some(Value::Bool(b)) => {
            if *b {
                "true".into()
            } else {
                "false".into()
            }
        }
        Some(Value::Number(n)) => n.to_string(),
        _ => String::new(),
    }
}
fn display(v: &Value, key: &str) -> String {
    match key {
        "enabled" => {
            if v[key].as_bool().unwrap_or(false) {
                "已开启".into()
            } else {
                "已关闭".into()
            }
        }
        "role" => match v[key].as_str().unwrap_or("") {
            "admin" => "管理员",
            "operator" => "操作员",
            _ => "只读",
        }
        .into(),
        "feeRate" | "minAmount" | "maxAmount" | "amount" => {
            format!("{:.2}", v[key].as_f64().unwrap_or(0.0))
        }
        _ => text(v, key),
    }
}
fn load(kind: &'static str, rows: RwSignal<Vec<Value>>, error: RwSignal<String>) {
    spawn_local(async move {
        match api("GET", &endpoint(kind), None).await {
            Ok(v) => rows.set(v.as_array().cloned().unwrap_or_default()),
            Err(e) => error.set(e),
        }
    });
}
#[component]
pub fn ManagedPage(kind: &'static str, readonly: bool) -> impl IntoView {
    let access = crate::access::access();
    let rows = RwSignal::new(Vec::<Value>::new());
    let editor = RwSignal::new(None::<Value>);
    let original = RwSignal::new(String::new());
    let error = RwSignal::new(String::new());
    let notice = RwSignal::new(String::new());
    let query = RwSignal::new(String::new());
    let busy = RwSignal::new(false);
    Effect::new(move |_| load(kind, rows, error));
    let specs = fields(kind);
    let columns = fields(kind)
        .into_iter()
        .filter(|f| f.kind != "password")
        .collect::<Vec<_>>();
    let header = columns.clone();
    let form_fields = specs.clone();
    view! {<div class="page-heading"><div><h1>{title(kind)}</h1><p>{match kind{"channels"=>"配置费率、支付方式、限额和启停状态。","products"=>"费率更新只影响新订单；历史订单保留原手续费。","transfers"=>"登记线下付款流水与状态，保存不会发起转账。","blacklist"=>"名称填写商户号或商户名称；生效后阻止该商户创建订单。",_=>"新增、编辑和处理记录；保存到本地数据库。"}}</p></div><button class="button primary" hidden=move||!access.can(&format!("{kind}.create")) disabled=readonly on:click=move |_|{let mut v=json!({});for field in fields(kind){v[field.key]=match field.kind{"number"=>json!(if field.key=="maxAmount"{2000.0}else if field.key=="minAmount"||field.key=="feeRate"{1.0}else{0.0}),"select" if field.key=="enabled"=>json!(true),"select"=>json!(field.options.split('|').next().unwrap_or_default()),_=>json!("")}}original.set(String::new());error.set(String::new());editor.set(Some(v));}>"＋ 新增"</button></div>
    <Show when=move||!notice.get().is_empty()><div class="success-note" role="status">{move||notice.get()}</div></Show><Show when=move||!error.get().is_empty()&&editor.get().is_none()><div class="form-error" role="alert">{move||error.get()}</div></Show>
    <section class="panel filter-panel"><div class="filters"><label class="search-field"><span>"关键词"</span><input placeholder="名称、标识或状态" prop:value=move||query.get() on:input=move|ev|query.set(event_target_value(&ev))/></label><button class="button secondary" on:click=move |_|query.set(String::new())>"重置"</button><button class="button secondary" on:click=move |_|load(kind,rows,error)>"刷新"</button></div></section>
    <section class="panel table-panel"><div class="table-toolbar"><strong>{title(kind)}</strong><small>{move||format!("共 {} 条",rows.get().len())}</small></div><div class="table-scroll"><table><thead><tr>{header.into_iter().map(|f|view!{<th>{f.label}</th>}).collect_view()}<th>"操作"</th></tr></thead><tbody><For each={move||{let q=query.get().to_lowercase();rows.get().into_iter().filter(|r|q.is_empty()||r.to_string().to_lowercase().contains(&q)).collect::<Vec<_>>()}} key=move |v|v.to_string() children=move |row:Value|{let cells=columns.clone();let edit=row.clone();view!{<tr>{cells.into_iter().map(|f|view!{<td>{display(&row,f.key)}</td>}).collect_view()}<td><button class="text-button" hidden=move||!access.can(&format!("{kind}.edit")) disabled=readonly on:click=move |_|{original.set(text(&edit,if kind=="products"{"code"}else{"id"}));error.set(String::new());editor.set(Some(edit.clone()));}>"编辑"</button>{if kind=="channels" {let channel_id=text(&row,"id");view!{<button class="text-button" hidden=move||!access.can("channels.toggle") disabled=move||busy.get() on:click=move |_|{if busy.get(){return}busy.set(true);let path=format!("/api/channels/{channel_id}/toggle");spawn_local(async move{match api("POST",&path,None).await{Ok(_)=>load(kind,rows,error),Err(e)=>error.set(e)}busy.set(false);});}>"切换状态"</button>}.into_any()}else{view!{<span></span>}.into_any()}}</td></tr>}}/></tbody></table><Show when=move||rows.get().is_empty()><div class="empty-state">"暂无记录，点击新增开始使用。"</div></Show></div></section>
    <Show when=move||editor.get().is_some()><div class="modal-backdrop"><form class="modal" on:submit=move|ev|{ev.prevent_default();if busy.get(){return}busy.set(true);error.set(String::new());let data=editor.get().unwrap_or(json!({}));let existing=original.get();let path=if existing.is_empty(){endpoint(kind)}else{format!("{}/{}",endpoint(kind),existing)};spawn_local(async move{match api(if existing.is_empty(){"POST"}else{"PATCH"},&path,Some(data)).await{Ok(_)=>{editor.set(None);notice.set("已保存到本地数据库".into());load(kind,rows,error)},Err(e)=>error.set(e)}busy.set(false);});}>
    <div class="modal-head"><h2>{title(kind)}</h2><button type="button" class="close" disabled=move||busy.get() on:click=move |_|editor.set(None)>"×"</button></div><div class="editor-fields">{form_fields.clone().into_iter().map(|field|{let key=field.key;let opt=field.options;view!{<label>{field.label}{if field.kind=="select"{view!{<select disabled=move||kind=="channels"&&key=="enabled"&&!original.get().is_empty()&&!access.can("channels.toggle") prop:value=move||editor.get().map(|v|text(&v,key)).unwrap_or_default() on:change=move|ev|{let v=event_target_value(&ev);editor.update(|current|if let Some(x)=current{x[key]=if key=="enabled"{json!(v=="true")}else{json!(v)}});}>{opt.split('|').map(|v|view!{<option value=v>{match v{"true"=>"已开启","false"=>"已关闭","admin"=>"管理员","operator"=>"操作员","viewer"=>"只读","demo"=>"本地模拟","wechat_jsapi"=>"微信 JSAPI",_=>v}}</option>}).collect_view()}</select>}.into_any()}else{view!{<input type=field.kind step="0.01" required=field.key=="name"||field.key=="code"||field.kind=="password" disabled=move||key=="code"&&!original.get().is_empty() prop:value=move||editor.get().map(|v|text(&v,key)).unwrap_or_default() on:input=move|ev|{let val=event_target_value(&ev);editor.update(|current|if let Some(x)=current{x[key]=if field.kind=="number"{json!(val.parse::<f64>().unwrap_or(0.0))}else{json!(val)}});}/>} .into_any()}}</label>}}).collect_view()}</div><Show when=move||!error.get().is_empty()><p class="form-error" role="alert">{move||error.get()}</p></Show><div class="form-actions"><button type="button" class="button secondary" disabled=move||busy.get() on:click=move |_|editor.set(None)>"取消"</button><button class="button primary" disabled=move||busy.get()>{move||if busy.get(){"保存中…"}else{"保存"}}</button></div></form></div></Show>}
}
#[component]
pub fn OrderCreator(readonly: bool, on_created: Callback<()>) -> impl IntoView {
    let access = crate::access::access();
    let open = RwSignal::new(false);
    let merchants = RwSignal::new(Vec::<Merchant>::new());
    let products = RwSignal::new(Vec::<Product>::new());
    let channels = RwSignal::new(Vec::<Channel>::new());
    let error = RwSignal::new(String::new());
    let busy = RwSignal::new(false);
    let mid = RwSignal::new(String::new());
    let product = RwSignal::new(String::new());
    let channel = RwSignal::new(String::new());
    let number = RwSignal::new(String::new());
    let amount = RwSignal::new(String::new());
    view! {<button class="button primary" hidden=move||!access.can("orders.create") disabled=readonly on:click=move |_|{open.set(true);error.set(String::new());number.set(format!("LOCAL-{:.0}",js_sys::Date::now()));spawn_local(async move{for (path,kind) in [("/api/merchants",0),("/api/products",1),("/api/channels",2)]{match api("GET",path,None).await{Ok(v)=>match kind{0=>{let data:Vec<Merchant>=serde_json::from_value(v).unwrap_or_default();mid.set(data.first().map(|m|m.id.clone()).unwrap_or_default());merchants.set(data)},1=>{let data:Vec<Product>=serde_json::from_value(v).unwrap_or_default();product.set(data.first().map(|m|m.code.clone()).unwrap_or_default());products.set(data)},_=>{let data:Vec<Channel>=serde_json::from_value(v).unwrap_or_default();channel.set(data.first().map(|m|m.id.clone()).unwrap_or_default());channels.set(data)}},Err(e)=>error.set(e)}}});}>"＋ 新建收款订单"</button>
    <Show when=move||open.get()><div class="modal-backdrop"><form class="modal" on:submit=move|ev|{ev.prevent_default();if busy.get(){return}busy.set(true);error.set(String::new());let body=json!({"merchantId":mid.get(),"merchantOrder":number.get(),"productCode":product.get(),"channelId":channel.get(),"amount":amount.get().parse::<f64>().unwrap_or(0.0)});spawn_local(async move{match api("POST","/api/payments",Some(body)).await{Ok(_)=>{open.set(false);on_created.run(())},Err(e)=>error.set(e)}busy.set(false);});}><div class="modal-head"><h2>"新建收款订单"</h2><button type="button" class="close" disabled=move||busy.get() on:click=move |_|open.set(false)>"×"</button></div><div class="editor-fields"><label>"商户"<select required prop:value=move||mid.get() on:change=move|ev|mid.set(event_target_value(&ev))><For each=move||merchants.get() key=|m|format!("{:?}",m) let:m><option value=m.id disabled=!m.enabled>{m.name}</option></For></select></label><label>"商户订单号"<input required prop:value=move||number.get() on:input=move|ev|number.set(event_target_value(&ev))/></label><label>"产品"<select required prop:value=move||product.get() on:change=move|ev|{let code=event_target_value(&ev);let method=products.get().into_iter().find(|p|p.code==code).map(|p|p.method).unwrap_or_default();product.set(code);channel.set(channels.get().into_iter().find(|c|c.enabled&&c.method==method).map(|c|c.id).unwrap_or_default());}><For each=move||products.get() key=|p|format!("{:?}",p) let:p><option value=p.code disabled=!p.enabled>{p.name}</option></For></select></label><label>"支付通道"<select required prop:value=move||channel.get() on:change=move|ev|channel.set(event_target_value(&ev))><For each={move||{let code=product.get();let method=products.get().into_iter().find(|p|p.code==code).map(|p|p.method).unwrap_or_default();channels.get().into_iter().filter(|c|c.method==method).collect::<Vec<_>>()}} key=|c|format!("{:?}",c) let:c><option value=c.id disabled=!c.enabled>{c.name}</option></For></select></label><label>"订单金额（元）"<input required type="number" min="0.01" step="0.01" prop:value=move||amount.get() on:input=move|ev|amount.set(event_target_value(&ev))/></label></div><Show when=move||merchants.get().is_empty()><div class="demo-note">"请先在商户管理中新增商户。"</div></Show><p class="demo-note">"微信 JSAPI 订单创建后自动生成收款二维码，可在订单列表查看。创建时将预留商户手续费。"</p><Show when=move||!error.get().is_empty()><p class="form-error" role="alert">{move||error.get()}</p></Show><div class="form-actions"><button class="button primary" disabled=move||busy.get()||merchants.get().is_empty()>{move||if busy.get(){"创建中…"}else{"创建订单"}}</button></div></form></div></Show>}
}
#[component]
pub fn OrderDetails(
    readonly: bool,
    order: PaymentOrder,
    on_update: Callback<PaymentOrder>,
) -> impl IntoView {
    let access = crate::access::access();
    let status = order.status.clone();
    let is_wechat = order.plugin == "wechat_jsapi";
    let checkout_id = order.system_id.clone();
    let id = order.system_id.clone();
    let busy = RwSignal::new(false);
    let error = RwSignal::new(String::new());
    let perform = move |result: &'static str| {
        if busy.get() {
            return;
        }
        busy.set(true);
        let path = format!("/api/payments/{id}/simulate");
        spawn_local(async move {
            match api("POST", &path, Some(json!({"status":result}))).await {
                Ok(v) => match serde_json::from_value::<PaymentOrder>(v) {
                    Ok(o) => on_update.run(o),
                    Err(_) => error.set("订单响应无效".into()),
                },
                Err(e) => error.set(e),
            }
            busy.set(false);
        });
    };
    let success = perform.clone();
    view! {<div class="detail-grid"><Detail label="系统单号" value=order.system_id/><Detail label="商户单号" value=order.merchant_order/><Detail label="商户" value=format!("{} · {}",order.merchant_id,order.merchant_name)/><Detail label="产品" value=order.product_name/><Detail label="订单金额" value=format!("¥ {:.2}",order.amount)/><Detail label="通道" value=order.channel_name/><Detail label="支付状态" value=order.status/><Detail label="创建时间" value=order.created_at/><Detail label="回调时间" value=if order.callback_at.is_empty(){"—".into()}else{order.callback_at}/><Detail label="微信商户订单号" value=if order.provider_order.is_empty(){"—".into()}else{order.provider_order}/><Detail label="第三方流水" value=if order.provider_txn.is_empty(){"—".into()}else{order.provider_txn}/></div>{if is_wechat {view!{<crate::wechat::WechatCheckout order_id=checkout_id readonly=readonly on_update=on_update/>}.into_any()}else{view!{<p class="demo-note">"模拟结果用于本地通道，成功后扣除商户预付手续费。"</p>}.into_any()}}{if !is_wechat&&access.can("orders.simulate")&&(status=="未支付"||status=="处理中"){view!{<div class="form-actions"><button class="button secondary" disabled=move||readonly||busy.get() on:click=move |_|perform("支付失败")>"模拟失败"</button><button class="button primary" disabled=move||readonly||busy.get() on:click=move |_|success("已支付")>"模拟支付成功"</button></div>}.into_any()}else{view!{<span></span>}.into_any()}}<Show when=move||!error.get().is_empty()><p class="form-error" role="alert">{move||error.get()}</p></Show>}
}
#[component]
pub fn ReportsPage(
    title: &'static str,
    endpoint: &'static str,
    page_permission: &'static str,
) -> impl IntoView {
    let access = crate::access::access();
    let rows = RwSignal::new(Vec::<FinanceRow>::new());
    let error = RwSignal::new(String::new());
    let from = RwSignal::new(String::new());
    let to = RwSignal::new(String::new());
    let query = RwSignal::new(String::new());
    let url = move || {
        format!(
            "/api/finance/{endpoint}?from={}&to={}&q={}",
            from.get(),
            to.get(),
            crate::url_encode(&query.get())
        )
    };
    let refresh = move || {
        let path = url();
        spawn_local(async move {
            match api("GET", &path, None).await {
                Ok(v) => rows.set(serde_json::from_value(v).unwrap_or_default()),
                Err(e) => error.set(e),
            }
        });
    };
    Effect::new(move |_| refresh());
    view! {<div class="page-heading"><div><h1>{title}</h1><p>"根据数据库订单和不可变余额流水汇总。"</p></div><a class="button secondary export-link" hidden=move||!access.can(&format!("{page_permission}.export")) href=move||format!("{}&export=csv",url())>"导出 CSV"</a></div><section class="panel filter-panel"><div class="filters"><label>"开始日期"<input type="date" prop:value=move||from.get() on:input=move|ev|from.set(event_target_value(&ev))/></label><label>"结束日期"<input type="date" prop:value=move||to.get() on:input=move|ev|to.set(event_target_value(&ev))/></label><label>"商户名称"<input prop:value=move||query.get() on:input=move|ev|query.set(event_target_value(&ev))/></label><button class="button primary" on:click=move |_|refresh()>"查询"</button><button class="button secondary" on:click=move |_|{from.set(String::new());to.set(String::new());query.set(String::new());}>"重置"</button></div></section><p class="form-error">{move||error.get()}</p><section class="panel table-panel"><div class="table-scroll"><table><thead><tr><th>"名称"</th><th>"订单笔数"</th><th>"交易 / 变更金额"</th><th>"手续费"</th><th>"预付余额"</th><th>"变更前"</th><th>"变更后"</th><th>"时间"</th><th>"类型 / 关联单号"</th></tr></thead><tbody><For each=move||rows.get() key=|r|format!("{:?}",r) let:row><tr><td>{row.name}</td><td>{row.orders}</td><td>{format!("¥ {:.2}",row.amount)}</td><td>{format!("¥ {:.2}",row.fee)}</td><td>{format!("¥ {:.2}",row.remaining)}</td><td>{format!("¥ {:.2}",row.before)}</td><td>{format!("¥ {:.2}",row.after)}</td><td>{row.date}</td><td>{row.kind}<small>{row.related_id}</small></td></tr></For></tbody></table><Show when=move||rows.get().is_empty()><div class="empty-state">"所选条件下暂无数据。"</div></Show></div></section>}
}
#[component]
pub fn SettingsEditor(readonly: bool, on_saved: Callback<Value>) -> impl IntoView {
    let data = RwSignal::new(json!({}));
    let error = RwSignal::new(String::new());
    let notice = RwSignal::new(String::new());
    let busy = RwSignal::new(false);
    Effect::new(move |_| {
        spawn_local(async move {
            match api("GET", "/api/settings", None).await {
                Ok(v) => data.set(v),
                Err(e) => error.set(e),
            }
        })
    });
    let schema = vec![
        f("siteName", "网站名称", "text"),
        f("supportContact", "客服联系方式", "text"),
        f("minAmount", "最小收款金额（元）", "number"),
        f("maxAmount", "最大收款金额（元）", "number"),
        f("orderTimeoutMinutes", "未支付订单超时（分钟）", "number"),
    ];
    view! {<div class="page-heading"><div><h1>"系统配置"</h1><p>"配置保存后生效，并记录到操作日志。"</p></div></div><form class="panel settings-form" on:submit=move|ev|{ev.prevent_default();if busy.get(){return}busy.set(true);error.set(String::new());spawn_local(async move{match api("PATCH","/api/settings",Some(data.get_untracked())).await{Ok(_)=>{notice.set("配置已保存".into());on_saved.run(data.get_untracked());},Err(e)=>error.set(e)}busy.set(false);});}><div class="editor-fields">{schema.into_iter().map(|field|{let key=field.key;view!{<label>{field.label}<input disabled=readonly type=if field.kind=="select"{"text"}else{field.kind} step="0.01" prop:value=move||text(&data.get(),key) on:input=move|ev|data.update(|v|v[key]=json!(event_target_value(&ev)))/></label>}}).collect_view()}</div><p class="form-error" role="alert">{move||error.get()}</p><p class="success-note" role="status">{move||notice.get()}</p><button class="button primary" hidden=readonly disabled=move||readonly||busy.get()>"保存配置"</button></form>}
}
#[component]
pub fn AuditPage() -> impl IntoView {
    let rows = RwSignal::new(Vec::<Value>::new());
    let error = RwSignal::new(String::new());
    let refresh = move || {
        spawn_local(async move {
            match api("GET", "/api/audit", None).await {
                Ok(v) => rows.set(v.as_array().cloned().unwrap_or_default()),
                Err(e) => error.set(e),
            }
        })
    };
    Effect::new(move |_| refresh());
    view! {<div class="page-heading"><div><h1>"登录与操作日志"</h1><p>"记录登录、配置修改、订单操作及余额变动。"</p></div><button class="button secondary" on:click=move |_|refresh()>"刷新"</button></div><p class="form-error">{move||error.get()}</p><section class="panel table-panel"><div class="table-scroll"><table><thead><tr><th>"时间"</th><th>"账号"</th><th>"动作"</th><th>"目标"</th><th>"详情"</th><th>"IP"</th></tr></thead><tbody><For each=move||rows.get() key=|v|v["id"].to_string() let:row><tr><td>{text(&row,"createdAt")}</td><td>{text(&row,"actor")}</td><td>{text(&row,"action")}</td><td>{text(&row,"target")}</td><td>{text(&row,"detail")}</td><td>{text(&row,"ip")}</td></tr></For></tbody></table></div></section>}
}
#[component]
pub fn AvailablePlugins() -> impl IntoView {
    view! {<div class="page-heading"><div><h1>"支付插件"</h1><p>"管理支付通道及接入状态。"</p></div></div><crate::wechat::WechatSettings/><section class="panel"><table><thead><tr><th>"插件"</th><th>"说明"</th><th>"支付方式"</th></tr></thead><tbody><tr><td>"wechat_jsapi"</td><td>"微信扫码打开收款页，网页授权后调起支付"</td><td>"微信支付"</td></tr><tr><td>"demo"</td><td>"本地模拟订单与支付结果"</td><td>"支付宝 / 微信支付 / 网银支付"</td></tr></tbody></table></section>}
}
#[component]
pub fn PaymentMethods() -> impl IntoView {
    let rows = RwSignal::new(Vec::<Channel>::new());
    Effect::new(move |_| {
        spawn_local(async move {
            if let Ok(v) = api("GET", "/api/channels", None).await {
                rows.set(serde_json::from_value(v).unwrap_or_default())
            }
        })
    });
    view! {<div class="page-heading"><div><h1>"支付方式"</h1><p>"启用状态由对应通道配置决定。"</p></div></div><section class="panel"><table><thead><tr><th>"支付方式"</th><th>"已开启通道"</th><th>"已接入适配器"</th></tr></thead><tbody>{["支付宝","微信支付","网银支付"].into_iter().map(|method|view!{<tr><td>{method}</td><td>{move||rows.get().iter().filter(|c|c.method==method&&c.enabled).count()}</td><td>{move||if rows.get().iter().any(|c|c.method==method&&c.plugin=="wechat_jsapi"){"微信 JSAPI / 本地模拟"}else{"本地模拟"}}</td></tr>}).collect_view()}</tbody></table></section>}
}
#[component]
pub fn MerchantEditor(merchant: Merchant, readonly: bool, on_saved: Callback<()>) -> impl IntoView {
    let access = crate::access::access();
    let open = RwSignal::new(false);
    let name = RwSignal::new(merchant.name);
    let enabled = RwSignal::new(merchant.enabled);
    let error = RwSignal::new(String::new());
    let busy = RwSignal::new(false);
    let mid = merchant.id;
    view! {<button class="text-button" hidden=move||!access.can("merchants.edit") disabled=readonly on:click=move |_|open.set(true)>"编辑"</button><Show when=move||open.get()><div class="modal-backdrop"><form class="modal small-modal" on:submit={let mid=mid.clone();move|ev|{ev.prevent_default();if busy.get(){return}busy.set(true);let path=format!("/api/merchants/{mid}");spawn_local(async move{match api("PATCH",&path,Some(json!({"name":name.get(),"enabled":enabled.get()}))).await{Ok(_)=>{open.set(false);on_saved.run(())},Err(e)=>error.set(e)}busy.set(false);});}}><div class="modal-head"><h2>"编辑商户"</h2><button type="button" class="close" on:click=move |_|open.set(false)>"×"</button></div><div class="editor-fields"><label>"商户名称"<input required prop:value=move||name.get() on:input=move|ev|name.set(event_target_value(&ev))/></label><label>"状态"<select disabled=move||!access.can("merchants.toggle") prop:value=move||if enabled.get(){"true"}else{"false"} on:change=move|ev|enabled.set(event_target_value(&ev)=="true")><option value="true">"正常"</option><option value="false">"已关闭"</option></select></label></div><p class="form-error">{move||error.get()}</p><div class="form-actions"><button class="button primary" disabled=move||busy.get()>"保存"</button></div></form></div></Show>}
}
