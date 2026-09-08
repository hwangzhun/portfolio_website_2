const fs = require("node:fs");
const path = require("node:path");
const crypto = require("node:crypto");
const Database = require("better-sqlite3");

const dataDir = process.env.DATA_DIR
  ? path.resolve(process.env.DATA_DIR)
  : path.join(__dirname, "data");
fs.mkdirSync(dataDir, { recursive: true });
const db = new Database(path.join(dataDir, "portfolio.sqlite"));
db.pragma("journal_mode = WAL");
db.pragma("foreign_keys = ON");
db.exec(`
  CREATE TABLE IF NOT EXISTS site_content (key TEXT PRIMARY KEY, value TEXT NOT NULL);
  CREATE TABLE IF NOT EXISTS videos (id INTEGER PRIMARY KEY AUTOINCREMENT,title TEXT NOT NULL,description TEXT DEFAULT '',cover_url TEXT DEFAULT '',file_id TEXT DEFAULT '',playback_url TEXT DEFAULT '',sort_order INTEGER DEFAULT 0,published INTEGER DEFAULT 1,created_at TEXT DEFAULT CURRENT_TIMESTAMP,updated_at TEXT DEFAULT CURRENT_TIMESTAMP);
  CREATE TABLE IF NOT EXISTS admin_users (id INTEGER PRIMARY KEY AUTOINCREMENT,username TEXT UNIQUE NOT NULL,password_hash TEXT NOT NULL);
  CREATE TABLE IF NOT EXISTS site_documents (name TEXT PRIMARY KEY CHECK(name IN ('draft','published')),content TEXT NOT NULL,updated_at TEXT DEFAULT CURRENT_TIMESTAMP);
  CREATE TABLE IF NOT EXISTS content_revisions (id INTEGER PRIMARY KEY AUTOINCREMENT,content TEXT NOT NULL,created_at TEXT DEFAULT CURRENT_TIMESTAMP,created_by TEXT DEFAULT 'admin');
  CREATE TABLE IF NOT EXISTS admin_sessions (token_hash TEXT PRIMARY KEY,username TEXT NOT NULL,expires_at INTEGER NOT NULL,created_at TEXT DEFAULT CURRENT_TIMESTAMP);
  CREATE TABLE IF NOT EXISTS media_assets (id INTEGER PRIMARY KEY AUTOINCREMENT,original_name TEXT NOT NULL,storage TEXT NOT NULL,original_url TEXT NOT NULL,optimized_url TEXT NOT NULL,original_key TEXT DEFAULT '',optimized_key TEXT DEFAULT '',mime_type TEXT NOT NULL,width INTEGER DEFAULT 0,height INTEGER DEFAULT 0,size INTEGER DEFAULT 0,created_at TEXT DEFAULT CURRENT_TIMESTAMP);
  CREATE TABLE IF NOT EXISTS cms_settings (key TEXT PRIMARY KEY,value TEXT NOT NULL);
  CREATE TABLE IF NOT EXISTS admin_logs (id INTEGER PRIMARY KEY AUTOINCREMENT,username TEXT NOT NULL DEFAULT 'system',action TEXT NOT NULL,target_type TEXT DEFAULT '',target_id TEXT DEFAULT '',detail TEXT DEFAULT '',ip_address TEXT DEFAULT '',created_at TEXT DEFAULT CURRENT_TIMESTAMP);
`);
const revisionColumns = db
  .prepare("PRAGMA table_info(content_revisions)")
  .all();
if (!revisionColumns.some((column) => column.name === "content_hash")) {
  db.exec(
    "ALTER TABLE content_revisions ADD COLUMN content_hash TEXT NOT NULL DEFAULT ''",
  );
}
db.exec(
  "CREATE INDEX IF NOT EXISTS idx_content_revisions_hash ON content_revisions(content_hash); CREATE INDEX IF NOT EXISTS idx_admin_logs_created_at ON admin_logs(created_at DESC, id DESC);",
);

const defaultContent = {
  profile: {
    siteName: "CAYSON HUANG",
    name: "黄臻",
    englishName: "Cayson",
    title: "摄影师 / 摄像师 / 剪辑师",
    email: "Huangzhenmsn@hotmail.com",
    phone: "",
    location: "广东 深圳",
    avatarUrl: "/assets/images/my-avatar-1.png",
  },
  hero: {
    eyebrow: "SELECTED WORKS · 2018—2025",
    title: "以影像记录",
    linkedTitle: "真实发生的事",
    description:
      "我是黄臻，影像创作者与剪辑师。专注于把品牌、人物与现场的细微瞬间，整理成有温度且有节奏的视觉叙事。",
    featureEyebrow: "FIELD NOTES / 01",
    featureTitle: "观察，然后构成。",
    featureDescription:
      "每一个项目都从一次仔细的观察开始：光线如何移动，人群如何呼吸，故事如何露出它的轮廓。",
    featureImage: "/assets/images/field-notes-hero.png",
  },
  about: {
    eyebrow: "ABOUT THE PRACTICE",
    title: "从现场到银幕，让感觉被看见。",
    description:
      "摄影、摄像、剪辑与调色并非孤立的工序，而是一条完整的叙事线。我习惯深入项目的前期思考，也珍惜后期中每一次克制的取舍。",
  },
  seo: {
    siteUrl: "https://hwangzhun.com",
    title: "黄臻 Cayson｜深圳摄影师、摄像师与剪辑师作品集",
    description:
      "黄臻（Cayson）个人影像作品集，深圳摄影师、摄像师与剪辑师，专注品牌影片、产品影像、活动纪实、后期剪辑与调色。",
    keywords:
      "黄臻,Cayson Huang,深圳摄影师,深圳摄像师,视频剪辑师,品牌影片,产品摄影,活动摄影,影像作品集",
    author: "黄臻 Cayson Huang",
    socialTitle: "黄臻 Cayson｜影像作品与创作档案",
    socialDescription:
      "品牌影片、产品影像与活动纪实作品集——以有温度、有节奏的视觉叙事记录真实发生的事。",
    socialImage: "/assets/images/project-8.jpg",
    allowIndexing: true,
    enableStructuredData: true,
  },
  sections: {
    worksEyebrow: "SELECTED ARCHIVE",
    worksTitle: "精选作品",
    recordsEyebrow: "A WORKING RECORD",
    recordsTitle: "工作与项目经历",
    experienceTitle: "工作经历",
    caseStudyTitle: "项目经历",
    toolsEyebrow: "TOOLS OF THE TRADE",
  },
  experiences: [
    {
      startDate: "2022",
      endDate: "现在",
      company: "深圳恩浦诺科技有限公司",
      department: "新媒体运营团队",
      role: "影像内容负责人 / 摄影师 / 摄像师",
      visible: true,
      details: [
        "工作内容：主导众筹项目的视频策划、拍摄与后期制作，并负责新品发布、线上活动及电商产品图、场景图的视觉内容。",
        "工作内容：持续维护 Facebook、Instagram 等社交平台的影像内容与品牌表达，协调产品、运营与市场团队完成内容交付。",
        "工作成就：CEBA Rapi 众筹项目实际筹得 55,472 美元，超出目标 1,109.4%；以视觉叙事清晰呈现产品核心卖点。",
        "工作成就：支持黑色星期五等大型营销节点，活动期间销售额超过 300 万美元；社媒内容吸引 2,000+ 活跃粉丝。",
      ],
    },
    {
      startDate: "2019",
      endDate: "2022",
      company: "深圳市捷时行教育科技有限公司",
      department: "",
      role: "视频制作专员 / 摄影师 / 剪辑师",
      visible: true,
      details: [
        "工作内容：负责在线教育课程从拍摄、收音到剪辑、包装的全流程制作，并与教师及教研团队共同梳理教学表达。",
        "工作成就：主导完成 500+ 条教育视频，覆盖多个学科；制作《全国教学能大赛》参赛视频，支持 5 位教授取得优异成绩。",
      ],
    },
    {
      startDate: "2018",
      endDate: "2019",
      company: "中国国际光电博览会",
      department: "新媒体团队",
      role: "展会影像制作 / 摄像师 / 剪辑师",
      visible: true,
      details: [
        "工作内容：策划并拍摄企业宣传片、展会活动视频及企业家访谈，记录现场核心事件与品牌叙事。",
        "工作成就：为后续宣传沉淀高质量视觉素材，并提升展会期间品牌曝光。",
      ],
    },
  ],
  caseStudies: [
    {
      period: "2024",
      title: "CEBA Rapi 充电宝众筹项目",
      role: "项目负责人 · 摄影师 / 摄像师",
      visible: true,
      link: "https://www.kickstarter.com/projects/ennoprogroup/ceba-rapi-worlds-fastest-charging-power-bank",
      details: [
        "工作内容：负责宣传片、场景图与白底图拍摄，制定拍摄计划并完成后期制作。",
        "项目成果：项目筹得 55,472 美元，超过 5,000 美元众筹目标。",
      ],
    },
    {
      period: "2023",
      title: "GlassOuse 辅助设备新产品上线",
      role: "项目负责人 · 摄影师 / 摄像师",
      visible: true,
      link: "",
      details: [
        "工作内容：为三个新产品制定拍摄脚本与执行计划，协调团队完成拍摄、剪辑和上线素材。",
        "项目成果：在黑色星期五前完成上线并为品牌引流，黑五期间销售额达到 500 万元人民币。",
      ],
    },
  ],
  projects: [
    {
      id: "lululemon-camping",
      type: "photo",
      title: "Lululemon 露营活动",
      category: "活动拍摄",
      year: "2024",
      meta: "现场摄影 / 影像记录",
      description: "",
      coverUrl: "/assets/images/project-8.jpg",
      externalUrl: "https://www.bilibili.com",
      videoUrl: "",
      featured: true,
      published: true,
    },
    {
      id: "cioe-2018",
      type: "video",
      title: "CIOE 2018",
      category: "视频",
      year: "2018",
      meta: "导演 / 剪辑",
      description: "中国国际光电博览会现场影像记录与品牌影片。",
      coverUrl: "/assets/images/project-1.jpg",
      externalUrl: "",
      videoUrl:
        "https://blog-data-1306368489.cos.ap-guangzhou.myqcloud.com/portfolio_video/Highlights%20of%20CIOE%202018.mp4",
      featured: false,
      published: true,
    },
    {
      id: "cioe-2019",
      type: "video",
      title: "CIOE 2019",
      category: "视频",
      year: "2019",
      meta: "摄影 / 后期",
      description: "中国国际光电博览会现场影像记录与品牌影片。",
      coverUrl: "/assets/images/project-2.png",
      externalUrl: "",
      videoUrl:
        "https://blog-data-1306368489.cos.ap-guangzhou.myqcloud.com/portfolio_video/Highlights%20of%20CIOE%202019.mp4",
      featured: false,
      published: true,
    },
    {
      id: "frisbee-dog-go",
      type: "photo",
      title: "飞盘狗狗 GO",
      category: "活动拍摄",
      year: "2024",
      meta: "现场摄影",
      description: "",
      coverUrl: "/assets/images/project-3.jpg",
      externalUrl: "https://www.bilibili.com",
      videoUrl: "",
      featured: false,
      published: true,
    },
    {
      id: "glassouse-blink",
      type: "video",
      title: "GlassOuse Blink",
      category: "视频",
      year: "2023",
      meta: "产品影像 / 剪辑",
      description: "GlassOuse Blink 辅助设备产品宣传影片。",
      coverUrl: "/assets/images/project-4.png",
      externalUrl: "",
      videoUrl:
        "https://blog-data-1306368489.cos.ap-guangzhou.myqcloud.com/portfolio_video/blink%20switch.mp4",
      featured: false,
      published: true,
    },
    {
      id: "vascular-course",
      type: "photo",
      title: "周围血管疾病学习班",
      category: "活动拍摄",
      year: "2023",
      meta: "活动记录",
      description: "",
      coverUrl: "/assets/images/project-7.png",
      externalUrl: "https://www.bilibili.com",
      videoUrl: "",
      featured: false,
      published: true,
    },
  ],
  software: [
    { name: "Premiere Pro", iconUrl: "/assets/images/pr.png", visible: true },
    { name: "After Effects", iconUrl: "/assets/images/ae.png", visible: true },
    {
      name: "DaVinci Resolve",
      iconUrl: "/assets/images/davinci.png",
      visible: true,
    },
    { name: "Lightroom", iconUrl: "/assets/images/lr.png", visible: true },
    { name: "Photoshop", iconUrl: "/assets/images/ps.png", visible: true },
    { name: "Illustrator", iconUrl: "/assets/images/ai.png", visible: true },
  ],
  socials: [
    {
      platform: "站酷",
      url: "https://www.zcool.com.cn/u/19165541",
      visible: true,
    },
    { platform: "个人博客", url: "https://blog.hwangzhun.com", visible: true },
    {
      platform: "Instagram",
      url: "https://www.instagram.com/hwangzhun/",
      visible: true,
    },
  ],
  tools:
    "Adobe Premiere Pro · After Effects · DaVinci Resolve · Lightroom · Photoshop · Illustrator",
  footer: {
    prompt: "想聊聊下一个项目？",
    copyright: "© 2025 CAYSON HUANG. SHENZHEN, CHINA.",
  },
};
const clone = (value) => JSON.parse(JSON.stringify(value));
const safeJson = (value, fallback) => {
  try {
    return JSON.parse(value);
  } catch {
    return fallback;
  }
};
const legacyVideoUrls = {
  "CIOE 2018":
    "https://blog-data-1306368489.cos.ap-guangzhou.myqcloud.com/portfolio_video/Highlights%20of%20CIOE%202018.mp4",
  "CIOE 2019":
    "https://blog-data-1306368489.cos.ap-guangzhou.myqcloud.com/portfolio_video/Highlights%20of%20CIOE%202019.mp4",
  "5G应急专网部署与优化":
    "https://blog-data-1306368489.cos.ap-guangzhou.myqcloud.com/portfolio_video/%E6%95%99%E6%A1%88%E9%85%8D%E5%A5%97-5G%E5%BA%94%E6%80%A5%E4%B8%93%E7%BD%91%E9%83%A8%E7%BD%B2%E4%B8%8E%E4%BC%98%E5%8C%96.mp4",
  平安好学:
    "https://blog-data-1306368489.cos.ap-guangzhou.myqcloud.com/portfolio_video/%E5%BE%AE%E5%89%A7%E6%83%85-%E5%B9%B3%E5%AE%89%E5%A5%BD%E5%AD%A6.mp4",
  口罩工厂:
    "https://blog-data-1306368489.cos.ap-guangzhou.myqcloud.com/portfolio_video/%E5%AE%A3%E4%BC%A0%E7%89%87-%E5%8F%A3%E7%BD%A9%E5%B7%A5%E5%8E%82.mp4",
  英语面试:
    "https://blog-data-1306368489.cos.ap-guangzhou.myqcloud.com/portfolio_video/TVC-%E8%8B%B1%E8%AF%AD%E9%9D%A2%E8%AF%95.mp4",
  工具箱:
    "https://blog-data-1306368489.cos.ap-guangzhou.myqcloud.com/portfolio_video/%E7%94%B5%E5%95%86-%E5%B7%A5%E5%85%B7%E7%AE%B1.mp4",
  "个人 IP 口播":
    "https://blog-data-1306368489.cos.ap-guangzhou.myqcloud.com/portfolio_video/%E4%B8%AA%E4%BA%BAIP-%E5%8F%A3%E6%92%AD.mp4",
  "GlassOuse Blink":
    "https://blog-data-1306368489.cos.ap-guangzhou.myqcloud.com/portfolio_video/blink%20switch.mp4",
};
function normalizeContent(input = {}) {
  const result = clone(defaultContent);
  for (const key of ["profile", "hero", "footer", "sections", "seo"])
    if (input[key] && typeof input[key] === "object")
      Object.assign(result[key], input[key]);
  if (
    input.about &&
    !Array.isArray(input.about) &&
    typeof input.about === "object"
  )
    Object.assign(result.about, input.about);
  if (Array.isArray(input.about))
    result.about.description = input.about.join("\n\n");
  if (typeof input.tools === "string") result.tools = input.tools;
  for (const key of [
    "experiences",
    "caseStudies",
    "projects",
    "software",
    "socials",
  ])
    if (Array.isArray(input[key])) result[key] = input[key];
  result.projects = result.projects.map((item, index) => {
    const type =
        item.type ||
        ((item.category || "").includes("视频") ? "video" : "photo"),
      id =
        item.id ||
        `work-${crypto
          .createHash("sha1")
          .update(`${item.title || "item"}-${index}`)
          .digest("hex")
          .slice(0, 10)}`;
    const migrated = {
      ...item,
      id,
      type,
      description: item.description || "",
      externalUrl:
        item.externalUrl ?? (type === "photo" ? item.link || "" : ""),
      videoUrl:
        item.videoUrl ??
        (type === "video"
          ? legacyVideoUrls[item.title] ||
            (/\.mp4(?:\?|$)/i.test(item.link || "") ? item.link : "")
          : ""),
    };
    delete migrated.link;
    return migrated;
  });
  result.experiences = result.experiences.map((item) => {
    const migrated = { ...item, department: item.department || "" };
    if ((!migrated.startDate || !migrated.endDate) && item.period) {
      const match = String(item.period).match(
        /^(.*?)\s*(?:—|–|\s-\s)\s*(.*?)$/,
      );
      if (match) {
        migrated.startDate = migrated.startDate || match[1].trim();
        migrated.endDate = migrated.endDate || match[2].trim();
      } else migrated.startDate = migrated.startDate || item.period;
    }
    migrated.startDate = migrated.startDate || "";
    migrated.endDate = migrated.endDate || "";
    delete migrated.period;
    return migrated;
  });
  return result;
}
function stableStringify(value) {
  if (Array.isArray(value))
    return `[${value.map((item) => stableStringify(item)).join(",")}]`;
  if (value && typeof value === "object") {
    return `{${Object.keys(value)
      .sort()
      .map((key) => `${JSON.stringify(key)}:${stableStringify(value[key])}`)
      .join(",")}}`;
  }
  return JSON.stringify(value);
}
function contentHash(content) {
  return crypto
    .createHash("sha256")
    .update(stableStringify(normalizeContent(content)))
    .digest("hex");
}
const legacyRow = db
  .prepare("SELECT value FROM site_content WHERE key='main'")
  .get();
const initial = normalizeContent(
  legacyRow ? safeJson(legacyRow.value, {}) : {},
);
const oldVideos = db
  .prepare(
    "SELECT title,description,cover_url AS coverUrl,playback_url AS link,published FROM videos ORDER BY sort_order,id",
  )
  .all();
if (oldVideos.length) {
  const known = new Set(initial.projects.map((item) => item.title));
  for (const [index, item] of oldVideos.entries())
    if (!known.has(item.title))
      initial.projects.push({
        id: `legacy-video-${index + 1}`,
        type: "video",
        title: item.title,
        category: "视频",
        year: "",
        meta: "",
        description: item.description || "",
        coverUrl: item.coverUrl || "",
        externalUrl: "",
        videoUrl: item.link || legacyVideoUrls[item.title] || "",
        featured: false,
        published: Boolean(item.published),
      });
}
const insertDocument = db.prepare(
  "INSERT OR IGNORE INTO site_documents (name,content) VALUES (?,?)",
);
insertDocument.run("published", JSON.stringify(initial));
insertDocument.run("draft", JSON.stringify(initial));
const migrateDocuments = db.transaction(() => {
  const rows = db.prepare("SELECT name,content FROM site_documents").all();
  const update = db.prepare("UPDATE site_documents SET content=? WHERE name=?");
  for (const row of rows)
    update.run(
      JSON.stringify(normalizeContent(safeJson(row.content, {}))),
      row.name,
    );
});
migrateDocuments();
const backfillRevisionHashes = db.transaction(() => {
  const rows = db
    .prepare("SELECT id,content FROM content_revisions WHERE content_hash='' ")
    .all();
  const update = db.prepare(
    "UPDATE content_revisions SET content_hash=? WHERE id=?",
  );
  for (const row of rows)
    update.run(contentHash(safeJson(row.content, {})), row.id);
});
backfillRevisionHashes();
db.prepare(
  "INSERT OR IGNORE INTO cms_settings (key,value) VALUES ('storage_provider','local')",
).run();

function getDocument(name = "published") {
  const row = db
    .prepare(
      "SELECT content,updated_at AS updatedAt FROM site_documents WHERE name=?",
    )
    .get(name);
  const content = normalizeContent(safeJson(row.content, {}));
  return { content, updatedAt: row.updatedAt, hash: contentHash(content) };
}
function saveDraft(content) {
  const normalized = normalizeContent(content);
  db.prepare(
    "UPDATE site_documents SET content=?,updated_at=CURRENT_TIMESTAMP WHERE name='draft'",
  ).run(JSON.stringify(normalized));
  return getDocument("draft");
}
const publishTx = db.transaction((username) => {
  const draft = getDocument("draft").content;
  const hash = contentHash(draft);
  const published = getDocument("published");
  if (hash === published.hash)
    return { changed: false, id: null, ...published };
  db.prepare(
    "UPDATE site_documents SET content=?,updated_at=CURRENT_TIMESTAMP WHERE name='published'",
  ).run(JSON.stringify(draft));
  const id = db
    .prepare(
      "INSERT INTO content_revisions (content,content_hash,created_by) VALUES (?,?,?)",
    )
    .run(JSON.stringify(draft), hash, username).lastInsertRowid;
  return { changed: true, id, ...getDocument("published") };
});
function listRevisions() {
  const publishedHash = getDocument("published").hash;
  const draftHash = getDocument("draft").hash;
  const rows = db
    .prepare(
      "SELECT id,content,content_hash AS hash,created_at AS createdAt,created_by AS createdBy FROM content_revisions ORDER BY id DESC LIMIT 30",
    )
    .all();
  return rows.map((row, index) => {
    const content = normalizeContent(safeJson(row.content, {}));
    return {
      id: row.id,
      hash: row.hash,
      shortHash: row.hash.slice(0, 10),
      matchesPublished: row.hash === publishedHash,
      matchesDraft: row.hash === draftHash,
      sameAsPrevious: row.hash === rows[index + 1]?.hash,
      createdAt: row.createdAt,
      createdBy: row.createdBy,
      siteName: content.profile.siteName,
      heroTitle: `${content.hero.title} ${content.hero.linkedTitle}`.trim(),
      counts: {
        experiences: content.experiences.length,
        caseStudies: content.caseStudies.length,
        projects: content.projects.length,
      },
    };
  });
}
function restoreRevision(id) {
  const row = db
    .prepare("SELECT content FROM content_revisions WHERE id=?")
    .get(id);
  return row ? saveDraft(safeJson(row.content, {})) : null;
}
function deleteRevision(id) {
  const row = db
    .prepare(
      "SELECT id,content_hash AS hash,created_at AS createdAt,created_by AS createdBy FROM content_revisions WHERE id=?",
    )
    .get(id);
  if (!row) return null;
  db.prepare("DELETE FROM content_revisions WHERE id=?").run(id);
  return row;
}
function createAdminLog({
  username = "system",
  action,
  targetType = "",
  targetId = "",
  detail = "",
  ipAddress = "",
}) {
  const detailText =
    typeof detail === "string" ? detail : JSON.stringify(detail || {});
  return Number(
    db
      .prepare(
        "INSERT INTO admin_logs (username,action,target_type,target_id,detail,ip_address) VALUES (?,?,?,?,?,?)",
      )
      .run(
        username,
        action,
        targetType,
        String(targetId || ""),
        detailText,
        ipAddress,
      ).lastInsertRowid,
  );
}
function listAdminLogs(limit = 100) {
  const safeLimit = Math.max(1, Math.min(Number(limit) || 100, 500));
  return db
    .prepare(
      "SELECT id,username,action,target_type AS targetType,target_id AS targetId,detail,ip_address AS ipAddress,created_at AS createdAt FROM admin_logs ORDER BY id DESC LIMIT ?",
    )
    .all(safeLimit);
}
function getSetting(key) {
  return db.prepare("SELECT value FROM cms_settings WHERE key=?").get(key)
    ?.value;
}
function setSetting(key, value) {
  db.prepare(
    "INSERT INTO cms_settings (key,value) VALUES (?,?) ON CONFLICT(key) DO UPDATE SET value=excluded.value",
  ).run(key, String(value));
}
function createMedia(asset) {
  return Number(
    db
      .prepare(
        "INSERT INTO media_assets (original_name,storage,original_url,optimized_url,original_key,optimized_key,mime_type,width,height,size) VALUES (@originalName,@storage,@originalUrl,@optimizedUrl,@originalKey,@optimizedKey,@mimeType,@width,@height,@size)",
      )
      .run(asset).lastInsertRowid,
  );
}
function listMedia() {
  return db
    .prepare(
      "SELECT id,original_name AS originalName,storage,original_url AS originalUrl,optimized_url AS optimizedUrl,mime_type AS mimeType,width,height,size,created_at AS createdAt FROM media_assets ORDER BY id DESC",
    )
    .all();
}
function getMedia(id) {
  return db
    .prepare(
      "SELECT id,original_name AS originalName,storage,original_url AS originalUrl,optimized_url AS optimizedUrl,original_key AS originalKey,optimized_key AS optimizedKey,mime_type AS mimeType,width,height,size FROM media_assets WHERE id=?",
    )
    .get(id);
}
function deleteMediaRecord(id) {
  db.prepare("DELETE FROM media_assets WHERE id=?").run(id);
}
function cleanSessions() {
  db.prepare("DELETE FROM admin_sessions WHERE expires_at<?").run(Date.now());
}
module.exports = {
  db,
  dataDir,
  defaultContent,
  normalizeContent,
  contentHash,
  getDocument,
  saveDraft,
  publishDraft: publishTx,
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
};
