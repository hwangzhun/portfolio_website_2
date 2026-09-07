# 个人作品集网站

Express 在同一个 origin 下提供前台、CMS 与 API；不再依赖 Parcel。SQLite 数据库和本地上传媒体都保存在 `DATA_DIR`，Docker 部署时固定为命名卷中的 `/app/data`。

## 本地开发

```bash
npm install
ADMIN_PASSWORD=change-me npm run dev
```

打开 `http://localhost:8787`，后台地址为 `http://localhost:8787/manage`。`npm run dev` 使用 Node 的 watch 模式，修改服务端代码后会自动重启；前端静态文件刷新浏览器即可看到变化。

## Docker 部署

`compose.yaml` 已包含部署所需的全部环境变量，并直接使用 Docker Hub 镜像。部署前至少将 `ADMIN_PASSWORD` 改为强密码；如果需要修改宿主机端口，将 `8787:8787` 左侧的端口改掉即可。

```bash
docker compose up -d
```

站点默认监听宿主机 `8787` 端口。由 1Panel、Nginx、Caddy 或云负载均衡将 HTTPS 流量反向代理到该端口；应用本身只提供 HTTP。

运行数据位于 Docker 命名卷 `portfolio_data`：SQLite 主库、WAL/SHM 文件和本地上传目录均在其中。不要把数据库或上传文件写进镜像，也不要在升级时使用会删除卷的 `docker compose down -v`。

## 备份、恢复与升级

内容级备份可在 CMS 的“设置与备份”中导出 JSON。完整备份应在停止服务后归档数据卷：

```bash
docker compose stop
docker run --rm -v portfolio_website_portfolio_data:/data -v "$PWD":/backup busybox tar czf /backup/portfolio-data.tar.gz -C /data .
docker compose start
```

恢复到一个空的数据卷时，先停止服务，再解压备份，最后启动服务：

```bash
docker compose stop
docker run --rm -v portfolio_website_portfolio_data:/data -v "$PWD":/backup busybox sh -c 'rm -rf /data/* /data/.[!.]* /data/..?*; tar xzf /backup/portfolio-data.tar.gz -C /data'
docker compose start
```

若 Compose 项目名不同，请将上述 `portfolio_website_portfolio_data` 替换为 `docker volume ls` 中实际的卷名。升级时使用 `docker compose pull && docker compose up -d`；该操作会更新容器但保留数据卷。

## 媒体存储

默认上传图片保存到持久化卷，原图与 WebP 派生图都会保留。填写 `compose.yaml` 中的腾讯云 COS 参数后，可以在后台切换新上传媒体到 COS；SQLite 与 CMS 内容仍保留在 Docker 卷中。
