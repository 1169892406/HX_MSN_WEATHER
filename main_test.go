// 单测：只测**纯函数**（URL 编排、时间/数值的降级处理、配置兜底）。
//
// 出站路径（ctx.HTTPRequest）依赖 Host 的 host.call —— 非 wasm 构建里 hostCall 是 nil，
// 调了就 panic，所以真实取数由平台侧集成验证，这里不伪装 Host。
package main

import (
	"encoding/json"
	"net/url"
	"testing"
)

// TestOverviewURLContainsCriticalParams 钉住那几个"少一个就 401"的参数。
//
// 这条测试存在的理由（实测 2026-10-30）：`ocid=msnweather` 会稳定 401，
// 必须 `msftweather`；`appId` 缺了也 401；而两者的 401 报错**完全一样**
// （"App authentication info not found in the security db"）。
// 也就是说 401 无法告诉你是哪个参数错了 —— 只能在编排层把它钉死。
func TestOverviewURLContainsCriticalParams(t *testing.T) {
	conf = defaults
	raw := overviewURL(32.0617, 118.7778, "test-key")
	u, err := url.Parse(raw)
	if err != nil {
		t.Fatalf("URL 解析失败：%v", err)
	}
	q := u.Query()

	checks := map[string]string{
		"apikey": "test-key",
		"ocid":   defaultOCID,   // 必须是 msftweather
		"appId":  defaultAppID,  // 必须与 key 配套
		"lat":    "32.0617",
		"lon":    "118.7778",
		"days":   "10",
	}
	for key, want := range checks {
		if got := q.Get(key); got != want {
			t.Errorf("参数 %s = %q，期望 %q", key, got, want)
		}
	}
	if u.Path != "/service/weather/overview" {
		t.Errorf("路径 = %q，期望 /service/weather/overview", u.Path)
	}
}

// TestOverviewURLUnitsFahrenheit 温度单位要真的传到上游，而不是只在本地换算。
func TestOverviewURLUnitsFahrenheit(t *testing.T) {
	conf = defaults
	conf.TempUnit = "fahrenheit"
	q, _ := url.Parse(overviewURL(0, 0, "k"))
	if got := q.Query().Get("units"); got != "F" {
		t.Errorf("华氏度时 units = %q，期望 F", got)
	}

	conf.TempUnit = "celsius"
	q2, _ := url.Parse(overviewURL(0, 0, "k"))
	if got := q2.Query().Get("units"); got == "F" {
		t.Error("摄氏度时不该传 units=F")
	}
}

// TestPointResolution 坐标解析的三条分支，尤其是"只给一半必须报错"。
//
// 为什么这条重要：只给 latitude 而拿配置的 longitude 配上去，
// 会得到一个**两个真实位置拼出来的虚构点** —— 数据看着正常，位置是错的。
func TestPointResolution(t *testing.T) {
	conf = defaults
	lat, lon := 31.5, 120.5

	both := pointArgs{Latitude: &lat, Longitude: &lon}
	gotLat, gotLon, usedDefault, err := both.point()
	if err != nil || gotLat != lat || gotLon != lon || usedDefault {
		t.Errorf("成对给坐标：得到 (%v,%v) usedDefault=%v err=%v", gotLat, gotLon, usedDefault, err)
	}

	onlyLat := pointArgs{Latitude: &lat}
	if _, _, _, err := onlyLat.point(); err == nil {
		t.Error("只给 latitude 必须报错（否则会用配置的经度拼出一个虚构位置）")
	}

	none := pointArgs{}
	gLat, gLon, gUsed, err := none.point()
	if err != nil {
		t.Fatalf("用配置默认坐标时应成功：%v", err)
	}
	if gLat != defaultLat || gLon != defaultLon || !gUsed {
		t.Errorf("默认坐标：得到 (%v,%v) usedDefault=%v", gLat, gLon, gUsed)
	}
}

// TestConfigureDefaultsAndClamps 配置兜底：缺项回落默认、天数夹到 1-10、单位非法值被忽略。
func TestConfigureDefaultsAndClamps(t *testing.T) {
	t.Run("空配置全用默认", func(t *testing.T) {
		conf = config{}
		if err := configure(nil, nil); err != nil {
			t.Fatalf("configure(nil) 失败：%v", err)
		}
		if conf.OCID != defaultOCID || conf.Days != defaultDays || conf.TempUnit != "celsius" {
			t.Errorf("默认值不对：%+v", conf)
		}
	})

	t.Run("天数夹到 1-10", func(t *testing.T) {
		conf = config{}
		raw := json.RawMessage(`{"days": 30}`)
		if err := configure(nil, raw); err != nil {
			t.Fatalf("configure 失败：%v", err)
		}
		if conf.Days != 10 {
			t.Errorf("days=30 应被夹到 10，得到 %d", conf.Days)
		}
		conf = config{}
		raw = json.RawMessage(`{"days": 0}`)
		_ = configure(nil, raw)
		if conf.Days != 1 {
			t.Errorf("days=0 应被夹到 1，得到 %d", conf.Days)
		}
	})

	t.Run("非法温度单位被忽略（保持默认）", func(t *testing.T) {
		conf = config{}
		raw := json.RawMessage(`{"temperature_unit": "kelvin"}`)
		_ = configure(nil, raw)
		if conf.TempUnit != "celsius" {
			t.Errorf("非法单位应保持 celsius，得到 %q", conf.TempUnit)
		}
	})

	t.Run("坐标越界报错", func(t *testing.T) {
		conf = config{}
		raw := json.RawMessage(`{"default_latitude": 200}`)
		if err := configure(nil, raw); err == nil {
			t.Error("纬度 200 应报错")
		}
	})
}

// TestNumOrNullKeepsNull 缺值必须是 null，不能变 0。
//
// 这条是全仓同一条纪律：MSN 夜间不给 uv、某些城市不给 aqi，
// 填 0 会变成"紫外线 0 = 完全没有""AQI 0 = 空气极好"两条假数据。
func TestNumOrNullKeepsNull(t *testing.T) {
	if got := numOrNull(nil); got != nil {
		t.Errorf("nil 应保持 null，得到 %v", got)
	}
	v := 0.0
	if got := numOrNull(&v); got != 0.0 {
		t.Errorf("真实的 0 必须保留为 0，得到 %v", got)
	}
	if got := textOrNull("  "); got != nil {
		t.Errorf("空白串应出 null，得到 %v", got)
	}
	if got := textOrNull("晴朗"); got != "晴朗" {
		t.Errorf("有值应原样返回，得到 %v", got)
	}
}

// TestISOAndTimeParsing 时间解析：拿不准就降级，不 panic。
func TestISOAndTimeParsing(t *testing.T) {
	if got := isoDate("2026-10-30T00:00:00+08:00"); got != "2026-10-30" {
		t.Errorf("isoDate 得到 %q", got)
	}
	if got := isoDate("2026-10-30"); got != "2026-10-30" {
		t.Errorf("纯日期应原样返回前缀，得到 %q", got)
	}
	if got := isoDate(""); got != "" {
		t.Errorf("空串应返回空串，得到 %q", got)
	}
	if _, ok := parseISOTime("坏时间"); ok {
		t.Error("非法时间应返回 ok=false")
	}
}

// TestPlaceDisplay 区级地名拼接：省+市+区，且逐级降级、去重。
//
// 这条钉的是「精确到区」的显示口径（2026-10-30 用户要求）：
//   - 三级齐全 ⇒「江苏省南京市溧水区」
//   - 缺市 ⇒「江苏省溧水区」（不能出现「江苏省undefined」这种）
//   - 直辖市 im 省市同名 ⇒ 去重成「北京市朝阳区」
func TestPlaceDisplay(t *testing.T) {
	cases := []struct {
		name string
		in   placeInfo
		want string
	}{
		{"三级齐全", placeInfo{Province: "江苏省", City: "南京市", Locality: "溧水区"}, "江苏省南京市溧水区"},
		{"缺市", placeInfo{Province: "江苏省", Locality: "溧水区"}, "江苏省溧水区"},
		{"直辖市去重", placeInfo{Province: "北京市", City: "北京市", Locality: "朝阳区"}, "北京市朝阳区"},
		{"只有区", placeInfo{Locality: "溧水区"}, "溧水区"},
		{"全空", placeInfo{}, ""},
		{"带空格", placeInfo{Province: " 江苏省 ", City: " 南京市 ", Locality: " 溧水区 "}, "江苏省南京市溧水区"},
	}
	for _, c := range cases {
		if got := c.in.Display(); got != c.want {
			t.Errorf("%s：Display() = %q，期望 %q", c.name, got, c.want)
		}
	}
}

// TestApikeyPatternExtractsFirstSegment 密钥提取只取第一段（与页面自身的截断逻辑一致）。
func TestApikeyPatternExtractsFirstSegment(t *testing.T) {
	html := `apikey:"f8857dedc6f54ca8962cfb713e01e7d7-e9250191-fe0b-446f-95ae-07516262f98c-7028"`
	m := apikeyPattern.FindStringSubmatch(html)
	if m == nil {
		t.Fatal("应能匹配到密钥形态")
	}
	if m[1] != "f8857dedc6f54ca8962cfb713e01e7d7" {
		t.Errorf("应只取第一段，实际 %q", m[1])
	}
}
