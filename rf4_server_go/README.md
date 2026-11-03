# RF4 Go 后端

当前版本：`0.1.0`
客户端版本：`4.0.25034`
更新日期：`2026-11-03`

这是俄罗斯钓鱼 4 本地私服后端的 Go 实现。项目采用单进程结构，登录网关、TCP 游戏通道和业务处理都在 `rf4server` 中运行，玩家运行数据以 MySQL 为唯一真值。

> 本项目用于协议研究、学习和技术交流，不是完整生产级私服，也不包含管理后台、权限系统或多用户运营管理。

## 核心原则

1. **运行时只读真实 MySQL。** 钱包、技能、背包、鱼护、装备、快捷栏、商城和流水都以数据库为准。
2. **运行时不发送抓包快照。** 协议构造器按当前玩家和静态定义实时生成响应；录制数据只能作为测试答案。
3. **静态定义与玩家数据分离。** 协议字段表、地图与配置块只读；玩家状态每次查询数据库。
4. **未实现命令必须明确回错。** 不允许让客户端无限等待。

## 已完成功能

- 登录器 HTTP 网关：`login.php`、`login_token.php`，兼容 `7515` 入口。
- TCP 首包、token 认证、Hermes 握手、RC4、帧序号、ACK、心跳和断线诊断。
- 登录票持久化到 MySQL，后端重启后旧登录票仍可续期。
- 玩家进场、档案、状态推送、地图、天气和 9 个游戏配置块。
- MySQL 玩家档案、钱包、背包、鱼护、装备、容器、快捷栏和技能树。
- 背包 `4/1`、`4/4`、`4/5` 按容器分别读取，修复重登物品消失和鱼护 `17/0` 问题。
- 商城货架、库存、金币/银币价格、购买扣款、物件生成和购买流水。
- 装配、拆卸、全部拆卸、钓组零件和快捷栏同步。
- 鱼护真实记录读取、容量 `N/100/150`、鱼卡片重建和渔获流水。
- 按客户端当前地图从 MySQL 名册随机出鱼，支持切图。
- 铲子、红虫长柄勺挖饵：不同熟练度阶段使用不同概率表，产物真实进背包。
- 挖饵和制作鱼饵每次增加 `0.1%`「捕获鱼饵」熟练度，最高 `100%`。
- 鱼饵制作：按 MySQL 配方解析，每次固定产出 `30` 个，优先堆叠到现有物品。
- 只读 HTTP 查询接口和分类日志；IDEA 控制台同步显示日志。

详细功能、启动步骤、数据库说明和已知边界见 [交付说明书.md](交付说明书.md) 与 [docs/mysql_schema.sql](docs/mysql_schema.sql)。

## 快速开始

前置条件：

- Windows x64
- Go `1.27.3+`
- MySQL `8.0+`
- 已导入 `docs/mysql_schema.sql` 和项目所需静态基础数据

构建并启动：

```powershell
.\build.ps1
.\start-server.ps1
```

默认连接参数：

```text
MySQL: 127.0.0.1:3306 / rf4 / rf4
用户名: rf4
密码: rf4pass

游戏 TCP: 19213
HTTP 网关: 19214
客户端兼容 HTTP: 7515
```

参数不同时：

```powershell
.\start-server.ps1 -MyHost 127.0.0.1 -MyPort 3306 `
  -MyUser rf4 -MyPass rf4pass -MyDb rf4
```

本地示例账号：`return2` / `rf4demo`。示例只用于脱敏联调，正式使用请替换为自己的账号记录。

## IDEA 启动

1. 用 IDEA 打开 `rf4_server_go` 目录。
2. 运行入口：`cmd/rf4server/main.go`。
3. 工作目录设为 `rf4_server_go`。
4. 环境变量设置 `RF4_DATA_DIR=<绝对路径>/data`。
5. 程序参数按需填写：

```text
--http-port 19214 --http-compat-port 7515 --tcp-port 19213
--mysql-host 127.0.0.1 --mysql-port 3306
--mysql-user rf4 --mysql-pass rf4pass --mysql-db rf4
--logs logs
```

启动前确认旧的 `rf4server` 或 Node 后端没有占用 `19213`、`19214`、`7515`。运行后 IDEA 控制台会实时显示日志，同时写入 `logs/`。

## 测试

```powershell
go test ./...
go vet ./...
go build ./...
```

`go test ./...` 默认会运行可用的 MySQL 测试；连接不上 MySQL 时相关测试会自动跳过。历史迁移对账默认关闭，需要时设置：

```powershell
$env:RF4_LIVE_SNAPSHOT_AUDIT = '1'
go test ./internal/gamestore
```

这只是人工迁移审计，不是运行时快照。

## 数据与隐私

- `data/db.json` 是脱敏示例账号。
- `data/rpc_table_real.json` 是裁剪后的协议测试样本，只供 `_test.go` 与离线工具使用。
- 不包含真实玩家机器码、IP、管理员数据、抓包回放或 MySQL 业务库导出。
- 不包含用户管理、管理员鉴权、封禁、邀请、CDK 等运营功能。

## 目录

```text
cmd/rf4server/       主后端入口
internal/gateway/    登录网关
internal/gamesess/   TCP 会话与 RPC 路由
internal/gamestore/  MySQL 数据入口
internal/inventory/  背包与容器清单
internal/fishing/    钓鱼与鱼护
internal/baitdig/    挖饵
internal/craft/      鱼饵制作
internal/shop/       商城
internal/buy/        购买
internal/skilltree/  技能树
data/                脱敏示例账号与静态定义
docs/                MySQL 建表脚本
bin/                 构建产物，不提交
logs/                运行日志，不提交
```

## 已知边界

- `14/12` 搏鱼/收线细节、`1/14` 旅行中的部分快照结构仍未完全实现。
- 部分手具、钓组细分命令和低频社交命令仍会返回明确错误回执。
- 经济、鱼群、生态和赛事机制不是完整官方复刻。
- 本后端未实现管理后台、多用户运营管理、封禁、邀请、CDK、邮件等后台功能。
