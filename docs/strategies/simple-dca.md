# Strategy: simple-dca

**最簡 DCA baseline 策略 —— 用來驗證 framework 接口、給 demo 用戶第一個能跑的策略。**

不進化、無微觀、無市場狀態判斷。每月 1 號花固定金額買入。

---

## 1. Manifest

```go
strategy.Manifest{
    ID:                  "simple-dca",
    Name:                "簡單定投",
    Version:             "1.0.0",
    IsSpot:              true,
    SupportedSymbols:    nil, // 任意 symbol
    RequiredDataKind:    "close-only",
    MinWarmupBars:       1,
    AggregationInterval: "1d",
    SupportsEvolution:   false,  // 不進化
}
```

---

## 2. Params

```go
type Params struct {
    MonthlyAmountUSDT float64 // 每月買入金額
    BuyDayOfMonth     int     // 1..28，月內買入日
}

func (s *Strategy) DefaultParams() strategy.Params {
    return Params{
        MonthlyAmountUSDT: 100.0,
        BuyDayOfMonth:     1,
    }
}
```

不進化 → DefaultParams 即為唯一 params。用戶可在 Instance 建立時 override（透過 SpawnPoint）。

---

## 3. RuntimeState

```go
type RuntimeState struct {
    LastBuyYearMonth string // "2026-05"
}
```

只記「上次買在哪個月」做去重。

---

## 4. Step() 邏輯

```go
func (s *Strategy) Step(in strategy.StrategyInput, p strategy.Params) strategy.StrategyOutput {
    params := p.(Params)
    runtime := decodeRuntime(in.Runtime)

    bartime := time.UnixMilli(in.LatestBarTimeMs).UTC()
    yearMonth := bartime.Format("2006-01")
    isBuyDay := bartime.Day() == params.BuyDayOfMonth

    out := strategy.StrategyOutput{
        NewRuntime:  runtime,
        Diagnostics: map[string]float64{},
    }

    // 已經買過此月 → 跳過
    if runtime.LastBuyYearMonth == yearMonth {
        return out
    }

    // 不是買入日 → 跳過
    if !isBuyDay {
        return out
    }

    // SpendableUSDT 不足 → 跳過（等下個月）
    if in.Portfolio.USDTBalance < params.MonthlyAmountUSDT {
        out.Diagnostics["skip_reason_insufficient"] = 1
        return out
    }

    // 下單意圖
    out.Intents = []strategy.TradeIntent{{
        Action:     "BUY",
        Engine:     "DCA",
        LotType:    "DEAD",
        AmountUSDT: params.MonthlyAmountUSDT,
    }}

    runtime.LastBuyYearMonth = yearMonth
    out.NewRuntime = runtime
    return out
}
```

---

## 5. 沒有的東西

- 沒有 `EvolvableStrategy` 接口（`SupportsEvolution = false`）
- 沒有信號計算
- 沒有市場狀態
- 沒有 Sigmoid
- 沒有微觀引擎
- 沒有底倉釋放
- 沒有 hard release / soft release
- 沒有楔形過濾

**這就是它的價值 —— 證明 framework 不強制任何複雜性**。一個 < 50 行的策略也能完整運作。

---

## 6. 使用場景

1. **Framework 接口驗證** —— 寫完 framework 後，第一個能跑的策略
2. **Demo baseline** —— 跟更複雜策略對比的「dumb baseline」
3. **教學範例** —— 教用戶「怎麼自己寫策略」的最小範本
4. **Paper 模式入門** —— 新用戶第一個試的策略，行為可預測

---

## 7. 鐵律檢查

```bash
# 純函數
rg -n '(net/http|database/sql|gorm\.io/|os\.Open|time\.Now|math/rand)' \
   strategies/simple-dca/
# 期望：無匹配

# 標的中立
rg -n '"BTCUSDT"|"ETHUSDT"' strategies/simple-dca/
# 期望：無匹配
```

`time` 包僅允許用於：
- `time.UnixMilli(in.LatestBarTimeMs)` 解析 bar 時間（純函數，OK）
- `time.UTC()` / `time.Day()` / `time.Format()` 等純解析 API（OK）

**禁止** `time.Now()`、`time.Sleep`、`time.NewTicker` 等帶副作用的 API。

---

## 8. 預期行為（Worked Examples）

假設 `MonthlyAmountUSDT = 100`、`BuyDayOfMonth = 1`、`USDTBalance = 1000`：

| Tick (UTC) | DayOfMonth | yearMonth | LastBuyYearMonth | Action |
|-----------|-----------|-----------|-----------------|--------|
| 2026-05-01 00:00 | 1 | 2026-05 | "" | BUY 100 USDT，runtime 更新 2026-05 |
| 2026-05-01 04:00 | 1 | 2026-05 | 2026-05 | 跳過（已買過此月） |
| 2026-05-15 00:00 | 15 | 2026-05 | 2026-05 | 跳過（非買入日） |
| 2026-06-01 00:00 | 1 | 2026-06 | 2026-05 | BUY 100 USDT，runtime 更新 2026-06 |

8 行邏輯，行為完全可預測。

---

## 9. 不可能虧損？

**會虧**。`simple-dca` 跟 BTC 同進退（持有現貨）。BTC 跌 50%，這個策略也跟著跌 50%（fee 加成 -50.05%）。

它不是「保本策略」，是「**最簡定投策略**」。功能：在任何市場條件下都不會主動加碼風險，純粹反映 BTC 的 beta。

---

## 10. UI 顯示文案建議

| 內部 | UI |
|------|----|
| `simple-dca` | 簡單定投 |
| `MonthlyAmountUSDT` | 每月投入 |
| `BuyDayOfMonth` | 投入日（每月） |
| Engine `DCA` | 定投 |
| LotType `DEAD` | 長期持倉 |
