# crackstation — 门座起重机焊缝疲劳裂纹检查计划后端

管理每个被监测部位的探伤历史、部位专属 Paris 扩展系数修正、剩余寿命估算和下次探伤计划。

- 语言/框架：Go 1.23 + Echo v4
- 数据库：PostgreSQL 16（pgx/v5，标准库 `database/sql`）
- 无前端页面，仅 HTTP JSON API

## 快速开始

```bash
docker compose up --build
# API: http://localhost:8080 ，postgres:16-alpine: localhost:5432，启动时自动建表
```

无数据库时的内存模式（非持久，仅演示/测试）：

```bash
go run ./cmd/server                 # 或 DRIVER=memory HTTP_ADDR=:8080
```

## 测试

```bash
go test ./...                       # 全部单元/行为/HTTP 测试，无需数据库
# PostgreSQL 存储层与领域行为的真实数据库测试（自动建独立 scratch 库）：
CRACK_TEST_DSN="postgres://crack@localhost:5432/crackstation?sslmode=disable" go test ./...
```

测试均用 Go 自带 `testing`，无第三方测试框架。

## 验收点对照

| 验收项 | 位置 |
|---|---|
| 恒幅参考例（7.77×10⁵ 循环） | `internal/propagation/propagation_test.go: TestAnalyticReferenceConstantAmplitude` |
| 应力幅加倍 → 1/8 | `TestDoubleStressOneEighth`、`TestCenterCrackDoubleAmplitude` |
| 两种几何 β 趋势 | `TestBetaTrends` |
| 积分误差（相对 20 万步细网格 1.6×10⁻⁸，要求 ≤0.1%） | `TestFiniteWidthConvergence`，说明见 `docs/accuracy.md` |
| 系数修正还原合成数据 | `internal/calibration/calibration_test.go: TestRecoverCFromSyntheticFoundHistory` |
| “未发现”记录处理 | `TestNotFoundConstraintPullsCDown` + 包注释（单侧上限约束） |
| 补录与顺序录入一致 | `internal/recordlog/behavior_test.go: TestBackfillMatchesChronologicalEntry`（内存 + PG 两套） |
| 历史日期查询/对比 | `TestHistoryDateQuery`、`TestPGCorrectionHistoryAndReplay` |
| 材料更新影响清单 | `TestMaterialUpdateImpact` / `TestPGMaterialImpact` |
| 并发提交 | `TestConcurrentSubmissions` / `TestPGConcurrentSubmissions` |
| 字段拒收 | `TestRejectInvalidFields`、`internal/httpapi TestHTTPValidation` |
| 净截面屈服可先于断裂 | `TestNetYieldCanGovern` |

## 关键设计

- **几何与 SIF**（`internal/geometry`）：中心穿透裂纹物理长度 2a、边缘裂纹 a；
  β 分别为 √sec(πa/2W) 与 Tada 边缘多项式（a/W≤0.65）。
- **扩展积分**（`internal/propagation`）：按裂纹增量分步，每步中点冻结 β 后用
  Paris 幂律解析原函数，自适应对分 + Richardson 修正；临界尺寸取 Kmax=KIC 与
  净截面屈服（σmax·W/lig=σy）的先到者。精度/耗时见 `docs/accuracy.md`。
- **系数修正**（`internal/calibration`）：m 按材料固定，只拟合 C；过首条 found
  锚点做最小二乘，“未发现”记录作为 `C ≤ cap` 的单侧上限（只能压低 C，即相对
  忽略它略偏激进；靠安全系数保裕度），矛盾时残差 RMS 暴露不一致。
- **版本与确定性**（`internal/recordlog` + `internal/store`）：记录只追加、
  更正保留旧行；每部位记录版本单调递增；计划是“当时活动记录集 + 当时材料/谱
  版本”的纯函数，所以补录与乱序并发的最终结果都等于按日期顺序处理。Postgres
  写入在事务内 `SELECT ... FOR UPDATE` 锁部位行串行化。
- **历史回放**：按 `created_at` 重放当时存在且未被当时更正取代的记录、当时生效
  的材料版本与谱修订（拟合暴露量按谱修订时间点分段）。

详见 `docs/design.md`、`docs/accuracy.md`。

## 主要接口

```
POST   /api/v1/materials
POST   /api/v1/materials/:id/versions            # 返回因此提前的部位清单
POST   /api/v1/locations
POST   /api/v1/locations/:id/spectrum            # 载荷谱新修订（不可变）
POST   /api/v1/locations/:id/records             # 提交探伤记录
POST   /api/v1/locations/:id/records/:rid/correct
GET    /api/v1/locations/:id/records             # 当前有效
GET    /api/v1/locations/:id/records/history     # 含已更正的全部版本
GET    /api/v1/locations/:id/plan
GET    /api/v1/locations/:id/life
GET    /api/v1/locations/:id/plan/as-of?date=YYYY-MM-DD
GET    /api/v1/locations/:id/plan/compare?date=YYYY-MM-DD
```

校验失败返回 `422`（或 `400`），body 形如
`{"error":"validation_failed","field":"crack_length_m","reason":"..."}`。
