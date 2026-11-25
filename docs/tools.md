# ツール仕様

使えるツールは `--mode` と `--mcp-server` で決まります。パスは絶対パスか、`default_dir`（先頭の `--allow-dir`）からの相対パスで指定します。

## モード別のツール

○ は使える、△ は使えるが毎回ユーザーの承認が要る、を表します（[承認の仕組み](security.md#2-モードと承認)）。

| ツール | `read` | `ask` | `full` | 概要 |
| :--- | :---: | :---: | :---: | :--- |
| `get_environment` | ○ | ○ | ○ | OS・許可ディレクトリ・モード・承認の方法・使えるツールの一覧 |
| `read_file` | ○ | ○ | ○ | 行番号付きで読む（画像も可） |
| `list_dir` | ○ | ○ | ○ | ディレクトリの一覧 |
| `search_files` | ○ | ○ | ○ | 正規表現で検索、glob でファイル一覧 |
| `git_status` / `git_diff` / `git_log` | ○ | ○ | ○ | git の状態・差分・履歴（読み取り専用。git がある場合のみ） |
| `write_file` / `edit_file` | — | △ | ○ | 新規作成・全文上書き / 文字列を置換して diff を返す |
| `delete_file` / `move_file` | — | △ | ○ | 削除 / 移動・名前変更 |
| `run_command` | — | △（`--auto-approve-command` は ○） | ○ | シェルを介さずコマンド実行 |
| `run_shell` | — | △ | ○ | シェルスクリプト実行 |
| `get_job_output` / `kill_job` / `list_jobs` | — | ○ | ○ | バックグラウンドジョブの操作 |
| `write_stdin` | — | △ | ○ | `tty` で起動したジョブへのキー入力（対話シェルなど） |
| `list_mcp_servers` / `list_mcp_tools` | ※ | ○ | ○ | 中継先 MCP サーバーの一覧（※ `--mcp-server` 指定時） |
| `call_mcp_tool` | ※（読み取り専用のツールのみ） | △（読み取り専用のツールは ○） | ○ | 中継先 MCP サーバーのツール呼び出し |
| `start_mcp_server` / `stop_mcp_server` | — | △ | ○ | 中継先 MCP サーバーの起動・停止 |

## ファイルの読み取り・検索

### `read_file`

| パラメータ | 型 | 必須 | 説明 |
| :--- | :--- | :---: | :--- |
| `path` | `string` | ※ | ファイル |
| `paths` | `[]string` | ※ | 一度に読む複数のファイル（最大 20、`path` とどちらか一方） |
| `start_line` | `int` | — | 開始行（既定 `1`） |
| `end_line` | `int` | — | 終了行（既定 `start_line + 1999`） |

1 ファイルにつき最大 2,000 行・256 KiB を返します。`paths` のときは全体で 512 KiB までで、読めないファイルがあってもエラーとして並べて残りを読みます（残りの上限より大きい画像は、単独で読むよう案内します）。総行数は `--max-file-bytes` 以下のファイルでだけ数え、それより大きいファイル（ログなど）は指定範囲まで読んで止めます。PNG / JPEG / GIF / WebP は画像として返し、その他のバイナリはエラーです。

### `list_dir`

| パラメータ | 型 | 必須 | 説明 |
| :--- | :--- | :---: | :--- |
| `path` | `string` | — | ディレクトリ（既定 `default_dir`） |
| `depth` | `int` | — | 深さ `1`〜`10`（既定 `1`） |
| `include_hidden` | `bool` | — | `.` で始まる項目も含める |

最大 1,000 件です。

### `search_files`

| パラメータ | 型 | 必須 | 説明 |
| :--- | :--- | :---: | :--- |
| `pattern` | `string` | ※ | 正規表現（RE2）。`glob` とどちらかが必須 |
| `path` | `string` | — | 検索するディレクトリかファイル（既定 `default_dir`） |
| `glob` | `string` | ※ | 対象ファイルの絞り込み（例: `*.go`, `src/**/*.ts`） |
| `case_insensitive` | `bool` | — | 大文字・小文字を区別しない |
| `context_lines` | `int` | — | 前後に表示する行数（`0`〜`10`） |
| `max_results` | `int` | — | 最大件数（既定 `200`、上限 `2000`） |
| `respect_gitignore` | `bool` | — | `.gitignore` の対象を除く（既定 `true`） |

`pattern` を省くと `glob` に一致するファイル名の一覧を返します。`.git` とバイナリファイルは対象外です。

## git（全モード）

sh-mcp が引数を固定して git を実行するので、読み取り専用で、どのモードでも承認なしで使えます。`path` は対象のファイルかディレクトリで、その中の変更・履歴だけを表示します（既定 `default_dir`）。`--deny-glob` で保護されたファイルは、一覧からも差分からも除きます。名前の変更は「削除」と「追加」として表示します（変更前の名前が保護されていても漏れないように）。

* `git_status`: `path`。ブランチと、変更・ステージ済み・未追跡のファイル（`git status --short --branch`）。
* `git_diff`: `path`、`staged`（ステージ済みの差分）、`base`（比べるリビジョン。例 `HEAD`・`main`）、`target`（`base` と比べる2つ目のリビジョン。どちらもコミットのみで、`HEAD:dir` のようなツリーは不可）、`stat_only`（ファイルごとの増減行数だけ）。
* `git_log`: `path`、`revision`（例 `main`・`HEAD~5..HEAD`）、`max_count`（`1`〜`200`、既定 `20`）、`patch`（各コミットの差分も表示）。

## ファイルの編集（`ask` / `full`）

### `write_file`

| パラメータ | 型 | 必須 | 説明 |
| :--- | :--- | :---: | :--- |
| `path` | `string` | ○ | 書き込み先 |
| `content` | `string` | ○ | ファイルの全文 |
| `create_dirs` | `bool` | — | 親ディレクトリがなければ作る |

書き込みはアトミックで、既存ファイルのパーミッションを保ちます。

### `edit_file`

| パラメータ | 型 | 必須 | 説明 |
| :--- | :--- | :---: | :--- |
| `path` | `string` | ○ | 対象ファイル |
| `edits` | `[]object` | ○ | `{old_text, new_text, replace_all?}` の配列 |

`replace_all` がなければ `old_text` はちょうど 1 か所に一致する必要があります。1 つでも失敗するとファイルは変更されません。改行コードと BOM は保たれます。

### `delete_file` / `move_file`

* `delete_file`: `path`、`recursive`（中身ごと削除）。保護されたファイルを含むディレクトリは削除できません。
* `move_file`: `source`、`destination`、`overwrite`（既存ファイルを上書き）。同じ `--allow-dir` の中でのみ移動でき、保護されたファイルを含むディレクトリは移動できません。

## コマンド実行（`ask` / `full`）

`wait_sec` 秒以内に終わらないコマンドはバックグラウンドで続行し、`job_id` を返します。同時に実行できるのは 16 件までで、1 回に返す出力は 64 KiB までです。

### `run_command` / `run_shell`

| パラメータ | 型 | 必須 | 説明 |
| :--- | :--- | :---: | :--- |
| `command` / `args` | `string` / `[]string` | ○ / — | `run_command` のみ。シェルを介さず実行 |
| `script` | `string` | ○ | `run_shell` のみ |
| `cwd` | `string` | — | 作業ディレクトリ（既定 `default_dir`） |
| `stdin` | `string` | — | 標準入力 |
| `wait_sec` | `int` | — | 待つ秒数 `0`〜`120`（既定 `30`、`0` ですぐバックグラウンドへ） |
| `timeout_sec` | `int` | — | 強制終了までの秒数（既定 `600`、`0` で無制限） |
| `tty` | `bool` | — | 疑似端末で実行し、`write_stdin` で操作できるようにする |

### 対話的な操作（`tty`）

`tty: true` で起動すると、シェル・REPL・ssh・パスワード入力など、対話的なプログラムを操作できます。起動直後は、出力が止まった（入力待ちになった）時点で結果を返します。開いたままにするセッションは `timeout_sec: 0` で起動してください。

```text
run_command  {"command": "bash", "tty": true, "timeout_sec": 0}   → job-1
write_stdin  {"job_id": "job-1", "input": "cd /srv && ls\n"}
write_stdin  {"job_id": "job-1", "input": "\u0003"}               ← Ctrl-C
```

* 出力は、画面に見えるとおりの文字にして返します（色などの制御コードは除き、行の上書き・進捗表示・REPL の再描画は画面と同じ結果）。入力途中の行（プロンプトなど）は結果の最後に付き、確定すると次の結果の先頭にもう一度現れます。画面全体を描くプログラム（`vim`、`top`）は読めません。
* 端末の大きさは 120 桁 × 30 行です。
* Windows では ConPTY（Windows 10 1809 以降）を使います。`cmd` や PowerShell も操作でき、改行の `\n` は Enter キーとして送ります。

### ジョブの操作

* `get_job_output`: `job_id`、`offset`（前回の `next_offset` を渡すと続きだけ取得）、`wait_sec`（既定 `30`）
* `write_stdin`: `job_id`、`input`、`wait_sec`（既定 `5`）。`tty` で起動したジョブに入力し、その後の出力を返します。出力が 0.3 秒止まった時点で返します。`input` を空にすると新しい出力だけを読みます。改行は `\n`、Ctrl-C は `\u0003`、Ctrl-D は `\u0004`、↑ は `\u001b[A` のように JSON のエスケープで書きます。`tty` なしのジョブには Ctrl-C（`\u0003`）だけ送れます（Unix のみ）。
* `kill_job`: `job_id`。子プロセスもまとめて終了します。
* `list_jobs`: 実行中と最近終了したジョブの一覧

## MCP サーバーの中継

* `list_mcp_servers`: 中継先サーバーの一覧と状態
* `list_mcp_tools`: `server` のツール一覧（未起動なら起動）
* `call_mcp_tool`: `server`、`tool`、`arguments`。120 秒でタイムアウトします。中継先のサーバーが読み取り専用の印（`readOnlyHint`）を付けていないツールは、`read` では呼べず、`ask` では承認が要ります。
* `start_mcp_server` / `stop_mcp_server`（`read` 以外）: `server`、`command`、`args`、`cwd` で起動し、`server` を指定して停止します。
