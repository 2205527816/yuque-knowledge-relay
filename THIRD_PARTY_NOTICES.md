# 第三方资源与许可

本项目 MIT 授权，但随包附带的资源各有自己的许可，使用时请一并遵守。

## 字体

**Noto Sans SC**（`app/static/fonts/NotoSansSC-VF.ttf`）

- 许可：SIL Open Font License 1.1
- 来源：Google Fonts / Noto 项目
- 全文见 [licenses/OFL-1.1.txt](licenses/OFL-1.1.txt)

使用的目的是让界面在缺少中文字体的环境里也能正常显示。因为体积较大（约 17 MB），如果不需要，可以删除该文件并移除 `app/static/style.css` 中的 `@font-face` 规则。

## Go 依赖

直接依赖：

| 模块 | 许可 |
|---|---|
| `github.com/JohannesKaufmann/html-to-markdown` | MIT |
| `github.com/PuerkitoBio/goquery` | BSD-3-Clause |
| `github.com/microcosm-cc/bluemonday` | BSD-3-Clause |
| `github.com/yuin/goldmark` | MIT |
| `golang.org/x/image` | BSD-3-Clause |
| `gopkg.in/yaml.v3` | MIT |
| `modernc.org/sqlite` | BSD-3-Clause |

完整依赖树与各模块许可可通过以下命令查看：

```bash
cd goapp
go list -m all
go mod download -json | grep -E '"Path"|"GoMod"'
```

各模块的具体许可文本位于其模块目录中，随 `go.sum` 锁定版本。

## 图标与印章

`app/static/icons.svg` 与 `app/static/kr-bauhaus-20261118/seals.svg` 为本项目自制，随本项目一同按 MIT 授权。

## 外部服务

本程序会调用以下外部服务，使用前请阅读各自的服务条款：

- 语雀开放 API（写作能力取决于你的账号权限）
- 你配置的 OpenAI 兼容模型接口
- 百度百科与 Bing 搜索（仅在 `explain.online` 为真时）
- 可选的 GitHub API（仅在启用备份时）

这些服务的数据处理与费用由各自的提供方决定，本项目不做中介也不代为计费。
