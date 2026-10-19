use crate::{PaymentOrder, features::api};
use leptos::prelude::*;
use serde::Deserialize;
use serde_json::Value;
use std::sync::{
    Arc,
    atomic::{AtomicBool, Ordering},
};
use wasm_bindgen::{JsCast, JsValue, closure::Closure};
use wasm_bindgen_futures::{JsFuture, spawn_local};

#[component]
pub fn Root() -> impl IntoView {
    let path = web_sys::window()
        .and_then(|w| w.location().pathname().ok())
        .unwrap_or_default();
    if let Some(token) = path.strip_prefix("/pay/") {
        view! {<Cashier token=token.to_string()/>}.into_any()
    } else {
        view! {<crate::features::App/>}.into_any()
    }
}

#[component]
pub fn WechatSettings() -> impl IntoView {
    let config = RwSignal::new(None::<Value>);
    let error = RwSignal::new(String::new());
    let refresh = move || {
        spawn_local(async move {
            match api("GET", "/api/integrations/wechat", None).await {
                Ok(v) => {
                    config.set(Some(v));
                    error.set(String::new());
                }
                Err(e) => error.set(e),
            }
        })
    };
    refresh();
    view! {<section class="panel wechat-config"><div class="table-toolbar"><strong>"微信 JSAPI · 普通商户"</strong><button class="button secondary" on:click=move |_|refresh()>"刷新状态"</button></div>
        <p class="form-error">{move||error.get()}</p>
        {move||config.get().map(|v| {
            let ready = v["ready"].as_bool().unwrap_or(false);
            let missing = v["missing"].as_array().map(|list| list.iter().filter_map(Value::as_str).collect::<Vec<_>>().join("、")).unwrap_or_default();
            view! {<div class="wechat-config-body"><span class="status-badge">{if ready {"已加载本地配置 · 待实测确认"} else {"未启用"}}</span><p>"微信扫一扫 → 收款网页 → 微信授权 → 确认支付"</p>
                {if ready {view!{<p>"支付通知："<code>{v["notifyUrl"].as_str().unwrap_or_default().to_string()}</code></p><p>"网页授权回调："<code>{v["oauthCallback"].as_str().unwrap_or_default().to_string()}</code></p>}.into_any()}else{view!{<p>"请按项目中的 docs/WECHAT_JSAPI.md 配置商户参数和密钥文件，再由维护人员重启服务。"</p><p>"待配置："<code>{missing}</code></p>}.into_any()}}
                <p class="demo-note">"在通道管理中新建微信支付通道，插件选择“微信 JSAPI”。配置已加载不代表微信平台授权、域名和商户权限已验证。"</p>
            </div>}
        })}
    </section>}
}

#[component]
pub fn WechatCheckout(
    order_id: String,
    readonly: bool,
    on_update: Callback<PaymentOrder>,
) -> impl IntoView {
    let access = crate::access::access();
    let checkout = RwSignal::new(None::<Value>);
    let busy = RwSignal::new(false);
    let error = RwSignal::new(String::new());
    let query_id = order_id.clone();
    view! {<section class="wechat-checkout"><p class="demo-note">"此订单使用微信 JSAPI。微信扫码后完成支付，结果由后端接收支付通知或查单确认。"</p>
        <div class="form-actions">
            <button class="button secondary" hidden=move||!access.any(&["orders.query","history.query"]) disabled=move||readonly||busy.get() on:click=move |_|{
                if busy.get_untracked(){return} busy.set(true); error.set(String::new());
                let path=format!("/api/payments/{query_id}/query");
                spawn_local(async move{match api("POST",&path,None).await{Ok(v)=>match serde_json::from_value(v){Ok(o)=>on_update.run(o),Err(_)=>error.set("订单响应无效".into())},Err(e)=>error.set(e)}busy.set(false);});
            }>"查询支付结果"</button>
            <button class="button primary" hidden=move||!access.any(&["orders.qr","history.qr"]) disabled=move||busy.get() on:click=move |_|{
                if busy.get_untracked(){return} busy.set(true); error.set(String::new());
                let path=format!("/api/payments/{order_id}");
                spawn_local(async move{match api("GET",&path,None).await{Ok(v)=>{if v["payUrl"].as_str().unwrap_or_default().is_empty(){error.set("该订单尚无收款二维码".into())}else{checkout.set(Some(serde_json::json!({"url":v["payUrl"],"qrImage":v["qrImage"],"expiresAt":v["expiresAt"],"status":v["status"],"canPay":v["canPay"]})))}},Err(e)=>error.set(e)}busy.set(false);});
            }>"查看收款二维码"</button>
        </div>
        {move||checkout.get().map(|v|{
            let src=v["qrImage"].as_str().unwrap_or_default().to_string();
            let url=v["url"].as_str().unwrap_or_default().to_string();
            let can_pay=v["canPay"].as_bool().unwrap_or(false);
            let paid=v["status"].as_str()==Some("已支付");
            view!{<div class="checkout-code"><img class:qr-inactive=!can_pay src=src alt="该订单的收款二维码" width="256" height="256"/><strong>{if paid{"订单已支付，请勿重复付款"}else if can_pay{"请使用微信扫一扫付款"}else{"订单已关闭或支付时间已过"}}</strong>{if can_pay{view!{<a href=url.clone() target="_blank" rel="noreferrer noopener">"打开收款页面"</a>}.into_any()}else{view!{<span></span>}.into_any()}}<p class="checkout-url">{url}</p></div>}
        })}
        <p class="form-error" role="alert">{move||error.get()}</p>
    </section>}
}

#[derive(Clone, Deserialize)]
#[serde(rename_all = "camelCase")]
struct CashierInfo {
    description: String,
    amount: f64,
    order_id: String,
    status: String,
    ready: bool,
    authenticated: bool,
    can_pay: bool,
}
async fn fetch_info(path: &str) -> Result<CashierInfo, String> {
    serde_json::from_value(api("GET", path, None).await?)
        .map_err(|_| "收款信息格式无效".to_string())
}
pub(crate) async fn delay(ms: i32) {
    let promise = js_sys::Promise::new(&mut |resolve, _| {
        if let Some(window) = web_sys::window() {
            let _ = window
                .set_timeout_with_callback_and_timeout_and_arguments_0(resolve.unchecked_ref(), ms);
        }
    });
    let _ = JsFuture::from(promise).await;
}
async fn invoke_payment(value: Value) -> Result<String, String> {
    let bridge = js_sys::Reflect::get(&js_sys::global(), &JsValue::from_str("WeixinJSBridge"))
        .map_err(|_| "无法读取微信支付组件".to_string())?;
    if bridge.is_undefined() || bridge.is_null() {
        return Err("微信支付尚未就绪，请稍候重试，并确认使用微信打开。".into());
    }
    let invoke = js_sys::Reflect::get(&bridge, &JsValue::from_str("invoke"))
        .ok()
        .and_then(|f| f.dyn_into::<js_sys::Function>().ok())
        .ok_or("微信支付组件不可用")?;
    let params = js_sys::JSON::parse(&value.to_string()).map_err(|_| "支付参数无效".to_string())?;
    let promise = js_sys::Promise::new(&mut |resolve, reject| {
        let cb = Closure::once_into_js(move |result: JsValue| {
            let _ = resolve.call1(&JsValue::UNDEFINED, &result);
        });
        if invoke
            .call3(
                &bridge,
                &JsValue::from_str("getBrandWCPayRequest"),
                &params,
                &cb,
            )
            .is_err()
        {
            let _ = reject.call1(
                &JsValue::UNDEFINED,
                &JsValue::from_str("调起微信支付失败，请稍后重试"),
            );
        }
    });
    let response = JsFuture::from(promise)
        .await
        .map_err(|_| "调起微信支付失败，请稍后重试".to_string())?;
    Ok(
        js_sys::Reflect::get(&response, &JsValue::from_str("err_msg"))
            .ok()
            .and_then(|v| v.as_string())
            .unwrap_or_default(),
    )
}

#[component]
fn Cashier(token: String) -> impl IntoView {
    let info = RwSignal::new(None::<CashierInfo>);
    let error = RwSignal::new(String::new());
    let message = RwSignal::new(String::new());
    let busy = RwSignal::new(false);
    let in_wechat = web_sys::window()
        .and_then(|w| w.navigator().user_agent().ok())
        .unwrap_or_default()
        .to_lowercase()
        .contains("micromessenger");
    let path = StoredValue::new(format!("/api/cashier/{token}"));
    let oauth_url = format!("/api/cashier/{token}/oauth/start");
    let qr_url = format!("/api/cashier/{token}/qr.png");
    let alive = Arc::new(AtomicBool::new(true));
    let cleanup = alive.clone();
    on_cleanup(move || cleanup.store(false, Ordering::Relaxed));
    if web_sys::window()
        .and_then(|w| w.location().search().ok())
        .unwrap_or_default()
        .contains("auth_error=1")
    {
        error.set("微信网页授权失败，请重新授权或联系商户。".into());
    }
    let initial_path = path.get_value();
    spawn_local(async move {
        for _ in 0..2700 {
            if !alive.load(Ordering::Relaxed) {
                return;
            }
            match fetch_info(&initial_path).await {
                Ok(v) => {
                    let done =
                        v.status == "已支付" || v.status == "已关闭" || v.status == "支付失败";
                    if !alive.load(Ordering::Relaxed) {
                        return;
                    }
                    info.set(Some(v));
                    if done {
                        return;
                    }
                }
                Err(e) => {
                    if alive.load(Ordering::Relaxed) {
                        error.set(e)
                    }
                    return;
                }
            }
            delay(3000).await;
        }
    });
    let refresh = move || {
        if busy.get_untracked() {
            return;
        }
        busy.set(true);
        error.set(String::new());
        let base = path.get_value();
        spawn_local(async move {
            let result = if info.get_untracked().is_some_and(|v| v.authenticated) {
                api("POST", &format!("{base}/refresh"), None)
                    .await
                    .and_then(|v| serde_json::from_value(v).map_err(|_| "收款信息格式无效".into()))
            } else {
                fetch_info(&base).await
            };
            match result {
                Ok(v) => info.set(Some(v)),
                Err(e) => error.set(e),
            }
            busy.set(false);
        });
    };
    let pay = move || {
        if busy.get_untracked() {
            return;
        }
        busy.set(true);
        error.set(String::new());
        message.set(String::new());
        let base = path.get_value();
        spawn_local(async move {
            match api("POST", &format!("{base}/prepay"), None).await {
                Ok(params) => match invoke_payment(params).await {
                    Ok(result) => {
                        message.set(
                            if result.ends_with(":cancel") {
                                "已取消，可以重新支付。"
                            } else if result.ends_with(":ok") {
                                "正在向微信确认支付结果…"
                            } else {
                                "支付未完成，请查询结果或重试。"
                            }
                            .into(),
                        );
                        match api("POST", &format!("{base}/refresh"), None).await {
                            Ok(v) => {
                                if let Ok(v) = serde_json::from_value(v) {
                                    info.set(Some(v))
                                }
                            }
                            Err(e) => error.set(e),
                        }
                    }
                    Err(e) => error.set(e),
                },
                Err(e) => error.set(e),
            }
            busy.set(false);
        });
    };
    view! {<main class="cashier-shell"><section class="cashier-card"><header class="cashier-header"><span class="cashier-mark">"微"</span><span>"微信支付"</span></header>
        {move||info.get().map(|v|view!{<h1>{v.description}</h1><div class="cashier-amount"><small>"¥"</small>{format!("{:.2}",v.amount)}</div><p class="cashier-order">{format!("订单 {}",v.order_id)}</p>
            {if v.status=="已支付" {view!{<div class="cashier-success" role="status"><strong>"支付成功"</strong><p>"商户已确认收到微信支付结果。"</p></div>}.into_any()}
            else if !v.ready {view!{<p class="form-error">"商户暂未启用微信支付，请联系商户。"</p>}.into_any()}
            else if !v.can_pay {view!{<p class="demo-note">"订单已关闭或支付时间已过，请联系商户确认结果。"</p>}.into_any()}
            else if !in_wechat {view!{<div class="checkout-code"><img src=qr_url.clone() width="256" height="256" alt="微信扫一扫付款"/><strong>"请使用微信扫一扫付款"</strong><p>"在微信中打开后可确认支付。"</p></div>}.into_any()}
            else if !v.authenticated {view!{<a class="button cashier-pay" href=oauth_url.clone()>"微信授权并继续"</a><p class="cashier-help">"授权用于获取本次支付所需的微信身份。"</p>}.into_any()}
            else {view!{<button class="button cashier-pay" disabled=move||busy.get() on:click=move |_|pay()>{move||if busy.get(){"正在处理…"}else{"确认支付"}}</button>}.into_any()}}
        })}
        <Show when=move||info.get().is_none()&&error.get().is_empty()><p>"正在读取收款信息…"</p></Show>
        <p class="cashier-help" role="status">{move||message.get()}</p><p class="form-error" role="alert">{move||error.get()}</p>
        <button class="text-button" disabled=move||busy.get() on:click=move |_|refresh()>"刷新支付结果"</button>
        <footer class="cashier-footer">"付款前请核对商品和金额"</footer>
    </section></main>}
}
