# mybot

一个最小可用的 Telegram Bot，用来把 Telegram 对话桥接到本机/远程机器上的 `codex`（以及其他 CLI）。目标是“能用、好调试、易扩展”。

当前默认推荐用 **Codex exec(JSONL) 模式**，避免 TUI 光标/终端能力问题，输出也更干净，适合 Telegram。

## 功能概览

- Telegram <-> Codex CLI（默认 `codex exec --json`）
- 单人使用：`TELEGRAM_ALLOWLIST` 白名单
- 长会话：exec 模式会持久化 `thread_id`，重启后自动续聊
- 文件上传：Telegram 发文件自动保存到 `WORKDIR/UPLOAD_DIR/`，并把路径作为上下文喂给 codex
- 安全删除：`/delete` 只允许删除 `UPLOAD_DIR` 下文件
- skills 管理：`/skills` 列表/安装/删除/查看目录
- 输出格式化：使用 Telegram HTML（自动转义）；多行/列表类输出使用 `<pre>`

## 快速开始（本机 long polling）

1. 准备 `.env`

参考 `.env.example` 创建 `.env`，至少需要：

- `TELEGRAM_BOT_TOKEN`
- `TELEGRAM_ALLOWLIST`

2. 启动

```bash
cd /Users/junhao/Project/mybot
DOTENV_OVERRIDE=1 GOCACHE=/tmp/gocache go run ./cmd/mybot
```

说明：
- 程序会自动加载当前目录 `.env`（默认不覆盖已存在的环境变量）
- 若你希望 `.env` 覆盖 shell 里已 export 的变量，使用 `DOTENV_OVERRIDE=1`

## 配置说明（环境变量）

### Telegram

- `TELEGRAM_BOT_TOKEN`：BotFather 生成的 token（必填）
- `TELEGRAM_ALLOWLIST`：允许使用的 `chat_id`（必填，逗号分隔）
- `TELEGRAM_LOG_UNKNOWN`：`1` 表示把“未在白名单的 chat_id”打到服务端日志（用于首次获取 chat_id）
- `TELEGRAM_HIDE_STATUS`：`1` 表示不在 Telegram 输出中显示内部状态行（比如 resumed/started）
- `TELEGRAM_SET_COMMANDS`：`1` 表示启动时调用 Telegram `setMyCommands`，让指令在聊天输入框的菜单里可见（默认 1）

### 代理（国内常用）

如果需要代理访问 Telegram 或 codex 的网络端点：

- HTTP 代理：
  - `HTTP_PROXY=http://127.0.0.1:890`
  - `HTTPS_PROXY=http://127.0.0.1:890`
- SOCKS5 代理：
  - `HTTP_PROXY=socks5://127.0.0.1:890`
  - `HTTPS_PROXY=socks5://127.0.0.1:890`
- 建议总是设置：
  - `NO_PROXY=127.0.0.1,localhost`

不需要为了运行程序去 `unset HTTP_PROXY...`，除非你怀疑代理配置导致 Telegram 连接失败，用来定位问题。

### 工作目录与日志

- `WORKDIR`：codex 的工作目录、以及上传目录的根目录（默认：启动时当前目录）
- `LOG_DIR`：日志目录（默认：`logs`）
  - exec 模式的会话状态会写入：`LOG_DIR/state.json`
  - 每个 session 会有 transcript：`LOG_DIR/sessions/<session_id>.log`

### 上传（文件/patch）

- `UPLOAD_DIR`：上传文件保存子目录（默认：`uploads`）
- `MAX_UPLOAD_BYTES`：上传最大字节数（默认：`20971520`，20MB）

保存路径规则：
- 实际落盘：`WORKDIR/UPLOAD_DIR/<timestamp>_<original_name>`
- Telegram 会回显相对路径：`uploads/<timestamp>_<original_name>`

### Codex

- `CODEX_CMD`：默认 `codex`；也可用 `/bin/bash` 等交互式 CLI 做 smoke test
- `CODEX_ARGS`：额外参数（会附加在内部“安全 QoL 参数”之后）
- `CODEX_ENABLE_SEARCH`：`1` 表示为 `codex` 增加全局 `--search`（“最新资讯”类需求建议开启）
- `CODEX_DRIVER`：`exec` 或 `interactive`
  - 默认：当 `CODEX_CMD` 是 `codex` 时为 `exec`，否则为 `interactive`
- `CODEX_SKIP_GIT_REPO_CHECK`：`1` 表示 exec 模式增加 `--skip-git-repo-check`

推荐（生产/Telegram）：
- `CODEX_CMD=codex`
- `CODEX_DRIVER=exec`

### Skills

- `SKILLS_DIR`：skills 根目录
  - 默认：`$CODEX_HOME/skills`；否则 `~/.codex/skills`

## Telegram 指令

- `/new`：开始一个新会话（exec 模式会清掉该 chat 的持久化 thread_id）
- `/status`：查看当前会话状态
- `/cancel`：中断当前执行（exec 模式会对正在运行的 `codex exec` 进程发送 SIGINT）
 - `/schedule`：定时任务管理（见下）

### 上传与删除

- 直接发送文件（Document）：
  - bot 会保存文件并自动把路径作为上下文发给 codex
- `/uploads`：列出最近 20 个上传文件
- `/delete <name-or-path>`（别名 `/rm`）：
  - 只允许删除 `UPLOAD_DIR` 内文件
  - `<name-or-path>` 可以是：
    - `uploads/20260209_131717_xxx.txt`
    - `xxx.txt`（会匹配最新的 `*_xxx.txt`）

### Skills 管理

- `/skills` 或 `/skills ls`：列出已安装 skills
- `/skills path`：显示 skills 目录
- `/skills install <git-url-or-local-path> [name]`：
  - 从本地目录复制安装，或 git clone 安装（`--depth 1`）
- `/skills rm <name>`：删除 skill（只会在 skills 目录内删除）

## 发版说明

### 创建新版本

使用 git tag 创建版本并推送，GitHub Actions 会自动构建多平台二进制文件并创建 Release：

```bash
# 创建版本标签
git tag v1.0.0

# 推送标签到远程
git push origin v1.0.0
```

GitHub Actions 会自动：
- 构建 Linux (amd64/arm64)、macOS (Intel/Apple Silicon)、Windows 平台的二进制文件
- 创建 GitHub Release
- 上传所有构建产物

### 下载使用

在 [Releases](https://github.com/gongjunhao/mybot/releases) 页面下载对应平台的二进制文件：

- `mybot-linux-amd64`: Linux x86_64
- `mybot-linux-arm64`: Linux ARM64
- `mybot-darwin-amd64`: macOS Intel
- `mybot-darwin-arm64`: macOS Apple Silicon
- `mybot-windows-amd64.exe`: Windows x86_64

下载后：
1. 赋予执行权限（Linux/macOS）：`chmod +x mybot-*`
2. 复制 `.env.example` 为 `.env` 并配置
3. 运行：`./mybot-linux-amd64`（或对应平台的文件）

## 常见问题

### 1) 提示 telegram: Unauthorized

几乎总是 token 无效或被旧环境变量覆盖。

- 确认 `.env` 里的 `TELEGRAM_BOT_TOKEN` 是最新的（BotFather revoke 后旧 token 立刻失效）
- 使用 `DOTENV_OVERRIDE=1` 强制 `.env` 覆盖 shell 变量

### 2) codex TUI 乱码/终端报错

建议用 `CODEX_DRIVER=exec`。exec 模式使用 JSONL 输出，不依赖 TTY 光标能力，更适合 Telegram。

## 定时任务（每天 HH:MM）

支持两种方式创建定时任务：

1) 自然语言（推荐）

示例：
- `每天上午9点获取最新AI资讯发送给我`

2) 指令

- `/schedule` 或 `/schedule ls`：列出任务
- `/schedule add HH:MM <prompt>`：新增/覆盖同一时间点的任务
- `/schedule rm <id>`：删除任务
- `/schedule on <id>` / `/schedule off <id>`：启用/停用

说明：
- 定时任务持久化在 `LOG_DIR/schedules.json`
- 触发时按机器本地时区（`time.Local`）
