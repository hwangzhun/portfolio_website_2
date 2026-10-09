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

「SEO 设置」中可上传或从媒体库选择网站图标。源图需为不小于 512×512 的正方形图片；发布后服务会自动生成 favicon、Apple Touch 图标和 Web App Manifest 的标准 PNG 尺寸。

图片默认保存到数据卷。配置 `TENCENT_COS_SECRET_ID`、`TENCENT_COS_SECRET_KEY`、`TENCENT_COS_BUCKET` 和 `TENCENT_COS_REGION` 后，可以在后台切换新上传媒体到腾讯云 COS；SQLite 与 CMS 内容仍保留在 Docker 卷中。

如果 COS 已绑定自定义源站或 CDN 域名，设置 `TENCENT_COS_CUSTOM_DOMAIN=https://media.example.com`。也可只填域名，程序会默认使用 HTTPS。该配置会同时应用于新上传媒体和数据库中已有的 COS 媒体 URL，无需重新上传。旧的 `TENCENT_COS_CDN_URL` 仍可使用，但新变量的优先级更高。

程序上传的 COS 对象默认继承存储桶权限，不会自动改为对象级公有读。前台 API、后台媒体预览和 SEO 标签会在请求时使用 COS SecretID/SecretKey 生成短期 GET 签名 URL；数据库仍只保存稳定对象地址和 key，不会持久化过期签名。可通过 `TENCENT_COS_SIGNED_URL_TTL=1h` 调整有效期，范围为 1 分钟至 24 小时。包含签名 URL 的前台 JSON 与 HTML 响应使用 `Cache-Control: private, no-store`，避免 CDN 缓存过期链接。

## 腾讯云 VOD 视频播放

站内视频通过腾讯云 TCPlayer 5.3.4 播放，不接受 MP4/HLS 直链。在腾讯云控制台准备 AppID、默认分发配置中的播放密钥，并申请、绑定正式站点域名的 Web 基础版 License。然后配置：

```bash
TENCENT_VOD_APP_ID=1250000000
TENCENT_VOD_PLAYBACK_KEY=replace-with-playback-key
TENCENT_VOD_LICENSE_URL=https://license.vod2.myqcloud.com/license/v2/...
TENCENT_VOD_SIGNATURE_TTL=10m
```

播放密钥只在 Go 服务端用于生成短时 `psign`，不会返回浏览器。在后台的视频作品中填写媒资管理页面显示的 FileID，站点会使用 `appID + fileID + psign + licenseUrl` 初始化 TCPlayer。

从旧版升级前请先导出 CMS 备份。旧 `videos` 表中的 `file_id` 会自动迁移；原来保存的 `videoUrl` 无法可靠反推 FileID，升级后需在 CMS 中逐条补录，否则该视频不能发布或播放。

前台的 VOD 配置请求失败、TCPlayer SDK 错误与图片加载失败会回传到 CMS 的「系统日志」。每个 HTTP 请求都有 `X-Request-ID`，播放配置请求可以用该 ID 与浏览器错误串联。记录包含方法、路径、状态码、响应大小、耗时、IP、User-Agent 和 Referer；URL 查询参数会被移除（仅保留视频页的作品 ID），`psign`/token 会脱敏，不会记录播放密钥。

终端标准输出会记录所有 HTTP 访问。SQLite 默认不保存成功的 `/assets/`、`/uploads/` 和 `/api/health` 请求，但会保存它们的 4xx/5xx 失败；如需保存全量记录，设置 `ACCESS_LOG_STATIC=true` 或 `ACCESS_LOG_HEALTH=true`。数据库保留最新 10,000 条。反向代理部署可设置 `TRUST_PROXY=true` 以从 `X-Forwarded-For`/`X-Real-IP` 读取真实 IP；只有代理会清理并重写这些请求头时才应开启。
