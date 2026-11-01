# MSN 天气 编译说明

HOMEX 平台的 WASM 插件（Go）。

## 编译

```bash
cd homex/plugins/official/weather-msn
GOOS=wasip1 GOARCH=wasm go build -buildmode=c-shared -o plugin.wasm .
```

## 打包签名

```bash
go run ./cmd/smartctl plugin package --dir plugins/official/weather-msn --out build/msn.hpkg
go run ./cmd/smartctl plugin sign build/msn.hpkg --key-id hx-weather-community
```

## 数据源

MSN 天气接口（assets.msn.cn），无需 API key：
- 自动定位（IP 归属地，三家服务轮询）
- 实时天气 / 逐小时 / 每日预报 / 空气质量
- 按坐标反查行政地名（精确到区）

## 版本

- 1.2.3：社区签名版（hx-weather-community）
