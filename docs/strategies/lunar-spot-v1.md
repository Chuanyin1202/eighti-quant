# Strategy: lunar-spot-v1

**雙引擎 + PVA + SJM 三態 + Sigmoid 動態天平 —— 影片作者展示的旗艦策略**

複雜度最高的 reference 實作。展示 framework 對「**多引擎 + 多狀態感知 + 14 染色體 GA**」用例的完整支援。

---

## 1. Manifest

```go
strategy.Manifest{
    ID:                  "lunar-spot-v1",
    Name:                "雙軌量化策略 v1",
    Version:             "1.0.0",
    IsSpot:              true,
    SupportedSymbols:    []string{"BTCUSDT", "ETHUSDT"},
    RequiredDataKind:    "close-only",
    MinWarmupBars:       600,    // EMA600 + σ 90 天滾動分位數
    AggregationInterval: "4h",
    SupportsEvolution:   true,
}
```

---

## 2. 設計哲學（影片三條鐵律）

1. **化繁為簡（剝離 K 線）**：只用 closes + 即時順勢價，無 OHLCV
2. **最悲觀原則**：默認所有環節出錯，只能用現貨 + 多頭趨勢標的（BTC/ETH）
3. **雙軌並行**：宏觀 DCA 吃長線趨勢 + 微觀 Sigmoid 吃盤整，**共用同一套資金權益**

---

## 3. 資產三態（Portfolio State）

| 程式結構 | LotType 字串 | UI | 語義 | 來源 |
|---------|-------------|-----|------|------|
| `DeadAsset` | `"DEAD"` | 長期持倉 | 宏觀底倉，原則只進不出 | 宏觀 DCA 引擎吸入 |
| `FloatAsset` | `"FLOAT"` | 活躍倉位 | 微觀浮倉，可自由買賣 | 微觀 Sigmoid 引擎管理 |
| `ColdSealedAsset` | `"COLD_SEALED"` | 封存資產 | 永不釋放 | 用戶建 Instance 時指定的封存比例 |

通用會計公式：

```
TotalAssetQty   = DeadAsset + FloatAsset + ColdSealedAsset
TotalEquity     = USDTBalance + TotalAssetQty × LivePrice
CurrentMicroWeight = (FloatAsset × LivePrice) / TotalEquity   // 分子只算 Float
```

**重要：兩個引擎共用 `TotalEquity`**（影片強調，否則數學期望直接除以 2）。

---

## 4. PVA 三純物理量

從 `Closes` 序列計算，全部無量綱化：

```
EMABase = EMA(Closes, EMABaseBars)        // 染色體：EMABaseBars
σ       = StdDev(LogReturns, SigmaWindow) // 染色體：SigmaWindow

P (Position)     = (Close - EMABase) / max(σ, SigmaFloor)
                   // 價格相對均線的偏離 z-score（Close > EMA → P > 0）

V (Velocity)     = log(Closes[-1] / Closes[-VelocityLookback]) / VelocityLookback
                   // 一階導數：價格運動動能（單位時間的對數收益）

A (Acceleration) = V_now - V_prev
                   // 二階導數：V 是否在加速 / 衰竭
```

**符號約定**：P > 0 = 過熱（看空、預期回落）。V / A 由 SignalW 自己學方向。

---

## 5. SJM 三態感知

從 PVA 壓縮成三態（不預測，只做狀態壓縮）：

| 狀態 | 判斷規則 | TimeDilation | BetaMultiplier | IsQuiet |
|------|---------|--------------|----------------|---------|
| **恐慌 (Panic)** | `A < -PanicSigmaMul × σ_A` | 1.5 | 2.5 | false |
| **貪婪 (Greed)** | `V > GreedVelocityMean` AND `A > 0` | 1.0 | 1.0 | false |
| **平靜 (Quiet)** | 其他（垃圾時間） | 1.0 | 0.5 | **true** |

`σ_A = StdDev(A, SigmaWindow)`（A 自己的滾動標準差）

修飾器（Modifiers）給雙引擎使用：
- `TimeDilation` → 宏觀 DCA 加速倍率
- `BetaMultiplier` → 微觀 Sigmoid Beta 倍率
- `IsQuiet` → true 時微觀粉塵訂單歸零

---

## 6. 微觀引擎 (Sigmoid 動態天平)

### 6.1 核心公式

```
EffectiveBeta  = max(0.01, Beta × Regime.BetaMultiplier)
InventoryBias  = clamp(CurrentMicroWeight, 0, 1) - 0.5
Signal         = SignalW_P × P + SignalW_V × V + SignalW_A × A
Exponent       = EffectiveBeta × Signal + Gamma × InventoryBias
TargetWeight   = 1 / (1 + exp(Exponent)),  clamp(0, 1)
DeltaWeight    = TargetWeight - CurrentMicroWeight
TheoreticalUSD = DeltaWeight × TotalEquity
```

**符號約定**：
- `Signal > 0` → Exponent ↑ → TargetWeight ↓ → 減倉
- `Signal < 0` → Exponent ↓ → TargetWeight ↑ → 加倉

### 6.2 Sigmoid Sanity Check（必驗）

| 場景 | 輸入 | Exponent | TargetWeight | 方向 |
|------|------|----------|--------------|------|
| 看空中性倉 | Signal=+1, β=1.5, γ=0.5, CurWeight=0.5 | +1.5 | ≈ 0.18 | DeltaWeight ≈ -0.32（賣） |
| 看多中性倉 | Signal=-1, β=1.5, γ=0.5, CurWeight=0.5 | -1.5 | ≈ 0.82 | DeltaWeight ≈ +0.32（買） |
| 高倉位無信號 | Signal=0, β=1.5, γ=0.5, CurWeight=0.8 | +0.15 | ≈ 0.46 | DeltaWeight ≈ -0.34（彈簧拉回） |

實作後必跑 `step_sigmoid_sanity_test.go`，三組都對才算實作正確。

### 6.3 楔形過濾（Wedge Filter）

```
過濾規則：
  if abs(TheoreticalUSD) >= MinOrderUSDT:
      OrderUSD = TheoreticalUSD（直接下單）
  elif 0 < abs(TheoreticalUSD) < MinOrderUSDT:
      if !Regime.IsQuiet AND
         (abs(DeltaWeight) >= WedgeDeltaThr):
          OrderUSD = sign(TheoreticalUSD) × MinOrderUSDT  // 強制最小訂單
      else:
          OrderUSD = 0
  else:
      OrderUSD = 0
```

### 6.4 微觀層硬釋放

當 `OrderUSD < 0`（SELL）但 `FloatAsset × LivePrice < |OrderUSD|`：
1. 從 `Lots` 篩 `Type=DEAD AND !IsColdSealed`，FIFO 累積到補足缺口
2. 產出 `ReleaseIntent { Kind: "hard_release", Slices: [...], RelatedClientOrderID: <SELL command 的 client_order_id> }`
3. 外圈在原子事務內執行 lot 轉換

---

## 7. 宏觀引擎 (DCA + per-tick carry)

### 7.1 RuntimeState

```go
type MacroRuntimeState struct {
    CurrentMonthYearMonth string  // "2026-05"
    MonthlyBudget         float64
    SpentThisMonth        float64
    PendingMacroBudget    float64 // per-tick 累積、跨月 carry over
    LastMacroBuyBarTime   int64
}
```

### 7.2 每 tick 邏輯（per-tick carry + 10.1 USDT 訂單門檻）

```
時間衍生量（用 LatestBarTimeMs，UTC）：
  YearMonth, DayOfMonth, DaysInMonth, BarOfDay (0..5), BarsPerDay = 6

# Step 0: 同 bar 幂等檢查（必須前置，所有 mutation 之前）
if LastMacroBuyBarTime == LatestBarTimeMs:
    return zero-mutation early return

# Step 1: 跨月處理
if YearMonth != Runtime.CurrentMonthYearMonth:
    Reset MonthlyBudget = MonthlyInjectUSDT, SpentThisMonth = 0
    Carry over PendingMacroBudget
    Update CurrentMonthYearMonth

# Step 2: 累積 per-tick 額度
RemainingDays      = DaysInMonth - (DayOfMonth - 1)
RemainingBarsToday = BarsPerDay - BarOfDay
RemainingBars      = (RemainingDays - 1) × BarsPerDay + RemainingBarsToday
RemainingBudget    = MonthlyBudget - SpentThisMonth - PendingMacroBudget

PerTickBase        = RemainingBudget / max(RemainingBars, 1)
PerTickAccelerated = PerTickBase × Regime.TimeDilation × MacroAccelerator(若 Panic)
PendingMacroBudget += PerTickAccelerated

# Step 3: 決定下單
IsDeadline = (DayOfMonth >= MacroDeadlineDays)
CanFire    = (PendingMacroBudget >= MinOrderUSDT) OR IsDeadline

if !CanFire:
    OrderUSD = 0  // 累積中

elif IsDeadline:
    AvailableThisMonth = MonthlyBudget - SpentThisMonth
    OrderUSD = min(AvailableThisMonth, SpendableUSDT)
    if OrderUSD < MinOrderUSDT:
        OrderUSD = 0  // 連最小訂單都湊不齊 → 全額 carry forward
    else:
        SpentThisMonth += OrderUSD
        unfilled = AvailableThisMonth - OrderUSD
        PendingMacroBudget = unfilled  // carry forward 差額（不 erase）

else:
    OrderUSD = min(PendingMacroBudget, RemainingBudget+PendingMacroBudget, SpendableUSDT)
    if OrderUSD < MinOrderUSDT:
        OrderUSD = 0
    else:
        PendingMacroBudget -= OrderUSD
        SpentThisMonth += OrderUSD

# Step 4: 標記 bar 處理完成
LastMacroBuyBarTime = LatestBarTimeMs
產出 MacroIntent { Action: BUY, Engine: MACRO, LotType: DEAD, AmountUSDT: OrderUSD }
```

### 7.3 鐵律
- **只產出 BUY 意圖**，絕不 SELL
- 訂單金額與 `SpendableUSDT` clamp
- 同 bar 不重複（Step 0 早 return）
- 不讀 `NowMs`，全用 `LatestBarTimeMs`

---

## 8. DeadAsset 釋放規則

### 8.1 軟釋放（Soft Release）

當 `Type=DEAD AND !IsColdSealed AND BuyTimeMs 老化 >= DeadAgingMonths × 30 天` 的 lot 存在，且 `CurrentMicroWeight < TargetMicroWeightFloor`：

按 BuyTimeMs FIFO 累積，每 lot 取 `min(lot.Qty, ReleaseQty - 累計)`（**支援 partial lot**）。產出：

```
ReleaseIntent {
    Kind: "soft_release",
    Slices: [{LotID, Qty}, ...],
    TotalQtyAsset: 累計,
    RelatedClientOrderID: ""   // soft release 不對應 SELL
}
```

### 8.2 硬釋放（Hard Release）

見 §6.4。`Kind: "hard_release"`，`RelatedClientOrderID` 必填（配對 SELL TradeIntent 的 client_order_id）。

### 8.3 鐵律
- `IsColdSealed=true` 任何情況不釋放
- 釋放只改 SaaS 帳本（lot type 標籤），**不**下發 Agent
- 寫 audit log

---

## 9. 染色體（14 個欄位，全部進化）

### 9.1 欄位清單

| # | 欄位 | 邊界 | 預設 | 步長 | 用途 |
|---|------|------|------|------|------|
| 1 | `Beta` | [0.1, 5.0] | 1.5 | 0.1 | Sigmoid 激進係數 |
| 2 | `Gamma` | [0.0, 2.0] | 0.5 | 0.05 | 倉位偏置（彈簧剛度） |
| 3 | `SigmaFloor` | [0.001, 0.05] | 0.005 | 0.001 | σ 下限，防除零 |
| 4 | `SignalW_P` | [-2.0, 2.0] | **+0.5** | 0.1 | P 因子權重（非零預設保證 seed 確定性） |
| 5 | `SignalW_V` | [-2.0, 2.0] | 0.0 | 0.1 | V 因子權重（GA 自學符號方向） |
| 6 | `SignalW_A` | [-2.0, 2.0] | 0.0 | 0.1 | A 因子權重 |
| 7 | `EMABaseBars` | [50, 600] | 200 | 50 | P 用的基準線視窗（影片提「200 還是 144」） |
| 8 | `SigmaWindow` | [10, 100] | 30 | 10 | σ 滾動視窗 |
| 9 | `VelocityLookback` | [1, 10] | 3 | 1 (整數) | V 計算回看根數 |
| 10 | `WedgeDeltaThr` | [0.01, 0.1] | 0.03 | 0.005 | 楔形權重變動閾值 |
| 11 | `MacroAccelerator` | [1.0, 3.0] | 1.5 | 0.1 | 恐慌時宏觀加速倍率 |
| 12 | `MacroDeadlineDays` | [25, 30] | 28 | 1 (整數) | 月底死線觸發日 |
| 13 | `DeadAgingMonths` | [3, 24] | 6 | 1 (整數) | 底倉老化釋放門檻 |
| 14 | `TargetMicroWeightFloor` | [0.05, 0.3] | 0.15 | 0.01 | 微觀層權重下限（觸發軟釋放） |

### 9.2 結構約束（Clamp 必修復）

1. **三 SignalW 至少一個有效**：`abs(W_P) + abs(W_V) + abs(W_A) >= 0.1`，否則 `W_P = +0.5`（**確定性 fallback**，不用隨機）
2. **整數欄位先 clamp 再 round**：`EMABaseBars`、`SigmaWindow`、`VelocityLookback`、`MacroDeadlineDays`、`DeadAgingMonths`
3. **TargetMicroWeightFloor < 0.5**：低於 Sigmoid 中性點

### 9.3 SpawnPoint（不進化）

```go
type SpawnPoint struct {
    Policy CapitalPolicy
    Risk   RiskBounds
}

type CapitalPolicy struct {
    MonthlyInjectUSDT  float64  // 用戶設定
    InitialCapitalUSDT float64
    ColdSealedPct      float64  // 永久封存比例
    // 注意：不再有 micro_reserve_pct（雙引擎共用資金）
}

type RiskBounds struct {
    GlobalStopLossPct float64  // 預設 0.5
    FeeRate           float64  // Binance spot 0.001
    MinOrderUSDT      float64  // 10.1
    LotStepSize       float64
    LotMinQty         float64
}
```

### 9.4 DefaultSeedChromosome 確定性

啟動條件：DB 無精英基因 + GA 種群 index 0 = 預設種子。
**所有部署、所有時點必須產生相同 chromosome bytes 與 Fingerprint**，因此：
1. 表中 `SignalW_P = +0.5` 非零 → Clamp 結構約束 1 自動滿足
2. Clamp fallback 採確定性（指派 `W_P=+0.5`，不用隨機）
3. seed 構造直接從常量複製，不走 `Sample(rng)`

---

## 10. StrategyInput / Step() 主流程

### 10.1 Input 補充欄位

`lunar-spot-v1` 用標準 `strategy.StrategyInput`（見 `10-策略框架接口.md`）。額外用到的欄位：
- `Closes`（≥ 600 根，warm-up）
- `LivePrice`（即時順勢價）
- `Portfolio.Lots`（軟/硬釋放需要）
- `LatestBarTimeMs`（時間判定）
- `MinOrderUSD`（楔形過濾）

### 10.2 Step 主流程

```go
func (s *Strategy) Step(in strategy.StrategyInput, p strategy.Params) strategy.StrategyOutput {
    chromo := p.(Params)  // 14 欄位
    runtime := decodeRuntime(in.Runtime)

    // 1. Warm-up 檢查
    if len(in.Closes) < 600 {
        return strategy.StrategyOutput{NewRuntime: runtime}
    }

    // 2. 帳戶會計
    totalEquity := computeTotalEquity(in.Portfolio, in.LivePrice)
    spendableUSDT := math.Max(0, in.Portfolio.USDTBalance)  // 雙引擎共用，無 reserve floor
    currentMicroWeight := (in.Portfolio.FloatAsset * in.LivePrice) / totalEquity

    // 3. 計算 PVA + SJM
    pva := computePVA(in.Closes, chromo)
    regime := computeSJM(pva, chromo)

    // 4. 宏觀引擎
    newMacroState, macroIntent := macro.Decide(runtime.Macro, in.LatestBarTimeMs, spendableUSDT, regime, chromo, in.Portfolio.MonthlyInjectUSDT)

    // 5. 微觀引擎（Sigmoid 動態天平）
    microIntent, microDiag := micro.Decide(in.Closes, in.LivePrice, currentMicroWeight, totalEquity, regime, chromo, in.MinOrderUSD)

    // 6. 底倉釋放
    softRelease := release.MaybeSoftRelease(in.Portfolio.Lots, currentMicroWeight, chromo, in.LatestBarTimeMs)
    hardRelease := release.MaybeHardRelease(microIntent, in.Portfolio, microIntent.ClientOrderID)  // 配對 SELL

    // 7. 全局止損熔斷
    if totalEquity <= chromo.SpawnPoint.Policy.InitialCapitalUSDT * (1 - chromo.SpawnPoint.Risk.GlobalStopLossPct) {
        return strategy.StrategyOutput{NewRuntime: runtime}  // 熔斷
    }

    // 8. 組裝
    return strategy.StrategyOutput{
        Intents:     append(toIntents(macroIntent), toIntent(microIntent)...),
        Releases:    append(softRelease, hardRelease...),
        NewRuntime:  RuntimeState{Macro: newMacroState, /* Micro 無狀態 */},
        Diagnostics: mergeDiag(pva, regime, microDiag),
    }
}
```

---

## 11. EvolvableStrategy 接口

實作於 `internal/saas/ga/lunar_spot_v1_evolvable.go`（**不**放策略包，避免循環依賴，見 `20-進化計算引擎.md` §7.2）。

8 動詞範本參考 `strategies/grid.md` §6，但 chromosome 是 14 欄位 Params 結構。

---

## 12. 鐵律檢查

```bash
# 純函數
rg -n '(net/http|database/sql|gorm\.io/|os\.Open|math/rand|time\.(Now|Sleep|NewTicker))' \
   strategies/lunar-spot-v1/

# 標的中立（除 Manifest.SupportedSymbols 外）
rg -n '"BTCUSDT"|"ETHUSDT"' strategies/lunar-spot-v1/ | rg -v 'Manifest|SupportedSymbols'

# Mode 中立
rg -n 'isPaperMode|PaperMode|LiveMode|isBacktest' strategies/lunar-spot-v1/

# NowMs 不洩漏（Diagnostics 除外）
rg -n 'in\.NowMs|input\.NowMs' strategies/lunar-spot-v1/
```

任一非空 → 違反鐵律。

---

## 13. 必驗測試

| 測試 | 內容 |
|------|------|
| `step_determinism_test` | 相同 input 跑兩次，output 相同 |
| `sigmoid_sanity_test` | §6.2 三組 case |
| `macro_idempotency_test` | 同 bar 第二次呼叫 zero-mutation early return |
| `macro_carry_forward_test` | 5 個 worked example case（見 §7.2） |
| `factor_collinearity_test` | 對 BTC/ETH 4h 5 年數據算 PVA 三因子相關矩陣 + VIF；任兩因子 \|r\| < 0.6, VIF < 5 |
| `seed_determinism_test` | DefaultSeedChromosome 兩次構造產生相同 Fingerprint |
| `release_partial_lot_test` | 單一 oversized lot 觸發 partial release 而非 break |
| `mode_invariance_test` | Paper / Live 模式接同 input 產生同 intent |

---

## 14. 已知限制（影片新版的設計選擇）

| 限制 | 原因 |
|------|------|
| **不存 OHLCV** | 影片：「機器不需要看形態」 |
| **只支援 BTCUSDT / ETHUSDT** | 「最悲觀原則」要求多頭趨勢標的 |
| **不接針** | 80% 的人想接針 → 二八定律不接 |
| **不設止盈止損** | 用 Sigmoid 數學抹平，不依賴固定預值 |

這些是**設計選擇**，不是 framework 限制。其他策略（如 `grid`）不受此限制。

---

## 15. UI 文案建議

| 內部 | UI |
|------|----|
| `lunar-spot-v1` | 雙軌量化策略 |
| `DeadAsset` | 長期持倉 |
| `FloatAsset` | 活躍倉位 |
| `ColdSealedAsset` | 封存資產 |
| Engine `MACRO` | 宏觀引擎 |
| Engine `MICRO` | 微觀引擎 |
| Regime `Panic` | 恐慌 |
| Regime `Greed` | 貪婪 |
| Regime `Quiet` | 平靜 |
| `TargetMicroWeightFloor` | 活躍倉位下限 |
| `MacroAccelerator` | 加速倍率 |
| `MacroDeadlineDays` | 月底兜底日 |
