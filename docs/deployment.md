# 運用・デプロイガイド

Linux サーバーで sh-mcp を常駐させ、[Cloudflare Tunnel](https://developers.cloudflare.com/cloudflare-one/networks/connectors/cloudflare-tunnel/get-started/) で公開する手順です。常駐の仕組み（systemd・launchd など）は各自の環境に合わせて用意してください。

## 1. 配置

```bash
sudo install -m 0755 sh-mcp-linux-amd64 /usr/local/bin/sh-mcp
```

## 2. 常駐させる

sh-mcp は普段使っている自分のユーザーで動かしてください。専用ユーザーで動かすと、作られたファイルを自分で編集できない、`git` が所有者違いで止まる、`~/.gitconfig` やビルドキャッシュが使えない、といった問題が起きます。

設定は環境変数をファイルにまとめ、`0600` で保護します。トークンは `sh-mcp --gen-token` で生成します。

```dotenv
SH_MCP_MODE=full
SH_MCP_ALLOW_DIR=/home/you/projects
SH_MCP_PUBLIC_URL=https://mcp.yourdomain.example
SH_MCP_AUTH_TOKEN=<生成したトークン>
SH_MCP_AUDIT_LOG=/var/log/sh-mcp/audit.jsonl
```

systemd の例です（`/etc/sh-mcp/env` に上の設定を置いた場合）。

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

起動後、`curl http://127.0.0.1:8080/healthz` で `ok` が返れば動いています。

注意点:

* **`PATH`**: systemd から起動すると `PATH` が最小限になり、`~/go/bin` や nvm の `node` などが見つかりません。必要なディレクトリを `PATH` に含めてください。エージェントから見えるツールは `get_environment` で確認できます。
* **環境変数**: `.bashrc` などは読まれません。基本リストにない変数は `--env-passthrough` で渡します（[設定リファレンス](configuration.md#子プロセスの環境変数)）。
* **`journalctl`**: 自分のユーザーが `systemd-journal`（または `adm`）グループに入っていないと、他のサービスのログは読めません。
* **再起動**: 実行中のジョブと中継先の MCP サーバーも止まります。

## 3. Cloudflare Tunnel で公開

1. Tunnel を作成し、サーバーで `cloudflared` を動かします。
2. Public Hostname に `SH_MCP_PUBLIC_URL` と同じホスト名、Service に `http://127.0.0.1:8080` を設定します。
3. `curl https://mcp.yourdomain.example/.well-known/oauth-authorization-server` で応答を確認します。

ホスト名が `SH_MCP_PUBLIC_URL` と一致しないと `403` になります。Cloudflare Access を使う場合は、`/mcp`・`/oauth/*`・`/.well-known/*` を対象から外してください（OAuth クライアントが接続できなくなります）。

## 4. クライアントの接続

### Gemini アプリ

[カスタムアプリ](https://support.google.com/gemini/answer/17209137?hl=en&co=GENIE.Platform%3DAndroid) か [デベロッパーモード](https://developers.google.com/connected-apps/mcp/dev-mode/usage?hl=ja) で `https://mcp.yourdomain.example/mcp` を指定し、同意画面で `SH_MCP_AUTH_TOKEN` を入力します。

### Claude Code（リモート）

```bash
claude mcp add --transport http sh-mcp https://mcp.yourdomain.example/mcp \
  --header "Authorization: Bearer $SH_MCP_AUTH_TOKEN"
```

### ローカル（`stdio`）

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

## 5. ログローテーション

`/etc/logrotate.d/sh-mcp` の例です。

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

## 6. トラブルシューティング

* `403 Forbidden: invalid Host header`: プロキシ／Tunnel のホスト名と `--public-url` が一致していません。
* `--allow-dir is required when serving HTTP`: HTTP では `SH_MCP_ALLOW_DIR` が必須です。
* コマンドや MCP サーバーが見つからない: 起動時の `PATH` に含まれていません。
* トークンを全部無効にしたい: `SH_MCP_AUTH_TOKEN` を差し替えて再起動します。
