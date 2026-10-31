package main

import (
	"net/url"
	"regexp"
	"strings"

	"homex.dev/homex/sdk/go/plugin"
)

// keyProbePage 是密钥探测的取样页面：MSN 天气首页。
//
// 为什么探测的是这个页面而不是接口文档：这把 key 只出现在页面自己的请求里，
// 官方没有发过它，也就没有"文档"可看。
const keyProbePage = "https://www.msn.cn/zh-cn/weather"

// apikeyPattern 匹配 MSN 页面里出现的密钥形态。
//
// 观察到的形态是「32 位十六进制-guid-4 位数字」两段式，例如
// f8857dedc6f54ca8962cfb713e01e7d7-e9250191-fe0b-446f-95ae-07516262f98c-7028。
// 提取时**只取第一段**（与页面自己的 `indexOf("-")` 截断逻辑一致）。
var apikeyPattern = regexp.MustCompile(`([a-f0-9]{32})-([0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12})-\d{3,4}`)

// probeKeys 从 MSN 天气页抓一批候选密钥。
//
// # 诚实说明这个函数能做什么、不能做什么
//
// 能做：微软如果把新 key **写进页面 HTML**（哪怕是内联脚本），我们能扫到并自动换上。
//
// 不能做：MSN 的页面是 JS 壳，key 由脚本在**运行时**请求注入内存。
// 实测 2026-10-30：静态 HTML 里能扫到 2 个候选 key，但它们**都是 401**
// （是别的服务用的），真正能用的那把不在 HTML 里。
//
// 所以这只是"尽力而为"的兜底：探测到候选就试，试不通就如实报错让用户手填。
// 不把它包装成"自动更新密钥成功" —— 那会让人误以为以后永远不用管了。
func probeKeys(ctx *plugin.Context) []string {
	req := plugin.HTTPRequest{Method: "GET", URL: keyProbePage, TimeoutMS: conf.TimeoutMS}
	resp, err := ctx.HTTPRequest(req)
	if err != nil {
		return nil
	}
	body, err := resp.Body()
	if err != nil || resp.Status < 200 || resp.Status >= 300 {
		return nil
	}
	seen := map[string]bool{}
	var out []string
	for _, m := range apikeyPattern.FindAllStringSubmatch(string(body), -1) {
		k := m[1] // 只取第一段
		if seen[k] {
			continue
		}
		seen[k] = true
		out = append(out, k)
	}
	return out
}

// tryKeysOnOverview 用给定候选 key 依次真打一次 overview，返回第一个成功的 key。
//
// 小请求：days=1，只为验证"这个 key 能不能用"，不为取数据。
func tryKeysOnOverview(ctx *plugin.Context, lat, lon float64, keys []string) (string, bool) {
	for _, k := range keys {
		if strings.TrimSpace(k) == "" {
			continue
		}
		q := url.Values{}
		q.Set("apikey", k)
		q.Set("ocid", conf.OCID)
		q.Set("appId", conf.AppID)
		q.Set("apiversion", "1.0")
		q.Set("locale", defaultLocale)
		q.Set("market", defaultLocale)
		q.Set("region", defaultRegion)
		q.Set("it", "web")
		q.Set("scn", "ANON")
		q.Set("lat", "31.0")
		q.Set("lon", "118.0")
		q.Set("days", "1")
		req := plugin.HTTPRequest{Method: "GET", URL: conf.APIBase + "/overview?" + q.Encode(), TimeoutMS: 4000}
		resp, err := ctx.HTTPRequest(req)
		if err != nil {
			continue
		}
		if resp.Status >= 200 && resp.Status < 300 {
			return k, true
		}
	}
	return "", false
}
