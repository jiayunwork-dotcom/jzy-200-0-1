# 设计说明（裂纹探伤后端）

## 包结构

| 包 | 职责 |
|---|---|
| `internal/geometry` | 两种几何、β(a/W)、K=σ√(πa)β、物理长度与特征长度换算、净截面应力 |
| `internal/propagation` | Paris 律积分、临界尺寸（断裂/净截面屈服）、恒幅参考例入口、β 感知暴露量 Φ |
| `internal/spectrum` | 载荷块、每日循环数、谱权重 |
| `internal/calibration` | 由探伤历史拟合部位专属 C（m 按材料固定）；“未发现”作为单侧上限约束 |
| `internal/schedule` | 剩余寿命 → 下次探伤日期（除以安全系数）、计划对比 |
| `internal/recordlog` | 记录提交/更正/补录、版本库、历史回放、材料更新影响清单 |
| `internal/store` | Repository 接口、PostgreSQL 16 实现、内存实现（测试） |
| `internal/httpapi` | Echo 路由与 DTO |
| `cmd/server` | 启动、内嵌迁移 |

## 数据模型要点

- `materials` / `material_versions`：牌号下不可变版本（m、C、KIC、σy），每次更新追加新版本。
- `locations`：几何、板宽、牌号、投用日期、安全系数。
- `spectrum_revisions`：载荷谱不可变修订，`blocks` 为 JSONB；拟合暴露量时按修订生效区间分段累加。
- `records`：每次提交/更正都是追加行；`logical_id` 标识同一探伤事件，更正行 `supersedes_id` 指向旧行，旧行置 `superseded_by`，**永不删除**。
- `record_versions`：每部位单调递增的版本号，每次记录变化 +1，绑定触发它的记录。
- `plan_snapshots`：每次重算落一份快照，绑定记录版本、材料版本、谱版本、临界尺寸、拟合 C、下次日期与触发原因。

## 确定性（补录与并发的核心）

任意时刻的计划是下列数据的**纯函数**：

1. 该时刻已存在、且当时仍有效的记录，按 `(探伤日期, 录入时间, id)` 排序；
2. 该时刻生效的材料版本；
3. 该时刻生效的谱修订（拟合暴露量按修订时间点分段）。

C 的拟合（过锚点最小二乘 + 单侧上限）对记录集合是**无序依赖**的，只依赖排序后的探伤日期与暴露量。因此：

- **补录**一条日期更早的记录，最终活动记录集合与“当初按日期顺序录入”完全相同 → C、剩余寿命、下次日期逐位一致（`TestBackfillMatchesChronologicalEntry`）。
- **并发**：同一部位的提交在进程内用每部位互斥串行化，在 Postgres 事务内先 `SELECT ... FOR UPDATE` 锁部位行再追加记录、推进版本号；两条都保留，最终状态只取决于探伤日期集合，与顺序处理一致（`TestConcurrentSubmissions`）。

## 历史查询

`GET /locations/:id/plan/as-of?date=...` 重放：

- 仅取 `created_at <= t` 的记录；若旧记录在 t 时还未被更正（更正行 created_at>t），则旧记录在当时仍有效；
- 材料、谱都取 t 时生效的版本；
- 重算只读，不写快照。
- `.../plan/compare?date=...` 给出当时计划与当前计划的下次日期差（负=提前）及两侧版本号。

## “未发现”记录的处理（保守/激进取向）

见 `internal/calibration` 包注释。结论：

- **只作单侧上限约束**：a(探伤时刻) ≤ 检出限对应的特征长度，绝不当作“实测长度=检出限”；
- 在“过第一条 found 锚点”的线性模型 `Φ(a_k)=Φ(a0)+C·X_k` 下，每条未发现记录给出一个标量上限 `C ≤ (Φ(a限)−Φ(a0))/X`，最终 `C=min(最小二乘 C, 最小上限)`；
- 相对**忽略**未发现记录，它只能把 C 往下压 → 预测扩展变慢 → 间隔变长，即**略微偏激进**；但这正确利用了“裂纹确实没长过检出限”的信息，安全系数仍提供裕度。约束与 found 数据矛盾时取最紧上限，残差 RMS 会暴露数据不一致，供检验员判断。
- 第一条 found 之前的未发现记录：锚点尚未建立，不参与拟合（无 found 基准），仅保留在历史中。

## 校验拒收（返回 422，带字段名）

- 裂纹长度 ≤0 或 ≥ 板宽：`crack_length_m`
- 应力幅 <0 或 >2×最大应力：`blocks[i].stress_amp_mpa`
- 每天循环数 <0：`blocks[i].cycles_per_day`
- 断裂韧度 ≤0（及 m、C、σy 非正）：`fracture_kic_mpa_sqrt_m` 等
- 未发现记录缺检出限：`detection_limit_m`
- 探伤日期早于投用日期：`inspect_date`

## 材料更新影响清单

`POST /materials/:id/versions` 存新版本后，对该牌号全部部位重算并持久化快照（触发原因 `material_update`），返回所有**下次探伤日期因此提前**的部位（旧/新日期、提前天数、两侧材料版本 id）。

## 接口一览

```
POST   /api/v1/materials
GET    /api/v1/materials
GET    /api/v1/materials/:id
POST   /api/v1/materials/:id/versions                 # 返回提前清单
GET    /api/v1/materials/:id/versions
POST   /api/v1/locations
GET    /api/v1/locations
GET    /api/v1/locations/:id
POST   /api/v1/locations/:id/spectrum                 # 新谱修订（不可变）
GET    /api/v1/locations/:id/spectrum/latest
POST   /api/v1/locations/:id/records                  # 提交
POST   /api/v1/locations/:id/records/:rid/correct     # 更正（旧版保留）
GET    /api/v1/locations/:id/records                  # 当前有效记录
GET    /api/v1/locations/:id/records/history          # 含被更正的全部版本
GET    /api/v1/locations/:id/plan                     # 当前计划
GET    /api/v1/locations/:id/life                     # 剩余寿命
GET    /api/v1/locations/:id/plan/as-of?date=YYYY-MM-DD
GET    /api/v1/locations/:id/plan/compare?date=YYYY-MM-DD
GET    /api/v1/health
```

## 运行

```bash
docker compose up --build        # postgres:16-alpine + API:8080，自动建表
# 或
DRIVER=memory HTTP_ADDR=:8080 ./crackstation   # 无数据库演示/测试用，非持久
go test ./...                    # 全部单元/行为/HTTP 测试（无需数据库）
```

## 未实现/可扩展

- 平面应力/平面应变 KIC 区分、焊接残余应力、超载迟滞未建模；
- 几何只支持题目要求的两种，新增几何只需在 `geometry` 包加 Type 与 β；
- 认证鉴权未包含（站内系统，假定置于内网/网关之后）。
