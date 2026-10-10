# 核查记录

- app.py 使用 requests.request，无 timeout；MSAL 构造也未显式设置 timeout。
- 摘要正常冷加载 7 次 Graph 请求，角色缓存命中后 5 次；存在后台角色分配和删除线程。
- config 在导入时加载，多个 worker 不会同步组织/密码修改。
- Docker 工作目录 /app；README 挂载路径错误；本地无 Compose、CI 或测试。
- Graph 错误以 dict 返回，多个调用方只判断 None，存在误报与空值异常。
- 本地未提供线上容器或 Caddy 连接，不能验证线上部署和时延。
- Context7 工具不可用，改为核对包源码及官方资料。
- MSAL 1.25.0 的 timeout 参数传给内部 requests Session，支持连接/读取二元组；库自带一次连接重试，不等同于整页期限。
- 已读取 docker/build-push-action v6 和 metadata-action v5 官方 README，核对平台、标签及发布参数。
- 新装 Flask 2.3.2 搭配 Werkzeug 3.1.9 时 test_client 抛出 __version__ AttributeError；升级 Flask 3.1.3 以兼容，其他直接依赖保持不变。
- 本地 Docker Desktop Linux 引擎未运行，不能执行镜像构建或容器验收。
- 已核对 origin 所有者 lcyan，示例和 Compose 默认镜像均使用 ghcr.io/lcyan/office365-manager:latest。
- 角色写入原先忽略返回值，会在新增失败时继续删除另一角色并误报成功；已检查结果并在失败时中止后续移除。
- 25 项回归覆盖超时、认证异常、403/429/500、缓存命中/失效/组织隔离、配置保存重载及写操作失败。
- Actionlint 官方 Windows 文件格式为 zip；校验和 HTTP 响应为 byte[]，需 UTF-8 解码后比较。已验证 SHA256 并运行检查通过。
