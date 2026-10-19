# 构建本地制品

- 执行：根目录 `pnpm build`，先构建 Vite，再嵌入 Go；同时构建 Python wheel/sdist。
- 启动：根目录 `pnpm start`；默认仅监听 `127.0.0.1:8080`，通过 `AGENTOS_HTTP_ADDR` 显式配置地址。
- 区分：`/api/health` 仅表示进程存活；未配置数据库、身份、模型或生产级就绪检查。
- 限定：当前提供开发制品；生产容器、认证与离线安装包待后续切片实现。
