# セキュリティ・OAuth・監査ログ

## 1. パス隔離

* 起動時に各 `--allow-dir` を `os.OpenRoot` で開き、すべてのファイル操作を `*os.Root` 経由で行います。`..` やシンボリックリンクによるルート外脱出をオープン時に拒否し、TOCTOU 競合を防ぎます。
* `--deny-glob` は要求パスとシンボリックリンクの実体パス（未作成ファイルは最も深い既存親ディレクトリ）の両方に適用されます。`!` で始まる例外パターンに一致するパスは保護対象から外れます。
* `--deny-glob` が守るのは sh-mcp のファイル操作ツールだけです。`full` モードのコマンドや許可したプログラムは、プロセスの権限で読めるファイルをすべて読めます。
* Windows では UNC パス、ドライブ相対パス、NTFS 代替データストリーム（`:`）を拒否します。
* HTTP モードでは `--allow-dir` 未指定のまま起動できません（`systemd` の `/` 起動事故防止）。

## 2. コマンド実行・環境変数

* `ops` モードの `--allow-command` は起動時に `exec.LookPath` で絶対パスへ固定し、実行時の `PATH` すり替えを防ぎます。
* `ops` モードで制限されるのは「どのプログラムを起動できるか」だけで、引数は制限しません。「ファイル変更不可・シェル不可」は sh-mcp のツールについての制限で、許可したプログラム自身の動作は防げません。そのため、引数次第で任意のコードを実行したりファイルを書き換えたりできるプログラムは許可しないでください。
  * 例: `git`（`-c core.pager=…`、`-c alias.x='!cmd'`、`--upload-pack` 等で任意コマンドを実行でき、`checkout` や `config` でファイルも変更可能）、`docker`（実質 root 権限）、シェル・各種インタプリタ（`python`, `node` 等）、`find`（`-exec`）、`xargs`、`env`、`make`、`go`、`npm`
  * 向いているのは `df`, `free`, `uptime`, `journalctl` のような参照系のプログラムです。
* `run_command` は `exec.CommandContext` で直接実行します。シェルを介する `run_shell` は `full` モードのみ有効です。
* 子プロセスには基本リスト（ツールチェーンの場所・プロキシ等。プロキシ URL に認証情報を埋め込んでいる場合を除き秘密情報は含まない）＋`--env-passthrough` のみ渡し、`SH_MCP_AUTH_TOKEN` は前方一致の指定でも必ず除外します。
* タイムアウト・キャンセル・`kill_job`・終了時はプロセスツリーごと終了します（Unix: プロセスグループ、Windows: Job Object）。

## 3. HTTP 保護

* HTTP モードは 16 文字以上の認証トークンが必須です（`--insecure-no-auth` 指定時を除く）。
* 静的 Bearer トークンは SHA-256 後に `crypto/subtle.ConstantTimeCompare` で検証します。
* `--public-url` 指定時は `CheckHost` で公開ホスト名・ループバック以外の `Host` ヘッダーを `403` 拒否します。
* `/mcp` と `POST /oauth/authorize` に `http.NewCrossOriginProtection()` を適用します。

## 4. 内蔵 OAuth 2.1 認可サーバー

`--public-url` と認証トークンの両方が設定されている場合、単一オーナー向けの OAuth 2.1 認可サーバーが自動起動します。

| エンドポイント | 役割 |
| :--- | :--- |
| `GET /.well-known/oauth-protected-resource`（および `/mcp` 配下） | Protected Resource Metadata（RFC 9728） |
| `GET /.well-known/oauth-authorization-server` | Authorization Server Metadata（RFC 8414） |
| `POST /oauth/register` | 動的クライアント登録（RFC 7591） |
| `GET` / `POST /oauth/authorize` | 同意画面（PKCE `S256` 必須、失敗 5 回/分でレート制限） |
| `POST /oauth/token` | `authorization_code`（1 回限り・2 分有効） / `refresh_token` |
| `POST /oauth/revoke` | トークン失効（RFC 7009） |

* 登録クライアント・認可コード・アクセストークン（1 時間有効）・リフレッシュトークンは、`SH_MCP_AUTH_TOKEN` から HKDF-SHA256 で導出した鍵による HMAC-SHA256 署名でディスクレス管理します。`SH_MCP_AUTH_TOKEN` を変更して再起動すると全トークンが一括失効します。
* Gemini クライアント互換のため、`refresh_token` グラント時はリフレッシュトークンをローテーションしません。リフレッシュトークンに有効期限はありません。
* `POST /oauth/revoke` による失効と使用済み認可コードはメモリ上でのみ管理するため、再起動すると失効の記録は消え、失効させたトークンも再び使えるようになります。確実に失効させるには `SH_MCP_AUTH_TOKEN` を差し替えて再起動してください。
* OAuth 有効時でも `Authorization: Bearer <SH_MCP_AUTH_TOKEN>` での直接アクセスを併用できます。

## 5. 監査ログ

全ツール呼び出しを JSONL 形式で出力します（`--audit-log` 未指定時は `stderr`）。

```json
{"time":"2026-11-25T09:00:00Z","level":"INFO","msg":"tool_call","event":"tool_call","tool":"write_file","args_summary":{"bytes":128,"path":"main.go","sha256":"a1b2c3d4e5f60708"},"duration_ms":2,"ok":true,"client":"oauth:Gemini#1234abcd"}
```

* `write_file` は本文を記録せずバイト数と SHA-256 先頭 16 桁のみ、`call_mcp_tool` は引数の値ではなくキー名のみを記録します。
* ログファイルは `0600` で作成され、`SIGHUP` でファイルハンドルを再オープンします。
