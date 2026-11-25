# sh-mcp

sh-mcpは、AIアシスタントにMCP経由でマシン上のファイル操作とコマンド実行を行わせるためのサーバーです。

Go製のシングルバイナリで、ローカル（`stdio`）とリモート（Streamable HTTP）の両方で使えます。

ほぼほぼコーディングエージェントととかと同じような作業ができます。あとstdioで動いてるMCPサーバーをsh-mcp経由でリモートから使用したりとかもできます。

## 主な特徴

* Linux / macOS / Windows（amd64 / arm64）対応。設定ファイル不要で、フラグか環境変数（`SH_MCP_*`）だけで設定できます。
* 許可ディレクトリ外へのアクセス禁止、機密ファイル（`.env`、秘密鍵など）の既定保護、監査ログ。
* `--public-url` を指定すると OAuth 2.1 認可サーバーが有効になり、Gemini などから接続できます。
* 長時間のコマンドはバックグラウンドジョブとして続行し、後から出力の取得や停止ができます。
* 疑似端末（`tty`）で起動すれば、シェル・REPL・ssh などを対話的に操作できます（Windows は ConPTY を使用）。
* 同じマシン上の `stdio` 専用 MCP サーバーを、sh-mcp 経由でリモートから使えます。

## インストール

[GitHub Releases](https://github.com/yh2237/sh-mcp/releases) からバイナリを取得して実行権限付けるだけです。

以下でも `~/.local/bin`（Windows は `%LOCALAPPDATA%\Programs\sh-mcp`）にインストールできます。

Linux / macOS:

```bash
curl -fsSL https://raw.githubusercontent.com/yh2237/sh-mcp/main/install.sh | sh
```

Windows（PowerShell）:

```powershell
irm https://raw.githubusercontent.com/yh2237/sh-mcp/main/install.ps1 | iex
```

更新は `sh-mcp --update` です。

ソースからビルドする場合（Go 1.26 以上）:

```bash
git clone https://github.com/yh2237/sh-mcp.git
cd sh-mcp
make build
```

## クイックスタート

### ローカル（`stdio`）

既定は読み取り専用（`--mode read`）で、対象はカレントディレクトリです。

```bash
sh-mcp
sh-mcp --mode full --allow-dir ~/projects
```

### リモート公開（`http`＋OAuth）

```bash
export SH_MCP_AUTH_TOKEN="$(sh-mcp --gen-token)"
sh-mcp \
  --mode ask \
  --allow-dir /home/user/projects \
  --auto-approve-command df \
  --public-url https://mcp.yourdomain.example
```

`127.0.0.1:8080` で待ち受けるので、[Cloudflare Tunnel](https://developers.cloudflare.com/cloudflare-one/networks/connectors/cloudflare-tunnel/get-started/) 等で公開し、クライアントから `https://mcp.yourdomain.example/mcp` に接続します。OAuth の同意画面では `SH_MCP_AUTH_TOKEN` を入力します。

## 動作モード

| モード | ファイル読み取り | ファイル変更 | コマンド実行 |
| :--- | :---: | :--- | :--- |
| `read`（既定） | 可 | 不可 | 不可 |
| `ask` | 可 | 毎回承認 | 毎回承認（`--auto-approve-command` のプログラムは承認なし） |
| `full` | 可 | 自由 | 自由 |

承認は、クライアントが対応していれば sh-mcp が確認を出し、対応していなければクライアント自身の確認画面に任せます（Gemini アプリでは操作ごとに許可カードが出ます）。詳しくは[セキュリティ](docs/security.md)を見てください。

## ドキュメント

* [設定リファレンス](docs/configuration.md)
* [ツール仕様](docs/tools.md)
* [セキュリティ・OAuth・監査ログ](docs/security.md)
* [運用・デプロイガイド](docs/deployment.md)
* [エージェント向けセットアップガイド](docs/agent_setup.md): AI エージェントにインストールと設定を任せる場合は、このファイルを読ませてください。

## ライセンス

[MIT License](LICENSE)
