package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"strconv"
	"strings"

	"homex.dev/homex/sdk/go/plugin"
)

// pointArgs 是取数工具的入参（坐标优先，缺则用配置的默认坐标）。
type pointArgs struct {
	Latitude  *float64 `json:"latitude"`
	Longitude *float64 `json:"longitude"`
	Refresh   bool     `json:"refresh"`
}

// point 解析最终坐标。返回值里的 usedDefault 用于在响应里如实标注
// "这次用的是配置默认位置还是调用方给的" —— 用户看数据时能分清。
func (a pointArgs) point() (lat, lon float64, usedDefault bool, err error) {
	if a.Latitude != nil && a.Longitude != nil {
		return *a.Latitude, *a.Longitude, false, nil
	}
	if a.Latitude != nil || a.Longitude != nil {
		// 只给一半是错误用法：拿配置的一半配调用的一半会得到"半个虚构位置"。
		return 0, 0, false, fmt.Errorf("latitude 与 longitude 必须成对给（只给了一半）")
	}
	if conf.DefaultLat == 0 && conf.DefaultLon == 0 {
		// 配置里没有默认坐标 ⇒ 交给调用方去 IP 定位（见 fetchWithAutoLocate）。
		// 这里返回一个哨兵，而不是直接报错：早先的版本在这里就失败，
		// 用户必须手填坐标才能用，与"自动定位"的要求冲突。
		return 0, 0, false, errNeedLocate
	}
	return conf.DefaultLat, conf.DefaultLon, true, nil
}

// errNeedLocate 表示"没坐标，需要走自动定位"。调用方据此调 ipLocate。
var errNeedLocate = fmt.Errorf("需要自动定位")

// resolvePointWithLocate 是 point() 的**自动定位版**：没坐标就按出口 IP 定一次。
//
// 三层优先级（与用户「自动定位自动获取就好」的要求一致）：
//  1. 调用方给的坐标（浏览器 Geolocation，最准）
//  2. 配置里的默认坐标（用户显式设过，尊重它）
//  3. 出口 IP 定位（全自动，无需任何配置）
func resolvePointWithLocate(ctx *plugin.Context, a pointArgs) (lat, lon float64, source string, err error) {
	lat, lon, usedDefault, err := a.point()
	if err == nil {
		if usedDefault {
			return lat, lon, "config", nil
		}
		return lat, lon, "caller", nil
	}
	if !errors.Is(err, errNeedLocate) {
		return 0, 0, "", err
	}
	if !conf.AutoLocate {
		return 0, 0, "", fmt.Errorf("没有可用坐标，且自动定位已关闭（auto_locate=false）：" +
			"请填 default_latitude/default_longitude，或让调用方传坐标")
	}
	loc, lerr := ipLocate(ctx)
	if lerr != nil {
		return 0, 0, "", fmt.Errorf("没有可用坐标，且自动定位失败：%w。"+
			"请在配置页填 default_latitude/default_longitude，或让调用方传坐标", lerr)
	}
	return loc.Lat, loc.Lon, "ip:" + loc.Source, nil
}

// overviewURL 拼出一次取数的完整 URL。
//
// 参数集的来历（2026-10-30 实测钉的，不是猜的）：
//   - `ocid=msftweather` 是**关键**：写 `msnweather` 会稳定 401
//     （"App authentication info not found in the security db"）；
//   - `appId` 必须与 apikey 配套，缺了也 401；
//   - `days` 决定返回几天预报，上限 10；
//   - `units` 填 C 或 F，与 temperature_unit 对应。
//
// 少任何一个关键参数都会 401，而 401 的报错信息完全一样 —— 这就是把参数集写在一处的原因。
func overviewURL(lat, lon float64, key string) string {
	q := url.Values{}
	q.Set("apikey", key)
	q.Set("ocid", conf.OCID)
	q.Set("appId", conf.AppID)
	q.Set("apiversion", "1.0")
	q.Set("locale", defaultLocale)
	q.Set("market", defaultLocale)
	q.Set("region", defaultRegion)
	q.Set("cm", defaultLocale)
	q.Set("it", "web")
	q.Set("scn", "ANON")
	q.Set("lat", strconv.FormatFloat(lat, 'f', 4, 64))
	q.Set("lon", strconv.FormatFloat(lon, 'f', 4, 64))
	q.Set("days", strconv.Itoa(conf.Days))
	q.Set("startDate", "-1")
	q.Set("endDate", "+9")
	q.Set("cuthour", "true")
	q.Set("wrapodata", "false")
	q.Set("distanceinkm", "0")
	q.Set("regionDataCount", "20")
	q.Set("orderby", "distance")
	if conf.TempUnit == "fahrenheit" {
		q.Set("units", "F")
	}
	return conf.APIBase + "/overview?" + q.Encode()
}

// fetchOverviewWithKey 用**指定的 key** 取一次数据（供 401 自动探测后重试用）。
func fetchOverviewWithKey(ctx *plugin.Context, lat, lon float64, key string) (*weatherPayload, error) {
	req := plugin.HTTPRequest{Method: "GET", URL: overviewURL(lat, lon, key), TimeoutMS: conf.TimeoutMS}
	resp, err := ctx.HTTPRequest(req)
	if err != nil {
		return nil, classifyTransportError(err)
	}
	body, err := resp.Body()
	if err != nil {
		return nil, fmt.Errorf("响应体解码失败：%w", err)
	}
	if resp.Status < 200 || resp.Status >= 300 {
		return nil, fmt.Errorf("上游返回 %d", resp.Status)
	}
	var wrap struct {
		Responses []struct {
			Weather []weatherPayload `json:"weather"`
		} `json:"responses"`
	}
	if err := json.Unmarshal(body, &wrap); err != nil {
		return nil, fmt.Errorf("上游响应不是预期的 JSON 形状：%w", err)
	}
	if len(wrap.Responses) == 0 || len(wrap.Responses[0].Weather) == 0 {
		return nil, fmt.Errorf("上游返回了空 weather")
	}
	return &wrap.Responses[0].Weather[0], nil
}

// fetchOverview 打一次上游并解出天气对象。
//
// 响应形状：{"responses":[{"weather":[{...}]}]} —— 外面两层数组是 OData 的包装，
// 我们只要第一个 weather 对象。
func fetchOverview(ctx *plugin.Context, lat, lon float64) (*weatherPayload, error) {
	key, _, err := apiKey(ctx)
	if err != nil {
		return nil, err
	}
	rawURL := overviewURL(lat, lon, key)
	req := plugin.HTTPRequest{Method: "GET", URL: rawURL, TimeoutMS: conf.TimeoutMS}
	resp, err := ctx.HTTPRequest(req)
	if err != nil {
		return nil, classifyTransportError(err)
	}
	body, err := resp.Body()
	if err != nil {
		return nil, fmt.Errorf("响应体解码失败：%w", err)
	}
	if resp.Status == 401 || resp.Status == 403 {
		// 401 的处理顺序（2026-10-30）：
		//  1. 先自动探测：去页面里扫候选 key 并逐个试（微软把新 key 写进 HTML 时能自愈）
		//  2. 探测失败 ⇒ 如实报错，并给出可操作指引
		//
		// 探测结果**只在本次生效**（不写回配置）：插件的密钥写入要走密钥库，
		// 而 wasm 沙箱里的 ctx 没有写密钥的能力 —— 这也是"自动更新"的硬边界。
		if k, ok := tryKeysOnOverview(ctx, lat, lon, probeKeys(ctx)); ok {
			if retry, rerr := fetchOverviewWithKey(ctx, lat, lon, k); rerr == nil {
				ctx.Log(1, "MSN 密钥失效，已自动探测到可用新 key 并完成本次取数（未写回配置）")
				return retry, nil
			}
		}
		return nil, fmt.Errorf("上游拒绝鉴权（HTTP %d）：MSN 天气接口的密钥已失效。"+
			"已尝试自动从页面探测新 key，但探测到的候选均无效（MSN 的 key 只在浏览器运行时注入，"+
			"静态页面里没有）。请手动更新：打开 https://www.msn.cn/zh-cn/weather ，"+
			"在开发者工具的 Network 里找 weather/overview 请求，把其中的 apikey 值复制到本插件配置页的 api_key", resp.Status)
	}
	if resp.Status < 200 || resp.Status >= 300 {
		return nil, fmt.Errorf("上游返回 %d：%s", resp.Status, snippet(body))
	}

	var wrap struct {
		Responses []struct {
			Weather []weatherPayload `json:"weather"`
		} `json:"responses"`
	}
	if err := json.Unmarshal(body, &wrap); err != nil {
		return nil, fmt.Errorf("上游响应不是预期的 JSON 形状（MSN 可能在改版）：%w", err)
	}
	if len(wrap.Responses) == 0 || len(wrap.Responses[0].Weather) == 0 {
		return nil, fmt.Errorf("上游返回了空 responses/weather：这次请求没有拿到任何天气数据")
	}
	w := &wrap.Responses[0].Weather[0]
	return w, nil
}

// —— 上游数据结构（只声明我们真的用到的字段）——

// weatherPayload 是一次取数的天气对象。
//
// 字段名与 MSN 的真实键一一对应（大写首字母除外）。**没声明的字段不会解析**，
// 这是刻意的：MSN 的响应有 147KB，其中大量是前端渲染用的 urlIcon、symbol、wazes 之类，
// 我们不需要，也就不会因为它们的形状变化而失败。
type weatherPayload struct {
	Alerts    []alertItem `json:"alerts"`
	Current   currentWx   `json:"current"`
	Forecast  struct {
		Days []forecastDay `json:"days"`
	} `json:"forecast"`
	Nowcasting *nowcastBlock `json:"nowcasting"`
}

type alertItem struct {
	Title    string `json:"title"`
	Severity string `json:"severity"`
	Detail   string `json:"detail"`
	Valid    string `json:"valid"`
}

// currentWx 是当前实况。MSN 这里给的字段比任何一家都全，所以尽量都接住。
type currentWx struct {
	Cap      string  `json:"cap"`      // 天气文案（中文："晴朗"）
	Temp     *float64 `json:"temp"`     // 温度
	Feels    *float64 `json:"feels"`    // 体感
	RH       *float64 `json:"rh"`       // 相对湿度 %
	DewPt    *float64 `json:"dewPt"`    // 露点
	Baro     *float64 `json:"baro"`     // 气压
	Vis      *float64 `json:"vis"`      // 能见度 km
	UV       *float64 `json:"uv"`       // 紫外线指数
	UVDesc   string  `json:"uvDesc"`    // 紫外线描述（中文："中"）
	Cloud    *float64 `json:"cloudCover"` // 云量 %
	Icon     int     `json:"icon"`
	WindDir  *float64 `json:"windDir"`  // 风向角度
	WindSpd  *float64 `json:"windSpd"`  // 风速
	WindGust *float64 `json:"windGust"` // 阵风
	PvdrWindDir string `json:"pvdrWindDir"` // 中文风向（"西风"）
	PvdrWindSpd string `json:"pvdrWindSpd"` // 中文风力（"2级"）
	Aqi      *float64 `json:"aqi"`
	AqiSev   string  `json:"aqiSeverity"` // "空气质量中等"
	AqLevel  *int    `json:"aqLevel"`
	Primary  string  `json:"primaryPollutant"` // "PM2.5 23 μg/m³"
	Created  string  `json:"created"`
	Daytime  string  `json:"daytime"` // d / n
}

// forecastDay 是逐日预报的一条。
//
// ⚠ 结构上的坑（2026-10-30 实测踩到）：MSN 的一天是
// `{hourly: [...], daily: {...}, almanac: {...}}` —— 温度/天气/风等字段全在
// **`daily` 子对象里**，不在这一层。第一版把 tempHi 等平铺在 forecastDay 上，
// 结果全部解析成 nil，10 天预报整块空白。
//
// 所以这里必须两层：外层拿 hourly，内层 daily 拿当日实况。
type forecastDay struct {
	// Hourly 挂在外层（与 daily 平级）
	Hourly []hourItem `json:"hourly"`
	// Daily 是当日汇总，温度/文案/风/湿度/UV 都在这儿
	Daily dayDetail `json:"daily"`
}

// dayDetail 是 `forecast.days[].daily` 的内容。
type dayDetail struct {
	Valid      string   `json:"valid"`
	TempHi     *float64 `json:"tempHi"`
	TempLo     *float64 `json:"tempLo"`
	FeelsHi    *float64 `json:"feelsHi"`
	FeelsLo    *float64 `json:"feelsLo"`
	Icon       int      `json:"icon"`
	PvdrCap    string   `json:"pvdrCap"`
	WindMax    *float64 `json:"windMax"`
	Precip     *float64 `json:"precip"`
	RainAmount *float64 `json:"rainAmount"`
	SnowAmount *float64 `json:"snowAmount"`
	RHHi       *float64 `json:"rhHi"`
	RHLo       *float64 `json:"rhLo"`
	UV         *float64 `json:"uv"`
	UVDesc     string   `json:"uvDesc"`
	AQI        *float64 `json:"aqi"`
	Day        struct {
		Cap     string `json:"cap"`
		Summary string `json:"summary"`
		Icon    int    `json:"icon"`
	} `json:"day"`
	Night struct {
		Cap     string `json:"cap"`
		Summary string `json:"summary"`
		Icon    int    `json:"icon"`
	} `json:"night"`
}

// hourItem 是逐时预报的一条。
type hourItem struct {
	Valid   string   `json:"valid"`
	Temp    *float64 `json:"temp"`
	Feels   *float64 `json:"feels"`
	RH      *float64 `json:"rh"`
	Precip  *float64 `json:"precip"`
	Cap     string   `json:"cap"`
	Icon    int      `json:"icon"`
	WindSpd *float64 `json:"windSpd"`
	UV      *float64 `json:"uv"`
	AQI     *float64 `json:"aqi"`
	Summary string   `json:"summary"`
}

// nowcastBlock 是分钟级降水临近预报。
type nowcastBlock struct {
	Precipitation []float64 `json:"precipitation"`
	Summary       string    `json:"summary"`
	ShortSummary  string    `json:"shortSummary"`
	MinutesToTransit *int   `json:"minutesToTransit"`
	TemplateType  string    `json:"templateType"`
}

// classifyTransportError 把平台的出站拒绝翻译成运维能照做的错误。
func classifyTransportError(err error) error {
	if plugin.CodeIs(err, plugin.CodePermissionDenied) {
		// 闸门拒绝是**运维决策**，不是网络故障：报错要指向"去放行主机"这个动作。
		return fmt.Errorf("平台出站闸门拒绝：assets.msn.cn 不在 allowed_hosts 白名单里。"+
			"这是运维配置，插件不重试；请在平台插件的出站白名单里放行该主机（原始错误：%v）", err)
	}
	if plugin.CodeIs(err, plugin.CodeTimeout) {
		return fmt.Errorf("请求超时（timeout_ms=%d）：域名可能可达但响应太慢", conf.TimeoutMS)
	}
	low := strings.ToLower(err.Error())
	if strings.Contains(low, "deadline exceeded") || strings.Contains(low, "timeout") {
		return fmt.Errorf("请求超时（timeout_ms=%d）：%s", conf.TimeoutMS, err.Error())
	}
	return fmt.Errorf("上游不可达（域名解析失败 / 连接被拒 / TLS 失败）：%s", err.Error())
}

func snippet(body []byte) string {
	s := strings.TrimSpace(string(body))
	if len(s) > 200 {
		return s[:200] + "…"
	}
	return s
}
