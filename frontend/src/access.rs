use crate::{
    Merchant,
    features::{AuthStatus, User, api},
};
use leptos::prelude::*;
use serde::Deserialize;
use serde_json::{Value, json};
use std::sync::{
    Arc,
    atomic::{AtomicBool, Ordering},
};
use wasm_bindgen_futures::spawn_local;

#[derive(Clone, Copy)]
pub struct Access(pub RwSignal<User>);
impl Access {
    pub fn can(self, permission: &str) -> bool {
        self.0
            .with(|u| u.super_user || u.permissions.iter().any(|p| p == permission))
    }
    pub fn any(self, permissions: &[&str]) -> bool {
        permissions.iter().any(|p| self.can(p))
    }
    pub fn page(self, page: &str) -> bool {
        if page == "accounts" || page == "roles" {
            self.0.with(|u| u.super_user)
        } else {
            self.can(&format!("{page}.view"))
        }
    }
    pub fn query_param(self, page: &str, key: &str, param: &str, value: &str) -> String {
        if self.can(&format!("{page}.view"))
            && self.can(&format!("{page}.{key}"))
            && !value.trim().is_empty()
        {
            format!("&{param}={}", crate::url_encode(value.trim()))
        } else {
            String::new()
        }
    }
    pub fn internal(self) -> bool {
        self.0.with(|u| u.identity == "internal")
    }
}
pub fn access() -> Access {
    use_context::<Access>().expect("后台权限上下文")
}
pub const PAGES: &[&str] = &[
    "home",
    "orders",
    "history",
    "merchants",
    "products",
    "channels",
    "merchant_settlement",
    "finance_channels",
    "finance_products",
    "finance_merchants",
    "prepay",
    "statistics",
    "receivers",
    "complaints",
    "methods",
    "plugins",
    "official",
    "robot",
    "site_settings",
    "pay_settings",
    "quick_login",
    "accounts",
    "roles",
    "blacklist",
    "transfers",
    "logs",
    "user_ids",
];
pub fn install(user: User) -> Access {
    let access = Access(RwSignal::new(user));
    provide_context(access);
    let alive = Arc::new(AtomicBool::new(true));
    let cleanup = alive.clone();
    on_cleanup(move || cleanup.store(false, Ordering::Relaxed));
    spawn_local(async move {
        loop {
            crate::wechat::delay(15000).await;
            if !alive.load(Ordering::Relaxed) {
                return;
            }
            let result = api("GET", "/api/auth/status", None).await;
            if !alive.load(Ordering::Relaxed) {
                return;
            }
            if let Ok(v) = result {
                if let Ok(status) = serde_json::from_value::<AuthStatus>(v) {
                    if !status.authenticated {
                        if let Some(w) = web_sys::window() {
                            let _ = w.location().reload();
                        }
                        return;
                    }
                    if access.0.get_untracked() != status.user {
                        access.0.set(status.user);
                    }
                }
            }
        }
    });
    access
}

#[derive(Clone, Debug, Deserialize, Default)]
struct Permission {
    code: String,
    name: String,
    page: String,
    merchant: bool,
    requires: Vec<String>,
}
#[derive(Clone, Debug, Deserialize, Default)]
struct Role {
    id: String,
    name: String,
    identity: String,
    enabled: bool,
    permissions: Vec<String>,
    #[serde(rename = "dataScope")]
    data_scope: String,
    #[serde(rename = "merchantIds")]
    merchant_ids: Vec<String>,
}
fn label(identity: &str) -> &'static str {
    if identity == "merchant" {
        "商户"
    } else {
        "内部"
    }
}
fn strval(v: &Value, key: &str) -> String {
    v[key].as_str().unwrap_or_default().into()
}
fn selected(v: &Value, key: &str, item: &str) -> bool {
    v[key]
        .as_array()
        .is_some_and(|a| a.iter().any(|x| x.as_str() == Some(item)))
}
fn toggle(v: &mut Value, key: &str, item: &str, on: bool) {
    let mut values = v[key].as_array().cloned().unwrap_or_default();
    values.retain(|x| x.as_str() != Some(item));
    if on {
        values.push(json!(item));
    }
    v[key] = json!(values);
}
fn load_users(
    rows: RwSignal<Vec<User>>,
    roles: RwSignal<Vec<Role>>,
    merchants: RwSignal<Vec<Merchant>>,
    error: RwSignal<String>,
) {
    spawn_local(async move {
        match api("GET", "/api/users", None).await {
            Ok(v) => rows.set(serde_json::from_value(v).unwrap_or_default()),
            Err(e) => error.set(e),
        }
        match api("GET", "/api/access/roles", None).await {
            Ok(v) => roles.set(serde_json::from_value(v).unwrap_or_default()),
            Err(e) => error.set(e),
        }
        match api("GET", "/api/merchants", None).await {
            Ok(v) => merchants.set(serde_json::from_value(v).unwrap_or_default()),
            Err(e) => error.set(e),
        }
    });
}
#[component]
pub fn UsersPage() -> impl IntoView {
    let rows = RwSignal::new(Vec::<User>::new());
    let roles = RwSignal::new(Vec::<Role>::new());
    let merchants = RwSignal::new(Vec::<Merchant>::new());
    let error = RwSignal::new(String::new());
    let notice = RwSignal::new(String::new());
    let editor = RwSignal::new(None::<Value>);
    let target = RwSignal::new(String::new());
    let busy = RwSignal::new(false);
    let query = RwSignal::new(String::new());
    let reset = RwSignal::new(None::<User>);
    let password = RwSignal::new(String::new());
    load_users(rows, roles, merchants, error);
    view! {
    <div class="page-heading"><div><h1>"用户管理"</h1><p>"由超管统一创建账号、绑定商户并分配角色。"</p></div><button class="button primary" on:click=move |_|{target.set(String::new());error.set(String::new());editor.set(Some(json!({"name":"","password":"","identity":"internal","merchantId":"","enabled":true,"roleIds":[]})));}>"＋ 创建用户"</button></div>
    <p class="success-note" role="status">{move||notice.get()}</p><p class="form-error" role="alert">{move||error.get()}</p>
    <section class="panel filter-panel"><div class="filters"><label>"账号搜索"<input prop:value=move||query.get() on:input=move|ev|query.set(event_target_value(&ev))/></label><button class="button secondary" on:click=move |_|{error.set(String::new());load_users(rows,roles,merchants,error);}>"刷新"</button></div></section>
    <section class="panel table-panel"><div class="table-scroll"><table><thead><tr><th>"账号"</th><th>"身份"</th><th>"归属商户"</th><th>"角色"</th><th>"状态"</th><th>"操作"</th></tr></thead><tbody>
    <For each={move||rows.get().into_iter().filter(|u|u.name.contains(&query.get())).collect::<Vec<_>>()} key=|u|format!("{u:?}") let:u>{let edit=u.clone();let reset_user=u.clone();let role_ids=u.role_ids.clone();let mid=u.merchant_id.clone();view!{<tr><td><strong>{u.name}</strong><small>{u.id.clone()}</small></td><td>{if u.super_user{"内部 · 超级管理员"}else{label(&u.identity)}}</td><td>{move||merchants.get().into_iter().find(|m|m.id==mid).map(|m|m.name).unwrap_or_else(||"—".into())}</td><td>{move||{let all=roles.get();role_ids.iter().map(|id|all.iter().find(|r|&r.id==id).map(|r|format!("{}{}",r.name,if r.enabled{""}else{"（停用）"})).unwrap_or_else(||id.clone())).collect::<Vec<_>>().join("、")}}</td><td><span class:badge-on=u.enabled class="status-badge">{if u.enabled{"启用"}else{"停用"}}</span></td><td><button class="text-button" on:click=move |_|{target.set(edit.id.clone());error.set(String::new());editor.set(Some(json!({"name":edit.name,"identity":edit.identity,"merchantId":edit.merchant_id,"enabled":edit.enabled,"roleIds":edit.role_ids})));}>"编辑 / 授权"</button><button class="text-button" on:click=move |_|{password.set(String::new());error.set(String::new());reset.set(Some(reset_user.clone()));}>"重置密码"</button></td></tr>}}</For>
    </tbody></table><Show when=move||rows.get().is_empty()><div class="empty-state">"暂无用户"</div></Show></div></section>
    <Show when=move||editor.get().is_some()><div class="modal-backdrop"><form class="modal access-modal" on:submit=move|ev|{ev.prevent_default();if busy.get(){return}busy.set(true);error.set(String::new());let data=editor.get().unwrap_or_default();let uid=target.get();let creating=uid.is_empty();let path=if creating{"/api/users".into()}else{format!("/api/users/{uid}")};spawn_local(async move{match api(if creating{"POST"}else{"PATCH"},&path,Some(data)).await{Ok(_)=>{editor.set(None);notice.set("用户及角色已保存".into());load_users(rows,roles,merchants,error)},Err(e)=>error.set(e)}busy.set(false);});}>
    <div class="modal-head"><h2>{move||if target.get().is_empty(){"创建用户"}else{"编辑用户及角色"}}</h2><button type="button" class="close" disabled=move||busy.get() on:click=move |_|editor.set(None)>"×"</button></div>
    <div class="editor-fields"><label>"账号"<input required minlength="2" maxlength="40" autocomplete="off" prop:value=move||editor.get().map(|v|strval(&v,"name")).unwrap_or_default() on:input=move|ev|editor.update(|v|if let Some(v)=v{v["name"]=json!(event_target_value(&ev))})/></label>
    <Show when=move||target.get().is_empty()><label>"初始密码"<input type="password" required minlength="10" maxlength="72" autocomplete="new-password" prop:value=move||editor.get().map(|v|strval(&v,"password")).unwrap_or_default() on:input=move|ev|editor.update(|v|if let Some(v)=v{v["password"]=json!(event_target_value(&ev))})/></label></Show>
    <label>"业务身份"<select disabled=move||!target.get().is_empty() prop:value=move||editor.get().map(|v|strval(&v,"identity")).unwrap_or_default() on:change=move|ev|editor.update(|v|if let Some(v)=v{v["identity"]=json!(event_target_value(&ev));v["merchantId"]=json!("");v["roleIds"]=json!([]);})><option value="internal">"内部"</option><option value="merchant">"商户"</option></select></label>
    <Show when=move||editor.get().is_some_and(|v|v["identity"]=="merchant")><label>"归属商户"<select required disabled=move||!target.get().is_empty() prop:value=move||editor.get().map(|v|strval(&v,"merchantId")).unwrap_or_default() on:change=move|ev|editor.update(|v|if let Some(v)=v{v["merchantId"]=json!(event_target_value(&ev))})><option value="">"请选择商户"</option><For each=move||merchants.get() key=|m|m.id.clone() let:m><option value=m.id>{format!("{}{}",m.name,if m.enabled{""}else{"（商户已停用）"})}</option></For></select></label></Show>
    <label>"账号状态"<select prop:value=move||if editor.get().is_some_and(|v|v["enabled"]==true){"true"}else{"false"} on:change=move|ev|editor.update(|v|if let Some(v)=v{v["enabled"]=json!(event_target_value(&ev)=="true")})><option value="true">"启用"</option><option value="false">"停用"</option></select></label></div>
    <fieldset class="permission-box"><legend>"分配角色（可多选）"</legend><For each=move||{let identity=editor.get().map(|v|strval(&v,"identity")).unwrap_or_default();roles.get().into_iter().filter(|r|r.identity==identity).collect::<Vec<_>>()} key=|r|r.id.clone() let:r>{let rid=r.id.clone();let checked_id=rid.clone();view!{<label class="permission-choice"><input type="checkbox" prop:checked=move||editor.get().is_some_and(|v|selected(&v,"roleIds",&checked_id)) disabled=move||!r.enabled&&!editor.get().is_some_and(|v|selected(&v,"roleIds",&r.id)) on:change=move|ev|editor.update(|v|if let Some(v)=v{toggle(v,"roleIds",&rid,event_target_checked(&ev))})/>{format!("{}{}",r.name,if r.enabled{""}else{"（停用，保存时请取消）"})}</label>}}</For><p class="subtle">"仅可分配同身份的启用角色；没有角色的普通用户没有业务访问权限。"</p></fieldset>
    <p class="form-error" role="alert">{move||error.get()}</p><div class="form-actions"><button type="button" class="button secondary" disabled=move||busy.get() on:click=move |_|editor.set(None)>"取消"</button><button class="button primary" disabled=move||busy.get()>"保存用户"</button></div></form></div></Show>
    <Show when=move||reset.get().is_some()><div class="modal-backdrop"><form class="modal small-modal" on:submit=move|ev|{ev.prevent_default();if busy.get(){return}busy.set(true);error.set(String::new());let uid=reset.get().unwrap_or_default().id;let new_password=password.get();spawn_local(async move{match api("POST",&format!("/api/users/{uid}/password"),Some(json!({"password":new_password}))).await{Ok(_)=>{reset.set(None);password.set(String::new());notice.set("密码已重置，该用户需重新登录".into());},Err(e)=>error.set(e)}busy.set(false);});}><div class="modal-head"><h2>{move||format!("重置密码 · {}",reset.get().unwrap_or_default().name)}</h2><button type="button" class="close" disabled=move||busy.get() on:click=move |_|{reset.set(None);password.set(String::new());}>"×"</button></div><label>"新密码"<input type="password" required minlength="10" maxlength="72" autocomplete="new-password" prop:value=move||password.get() on:input=move|ev|password.set(event_target_value(&ev))/></label><p>"保存后，该账号的已有登录会话全部失效。"</p><p class="form-error">{move||error.get()}</p><div class="form-actions"><button class="button primary" disabled=move||busy.get()>"确认重置"</button></div></form></div></Show>
    }
}

fn load_roles(
    rows: RwSignal<Vec<Role>>,
    catalog: RwSignal<Vec<Permission>>,
    error: RwSignal<String>,
) {
    spawn_local(async move {
        match api("GET", "/api/access/roles", None).await {
            Ok(v) => rows.set(serde_json::from_value(v).unwrap_or_default()),
            Err(e) => error.set(e),
        }
        match api("GET", "/api/access/permissions", None).await {
            Ok(v) => catalog.set(serde_json::from_value(v).unwrap_or_default()),
            Err(e) => error.set(e),
        }
    });
}
#[component]
pub fn RolesPage() -> impl IntoView {
    let rows = RwSignal::new(Vec::<Role>::new());
    let catalog = RwSignal::new(Vec::<Permission>::new());
    let error = RwSignal::new(String::new());
    let notice = RwSignal::new(String::new());
    let editor = RwSignal::new(None::<Value>);
    let target = RwSignal::new(String::new());
    let busy = RwSignal::new(false);
    let merchants = RwSignal::new(Vec::<Merchant>::new());
    spawn_local(async move {
        match api("GET", "/api/merchants", None).await {
            Ok(v) => merchants.set(serde_json::from_value(v).unwrap_or_default()),
            Err(e) => error.set(e),
        }
    });

    load_roles(rows, catalog, error);
    view! {
    <div class="page-heading"><div><h1>"角色与权限"</h1><p>"按页面勾选查看权限，再分别授权页面操作；多个角色的权限合并生效。"</p></div><button class="button primary" on:click=move |_|{target.set(String::new());error.set(String::new());editor.set(Some(json!({"name":"","identity":"internal","enabled":true,"permissions":[],"dataScope":"all","merchantIds":[]})));}>"＋ 创建角色"</button></div>
    <p class="form-error" role="alert">{move||error.get()}</p><p class="success-note">{move||notice.get()}</p>
    <section class="panel table-panel"><div class="table-scroll"><table><thead><tr><th>"角色"</th><th>"适用身份"</th><th>"已授权权限数"</th><th>"数据查看范围"</th><th>"状态"</th><th>"操作"</th></tr></thead><tbody><For each=move||rows.get() key=|r|format!("{r:?}") let:r>{let edit=r.clone();view!{<tr><td><strong>{r.name}</strong><small>{r.id}</small></td><td>{label(&r.identity)}</td><td>{r.permissions.len()}</td><td>{match r.data_scope.as_str(){"all"=>"全部数据","selected_merchants"=>"指定商户",_=>if r.identity=="merchant"{"当前账号所属商户"}else{"未配置"}}}<small>{if r.data_scope=="selected_merchants"{format!("{} 个商户",r.merchant_ids.len())}else{String::new()}}</small></td><td>{if r.enabled{"启用"}else{"停用"}}</td><td><button class="text-button" on:click=move |_|{target.set(edit.id.clone());error.set(String::new());editor.set(Some(json!({"name":edit.name,"identity":edit.identity,"enabled":edit.enabled,"permissions":edit.permissions,"dataScope":edit.data_scope,"merchantIds":edit.merchant_ids})));}>"配置菜单及操作权限"</button></td></tr>}}</For></tbody></table></div></section>
    <Show when=move||editor.get().is_some()><div class="modal-backdrop"><form class="modal access-modal" on:submit=move|ev|{ev.prevent_default();if busy.get(){return}busy.set(true);error.set(String::new());let data=editor.get().unwrap_or_default();let rid=target.get();let creating=rid.is_empty();let path=if creating{"/api/access/roles".into()}else{format!("/api/access/roles/{rid}")};spawn_local(async move{match api(if creating{"POST"}else{"PATCH"},&path,Some(data)).await{Ok(_)=>{editor.set(None);notice.set("角色权限已保存，后续请求按新权限执行".into());load_roles(rows,catalog,error)},Err(e)=>error.set(e)}busy.set(false);});}>
    <div class="modal-head"><h2>"配置角色"</h2><button type="button" class="close" disabled=move||busy.get() on:click=move |_|editor.set(None)>"×"</button></div>
    <div class="editor-fields"><label>"角色名称"<input required maxlength="100" prop:value=move||editor.get().map(|v|strval(&v,"name")).unwrap_or_default() on:input=move|ev|editor.update(|v|if let Some(v)=v{v["name"]=json!(event_target_value(&ev))})/></label>
    <label>"适用身份"<select disabled=move||!target.get().is_empty() prop:value=move||editor.get().map(|v|strval(&v,"identity")).unwrap_or_default() on:change=move|ev|editor.update(|v|if let Some(v)=v{v["identity"]=json!(event_target_value(&ev));v["permissions"]=json!([]);v["dataScope"]=json!(if v["identity"]=="merchant"{"own_merchant"}else{"all"});v["merchantIds"]=json!([]);})><option value="internal">"内部"</option><option value="merchant">"商户"</option></select></label>
    <label>"角色状态"<select prop:value=move||if editor.get().is_some_and(|v|v["enabled"]==true){"true"}else{"false"} on:change=move|ev|editor.update(|v|if let Some(v)=v{v["enabled"]=json!(event_target_value(&ev)=="true")})><option value="true">"启用"</option><option value="false">"停用"</option></select></label></div>
    <fieldset class="permission-box"><legend>"数据查看范围"</legend><div class="editor-fields"><label>"范围"<select prop:value=move||editor.get().map(|v|strval(&v,"dataScope")).unwrap_or_default() on:change=move|ev|editor.update(|v|if let Some(v)=v{v["dataScope"]=json!(event_target_value(&ev));v["merchantIds"]=json!([]);})>
    {move||if editor.get().is_some_and(|v|v["identity"]=="merchant"){view!{<option value="own_merchant">"当前账号所属商户"</option>}.into_any()}else{view!{<option value="own_merchant" disabled>"请选择范围"</option><option value="all">"全部数据"</option><option value="selected_merchants">"指定商户"</option>}.into_any()}}</select></label></div>
    <Show when=move||editor.get().is_some_and(|v|v["dataScope"]=="selected_merchants")><div class="permission-actions scope-merchants"><For each=move||merchants.get() key=|m|m.id.clone() let:m>{let id=m.id.clone();let checked=id.clone();view!{<label class="permission-choice"><input type="checkbox" prop:checked=move||editor.get().is_some_and(|v|selected(&v,"merchantIds",&checked)) on:change=move|ev|editor.update(|v|if let Some(v)=v{toggle(v,"merchantIds",&id,event_target_checked(&ev))})/>{format!("{} · {}",m.name,m.id)}</label>}}</For></div><Show when=move||merchants.get().is_empty()><p>"暂无商户，请先在商户列表创建商户。"</p></Show></Show>
    <p class="subtle">"范围按数据归属商户判断，与创建人无关。同一页面的多个角色范围取并集；列表、详情、统计及导出统一生效。商户账号始终限于其归属商户。"</p><p class="subtle">"没有商户归属的全局配置仅在“全部数据”范围可见。"</p></fieldset>
    <p class="demo-note">"用户及角色管理仅限超管。商户角色仅列出商户可用功能，数据始终限定为所属商户。"</p>
    <div class="permission-toolbar"><button type="button" class="text-button" on:click=move |_|{let merchant=editor.get().is_some_and(|v|v["identity"]=="merchant");let grants=catalog.get().into_iter().filter(|p|!merchant||p.merchant).map(|p|p.code).collect::<Vec<_>>();editor.update(|v|if let Some(v)=v{v["permissions"]=json!(grants)})}>"全选当前身份权限"</button><button type="button" class="text-button" on:click=move |_|editor.update(|v|if let Some(v)=v{v["permissions"]=json!([])})>"清空权限"</button></div>
    <div class="permission-tree"><For each=move||{let merchant=editor.get().is_some_and(|v|v["identity"]=="merchant");catalog.get().into_iter().filter(|p|p.code.ends_with(".view")&&(!merchant||p.merchant)).collect::<Vec<_>>()} key=|p|p.code.clone() let:page_permission>{let page=page_permission.page.clone();let view_code=page_permission.code.clone();let checked=view_code.clone();view!{
    <fieldset class="permission-box"><legend><label class="permission-choice"><input type="checkbox" prop:checked=move||editor.get().is_some_and(|v|selected(&v,"permissions",&checked)) on:change={let page=page.clone();move|ev|{let on=event_target_checked(&ev);editor.update(|v|if let Some(v)=v{toggle(v,"permissions",&view_code,on);if !on {let prefix=format!("{page}.");let remaining=v["permissions"].as_array().cloned().unwrap_or_default().into_iter().filter(|x|!x.as_str().unwrap_or_default().starts_with(&prefix)).collect::<Vec<_>>();v["permissions"]=json!(remaining);}})}}/>{page_permission.name}</label></legend>
    <div class="permission-actions"><For each={let page=page.clone();move||{let merchant=editor.get().is_some_and(|v|v["identity"]=="merchant");catalog.get().into_iter().filter(|p|p.page==page&&!p.code.ends_with(".view")&&(!merchant||p.merchant)).collect::<Vec<_>>()}} key=|p|p.code.clone() let:p>{let code=p.code.clone();let checked=code.clone();view!{<label class="permission-choice"><input type="checkbox" prop:checked=move||editor.get().is_some_and(|v|selected(&v,"permissions",&checked)) on:change=move|ev|{let on=event_target_checked(&ev);editor.update(|v|if let Some(v)=v{toggle(v,"permissions",&code,on);if on {for dep in &p.requires{toggle(v,"permissions",dep,true)}}});}/>{p.name.split('：').next_back().unwrap_or_default().to_string()}</label>}}</For></div></fieldset>
    }}</For></div><p class="form-error" role="alert">{move||error.get()}</p><div class="form-actions"><button type="button" class="button secondary" disabled=move||busy.get() on:click=move |_|editor.set(None)>"取消"</button><button class="button primary" disabled=move||busy.get()>"保存角色权限"</button></div>
    </form></div></Show>
    }
}
#[component]
pub fn OwnPassword() -> impl IntoView {
    let open = RwSignal::new(false);
    let old = RwSignal::new(String::new());
    let password = RwSignal::new(String::new());
    let error = RwSignal::new(String::new());
    let busy = RwSignal::new(false);
    view! {<button class="text-button" on:click=move |_|{old.set(String::new());password.set(String::new());error.set(String::new());open.set(true)}>"修改密码"</button><Show when=move||open.get()><div class="modal-backdrop"><form class="modal small-modal" on:submit=move|ev|{ev.prevent_default();if busy.get(){return}busy.set(true);let body=json!({"currentPassword":old.get(),"password":password.get()});spawn_local(async move{match api("POST","/api/auth/password",Some(body)).await{Ok(_)=>{if let Some(w)=web_sys::window(){let _=w.location().reload();}},Err(e)=>error.set(e)}busy.set(false);});}><div class="modal-head"><h2>"修改密码"</h2><button class="close" type="button" disabled=move||busy.get() on:click=move |_|open.set(false)>"×"</button></div><label>"原密码"<input required type="password" autocomplete="current-password" prop:value=move||old.get() on:input=move|ev|old.set(event_target_value(&ev))/></label><label>"新密码"<input required type="password" minlength="10" maxlength="72" autocomplete="new-password" prop:value=move||password.get() on:input=move|ev|password.set(event_target_value(&ev))/></label><p>"保存后请使用新密码重新登录。"</p><p class="form-error">{move||error.get()}</p><div class="form-actions"><button class="button primary" disabled=move||busy.get()>"确认修改"</button></div></form></div></Show>}
}

#[component]
pub fn QueryField(
    page: &'static str,
    permission: &'static str,
    label: &'static str,
    value: RwSignal<String>,
    #[prop(default = "text")] kind: &'static str,
) -> impl IntoView {
    let access = access();
    view! {<Show when=move||access.can(&format!("{page}.{permission}"))><label><span>{label}</span><input type=kind prop:value=move||value.get() on:input=move|ev|value.set(event_target_value(&ev))/></label></Show>}
}
