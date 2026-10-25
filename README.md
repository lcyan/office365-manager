# Office 365 多租户管理工具（Go 版）

基于 Go 标准库 `net/http` 的 Web 应用，通过 Microsoft Graph 集中管理多个
Microsoft 365/Office 365 租户的用户、角色和订阅。界面为中文，单二进制部署，
内置模板与静态资源，运行时内存占用约 20-30 MB。

相对旧版（Python/Flask）的主要变化：

- **分页直达**：用户列表改用 Graph `$count + $top/$skip`，页码与内容永远一致。
- **删除更安全**：删除用户先尝试直接删除，仅在权限不足时才移除角色并重试；
  角色移除结果如实反馈，不再出现“删除失败但管理员角色已被剥离”的情况。
- **错误不再静默**：订阅、域名等读取失败会在页面上明确显示原因。
- **安全加固**：argon2id 密码哈希（旧版 SHA-256 首次登录自动升级）、全站
  CSRF 保护、登录限速、`SESSION_SECRET` 缺失拒绝启动、OData/路径注入防护、
  破坏性操作改为 POST + 确认页。
- **新用户临时密码**：仅在专门页面显示一次，不再随 cookie 传播。
- 角色筛选视图支持组/服务主体成员，不再因非用户主体整页报错。

## 🚀 快速开始（Docker Compose）

```bash
cp .env.example .env
# 编辑 .env：为 SESSION_SECRET 填入随机长字符串，例如
#   openssl rand -hex 32
docker compose pull
docker compose up -d
```

访问 `http://<服务器IP>:5000`，首次登录密码为 `admin`，**登录后请立即修改**。
Compose 将 `./data` 挂载到 `/app/data`，`CONFIG_FILE=/app/data/config.json`
持久化配置与租户凭据。

### 环境变量

| 变量 | 必填 | 说明 |
| --- | --- | --- |
| `SESSION_SECRET` | 是 | 会话签名密钥，随机长字符串；缺失时进程拒绝启动。`DEV_MODE=1` 时可用临时密钥本地调试。 |
| `CONFIG_FILE` | 否 | 配置文件路径，默认 `config.json`（容器内 `/app/config.json`）。 |
| `ADDR` | 否 | 监听地址，默认 `:5000`。 |
| `COOKIE_SECURE` | 否 | HTTPS 反向代理后设为 `1`。 |

### 从旧版（Python）迁移

无需手动操作：检测到旧版 `config.json`（无 `version` 字段）时自动迁移——
组织原样保留；旧密码哈希在**首次成功登录后**自动升级为 argon2id 并写回。
建议迁移前照常备份 `data/config.json`。

## 🛠 本地开发

```bash
go vet ./...
go test ./...            # 全部测试使用伪 Graph/会话，不访问真实租户
DEV_MODE=1 go run ./cmd/server
```

模板与静态资源位于 `assets/`，通过 `go:embed` 打包进二进制；配置目录下的
`sku_product_mapping.json` 可覆盖内置 SKU 产品名映射，无需重新编译。

项目结构：

```
cmd/server        入口（装配依赖、启动 HTTP）
internal/config   配置加载/校验/原子写入/旧版迁移
internal/auth     argon2id 哈希、旧哈希迁移、登录限速
internal/graph    Graph 客户端：令牌缓存、OData 转义、类型化错误、分页
internal/web      会话、中间件、模板渲染、全部页面 handler
internal/background  后台重试任务（带结构化日志）
internal/licenses SKU 产品名映射与状态标签
assets            模板 / 静态资源 / SKU 映射（go:embed）
```

## ⏱ Graph 访问与缓存

- 请求超时 20 秒；令牌（client credentials）按组织缓存并提前 60 秒刷新，
  收到 401 时丢弃缓存并重试一次。
- 组织概况缓存 60 秒；任何写操作完成后立即失效该组织缓存。
- 角色分配查询跟随全部分页（`$top=999`），超过 100 条不会截断。
- 日志只记录资源类别、方法、状态与耗时，不含令牌、ID 或响应体。

## 🔐 使用注意事项

* 需要相应的 Microsoft Entra 权限（Directory.ReadWrite.All、
  User.ReadWrite.All、User.PasswordProfile.ReadWrite.All 等）。
* 生产环境建议置于 HTTPS 反向代理之后，并设置 `COOKIE_SECURE=1`。
* `data/config.json` 包含各租户客户端密钥，注意目录权限与备份安全。
* 配置写入为原子操作（临时文件 + rename），进程崩溃不会损坏配置文件。
