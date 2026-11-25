# sh-mcp

sh-mcpは、AIアシスタントにMCP経由でマシン上のファイル操作とコマンド実行を任せるためのサーバーです。ほぼほぼコーディングエージェントと同じような作業ができます。Go製のシングルバイナリで、ローカル（`stdio`）とリモート（Streamable HTTP）の両方で使えます。

## 主な特徴

* Linux / macOS / Windows（amd64 / arm64）対応。設定ファイル不要で、フラグか環境変数（`SH_MCP_*`）だけで設定できます。
* 許可ディレクトリ外へのアクセス禁止、機密ファイル（`.env`、秘密鍵など）の既定保護、監査ログ。
* `--public-url` を指定すると OAuth 2.1 認可サーバーが有効になり、Gemini などから接続できます。
* 長時間のコマンドはバックグラウンドジョブとして続行し、後から出力の取得や停止ができます。
* 同じマシン上の `stdio` 専用 MCP サーバーを、sh-mcp 経由でリモートから使えます。

## インストール

[GitHub Releases](https://github.com/yh2237/sh-mcp/releases) からバイナリを取得して配置します。

```bash
chmod +x sh-mcp-linux-amd64
sudo mv sh-mcp-linux-amd64 /usr/local/bin/sh-mcp
```

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
  --mode ops \
  --allow-dir /home/user/projects \
  --allow-command df \
  --allow-command free \
  --allow-command journalctl \
  --public-url https://mcp.yourdomain.example
```

`127.0.0.1:8080` で待ち受けるので、[Cloudflare Tunnel](https://developers.cloudflare.com/cloudflare-one/networks/connectors/cloudflare-tunnel/get-started/) 等で公開し、クライアントから `https://mcp.yourdomain.example/mcp` に接続します。OAuth の同意画面では `SH_MCP_AUTH_TOKEN` を入力します。

## 動作モード

| モード | ファイル読み取り・検索 | ファイル変更 | コマンド実行 | `run_shell` | MCP 中継 |
| :--- | :---: | :---: | :--- | :---: | :--- |
| `read`（既定） | 可 | 不可 | 不可 | 不可 | `--mcp-server` 指定分のみ |
| `ops` | 可 | 不可 | `--allow-command` のみ | 不可 | `--mcp-server` 指定分のみ |
| `full` | 可 | 可 | 任意コマンド | 可 | 指定分＋動的起動/停止 |

`ops` の制限は sh-mcp のツールに対するもので、許可したコマンド自身の動作は防げません。（[詳細](docs/security.md)）

## ドキュメント

* [設定リファレンス](docs/configuration.md)
* [ツール仕様](docs/tools.md)
* [セキュリティ・OAuth・監査ログ](docs/security.md)
* [運用・デプロイガイド](docs/deployment.md)

## ライセンス

[MIT License](LICENSE)
