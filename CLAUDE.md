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

### 2.4 GORM Code-First 唯一 Schema 真源（含 raw SQL 規則）

**單一規則：表結構（tables、columns、types）真源在 Go struct，由 `db.AutoMigrate(...)` 同步；其他 DB 物件（partial index、check constraint、advisory lock 設定）由受控 raw SQL 管理。**

#### 2.4.1 由 struct + AutoMigrate 管理（**禁止** raw SQL）
- 表的 CREATE / 欄位的 ADD / 型別變更
- 全表 unique index（GORM tag `uniqueIndex` 可表達者）
- 一般 index（GORM tag `index` 可表達者）

要改以上任一項 → 改 Go struct → 重啟 SaaS（AutoMigrate 自動同步）。**禁止**寫 migration `.sql` 改表結構、**禁止** 手動 `ALTER TABLE`、**禁止** 維護版本化 migration 腳本。

#### 2.4.2 由 raw SQL 管理（**強制**用 `internal/saas/store/indexes.sql`）

GORM struct tag 表達不了的 DB 物件，**必須**寫進 `internal/saas/store/indexes.sql`，由 `db.go` 在 `AutoMigrate` 完成後 `db.Exec(ddl)` 執行。當前已知用例：

- **PostgreSQL partial index**（含 `WHERE` 子句）
  - 例：`CREATE UNIQUE INDEX uniq_active_champion ON gene_records (strategy_id, symbol) WHERE role = 'champion';`
  - 用途：強制單一 champion 不變式，是 promote 競態的最後防線（見 `docs/進化計算引擎.md` §6.2.1.1）
- **Check constraint**（GORM 不支援 expression 級檢查）
- **資料庫層級的 trigger / function**（當前 Phase 0-13 不使用）

`indexes.sql` 必須：
- 每條 DDL 寫 `IF NOT EXISTS`，可重複執行不報錯
- 在文件（`docs/`）對應段落說明該 DB 物件的目的
- 變更時走 PR review，不允許直接動 production DB

#### 2.4.3 為什麼這樣切
- 表結構在 struct → code review 友善、跨環境一致
- DB 層級不變式約束（partial unique 等）必須在 DB 強制，**不能**只靠應用層協議
- 兩者分職守，互不混淆

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
→ 遵守 §2.4 規則：表結構改 struct → AutoMigrate 自動同步；partial index / check constraint 等 GORM 不支援的物件**必須**進 `internal/saas/store/indexes.sql`（由 `db.go` 在 AutoMigrate 後 Exec）。**禁止**寫 migration `.sql` 或手動 ALTER TABLE 改表結構。

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
```

### 6.1 鐵律驗證（用 ripgrep，必須全部無匹配）

優先使用 `rg`（ripgrep）。**注意：`rg` 不帶 `-E`**，因為 `rg -E` 是 `--encoding`（指定檔案編碼），不是 grep 的「擴展正規」。`rg` 預設就支援 alternation `|` 與其他標準 regex 構件。

```bash
# 鐵律 1: 策略同構（無 isBacktest 分支）
rg -n 'isBacktest' internal/strategies/

# 鐵律 2: API Key 物理隔離（SaaS 側無交易所憑證）
rg -n '(api_?key|secret_?key|passphrase)' internal/saas/ internal/quant/

# 鐵律 3: OHLC 剝離（策略內核不依賴 Bar 結構）
rg -n 'quant\.Bar' internal/strategies/

# 鐵律 4: 策略純函數（禁網路 / DB / 檔案 / 計時器 / 隨機）
rg -n '(net/http|google\.golang\.org/grpc|gorilla/websocket|nhooyr\.io/websocket|database/sql|gorm\.io/|io/ioutil|os\.(Open|OpenFile|Create|ReadFile|WriteFile)|time\.(Now|NewTimer|NewTicker|Tick|Since|Sleep|After|AfterFunc)|math/rand)' internal/strategies/ internal/quant/

# 鐵律 5: 內核標的中立（禁止 if symbol == "BTCUSDT" 之類）
rg -n '"BTCUSDT"|"ETHUSDT"' internal/strategies/ internal/quant/
```

**退出碼期望**：鐵律驗證**期望「無匹配」**（`rg` exit 1）才算通過。`rg` 的退出碼語義：
- `0` = 有匹配 → **違反鐵律，CI 必須 fail**
- `1` = 無匹配 → 通過
- `2` = 命令錯誤 → CI 必須 fail（避免「壞命令當通過」）

CI script 範本（每條鐵律包一個 wrapper，反轉 exit 0）：

```bash
#!/bin/bash
set -e

check() {
  local rule="$1"; shift
  if rg "$@" > /tmp/rg.out; then
    echo "❌ 違反鐵律: $rule"
    cat /tmp/rg.out
    exit 1
  elif [ $? -eq 2 ]; then
    echo "❌ rg 命令錯誤（鐵律 $rule）"
    exit 2
  fi
}

check "1-isBacktest" -n 'isBacktest' internal/strategies/
check "2-apikey" -n '(api_?key|secret_?key|passphrase)' internal/saas/ internal/quant/
# ... 以此類推
```

> 註：歷史踩坑紀錄：原本寫 `grep -rn '...\|...'` 用 BRE alternation 實際抓不到，後改 `rg -nE` 又踩到「-E 在 rg 是 encoding flag 不是 extended regex」，這次最終定案不帶 -E，並用上述 wrapper script 反轉退出碼語義。

---

## 7. UI 文案紀律（前端）

面向用戶的字串中**避免**出現：
- 內部狀態機術語（`"DEAD"` / `"FLOAT"`、`S3_Panic`，UI 應翻為「長期持倉/活躍倉位」）
- 無上下文的裸數學量名（`TheoreticalUSD`、`VolatilityRatio`）
- 希臘字母單獨出現（`β`、`γ`），若必須保留須加文字釋義（如「σ（標準差）」）

替換對照表見 `docs/系統總體拓撲結構.md` 附錄。

---

## 8. 不做的事（範圍排除）

以下功能在 **Phase 0-13 主線範圍不實作**，被提及時直接告知「不在當前範圍」：

### 8.1 永不做（產品定位排除）
- 槓桿合約交易（本系統定位為現貨）
- 跨交易所套利（單一交易所 Binance）
- 多用戶 Agent 共用（每用戶最多一個 Agent 連線）

### 8.2 Phase 14+ 重新評估（暫不做）
- WFO（Walk-Forward Optimization）滾動回測
- 自動定時觸發 GA（當前必須人工觸發）
- 全幣種訊號掃描（當前主線僅 BTCUSDT + ETHUSDT 雙標的）
- AI 多維信號層（LLM 輔助）

需求變更時，先更新本檔案再動手。
