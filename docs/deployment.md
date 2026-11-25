# 運用・デプロイガイド

Linux サーバーで `sh-mcp` を常駐させ、[Cloudflare Tunnel](https://developers.cloudflare.com/cloudflare-one/networks/connectors/cloudflare-tunnel/get-started/) で HTTPS 公開する際の手順と注意点です。`sh-mcp` は実行ファイル 1 つで動きます。常駐の仕組み（systemd・launchd・タスクスケジューラ等）は各自の環境に合わせて用意してください。

## 1. 配置

```bash
sudo install -m 0755 sh-mcp-linux-amd64 /usr/local/bin/sh-mcp
```

## 2. 常駐させる

### 自分のアカウントで動かす

`sh-mcp` は普段使っている自分のユーザーで動かすことを前提にしています。専用ユーザーで動かすと、次のような問題が起きます。

* エージェントが作ったファイルの所有者が専用ユーザーになり、自分で編集できない
* 他ユーザー所有のリポジトリとして `git` が `detected dubious ownership` で止まる
* `~/.gitconfig`・SSH 鍵・ビルドキャッシュ（`~/.cache` 等）が使えず、`git commit` や `go build` / `npm install` が失敗する

### 設定のポイント

* **設定の置き場所**: フラグの代わりに環境変数（`SH_MCP_*`）をファイルにまとめ、`0600` で保護します。トークンは `sh-mcp --gen-token` で生成します。

  ```dotenv
  # full モード例（ops モードの場合は SH_MCP_MODE=ops と SH_MCP_ALLOW_COMMAND=df;free;journalctl 等を指定）
  SH_MCP_MODE=full
  SH_MCP_ALLOW_DIR=/home/you/projects
  SH_MCP_PUBLIC_URL=https://mcp.yourdomain.example
  SH_MCP_AUTH_TOKEN=<生成したトークン>
  SH_MCP_AUDIT_LOG=/var/log/sh-mcp/audit.jsonl
  ```

* **`PATH`**: systemd などから起動すると `PATH` が最小限になり、`~/go/bin`・`~/.local/bin`・nvm の `node` などが見つかりません。必要なディレクトリを含めた `PATH` を明示します。エージェントから見えるツールは `get_environment` の `tools_on_path` で確認できます。
* **子プロセスの環境変数**: シェルの初期化ファイル（`.bashrc` 等）は読まれません。コマンドに渡す変数は、基本リストにないものを `--env-passthrough` で指定します（[設定リファレンス](configuration.md#子プロセスの環境変数)）。
* **監査ログの再オープン**: `SIGHUP` で監査ログを開き直すので、リロード操作で `SIGHUP` を送れるようにしておくと logrotate と組み合わせられます。
* **`ops` モードで `journalctl`**: 自分のユーザーが `systemd-journal`（または `adm`）グループに入っていないと、他サービスのログが読めません。
* **再起動の影響**: 再起動すると実行中のジョブ（`timeout_sec: 0` で起動したサーバー等）と下流 MCP サーバーも終了します。

### systemd の例

最小限の例です。`you` と各パスは自分の環境に合わせてください。設定ファイルは `/etc/sh-mcp/env`（`root` 所有・`0600`）に置いています。

```ini
[Unit]
Description=sh-mcp
After=network-online.target
Wants=network-online.target

[Service]
User=you
EnvironmentFile=/etc/sh-mcp/env
Environment=PATH=/home/you/go/bin:/home/you/.local/bin:/usr/local/bin:/usr/bin:/bin
ExecStart=/usr/local/bin/sh-mcp
ExecReload=/bin/kill -HUP $MAINPID
LogsDirectory=sh-mcp
Restart=on-failure

[Install]
WantedBy=multi-user.target
```

`LogsDirectory=sh-mcp` により、監査ログ用の `/var/log/sh-mcp` が `User=` の所有で作成されます。起動後は `curl -i http://127.0.0.1:8080/healthz` で `ok` が返ることを確認します。

## 3. Cloudflare Tunnel で HTTPS 公開

1. Cloudflare Zero Trust で Tunnel を作成し、サーバーに `cloudflared` を常駐させます。
2. Public Hostname に `SH_MCP_PUBLIC_URL` と同じホスト名、Service に `HTTP` / `127.0.0.1:8080` を登録します。
3. OAuth メタデータの応答を確認します。

```bash
curl -fsS https://mcp.yourdomain.example/.well-known/oauth-authorization-server | jq .
```

> 注意: Tunnel 側のホスト名と `SH_MCP_PUBLIC_URL` が一致しないと `Host` 検証で `403` になります。また、Cloudflare Access を前段に置くと Gemini などの OAuth クライアントが接続できなくなります。`/mcp`・`/oauth/*`・`/.well-known/*` は Access の対象から外してください。

## 4. クライアント接続例

### Gemini アプリ（リモート HTTP＋OAuth）

[カスタムアプリ](https://support.google.com/gemini/answer/17209137?hl=en&co=GENIE.Platform%3DAndroid) または [デベロッパーモード](https://developers.google.com/connected-apps/mcp/dev-mode/usage?hl=ja) で、URL に `https://mcp.yourdomain.example/mcp` を指定し、OAuth 同意画面で `SH_MCP_AUTH_TOKEN` を入力します。

### Claude Code（リモート HTTP）

```bash
claude mcp add --transport http sh-mcp https://mcp.yourdomain.example/mcp \
  --header "Authorization: Bearer $SH_MCP_AUTH_TOKEN"
```

### ローカル CLI（`stdio`）

```json
{
  "mcpServers": {
    "sh-mcp": {
      "command": "/usr/local/bin/sh-mcp",
      "args": ["--mode", "full", "--allow-dir", "/home/you/projects"]
    }
  }
}
```

## 5. ログローテーション・トラブルシューティング

`/etc/logrotate.d/sh-mcp` の設定例です（上の systemd の例を使う場合）。

```text
/var/log/sh-mcp/audit.jsonl {
    weekly
    rotate 8
    compress
    missingok
    notifempty
    create 0600 you you
    postrotate
        systemctl reload sh-mcp >/dev/null 2>&1 || true
    endscript
}
```

* `403 Forbidden: invalid Host header`: リバースプロキシ／Tunnel の `Host` ヘッダーと `--public-url` のホスト名が不一致です。
* `--allow-dir is required when serving HTTP`: HTTP モードでは `SH_MCP_ALLOW_DIR` の指定が必須です。
* コマンドや `--mcp-server` のプログラムが見つからない: 起動時の `PATH` に含まれていません（[常駐させる](#2-常駐させる)の `PATH` を参照）。
* 全トークンの一括失効: `SH_MCP_AUTH_TOKEN` を差し替えて再起動します（`/oauth/revoke` による個別の失効は再起動で消えるため、確実に失効させるにはこの方法を使います）。
