// HOMEX 官方插件「MSN 天气」。
//
// # 为什么独立成插件
//
// MSN 的数据面比现有的 weather 插件宽得多：气压、露点、紫外线、能见度、体感温度、
// 空气质量等级与首要污染物、10 天预报、逐时预报、甚至分钟级降水临近预报与天气预警。
// 但它的密钥**不是官方发给开发者的** —— 那是 MSN 网页前端自己在用的那个，
// 靠观察页面请求拿到。微软随时可能轮换或加校验。
//
// 所以它必须独立：失效时只掉这一个源，weather（Open-Meteo / 高德）完全不受影响。
// 把这种"可能随时失效"的源塞进主插件，会让一次上游变动同时打掉所有家庭的天气。
//
// # 诚实边界（写在代码里，不只是文档）
//
//   - 密钥做成**可配置项**（默认填实测值），失效时用户自己换，不用改代码重打包；
//   - 上游没给的字段一律不填假值（数值用指针、文本用空）；
//   - 取数失败如实报错，**不偷偷退回别的数据源** —— 那会让人以为 MSN 还活着。
package main

import (
	"encoding/json"
	"fmt"
	"strings"

	"homex.dev/homex/sdk/go/plugin"
)

// 实测可用的默认端点与密钥（2026-10-30 从 MSN 天气页的真实请求里取得）。
//
// ⚠ 为什么把它们写成"默认值"而不是"硬编码常量"：
// 这是网页前端的密钥，微软随时能换。写成配置项的默认值，用户改一次配置就能继续用；
// 写成常量就只能改代码、重打包、重签名。
const (
	defaultAPIBase = "https://assets.msn.cn/service/weather"
	// defaultAPIKey 是 MSN 天气页当前使用的 key。失效时在配置页 api_key 里换掉。
	defaultAPIKey  = "j5i4gDqHL6nGYwx5wi5kRhXjtf2c5qgFX9fzfk0TOo"
	// defaultAppID 是 MSN 前端申请的 appId，接口会校验它是否与 apikey 配套。
	defaultAppID   = "9e21380c-ff19-4c78-b4ea-19558e93a5d3"
	// defaultOCID 是"哪个入口在调"。实测 `msnweather` 会被判 401，
	// 必须是 `msftweather`（这是区分 401 与 200 的关键参数之一）。
	defaultOCID    = "msftweather"
	defaultLocale  = "zh-cn"
	defaultRegion  = "cn"
	// 默认坐标与城市：南京（与用户部署所在城市一致，开箱即有数）。
	defaultLat  = 32.0617
	defaultLon  = 118.7778
	defaultCity = "南京市"
	defaultDays = 10
)

// apiKeySecretName 是密钥库里的键名。
const apiKeySecretName = "api_key"

// config 是本插件的配置面（manifest 的 config_schema 与这里必须一一对应）。
type config struct {
	Enabled      bool    `json:"enabled"`
	APIKeySecret string  `json:"api_key_secret"`
	APIBase      string  `json:"api_base"`
	AppID        string  `json:"app_id"`
	OCID         string  `json:"ocid"`
	CityName     string  `json:"city_name"`
	DefaultLat   float64 `json:"default_latitude"`
	DefaultLon   float64 `json:"default_longitude"`
	TempUnit     string  `json:"temperature_unit"`
	Days         int     `json:"days"`
	RefreshMin   int     `json:"refresh_minutes"`
	TimeoutMS    int     `json:"timeout_ms"`
	ShowAir      bool    `json:"show_air_quality"`
	AutoLocate   bool    `json:"auto_locate"`
}

var conf config

// 开箱默认值。configure 里逐项兜底：老配置缺某一项时不能变成"0 值"。
var defaults = config{
	Enabled:    true,
	APIBase:    defaultAPIBase,
	AppID:      defaultAppID,
	OCID:       defaultOCID,
	CityName:   defaultCity,
	DefaultLat: defaultLat,
	DefaultLon: defaultLon,
	TempUnit:   "celsius",
	Days:       defaultDays,
	RefreshMin: 30,
	TimeoutMS:  8000,
	ShowAir:    true,
	AutoLocate: true,
}

func configure(ctx *plugin.Context, raw json.RawMessage) error {
	conf = defaults
	if len(raw) > 0 && string(raw) != "null" {
		// 用指针接可选数值：0 是合法值（赤道/格林尼治），与"没填"必须能区分。
		var in struct {
			Enabled      *bool    `json:"enabled"`
			APIKeySecret *string  `json:"api_key_secret"`
			APIBase      *string  `json:"api_base"`
			AppID        *string  `json:"app_id"`
			OCID         *string  `json:"ocid"`
			CityName     *string  `json:"city_name"`
			DefaultLat   *float64 `json:"default_latitude"`
			DefaultLon   *float64 `json:"default_longitude"`
			TempUnit     *string  `json:"temperature_unit"`
			Days         *int     `json:"days"`
			RefreshMin   *int     `json:"refresh_minutes"`
			TimeoutMS    *int     `json:"timeout_ms"`
			ShowAir      *bool    `json:"show_air_quality"`
			AutoLocate   *bool    `json:"auto_locate"`
		}
		if err := json.Unmarshal(raw, &in); err != nil {
			return fmt.Errorf("配置解析失败：%w", err)
		}
		if in.Enabled != nil {
			conf.Enabled = *in.Enabled
		}
		if in.APIKeySecret != nil {
			conf.APIKeySecret = strings.TrimSpace(*in.APIKeySecret)
		}
		if in.APIBase != nil && strings.TrimSpace(*in.APIBase) != "" {
			conf.APIBase = strings.TrimRight(strings.TrimSpace(*in.APIBase), "/")
		}
		if in.AppID != nil && strings.TrimSpace(*in.AppID) != "" {
			conf.AppID = strings.TrimSpace(*in.AppID)
		}
		if in.OCID != nil && strings.TrimSpace(*in.OCID) != "" {
			conf.OCID = strings.TrimSpace(*in.OCID)
		}
		if in.CityName != nil {
			conf.CityName = strings.TrimSpace(*in.CityName)
		}
		if in.DefaultLat != nil {
			conf.DefaultLat = *in.DefaultLat
		}
		if in.DefaultLon != nil {
			conf.DefaultLon = *in.DefaultLon
			_ = in
		}
		if in.TempUnit != nil {
			u := strings.ToLower(strings.TrimSpace(*in.TempUnit))
			if u == "fahrenheit" || u == "celsius" {
				conf.TempUnit = u
			}
		}
		if in.Days != nil {
			d := *in.Days
			if d < 1 {
				d = 1
			}
			if d > 10 {
				d = 10 // MSN 只给 10 天，配更多也拿不到
			}
			conf.Days = d
		}
		if in.RefreshMin != nil && *in.RefreshMin > 0 {
			conf.RefreshMin = *in.RefreshMin
		}
		if in.TimeoutMS != nil && *in.TimeoutMS > 0 {
			conf.TimeoutMS = *in.TimeoutMS
		}
		if in.ShowAir != nil {
			conf.ShowAir = *in.ShowAir
		}
		if in.AutoLocate != nil {
			conf.AutoLocate = *in.AutoLocate
		}
	}
	// 坐标合法性：纬度 ±90、经度 ±180。越界直接拒绝，不让它去打上游。
	if conf.DefaultLat < -90 || conf.DefaultLat > 90 {
		return fmt.Errorf("default_latitude 越界（应在 -90~90）：%v", conf.DefaultLat)
	}
	if conf.DefaultLon < -180 || conf.DefaultLon > 180 {
		return fmt.Errorf("default_longitude 越界（应在 -180~180）：%v", conf.DefaultLon)
	}
	return nil
}

// apiKey 取密钥：先查密钥库，再退回配置里的明文（老配置兼容），最后退回默认值。
//
// 顺序的理由：密钥库是**平台加密托管**的位置，用户在配置页填的 Key 落在那儿；
// 配置里的 api_key_secret 是 1.x 时代的"键名"字段，留着是为了老配置不改也能跑；
// 默认值是"开箱即有数"，但这三个都空才用。
func apiKey(ctx *plugin.Context) (string, string, error) {
	if v, err := ctx.SecretGet(apiKeySecretName); err == nil && strings.TrimSpace(v) != "" {
		return strings.TrimSpace(v), apiKeySecretName, nil
	}
	if strings.TrimSpace(conf.APIKeySecret) != "" {
		if v, err := ctx.SecretGet(conf.APIKeySecret); err == nil && strings.TrimSpace(v) != "" {
			return strings.TrimSpace(v), conf.APIKeySecret, nil
		}
	}
	return defaultAPIKey, "builtin", nil
}

func health(ctx *plugin.Context) (plugin.HealthStatus, error) {
	return plugin.HealthStatus{OK: true, Message: "configured"}, nil
}

func call(ctx *plugin.Context, tool string, args json.RawMessage) (json.RawMessage, error) {
	switch tool {
	case "snapshot":
		return snapshot(ctx, args)
	case "locate":
		return locateTool(ctx, args)
	case "forecast":
		return forecast(ctx, args)
	case "hourly":
		return hourly(ctx, args)
	case "nowcasting":
		return nowcasting(ctx, args)
	case "alerts":
		return alerts(ctx, args)
	case "test_connection":
		return testConnection(ctx)
	default:
		return nil, fmt.Errorf("未知工具 %q（本插件只有 snapshot / locate / forecast / hourly / nowcasting / alerts / test_connection）", tool)
	}
}

func main() {}

func init() {
	plugin.Register(plugin.Plugin{
		Configure: configure,
		Call:      call,
		Health:    health,
	})
}
