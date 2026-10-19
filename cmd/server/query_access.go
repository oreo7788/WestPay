package main

import (
	"encoding/json"
	"net/http"
	"strings"
)

type QueryFilter struct {
	Param, Key, Name string
	Merchant         bool
}

func queryFilters(page string) []QueryFilter {
	switch page {
	case "orders", "history":
		return []QueryFilter{
			{"systemId", "filter_system_id", "系统单号查询", true},
			{"merchantOrder", "filter_merchant_order", "商户单号查询", true},
			{"merchant", "filter_merchant", "商户查询", true},
			{"method", "filter_method", "支付方式查询", true},
			{"status", "filter_status", "订单状态查询", true},
			{"from", "filter_from", "开始日期查询", true},
			{"to", "filter_to", "结束日期查询", true},
			{"merchantId", "filter_merchant_id", "商户号查询", true},
			{"channelId", "filter_channel_id", "通道查询", true},
		}
	case "merchant_settlement", "finance_channels", "finance_products", "finance_merchants", "prepay", "statistics":
		return []QueryFilter{{"from", "filter_from", "开始日期查询", true}, {"to", "filter_to", "结束日期查询", true}, {"q", "filter_merchant", "商户名称查询", true}}
	case "merchants":
		return []QueryFilter{{"q", "filter_keyword", "商户名称或商户号查询", false}, {"enabled", "filter_status", "商户状态查询", false}}
	case "products", "channels", "receivers", "complaints", "official", "blacklist", "transfers", "user_ids":
		return []QueryFilter{{"q", "filter_keyword", "关键词查询", false}}
	}
	return nil
}

func financePermissionPage(r *http.Request) (string, error) {
	page := map[string]string{"channels": "finance_channels", "products": "finance_products", "merchants": "finance_merchants", "prepay": "prepay", "statistics": "statistics"}[r.PathValue("kind")]
	if page == "" {
		return "", notfound("报表不存在")
	}
	if selected := r.URL.Query().Get("reportPage"); selected != "" {
		if selected != page && !(page == "finance_merchants" && selected == "merchant_settlement") {
			return "", bad("报表页面无效")
		}
		page = selected
	}
	return page, nil
}

// Check filters on every request, including CSV exports and repeated parameters.
func authorizeQuery(r *http.Request, u User, page string) error {
	filters := queryFilters(page)
	if len(filters) == 0 {
		return nil
	}
	known := map[string]QueryFilter{}
	for _, f := range filters {
		known[f.Param] = f
	}
	controls := map[string]bool{}
	switch page {
	case "orders", "history":
		for _, key := range []string{"archive", "all", "page", "pageSize", "export"} {
			controls[key] = true
		}
	case "merchant_settlement", "finance_channels", "finance_products", "finance_merchants", "prepay", "statistics":
		controls["reportPage"] = true
		controls["export"] = true
	}
	for param, values := range r.URL.Query() {
		if len(values) != 1 {
			return bad("查询参数不能重复")
		}
		f, ok := known[param]
		if !ok {
			if controls[param] {
				continue
			}
			return bad("不支持的查询参数：" + param)
		}
		if strings.TrimSpace(values[0]) == "" {
			continue
		}
		if !u.can(page+".view") || !u.can(page+"."+f.Key) || u.merchant() && !f.Merchant {
			return apiError{403, "没有使用“" + f.Name + "”的权限"}
		}
		// An all-orders request covers both date partitions and their grants.
		if page == "orders" && r.URL.Query().Get("all") == "true" && !u.can("history."+f.Key) {
			return forbid()
		}
	}
	return nil
}

// Shared keyword matching for management lists; page/filter grants are checked
// before invoking the handlers. Fields here are the same public list response.
func filterKeyword[T any](r *http.Request, items []T) []T {
	keyword := strings.ToLower(strings.TrimSpace(r.URL.Query().Get("q")))
	if keyword == "" {
		return items
	}
	out := []T{}
	for _, item := range items {
		data, _ := json.Marshal(item)
		if strings.Contains(strings.ToLower(string(data)), keyword) {
			out = append(out, item)
		}
	}
	return out
}
