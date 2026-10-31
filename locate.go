package main

import (
	"encoding/json"
	"fmt"
	"strings"

	"homex.dev/homex/sdk/go/plugin"
)

// locate 按出口 IP 自动定位（2026-10-30 用户要求「自动定位自动获取就好」）。
//
// # 为什么用出口 IP 而不是浏览器 Geolocation
//
// 浏览器 Geolocation 要用户点授权弹窗，而仪表盘卡片是"打开就该有数"的东西；
// 出口 IP 定位不需要任何交互、也不需要前端配合。它精度到城市/区（不精确到米），
// 而天气卡片恰好只需要城市级精度 —— 之后的「精确到区」由 locations/search 反查补齐。
//
// # 三家上游轮询而不是只用一家
//
// 三家都免密钥（2026-10-30 实测均 200）。只用一家的话，那家挂了定位就整体失效；
// 依次尝试能让"某一家被墙/限流"变成一个可恢复的小故障。
//
// 返回的 source 字段如实标出是谁给的坐标，便于排查
// （「定位不准」时能立刻知道是哪家上游给的）。
type ipLocation struct {
	City    string  `json:"city"`
	Region  string  `json:"region,omitempty"`
	Country string  `json:"country,omitempty"`
	Lat     float64 `json:"latitude"`
	Lon     float64 `json:"longitude"`
	Source  string  `json:"source"`
}

// ipLocate 依次问三家 IP 归属地服务，第一家给出可用坐标就停。
func ipLocate(ctx *plugin.Context) (ipLocation, error) {
	var errs []string
	for _, f := range []struct {
		name string
		url  string
		parse func([]byte) (ipLocation, error)
	}{
		{"ip-api.com", "http://ip-api.com/json/?fields=status,city,regionName,country,lat,lon", parseIPAPI},
		{"ipwho.is", "https://ipwho.is/", parseIPWhoIs},
		{"ipinfo.io", "https://ipinfo.io/json", parseIPInfo},
	} {
		req := plugin.HTTPRequest{Method: "GET", URL: f.url, TimeoutMS: 4000}
		resp, err := ctx.HTTPRequest(req)
		if err != nil {
			errs = append(errs, f.name+"："+err.Error())
			continue
		}
		body, err := resp.Body()
		if err != nil || resp.Status < 200 || resp.Status >= 300 {
			errs = append(errs, fmt.Sprintf("%s：HTTP %d", f.name, resp.Status))
			continue
		}
		loc, perr := f.parse(body)
		if perr != nil {
			errs = append(errs, f.name+"："+perr.Error())
			continue
		}
		loc.Source = f.name
		return loc, nil
	}
	return ipLocation{}, fmt.Errorf("三家 IP 归属地服务都没给出坐标（%s）", strings.Join(errs, "；"))
}

func parseIPAPI(body []byte) (ipLocation, error) {
	var r struct {
		Status string  `json:"status"`
		City   string  `json:"city"`
		Region string  `json:"regionName"`
		Country string `json:"country"`
		Lat    float64 `json:"lat"`
		Lon    float64 `json:"lon"`
	}
	if err := json.Unmarshal(body, &r); err != nil {
		return ipLocation{}, fmt.Errorf("响应不是 JSON：%w", err)
	}
	if r.Status != "success" {
		return ipLocation{}, fmt.Errorf("上游返回 status=%q", r.Status)
	}
	if r.Lat == 0 && r.Lon == 0 {
		// (0,0) 是几内亚湾，几乎必然是"没给坐标"而不是真有设备在那儿
		return ipLocation{}, fmt.Errorf("没有给出坐标（lat/lon 均为 0）")
	}
	return ipLocation{City: r.City, Region: r.Region, Country: r.Country, Lat: r.Lat, Lon: r.Lon}, nil
}

func parseIPWhoIs(body []byte) (ipLocation, error) {
	var r struct {
		Success   bool    `json:"success"`
		City      string  `json:"city"`
		Region    string  `json:"region"`
		Country   string  `json:"country"`
		Latitude  float64 `json:"latitude"`
		Longitude float64 `json:"longitude"`
	}
	if err := json.Unmarshal(body, &r); err != nil {
		return ipLocation{}, fmt.Errorf("响应不是 JSON：%w", err)
	}
	if !r.Success {
		return ipLocation{}, fmt.Errorf("上游返回 success=false")
	}
	if r.Latitude == 0 && r.Longitude == 0 {
		return ipLocation{}, fmt.Errorf("没有给出坐标")
	}
	return ipLocation{City: r.City, Region: r.Region, Country: r.Country, Lat: r.Latitude, Lon: r.Longitude}, nil
}

func parseIPInfo(body []byte) (ipLocation, error) {
	var r struct {
		City  string `json:"city"`
		Region string `json:"region"`
		Country string `json:"country"`
		Loc   string `json:"loc"` // "lat,lon" 形式
	}
	if err := json.Unmarshal(body, &r); err != nil {
		return ipLocation{}, fmt.Errorf("响应不是 JSON：%w", err)
	}
	if strings.TrimSpace(r.Loc) == "" {
		return ipLocation{}, fmt.Errorf("没有 loc 字段")
	}
	parts := strings.Split(r.Loc, ",")
	if len(parts) != 2 {
		return ipLocation{}, fmt.Errorf("loc 格式不是 lat,lon：%q", r.Loc)
	}
	var lat, lon float64
	if _, err := fmt.Sscanf(parts[0], "%f", &lat); err != nil {
		return ipLocation{}, fmt.Errorf("纬度解析失败：%w", err)
	}
	if _, err := fmt.Sscanf(parts[1], "%f", &lon); err != nil {
		return ipLocation{}, fmt.Errorf("经度解析失败：%w", err)
	}
	if lat == 0 && lon == 0 {
		return ipLocation{}, fmt.Errorf("没有给出坐标")
	}
	return ipLocation{City: r.City, Region: r.Region, Country: r.Country, Lat: lat, Lon: lon}, nil
}

// locateTool 是暴露给平台/前端的 locate 工具实现。
func locateTool(ctx *plugin.Context, args json.RawMessage) (json.RawMessage, error) {
	// 调用方给了坐标就直接用（浏览器 Geolocation 更准），否则按出口 IP 定位。
	var a pointArgs
	if len(args) > 0 && string(args) != "null" {
		if err := json.Unmarshal(args, &a); err != nil {
			return nil, fmt.Errorf("入参解析失败：%w", err)
		}
	}
	if a.Latitude != nil && a.Longitude != nil {
		out := map[string]any{
			"latitude":  *a.Latitude,
			"longitude": *a.Longitude,
			"source":    "caller",
		}
		// 有能力就顺手反查地名，让调用方拿到「江苏省南京市溧水区」
		if pi, err := reverseGeocode(ctx, *a.Latitude, *a.Longitude); err == nil {
			out["city"] = pi.Display()
			out["place"] = map[string]any{
				"full": pi.Full, "locality": pi.Locality,
				"city": pi.City, "province": pi.Province, "postal_code": pi.PostalCode,
			}
		}
		return json.Marshal(out)
	}

	loc, err := ipLocate(ctx)
	if err != nil {
		return nil, err
	}
	out := map[string]any{
		"latitude":  loc.Lat,
		"longitude": loc.Lon,
		"source":    "ip:" + loc.Source,
	}
	if pi, rerr := reverseGeocode(ctx, loc.Lat, loc.Lon); rerr == nil {
		out["city"] = pi.Display()
		out["place"] = map[string]any{
			"full": pi.Full, "locality": pi.Locality,
			"city": pi.City, "province": pi.Province, "postal_code": pi.PostalCode,
		}
	} else if loc.City != "" {
		// 反查失败时用 IP 服务给的城市名（精度低一级，但比没有强）
		out["city"] = loc.City
	}
	return json.Marshal(out)
}
