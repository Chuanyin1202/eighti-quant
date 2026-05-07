# QuantSaaS — 專案憲法

> 本檔案是所有 AI 對話與 code review 的最高優先依據。任何違反此處規則的提議，無論技術上多合理，都必須**先停下並提出說明**，不得直接執行。

---

## 1. 唯一功能真源

本專案的功能定義**只**依據 `docs/` 下的三份真源文件：

- `docs/系統總體拓撲結構.md`：定義系統三端部署、邏輯模組、狀態流轉、通訊協議
- `docs/策略數學引擎.md`：定義 `Step()` 函數的完整輸入輸出契約與內部各層計算邏輯
- `docs/進化計算引擎.md`：定義 GA 遺傳演算法引擎的規格與接口契約

**三份文件未定義的功能不進入實作**。實作中發現文件規格不足或矛盾，必須**先更新文件、再寫 code**，禁止「先寫 code 再回頭補文件」。

---

## 2. 核心鐵律（六條，違反必停）

### 2.1 策略同構
回測與實盤**必須**呼叫同一個 `Step()` 實作。`Step()` 內部禁止出現 `if isBacktest { ... } else { ... }` 類型的分支。

### 2.2 策略純函數
`Step()` 內部禁止：
- 計時器（`time.NewTimer`、`time.Tick`）
- 網路請求（`http.*`、`net.*`、WebSocket、gRPC）
- 資料庫讀寫（`*sql.DB`、`gorm.*`）
- 檔案 I/O（`os.Open`、`os.Create`）
- `time.Now()`（時間必須由 `StrategyInput` 帶入）

策略只接受 `StrategyInput`，只回傳 `StrategyOutput`，相同輸入必產相同輸出。

### 2.3 API Key 物理隔離
Binance API Key / Secret **只能**存在於：
- `LocalAgent` 本地的 `config.agent.yaml` 檔案
- 該檔案已加入 `.gitignore`，永不入 Git 歷史

**永不**：
- 進入 SaaS 服務記憶體或 process
- 寫入任何資料庫表
- 透過網路傳輸到雲端

發現任何 code 將 API Key 寫入 SaaS 側時，**必須立即停止並報告**。

### 2.4 GORM Code-First 唯一 Schema 真源
資料庫結構以 Go struct 為唯一真源，透過 `db.AutoMigrate(...)` 同步。

**禁止**：
- 寫 SQL migration 檔案（`.sql`、`migrations/` 目錄）
- 維護版本化 migration 腳本
- 手動 `ALTER TABLE`

要改 schema → 改 Go struct → 重啟 SaaS（AutoMigrate 自動同步）。

### 2.5 無量綱計算
所有價格相關計算**必須**使用對數收益率或比率（無量綱），**禁止**用絕對價格做跨標的比較。

範例：
- ❌ `if btc_price > eth_price`
- ✅ `if log(btc_close[t] / btc_close[t-1]) > threshold`

### 2.6 單一 Postgres + Redis 僅快取
- 整個系統使用**單一** Postgres 實例（資料庫名 `quantsaas`），不分 Shell DB / Live DB
- Redis **僅**作快取（冠軍基因、Session、AI 訊號 TTL），**不**承擔信號傳遞或事件佇列職責
- 不引入 RabbitMQ / Kafka / NATS 等訊息佇列

---

## 3. 工作順序（每次動手前自查）

### 3.1 涉及策略邏輯或回測
→ 先讀 `docs/策略數學引擎.md`，確認本次修改在文件規格範圍內。文件未定義 → 先補文件再寫 code。

### 3.2 涉及 GA / 進化 / 適應度
→ 先讀 `docs/進化計算引擎.md`，特別是 `EvolvableStrategy` 8 動詞接口。新增策略只需實作接口，**不得**修改 `engine.go`。

### 3.3 涉及 Go 後端 schema / DB
→ 嚴格遵守 GORM Code-First：改 struct → AutoMigrate。**不寫** SQL 檔案。

### 3.4 涉及價格 / 訊號計算
→ 優先用對數收益率、ratio、百分比。函數簽章避免出現 `priceUSDT`、`btcPrice` 等絕對價格參數（除非僅用於下單轉換）。

### 3.5 涉及架構邊界
→ SaaS / Agent / Strategy 三層分工**保持現狀**，不做預防性解耦、不抽象「未來可能會用到的接口」。三端職責邊界由 `docs/系統總體拓撲結構.md` 第 3 章定義。

---

## 4. 程式碼目錄職責

| 目錄 | 職責 | 禁區 |
|------|------|------|
| `cmd/saas/` | SaaS 進程入口 | 不寫業務邏輯 |
| `cmd/agent/` | LocalAgent 進程入口 | 不含策略代碼、不連 DB |
| `internal/saas/` | SaaS 雲端服務（API / WS Hub / Cron / GA / Instance） | 不持有 API Key、不直接呼叫交易所 |
| `internal/agent/` | LocalAgent 客戶端（Exchange / WS Client） | 不含策略代碼、不連 DB |
| `internal/strategy/` | 策略接口定義（`Step()` 簽章、`StrategyInput/Output`） | 不含具體策略實作 |
| `internal/strategies/[策略名]/` | 具體策略實作（純函數）| 禁網路 / DB / 檔案 I/O |
| `internal/quant/` | 量化數學基礎庫（EMA / Sigmoid / lot / Ghost DCA / 市場狀態）| 不依賴具體策略 |
| `internal/adapters/backtest/` | 回測適配器（呼叫 `Step()` 跑歷史 K 線）| 與實盤 cron tick 共用 `Step()` |

---

## 5. 主線設定（本專案的具體選型）

| 項目 | 選型 | 備註 |
|------|------|------|
| 交易所 | **Binance** 現貨 | 透過 Binance Spot REST API v3 |
| 主線交易對 | **BTCUSDT** + **ETHUSDT** 雙標的 | 每個 Instance 綁定一個標的，各自獨立 GA / champion |
| 策略代號 | `lunar-spot-v1` | 第一條主線策略，現貨多空中性 |
| 時鐘聚合週期 | `4h` K 線 | cron 每分鐘掃描，但 Instance tick 只在最新 4h bar 完成且未處理時推進 |
| Postgres | 15 | 單一實例 `quantsaas` |
| Redis | 7 | 僅快取 |
| Go 版本 | 1.26+ | |

「全幣種訊號掃描」屬於 Phase 14+ 的外掛模組，**不進入主線 Phase 0-13 開發範圍**。

---

## 6. 驗證命令

每次重大修改完成後，至少執行：

```bash
# 編譯檢查
go list ./...
go build ./...

# 完整測試（含資料競態檢查）
go test ./... -race -timeout 300s

# 鐵律驗證（grep 必須無結果）
grep -rn "isBacktest" internal/strategies/
grep -rn "api_key\|secret_key\|passphrase" internal/saas/
grep -rn "quant\.Bar" internal/strategies/
grep -rn "http\.\|database/sql\|os\.Open\|time\.Now" internal/strategies/
```

任何 grep 出現非空結果 → 違反鐵律 → 必須修正後才算完成。

---

## 7. UI 文案紀律（前端）

面向用戶的字串中**避免**出現：
- 內部狀態機術語（`DEAD_STACK`、`S3_Panic`）
- 無上下文的裸數學量名（`TheoreticalUSD`、`VolatilityRatio`）
- 希臘字母單獨出現（`β`、`γ`），若必須保留須加文字釋義（如「σ（標準差）」）

替換對照表見 `docs/系統總體拓撲結構.md` 附錄。

---

## 8. 不做的事（明確排除）

以下功能在當前 Phase 範圍**不實作**，提及時直接拒絕：

- WFO（Walk-Forward Optimization）滾動回測
- 自動定時觸發 GA（必須人工觸發）
- 全幣種訊號掃描（Phase 14+）
- 槓桿合約（僅做現貨）
- 多用戶 Agent 共用（每用戶最多一個 Agent 連線）
- 跨交易所套利

需求變更時，先更新本檔案再動手。
