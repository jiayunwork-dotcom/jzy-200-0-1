# crackwatch — 门座起重机焊缝裂纹剩余寿命与检查计划服务

管理每个被监测焊缝部位的几何、材料、载荷谱与探伤历史；按断裂力学估算剩余寿命、
用实测探伤数据修正该部位的 Paris 扩展系数，并生成下一次探伤计划。探伤记录为
**只追加（append-only）的事件流**，更正、删除、补录都产生新版本，旧版本永久保留；
剩余寿命与计划始终绑定它所依据的**记录版本 + 材料参数版本 + 载荷谱版本**，
可按任意历史日期重算"当时掌握的数据下"的计划并与今天对比。

技术栈：Go 1.23、Echo v4、PostgreSQL 16（pgx v5）。无前端页面。

## 目录结构（按包划分）

| 包 | 职责 |
|---|---|
| `internal/domain/geometry` | 两种几何的 β(a/W) 修正系数、净截面应力、特征尺寸换算 |
| `internal/domain/material` | 材料参数；临界尺寸求解（断裂韧度 / 净截面屈服，先到为准） |
| `internal/domain/spectrum` | 载荷块（应力幅、最大应力、每天循环数）及校验 |
| `internal/domain/growth` | Paris 定律在变幅谱下的自适应积分；循环数与扩展因子 |
| `internal/domain/calibration` | 由探伤历史反演部位 Paris 系数 C（指数固定），含"未发现"处理 |
| `internal/domain/plan` | 剩余寿命（循环、天数）与安全系数下的下次检查日期 |
| `internal/model` | 持久化实体：版本化材料、部位、谱版本、探伤事件、计划快照 |
| `internal/store` | 存储接口 + 内存实现（测试）+ PostgreSQL 16 实现 + schema |
| `internal/app` | 应用服务：校验、事件版本库、投影、历史查询、材料影响 |
| `internal/httpapi` | Echo 路由与 DTO |

## 断裂力学模型

### 几何与应力强度因子

统一写成

```
K = σ · √(π·a) · β(a/W)
```

其中 `a` 是"应力强度因子表达式里与 π 相乘的特征长度"，API 中录入的裂纹长度
（报告长度，毫米）按几何换算：

- **有限宽板中心穿透裂纹** `center_crack`：报告长度为总裂纹长 2a，`a = 长度/2`，
  几何上界 a→W/2；β 用 Feddersen 正割修正 `β = √sec(πa/W)`，a/W→1/2 时急剧增大。
- **有限宽板边缘裂纹** `edge_crack`：报告长度即 a，几何上界 a→W；β 用 Tada 闭式
  边缘裂纹修正（与 Isida 级数在常用范围内相差约 0.5%），a/W→0 时 β→1.122
  （自由表面系数），全范围单调上升，a/W→1 时发散。

两种 β 都随裂纹增大单调上升，且边缘 β 起始就大于 1（有测试覆盖）。

### 临界尺寸

同时求解两个条件，取先到达者（二分法）：

1. **断裂**：谱中最大块最大应力下 `Kmax(a) = K1c`；
2. **净截面屈服**：中心 `σ_net = σmax·W/(W−2a)`，边缘 `σnet = σmax·W/(W−a)`，达到屈服强度 Sy。

### 扩展积分

Paris 定律 `da/dN = C·(ΔK)^m`，`ΔK = Δσ·√(πa)·β(a)`，单位：C 为 m/循环
（ΔK 用 MPa·√m）。载荷谱每天重复，第 i 块为 nᵢ 个 Δσᵢ 循环。逐块累积后

```
da/dD = C·(√(πa)·β(a))^m · S,   S = Σᵢ nᵢ·Δσᵢ^m,   N = D·Σᵢ nᵢ
```

（一天内裂纹长度变化可忽略，按速率同时处理各块；块的顺序不影响结果。）

**积分方法（按裂纹增量分步 + 解析分段）**：每个步长内把 β 冻结在步中点，
剩余幂律积分有闭式解

```
D = [a^(1−m/2)]_{a0}^{a1} / ((1−m/2)·C·S·π^(m/2)·β^m)     (m=2 时取对数形式)
```

每一步同时按"一个整步（粗）"和"两个半步（细）"计算，用 Richardson 式
细/粗相对差估计冻结 β 的误差，超过局部容差则步长减半，接受后再放大；
累加的是细值（高阶精度）。

**精度/耗时取舍（见验收数据）**：局部容差取 1e-9。对真实两种几何
（含 a/W 接近边界的深裂纹）与 40 万格点 Simpson 独立参考解相比，实测整体
相对误差约 **2×10⁻¹⁰**，比 0.1% 要求高约 6 个数量级；全测试套件（含上百次
计划重算）约 2 秒。β 恒为 1 时闭式分段对任意步长都是精确解——
参考算例的离散误差为机器精度（约 2e-16）。

### 核对算例

β≡1、Δσ=100 MPa、m=3、C=1e-11、a 从 1 mm 到 10 mm：

```
N = 2·(a0^(−1/2) − a1^(−1/2)) / (C·(Δσ·√π)^3) = 7.7663×10⁵ 次循环
```

与"约 7.77×10⁵"一致；应力幅加倍到 200 MPa，因 m=3，循环数严格变为 1/8
（8.0000×）。两条都有测试（`growth_test.go`）。

### 系数修正（指数按材料固定）

至少有两条实测裂纹记录时，首末两次发现给出与录入顺序无关的端点估计：

```
Ĉ = G(a_首, a_末) / (t_末 − t_首)        （G = 单位 C 下的扩展天数）
```

加权平均在等相对误差假设下恰好化为该端点形式，所以**补录一条更早的记录，
结果与当初按日期顺序录入完全相同**（有测试）。少于两条实测时沿用材料表 C。

**"未发现"记录的处理**：只作为单边约束"真实裂纹 ≤ 该方法检出下限 a_d"，
不把它当作"裂纹正好等于 a_d"的测量（那会虚构扩展，最保守，本服务刻意不做），
也不忽略。由前一次实测点 (t₁,a₁) 到该 NDF 点 (t_d,a_d)，"到 t_d 仍未到 a_d"
给出 C 的上界 `C < G(a₁,a_d)/(t_d−t₁)`，取所有这类上界的最小值钳制估计值；
NDF 永不把 C 往上推。若 NDF 下限已小于此前实测裂纹，标记为数据矛盾但保留实测估计。

**对检查间隔的方向**：钳制使 C 变小 → 预测扩展变慢 → 剩余寿命变长 →
检查日期推迟。即：纳入"未发现"比忽略它**更激进（更不保守）**，但不会比
截尾数据允许的程度更激进；完全忽略 NDF 则更保守。两种效应（推迟起算
vs 钳低速率）在计划接口的 `current_is_bound`、`c_was_calibrated`、
`clamped` 相关字段中可追溯。

### 检查计划

从最近一次有效观测出发：剩余循环 = 从当前 a 积分到临界 a_c；剩余天数 =
剩余循环 / 每天总循环数。检查间隔 = 剩余天数 / 部位安全系数（默认 2），
下次检查日期 = max(今天, 最近一次探伤日期) + 间隔。已达临界则立即安排。

## 记录版本库与历史

`inspection_events` 只追加：

- 正常提交：`found`（带长度）或 `not_detected`（带检出下限）；
- 更正：追加一条 `supersedes_id` 指向被更正事件的新记录，旧记录保留；
- 删除/作废：追加一条 `kind=void` 且 `supersedes_id` 指向目标的记录；
- 补录：就是一条 `inspected_at` 更早的普通追加。

投影时先剔除被取代的记录，再按 `(inspected_at, seq)` 排序，因此结果与录入
顺序无关。每次提交/更正/作废都把新计划以
`(部位, 依据事件 seq, 材料版本, 谱版本)` 为键存入 `plan_snapshots`。

- `GET …/plan/at?at=…`：只用 `recorded_at ≤ at` 的事件与当时生效的材料/谱
  版本，重算"当时掌握的数据下"的计划；
- `GET …/plan/compare?at=…`：历史计划与当前计划并列，给出下次检查日期差；
- `GET …/plans`：该部位所有持久化计划快照。

材料/谱按 `effective_at` 生效：历史日期自动取当时有效版本。更新材料参数时
（`POST /materials/:id/versions`）对所有用该牌号的部位，在其它输入（实测数据、
谱）不变的情况下重算旧/新两份计划，返回所有**下次检查日期因此提前**的部位
清单（提前天数）。两名检验员并发提交同一部位：PostgreSQL 用
`SERIALIZABLE` 事务 + 按部位 `pg_advisory_xact_lock` 串行化，内存实现按部位
加锁；两条记录都保留，最终计划与顺序处理一致（有测试）。

## 校验（拒收并指出字段，HTTP 422）

- `length_mm`：实测裂纹长度必须为正且 **小于板宽**；
- 载荷块：`stress_amp` 不得为负、不得大于 2·`stress_max`；
  `cycles_per_day` 不得为负；至少有一个应力幅与循环数都为正的活跃块；
- 材料：`k1c`、`sy`、`m`、`c` 必须为正；
- `not_detected` 记录必须带正的 `detect_limit_mm`（且小于板宽）；
- `inspected_at` 不得早于部位 `commissioned_at`；
- 几何必须是 `center_crack` / `edge_crack`，板宽为正，安全系数为正。

错误体形如 `{"error": "...", "fields": [{"field": "blocks[0].stress_amp", "reason": "..."}]}`。

## 运行

```bash
docker compose up --build
# API: http://localhost:8080   PostgreSQL: localhost:5432
```

本地开发：

```bash
go test ./...                      # 全部单元/集成测试（内存存储，~2s）
CRACKWATCH_TEST_DSN='postgres://crackwatch:crackwatch@localhost:5432/crackwatch?sslmode=disable' \
  go test ./internal/store/        # PostgreSQL 存储集成测试（需 PG16）
go run ./cmd/crackwatch            # 需要 DATABASE_URL 指向 PG16
```

构建镜像在 `golang:1.23-alpine` 中静态编译（CGO_ENABLED=0），运行阶段是
`scratch` 精简镜像（仅 CA 证书 + 二进制，非 root）。schema 在启动时
幂等应用（`internal/store/migrations/schema.sql`）。

## HTTP 接口（前缀 `/api/v1`）

| 方法 | 路径 | 说明 |
|---|---|---|
| POST | `/materials` | 建材料牌号（含版本 1：grade,k1c,sy,m,c） |
| GET | `/materials`, `/materials/:id` | 列表 / 详情（含全部版本） |
| POST | `/materials/:id/versions` | 追加材料版本，返回检查提前影响清单 |
| POST | `/locations` | 建部位（几何、板宽、牌号、安全系数、投用日期、载荷块） |
| GET | `/locations`, `/locations/:id` | 列表 / 详情 |
| PATCH | `/locations/:id` | 改名称 / 安全系数（几何与板宽属结构属性，需新建部位） |
| PUT | `/locations/:id/spectrum` | 追加载荷谱新版本 |
| GET | `/locations/:id/spectrum` | 当前谱版本 |
| POST | `/locations/:id/records` | 提交探伤记录（found / not_detected） |
| POST | `/locations/:id/records/:eventId/correction` | 更正（旧记录保留） |
| POST | `/locations/:id/records/:eventId/void` | 作废（删除） |
| GET | `/locations/:id/records` | 该部位完整追加事件流 |
| GET | `/locations/:id/plan` | 当前剩余寿命与下次检查计划 |
| GET | `/locations/:id/plan/at?at=RFC3339` | 历史日期的计划 |
| GET | `/locations/:id/plan/compare?at=RFC3339` | 历史 vs 当前计划对比 |
| GET | `/locations/:id/plans` | 所有持久化计划快照 |
| GET | `/healthz` | 健康检查 |

### 快速示例

```bash
curl -s localhost:8080/api/v1/materials -H 'Content-Type: application/json' -d '{
  "grade":"Q345","k1c":330,"sy":345,"m":3,"c":1e-11}'

curl -s localhost:8080/api/v1/locations -H 'Content-Type: application/json' -d '{
  "name":"1#门机臂架A缝","geometry":"edge_crack","width_mm":300,
  "material_id":"mat_...","safety_factor":2,
  "commissioned_at":"2024-01-01T00:00:00Z",
  "blocks":[{"stress_amp":100,"stress_max":150,"cycles_per_day":500}]}'

curl -s localhost:8080/api/v1/locations/loc_.../records -H 'Content-Type: application/json' -d '{
  "inspected_at":"2026-01-01T00:00:00Z","method":"MT",
  "kind":"found","length_mm":5,"inspector":"zhang"}'
```

## 单位约定

内部全部使用 SI（裂纹长度 m、应力 MPa、K 为 MPa·√m、C 为 m/循环）；
HTTP 层裂纹长度、板宽、检出下限用毫米，载荷应力用 MPa，时间用 RFC3339 UTC。
