"""Extract a Baidu session cookie header from the official Netdisk client's store.

Why this exists
---------------
The official Baidu Netdisk Windows client keeps its session cookies in plain
text (no DPAPI, no App-Bound encryption) in:

    %APPDATA%\\BaiduNetdisk\\Network\\Cookies      (SQLite)

That is the same live session the client itself uses, so it is the most reliable
way to get BaiduPCS-Go logged in - far more reliable than the username/password
flow, which Baidu's risk control frequently rejects.

The subtlety this handles
------------------------
Recent clients store only `BDUSS_BFESS`, NOT a literal `BDUSS` cookie. But
upstream `pcsconfig/baidu.go:SetupUserByBDUSS` extracts the token with the regex
`BDUSS=(.+?);`, and `regexp.FindSubmatch` returns nil when it does not match, so
`sub[1]` panics with "index out of range". This script therefore emits an
explicit `BDUSS=<value of BDUSS_BFESS>` so the existing parser succeeds and no
upstream code needs to change.

Safety
------
* Read-only: the source database is opened in SQLite read-only mode via a copy.
* The cookie values are written to the path given by --out and are never printed
  to stdout/stderr. Only cookie NAME/domain/length metadata is printed.
* Nothing is sent anywhere; the caller decides what to do with the file.
"""

import argparse
import os
import shutil
import sqlite3
import sys
import tempfile

DEFAULT_SRC = os.path.join(
    os.environ.get("APPDATA", ""), "BaiduNetdisk", "Network", "Cookies"
)

# Cookie names worth forwarding, keyed by a lower-cased match rule.
# BDUSS_BFESS is the important one: it is the live session token on recent clients.
WANTED_EXACT = {
    "bduss", "bduss_bfess", "stoken", "stoken_bfess",
    "baiduid", "baiduid_bfess", "panpsc", "sboxtkn",
}


def read_cookies(src, tmp_dir):
    """Return [(name, host, value)] from the client's cookie database.

    tmp_dir must already exist: under the DSH file sandbox, a directory created
    by this process may not be writable, so the caller pre-creates it.
    """
    tmp_db = os.path.join(tmp_dir, "Cookies-copy.db")
    try:
        shutil.copy2(src, tmp_db)
    except OSError as exc:
        raise SystemExit(
            "无法复制客户端 cookie 数据库: {}\n"
            "如果错误是『另一个程序正在使用此文件』, 请先退出百度网盘客户端再试。\n"
            "源文件: {}".format(exc, src)
        )

    try:
        con = sqlite3.connect("file:{}?mode=ro".format(tmp_db.replace("\\", "/")), uri=True)
        rows = con.execute(
            "SELECT host_key, name, value FROM cookies"
        ).fetchall()
        con.close()
    finally:
        try:
            os.remove(tmp_db)
        except OSError:
            pass
    return [(name, host, value or "") for host, name, value in rows]


def build_cookie_header(rows):
    """Build a Cookie header string, adding BDUSS when only BDUSS_BFESS exists."""
    by_name = {}
    for name, host, value in rows:
        if value and name.lower() in WANTED_EXACT:
            # Prefer the plain name if both forms are present.
            if name.lower() not in by_name or not name.lower().endswith("_bfess"):
                by_name[name.lower()] = (name, value)

    bduss = by_name.get("bduss")
    bfess = by_name.get("bduss_bfess")
    if not bduss and bfess:
        # Emit BDUSS explicitly: upstream's parser only looks for 'BDUSS='.
        bduss = ("BDUSS", bfess[1])

    parts = []
    seen = set()

    def add(cookie_name, value):
        if cookie_name.lower() in seen:
            return
        seen.add(cookie_name.lower())
        parts.append("{}={}".format(cookie_name, value))

    if bduss:
        add("BDUSS", bduss[1])
    for key in ("stoken", "baiduid", "panpsc", "sboxtkn"):
        if key in by_name:
            add(by_name[key][0], by_name[key][1])
    return "; ".join(parts), by_name, bool(bfess), bool(by_name.get("bduss"))


def main():
    ap = argparse.ArgumentParser(add_help=True)
    ap.add_argument("--src", default=DEFAULT_SRC, help="客户端 Cookies 数据库路径")
    ap.add_argument("--out", required=True, help="写入 cookie 字符串的文件 (切勿提交到 git)")
    ap.add_argument("--tmpdir", default=None,
                    help="复制 cookie 数据库用的临时目录 (必须已存在; 沙箱下需在工作区内)")
    ap.add_argument("--dry-run", action="store_true", help="只打印元信息, 不写文件")
    args = ap.parse_args()

    if not os.path.exists(args.src):
        raise SystemExit("找不到客户端 cookie 数据库: {}".format(args.src))

    tmp_dir = args.tmpdir or tempfile.gettempdir()
    if not os.path.isdir(tmp_dir):
        raise SystemExit("临时目录不存在: {} (请先创建)".format(tmp_dir))

    rows = read_cookies(args.src, tmp_dir)
    header, by_name, had_bfess, had_plain = build_cookie_header(rows)

    print("源: {}".format(args.src))
    print("读到 cookie 条数: {}".format(len(rows)))
    print("")
    print("参与登录的 cookie (仅显示名称/长度, 不显示内容):")
    for name, _host, value in rows:
        if name.lower() in WANTED_EXACT and value:
            print("   {:<16} len={}".format(name, len(value)))
    print("")
    print("客户端含有字面 BDUSS cookie : {}".format(had_plain))
    print("客户端含有 BDUSS_BFESS      : {}".format(had_bfess))
    if had_bfess and not had_plain:
        print("=> 将把 BDUSS_BFESS 的值以 BDUSS=<...> 形式写入, 以匹配上游的解析正则。")
    if not header:
        raise SystemExit("未能从客户端 cookie 中提取到任何可用的登录凭据。")

    print("组装后的 cookie 头长度: {} 字符".format(len(header)))
    if args.dry_run:
        print("(dry-run: 未写入文件)")
        return

    out_dir = os.path.dirname(os.path.abspath(args.out))
    if out_dir:
        os.makedirs(out_dir, exist_ok=True)
    # Restrict permissions where the platform supports it.
    fd = os.open(args.out, os.O_WRONLY | os.O_CREAT | os.O_TRUNC, 0o600)
    with os.fdopen(fd, "w", encoding="utf-8") as fh:
        fh.write(header)
    print("")
    print("已写入: {}  (内容未显示)".format(args.out))
    print("提示: 该文件等同于账号凭据, 用完请删除。")


if __name__ == "__main__":
    sys.exit(main())
