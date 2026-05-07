# 30 — Paper Trade 模式

**文件目標：** 定義 EightiQuant 的 paper（模擬）/ live（實盤）雙模式。Paper 模式讓 demo 用戶**不需要真錢、不需要 API Key、不需要 LocalAgent** 也能完整體驗系統。

---

## 1. 設計目標

### 1.1 Paper Mode 是一級公民
- 不是「縮減版 live」，是平等模式
- Step() **完全不知道**自己跑在哪種模式下（鐵律 7：Paper / Live 同構）
- UI / API / 帳本 / 進化 / Reconcile 全部正常運作
- 唯一差異：成交透過 PaperBroker 模擬，而非透過 Agent 下實單

### 1.2 三層精度（C1 起步、預留 C2-C3）

| 等級 | 滑點 | 手續費 | 成交速度 | 部分成交 | 工作量 |
|------|------|--------|---------|---------|--------|
| **C1（本期）** | 無 | 固定 0.1% | 即時 | 無 | 1 day |
| **C2（規劃）** | ±0.05% 隨機 | Binance VIP 等級 | 即時 | 無 | 2-3 days |
| **C3（規劃）** | Order book 深度模擬 | 完整 fee schedule | 機率分布 | 有 | 1-2 weeks |

C1 滿足 demo 需求。C2/C3 等用戶反饋再做。

---

## 2. Broker 接口（架構預留擴展）

```go
// internal/broker/broker.go
package broker

type Broker interface {
    PlaceOrder(ctx context.Context, cmd TradeCommand, currentPrice float64) (Execution, error)
    GetBalances(ctx context.Context) ([]Balance, error)
    Kind() string  // "binance" / "paper-simple" / "paper-realistic" / "paper-advanced"
}

type Execution struct {
    ClientOrderID  string
    OrderID        string  // broker 端 ID（paper 自生成 UUID）
    FilledQty      float64
    FilledPrice    float64
    FeeAsset       string
    FeeAmount      float64
    Status         string  // "filled" / "partial" / "rejected"
    ExecutedAtMs   int64
}

type Balance struct {
    Asset string
    Free  float64
    Locked float64
}
```

### 2.1 Broker 實作清單

```
internal/broker/
├── broker.go              # 接口
├── factory.go             # 根據 instance.Mode 建構對應 broker
├── binance/
│   └── binance.go         # 真實 broker（透過 LocalAgent WS 中繼）
└── paper/
    ├── simple.go          # C1（本期實作）
    ├── realistic.go       # C2（規劃）
    └── advanced.go        # C3（規劃）
```

### 2.2 Factory 模式

```go
// internal/broker/factory.go
func NewBroker(instance *Instance, deps Dependencies) Broker {
    switch instance.Mode {
    case "live":
        return binance.New(deps.WSHub, instance.UserID)
    case "paper":
        switch instance.PaperPrecision {
        case "", "simple":
            return paper.NewSimple(deps.PriceProvider, deps.LedgerStore)
        case "realistic":
            return paper.NewRealistic(...) // C2 未實作 → return error
        case "advanced":
            return paper.NewAdvanced(...) // C3 未實作 → return error
        }
    }
    return nil
}
```

---

## 3. SimplePaperBroker（C1 規格）

### 3.1 規則

```
PlaceOrder:
  1. 取 currentPrice 作為成交價（無滑點）
  2. fee = order.amount × 0.001  // 固定 0.1%
  3. 立即更新 in-memory 帳本（Free balance）
  4. 回傳 Execution{
       Status: "filled",
       FilledQty: cmd.qty (or amount/currentPrice for BUY),
       FilledPrice: currentPrice,
       FeeAsset: "USDT" (BUY) or asset (SELL),
       FeeAmount: fee,
       ExecutedAtMs: clock.Now() (only for paper, not strategy time)
     }
  5. 同步寫入 SaaS DB 的 SpotExecution 表（status=filled，與 live 同表）
  6. 觸發 SaaS 內部「DeltaReport 處理」邏輯（與 live 同 code path）
```

### 3.2 帳本初始化

用戶建 paper instance 時：
```
POST /api/v1/instances
{
  "strategy_id": "...",
  "symbol": "BTCUSDT",
  "mode": "paper",
  "paper_initial_balance": {
    "USDT": 1000.0,
    "BTC": 0.0
  },
  "paper_monthly_inject": 100.0  // 月度注資模擬
}
```

PaperBroker 在 `paper_balances` 表持久化（不污染 live broker 的 `balances`）。

### 3.3 DB schema

```go
type PaperBalance struct {
    ID        uint    `gorm:"primaryKey"`
    InstanceID uint   `gorm:"uniqueIndex:idx_inst_asset"`
    Asset     string  `gorm:"uniqueIndex:idx_inst_asset"` // "USDT" / "BTC"
    Free      float64
    Locked    float64
    UpdatedAtMs int64
}
```

跟 live `Balance`（從 Binance 同步而來）分表，避免混淆。

### 3.4 月度注資模擬

PaperBroker 內建一個 cron job：每月 1 號 0:00 UTC 自動把 `paper_monthly_inject` 加到 USDT free balance。模擬「用戶每月把錢轉進交易所」。

### 3.5 LivePrice 來源

PaperBroker 跟 live 一樣需要「即時價」。實作：
- `internal/broker/priceprovider/`
- 從 Binance public REST `GET /api/v3/ticker/price?symbol=BTCUSDT` 拉
- **不需要 API Key**（public endpoint）
- 短 TTL cache（10 秒）避免過頻請求

```go
type PriceProvider interface {
    Get(symbol string) (float64, error)
}
```

Live 模式也用同一個 PriceProvider，paper / live 共用此設計。

---

## 4. Cron Tick 在 Paper 模式的差異

`docs/00-架構總覽.md` §6.2 的 Phase D 派單階段：

```
Live 模式：
  WS 下發 TradeCommand → LocalAgent → Binance REST → Agent 上報 DeltaReport

Paper 模式：
  PaperBroker.PlaceOrder() → 直接成交 → 同步寫 SpotExecution → 觸發內部 DeltaReport 處理
```

**Phase A/B/C 完全相同**：champion 載入、pending 預檢、Step()、原子持久化全部走同一條 code path。

---

## 5. Paper 模式下哪些功能保留

| 功能 | Paper | 說明 |
|------|-------|------|
| Step() | ✓ | 完全相同 |
| GA 進化 | ✓ | 完全相同（GA 本來就跑在歷史 closes 上）|
| Champion / Promote | ✓ | 完全相同 |
| BLOCKED auto-recovery | ✓ | 完全相同 |
| Pending Reconcile | ✗ | Paper 訂單即時成交，無 pending 概念 |
| Fill Compensation | ✗ | 不會發生 cancel / reject |
| Lot vs Balance 一致性檢查 | ✓ | 保留，定期自查（防止 PaperBroker bug）|
| Daily P&L 圖 | ✓ | 完全相同 |
| Audit Log | ✓ | 完全相同 |

---

## 6. Paper Mode 的限制（明示）

UI 必須清楚標示：

> ⚠️ **Paper 模式提示**：
> - 此模式為**模擬交易**，不會發生真實買賣
> - 模擬器使用即時市價成交、固定 0.1% 手續費，不模擬滑點
> - 結果**不能保證**反映真實交易效果（真實交易有滑點、深度限制、流動性風險）
> - 建議搭配同模式至少 30 天評估後，再考慮切換 Live 模式

**禁止**將 paper 模式的歷史績效作為「保證」、「預期收益」展示。

---

## 7. 從 Paper 升級到 Live 的流程

```
1. 用戶在 Paper 模式跑了 N 天，覺得策略 OK
2. UI 顯示「升級至實盤」按鈕
3. 流程：
   a. 創建新 instance（同 strategy_id + symbol）但 mode = "live"
   b. 用戶上傳 Binance API Key 到 LocalAgent
   c. 啟動 Agent 連線
   d. 新 instance 從 0 開始（不繼承 paper 的虛擬餘額）
4. 兩個 instance 同時存在 OK：用戶可以一邊看 paper、一邊跑 live
```

**禁止**「同一個 instance 從 paper 切換到 live」—— 帳本性質不同（虛擬 vs 真實），混合會混亂。

---

## 8. 測試覆蓋（必須）

| 測試案例 | 內容 |
|---------|------|
| `TestPaperSimple_BUY` | 用 100 USDT 買 BTC，驗算 USDT -100、BTC +(100×(1-0.001)/price) |
| `TestPaperSimple_SELL` | 賣 0.001 BTC，驗算 USDT +(0.001×price×(1-0.001)) |
| `TestPaperSimple_InsufficientFunds` | USDT 不足拒單 |
| `TestPaperSimple_DustOrder` | 訂單 < MinOrderUSDT 拒單 |
| `TestPaperMonthlyInject` | 月初自動注資 |
| `TestPaperPriceProviderTimeout` | LivePrice 拉不到 → 跳過此 tick（不暴衝） |
| `TestModeInvariance` | 同樣 input 跑 paper / live 模式（mock binance），Step() 產生同樣 intent |

---

## 9. 監控指標

Paper instance 對外公開以下 metrics（給用戶 dashboard）：
- 模擬累計收益（USDT）
- 模擬 ROI（%）
- 模擬交易次數
- 模擬勝率
- 模擬最大回撤
- vs 同期 BTC buy-and-hold 對比

**不顯示**「預期年化」、「Sharpe」這種需要長期統計才有意義的指標 —— 防止用戶誤判。
