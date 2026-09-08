require("dotenv").config();
const crypto = require("node:crypto");
const fs = require("node:fs");
const path = require("node:path");
const express = require("express");
const helmet = require("helmet");
const bcrypt = require("bcryptjs");
const rateLimit = require("express-rate-limit");
const multer = require("multer");
const sharp = require("sharp");
const COS = require("cos-nodejs-sdk-v5");
const { normalizeUploadFilename } = require("./filename");
const {
  db,
  dataDir,
  getDocument,
  saveDraft,
  publishDraft,
  listRevisions,
  restoreRevision,
  deleteRevision,
  createAdminLog,
  listAdminLogs,
  getSetting,
  setSetting,
  createMedia,
  listMedia,
  getMedia,
  deleteMediaRecord,
  cleanSessions,
} = require("./db");

const app = express();
const port = Number(process.env.API_PORT || process.env.PORT || 8787);
const projectRoot = path.join(__dirname, "..");
const uploadDir = path.resolve(
  process.env.UPLOAD_DIR || path.join(dataDir, "uploads"),
);
fs.mkdirSync(uploadDir, { recursive: true });
app.use(
  helmet({
    contentSecurityPolicy: false,
    crossOriginResourcePolicy: { policy: "cross-origin" },
  }),
);
app.use(express.json({ limit: "2mb" }));
app.use(
  "/uploads",
  express.static(uploadDir, { maxAge: "30d", immutable: true }),
);
app.use(
  "/assets",
  express.static(path.join(projectRoot, "assets"), { maxAge: "7d" }),
);
app.use(
  "/api/admin",
  rateLimit({
    windowMs: 15 * 60 * 1000,
    limit: 400,
    standardHeaders: true,
    legacyHeaders: false,
  }),
);

const upload = multer({
  storage: multer.memoryStorage(),
  limits: { fileSize: 12 * 1024 * 1024, files: 1 },
  fileFilter(_req, file, cb) {
    cb(null, ["image/jpeg", "image/png", "image/webp"].includes(file.mimetype));
  },
});
const hashToken = (token) =>
  crypto.createHash("sha256").update(token).digest("hex");
function cookieValue(req, name) {
  const match = (req.headers.cookie || "")
    .split(";")
    .map((v) => v.trim())
    .find((v) => v.startsWith(`${name}=`));
  return match ? decodeURIComponent(match.slice(name.length + 1)) : "";
}
function requireAdmin(req, res, next) {
  cleanSessions();
  const token = cookieValue(req, "portfolio_session");
  const session =
    token &&
    db
      .prepare(
        "SELECT username,expires_at FROM admin_sessions WHERE token_hash=?",
      )
      .get(hashToken(token));
  if (!session || session.expires_at < Date.now())
    return res.status(401).json({ error: "需要管理员登录" });
  req.admin = session.username;
  next();
}
function validateContent(value) {
  return (
    value &&
    typeof value === "object" &&
    value.profile &&
    value.hero &&
    value.about &&
    Array.isArray(value.experiences) &&
    Array.isArray(value.caseStudies) &&
    Array.isArray(value.projects)
  );
}
function projectValidationIssues(content) {
  return (content.projects || [])
    .filter(
      (item) =>
        item.published !== false &&
        ((item.type === "video" && !item.videoUrl) ||
          (item.type === "photo" && !item.externalUrl)),
    )
    .map(
      (item) =>
        `${item.title || "未命名作品"}缺少${item.type === "video" ? "视频地址" : "外部链接"}`,
    );
}
function cosConfig() {
  const required = [
    "TENCENT_COS_SECRET_ID",
    "TENCENT_COS_SECRET_KEY",
    "TENCENT_COS_BUCKET",
    "TENCENT_COS_REGION",
  ];
  const missing = required.filter((key) => !process.env[key]);
  return {
    ready: !missing.length,
    missing,
    bucket: process.env.TENCENT_COS_BUCKET || "",
    region: process.env.TENCENT_COS_REGION || "",
    cdn: (process.env.TENCENT_COS_CDN_URL || "").replace(/\/$/, ""),
  };
}
function storageStatus() {
  const cos = cosConfig();
  return {
    provider: getSetting("storage_provider") || "local",
    local: { ready: true, directory: uploadDir },
    cos: {
      ready: cos.ready,
      missing: cos.missing,
      bucket: cos.bucket,
      region: cos.region,
    },
  };
}
function publicCosUrl(key, config) {
  return config.cdn
    ? `${config.cdn}/${key}`
    : `https://${config.bucket}.cos.${config.region}.myqcloud.com/${key}`;
}
function referencedBy(asset) {
  const hits = [];
  for (const name of ["draft", "published"]) {
    const text = JSON.stringify(getDocument(name).content);
    if (text.includes(asset.optimizedUrl) || text.includes(asset.originalUrl))
      hits.push(name === "draft" ? "草稿" : "已发布内容");
  }
  return hits;
}
function audit(req, action, targetType = "", targetId = "", detail = "") {
  try {
    createAdminLog({
      username: req.admin || req.body?.username || "system",
      action,
      targetType,
      targetId,
      detail,
      ipAddress: req.ip || req.socket?.remoteAddress || "",
    });
  } catch (error) {
    console.error("Unable to write admin audit log", error);
  }
}
function escapeHtml(value) {
  return String(value ?? "").replace(
    /[&<>"']/g,
    (character) =>
      ({
        "&": "&amp;",
        "<": "&lt;",
        ">": "&gt;",
        '"': "&quot;",
        "'": "&#39;",
      })[character],
  );
}
function publicBaseUrl(req, seo = {}) {
  const configured = String(seo.siteUrl || "").trim().replace(/\/$/, "");
  if (/^https?:\/\//i.test(configured)) return configured;
  return `${req.protocol}://${req.get("host")}`;
}
function absolutePublicUrl(value, baseUrl) {
  if (!value) return "";
  try {
    return new URL(value, `${baseUrl}/`).href;
  } catch {
    return "";
  }
}
function renderPublicPage(req, res, filename, page = "home") {
  const content = getDocument("published").content;
  const seo = content.seo || {};
  const baseUrl = publicBaseUrl(req, seo);
  const project =
    page === "video"
      ? (content.projects || []).find(
          (item) =>
            item.id === req.query.id &&
            item.type === "video" &&
            item.published !== false,
        )
      : null;
  const title = project
    ? `${project.title}｜${content.profile?.siteName || "Cayson Huang"}`
    : seo.title || `${content.profile?.englishName || "Cayson"} — 影像档案`;
  const description = project?.description || seo.description || "";
  const socialTitle = project ? title : seo.socialTitle || title;
  const socialDescription = project?.description || seo.socialDescription || description;
  const image = absolutePublicUrl(
    project?.coverUrl || seo.socialImage,
    baseUrl,
  );
  const canonical = project
    ? `${baseUrl}/video.html?id=${encodeURIComponent(project.id)}`
    : `${baseUrl}/`;
  const robots = seo.allowIndexing === false ? "noindex,nofollow" : "index,follow";
  const tags = [
    `<title>${escapeHtml(title)}</title>`,
    `<meta name="description" content="${escapeHtml(description)}">`,
    seo.keywords
      ? `<meta name="keywords" content="${escapeHtml(seo.keywords)}">`
      : "",
    seo.author ? `<meta name="author" content="${escapeHtml(seo.author)}">` : "",
    `<meta name="robots" content="${robots}">`,
    `<link rel="canonical" href="${escapeHtml(canonical)}">`,
    `<meta property="og:locale" content="zh_CN">`,
    `<meta property="og:type" content="${project ? "video.other" : "website"}">`,
    `<meta property="og:site_name" content="${escapeHtml(content.profile?.siteName || "Cayson Huang")}">`,
    `<meta property="og:title" content="${escapeHtml(socialTitle)}">`,
    `<meta property="og:description" content="${escapeHtml(socialDescription)}">`,
    `<meta property="og:url" content="${escapeHtml(canonical)}">`,
    image ? `<meta property="og:image" content="${escapeHtml(image)}">` : "",
    `<meta name="twitter:card" content="${image ? "summary_large_image" : "summary"}">`,
    `<meta name="twitter:title" content="${escapeHtml(socialTitle)}">`,
    `<meta name="twitter:description" content="${escapeHtml(socialDescription)}">`,
    image ? `<meta name="twitter:image" content="${escapeHtml(image)}">` : "",
  ];
  if (seo.enableStructuredData !== false && !project) {
    const person = {
      "@context": "https://schema.org",
      "@type": "Person",
      name: content.profile?.name || seo.author,
      alternateName: content.profile?.englishName || "",
      url: canonical,
      image: absolutePublicUrl(content.profile?.avatarUrl, baseUrl),
      jobTitle: content.profile?.title || "",
      email: content.profile?.email || "",
      address: content.profile?.location || "",
      sameAs: (content.socials || [])
        .filter((item) => item.visible !== false && item.url)
        .map((item) => item.url),
    };
    tags.push(
      `<script type="application/ld+json" data-seo-schema>${JSON.stringify(person).replace(/</g, "\\u003c")}</script>`,
    );
  }
  let html = fs.readFileSync(path.join(projectRoot, filename), "utf8");
  html = html
    .replace(/<title>[\s\S]*?<\/title>/i, "")
    .replace(/<meta\s+name="description"[^>]*>/i, "")
    .replace(/<meta\s+name="(?:keywords|author|robots)"[^>]*>/gi, "")
    .replace(/<meta\s+property="og:[^"]+"[^>]*>/gi, "")
    .replace(/<meta\s+name="twitter:[^"]+"[^>]*>/gi, "")
    .replace(/<link\s+rel="canonical"[^>]*>/gi, "")
    .replace(/<script\s+type="application\/ld\+json"[^>]*>[\s\S]*?<\/script>/gi, "")
    .replace("</head>", `${tags.filter(Boolean).join("")}\n</head>`);
  res.type("html").send(html);
}
function adminPayload() {
  const draft = getDocument("draft"),
    published = getDocument("published"),
    media = listMedia(),
    revisions = listRevisions();
  return {
    content: draft.content,
    draftUpdatedAt: draft.updatedAt,
    publishedUpdatedAt: published.updatedAt,
    versionState: {
      draftHash: draft.hash,
      publishedHash: published.hash,
      hasUnpublishedChanges: draft.hash !== published.hash,
    },
    frontendUrl: process.env.FRONTEND_URL || "",
    revisions,
    media: media.map((item) => ({
      ...item,
      originalName: normalizeUploadFilename(item.originalName),
      references: referencedBy(item),
    })),
    settings: storageStatus(),
    logs: listAdminLogs(200),
    stats: {
      experiences: draft.content.experiences.length,
      caseStudies: draft.content.caseStudies.length,
      projects: draft.content.projects.length,
      media: media.length,
    },
  };
}

app.get("/api/health", (_req, res) => res.json({ ok: true }));
app.get("/api/site", (_req, res) => res.json(getDocument("published").content));
app.get("/robots.txt", (req, res) => {
  const seo = getDocument("published").content.seo || {};
  const baseUrl = publicBaseUrl(req, seo);
  const rules =
    seo.allowIndexing === false
      ? "User-agent: *\nDisallow: /"
      : "User-agent: *\nAllow: /\nDisallow: /manage\nDisallow: /api/admin";
  res.type("text/plain").send(`${rules}\nSitemap: ${baseUrl}/sitemap.xml\n`);
});
app.get("/sitemap.xml", (req, res) => {
  const content = getDocument("published").content;
  const baseUrl = publicBaseUrl(req, content.seo || {});
  const urls = [
    `${baseUrl}/`,
    ...(content.projects || [])
      .filter(
        (item) =>
          item.published !== false && item.type === "video" && item.videoUrl,
      )
      .map(
        (item) =>
          `${baseUrl}/video.html?id=${encodeURIComponent(item.id)}`,
      ),
  ];
  res
    .type("application/xml")
    .send(
      `<?xml version="1.0" encoding="UTF-8"?>\n<urlset xmlns="http://www.sitemaps.org/schemas/sitemap/0.9">${urls.map((url) => `<url><loc>${escapeHtml(url)}</loc></url>`).join("")}</urlset>`,
    );
});

const loginLimiter = rateLimit({
  windowMs: 10 * 60 * 1000,
  limit: 5,
  skipSuccessfulRequests: true,
  standardHeaders: true,
  legacyHeaders: false,
  message: { error: "尝试次数过多，请十分钟后再试" },
});
app.post("/api/admin/login", loginLimiter, (req, res) => {
  const { username = "admin", password = "" } = req.body || {};
  const user = db
    .prepare("SELECT * FROM admin_users WHERE username=?")
    .get(username);
  if (!user || !bcrypt.compareSync(password, user.password_hash)) {
    audit(req, "login_failed", "session", "", "登录验证失败");
    return res.status(401).json({ error: "用户名或密码错误" });
  }
  const token = crypto.randomBytes(32).toString("hex"),
    expires = Date.now() + 8 * 60 * 60 * 1000;
  db.prepare(
    "INSERT INTO admin_sessions (token_hash,username,expires_at) VALUES (?,?,?)",
  ).run(hashToken(token), username, expires);
  const secure = process.env.NODE_ENV === "production" ? "; Secure" : "";
  res.setHeader(
    "Set-Cookie",
    `portfolio_session=${token}; HttpOnly; SameSite=Lax${secure}; Path=/; Max-Age=28800`,
  );
  req.admin = username;
  audit(req, "login_success", "session", "", "登录后台");
  res.json({ ok: true, username });
});
app.post("/api/admin/logout", requireAdmin, (req, res) => {
  const token = cookieValue(req, "portfolio_session");
  db.prepare("DELETE FROM admin_sessions WHERE token_hash=?").run(
    hashToken(token),
  );
  res.setHeader(
    "Set-Cookie",
    "portfolio_session=; HttpOnly; SameSite=Lax; Path=/; Max-Age=0",
  );
  audit(req, "logout", "session", "", "退出后台");
  res.json({ ok: true });
});
app.get("/api/admin/bootstrap", requireAdmin, (_req, res) =>
  res.json(adminPayload()),
);
app.get("/api/admin/preview", requireAdmin, (_req, res) =>
  res.json(getDocument("draft").content),
);
app.put("/api/admin/content", requireAdmin, (req, res) => {
  if (!validateContent(req.body))
    return res.status(400).json({ error: "内容结构无效" });
  const saved = saveDraft(req.body);
  audit(req, "draft_saved", "content", "draft", `哈希 ${saved.hash}`);
  res.json(saved);
});
app.post("/api/admin/publish", requireAdmin, (req, res) => {
  const issues = projectValidationIssues(getDocument("draft").content);
  if (issues.length) return res.status(400).json({ error: issues.join("；") });
  const result = publishDraft(req.admin);
  audit(
    req,
    result.changed ? "site_published" : "publish_skipped",
    "revision",
    result.id || "",
    result.changed
      ? `发布版本 #${result.id}，哈希 ${result.hash}`
      : `内容哈希未变化，未创建重复版本（${result.hash}）`,
  );
  res.json(result);
});
app.post("/api/admin/revisions/:id/restore", requireAdmin, (req, res) => {
  const restored = restoreRevision(Number(req.params.id));
  if (!restored) return res.status(404).json({ error: "版本不存在" });
  audit(
    req,
    "revision_restored",
    "revision",
    req.params.id,
    `恢复为草稿，哈希 ${restored.hash}`,
  );
  res.json(restored);
});
app.delete("/api/admin/revisions/:id", requireAdmin, (req, res) => {
  const deleted = deleteRevision(Number(req.params.id));
  if (!deleted) return res.status(404).json({ error: "版本不存在" });
  audit(
    req,
    "revision_deleted",
    "revision",
    deleted.id,
    `删除历史版本，哈希 ${deleted.hash}`,
  );
  res.status(204).end();
});
app.put("/api/admin/settings/storage", requireAdmin, (req, res) => {
  const provider = req.body?.provider;
  if (!["local", "cos"].includes(provider))
    return res.status(400).json({ error: "无效的存储类型" });
  if (provider === "cos" && !cosConfig().ready)
    return res.status(400).json({ error: "腾讯云 COS 配置不完整" });
  const previous = getSetting("storage_provider") || "local";
  setSetting("storage_provider", provider);
  audit(
    req,
    "storage_updated",
    "setting",
    "storage_provider",
    `${previous} → ${provider}`,
  );
  res.json(storageStatus());
});

app.post(
  "/api/admin/media",
  requireAdmin,
  upload.single("image"),
  async (req, res, next) => {
    try {
      if (!req.file)
        return res.status(400).json({ error: "请选择 JPG、PNG 或 WebP 图片" });
      const displayName = normalizeUploadFilename(req.file.originalname);
      const stamp = `${Date.now()}-${crypto.randomBytes(5).toString("hex")}`,
        ext = path.extname(displayName).toLowerCase() || ".bin";
      const originalName = `${stamp}${ext}`,
        optimizedName = `${stamp}.webp`;
      const optimized = await sharp(req.file.buffer)
        .rotate()
        .resize({
          width: 2400,
          height: 2400,
          fit: "inside",
          withoutEnlargement: true,
        })
        .webp({ quality: 82 })
        .toBuffer();
      const metadata = await sharp(optimized).metadata();
      const provider = getSetting("storage_provider") || "local";
      let originalUrl, optimizedUrl, originalKey, optimizedKey;
      if (provider === "cos") {
        const config = cosConfig();
        if (!config.ready)
          return res
            .status(400)
            .json({ error: `COS 配置缺少：${config.missing.join(", ")}` });
        const cos = new COS({
          SecretId: process.env.TENCENT_COS_SECRET_ID,
          SecretKey: process.env.TENCENT_COS_SECRET_KEY,
        });
        originalKey = `portfolio/original/${originalName}`;
        optimizedKey = `portfolio/web/${optimizedName}`;
        const put = (Key, Body, ContentType) =>
          new Promise((resolve, reject) =>
            cos.putObject(
              {
                Bucket: config.bucket,
                Region: config.region,
                Key,
                Body,
                ContentType,
              },
              (error, data) => (error ? reject(error) : resolve(data)),
            ),
          );
        await put(originalKey, req.file.buffer, req.file.mimetype);
        await put(optimizedKey, optimized, "image/webp");
        originalUrl = publicCosUrl(originalKey, config);
        optimizedUrl = publicCosUrl(optimizedKey, config);
      } else {
        const originals = path.join(uploadDir, "original"),
          web = path.join(uploadDir, "web");
        fs.mkdirSync(originals, { recursive: true });
        fs.mkdirSync(web, { recursive: true });
        fs.writeFileSync(path.join(originals, originalName), req.file.buffer);
        fs.writeFileSync(path.join(web, optimizedName), optimized);
        originalKey = `original/${originalName}`;
        optimizedKey = `web/${optimizedName}`;
        originalUrl = `/uploads/${originalKey}`;
        optimizedUrl = `/uploads/${optimizedKey}`;
      }
      const id = createMedia({
        originalName: displayName,
        storage: provider,
        originalUrl,
        optimizedUrl,
        originalKey,
        optimizedKey,
        mimeType: req.file.mimetype,
        width: metadata.width || 0,
        height: metadata.height || 0,
        size: optimized.length,
      });
      audit(
        req,
        "media_uploaded",
        "media",
        id,
        `${displayName} · ${provider} · ${metadata.width || 0}×${metadata.height || 0}`,
      );
      res.status(201).json(getMedia(id));
    } catch (error) {
      next(error);
    }
  },
);
app.delete("/api/admin/media/:id", requireAdmin, async (req, res, next) => {
  try {
    const asset = getMedia(Number(req.params.id));
    if (!asset) return res.status(404).json({ error: "图片不存在" });
    const references = referencedBy(asset);
    if (references.length)
      return res
        .status(409)
        .json({ error: `图片正在被${references.join("、")}使用` });
    if (asset.storage === "local") {
      for (const key of [asset.originalKey, asset.optimizedKey]) {
        const target = path.resolve(uploadDir, key);
        if (
          target.startsWith(`${uploadDir}${path.sep}`) &&
          fs.existsSync(target)
        )
          fs.unlinkSync(target);
      }
    } else {
      const config = cosConfig();
      if (!config.ready)
        return res
          .status(400)
          .json({ error: "COS 配置不完整，无法删除远端文件" });
      const cos = new COS({
        SecretId: process.env.TENCENT_COS_SECRET_ID,
        SecretKey: process.env.TENCENT_COS_SECRET_KEY,
      });
      await new Promise((resolve, reject) =>
        cos.deleteMultipleObject(
          {
            Bucket: config.bucket,
            Region: config.region,
            Objects: [{ Key: asset.originalKey }, { Key: asset.optimizedKey }],
          },
          (error) => (error ? reject(error) : resolve()),
        ),
      );
    }
    deleteMediaRecord(asset.id);
    audit(
      req,
      "media_deleted",
      "media",
      asset.id,
      `${normalizeUploadFilename(asset.originalName)} · ${asset.storage}`,
    );
    res.status(204).end();
  } catch (error) {
    next(error);
  }
});
app.get("/api/admin/export", requireAdmin, (req, res) => {
  audit(req, "backup_exported", "backup", "", "导出完整 JSON 备份");
  res
    .type("application/json")
    .attachment("portfolio-cms-backup.json")
    .send(
      JSON.stringify(
        {
          exportedAt: new Date().toISOString(),
          draft: getDocument("draft"),
          published: getDocument("published"),
          revisions: listRevisions(),
          logs: listAdminLogs(500),
          media: listMedia(),
        },
        null,
        2,
      ),
    );
});

app.get("/manage", (_req, res) =>
  res.sendFile(path.join(__dirname, "admin.html")),
);
app.get(["/", "/index.html"], (req, res) =>
  renderPublicPage(req, res, "index.html"),
);
app.get("/video.html", (req, res) =>
  renderPublicPage(req, res, "video.html", "video"),
);
app.use((error, _req, res, _next) => {
  console.error(error);
  if (error instanceof multer.MulterError)
    return res.status(400).json({
      error:
        error.code === "LIMIT_FILE_SIZE" ? "图片不能超过 12MB" : error.message,
    });
  res.status(500).json({ error: "服务器处理失败" });
});

const adminPassword = process.env.ADMIN_PASSWORD;
if (!adminPassword)
  console.warn("ADMIN_PASSWORD 未设置，后台不会创建管理员账号。");
else {
  const user = db
    .prepare("SELECT * FROM admin_users WHERE username='admin'")
    .get();
  if (!user)
    db.prepare(
      "INSERT INTO admin_users (username,password_hash) VALUES (?,?)",
    ).run("admin", bcrypt.hashSync(adminPassword, 12));
  else if (!bcrypt.compareSync(adminPassword, user.password_hash))
    db.prepare("UPDATE admin_users SET password_hash=? WHERE username=?").run(
      bcrypt.hashSync(adminPassword, 12),
      "admin",
    );
}
cleanSessions();
app.listen(port, () =>
  console.log(`Portfolio CMS listening on http://localhost:${port}/manage`),
);
