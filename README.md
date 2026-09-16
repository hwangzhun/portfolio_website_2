# 个人作品集网站

Go 服务在同一个 origin 下提供前台、CMS 与 API。前端资源编译进单一静态二进制；SQLite 数据库和本地上传媒体保存在 `DATA_DIR`，Docker 部署时固定为命名卷中的 `/app/data`。

## 本地开发

需要 Go 1.25 或更高版本：

```bash
cp server/.env.example .env
set -a && . ./.env && set +a
go run .
```

打开 `http://localhost:8787`，后台地址为 `http://localhost:8787/manage`。默认管理员用户名是 `admin`；修改 `ADMIN_PASSWORD` 并重启后，管理员密码会同步更新。

运行测试：

```bash
go test ./...
```

## Docker 构建与部署

最终镜像是 `linux/amd64` Scratch 镜像，仅包含静态 Go 服务和 CA 根证书：

```bash
docker build --platform linux/amd64 -t hwangzhun/portfolio_website_2:latest .
docker image inspect hwangzhun/portfolio_website_2:latest --format '{{.Size}}'
```

`compose.yaml` 默认从 Docker Hub 拉取该镜像。部署前至少将 `ADMIN_PASSWORD` 改为强密码，然后启动：

```bash
docker compose pull
docker compose up -d
```

站点默认监听宿主机 8787 端口。由 1Panel、Nginx、Caddy 或云负载均衡将 HTTPS 流量反向代理到该端口；应用本身只提供 HTTP。

容器以 UID/GID `1000:1000` 运行，并保留同 UID 的 `node` 用户别名及旧 `docker-entrypoint.sh` 兼容入口，可适配 1Panel 在升级时沿用旧 Node 容器配置的行为。兼容入口仍是同一个静态 Go 二进制，不包含 shell 或 Node。健康检查由 `/app/portfolio healthcheck` 执行，Scratch 镜像中没有 curl 或包管理器。

## 数据升级、备份与恢复

Go 服务直接复用原来的 `/app/data/portfolio.sqlite`、WAL/SHM 文件和上传目录，不需要导入或转换。首次升级前仍建议停止服务并备份数据卷：

```bash
docker compose stop
docker run --rm -v portfolio_website_portfolio_data:/data -v "$PWD":/backup busybox tar czf /backup/portfolio-data.tar.gz -C /data .
docker compose start
```

恢复到空数据卷时：

```bash
docker compose stop
docker run --rm -v portfolio_website_portfolio_data:/data -v "$PWD":/backup busybox sh -c 'rm -rf /data/* /data/.[!.]* /data/..?*; tar xzf /backup/portfolio-data.tar.gz -C /data'
docker compose start
```

若 Compose 项目名不同，请将卷名替换为 `docker volume ls` 中的实际名称。不要在升级时执行 `docker compose down -v`。

## 内容与媒体

后台支持站点文案、首屏、关于、经历、项目、作品、草稿发布、历史版本和审计日志。上传支持 JPG、PNG 和 WebP，限制 12 MiB；服务会校正 EXIF 方向、缩放到最大 2400×2400，并生成质量 82 的 WebP。

图片默认保存到数据卷。配置 `TENCENT_COS_SECRET_ID`、`TENCENT_COS_SECRET_KEY`、`TENCENT_COS_BUCKET` 和 `TENCENT_COS_REGION` 后，可以在后台切换新上传媒体到腾讯云 COS；SQLite 与 CMS 内容仍保留在 Docker 卷中。
