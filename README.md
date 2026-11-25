# sh-mcp

sh-mcpは、自宅サーバー・VPS向けのGo製シングルバイナリMCPサーバーです。ローカル（`stdio`）とリモート（Streamable HTTP＋内蔵OAuth2.1）の両方に対応します。

## 主な特徴

* `CGO_ENABLED=0` の静的ビルドで Linux / macOS / Windows（amd64 / arm64）に対応。設定ファイル不要で、CLI フラグまたは環境変数（`SH_MCP_*`）のみで設定できます。
* `*os.Root` によるパス隔離、デフォルトの機密ファイル除外（`.env`, `.env.*`, `*.pem`, `*.key`, `*id_rsa*`, `*id_ed25519*`）、CSRF／Host 検証、JSONL 監査ログ（`SIGHUP` 再オープン対応）を備えます。
* `--public-url` を指定すると、DCR（RFC 7591）・PKCE（S256）・RFC 9728 対応のステートレス OAuth 2.1 認可サーバーが有効になります。
* 長時間コマンドは `wait_sec` 超過でバックグラウンドジョブへ移行し、差分出力の取得やプロセスツリーごとの終了ができます。
* 同一マシン上のローカル専用（`stdio`）MCP サーバーを、`sh-mcp` 経由でリモートから呼び出せます。

## インストール

GitHub Releasesからバイナリを取得して配置します。

```bash
chmod +x sh-mcp-linux-amd64
sudo mv sh-mcp-linux-amd64 /usr/local/bin/sh-mcp
```

ソースからビルドする場合（Go 1.26 以上）は以下を実行します。

```bash
git clone https://github.com/yh2237/sh-mcp.git
cd sh-mcp
make build
```

クロスコンパイルは `make cross` です。

## クイックスタート

### ローカル（`stdio`）

`--listen` と `--public-url` が未指定の場合、`stdio` モード（`--mode read`、対象はカレントディレクトリ）で起動します。

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

`127.0.0.1:8080` で待ち受けるため、[Cloudflare Tunnel](https://developers.cloudflare.com/cloudflare-one/networks/connectors/cloudflare-tunnel/get-started/) 等で `https://mcp.yourdomain.example` を転送し、クライアントから `https://mcp.yourdomain.example/mcp` へ接続します。同意画面では `SH_MCP_AUTH_TOKEN` を入力します。

## 動作モード

| モード | ファイル読み取り・検索 | ファイル変更 | コマンド実行 | `run_shell` | MCP 中継 |
| :--- | :---: | :---: | :--- | :---: | :--- |
| `read`（既定） | 可 | 不可 | 不可 | 不可 | `--mcp-server` 指定分のみ |
| `ops` | 可 | 不可 | `--allow-command` のみ | 不可 | `--mcp-server` 指定分のみ |
| `full` | 可 | 可 | 任意コマンド | 可 | 指定分＋動的起動/停止 |

`ops` の「ファイル変更 不可」は sh-mcp のファイル操作ツールについての制限です。許可したコマンド自身が行う変更は防げません。`--allow-command` には、引数次第で任意のコードを実行したりファイルを書き換えたりできるプログラム（`git`、`docker`、シェル、各種インタプリタ、`find`、`xargs`、`env`、`make` など）を指定しないでください（[詳細](docs/security.md)）。

## ドキュメント

* [設定リファレンス](docs/configuration.md)
* [ツール仕様](docs/tools.md)
* [セキュリティ・OAuth・監査ログ](docs/security.md)
* [運用・デプロイガイド](docs/deployment.md)

## 開発

CI（GitHub Actions）は push・プルリクエストごとに gofmt・`go vet`・govulncheck・クロスビルドと Linux / macOS / Windows でのテストを実行し、新たに公開された脆弱性を検出するため毎週月曜にも定期実行します。依存モジュールと Actions の更新は Dependabot が毎週まとめてプルリクエストにします。`v*` タグを push するとリリースビルドが走ります。

## ライセンス

[MIT License](LICENSE)
