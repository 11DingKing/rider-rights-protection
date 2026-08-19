# RiderGuard - 骑手权益诉求协同服务

RiderGuard 面向新就业群体权益保护检察服务站。骑手或检察联络员登记权益诉求并关联证据后，系统依据生效的分派规则确定承办机构与协作机构，支持法律咨询、心理辅导、劳动报酬、职业安全和平台争议等诉求的受理、转办、会商、升级、回访与审计。超期诉求自动升级，结案结论不可被后续规则覆盖，重复线索按幂等键合并。

- 语言：Go 1.26
- 服务端口：**49660**（HTTP 入口默认监听）
- 存储：分片 JSONL 文件 + SQLite 索引（`modernc.org/sqlite`，纯 Go，无需单独启动数据库进程）

## 系统结构

```
cmd/
  riderguard/        HTTP 服务入口，优雅关闭、信号处理
  riderctl/         运维命令行（init / import / export / reconcile / rebuild-index / diagnose）
internal/
  domain/           领域模型：RightsCase / Rule / Referral / Escalation / AuditEntry / PermanentFailure / ImportBatch
                    显式状态机跃迁表、领域错误、时钟接口
  dispatch/         分办裁定引擎：规则匹配、优先级排序、唯一牵头部门裁定
  service/          业务编排：注册裁定、超期升级、规则版本、批量导入导出、查询统计
  store/            持久化接口（Tx / Store）与报告类型，领域层依赖此接口
  shard/            分片文件读写：按日期分桶、追加写、Sync 落盘、校验和、损坏跳过
  index/            SQLite 索引层：建表迁移、查询、写入、事务
  repo/             持久化实现：shard 写入 + index 事务的复合提交/回滚、重建索引、核对、诊断
  httpapi/          对外接入：路由、请求响应模型、中间件、统一错误格式
  config/           配置解析与校验（YAML + 环境变量覆盖）
  scheduler/        后台周期任务：超期升级巡检、核对巡检、重试退避、死信落盘
  worker/           后台重评估巡检：规则版本演进后，重路由仍处于可重评估状态的待办项
  auditlog/         审计动作常量与审计条目构造
  applog/           结构化日志（zerolog 封装）
migrations/
  0001_init.sql     初始建表（含 schema_version、items、rules、assignments、escalations、audit、failures、batches）
  0002_add_retry_count.sql  为死信表补充重试追踪列（前向兼容）
  embed.go          迁移 SQL 内嵌进二进制
```

分层依赖方向单向：`httpapi → service → dispatch / store(接口) → repo → shard + index`。对外接入层不直接读写存储文件，领域层不依赖 HTTP 与存储实现。

## 数据模型与持久化

核心业务实体落在分片文件（追加写 JSONL，按日期分桶）与 SQLite 索引（查询、事务、唯一约束）中，二者通过业务键关联：

- **权益诉求 items** — 主实体，`external_ref` 唯一（重复报送只登记一次），承载骑手、证据、诉求类别、承办机构、承诺时限和升级层级。
- **分办规则 rules** — 带版本号与生效窗口；新规则生效时旧规则被标记为 `superseded`，已办结事项保留当时规则结论。
- **裁定记录 assignments** — 通过 `item_id` 关联事项、通过 `rule_version` 关联规则；`is_current` 标记当前生效裁定，升级时旧裁定被置为非当前。
- **升级记录 escalations** — 通过 `item_id` 关联事项，记录层级跃迁、新旧牵头部门、新承诺时限。
- **审计 audit_entries** — 通过 `entity_id` 关联任意实体，记录谁在何时做了什么。
- **死信 permanent_failures** — 后台任务重试用尽后的永久失败记录，通过 `entity_id` 关联事项，可人工重投。
- **批量 import_batches** — 按窗口/日期报送批次记录，关联当日事项。

每条记录带 `data_version` 迁移标记。落盘点明确：分片文件 `OpenFile(O_APPEND) + Write + Sync`，索引层走 SQLite 事务 `BeginTx → Commit/Rollback`。`WithTx` 将分片追加写与索引插入包在同一个逻辑事务中，`fn` 返回错误即回滚索引事务，中途失败不留半条记录。

## 配置项

配置通过 YAML 文件加载，环境变量覆盖（前缀 `RIDERGUARD_`）。参考 `config.example.yaml` 与 `.env.example`。

| 配置键 | 环境变量 | 默认值 | 说明 |
|---|---|---|---|
| server.port | RIDERGUARD_SERVER_PORT | 49660 | HTTP 监听端口 |
| server.read_timeout | RIDERGUARD_SERVER_READ_TIMEOUT | 30s | 读超时 |
| server.write_timeout | RIDERGUARD_SERVER_WRITE_TIMEOUT | 30s | 写超时 |
| server.shutdown_timeout | RIDERGUARD_SERVER_SHUTDOWN_TIMEOUT | 15s | 优雅关闭超时 |
| storage.data_dir | RIDERGUARD_STORAGE_DATA_DIR | ./data | 数据目录（分片 + index.db） |
| storage.shard_max_size | RIDERGUARD_STORAGE_SHARD_MAX_SIZE | 1048576 | 单分片文件上限字节，超出轮转 |
| storage.sync_on_write | RIDERGUARD_STORAGE_SYNC_ON_WRITE | true | 每次追加是否 fsync |
| scheduler.escalation_interval | RIDERGUARD_SCHEDULER_ESCALATION_INTERVAL | 30s | 超期升级巡检周期 |
| scheduler.reconciliation_interval | RIDERGUARD_SCHEDULER_RECONCILIATION_INTERVAL | 5m | 核对巡检周期 |
| scheduler.reeval_interval | RIDERGUARD_SCHEDULER_REEVAL_INTERVAL | 1m | 规则版本演进重评估巡检周期 |
| scheduler.max_retries | RIDERGUARD_SCHEDULER_MAX_RETRIES | 3 | 最大重试次数 |
| scheduler.base_backoff | RIDERGUARD_SCHEDULER_BASE_BACKOFF | 1s | 退避基数 |
| scheduler.task_timeout | RIDERGUARD_SCHEDULER_TASK_TIMEOUT | 10s | 单次任务超时 |
| business.default_deadline | RIDERGUARD_BUSINESS_DEFAULT_DEADLINE | 72h | 默认承诺时限 |
| business.escalation_deadline_extension | RIDERGUARD_BUSINESS_ESCALATION_DEADLINE_EXTENSION | 48h | 升级后新承诺时限 |
| business.max_escalation_level | RIDERGUARD_BUSINESS_MAX_ESCALATION_LEVEL | 3 | 最大升级层级 |
| auth.bootstrap_users | RIDERGUARD_AUTH_BOOTSTRAP_USERS | 无默认值 | 首次启动时通过 JSON 注入账号；口令只用于生成 bcrypt 哈希，不写入配置文件 |

## 迁移方式

建表语句随仓库提交于 `migrations/`，通过 `migrations/embed.go` 内嵌进二进制。`index.Open` 启动时按 `schema_version` 表顺序应用未执行迁移并记录版本号。迁移前向兼容、可回滚（旧版本记录读取时保持原语义，`data_version` 字段标记数据版本）。`rebuild-index` 子命令可从分片文件重建索引，损坏分片跳过并上报。

## 启动方法

```bash
# 构建
go build ./...

# 初始化数据目录并写入默认分办规则
go run ./cmd/riderctl init -data-dir ./data

# 启动 HTTP 服务（默认端口 49660）。首次启动必须注入至少一个账号；请在 shell
# 或密钥管理器中提供真实口令，不要把它写入配置文件或提交到 Git。
RIDERGUARD_AUTH_BOOTSTRAP_USERS='[{"id":"u-admin","username":"admin","password":"<strong-password>","role":"admin"}]' \\
  go run ./cmd/riderguard config.example.yaml

# 或用环境变量覆盖端口与数据目录
RIDERGUARD_SERVER_PORT=49660 RIDERGUARD_STORAGE_DATA_DIR=./data go run ./cmd/riderguard
```

服务启动后：

- 存活检查：`GET /healthz` — 进程存活即返回 200。
- 就绪检查：`GET /readyz` — 探测存储 `PingContext`、数据目录可读写、后台调度器已启动，任一不满足返回 503。

## 主要 API

| 方法 | 路径 | 说明 |
|---|---|---|
| POST | /api/items | 登记骑手权益诉求并分派承办机构 |
| GET | /api/items | 分页查询诉求（状态/承办机构/服务站/时间/超期筛选） |
| GET | /api/items/{id} | 事项详情（含裁定、升级、审计链） |
| PATCH | /api/items/{id} | 修改事项信息 |
| POST | /api/items/{id}/start | 开始办理 |
| POST | /api/items/{id}/return | 退回补正 |
| POST | /api/items/{id}/resubmit | 补正后重新提交 |
| POST | /api/items/{id}/cancel | 撤销错登事项 |
| POST | /api/items/{id}/complete | 结案（结案后结论不再改动） |
| POST | /api/rules | 新建分办规则（旧规则自动标记 superseded） |
| GET | /api/rules | 规则版本列表 |
| GET | /api/rules/{version} | 指定版本规则 |
| POST | /api/batches/import | 批量导入（逐条校验，行级结果，失败行不影响成功行） |
| GET | /api/batches/export | 批量导出事项 |
| GET | /api/batches | 批次列表 |
| GET | /api/stats/backlog | 积压与超期统计 |
| GET | /api/audit | 审计记录查询（分页） |
| GET | /api/failures | 死信列表 |
| POST | /api/failures/{id}/retry | 人工重投永久失败任务 |

### 示例请求（端口 49660）

登记事项：

```bash
curl -s -X POST http://localhost:49660/api/items \
  -H 'Content-Type: application/json' \
  -d '{
    "external_ref": "WIN-20260819-0001",
    "title": "企业开办联合办理",
    "description": "营业执照与刻章联合办理",
    "applicant_name": "张三",
    "applicant_contact": "13800000001",
    "materials": ["身份证明.pdf"],
    "category": "企业登记",
    "keywords": ["营业执照", "刻章"],
    "reported_by": "受理员A",
    "service_station_id": "WIN-03"
  }'
```

批量导入（每行一项，行级结果）：

```bash
curl -s -X POST http://localhost:49660/api/batches/import \
  -H 'Content-Type: application/json' \
  -d '{"items":[
    {"external_ref":"B-001","title":"食品经营许可","reported_by":"受理员B","service_station_id":"WIN-03"},
    {"external_ref":"B-002","title":"户外招牌设置","reported_by":"受理员B","service_station_id":"WIN-03"}
  ]}'
```

办结事项：

```bash
curl -s -X POST 'http://localhost:49660/api/items/{id}/complete?actor=牵头部门经办人'
```

查询超期积压：

```bash
curl -s 'http://localhost:49660/api/stats/backlog'
```

## 运维命令行

```bash
go run ./cmd/riderctl init           # 初始化数据目录与默认规则
go run ./cmd/riderctl import -file items.json
go run ./cmd/riderctl export -from 2026-08-01T00:00:00Z -to 2026-08-31T23:59:59Z -out items.jsonl
go run ./cmd/riderctl reconcile       # 分片与索引核对
go run ./cmd/riderctl rebuild-index  # 从分片重建索引（损坏分片跳过上报）
go run ./cmd/riderctl diagnose        # 状态诊断
```

## 测试命令

```bash
go fmt ./...
go vet ./...
go build ./...
go test -timeout=300s -count=1 ./...
go test -race -timeout=420s -count=1 ./...
```

测试覆盖：重启恢复、提交回滚、非法状态转换、并发竞态、幂等重复、规则版本演进、超期升级、分片损坏跳过、分页边界、错误链判定等。时间与时钟可注入，测试不依赖真实等待。

## 失败恢复

服务中断重启后，未办结事项从磁盘分片与索引恢复，已办结事项结论不变。后台任务对超期事项自动升级；重试用尽后写入死信表（含最后错误、尝试次数、时间），重启后不丢，可通过 `/api/failures` 查询与 `/api/failures/{id}/retry` 人工重投。
