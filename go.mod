// HOMEX 官方插件 Weather MSN —— 独立 module（只在 GOOS=wasip1 下有意义）。
// 与 weather（Open-Meteo / 高德）并列的**第三个天气数据源**：
// MSN 的数据面更全（气压/露点/UV/能见度/体感/空气质量等级/10 天预报/降水临近），
// 但它的密钥是网页前端抓来的、不是官方发的，所以**独立成插件**而不是塞进 weather ——
// 它失效时只掉这一个源，原插件不受影响。
module homex.dev/homex/plugins/official/weather-msn

go 1.24

require homex.dev/homex/sdk/go v0.0.0

replace homex.dev/homex/sdk/go => ../../../sdk/go
