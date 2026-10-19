use crate::{PaymentOrder, features::api};
use leptos::prelude::*;
use serde_json::{Value, json};
use std::sync::{
    Arc,
    atomic::{AtomicBool, Ordering},
};
use wasm_bindgen::JsValue;
use wasm_bindgen_futures::spawn_local;

pub fn expiration_label(expires: i64) -> String {
    if expires == 0 {
        return "—".into();
    }
    js_sys::Date::new(&JsValue::from_f64(expires as f64 * 1000.0))
        .to_locale_string("zh-CN", &JsValue::UNDEFINED)
        .as_string()
        .unwrap_or_default()
}

#[component]
pub fn OrderQRCodeDialog(
    order: PaymentOrder,
    readonly: bool,
    on_close: Callback<()>,
    on_update: Callback<PaymentOrder>,
) -> impl IntoView {
    let access = crate::access::access();
    let current = RwSignal::new(order);
    let error = RwSignal::new(String::new());
    let busy = RwSignal::new(false);
    let revision = RwSignal::new(0_u64);
    let alive = Arc::new(AtomicBool::new(true));
    let cleanup = alive.clone();
    on_cleanup(move || cleanup.store(false, Ordering::Relaxed));
    let oid = current.get_untracked().system_id;
    spawn_local(async move {
        loop {
            crate::wechat::delay(5000).await;
            if !alive.load(Ordering::Relaxed) {
                return;
            }
            if busy.get_untracked() {
                continue;
            }
            let version = revision.get_untracked();
            let result = api("GET", &format!("/api/payments/{oid}"), None).await;
            if !alive.load(Ordering::Relaxed) {
                return;
            }
            if revision.get_untracked() != version {
                continue;
            }
            match result {
                Ok(v) => match serde_json::from_value::<PaymentOrder>(v) {
                    Ok(o) => {
                        let done = matches!(o.status.as_str(), "已支付" | "已关闭" | "支付失败");
                        error.set(String::new());
                        current.set(o.clone());
                        on_update.run(o);
                        if done {
                            return;
                        }
                    }
                    Err(_) => error.set("订单响应无效，稍后自动重试".into()),
                },
                Err(e) => error.set(format!("{e}；稍后自动重试")),
            }
        }
    });
    let query = move || {
        if busy.get_untracked() {
            return;
        }
        revision.update(|v| *v += 1);
        busy.set(true);
        error.set(String::new());
        let id = current.get_untracked().system_id;
        spawn_local(async move {
            let result = api("POST", &format!("/api/payments/{id}/query"), None).await;
            if current.try_get_untracked().is_none() {
                return;
            }
            match result {
                Ok(v) => match serde_json::from_value::<PaymentOrder>(
                    v.get("order").cloned().unwrap_or(v),
                ) {
                    Ok(o) => {
                        current.set(o.clone());
                        on_update.run(o)
                    }
                    Err(_) => error.set("订单响应无效".into()),
                },
                Err(e) => error.set(e),
            }
            busy.set(false);
        });
    };
    view! {<div class="modal-backdrop"><section class="modal qr-order-modal" role="dialog" aria-modal="true" aria-label="订单收款二维码">
        <div class="modal-head"><div><h2>"订单收款二维码"</h2><p>"支付状态每 5 秒自动刷新"</p></div><button class="close" aria-label="关闭" on:click=move |_|on_close.run(())>"×"</button></div>
        {move||{let o=current.get();view!{
            <div class="qr-order-summary"><strong>{o.description}</strong><span>{format!("¥ {:.2}",o.amount)}</span><span class=crate::status_class(&o.status)>{o.status.clone()}</span></div>
            <div class="checkout-code"><img class:qr-inactive=!o.can_pay src=o.qr_image alt="该订单的微信收款二维码" width="256" height="256"/>
                <strong>{if o.status=="已支付"{"订单已支付，请勿重复付款"}else if o.can_pay{"请使用微信扫一扫支付"}else{"二维码已不能付款，请核对订单状态"}}</strong>
                <p class="cashier-help">{format!("商户订单号：{}",o.merchant_order)}</p><p class="cashier-help">{format!("支付截止：{}",expiration_label(o.expires_at))}</p>
                {if o.can_pay{view!{<a href=o.pay_url target="_blank" rel="noreferrer noopener">"打开收款页面"</a>}.into_any()}else{view!{<span></span>}.into_any()}}
            </div>
        }}}
        <p class="form-error" role="alert">{move||error.get()}</p><div class="form-actions"><button class="button secondary" on:click=move |_|on_close.run(())>"关闭"</button><button class="button primary" hidden=move||!access.any(&["orders.query","history.query"]) disabled=move||readonly||busy.get() on:click=move |_|query()>{move||if busy.get(){"查询中…"}else{"向微信查单"}}</button></div>
    </section></div>}
}

#[component]
pub fn MerchantApiAccess(merchant_id: String) -> impl IntoView {
    let mid = StoredValue::new(merchant_id);
    let open = RwSignal::new(false);
    let state = RwSignal::new(None::<Value>);
    let key = RwSignal::new(String::new());
    let busy = RwSignal::new(false);
    let error = RwSignal::new(String::new());
    let load = move || {
        error.set(String::new());
        key.set(String::new());
        state.set(None);
        let path = format!("/api/merchants/{}/api-key", mid.get_value());
        spawn_local(async move {
            match api("GET", &path, None).await {
                Ok(v) => state.set(Some(v)),
                Err(e) => error.set(e),
            }
        });
    };
    let save = move |revoke: bool| {
        if busy.get_untracked() {
            return;
        }
        busy.set(true);
        error.set(String::new());
        key.set(String::new());
        let path = format!(
            "/api/merchants/{}/api-key{}",
            mid.get_value(),
            if revoke { "/revoke" } else { "" }
        );
        spawn_local(async move {
            match api("POST", &path, None).await {
                Ok(v) => {
                    if revoke {
                        state.set(Some(json!({"enabled":false})))
                    } else {
                        key.set(v["apiKey"].as_str().unwrap_or_default().to_string());
                        state.set(Some(json!({"enabled":true,"prefix":"新密钥已生成"})))
                    }
                }
                Err(e) => error.set(e),
            }
            busy.set(false);
        });
    };
    view! {<button class="text-button" on:click=move |_|{open.set(true);load()}>"接口接入"</button>
        <Show when=move||open.get()><div class="modal-backdrop"><section class="modal api-access-modal" role="dialog" aria-modal="true" aria-label="商户接口接入">
            <div class="modal-head"><div><h2>"外部业务系统接入"</h2><p>{format!("商户 {}",mid.get_value())}</p></div><button class="close" disabled=move||busy.get() on:click=move |_|{key.set(String::new());open.set(false)}>"×"</button></div>
            <p>"业务系统用商户 API 密钥推送订单。每个密钥只能创建和查询所属商户的订单。"</p>
            <div class="api-endpoints"><code>"POST /api/v1/orders"</code><code>"GET /api/v1/orders/{商户订单号}"</code><code>"POST /api/v1/orders/{商户订单号}/query"</code></div>
            {move||state.get().map(|v|view!{<p class="demo-note">{if v["enabled"].as_bool().unwrap_or(false){format!("密钥已启用：{}。重新生成或撤销后，旧密钥立即失效。",v["prefix"].as_str().unwrap_or_default())}else{"尚未启用接口密钥。".into()}}</p>})}
            <Show when=move||!key.get().is_empty()><label class="api-key-field">"请立即复制保存，密钥只展示这一次"<textarea readonly autocomplete="off" rows="3" prop:value=move||key.get()/></label></Show>
            <p class="cashier-help">"下单时提交商户订单号和金额，可附商品描述。成功后接口返回收款链接、二维码地址和支付状态。重复推送同一订单不会重复创建或扣费。"</p>
            <p class="form-error" role="alert">{move||error.get()}</p><div class="form-actions"><button class="button secondary" disabled=move||busy.get()||!state.get().is_some_and(|v|v["enabled"]==true) on:click=move |_|save(true)>"撤销密钥"</button><button class="button primary" disabled=move||busy.get()||state.get().is_none() on:click=move |_|save(false)>{move||if state.get().is_some_and(|v|v["enabled"]==true){"重新生成密钥（旧密钥失效）"}else{"生成商户 API 密钥"}}</button></div>
        </section></div></Show>
    }
}
