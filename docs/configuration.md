# 設定リファレンス

設定は CLI フラグか環境変数で行います（フラグ ＞ 環境変数 ＞ 既定値）。フラグ `--x-y` は環境変数 `SH_MCP_X_Y` に対応し、複数指定できる項目は環境変数では `;` で区切ります（例: `SH_MCP_ALLOW_DIR=/srv/a;/srv/b`）。`--deny-glob` だけは既定値に追記されます。

## フラグ・環境変数一覧

### 権限・ファイル・実行

| フラグ | 環境変数 | 既定値 | 説明 |
| :--- | :--- | :--- | :--- |
| `--mode` | `SH_MCP_MODE` | `read` | `read` / `ask` / `full`（[セキュリティ](security.md#2-モードと承認)） |
| `--allow-dir` | `SH_MCP_ALLOW_DIR` | `stdio`: カレント / `http`: 必須 | アクセスを許可するディレクトリ（複数可）。先頭が相対パスの基準 |
| `--auto-approve-command` | `SH_MCP_AUTO_APPROVE_COMMAND` | なし | `ask` で承認なしに実行できるプログラム（複数可、`ask` でのみ指定） |
| `--deny-glob` | `SH_MCP_DENY_GLOB` | 後述 | ファイル操作ツールから保護する glob（複数可）。先頭 `!` で例外 |
| `--max-file-bytes` | `SH_MCP_MAX_FILE_BYTES` | 5 MiB | 読み書き・検索するファイルのサイズ上限 |
| `--shell` | `SH_MCP_SHELL` | Unix: `/bin/sh -c` / Windows: PowerShell | `run_shell` が使うシェル |
| `--env-passthrough` | `SH_MCP_ENV_PASSTHROUGH` | なし | コマンド・MCP サーバーに追加で渡す環境変数（複数可、末尾 `*` で前方一致） |
| `--mcp-server` | `SH_MCP_MCP_SERVER` | なし | 中継する stdio MCP サーバー（`name=command args...`、複数可） |

### HTTP・認証・ログ

| フラグ | 環境変数 | 既定値 | 説明 |
| :--- | :--- | :--- | :--- |
| `--listen` | `SH_MCP_LISTEN` | なし | 指定すると HTTP で待ち受け（例: `127.0.0.1:8080`） |
| `--public-url` | `SH_MCP_PUBLIC_URL` | なし | 公開 URL（例: `https://mcp.example.com`）。HTTP と OAuth を有効化 |
| `--auth-token-file` | `SH_MCP_AUTH_TOKEN_FILE` | なし | 認証トークンを読むファイル |
| `--insecure-no-auth` | `SH_MCP_INSECURE_NO_AUTH` | `false` | 認証なしの HTTP を許可（検証用） |
| `--audit-log` | `SH_MCP_AUDIT_LOG` | stderr | 監査ログ（JSONL）の出力先 |
| `--version` / `--gen-token` / `--update` | — | — | バージョン表示 / 認証トークン生成 / 最新リリースへの更新 |

## 認証トークン

HTTP では 16 文字以上のトークンが必須です。`ps` で見えないよう CLI 引数では受け付けず、`$SH_MCP_AUTH_TOKEN` か `--auth-token-file` のどちらかで渡します。

## 既定の `--deny-glob`

`**/.env`, `**/.env.*`, `**/*.pem`, `**/*.key`, `**/*id_rsa*`, `**/*id_ed25519*` を保護し、`.env.example` / `.env.sample` / `.env.template` は例外です。既定の保護を外すときも `!` を使います（例: `--deny-glob '!**/testdata/**/*.pem'`）。

## MCP サーバーの中継（`--mcp-server`）

```bash
sh-mcp \
  --mode ask \
  --allow-dir /home/user/projects \
  --mcp-server "github=github-mcp-server stdio" \
  --env-passthrough GITHUB_PERSONAL_ACCESS_TOKEN \
  --public-url https://mcp.yourdomain.example
```

サーバー名は英数字・`-`・`_` の 1〜32 文字で、最大 16 個です。初回利用時に起動し、30 分使われないと停止します。

## 子プロセスの環境変数

コマンドと MCP サーバーには、以下の基本リストと `--env-passthrough` の変数だけを渡します（`SH_MCP_AUTH_TOKEN` は常に除外）。対話待ちで止まらないよう `GIT_TERMINAL_PROMPT=0`, `GIT_PAGER=cat`, `PAGER=cat` を付けます。

* Unix: `PATH`, `HOME`, `USER`, `LOGNAME`, `SHELL`, `LANG`, `LANGUAGE`, `TERM`, `TZ`, `TMPDIR`, `LC_*`, `XDG_*`
* Windows: `PATH`, `PATHEXT`, `SystemRoot`, `SystemDrive`, `windir`, `COMSPEC`, `TEMP`, `TMP`, `USERPROFILE`, `USERNAME`, `HOMEDRIVE`, `HOMEPATH`, `APPDATA`, `LOCALAPPDATA`, `ProgramData`, `ProgramFiles`, `ProgramFiles(x86)`, `CommonProgramFiles`, `PROCESSOR_ARCHITECTURE`, `NUMBER_OF_PROCESSORS`, `OS`, `PSModulePath`
* 共通: Go・Rust・Java・Node・Python のツールチェーンの場所（`GOPATH`、`CARGO_HOME`、`JAVA_HOME`、`NVM_DIR`、`VIRTUAL_ENV` など）、プロキシ（`HTTP_PROXY` など）、CA 証明書（`SSL_CERT_FILE` など）

`SSH_AUTH_SOCK` や API キーなどは必要なら `--env-passthrough` で渡します。渡した値は `full` モードではエージェントからも読める点に注意してください。
