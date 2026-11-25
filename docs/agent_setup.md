# エージェント向けセットアップガイド

このドキュメントは、ユーザーに頼まれて sh-mcp をインストール・設定する AI エージェント向けの手順です。上から順に進めてください。

## 1. ユーザーに確認すること

推測で決めず、次の点をユーザーに確認してから作業を始めます。

1. **使い方**: 同じマシンの CLI エージェントから使う（ローカル / `stdio`）か、スマホのアプリなど別の場所から使う（リモート / HTTP）か。
2. **モード**: `read`（読み取りのみ）/ `ask`（編集とコマンド実行を毎回承認）/ `full`（制限なし）のどれにするか。迷っている場合は `read` か `ask` を勧めます。
3. **アクセスを許可するディレクトリ**: 作業対象のプロジェクトのディレクトリなど。
4. `ask` の場合は**承認なしで実行してよいコマンド**（任意）。リモートの場合は**公開 URL と公開方法**（Cloudflare Tunnel など）。

次のことは、ユーザーが明示的に望まない限りしないでください。

* `full` モードを選ぶ、`/` やホームディレクトリ全体を `--allow-dir` に指定する
* `--allow-command` に `git`・`docker`・シェル・インタプリタなど、引数次第で任意のコードを実行できるプログラムを入れる（[理由](security.md#2-モードと承認)）
* `--insecure-no-auth` で認証なしの HTTP を公開する

## 2. インストール

Linux / macOS:

```bash
curl -fsSL https://raw.githubusercontent.com/yh2237/sh-mcp/main/install.sh | sh
```

Windows（PowerShell）:

```powershell
irm https://raw.githubusercontent.com/yh2237/sh-mcp/main/install.ps1 | iex
```

インストール先は `~/.local/bin/sh-mcp`（Windows は `%LOCALAPPDATA%\Programs\sh-mcp\sh-mcp.exe`）です。`sh-mcp --version` で確認します。MCP クライアントの設定には、`PATH` に頼らず絶対パスを書いてください。

## 3. ローカル（`stdio`）で登録する

ユーザーが使っている MCP クライアントに登録します。以下の `/path/to/project` と `--mode` は手順 1 で確認した内容に置き換えます。

Claude Code:

```bash
claude mcp add --scope user sh-mcp -- ~/.local/bin/sh-mcp --mode read --allow-dir /path/to/project
```

Codex CLI（`~/.codex/config.toml`）:

```toml
[mcp_servers.sh-mcp]
command = "/home/you/.local/bin/sh-mcp"
args = ["--mode", "read", "--allow-dir", "/path/to/project"]
```

`mcpServers` 形式の JSON を使うクライアント:

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
* 常駐の設定（systemd など）と公開（Cloudflare Tunnel など）はユーザーの環境に合わせて行います。Windows と macOS の常駐方法はユーザーと相談してください。
* 起動後、`curl http://127.0.0.1:8080/healthz` が `ok` を返すこと、`https://<公開ホスト>/.well-known/oauth-authorization-server` が JSON を返すことを確認します。

## 5. 動作確認

クライアントを再起動（または再接続）し、sh-mcp の `get_environment` ツールを呼んで、モードと許可ディレクトリが意図どおりかを確認します。コマンドやツールが見つからない場合は、`tools_on_path` と起動時の `PATH` を確認してください。

## 関連ドキュメント

* [設定リファレンス](configuration.md): 全フラグ・環境変数、子プロセスに渡す環境変数
* [ツール仕様](tools.md): モードごとのツールとパラメータ
* [セキュリティ・OAuth・監査ログ](security.md): 保護の範囲、モードと承認の仕組み、トークンの失効
* [運用・デプロイガイド](deployment.md): 常駐・公開・クライアント接続・トラブルシューティング
