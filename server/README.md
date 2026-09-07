# Portfolio CMS

## 本地运行

```bash
npm install
cp server/.env.example .env
npm run dev
```

- 前台：`http://localhost:8787`
- 管理后台：`http://localhost:8787/manage`
- 默认管理员用户名：`admin`

`.env` 必须位于项目根目录。修改 `ADMIN_PASSWORD` 并重启后端后，管理员密码会同步更新。

## 内容与媒体

后台支持站点文案、首屏、关于、经历、项目和作品管理。编辑先保存为草稿，点击发布后才会更新公开的 `/api/site`。图片默认保存到 `DATA_DIR/uploads`，同时保留原图并生成 WebP。

如需腾讯云 COS，请配置 `TENCENT_COS_SECRET_ID`、`TENCENT_COS_SECRET_KEY`、`TENCENT_COS_BUCKET`、`TENCENT_COS_REGION`，然后在后台“设置与备份”中切换存储源。

## VPS 部署

生产环境将 `NODE_ENV` 改为 `production`，把 `DATA_DIR` 指向进程具有写权限的持久化目录。如果前台不与 CMS 同域部署，再设置 `FRONTEND_URL` 供后台的预览按钮使用。使用 PM2 或 systemd 运行 `npm start`，再由 Nginx 反向代理并启用 HTTPS。
