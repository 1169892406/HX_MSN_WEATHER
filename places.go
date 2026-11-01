package main

import (
	"encoding/json"
	"fmt"
	"net/url"
	"strconv"
	"strings"

	"homex.dev/homex/sdk/go/plugin"
)

// locationsSearchKey 是 MSN「位置反查」接口专用的 key（与天气取数那把**不同**）。
//
// 2026-10-30 从 MSN 天气页的真实请求里观察到：页面在拿到经纬度后会调
// `/service/weather/locations/search` 把坐标换成地名，用的就是这一把
// （`ocid=localizelocation-peregrine`）。天气取数那把（j5i4gDqHL…）在这个端点上是 401。
//
// 两把 key 分开，是因为它们属于两个不同的服务入口，任何一把变动都只影响自己的功能。
const locationsSearchKey = "0QfOX3Vn51YCzitbLaRkTTBadtWpgTN8NZLW0C1SEM"

// placeInfo 是按坐标反查出来的行政地名。
//
// 这就是「精确到区」的来源：MSN 的天气接口本身**不返回任何地名**（只有经纬度），
// 网页上显示的「江苏省南京市溧水区」是页面另调一次 locations/search 换来的。
// 我们照做，才能在没有用户手动填地名的情况下也显示到区。
type placeInfo struct {
	// Full 是完整地址（如「江苏省南京市溧水区常溧路」）
	Full string
	// Locality 区/县（「溧水区」）
	Locality string
	// City 地级市（「南京市」）
	City string
	// Province 省（「江苏省」）
	Province string
	// PostalCode 邮编
	PostalCode string
}

// Display 拼出卡片头部要显示的那串字：「江苏省南京市溧水区」。
//
// 逐级降级：省级/市级缺失时就少显示一级，而不是显示「江苏省undefined」。
// 全部缺失返回空串 —— 调用方据此回落到配置里的城市名。
func (p placeInfo) Display() string {
	parts := make([]string, 0, 3)
	for _, v := range []string{p.Province, p.City, p.Locality} {
		s := strings.TrimSpace(v)
		if s == "" {
			continue
		}
		// 去重：直辖市的 adminDistrict 与 adminDistrict2 可能都是「北京市」
		if len(parts) > 0 && parts[len(parts)-1] == s {
			continue
		}
		parts = append(parts, s)
	}
	return strings.Join(parts, "")
}

// reverseGeocode 用经纬度反查行政地名。
//
// 失败时返回 (零值, error) 由调用方决定怎么降级 —— 卡片取数不该因为
// "地名查不到"而整份失败，所以调用方会把这里的错误降级成"用配置里的城市名"。
func reverseGeocode(ctx *plugin.Context, lat, lon float64) (placeInfo, error) {
	q := url.Values{}
	q.Set("apikey", locationsSearchKey)
	q.Set("ocid", "localizelocation-peregrine")
	q.Set("cm", defaultLocale)
	q.Set("locale", defaultLocale)
	q.Set("it", "web")
	q.Set("scn", "ANON")
	q.Set("lat", strconv.FormatFloat(lat, 'f', 6, 64))
	q.Set("lon", strconv.FormatFloat(lon, 'f', 6, 64))
	rawURL := conf.APIBase + "/locations/search?" + q.Encode()

	// 地名反查是**增强**不是必需：查不到就用配置城市名，天气照常显示。
	// 所以给它一个比天气取数更小的超时 —— 让它不能拖垮整个 snapshot 的预算。
	const geocodeTimeoutMS = 3500
	req := plugin.HTTPRequest{Method: "GET", URL: rawURL, TimeoutMS: geocodeTimeoutMS}
	resp, err := ctx.HTTPRequest(req)
	if err != nil {
		return placeInfo{}, classifyTransportError(err)
	}
	body, err := resp.Body()
	if err != nil {
		return placeInfo{}, fmt.Errorf("位置反查响应解码失败：%w", err)
	}
	if resp.Status < 200 || resp.Status >= 300 {
		return placeInfo{}, fmt.Errorf("位置反查返回 %d：%s", resp.Status, snippet(body))
	}

	// 响应是 OData 包了两层：value[].responses[].locations[]
	var wrap struct {
		Value []struct {
			Responses []struct {
				Locations []struct {
					Name          string `json:"name"`
					Locality      string `json:"locality"`
					AdminDistrict string `json:"adminDistrict"`
					AdminDist2    string `json:"adminDistrict2"`
					PostalCode    string `json:"postalCode"`
				} `json:"locations"`
			} `json:"responses"`
		} `json:"value"`
	}
	if err := json.Unmarshal(body, &wrap); err != nil {
		return placeInfo{}, fmt.Errorf("位置反查响应不是预期的 JSON 形状：%w", err)
	}
	for _, v := range wrap.Value {
		for _, r := range v.Responses {
			if len(r.Locations) == 0 {
				continue
			}
			// 取第一条：按距离排序，第一条就是最近的行政区
			l := r.Locations[0]
			return placeInfo{
				Full:       strings.TrimSpace(l.Name),
				Locality:   strings.TrimSpace(l.Locality),
				City:       strings.TrimSpace(l.AdminDist2),
				Province:   strings.TrimSpace(l.AdminDistrict),
				PostalCode: strings.TrimSpace(l.PostalCode),
			}, nil
		}
	}
	return placeInfo{}, fmt.Errorf("位置反查没有返回任何行政区（该坐标可能不在陆地上）")
}
