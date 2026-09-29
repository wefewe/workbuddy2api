# wefewe/workbuddy2api

`workbuddy2api` 的自建构建镜像仓库（用于本集群内部部署，非上游官方仓库）。

## 这是什么

上游项目 `workbuddy2api`（原 `github.com/Sliverkiss/workbuddy2api`）的**原作者已删库**。
当前源码由 `ithtelab/workbuddy-manager` 的维护者留存并继续维护，**随其 Release 包分发**
（包内 `upstream/`）。

本仓库：

- `upstream/` —— 上游源码快照（自动同步自 `ithtelab/workbuddy-manager` 最新 Release）；
- `.github/workflows/build.yml` —— 多架构（amd64/arm64）构建并推送
  `ghcr.io/wefewe/workbuddy2api`；
- `.github/workflows/sync-upstream.yml` —— 每日自动拉取上游最新 Release 的 `upstream/`
  并提交（有变化时触发重新构建）。

## 许可

上游为 MIT 许可，版权归原作者，详见 `upstream/LICENSE`。再分发请保留 LICENSE 与版权声明。

## 部署（本集群）

用途：替换集群中已失去更新来源的孤儿镜像
`ghcr.io/sliverkiss/workbuddy2api:latest`，改用本仓库构建的
`ghcr.io/wefewe/workbuddy2api:latest`。

集群侧编排见 `/opt/swarm/stacks/rn/workbuddy.yml`（配合 `ithtelab/workbuddy-manager` 面板）。
