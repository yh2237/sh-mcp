# ツール仕様

公開ツールは `--mode` と `--mcp-server` に応じて決まります。パス引数はすべて絶対パスまたは `default_dir` からの相対パスを受け付けます。

## モード別の公開ツール

| ツール | `read` | `ops` | `full` | 概要 |
| :--- | :---: | :---: | :---: | :--- |
| `get_environment` | ○ | ○ | ○ | OS・許可パス・モード・`PATH` 上の主要ツール一覧 |
| `read_file` | ○ | ○ | ○ | 行番号付きテキスト読み取り・画像返却 |
| `list_dir` | ○ | ○ | ○ | ディレクトリツリー一覧 |
| `search_files` | ○ | ○ | ○ | RE2 正規表現検索・glob ファイル一覧 |
| `write_file` | — | — | ○ | 新規作成・全文上書き（アトミック） |
| `edit_file` | — | — | ○ | 複数箇所の完全一致置換＋diff 返却 |
| `delete_file` | — | — | ○ | ファイル・ディレクトリ削除 |
| `move_file` | — | — | ○ | 同一許可ルート内の移動・リネーム |
| `run_command` | — | ○（許可のみ） | ○ | シェルを介さない直接コマンド実行 |
| `run_shell` | — | — | ○ | シェルスクリプト実行 |
| `get_job_output` | — | ○ | ○ | バックグラウンドジョブの出力取得・完了待ち |
| `kill_job` | — | ○ | ○ | ジョブと子プロセスツリーの強制終了 |
| `list_jobs` | — | ○ | ○ | 実行中・最近終了したジョブ一覧 |
| `list_mcp_servers` / `list_mcp_tools` / `call_mcp_tool` | ※ | ※ | ○ | 下流 MCP サーバーの参照・実行（※`--mcp-server` 指定時） |
| `start_mcp_server` / `stop_mcp_server` | — | — | ○ | stdio MCP サーバーの動的起動・停止 |

## ファイル読み取り・検索

### `read_file`

| パラメータ | 型 | 必須 | 説明 |
| :--- | :--- | :---: | :--- |
| `path` | `string` | ○ | ファイルパス |
| `start_line` | `int` | — | 開始行（1 始まり、既定 `1`） |
| `end_line` | `int` | — | 終了行（両端含む、既定 `start_line + 1999`） |

* 1 回あたり最大 2,000 行・256 KiB、1 行 2,000 文字で切り詰めます。
* PNG / JPEG / GIF / WebP は `ImageContent` で返却します（`--max-file-bytes` 以内）。その他のバイナリはエラーになります。

### `list_dir`

| パラメータ | 型 | 必須 | 説明 |
| :--- | :--- | :---: | :--- |
| `path` | `string` | — | 対象ディレクトリ（既定 `default_dir`） |
| `depth` | `int` | — | 探索深さ `1`〜`10`（既定 `1`） |
| `include_hidden` | `bool` | — | `.` 始まりを含めるか（既定 `false`） |

* 最大 1,000 エントリで打ち切り、`--deny-glob` 一致分は除外します。

### `search_files`

| パラメータ | 型 | 必須 | 説明 |
| :--- | :--- | :---: | :--- |
| `pattern` | `string` | ※ | RE2 正規表現（`glob` とどちらか必須） |
| `path` | `string` | — | 対象ディレクトリまたはファイル（既定 `default_dir`） |
| `glob` | `string` | ※ | 相対パスフィルタ（例: `*.go`, `src/**/*.ts`） |
| `case_insensitive` | `bool` | — | 大文字・小文字を無視 |
| `context_lines` | `int` | — | 前後コンテキスト行数（`0`〜`10`） |
| `max_results` | `int` | — | 最大返却件数（既定 `200`、上限 `2000`） |
| `respect_gitignore` | `bool` | — | `.gitignore` を除外（既定 `true`） |

* `pattern` 省略時は `glob` 一致ファイル名一覧を返します。`.git`、バイナリ、`--max-file-bytes` 超過、`--deny-glob` 一致パスは除外します。

## ファイル編集・管理（`full` のみ）

### `write_file`

| パラメータ | 型 | 必須 | 説明 |
| :--- | :--- | :---: | :--- |
| `path` | `string` | ○ | 書き込み先（許可ルート自体は不可） |
| `content` | `string` | ○ | 全文（`--max-file-bytes` 以内） |
| `create_dirs` | `bool` | — | 親ディレクトリを作成するか |

* 一時ファイル経由の `fsync`＋`rename` でアトミックに書き込み、既存パーミッションを維持します（シンボリックリンクはリンク維持のまま上書き）。

### `edit_file`

| パラメータ | 型 | 必須 | 説明 |
| :--- | :--- | :---: | :--- |
| `path` | `string` | ○ | 対象ファイル |
| `edits` | `[]object` | ○ | `{old_text, new_text, replace_all?}` の配列 |

* `replace_all` が `false` のとき `old_text` はちょうど 1 回一致する必要があります。1 つでも失敗するとファイルは変更されません。改行コード（LF / CRLF）と UTF-8 BOM を維持します。

### `delete_file` / `move_file`

* `delete_file`: `path`（必須）、`recursive`（非空ディレクトリ削除）。`recursive: true` 時は配下に `--deny-glob` 保護ファイルが 1 つでもあれば拒否します。
* `move_file`: `source`（必須）、`destination`（必須、親ディレクトリ必須）、`overwrite`（既存ファイル上書き可、既存ディレクトリ不可）。同一許可ルート内のみ有効です。

## コマンド実行・ジョブ管理（`ops` / `full`）

`wait_sec` 以内に終了しないコマンドはバックグラウンドジョブとして継続し、`job_id` を返します（待機中は 5 秒ごとに Progress 通知）。同時実行は最大 16 件、終了保持は 32 件、出力リングは 1 MiB、1 回の返却上限は 64 KiB です。

### `run_command` / `run_shell`

| パラメータ | 型 | 必須 | 説明 |
| :--- | :--- | :---: | :--- |
| `command` / `args` | `string` / `[]string` | ○ / — | `run_command` 用（シェル展開なし） |
| `script` | `string` | ○ | `run_shell` 用（`full` のみ。Windows は PowerShell `-EncodedCommand` UTF-8） |
| `cwd` | `string` | — | 作業ディレクトリ（既定 `default_dir`） |
| `stdin` | `string` | — | 標準入力 |
| `wait_sec` | `int` | — | 待機秒数 `0`〜`120`（既定 `30`、`0` で即バックグラウンド） |
| `timeout_sec` | `int` | — | 強制終了秒数（既定 `600`、`0` で無制限） |

### `get_job_output` / `kill_job` / `list_jobs`

* `get_job_output`: `job_id`（必須）、`offset`（前回の `next_offset` で増分取得）、`wait_sec`（`0`〜`120`、既定 `30`）。
* `kill_job`: `job_id`（必須）。子プロセスツリーごと終了します。
* `list_jobs`: 実行中・最近終了したジョブ一覧を返します。

## MCP 中継プロキシ

* `list_mcp_servers`: 登録・起動中の下流サーバー一覧と状態を返します。
* `list_mcp_tools`: `server`（必須）のツール一覧と `input_schema` を返します（未起動なら自動起動）。
* `call_mcp_tool`: `server`, `tool`（必須）, `arguments`（任意）。タイムアウト 120 秒、テキスト返却上限 64 KiB。
* `start_mcp_server` / `stop_mcp_server`（`full` のみ）: `server`, `command`, `args`, `cwd` で動的起動、または `server` 指定で停止します。
