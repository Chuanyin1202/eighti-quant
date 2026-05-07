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

### 2.2 觸發規則（close-only 模式的明示限制）

策略 `RequiredDataKind = "close-only"`，意味著只用 bar close 判穿越：

```
ClosePrev = closes[len-2]   // 上一根完成 bar 的 close
CloseNow  = closes[len-1]   // 當前完成 bar 的 close

對每條網格線 G:
  - ClosePrev > G AND CloseNow <= G  → 下穿觸發 BUY
  - ClosePrev < G AND CloseNow >= G  → 上穿觸發 SELL
  - 其他情況 → 不觸發
```

**Close-only 觸發語義的 3 條限制（明示文件，不視為 bug）：**

1. **bar 內 wick 觸發不算**：bar 內 high 穿過網格線但 close 又退回，不觸發。真實 grid 用戶可能期望這算觸發 → 用戶要實精確 intrabar 觸發應改用未來的 `ohlcv` 變體（規劃中，當前 framework 不支援）。
2. **跳空多格收斂為 N 條訊號（每條獨立計算）**：bar close 一次穿越多條網格線時，每條都觸發一次。配合 §2.4 oversell 防護，總量受 FloatAsset 限制。
3. **同 bar 不重複**：cron tick 內同 bar 多次跑 Step() 會被外圈 idempotent guard 攔下（見 §00-架構總覽.md §6.2 Phase A）。

**不可避免的 fitness vs live 偏差**：GA backtest 跑歷史 closes 的觸發時點會跟 live 在同一根 bar 內某瞬間穿過、close 又退回的情境有差。此偏差是「**close-only 模式的代價**」，已寫進 Manifest 讓用戶知道。

### 2.3 區間外行為

`Close < LowerPrice` 或 `Close > UpperPrice`：
- 不觸發新單
- 寫 `Diagnostics["out_of_range"] = 1`
- 用戶看 dashboard 知道區間需要重新調整

### 2.4 Oversell 防護（多線同時穿越）

當 bar close 跳空向上穿過多條網格線時，每條線都會觸發 SELL。若不防護，累積 SELL 數量可能超過 `FloatAsset` 餘量，導致：
- 交易所 reject 部分訂單
- pending reconcile 噪音
- 帳本與實際 inventory 不一致

**演算法（在 Step 內追蹤 remaining inventory）：**

```
remainingFloatQty = in.Portfolio.FloatAsset
remainingUSDT     = in.Portfolio.USDTBalance  // 雖然主要由外圈 SpendableUSDT 管，但同 tick 多單先扣減

for i := 0..GridCount:
    if 上穿 (SELL):
        wantQty   = orderUSDT / closeNow
        sellQty   = min(wantQty, remainingFloatQty)
        if sellQty < LotMinQty:
            continue  // 沒貨可賣，跳過
        out.Intents = append(out.Intents, SELL sellQty)
        remainingFloatQty -= sellQty

    elif 下穿 (BUY):
        if orderUSDT > remainingUSDT:
            continue  // USDT 不足
        if orderUSDT < MinOrderUSDT:
            continue  // 過小
        out.Intents = append(out.Intents, BUY orderUSDT)
        remainingUSDT -= orderUSDT
```

**鐵律：grid 不使用 hard_release**（不從 DEAD lot 借貨）。FloatAsset 不夠就少賣，不去動 DEAD。這跟 lunar-spot-v1 設計選擇不同。

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

    // 4. 偵測穿越（含 oversell 防護）
    remainingFloatQty := in.Portfolio.FloatAsset
    remainingUSDT := in.Portfolio.USDTBalance

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
            if orderUSDT > remainingUSDT { continue }
            if orderUSDT < in.MinOrderUSD { continue }
            out.Intents = append(out.Intents, strategy.TradeIntent{
                Action: "BUY", Engine: "GRID", LotType: "FLOAT", AmountUSDT: orderUSDT,
            })
            remainingUSDT -= orderUSDT
            runtime.LastTriggerBarTime[i] = in.LatestBarTimeMs
            runtime.GridFillsCount[i]++

        } else if closePrev < gridLine && closeNow >= gridLine {
            // 上穿：SELL（受 remainingFloatQty 上限）
            wantQty := orderUSDT / closeNow
            sellQty := wantQty
            if sellQty > remainingFloatQty { sellQty = remainingFloatQty }
            if sellQty < in.LotMinQty { continue }
            out.Intents = append(out.Intents, strategy.TradeIntent{
                Action: "SELL", Engine: "GRID", LotType: "FLOAT", QtyAsset: sellQty,
            })
            remainingFloatQty -= sellQty
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
