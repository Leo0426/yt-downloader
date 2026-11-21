# YT Downloader

YT Downloader 提供桌面和 Web 两种运行方式。Web 版在服务器上下载，浏览器可添加链接、配置保存路径、管理账户和队列，并取回已完成的文件。桌面版提供独立窗口和网页嗅探。

## Web 版与 Docker 部署

在项目目录使用 Docker Compose v2 和 BuildKit 构建并启动：

```sh
docker compose up -d --build
docker compose logs downloader
```

打开 [Web 工作台](http://localhost:8080)。首次创建管理员时输入启动日志中的初始化令牌，或使用部署时通过 `YTDL_SETUP_TOKEN` 指定的值。已有账户直接登录。进入「设置 → 下载 → 默认保存位置」可填写下载根目录内的绝对路径或相对子目录，例如 `/downloads/videos` 或 `videos`。保存后，新任务会写到该目录，普通用户提交的任务沿用管理员设置。有文件权限的用户可在已完成的任务上点「下载文件」，把服务器上的成品保存到自己的设备。

| 容器路径 | 默认存储 | 内容 |
| --- | --- | --- |
| `/data` | 命名卷 `ytd-data` | 账户、角色、队列、设置、日志、工具和缓存 |
| `/downloads` | 命名卷 `ytd-downloads` | 下载成品及在界面中配置的子目录 |

**把文件直接保存到宿主机**时，可以用两个独立的宿主目录替换默认卷。以下命令将当前用户的 UID/GID 传给容器，使用该用户有写权限的文件夹：

```sh
mkdir -p "${HOME}/YTDownloader/data" "${HOME}/YTDownloader/videos"
YTDL_UID="$(id -u)" YTDL_GID="$(id -g)" \
YTDL_DATA_MOUNT="${HOME}/YTDownloader/data" \
YTDL_DOWNLOAD_MOUNT="${HOME}/YTDownloader/videos" \
docker compose up -d --build
```

例如，Web 默认保存位置填 `courses`，文件会出现在宿主机的 `~/YTDownloader/videos/courses`。正式部署可把以上变量的实际值写到项目旁的 `.env`，供后续 Compose 命令复用。重建容器时要继续挂载原来的两个卷；`docker compose down -v` 会删除 Compose 管理的命名卷。卷的备份和恢复可参考 [Docker 卷文档](https://docs.docker.com/engine/storage/volumes/)。

**从其他设备访问**时，可将 `YTDL_BIND_ADDRESS` 设置为服务器的局域网 IP，并将 `YTDL_PUBLIC_URL` 设置为浏览器实际使用的完整地址，例如 `http://192.168.1.20:8080`。用 `YTDL_PORT` 修改宿主端口。通过 HTTPS 反向代理访问时，可保持端口仅发布至回环地址，将 `YTDL_PUBLIC_URL` 设置为 `https://download.example.com`，并让反向代理保留外部 `Host`。默认配置仅允许从本机访问。

**从源码运行**只需 Go 1.25+。执行下方命令启动服务；macOS 也可以双击 `scripts/start-web.command`。如果使用已构建的 macOS Web 压缩包，解压后双击 `start-web.command` 即可启动，程序会在终端显示访问地址和首次初始化令牌。

```sh
go run . -web -listen 127.0.0.1:8080 \
  -data-dir "$HOME/.yt-downloader-web" \
  -download-root "$HOME/Downloads/YT Downloader Web"
```

直接执行 `go run . -web` 时，Web 账户与队列默认在系统配置目录的 `yt-downloader-web` 子目录，下载根目录默认是 `~/Downloads/YT Downloader Web`。桌面版和 Web 版默认各用一套数据目录。迁移已有工作台时，先停止原实例并备份数据；同一数据目录同时只能由一个实例使用。迁移后，保存在新下载根之外的历史路径需要重新提交链接。

Web 模式可在容器或普通服务器上持续处理队列，关闭浏览器后任务继续运行。桌面专属的网页嗅探、打开系统目录和退出应用仅在桌面版提供。Docker 镜像已包含 yt-dlp、EJS、Deno、FFmpeg 和 ffprobe，空数据卷也可直接开始下载。在「设置 → 组件」安装的更新会保存在 `/data` 卷。

## 桌面版使用

1. 解压与电脑系统及 CPU 匹配的 ZIP。macOS 双击 **YT Downloader.app**，Windows 双击 **YT Downloader.exe**，Linux 运行 **YT Downloader**，也可在解压目录执行 `./install-desktop.sh` 把图标和快捷入口加入系统应用菜单。
   首次打开时按提示创建本机工作台的管理员。已有账户可直接登录。macOS 首次保存到 Downloads 时，按系统提示允许访问下载文件夹。
2. 将链接粘贴或拖到输入区，每行一个。点「加入队列」或按 ⌘ + Enter（Windows、Linux 使用 Ctrl + Enter）。输入区下方会显示完整的保存路径，点「更改」可选择文件夹。
3. 在侧栏选择「进行中」「已完成」或「失败」可筛选队列，窄窗口中筛选入口位于顶部。队列可以按标题、文件名或链接搜索，也可以播放成品或打开所在文件夹。可以取消、重试并导出完整日志。点击任务右侧的垃圾桶可删除队列记录，仍在进行的下载会停止，已保存的媒体文件会保留。关闭窗口会停止当前下载，未完成的队列下次启动时继续处理。

支持 yt-dlp 能解析的视频和网页内嵌视频。每个链接取一个视频，含多个条目的页面取第一项。YouTube 的时间参数只影响在线播放起点，下载器保存完整视频。默认输出为 MP4。可为每批任务选择 MKV、M4A 或 MP3，也可在设置中更改默认格式。MP4 优先保留视频编码并输出 AAC 音频，兼容模式使用 H.264。MKV 优先复制源视频和音频编码，遇到不兼容的编码时尝试 H.264 与 AAC。M4A 和 MP3 仅保存音轨，可用于视频链接或独立音频。无音轨的视频仍可输出 MP4 或 MKV。

## 账户与角色权限

从旧版本升级前，请完全退出旧版下载器，再打开新版并沿用原数据目录。首个管理员由使用本机启动入口的人创建。之后由管理员在导航的账户名下打开「用户管理」添加用户、分配角色、重置密码或停用账户。用户可以在「我的账户」修改自己的密码或退出登录。

| 内置角色 | 默认可做的事 |
| --- | --- |
| 管理员 | 查看和管理整个队列、配置 AI 与下载设置、维护存储和组件、管理用户及角色。 |
| 操作员 | 添加下载和使用网页嗅探，查看、取消、重试或删除自己的任务，读取其日志、打开成品或启动 AI 辅助下载。 |
| 观察者 | 只读查看自己名下的任务。 |

在「角色与权限」可新建自定义角色。一个用户可以同时拥有多个角色，其权限取并集。例如，可在「观察者」之外分配含「查看所有用户的任务」的角色，允许查看整个队列；如需读取这些任务的日志，还需授予「查看任务日志」。跨用户取消、重试或删除任务需要同时具备查看与管理所有任务，以及查看和管理本人任务的权限。用户管理员只能管理自己权限范围内的账户和角色，内置角色只读，系统始终保留至少一个启用的内置管理员。

新任务会记录提交账户。升级前的任务显示为「历史任务」，由具备全队列权限的用户查看和管理。停用、删除用户或重置其密码会使该用户已有会话失效。角色变更从下一次请求开始生效。重新启动下载服务后需要再次登录。

账户与角色保存在当前工作台的数据目录中。RBAC 控制下载器界面、桌面操作和本地 API。操作系统决定谁能直接读取本机数据目录和已下载的文件；多人共用同一个系统账户时，输出目录仍然共享。

## AI 辅助下载

YouTube 或其他来源下载失败后，可以在该任务旁点「AI 辅助下载」。首次使用会打开「设置 → AI 辅助」。选择协议，填写 API 基础地址和服务所需的密钥，点「获取模型列表」，选中支持文本对话的模型，再点「保存设置」。回到失败任务后即可启动辅助下载。

- **OpenAI** 可选 Chat Completions 或 Responses，两者的官方基础地址均为 `https://api.openai.com/v1`。使用兼容网关时，按网关提供的协议和地址填写。旧配置沿用 Chat Completions。[OpenAI 模型列表](https://developers.openai.com/api/reference/resources/models/methods/list/)
- **Anthropic** 选择 Anthropic Messages，官方基础地址为 `https://api.anthropic.com/v1`，也支持兼容该协议的网关。[Anthropic 模型列表](https://platform.claude.com/docs/en/api/models/list)
- **本机 Ollama** 可选 OpenAI Chat Completions，地址填 `http://127.0.0.1:11434/v1`，密钥可留空。[Ollama 接口说明](https://docs.ollama.com/api/openai-compatibility)

「获取模型列表」直接查询当前填写的服务，填写模型名称前即可使用。服务未开放列表接口时，可以手动输入模型 ID 并保存。密钥仅在本次运行的内存中保留，重启或更换服务地址、协议后需重新填写。

模型接收本地归纳的故障摘要和错误类别、来源是否为 YouTube、输出格式、超时及重试次数。视频链接、保存路径和完整日志留在本机。模型可为这一次重试选择延长超时并逐步增加重试间隔、改用 IPv4，或优先使用音画合一的媒体流。单文件流的可用画质由源站决定。任务沿用原保存位置、输出格式和画质设置。

在任务行和「原因与日志」里可查看方案或处理建议。遇到需要登录、视频已下架等问题时，模型可以提示下一步并结束本次尝试。普通「重试」仍直接使用任务原来的设置。

## 网页嗅探

在导航中点击「网页嗅探」，或使用 ⌘ + Shift + S（Windows、Linux 使用 Ctrl + Shift + S）。在窗口顶部打开网页，再播放视频。左侧会列出匹配的媒体文件、HLS（M3U8）和 DASH（MPD）地址。可以筛选、复制、暂停或清空结果，勾选资源后点「加入下载队列」。

嗅探使用独立的临时网页会话。识别结果留在窗口中，手动提交后才进入下载队列。选择 M4A 或 MP3 后，纯音频也能加入队列。嗅探按响应类型和扩展名识别媒体，扩展的类型包括 AVI、WMV、3GP、M2TS、FLAC 和 AIFF。提交的地址需要能由下载器单独访问，浏览窗口的 Cookie、登录会话和请求头不会随地址传给下载器。带有效期的地址请尽快提交。受 DRM 保护的媒体不支持下载。

## 设置

- **下载**可设置默认保存位置、默认输出格式、分辨率上限、H.264 兼容模式和同时下载的任务数（1–6 个，默认 2 个）。画质上限按视频短边计算。添加下载时可在「画质上限」为当前批次单独指定分辨率。每项任务会保存提交时的下载和网络选项，重试也沿用这些设置。
- **网络**可设置连接超时、失败重试次数和下载限速。限速填 0 表示不限速。
- **外观**支持跟随系统、浅色和深色主题，磨砂玻璃可单独开关。桌面菜单可用 ⌘ + ,（Windows、Linux 使用 Ctrl + ,）打开设置。
- **组件**显示版本和安装位置，并可检查更新。
- **存储**显示缓存占用，可打开日志和数据目录、清理缓存、导入或导出配置，并可载入默认设置。导出的是已保存设置，导入和恢复默认值需点击「保存设置」后才生效。

点击「暂停队列」后，已开始的任务可完成，等待中的任务在恢复后继续。调低并发也会让已开始的任务继续完成。暂停状态会随队列保存。

## 核心组件

首次启动检查 yt-dlp、FFmpeg、ffprobe 和 Deno 的本机版本。新下载需要的工具如果缺失，会自动从发布渠道下载安装。在「设置 → 组件」点击「检查并更新」可查询最新发布。每个组件包先校验大小和 SHA-256，再试运行新工具，通过后切换到新版本。本机通过 Homebrew 等方式安装的可用工具也可以直接复用。

| 组件 | 发布渠道 |
| --- | --- |
| yt-dlp | [yt-dlp 稳定版](https://github.com/yt-dlp/yt-dlp/releases/latest) |
| Deno | [Deno 稳定版](https://github.com/denoland/deno/releases/latest) |
| FFmpeg、ffprobe（macOS） | [Jellyfin FFmpeg 便携构建](https://github.com/jellyfin/jellyfin-ffmpeg/releases/latest) |
| FFmpeg、ffprobe（Windows、Linux） | [yt-dlp 的 FFmpeg 滚动构建](https://github.com/yt-dlp/FFmpeg-Builds/releases/latest) |

首次安装组件和检查更新需要能访问 GitHub。macOS 便携版的更新节奏由 Jellyfin 决定，已有的较新系统版本会保留。桌面版仅监听本机回环地址，启动链接带有本次运行的访问凭据。Web 版的监听地址与公开站点地址可分别配置。

## 去重、缓存与恢复

- 同一批次或队列中仍在处理的同一视频、输出目录、输出格式和画质配置只排队一次。支持 YouTube 短链接、观看页、Shorts 和带时间参数的链接。
- 再次提交已完成的视频时会重新校验当前文件。缓存按提取器、媒体 ID 和画质配置区分视频，已解析网页会记录与视频的对应关系。不同输出格式使用独立的缓存对象，旧版 MP4 缓存可以继续复用。普通直链另按来源区分，避免不同网站同名文件冲突。
- 桌面版使用默认画质和编码模式时，可识别输出目录已有的 YouTube 文件，例如 `标题 [视频ID].mp4`。也可复用符合该命名的 WebM、MKV、MOV，在本机转换为 MP4。
- 已完成的缓存保存独立副本、大小和 SHA-256。换保存目录或删除导出的媒体后可从缓存恢复。遇到内容不同的同名文件，导出副本使用新的文件名。
- 未完成的下载按媒体和格式保留分片，下次处理时刷新临时媒体地址。续传取决于源站支持 Range 或分片、且所选格式仍然可用。下载后会检查格式、轨道和时长。
- 「设置 → 存储」显示缓存占用，可以在队列空闲时清理。清理会移除完整缓存、网址映射和未完成的分片。已经导出的媒体、任务记录和日志继续保留。完整缓存与导出副本都会占用磁盘空间。

## 日志与程序数据

每项任务有独立日志，记录尝试时间、地址、输出位置、下载与转码命令、工具诊断和退出错误。重试追加到原日志。失败任务的「原因与日志」显示诊断和处理建议，也可保存完整日志。对已识别视频 ID 的 YouTube 连接中断，重试会尝试复用同一视频的本地文件。组件更新另存于 `logs/tools.log`。

| 系统 | 默认程序数据目录 |
| --- | --- |
| macOS | `~/Library/Application Support/yt-downloader` |
| Windows | `%AppData%\yt-downloader` |
| Linux | `$XDG_CONFIG_HOME/yt-downloader` 或 `~/.config/yt-downloader` |

`accounts.json` 保存账户、加盐密码哈希和自定义角色，`queue.json` 保存任务队列、归属、暂停状态和设置，`cache/` 保存媒体及断点，`tools/` 保存程序管理的工具。用 `YTDL_DATA_DIR` 可以为桌面应用指定独立数据目录。调试 Go 服务时可用 `-data-dir` 和 `-no-browser` 指定数据目录并在终端打印启动地址。

## 从源码运行和构建

Web 服务从源码构建需要 Go 1.25+。构建桌面版还需要 Node.js 22.12+。打包程序自带 Electron 和 Go 下载服务，最终用户解压即可启动。

```sh
npm ci --prefix desktop
npm --prefix desktop start
go test ./...
node --test desktop/media-candidates.test.cjs
go run ./scripts/build.go
go run ./scripts/build.go -all
```

普通构建只打包当前平台。`-all` 从同一份源码生成 macOS、Windows、Linux 的 amd64 和 arm64 六个 ZIP，以及各自解压后的程序，默认位于 `dist/`，设置 `YTDL_BUILD_OUT` 可输出到独立目录。Linux 自动下载的组件面向使用 glibc 的发行版。当前发布物为本地构建，未做 macOS 公证或 Windows 发布者签名。应用图标由 `go run ./scripts/icon` 生成，适用于 macOS、Windows、Linux 和应用窗口。

桌面窗口与系统交互位于 `desktop`。其他模块包括 `internal/deps`（工具发现、更新与校验）、`internal/download`（解析、下载、校验与复用）、`web`（界面）。测试覆盖重复提交、失败重试、缓存损坏、跨目录恢复和中断续传。下载引擎集成测试需本机能找到 yt-dlp、FFmpeg 和 ffprobe。要额外验证当下发布源的真实冷安装，可运行：

```sh
YTDL_TEST_LIVE_INSTALL=1 go test -v -run TestLiveInstall ./internal/deps
```
