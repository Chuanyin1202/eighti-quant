# EightiQuant — 專案憲法

> 本檔案是所有 AI 對話與 code review 的最高優先依據。任何違反此處規則的提議，無論技術上多合理，都必須**先停下並提出說明**，不得直接執行。

EightiQuant 是一個**量化交易框架（framework）**，不是某個特定策略的實作。Reference 策略（`simple-dca` / `grid` / `lunar-spot-v1`）是 framework 的「示範作品」，可隨時新增、刪除、抽換。

---

## 1. 唯一功能真源

本專案的功能定義**只**依據 `docs/` 下的核心文件：

- `docs/00-架構總覽.md`：三端部署、framework 三層分層、paper/live 雙模式、生命週期
- `docs/10-策略框架接口.md`：Strategy SDK、`Step()` 契約、building blocks、寫策略教學
- `docs/20-進化計算引擎.md`：GA 黑盒引擎、8 動詞接口、Promote 安全協議
- `docs/30-Paper-Trade-模式.md`：模擬成交規則、PaperBroker 接口、demo UX
- `docs/strategies/<策略名>.md`：個別策略 reference 實作規格

**核心文件未定義的功能不進入實作**。實作中發現規格不足或矛盾，必須**先更新文件、再寫 code**。

`docs/legacy/` 內容**已廢棄**，僅供歷史參考，不再是 source of truth。

---

## 2. 核心鐵律（九條，違反必停）

### 2.1 策略同構
回測、Paper、Live 三種模式**必須**呼叫同一個 `Step()` 實作。`Step()` 內部禁止：
- `if isBacktest { ... }`
- `if isPaperMode { ... }`
- 任何依賴 mode 的分支

### 2.2 策略純函數
`Step()` 內部禁止：
- 時鐘類：`time.Now` / `time.NewTimer` / `time.NewTicker` / `time.Tick` / `time.Since` / `time.Sleep` / `time.After` / `time.AfterFunc`
- 網路類：`net/http` / `net/rpc` / `google.golang.org/grpc` / `gorilla/websocket` / `nhooyr.io/websocket`
- DB 類：`database/sql` / `gorm.io/*` / `*Tx`
- 檔案類：`os.Open` / `os.OpenFile` / `os.Create` / `os.ReadFile` / `os.WriteFile` / `io/ioutil.*`
- 隨機類：`math/rand`

策略只接受 `StrategyInput`，只回傳 `StrategyOutput`，相同輸入必產相同輸出。
時間判定**必須**用 `input.LatestBarTimeMs`（= bar close 時間戳），禁讀 `NowMs`。

### 2.3 API Key 物理隔離
Binance API Key / Secret **只能**存在於 LocalAgent 本地 `config.agent.yaml`：
- 永不進入 SaaS 服務記憶體
- 永不寫入任何資料庫表
- 永不透過網路傳輸到雲端

Paper 模式**不需要** Binance API Key（PaperBroker 不下實單）。

### 2.4 GORM Code-First + indexes.sql 例外

**單一規則：表結構（tables、columns、types）真源在 Go struct，由 `db.AutoMigrate(...)` 同步；其他 DB 物件（partial index、check constraint）由受控 raw SQL 管理。**

#### 2.4.1 由 struct + AutoMigrate 管理（**禁止** raw SQL）
- 表的 CREATE / 欄位 ADD / 型別變更
- 全表 unique index（GORM tag `uniqueIndex` 可表達）
- 一般 index（GORM tag `index` 可表達）

要改 → 改 Go struct → 重啟 SaaS。

#### 2.4.2 由 raw SQL 管理（**強制** `internal/saas/store/indexes.sql`）
- PostgreSQL partial index（含 `WHERE` 子句）
- Check constraint
- 由 `db.go` 在 AutoMigrate 完成後 `db.Exec(ddl)` 執行
- 必須 `IF NOT EXISTS`，文件對應段落說明用途

### 2.5 無量綱計算
跨標的禁用絕對價格。用對數收益率、ratio、百分比。

### 2.6 單一 Postgres + Redis 僅快取
- 單一 Postgres 實例（資料庫名 `eighti_quant`）
- Redis 僅作快取（冠軍基因、Session、Paper 帳本暫存等）
- 不引入 RabbitMQ / Kafka / NATS

### 2.7 Paper / Live 同構
Paper 與 Live 跑**同一個** `Step()`，差別只在 broker 實作層（`internal/broker/paper/` vs `internal/broker/binance/`）。framework 任何 code path 不應因 mode 分支。

### 2.8 Strategy 可抽換
- 增刪策略不影響 framework core
- `internal/saas/ga/engine.go` 等 framework 程式碼**禁止** `import` `strategies/` 任何包
- Application 層完全 plug-and-play

### 2.9 Framework 不感知策略
- Framework Core 不知道任何具體策略名稱
- Strategy SDK 不依賴任何具體 broker 實作

---

## 3. 工作順序

### 3.1 涉及策略邏輯
→ 讀 `docs/strategies/<策略名>.md` + `docs/10-策略框架接口.md`。先補文件再寫 code。

### 3.2 涉及 GA / 進化
→ 讀 `docs/20-進化計算引擎.md`。新策略只需實作 `EvolvableStrategy` 接口，**不得**修改 `engine.go`。

### 3.3 涉及 Go 後端 schema / DB
→ 遵守 §2.4：表結構改 struct → AutoMigrate；partial index 進 `indexes.sql`。**禁止**寫 migration `.sql` 改表結構。

### 3.4 涉及 Paper / Broker
→ 讀 `docs/30-Paper-Trade-模式.md`。新增 broker 實作必須符合 `internal/broker/Broker` 接口。

### 3.5 涉及價格計算
→ 優先用對數收益率、ratio、百分比。函數簽章避免 `priceUSDT`、`btcPrice` 絕對價格參數。

### 3.6 涉及架構邊界
→ Framework Core / Strategy SDK / Application **三層分工保持現狀**。

---

## 4. 程式碼目錄職責

| 目錄 | 職責 | 禁區 |
|------|------|------|
| `cmd/saas/` | SaaS 進程入口 | 不寫業務邏輯 |
| `cmd/agent/` | LocalAgent 進程入口 | 不含策略代碼、不連 DB |
| `internal/saas/` | SaaS 雲端服務（API / WS Hub / Cron / GA / Instance） | 不持 API Key、不直接呼叫交易所 |
| `internal/saas/ga/` | GA 進化引擎黑盒 | **不 import** `strategies/` |
| `internal/saas/store/` | GORM models + db.go + indexes.sql | 不寫業務邏輯 |
| `internal/agent/` | LocalAgent 客戶端（exchange / WS client） | 不含策略代碼、不連 DB |
| `internal/broker/` | Broker 接口 + 實作（binance / paper/*） | 不直接呼叫策略 |
| `internal/strategy/` | Strategy SDK 接口定義 | 不含具體策略實作 |
| `internal/strategies/<策略名>/` | 具體策略實作（純函數）| 禁網路 / DB / 檔案 / 計時器 / 隨機 |
| `internal/quant/` | 純數學工具庫（EMA / StdDev / Sigmoid / lot 工具）| 不依賴具體策略 |
| `internal/quant/signals/` | 信號計算器 building blocks | 純函數 |
| `internal/quant/regimes/` | 市場狀態偵測器 building blocks | 純函數 |
| `internal/quant/engines/` | 倉位管理引擎 building blocks（Sigmoid / DCA / Grid）| 純函數 |
| `internal/adapters/backtest/` | 回測適配器（呼叫 `Step()`）| 與實盤共用 `Step()` |

---

## 5. 主線設定（reference 實作清單）

| 策略 | 複雜度 | GA | 用途 |
|------|--------|----|------|
| `simple-dca` | 最簡 | 不進化 | baseline + 教學範例 |
| `grid` | 中等 | 5 染色體 | 「中等可進化策略」範例 |
| `lunar-spot-v1` | 複雜 | 14 染色體 | 「雙引擎 + 多狀態 + Sigmoid」範例（影片版） |

| 項目 | 主線 |
|------|------|
| 交易所 | Binance 現貨（live）+ PaperBroker（paper） |
| 標的（demo） | BTCUSDT + ETHUSDT |
| Paper 模式精度 | C1（簡單版，固定 0.1% fee 無滑點） |
| Postgres | 15，單一實例 `eighti_quant` |
| Redis | 7，僅快取 |
| Go 版本 | 1.26+ |

---

## 6. 驗證命令

```bash
# 編譯檢查
go list ./...
go build ./...

# 完整測試（含資料競態檢查）
go test ./... -race -timeout 300s
```

### 6.1 鐵律驗證

```bash
# 鐵律 1: 策略同構（無 mode 分支）
rg -n 'isBacktest|isPaperMode|isLiveMode' internal/strategies/

# 鐵律 2: API Key 物理隔離
rg -n '(api_?key|secret_?key|passphrase)' internal/saas/ internal/quant/ internal/broker/paper/

# 鐵律 3: 策略純函數
rg -n '(net/http|google\.golang\.org/grpc|gorilla/websocket|nhooyr\.io/websocket|database/sql|gorm\.io/|io/ioutil|os\.(Open|OpenFile|Create|ReadFile|WriteFile)|time\.(Now|NewTimer|NewTicker|Tick|Since|Sleep|After|AfterFunc)|math/rand)' internal/strategies/ internal/quant/

# 鐵律 4: NowMs 不洩漏（決策時間用 LatestBarTimeMs）
rg -n 'in\.NowMs|input\.NowMs' internal/strategies/

# 鐵律 5: 內核標的中立（除 Manifest.SupportedSymbols）
rg -n '"BTCUSDT"|"ETHUSDT"' internal/strategies/ | rg -v 'Manifest|SupportedSymbols'

# 鐵律 6: Framework 不感知策略
rg -n 'strategies/' internal/saas/ga/
rg -n '"lunar-spot-v1"|"simple-dca"|"grid"' internal/saas/ga/
```

**退出碼**：rg exit 0 = 有匹配 = 違反鐵律 = CI fail。

CI wrapper script 範本見 `docs/00-架構總覽.md` 附錄。

---

## 7. UI 文案紀律
面向用戶的字串避免：
- 內部狀態機術語（`"DEAD"` / `"FLOAT"`）
- 無上下文的裸數學量名（`TheoreticalUSD`）
- 希臘字母單獨出現

UI 對照表見 `docs/00-架構總覽.md` §8 + 各策略文件 UI 文案建議段落。

---

## 8. 範圍排除

### 8.1 永不做
- 槓桿合約交易（僅現貨）
- 多用戶 Agent 共用（每用戶最多一個 Agent 連線）

### 8.2 Phase 14+ 重新評估
- WFO（Walk-Forward Optimization）
- 自動定時觸發 GA
- AI 多維信號層（LLM 輔助）
- Rolling deploy / cluster 化（當前主線假設單 SaaS process）
- C2 / C3 高精度 paper broker

需求變更時，先更新本檔案再動手。

---

## 9. 商業化考量（內部備註，不對外文件）

EightiQuant 的商業價值在「**框架可複用 + 策略可抽換**」，不在「某個特定策略賺錢」。

- 不要把 demo 績效作為「保證」、「預期年化」展示
- Paper 模式 UI 必須清楚標示「**模擬交易，不能保證實盤效果**」
- ToS 必須寫清楚「**not financial advice / 用戶自負盈虧**」
- API Key 物理隔離鐵律恰好幫框架避開「自動化金融服務」的監管雷區
