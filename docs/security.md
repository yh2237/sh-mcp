# セキュリティ・OAuth・監査ログ

## 1. ファイルアクセス

* ファイル操作は `--allow-dir` の中に限られ、`..` やシンボリックリンクで外に出ることはできません。
* `--deny-glob` はシンボリックリンクのリンク先にも適用されます。
* Windows では UNC パス・ドライブ相対パス・代替データストリーム（`:`）を拒否します。
* `--deny-glob` が守るのはファイル操作ツールだけです。`full` モードのコマンドや `ops` で許可したプログラムは、プロセスの権限で読めるファイルをすべて読めます。

## 2. コマンド実行

* `--allow-command` は起動時に絶対パスへ解決し、後から `PATH` で差し替えられないようにします。
* `ops` で制限するのは起動できるプログラムだけで、引数は制限しません。引数次第で任意のコードを実行できるプログラムは許可しないでください。
  * 例: `git`（`-c core.pager=…` や `--upload-pack` で任意コマンドを実行でき、ファイルも変更できる）、`docker`（実質 root）、シェル、インタプリタ、`find`、`xargs`、`env`、`make`、`go`、`npm`
  * 向いているのは `df`, `free`, `uptime`, `journalctl` のような参照系のプログラムです。
* タイムアウト・`kill_job`・終了時は、子プロセスも含めてまとめて終了します。

## 3. HTTP

* HTTP では 16 文字以上の認証トークンが必須です（`--insecure-no-auth` を除く）。
* `--public-url` を指定すると、そのホスト名とループバック以外の `Host` ヘッダーを拒否します。
* ブラウザからのクロスオリジンのリクエストは拒否します。

## 4. OAuth

`--public-url` と認証トークンを設定すると、OAuth 2.1 の認可サーバーが有効になります。クライアントは動的に登録され（DCR）、同意画面で `SH_MCP_AUTH_TOKEN` を入力すると接続が許可されます。

| エンドポイント | 役割 |
| :--- | :--- |
| `/.well-known/oauth-protected-resource`, `/.well-known/oauth-authorization-server` | メタデータ |
| `/oauth/register` | クライアント登録 |
| `/oauth/authorize` | 同意画面（PKCE 必須、失敗は 1 分に 5 回まで） |
| `/oauth/token` | トークン発行・更新 |
| `/oauth/revoke` | トークン失効 |

* トークン類は `SH_MCP_AUTH_TOKEN` から作った鍵で署名し、サーバーには保存しません。再起動後もそのまま使えます。
* アクセストークンは 1 時間有効です。リフレッシュトークンは期限がなく、更新しても変わりません（Gemini との互換のため）。
* `/oauth/revoke` による失効は再起動すると消えます。確実に失効させるには `SH_MCP_AUTH_TOKEN` を差し替えて再起動します（全トークンが無効になります）。
* OAuth を有効にしても、`Authorization: Bearer <SH_MCP_AUTH_TOKEN>` で直接接続できます。

## 5. 監査ログ

全ツール呼び出しを JSONL で記録します（`--audit-log` 未指定時は stderr）。

```json
{"time":"2026-11-25T09:00:00Z","level":"INFO","msg":"tool_call","event":"tool_call","tool":"write_file","args_summary":{"bytes":128,"path":"main.go","sha256":"a1b2c3d4e5f60708"},"duration_ms":2,"ok":true,"client":"oauth:Gemini#1234abcd"}
```

* ファイル本文や `call_mcp_tool` の引数の値は記録しません（サイズ・ハッシュ・キー名のみ）。
* ログファイルは `0600` で作成し、`SIGHUP` で開き直します。
