# HTTP 接口与命令行参数

本项目只有一套 HTTP 接口，Web 页面和脚本调用的是同一批地址。所有接口默认不鉴权，请按 [DEPLOY.md](DEPLOY.md) 放在需要认证的入口后面。

## 约定

- 页面类接口返回 HTML，表单类接口成功后以 `303` 重定向回页面，消息通过 `?msg=` 与 `?error=` 传递。
- 除 `/ingest/simpread` 返回 JSON 外，其余数据接口为 JSON 或 HTML。
- 请求体上限：上传与导入为 10 MB，表单类为 8–32 MB。

## 页面

### `GET /`

文章队列首页。

| 查询参数 | 说明 |
|---|---|
| `q` | 按标题或原文链接搜索，最长 200 字 |
| `lib_id` | 只看某个库 |
| `retention` | `active`（默认）/ `archived` / `trash` / `all` |
| `status` | 按处理状态过滤，取值同下方状态表 |
| `page` | 页码，从 1 开始 |
| `per_page` | 每页条数，1–100，默认 20 |
| `batch` | 显示指定批次的进度 |
| `done` | 存在时弹出「全部处理完成」 |
| `msg` / `error` | 顶部提示 |

### `GET /docs/{id}`

文章审核页。包含正文预览、注释词卡片、库打分详情、处理进度、冲突提示（发生冲突时显示「强制覆盖远端」按钮）。

### `GET /libraries`

知识库列表，展示每个库的倍率与档案入口。

### `GET /libraries/{id}/profile`

查看某个库的分类档案（Markdown 原文，可直接编辑）。

### `GET /icons`

图标总览页，开发时用于核对图标是否齐全。

## 导入

### `POST /scan`

扫描 `inbox/` 目录下的 `.md` / `.html` / `.htm` 文件，建批次并开始处理，随后重定向到 `/?batch=<id>`。

无请求体。目录为空时提示「inbox 里没有新文件」。

### `POST /upload`

`multipart/form-data`，字段名 `file`。

只接受 `.md` / `.html` / `.htm`，单文件最大 10 MB。成功后重定向到 `/?batch=<id>`。

### `POST /ingest/simpread`

给简悦 Webhook 用的接口，**返回 JSON**，不重定向。

请求体可以是 JSON，也可以是纯文本/HTML：

| JSON 字段 | 说明 |
|---|---|
| `content` / `html` / `markdown` | 正文，三者取第一个非空的 |
| `title` | 标题，可选 |
| `url` | 原文链接，可选 |

行为：

1. 正文超过 10 MB 返回 413；
2. 写入 `inbox/`，文件名形如 `simpread-20261119-101500-1a2b3c4d.html`；
3. 复用当前正在运行的 `simpread` 批次（没有就新建），并立刻开始处理。

响应示例：

```json
{"ok": true, "file": "simpread-20261119-101500-1a2b3c4d.html", "batch_id": 12}
```

错误响应为 `{"ok": false, "error": "empty content"}` 等，状态码 400 / 413 / 500。

> 注意：该接口**不校验**任何密钥。`X-Ingest-Key` 只是给反向代理鉴权用的约定头。

## 审核与写回

### `POST /docs/{id}/save`

保存勾选状态与释义，不写回语雀。

表单字段：

- `term_ids`（可多个）：被勾选的词条 ID；
- `explanation_<termId>`：该词修改后的释义。

未出现在 `term_ids` 里的词会被置为未勾选。

### `POST /docs/{id}/preview`

返回渲染后的正文 HTML 片段（不含页面框架），用于页面局部刷新预览。

表单字段同 `save`。

### `POST /docs/{id}/write`

提交写回。表单字段：

- `term_ids` / `explanation_<termId>`：可选，存在时先保存；
- `force=1`：强制覆盖远端（跳过冲突检查）。

行为：入队后立即重定向。若存在下一篇待审核文章，跳到下一篇；否则回到首页并带上完成标记。

冲突时不会入队，而是把错误写进文章记录，页面显示「强制覆盖远端」按钮。

### `POST /docs/{id}/retry`

重跑一篇文章的处理：重置状态 → 缺分类则重新分类 → 缺文档则重新建档 → 重新抽词释义。

### `POST /docs/{id}/retention`

修改保留状态，表单字段 `retention_status`，取值 `active` / `archived` / `trash`。

### `POST /docs/{id}/library`

修改所属库。表单字段 `library_id`。

会在新库建档、尝试删除旧库文档、清空哈希基线；若文章尚未抽词，会立即按新库倍率抽词。

### `POST /docs/{id}/terms/{termId}/regenerate`

重新生成某个词的释义，**绕过缓存**。无请求体。

### `POST /docs/{id}/terms/add`

手动添加注释词。

| 字段 | 说明 |
|---|---|
| `term` | 必填，必须能在正文可注区域定位到 |
| `explanation` | 可选，留空则调用模型生成 |

定位不到的词会被拒绝并提示「正文中找不到该词」。

## 批量

### `POST /docs/batch`

表单字段 `action` 决定动作：

| action | 作用 | 额外字段 |
|---|---|---|
| `archive` | 归档选中文章 | `doc_ids` |
| `trash` | 移入回收站 | `doc_ids` |
| `activate` | 恢复到进行中 | `doc_ids` |
| `purge` | 永久删除本地记录（仅回收站内） | `doc_ids` |
| `retry_failed_active` | 重试所有进行中且失败的文章 | 无 |

可附带 `q` / `lib_id` / `retention` / `page` / `per_page` / `status`，用于操作后跳回原视图。

## 库设置

### `POST /libraries/{id}`

修改该库的候选词倍率。表单字段 `multiplier`，取值范围 0.5–2.0。

### `POST /libraries/{id}/profile`

保存该库的分类档案。表单字段 `content`，内容是完整 Markdown，会直接覆盖 `profiles/<库名>.md`，并递增档案版本号。

## 进度

### `GET /api/progress`

返回当前批次进度与待写回数量，供页面轮询。

| 查询参数 | 说明 |
|---|---|
| `batch` | 指定批次 ID；不传则取最新批次 |

响应示例：

```json
{
  "writes": 0,
  "batch": {
    "id": 12,
    "status": "running",
    "total": 8,
    "done": 5,
    "pct": 62,
    "current": {"title": "示例文章", "stage_detail": "3/9 路径依赖", "stage_pct": 63}
  }
}
```

没有批次时返回 `{"batch": null, "writes": 0}`。

`done` 的统计口径是「已到达终态」的文章数，包含 `terms_ready`、`pending_class`、`failed`、`written`。

## 其他

### `GET /healthz`

返回纯文本 `ok`，用于探活。

### `GET /sw.js`

返回一段注销 Service Worker 的脚本，用于清理旧版本遗留的缓存。

### `GET /img?u=<图片地址>&r=<所属文章>&view=queue`

图片代理。用于把文章里的外部图片取回来展示，可选生成本地缩略图缓存。只接受 `http` / `https` 且带主机名的地址。

### `GET /static/*`

静态资源：样式、图标、印章、字体。

## 命令行参数

```
knowledge-relay [参数]
```

| 参数 | 默认 | 说明 |
|---|---|---|
| `-addr` | `:8011` | 监听地址 |
| `-root` | `/opt/knowledge-relay` | 项目根目录，配置、数据、模板都相对它定位 |
| `-check <路径>` | 空 | 只做数据库迁移检查：补列、`quick_check`、输出各表计数 |
| `-check-yuque` | `false` | 用当前配置读取第一个库的目录，验证语雀通道是否可用 |
| `-reflow-doc <id>` | `0` | 重排指定文章正文的换行（用于修正 HTML 导入的长段落） |
| `-reflow-force` | `false` | 配合 `-reflow-doc`，未指定时只做 dry-run |

示例：

```bash
# 常规启动
./knowledge-relay -addr 127.0.0.1:8010 -root /opt/knowledge-relay

# 迁移前先检查数据库
./knowledge-relay -root /opt/knowledge-relay -check /opt/knowledge-relay/data/kb.db

# 验证语雀凭据与目录权限
./knowledge-relay -root /opt/knowledge-relay -check-yuque

# 先看换行重排差异，再真正写入
./knowledge-relay -root /opt/knowledge-relay -reflow-doc 12
./knowledge-relay -root /opt/knowledge-relay -reflow-doc 12 -reflow-force
```

> 程序按相对路径读取 `templates/`，因此**工作目录必须是 `goapp/`**（systemd 的 `WorkingDirectory` 或手动 `cd goapp` 都行）。

## 环境变量

读取 `.env`（若存在），已存在的环境变量优先。

| 变量 | 覆盖的配置项 |
|---|---|
| `YUQUE_TOKEN` | `yuque.token` |
| `YUQUE_SESSION` | `yuque.session` |
| `YUQUE_CTOKEN` | `yuque.ctoken` |
| `LLM_API_KEY` | `llm.api_key` |
| `EXPLAIN_PROXY` | `explain.proxy` |
| `GITHUB_TOKEN` | 备份功能使用的 GitHub 令牌 |

`YUQUE_SESSION` 与 `YUQUE_CTOKEN` 是可选的网页端会话凭据，只有在使用网页通道时才需要。
