# エージェント向けセットアップガイド

このドキュメントはユーザーに頼まれて sh-mcp をインストール・設定する AI エージェント向けの手順です。上から順に進めてください。

まずはREADME.mdやその他ドキュメントを読んでこのMCPサーバーについて把握してください。

## 1. ユーザーに確認すること

推測で決めず、次の点をユーザーに確認してから作業を始めます。

1. 使い方: 同じマシンの CLI エージェントから使う（ローカル / `stdio`）か、スマホのアプリなど別の場所から使う（リモート / HTTP）か。まぁ多分リモートです。このMCPサーバーをstdioで使う旨味は正直無い。
2. モード: `read`（読み取りのみ）/ `ask`（編集とコマンド実行を毎回承認）/ `full`（制限なし）のどれにするか。迷っている場合は `read` か `ask` を勧めます。
3. アクセスを許可するディレクトリ: 作業対象のプロジェクトのディレクトリなど。
4. `ask` の場合は承認なしで実行してよいコマンド（任意）。
5. リモートの場合は公開 URL と公開方法（Cloudflare Tunnel など）と、トンネルを動かすのがこのマシンか別のマシンか。
6. 同じマシンの stdio 専用の MCP サーバーを中継するか（任意。`--mcp-server`）。

次のことは、ユーザーが明示的に望まない限りしないでください。

* `full` モードを選ぶ、`/` やホームディレクトリ全体を `--allow-dir` に指定する
* `--auto-approve-command` に `git`・`ssh`・`docker`・シェル・インタプリタなど、引数次第で任意のコードを実行できるプログラムを入れる（[理由](security.md#2-モードと承認)）。git は承認なしで使える読み取り専用の `git_status` / `git_diff` / `git_log` があります。

## 2. インストール

Linux / macOS

```bash
curl -fsSL https://raw.githubusercontent.com/yh2237/sh-mcp/main/install.sh | sh
```

Windows（PowerShell）

```powershell
irm https://raw.githubusercontent.com/yh2237/sh-mcp/main/install.ps1 | iex
```

インストール先は `~/.local/bin/sh-mcp`（Windows は `%LOCALAPPDATA%\Programs\sh-mcp\sh-mcp.exe`）です。`sh-mcp --version` で確認します。すでに入っている場合は `sh-mcp --update` で最新版にします（動いているサーバーは再起動が必要です）。

MCP クライアントの設定には `PATH` に頼らず絶対パスを書いてください。

## 3. ローカル（`stdio`）で登録する

ユーザーが使っている MCP クライアントに登録します。以下の `/path/to/project` と `--mode` は手順 1 で確認した内容に置き換えます。Windows では実行ファイルを `C:\Users\<ユーザー>\AppData\Local\Programs\sh-mcp\sh-mcp.exe` に置き換えます（JSON や TOML では `\` を `\\` と書きます）。

Claude Code

```bash
claude mcp add --scope user sh-mcp -- ~/.local/bin/sh-mcp --mode read --allow-dir /path/to/project
```

Codex CLI（`~/.codex/config.toml`）

```toml
[mcp_servers.sh-mcp]
command = "/home/you/.local/bin/sh-mcp"
args = ["--mode", "read", "--allow-dir", "/path/to/project"]
```

`mcpServers` 形式の JSON を使うクライアント

```json
{
  "mcpServers": {
    "sh-mcp": {
      "command": "/home/you/.local/bin/sh-mcp",
      "args": ["--mode", "read", "--allow-dir", "/path/to/project"]
    }
  }
}
```

## 4. リモート（HTTP）で公開する

[運用・デプロイガイド](deployment.md)に沿って進めます。要点は次のとおりです。

* sh-mcp はユーザー自身のアカウントで動かします（専用ユーザーは作らない）。
* 設定は環境変数ファイルにまとめ、`0600` にします。認証トークンはチャットやコマンド履歴に出さず、ファイルに直接書き込みます。

  ```bash
  printf 'SH_MCP_AUTH_TOKEN=%s\n' "$(sh-mcp --gen-token)" >> /path/to/env
  ```

* トークンの値はユーザーに伝えず、保存場所だけを伝えます。OAuth の同意画面で入力するのはユーザー自身です。
* `SH_MCP_PUBLIC_URL` だけを指定すると `127.0.0.1:8080` で待ち受けます。トンネルが別のマシンにある場合は `SH_MCP_LISTEN=<このマシンの LAN の IP>:8080` も指定し、ファイアウォールでその通信を許可します。
* 常駐の設定（systemd など）と公開（Cloudflare Tunnel など）はユーザーの環境に合わせて行います。Windows と macOS の常駐方法はユーザーと相談してください。
* 起動後に次を確認します。公開ホスト名以外の `Host` は `403` になるので、LAN のアドレスで確かめるときは `Host` ヘッダーを付けます。

  ```bash
  curl -H "Host: <公開ホスト>" http://<待ち受けアドレス>/healthz                   # ok
  curl https://<公開ホスト>/.well-known/oauth-authorization-server                # JSON
  ```

* クライアントの接続はユーザーに次のとおり案内します。
  * Gemini アプリ: カスタムアプリの URL に `https://<公開ホスト>/mcp` を指定します。クライアント ID とシークレットの欄は空欄のままにします（sh-mcp が自動で登録します）。同意画面で `SH_MCP_AUTH_TOKEN` を入力します。編集やコマンドの前に出る許可カードで「常に許可」を選ぶと、以後は確認が出なくなります。
  * Claude Code: `claude mcp add --transport http sh-mcp https://<公開ホスト>/mcp --header "Authorization: Bearer $SH_MCP_AUTH_TOKEN"`

## 5. 動作確認

クライアントを再起動（または再接続）し、sh-mcp の `get_environment` ツールを呼んで次を確認します。

* モードと許可ディレクトリが意図どおりか。
* `ask` の場合、`approvals:` の行。sh-mcp が自分で承認を求めるのか、クライアントの確認画面に任せるのかが分かります。
* `tools_on_path` に必要なツールがあるか。足りない場合は、起動時の `PATH` を確認してください（systemd では最小限になります）。git がない環境では git ツールは出ません。

## 関連ドキュメント

* [設定リファレンス](configuration.md): 全フラグ・環境変数、子プロセスに渡す環境変数
* [ツール仕様](tools.md): モードごとのツール、対話シェル（`tty`）、git ツール
* [セキュリティ・OAuth・監査ログ](security.md): 保護の範囲、モードと承認の仕組み、トークンの失効
* [運用・デプロイガイド](deployment.md): 常駐・公開・クライアント接続・トラブルシューティング
