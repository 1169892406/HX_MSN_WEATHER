package main

import (
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"homex.dev/homex/sdk/go/plugin"
)

// numOrNull 把可能缺失的上游数值转成 JSON 里"有值或 null"。
//
// 为什么必须保持 null：MSN 某些时刻不给 aqi/uv（例如夜间没有紫外线），
// 把它填成 0 会变成"紫外线 0 = 完全没有""AQI 0 = 空气极好"两条假数据。
func numOrNull(v *float64) any {
	if v == nil {
		return nil
	}
	return *v
}

// textOrNull 空串一律出 null（不把"上游没给"伪装成空字符串）。
func textOrNull(s string) any {
	if strings.TrimSpace(s) == "" {
		return nil
	}
	return strings.TrimSpace(s)
}

// snapshot 返回仪表盘卡片要的那份快照（形状与 weather 插件的 snapshot 兼容）。
func snapshot(ctx *plugin.Context, args json.RawMessage) (json.RawMessage, error) {
	var a pointArgs
	if len(args) > 0 && string(args) != "null" {
		if err := json.Unmarshal(args, &a); err != nil {
			return nil, fmt.Errorf("入参解析失败：%w", err)
		}
	}
	if !conf.Enabled {
		return json.Marshal(map[string]any{"enabled": false, "provider": "msn"})
	}
	lat, lon, pointSource, err := resolvePointWithLocate(ctx, a)
	if err != nil {
		return nil, err
	}
	w, err := fetchOverview(ctx, lat, lon)
	if err != nil {
		return nil, err
	}
	cur := w.Current

	// 按坐标反查行政地名（2026-10-30 用户要求「定位自动精确到区」）。
	//
	// MSN 的天气接口**不返回任何地名**，只有经纬度；网页上那句
	// 「江苏省南京市溧水区」是页面另调一次 locations/search 换来的。
	// 这里照做 —— 否则卡片只能显示配置里那个粗糙的「南京市」。
	//
	// 失败**不降级整份快照**：地名查不到时用配置的城市名，天气照常显示。
	// 理由：用户要的是天气，地名只是标注；为一条标注让整张卡变错误态是错的。
	cityName := conf.CityName
	place := map[string]any(nil)
	if pi, gerr := reverseGeocode(ctx, lat, lon); gerr == nil {
		if d := pi.Display(); d != "" {
			cityName = d
		}
		place = map[string]any{
			"full":        pi.Full,
			"locality":    pi.Locality,
			"city":        pi.City,
			"province":    pi.Province,
			"postal_code": pi.PostalCode,
		}
	}

	out := map[string]any{
		"enabled":  true,
		"provider": "msn",
		"city":     cityName,
		"latitude": lat,
		"longitude": lon,
		// used_default_point 让调用方知道"这是配置里的默认位置，不是调用方指定的"
		"point_source":          pointSource,
		"weather":            textOrNull(cur.Cap),
		"temperature":        numOrNull(cur.Temp),
		"feels_like":         numOrNull(cur.Feels),
		"humidity":           numOrNull(cur.RH),
		"dew_point":          numOrNull(cur.DewPt),
		"pressure":           numOrNull(cur.Baro),
		"visibility":         numOrNull(cur.Vis),
		"uv_index":           numOrNull(cur.UV),
		"uv_desc":            textOrNull(cur.UVDesc),
		"cloud_cover":        numOrNull(cur.Cloud),
		"wind_speed":         numOrNull(cur.WindSpd),
		"wind_dir_deg":       numOrNull(cur.WindDir),
		"wind_gust":          numOrNull(cur.WindGust),
		"wind_dir_text":      textOrNull(cur.PvdrWindDir),
		"wind_power_text":    textOrNull(cur.PvdrWindSpd),
		// ⚠ icon 必须是**字符串**：weather 插件的契约是 emoji 文本，
		// 而 MSN 给的是数字代码（1=晴、29=晴夜…）。输出数字会让前端 schema 校验
		// 直接判整份响应不合格（实测 "icon: Expected string, received number"）。
		// 这里同时给两个字段：
		//   icon      —— 与 weather 同契约的 emoji（前端读它）
		//   icon_code —— MSN 原始数字（排查/将来接它自己的图标集时用）
		"icon":      msnIconEmoji(cur.Icon),
		"icon_code": cur.Icon,
		"daytime":            textOrNull(cur.Daytime),
		"report_time":        textOrNull(cur.Created),
		"updated_at":         time.Now().UTC().Format(time.RFC3339),
		"refresh_minutes":    conf.RefreshMin,
		"unit":               conf.TempUnit,
	}
	if place != nil {
		out["place"] = place
	}

	// 空气质量：配置关掉时**不出现这个键**（而不是给 null）——
	// 界面上"没有这个字段"与"查了但没有值"是两件事。
	if conf.ShowAir {
		out["air_quality"] = map[string]any{
			"aqi":               numOrNull(cur.Aqi),
			"severity":          textOrNull(cur.AqiSev),
			"level":             numOrNullInt(cur.AqLevel),
			"primary_pollutant": textOrNull(cur.Primary),
		}
	}

	// 今天的高温/低温（卡片主区那一行）。取 forecast.days[0]。
	if len(w.Forecast.Days) > 0 {
		d0 := w.Forecast.Days[0]
		out["today"] = map[string]any{
			// 兼容名：卡片与 forecast 都用 temperature_max/min，这里保持一致
			"temperature_max": numOrNull(d0.Daily.TempHi),
			"temperature_min": numOrNull(d0.Daily.TempLo),
			"temp_hi":     numOrNull(d0.Daily.TempHi),
			"temp_lo":     numOrNull(d0.Daily.TempLo),
			"feels_hi":    numOrNull(d0.Daily.FeelsHi),
			"feels_lo":    numOrNull(d0.Daily.FeelsLo),
			"day_cap":     textOrNull(d0.Daily.Day.Cap),
			"night_cap":   textOrNull(d0.Daily.Night.Cap),
			"summary":     textOrNull(d0.Daily.Day.Summary),
			"precip":      numOrNull(d0.Daily.Precip),
			"rain_amount": numOrNull(d0.Daily.RainAmount),
			"snow_amount": numOrNull(d0.Daily.SnowAmount),
			"wind_max":    numOrNull(d0.Daily.WindMax),
			"rh_hi":       numOrNull(d0.Daily.RHHi),
			"rh_lo":       numOrNull(d0.Daily.RHLo),
			"uv":          numOrNull(d0.Daily.UV),
			"uv_desc":     textOrNull(d0.Daily.UVDesc),
			"aqi":         numOrNull(d0.Daily.AQI),
		}
	}

	// 预警（MSN 的 alerts 常常是空数组，不是错误）
	if len(w.Alerts) > 0 {
		out["alerts"] = w.Alerts
	}
	return json.Marshal(out)
}

// forecast 返回逐日预报（默认 10 天，由配置 days 决定）。
func forecast(ctx *plugin.Context, args json.RawMessage) (json.RawMessage, error) {
	var a pointArgs
	if len(args) > 0 && string(args) != "null" {
		if err := json.Unmarshal(args, &a); err != nil {
			return nil, fmt.Errorf("入参解析失败：%w", err)
		}
	}
	lat, lon, pointSource, err := resolvePointWithLocate(ctx, a)
	if err != nil {
		return nil, err
	}
	w, err := fetchOverview(ctx, lat, lon)
	if err != nil {
		return nil, err
	}

	// dayOut 的字段名**必须与 weather 插件的 forecast 对齐**
	// （temperature_max / temperature_min / weather_day / weather_night）：
	// 仪表盘卡片按那一套名字读，两个插件产出不同名字会让链式切换时
	// 预报整块变空（实测 2026-10-30：切到 MSN 后 10 天全是空值）。
	// MSN 自己的命名作为补充字段保留在后面。
	type dayOut struct {
		Date        string `json:"date"`
		TempHi      any    `json:"temperature_max"` // 与 weather 同名（前端读这个）
		TempLo      any    `json:"temperature_min"` // 同上
		DayCap      any    `json:"weather_day"`     // 同上
		NightCap    any    `json:"weather_night"`   // 同上
		FeelsHi     any    `json:"feels_hi"`
		FeelsLo     any    `json:"feels_lo"`
		Icon        int    `json:"icon"`
		IconDay     int    `json:"icon_day"`
		IconNight   int    `json:"icon_night"`
		Precip      any    `json:"precip"`
		RainAmount  any    `json:"rain_amount"`
		SnowAmount  any    `json:"snow_amount"`
		WindMax     any    `json:"wind_max"`
		RHDay       any    `json:"rh_hi"`
		RHNight     any    `json:"rh_lo"`
		UV          any    `json:"uv"`
		UVDesc      any    `json:"uv_desc"`
		AQI         any    `json:"aqi"`
	}
	list := make([]dayOut, 0, len(w.Forecast.Days))
	for _, d := range w.Forecast.Days {
		list = append(list, dayOut{
			Date:       isoDate(d.Daily.Valid),
			TempHi:     numOrNull(d.Daily.TempHi),
			TempLo:     numOrNull(d.Daily.TempLo),
			FeelsHi:    numOrNull(d.Daily.FeelsHi),
			FeelsLo:    numOrNull(d.Daily.FeelsLo),
			DayCap:     textOrNull(d.Daily.Day.Cap),
			NightCap:   textOrNull(d.Daily.Night.Cap),
			Icon:       d.Daily.Icon,
			IconDay:    d.Daily.Day.Icon,
			IconNight:  d.Daily.Night.Icon,
			Precip:     numOrNull(d.Daily.Precip),
			RainAmount: numOrNull(d.Daily.RainAmount),
			SnowAmount: numOrNull(d.Daily.SnowAmount),
			WindMax:    numOrNull(d.Daily.WindMax),
			RHDay:      numOrNull(d.Daily.RHHi),
			RHNight:    numOrNull(d.Daily.RHLo),
			UV:         numOrNull(d.Daily.UV),
			UVDesc:     textOrNull(d.Daily.UVDesc),
			AQI:        numOrNull(d.Daily.AQI),
		})
	}

	return json.Marshal(map[string]any{
		"provider":           "msn",
		"city":               conf.CityName,
		"latitude":           lat,
		"longitude":          lon,
		"point_source":          pointSource,
		"unit":               conf.TempUnit,
		"days":               list,
	})
}

// hourly 返回逐时预报（把 MSN 按天分组的 hourly 摊平成一个连续序列）。
//
// 为什么要摊平：MSN 的 hourly 挂在每一天下面（days[0].hourly 只到当天结束），
// 而卡片要的是"未来 24 小时"的连续曲线。摊平后按时间排序、去掉已经过去的时刻。
func hourly(ctx *plugin.Context, args json.RawMessage) (json.RawMessage, error) {
	var a pointArgs
	if len(args) > 0 && string(args) != "null" {
		if err := json.Unmarshal(args, &a); err != nil {
			return nil, fmt.Errorf("入参解析失败：%w", err)
		}
	}
	lat, lon, pointSource, err := resolvePointWithLocate(ctx, a)
	if err != nil {
		return nil, err
	}
	w, err := fetchOverview(ctx, lat, lon)
	if err != nil {
		return nil, err
	}

	type hourOut struct {
		Time      string `json:"time"`
		Temp      any    `json:"temperature"`
		Feels     any    `json:"feels_like"`
		RH        any    `json:"humidity"`
		Precip    any    `json:"precip"`
		Cap       any    `json:"cap"`
		Icon      int    `json:"icon"`
		WindSpd   any    `json:"wind_speed"`
		UV        any    `json:"uv"`
		AQI       any    `json:"aqi"`
	}
	var list []hourOut
	now := time.Now()
	for _, d := range w.Forecast.Days {
		for _, h := range d.Hourly {
			// 已经过去的时刻不进曲线：卡片画的是"未来"。
			// 时间解析失败就照样收下（宁可多一条，也不因格式变化丢掉全部）。
			if t, ok := parseISOTime(h.Valid); ok && t.Before(now.Add(-30*time.Minute)) {
				continue
			}
			list = append(list, hourOut{
				Time:    h.Valid,
				Temp:    numOrNull(h.Temp),
				Feels:   numOrNull(h.Feels),
				RH:      numOrNull(h.RH),
				Precip:  numOrNull(h.Precip),
				Cap:     textOrNull(h.Cap),
				Icon:    h.Icon,
				WindSpd: numOrNull(h.WindSpd),
				UV:      numOrNull(h.UV),
				AQI:     numOrNull(h.AQI),
			})
			if len(list) >= 24 {
				break
			}
		}
		if len(list) >= 24 {
			break
		}
	}

	return json.Marshal(map[string]any{
		"provider":           "msn",
		"city":               conf.CityName,
		"latitude":           lat,
		"longitude":          lon,
		"point_source":          pointSource,
		"unit":               conf.TempUnit,
		"hours":              list,
	})
}

// nowcasting 返回分钟级降水临近预报（MSN 的独有能力之一）。
func nowcasting(ctx *plugin.Context, args json.RawMessage) (json.RawMessage, error) {
	var a pointArgs
	if len(args) > 0 && string(args) != "null" {
		if err := json.Unmarshal(args, &a); err != nil {
			return nil, fmt.Errorf("入参解析失败：%w", err)
		}
	}
	lat, lon, pointSource, err := resolvePointWithLocate(ctx, a)
	if err != nil {
		return nil, err
	}
	w, err := fetchOverview(ctx, lat, lon)
	if err != nil {
		return nil, err
	}
	if w.Nowcasting == nil {
		// 没有临近预报不是错误：某些地区/时刻上游本来就不给。
		return json.Marshal(map[string]any{
			"provider": "msn", "available": false,
			"note": "这次响应里没有 nowcasting 区块（上游对该位置/时刻未提供降水临近预报）",
		})
	}
	n := w.Nowcasting
	return json.Marshal(map[string]any{
		"provider":           "msn",
		"latitude":           lat,
		"longitude":          lon,
		"point_source":          pointSource,
		"available":          true,
		"summary":            textOrNull(n.Summary),
		"short_summary":      textOrNull(n.ShortSummary),
		"precipitation":      n.Precipitation,
		"minutes_to_transit": numOrNullInt(n.MinutesToTransit),
		"template_type":      textOrNull(n.TemplateType),
	})
}

// alerts 返回天气预警。
func alerts(ctx *plugin.Context, args json.RawMessage) (json.RawMessage, error) {
	var a pointArgs
	if len(args) > 0 && string(args) != "null" {
		if err := json.Unmarshal(args, &a); err != nil {
			return nil, fmt.Errorf("入参解析失败：%w", err)
		}
	}
	lat, lon, pointSource, err := resolvePointWithLocate(ctx, a)
	if err != nil {
		return nil, err
	}
	w, err := fetchOverview(ctx, lat, lon)
	if err != nil {
		return nil, err
	}
	out := map[string]any{
		"provider":           "msn",
		"latitude":           lat,
		"longitude":          lon,
		"point_source":          pointSource,
		"count":              len(w.Alerts),
		"alerts":             w.Alerts,
	}
	if len(w.Alerts) == 0 {
		out["note"] = "上游当前没有针对该位置的天气预警（空数组不是获取失败）"
	}
	return json.Marshal(out)
}

// testConnection 真打一次上游，把失败原因分类报出来。
//
// 它读的是**已保存的配置**（含密钥库里的 key）：用户改完不保存就测，
// 测到的仍是旧配置 —— 这与平台其它插件的口径一致，界面会提示"先保存"。
func testConnection(ctx *plugin.Context) (json.RawMessage, error) {
	// 与 snapshot/forecast/hourly 用**同一套坐标解析**（2026-10-30 补）。
	//
	// 原来这里直接读 conf.DefaultLat/Lon —— 配置默认已改成 0（"交给自动定位"），
	// 于是测试连接报 target=0.0000,0.0000：坐标是几内亚湾、数据是"那里"的天气，
	// 而用户看到 ok:true 会以为一切正常。测试入口必须与真实取数同一条路径，
	// 否则它测的不是用户实际会遇到的那条路。
	lat, lon, pointSource, perr := resolvePointWithLocate(ctx, pointArgs{})
	if perr != nil {
		return json.Marshal(map[string]any{
			"provider": "msn", "ok": false,
			"failure": map[string]any{"kind": "locate", "message": perr.Error()},
		})
	}
	started := time.Now()
	_, keySource, kerr := apiKey(ctx)
	if kerr != nil {
		return json.Marshal(map[string]any{
			"provider": "msn", "ok": false,
			"failure": map[string]any{"kind": "config", "message": kerr.Error()},
		})
	}
	w, err := fetchOverview(ctx, lat, lon)
	elapsed := time.Since(started).Milliseconds()
	if err != nil {
		kind := "unreachable"
		msg := err.Error()
		if strings.Contains(msg, "401") || strings.Contains(msg, "403") || strings.Contains(msg, "鉴权") {
			kind = "invalid_key"
		} else if strings.Contains(msg, "超时") {
			kind = "timeout"
		} else if strings.Contains(msg, "闸门") {
			kind = "blocked"
		} else if strings.Contains(msg, "JSON") || strings.Contains(msg, "空 responses") {
			kind = "bad_response"
		}
		return json.Marshal(map[string]any{
			"provider":   "msn",
			"ok":         false,
			"latency_ms": elapsed,
			"target":     fmt.Sprintf("%.4f,%.4f", lat, lon),
			"key_source": keySource,
			"failure":    map[string]any{"kind": kind, "message": msg},
		})
	}
	return json.Marshal(map[string]any{
		"provider":   "msn",
		"ok":         true,
		"latency_ms": elapsed,
		"target":     fmt.Sprintf("%.4f,%.4f", lat, lon),
		"point_source": pointSource,
		"key_source": keySource,
		"city":       conf.CityName,
		"sample": map[string]any{
			"weather":     textOrNull(w.Current.Cap),
			"temperature": numOrNull(w.Current.Temp),
			"humidity":    numOrNull(w.Current.RH),
			"aqi":         numOrNull(w.Current.Aqi),
			"days":        len(w.Forecast.Days),
			"report_time": textOrNull(w.Current.Created),
		},
		"unit": conf.TempUnit,
	})
}

// —— 小工具 ——

func numOrNullInt(v *int) any {
	if v == nil {
		return nil
	}
	return *v
}

// isoDate 把 "2026-10-30T00:00:00+08:00" 截成 "2026-10-30"。
// 解析失败就原样返回（宁可给个奇怪的字符串，也不要丢掉整条预报）。
func isoDate(s string) string {
	if t, ok := parseISOTime(s); ok {
		return t.Format("2006-01-02")
	}
	if len(s) >= 10 {
		return s[:10]
	}
	return s
}

func parseISOTime(s string) (time.Time, bool) {
	if strings.TrimSpace(s) == "" {
		return time.Time{}, false
	}
	t, err := time.Parse(time.RFC3339, s)
	if err != nil {
		return time.Time{}, false
	}
	return t, true
}


// msnIconEmoji 把 MSN 的天气图标代码映射成 emoji 文本。
//
// 为什么需要它：weather 插件的快照契约里 `icon` 是字符串（emoji），
// 前端按 string 校验；而 MSN 的 icon 是数字代码。不转换会让整份快照被拒。
//
// 代码含义来自 MSN 的 symbol 体系（d000 晴、d100 局部晴、d200 多云…），
// 这里只按**大类别**映射到 emoji：卡片展示的是天气类型，不是精确图标集。
// 认不出的代码回落到云 —— 不猜"晴"，否则阴天会显示太阳。
func msnIconEmoji(code int) string {
	switch code {
	// 晴 / 晴夜
	case 1, 2, 29, 30, 31, 32, 33:
		return "☀️"
	// 局部多云
	case 3, 4, 5, 6, 34, 35, 36, 37:
		return "🌤️"
	// 多云 / 阴
	case 7, 8, 9, 10, 11, 38, 39, 40, 41:
		return "☁️"
	// 雾 / 霾 / 沙尘
	case 12, 13, 14, 15, 16, 42, 43, 44, 45:
		return "🌫️"
	// 小雨 / 阵雨
	case 17, 18, 19, 20, 21, 46, 47, 48:
		return "🌦️"
	// 大雨 / 暴雨
	case 22, 23, 24, 25, 26, 49, 50, 51:
		return "🌧️"
	// 雷雨
	case 27, 28, 52, 53:
		return "⛈️"
	// 雪
	case 54, 55, 56, 57, 58, 59, 60:
		return "❄️"
	default:
		return "☁️"
	}
}
