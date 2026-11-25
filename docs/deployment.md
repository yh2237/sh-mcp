# 運用・デプロイガイド

Linux サーバーに `sh-mcp` を `systemd` で常駐させ、[Cloudflare Tunnel](https://developers.cloudflare.com/cloudflare-one/networks/connectors/cloudflare-tunnel/get-started/) で HTTPS 公開する手順です。

## 1. ユーザー作成とバイナリ配置

```bash
sudo useradd --system --no-create-home --shell /usr/sbin/nologin sh-mcp
sudo install -m 0755 sh-mcp-linux-amd64 /usr/local/bin/sh-mcp
sudo mkdir -p /var/log/sh-mcp
sudo chown sh-mcp:sh-mcp /var/log/sh-mcp
sudo chmod 0750 /var/log/sh-mcp
```

作業ディレクトリを `sh-mcp` ユーザーから参照・書き込みできるよう、必要に応じてグループ権限を設定します。Ubuntu 21.04 以降などホームディレクトリが `0750` の環境では、`sh-mcp` ユーザーはそのままでは `/home/user` 配下に入れません。`sudo usermod -aG user sh-mcp` でホームディレクトリのグループに加えるか、作業ディレクトリをホーム外（`/srv` 等）に置きます。

## 2. 環境変数ファイル（`/etc/sh-mcp/env`）

トークンは `/usr/local/bin/sh-mcp --gen-token` で生成します。ディレクトリを作成し、`sudoedit` で以下の内容を書き込みます。

```bash
sudo install -d -m 0750 /etc/sh-mcp
sudoedit /etc/sh-mcp/env
```

```dotenv
# full モード例（ops モードの場合は SH_MCP_MODE=ops と SH_MCP_ALLOW_COMMAND=df;free;journalctl 等を指定）
SH_MCP_MODE=full
SH_MCP_ALLOW_DIR=/home/user/projects
SH_MCP_PUBLIC_URL=https://mcp.yourdomain.example
SH_MCP_AUTH_TOKEN=<生成したトークン>
SH_MCP_AUDIT_LOG=/var/log/sh-mcp/audit.jsonl
```

```bash
sudo chown root:root /etc/sh-mcp/env
sudo chmod 0600 /etc/sh-mcp/env
```

## 3. systemd サービス化

リポジトリの [`deploy/sh-mcp.service`](../deploy/sh-mcp.service) を配置し、`WorkingDirectory` と `ReadWritePaths` を `SH_MCP_ALLOW_DIR`・`SH_MCP_AUDIT_LOG` に合わせて更新します（`ProtectSystem=strict` 等のサンドボックス設定済み）。リポジトリを clone していない場合は GitHub から直接取得します。

```bash
curl -fsSLo sh-mcp.service https://raw.githubusercontent.com/yh2237/sh-mcp/main/deploy/sh-mcp.service
sudo install -m 0644 sh-mcp.service /etc/systemd/system/sh-mcp.service
sudoedit /etc/systemd/system/sh-mcp.service
sudo systemctl daemon-reload
sudo systemctl enable --now sh-mcp
curl -i http://127.0.0.1:8080/healthz
```

## 4. Cloudflare Tunnel で HTTPS 公開

1. Cloudflare Zero Trust で Tunnel を作成し、サーバーに `cloudflared` を常駐させます。
2. Public Hostname に `SH_MCP_PUBLIC_URL` と同じホスト名、Service に `HTTP` / `127.0.0.1:8080` を登録します。
3. OAuth メタデータの応答を確認します。

```bash
curl -fsS https://mcp.yourdomain.example/.well-known/oauth-authorization-server | jq .
```

> 注意: Tunnel 側のホスト名と `SH_MCP_PUBLIC_URL` が一致しないと `Host` 検証で `403` になります。

## 5. クライアント接続例

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
      "args": ["--mode", "full", "--allow-dir", "/home/user/projects"]
    }
  }
}
```

## 6. ログローテーション・トラブルシューティング

`/etc/logrotate.d/sh-mcp` の設定例です。

```text
/var/log/sh-mcp/audit.jsonl {
    weekly
    rotate 8
    compress
    missingok
    notifempty
    create 0600 sh-mcp sh-mcp
    postrotate
        systemctl reload sh-mcp >/dev/null 2>&1 || true
    endscript
}
```

* `403 Forbidden: invalid Host header`: リバースプロキシ／Tunnel の `Host` ヘッダーと `--public-url` のホスト名が不一致です。
* `--allow-dir is required when serving HTTP`: HTTP モードでは `SH_MCP_ALLOW_DIR` の指定が必須です。
* 全トークンの一括失効: `/etc/sh-mcp/env` の `SH_MCP_AUTH_TOKEN` を差し替えて `sudo systemctl restart sh-mcp` を実行します（`/oauth/revoke` による個別の失効は再起動で消えるため、確実に失効させるにはこの方法を使います）。
