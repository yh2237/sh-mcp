# 設定リファレンス

設定はCLIフラグと環境変数で指定します。優先順位は CLIフラグ ＞ 環境変数 ＞ デフォルト値 です。

* フラグ `--x-y` は環境変数 `SH_MCP_X_Y` に対応します。
* 複数指定できる項目を環境変数で渡す場合は `;` で区切ります（例: `SH_MCP_ALLOW_COMMAND=df;free;journalctl`）。
* CLIフラグは環境変数を上書きします（`--deny-glob` のみ既存値へ追記）。

## フラグ・環境変数一覧

### 権限・ファイル・実行

| フラグ | 環境変数 | デフォルト | 説明 |
| :--- | :--- | :--- | :--- |
| `--mode` | `SH_MCP_MODE` | `read` | `read` / `ops` / `full` |
| `--allow-dir` | `SH_MCP_ALLOW_DIR` | `stdio`: カレント / `http`: 必須 | 許可ルートディレクトリ（複数可）。先頭が相対パスの基準（`default_dir`） |
| `--allow-command` | `SH_MCP_ALLOW_COMMAND` | なし | `ops` モードで許可するプログラム名（複数可、`ops` で必須、`read`/`full` で指定不可）。引数次第で任意コードを実行できるものは指定しない（[セキュリティ](security.md)参照） |
| `--deny-glob` | `SH_MCP_DENY_GLOB` | 後述の 6 件 | 保護対象の glob パターン（複数可・既定へ追記） |
| `--max-file-bytes` | `SH_MCP_MAX_FILE_BYTES` | `5242880`（5 MiB） | 読み書き・検索・画像返却のファイルサイズ上限 |
| `--shell` | `SH_MCP_SHELL` | Unix: `/bin/sh -c` / Win: PowerShell | `run_shell` が使うシェルコマンド列（末尾にスクリプトを付与） |
| `--env-passthrough` | `SH_MCP_ENV_PASSTHROUGH` | なし | コマンド・MCP サーバーへ追加で渡す環境変数名（複数可）。末尾 `*` で前方一致（例: `AWS_*`） |
| `--mcp-server` | `SH_MCP_MCP_SERVER` | なし | 中継するローカル stdio MCP サーバー（`name=command args...`、複数可） |

### HTTP・認証・ログ

| フラグ | 環境変数 | デフォルト | 説明 |
| :--- | :--- | :--- | :--- |
| `--listen` | `SH_MCP_LISTEN` | なし（指定時 HTTP 有効化） | 待ち受けアドレス（例: `127.0.0.1:8080`） |
| `--public-url` | `SH_MCP_PUBLIC_URL` | なし | 公開 HTTPS URL（パスなし）。HTTP（既定 `127.0.0.1:8080`）・OAuth 2.1・`Host` 検証を有効化（ループバックのみ `http://` 可） |
| `--auth-token-file` | `SH_MCP_AUTH_TOKEN_FILE` | なし | 認証トークンの読み込みファイル（`$SH_MCP_AUTH_TOKEN` と排他） |
| `--insecure-no-auth` | `SH_MCP_INSECURE_NO_AUTH` | `false` | 無認証 HTTP を明示許可（ローカル検証用） |
| `--audit-log` | `SH_MCP_AUDIT_LOG` | なし（`stderr`） | JSONL 監査ログ出力先（`0600` 作成、`SIGHUP` で再オープン） |
| `--version` / `--gen-token` | — | `false` | バージョン表示 / 128 bit ランダムトークン生成 |

## 認証トークン

HTTP モードでは `--insecure-no-auth` がない限り 16 文字以上のトークンが必須です。`ps` からの漏洩を防ぐため CLI 引数では受け付けず、`$SH_MCP_AUTH_TOKEN` または `--auth-token-file` のどちらか一方のみで渡します。

## デフォルトの `--deny-glob`

`**/.env`, `**/.env.*`, `**/*.pem`, `**/*.key`, `**/*id_rsa*`, `**/*id_ed25519*`（シンボリックリンクの実体パスにも適用）。

## ローカル MCP サーバー中継（`--mcp-server`）

```bash
sh-mcp \
  --mode ops \
  --allow-dir /home/user/projects \
  --allow-command df \
  --mcp-server "github=github-mcp-server stdio" \
  --env-passthrough GITHUB_PERSONAL_ACCESS_TOKEN \
  --public-url https://mcp.yourdomain.example
```

[GitHub MCP Server](https://github.com/github/github-mcp-server) のバイナリを `PATH` に置いた例です。サーバー名は英数字・`-`・`_` の1〜32文字、同時登録は最大 16 サーバーです。初回利用時に起動し、30 分アイドルで自動停止します。

## 子プロセスの環境変数

基本リスト＋`--env-passthrough` のみを継承し、`SH_MCP_AUTH_TOKEN` は必ず除外します。対話ブロック防止のため `GIT_TERMINAL_PROMPT=0`, `GIT_PAGER=cat`, `PAGER=cat` を自動付与します。

`--env-passthrough` で渡した変数は MCP サーバーだけでなく `run_command` / `run_shell` の子プロセスにも渡ります。`full` モードではエージェントが `env` 等で値を読めるため、API トークンなどを渡す場合はその前提で範囲を絞ってください。

* Unix 基本リスト: `PATH`, `HOME`, `USER`, `LOGNAME`, `SHELL`, `LANG`, `LANGUAGE`, `TERM`, `TZ`, `TMPDIR`, `LC_*`, `XDG_*`
* Windows 基本リスト: `PATH`, `PATHEXT`, `SystemRoot`, `SystemDrive`, `windir`, `COMSPEC`, `TEMP`, `TMP`, `USERPROFILE`, `USERNAME`, `HOMEDRIVE`, `HOMEPATH`, `APPDATA`, `LOCALAPPDATA`, `ProgramData`, `ProgramFiles`, `ProgramFiles(x86)`, `CommonProgramFiles`, `PROCESSOR_ARCHITECTURE`, `NUMBER_OF_PROCESSORS`, `OS`, `PSModulePath`
* 共通（ツールチェーン・プロキシ・CA 証明書）: `GOPATH`, `GOROOT`, `GOBIN`, `GOCACHE`, `GOMODCACHE`, `GOENV`, `GOFLAGS`, `GOTOOLCHAIN`, `GOPROXY`, `GONOPROXY`, `GOPRIVATE`, `GONOSUMDB`, `GOSUMDB`, `CARGO_HOME`, `RUSTUP_HOME`, `JAVA_HOME`, `NVM_DIR`, `NODE_PATH`, `PYENV_ROOT`, `VIRTUAL_ENV`, `HTTP_PROXY`, `HTTPS_PROXY`, `NO_PROXY`, `ALL_PROXY`（小文字も可）, `SSL_CERT_FILE`, `SSL_CERT_DIR`, `NODE_EXTRA_CA_CERTS`

`SSH_AUTH_SOCK`（ssh-agent 経由の `git push` 等）や API キーのように権限を伴う変数は基本リストに含めていません。必要な場合は `--env-passthrough` で明示します。
