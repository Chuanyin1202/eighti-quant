# Strategy: grid

**網格交易策略 —— 在預設價格區間用等距網格做高拋低吸。**

中等複雜度：單引擎、無市場狀態判斷、有 5 個染色體欄位、支援 GA 進化。用來證明 framework 的「**簡單可進化策略**」用例。

---

## 1. Manifest

```go
strategy.Manifest{
    ID:                  "grid",
    Name:                "網格交易",
    Version:             "1.0.0",
    IsSpot:              true,
    SupportedSymbols:    nil,
    RequiredDataKind:    "close-only",
    MinWarmupBars:       50,    // 用於初始化網格中心線
    AggregationInterval: "1h",
    SupportsEvolution:   true,
}
```

---

## 2. 核心邏輯

### 2.1 網格定義

```
價格區間   = [LowerPrice, UpperPrice]
網格數量   = N
等距網格   = LowerPrice + i × (UpperPrice - LowerPrice) / N , i = 0..N

每格訂單金額 = OrderUSDT
```

### 2.2 觸發規則

```
ClosePrev = closes[len-2]
ClosePrev = closes[len-1]   // 即 in.LivePrice 對應的 bar close

對每條網格線 G:
  - ClosePrev > G AND ClosePrev <= G  → 下穿觸發 BUY
  - ClosePrev < G AND ClosePrev >= G  → 上穿觸發 SELL
  - 其他情況 → 不觸發
```

每根 bar 最多觸發一次（同 bar 不重複，同 §Step 第 6 步幂等檢查）。

### 2.3 區間外行為

`Close < LowerPrice` 或 `Close > UpperPrice`：
- 不觸發新單
- 寫 `Diagnostics["out_of_range"] = 1`
- 用戶看 dashboard 知道區間需要重新調整

---

## 3. Params（5 個染色體欄位）

| 欄位 | 邊界 | 預設 | 步長 | 語義 |
|------|------|------|------|------|
| `LowerPriceRel` | [0.5, 0.95] | 0.7 | 0.05 | 區間下界 = `EMA(closes, 200) × LowerPriceRel` |
| `UpperPriceRel` | [1.05, 1.5] | 1.3 | 0.05 | 區間上界 = `EMA(closes, 200) × UpperPriceRel` |
| `GridCount` | [5, 30] | 10 | 1 (整數) | 網格數量 |
| `OrderUSDTRatio` | [0.005, 0.05] | 0.02 | 0.005 | 每格訂單金額 = `TotalEquity × Ratio` |
| `RebalanceCooldownBars` | [1, 24] | 6 | 1 (整數) | 同網格線觸發後冷卻 N 根 bar |

**結構約束（Clamp 必修復）：**
- `UpperPriceRel > LowerPriceRel + 0.1`（避免區間過窄）
- `GridCount × OrderUSDTRatio × TotalEquity ≥ N × MinOrderUSDT`（保證每格能下單）

**為什麼用 EMA200 相對倍率而非絕對價格：**
- 跨標的中立（鐵律 5）：BTC 跟 ETH 絕對價差 30 倍，但相對 EMA 倍率類似
- BTC 從 60k 漲到 80k，EMA200 跟著漲，網格區間自動 follow

---

## 4. RuntimeState

```go
type RuntimeState struct {
    LastTriggerBarTime map[int]int64  // gridIndex → 上次觸發的 bar time（冷卻用）
    GridFillsCount     map[int]int    // gridIndex → 累積觸發次數（diagnostics）
}
```

---

## 5. Step() 邏輯（精簡）

```go
func (s *Strategy) Step(in strategy.StrategyInput, p strategy.Params) strategy.StrategyOutput {
    params := p.(Params)
    runtime := decodeRuntime(in.Runtime)
    out := strategy.StrategyOutput{NewRuntime: runtime, Diagnostics: map[string]float64{}}

    if len(in.Closes) < 200 {
        return out  // warm-up 不足
    }

    // 1. 計算網格
    ema200 := quant.EMA(in.Closes, 200)
    lower := ema200 * params.LowerPriceRel
    upper := ema200 * params.UpperPriceRel
    step := (upper - lower) / float64(params.GridCount)

    closeNow := in.Closes[len(in.Closes)-1]
    closePrev := in.Closes[len(in.Closes)-2]

    // 2. 區間外不操作
    if closeNow < lower || closeNow > upper {
        out.Diagnostics["out_of_range"] = 1
        return out
    }

    // 3. 每網格訂單金額
    totalEquity := in.Portfolio.USDTBalance + (in.Portfolio.DeadAsset+in.Portfolio.FloatAsset+in.Portfolio.ColdSealedAsset)*in.LivePrice
    orderUSDT := totalEquity * params.OrderUSDTRatio
    if orderUSDT < in.MinOrderUSD {
        out.Diagnostics["order_below_min"] = 1
        return out
    }

    // 4. 偵測穿越
    for i := 0; i <= params.GridCount; i++ {
        gridLine := lower + float64(i)*step

        // 冷卻
        if runtime.LastTriggerBarTime[i] != 0 {
            cooldownMs := int64(params.RebalanceCooldownBars) * intervalMs
            if in.LatestBarTimeMs - runtime.LastTriggerBarTime[i] < cooldownMs {
                continue
            }
        }

        if closePrev > gridLine && closeNow <= gridLine {
            // 下穿：BUY
            out.Intents = append(out.Intents, strategy.TradeIntent{
                Action: "BUY", Engine: "GRID", LotType: "FLOAT", AmountUSDT: orderUSDT,
            })
            runtime.LastTriggerBarTime[i] = in.LatestBarTimeMs
            runtime.GridFillsCount[i]++
        } else if closePrev < gridLine && closeNow >= gridLine {
            // 上穿：SELL
            qty := orderUSDT / closeNow
            out.Intents = append(out.Intents, strategy.TradeIntent{
                Action: "SELL", Engine: "GRID", LotType: "FLOAT", QtyAsset: qty,
            })
            runtime.LastTriggerBarTime[i] = in.LatestBarTimeMs
            runtime.GridFillsCount[i]++
        }
    }

    out.NewRuntime = runtime
    return out
}
```

---

## 6. EvolvableStrategy 接口（GA 用）

```go
// strategies/grid/evolvable.go
type Gene Params

func (s *Strategy) Sample(rng *rand.Rand) strategy.Gene {
    return Gene{
        LowerPriceRel:         0.5 + rng.Float64()*0.45,
        UpperPriceRel:         1.05 + rng.Float64()*0.45,
        GridCount:             5 + rng.Intn(26),
        OrderUSDTRatio:        0.005 + rng.Float64()*0.045,
        RebalanceCooldownBars: 1 + rng.Intn(24),
    }
    // 注意：Clamp 在外圈處理
}

func (s *Strategy) Mutate(g strategy.Gene, prob, scale float64, rng *rand.Rand) strategy.Gene {
    gene := g.(Gene)
    if rng.Float64() < prob { gene.LowerPriceRel  += rng.NormFloat64() * 0.05 * scale }
    if rng.Float64() < prob { gene.UpperPriceRel  += rng.NormFloat64() * 0.05 * scale }
    if rng.Float64() < prob { gene.GridCount      = clampInt(gene.GridCount  + int(math.Round(rng.NormFloat64()*1*scale)), 5, 30) }
    if rng.Float64() < prob { gene.OrderUSDTRatio += rng.NormFloat64() * 0.005 * scale }
    if rng.Float64() < prob { gene.RebalanceCooldownBars = clampInt(...) }
    return clamp(gene)
}

func (s *Strategy) Crossover(p1, p2 strategy.Gene, rng *rand.Rand) strategy.Gene {
    // 均勻交叉，每維度 50% 概率選父代
    g1, g2 := p1.(Gene), p2.(Gene)
    var c Gene
    if rng.Float64() < 0.5 { c.LowerPriceRel = g1.LowerPriceRel } else { c.LowerPriceRel = g2.LowerPriceRel }
    // ... 其他欄位同樣
    return clamp(c)
}

func (s *Strategy) Fingerprint(g strategy.Gene) uint64 {
    gene := g.(Gene)
    h := fnv.New64a()
    binary.Write(h, binary.LittleEndian, math.Round(gene.LowerPriceRel*1e6))
    binary.Write(h, binary.LittleEndian, math.Round(gene.UpperPriceRel*1e6))
    binary.Write(h, binary.LittleEndian, int64(gene.GridCount))
    binary.Write(h, binary.LittleEndian, math.Round(gene.OrderUSDTRatio*1e6))
    binary.Write(h, binary.LittleEndian, int64(gene.RebalanceCooldownBars))
    return h.Sum64()
}
```

`Evaluate` / `DecodeElite` / `EncodeResult` / `Verify` 跟 framework 標準範本一致。

---

## 7. 為什麼適合做 reference 範例

1. **中等複雜度** —— 比 simple-dca 複雜（有觸發規則、有冷卻、有運行時狀態），比 lunar-spot-v1 簡單（無微觀 Sigmoid、無多市場狀態、無雙引擎）
2. **染色體欄位剛好示範各種類型** —— float / int / 比率，含結構約束
3. **完全自包含** —— 不依賴 framework 提供的 `signals/` / `regimes/` building blocks，純自己實作
4. **行為直觀** —— 用戶看 dashboard 一眼能懂「這個網格區間 / 觸發次數」
5. **GA 收斂可解釋** —— GA 找出的「最適合 BTC 的網格區間」有明確意義（不像 PVA 三因子權重那麼抽象）

---

## 8. 不可能適用所有市場

**會在以下情境表現差**：
- 持續單邊上漲：價格穿出 UpperPrice 後不再觸發 BUY，賣完就空倉看著 BTC 漲
- 持續單邊下跌：價格穿出 LowerPrice 後不再觸發 SELL，買完滿倉繼續跌
- 黑天鵝事件：跳空穿越多條網格時，觸發過多訂單可能撞 MinOrder 過濾

UI 必須顯示 `Diagnostics.out_of_range` 提醒用戶調整區間。

---

## 9. 跟 simple-dca 的對比

| | simple-dca | grid |
|---|-----------|------|
| 染色體欄位 | 0 | 5 |
| GA 進化 | 不進化 | 進化 |
| 市場狀態 | 不看 | 不看 |
| 信號 | 純時間（每月 1 號） | 純價格（穿越網格線） |
| 適合行情 | 任意（純 beta） | 區間震盪 |
| Lot type | DEAD（只進不出） | FLOAT（高拋低吸） |
| 賣出 | 從不賣 | 上穿觸發 |

---

## 10. UI 文案建議

| 內部 | UI |
|------|----|
| `grid` | 網格交易 |
| `LowerPriceRel` / `UpperPriceRel` | 價格區間（相對均線） |
| `GridCount` | 網格數量 |
| `OrderUSDTRatio` | 每格金額（佔總資產比例） |
| `RebalanceCooldownBars` | 同格冷卻時間 |
| Engine `GRID` | 網格 |
| `out_of_range` | 「價格已跑出網格範圍」 |
