# 实施计划

## 目标
修复 Graph 请求等待与摘要重复加载，提供安全配置迁移，并为 fork 仓库增加 GHCR amd64/arm64 镜像构建。

## 阶段
1. 核对运行环境、库接口和改动边界：完成。
2. Graph 超时、线程独立连接池、错误处理、摘要缓存和失效：完成。
3. Docker 持久化、迁移文档、GitHub Actions 多架构发布：完成。
4. 模拟上游回归测试、配置检查和最终审查：完成本地验证。

## 决策
- 保留单个同步 worker；多 worker 和查询并发后续按实测评估。
- 用户确认仅 linux/amd64、linux/arm64。
- 已核对 origin 为 lcyan/office365-manager，部署默认 ghcr.io/lcyan/office365-manager:latest。
- 不直接操作线上容器，不提交或推送 Git，不实际发布镜像。
- 使用标准库 unittest，测试隔离配置并模拟 Graph/MSAL。

## 验收
- 超时不导致未处理异常；错误响应不误报成功。
- 摘要 60 秒内命中缓存；刷新、过期和组织写入正确失效。
- 机密配置不进入 Git 或 Docker 构建上下文。
- PR 不登录或推送 GHCR；默认分支发布 latest；版本标签发布对应版本。

## 验证与交付
- 25 项 unittest 回归通过；actionlint 1.7.12、docker compose config --quiet 和 git diff --check 通过。
- 本地 Python 3.11 验证；Actions 使用与镜像相同的 Python 3.10。
- Docker 引擎未运行，实际 amd64/arm64 构建、镜像启动和 GHCR 发布需要推送后由 Actions 验证。
- 线上配置备份/迁移/重建验证按 README 执行；本次未连接线上。
