# Office 365 多租户管理工具

这是一个基于 Flask 和 MSAL 的 Web 应用，用于集中管理多个 Microsoft 365/Office 365 租户中的用户、角色和订阅。界面支持中文，具备权限管理、角色分配、订阅管理、组织切换等功能。

## ☁️ 使用自己 fork 仓库的 GHCR 镜像

`.github/workflows/docker.yml` 使用 GitHub Actions 构建并发布到 **`ghcr.io/lcyan/office365-manager`**。工作流从当前 GitHub 仓库自动生成镜像地址，名称转小写，后续再次 fork 也能复用。镜像同时包含 **linux/amd64 和 linux/arm64**，Docker 自动选择主机架构。

1. 在自己的 fork 中启用 Actions。工作流使用自带的 `GITHUB_TOKEN` 和 `packages: write` 权限，无需 Docker Hub 账号或额外发布 Token；组织策略也需要允许发布包。
2. 推送默认分支后运行测试并发布 `latest` 和 `sha-<短提交号>`。推送 `v1.0.0` 等版本标签会发布 `1.0.0` 和 SHA 标签，不覆盖 `latest`。PR 运行测试和双架构构建，不登录或推送。Actions 手动运行时，只有选中默认分支才发布。
3. 首次发布后检查仓库 Packages。匿名拉取需在包设置中将可见性设为 Public；私有包需在部署主机执行 `docker login ghcr.io`，使用有 `read:packages` 权限的凭证。复用已有包时需授予此仓库 Actions 对该包的访问权。
4. 新部署复制配置示例：

   ```bash
   cp .env.example .env
   ```

   编辑 `.env`：`IMAGE` 已设置为 `ghcr.io/lcyan/office365-manager:latest`，为 `FLASK_SECRET_KEY` 填入随机长字符串。生产部署可使用版本标签或镜像摘要固定版本。

   ```bash
   docker compose pull
   docker compose up -d
   ```

访问 `http://<服务器IP>:5000`，首次登录密码为 `admin`，登录后立即修改。Compose 将 `./data` 挂载到 `/app/data`，使用 `CONFIG_FILE=/app/data/config.json` 持久化配置；没有文件时才初始化。**已有部署先执行下方迁移步骤，不要直接重建旧容器。**

## 🛠 项目特点

- **多租户支持**：通过网页添加多个组织（租户）的应用程序 ID、目录 ID 及密钥，并在用户管理界面方便地切换不同组织进行操作。
- **权限安全**：系统使用登录密码保护，除拥有密码的管理员外无法访问。密码采用哈希形式存储在 `config.json` 文件中，登录成功后可在“修改密码”页面修改。
- **用户管理**：
  - 支持分页查询所有用户。
  - 用户列表显示显示名称、**姓、名**、登录名、角色等信息，可根据“全局管理员”或“特权角色管理员”筛选，并显示当前页用户的角色状态。
  - 支持添加用户。
  - 查看单个用户的详情，包括角色列表、订阅（许可证）以及账户启用状态。订阅部分同时显示 **SKU 名称和对应的产品名称**。
  - 支持修改用户密码、启用/禁用账户，并在下拉框中切换角色（普通用户 / 全局管理员 / 特权角色管理员）。。
  - 提供删除用户功能。
  - 订阅分配支持 **多选及撤销**：在分配订阅页面按产品名称和 SKU 列出所有租户内的许可，显示已分配/总数量、可用数量、状态和续订到期日期。
  - **组织概况**：在查询用户列表前，先显示租户概要信息，包括全局管理员数、特权管理员数、总用户数以及各订阅 SKU 的许可使用情况。许可信息除了 **产品名称** 和 **SKU 名称** 外，还显示 **已分配/总数量**、**可用许可证数**、**订阅状态** 以及 **续订到期日期**（若可获取）。
- **API 管理与检测**：组织管理界面可测试单个或全部组织的 API 配置是否可用，结果以颜色标识呈现；必要时可批量导入组织配置。
- **中文界面 & 自适应设计**：界面采用简洁的中文文本，配合自定义 CSS 样式，适用于桌面与移动浏览器。

## 📦 源码部署与本地构建

若您希望查看或修改源码，也可自行构建镜像运行：

1. 下载并解压本项目源码（包括 `Dockerfile`、`app.py`、模板与静态资源）。
2. 在项目根目录执行构建命令：

   ```bash
   docker build -t office365-manager .
   ```

3. 构建后运行容器，先设置 `FLASK_SECRET_KEY` 环境变量：

   ```bash
   mkdir -p data
   docker run -d -p 5000:5000 --name office365-manager \
     -e FLASK_SECRET_KEY -e CONFIG_FILE=/app/data/config.json \
     --mount type=bind,src="$(pwd)/data",dst=/app/data office365-manager
   ```

   默认密码同样是 `admin`，请登陆后及时修改。

4. （可选）**自定义许可映射**：更新 `sku_product_mapping.json` 后重新构建镜像；外部映射覆盖内置产品名称。

未设置 `CONFIG_FILE` 时仍兼容 `/app/config.json`。旧文档中的 `/usr/src/app/office365-manager/config.json` 不是本镜像读取的路径。继续使用单文件挂载到 `/app/config.json` 时必须提前放入有效 JSON；推荐使用目录挂载。

## 📥 已有容器的配置迁移与回滚

以下在 Linux 部署主机执行，将 `office365-manager` 替换为旧容器名。迁移期间停止页面配置编辑，等待后台任务完成。不要先删除或重建旧容器，宿主机空文件不能代替容器内配置。

1. 从旧容器备份并验证（主机需有 Python 3），不输出凭证：

   ```bash
   set -eu
   umask 077
   backup_dir="backups/$(date +%Y%m%d-%H%M%S)"
   mkdir -p "$backup_dir" data
   docker cp office365-manager:/app/config.json "$backup_dir/config.json"
   python3 - "$backup_dir/config.json" <<'PY'
   import json, sys
   with open(sys.argv[1], encoding='utf-8') as stream:
       config = json.load(stream)
   assert isinstance(config, dict)
   assert isinstance(config.get('password_hash'), str) and config['password_hash']
   assert isinstance(config.get('organizations'), list)
   for org in config['organizations']:
       assert isinstance(org, dict)
       assert all(isinstance(org.get(k), str) and org[k]
                  for k in ('id', 'name', 'client_id', 'tenant_id', 'client_secret'))
   print('配置验证通过，组织数：', len(config['organizations']))
   PY
   ```

   组织数量不符时停止迁移并确认容器。旧部署设置了 `CONFIG_FILE` 时按实际路径备份。保留独立备份并限制访问权限。

2. 验证通过后，在同一 shell 中备份宿主机原文件，再安装配置：

   ```bash
   if [ -e data/config.json ]; then
     cp data/config.json "$backup_dir/config-host-before.json"
   fi
   cp "$backup_dir/config.json" data/config.json
   chmod 600 data/config.json "$backup_dir/config.json"
   ```

3. 按快速启动说明设置 `.env`。使用独立 Compose 项目名保留旧容器供回滚：

   ```bash
   docker compose -p office365-ghcr pull
   docker stop office365-manager
   docker compose -p office365-ghcr up -d
   docker compose -p office365-ghcr logs --tail=50
   ```

4. 用原密码登录，核对组织数量与 API 状态。执行 `docker compose -p office365-ghcr up -d --force-recreate` 后再次验证持久化，再考虑清理旧容器。

   如启动失败或配置不符，执行 `docker compose -p office365-ghcr stop`，再执行 `docker start office365-manager` 恢复原容器。回滚前备份新服务配置；新服务上线后的修改不会自动回写旧容器。

空文件、无效 JSON 或缺少必需字段会明确报错并保留原文件，不会自动重置配置。`.gitignore` 排除配置、`.env`、数据和备份；Docker 构建上下文只允许应用代码与静态资源。

## 🧪 本地开发与验证

```bash
python -m venv .venv
# Linux/macOS: source .venv/bin/activate
# PowerShell: .\.venv\Scripts\Activate.ps1
python -m pip install -r requirements.txt
python -m unittest discover -s tests -v
python -m flask --app app run --debug --port 5000
```

测试使用临时配置和模拟 Graph/MSAL，不访问真实租户。Flask 已更新到 3.1.3，以兼容当前 Werkzeug。

## ⏱ Graph 请求与缓存

- Graph 和 MSAL 连接/读取超时为 `(5, 15)` 秒。Graph 使用线程独立 Session 复用连接，不新增自动重试；MSAL 保留库内置的连接重试。
- 成功概况按组织缓存 60 秒，点击“刷新概况”强制更新。数据不完整时展示提示，不缓存为完整结果；缺失统计显示“—”。
- Graph 写操作完成或超时后使该组织缓存失效，后台操作同样生效；编辑组织也清除令牌和角色定义缓存。
- 日志记录 Graph 资源类别、状态、耗时和令牌获取耗时，不记录认证头、请求体或查询参数。
- 首次加载仍串行执行，单次超时不是整页期限。保留单个同步 worker，避免多进程配置不同步，后续依据实测决定是否调整。

发布后用 `docker buildx imagetools inspect ghcr.io/lcyan/office365-manager:latest` 核对 amd64/arm64 清单，并在两种架构上验证启动和 `/login`。

## 🔐 使用注意事项

* 本工具仅在拥有相应 Microsoft Entra 权限（如 Directory.ReadWrite.All、User.ReadWrite.All、User.PasswordProfile.ReadWrite.All、User.EnableDisableAccount.All 等）时才能正确调用 Graph API。请确保您创建的应用已经分配了这些权限，并在组织管理页面录入正确的应用 ID、目录 ID 和密钥。对于删除用户、重置密码等操作，系统会根据 Graph 返回的错误信息直接反馈，例如提示密码复杂度不符合要求或权限不足，便于排查。
* 生产环境建议通过 HTTPS 部署，并调整 `FLASK_SECRET_KEY` 环境变量增强会话安全。
* 默认密码为 `admin`，务必在首次登录后及时修改。
* 本项目为演示性质，实现逻辑简洁明了，未考虑大规模并发或复杂的错误处理，请根据业务需求自行完善。

祝使用愉快！
