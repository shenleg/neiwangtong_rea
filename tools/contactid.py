"""从明确确认身份的完整离线nr副本只读提取姓名对应的联系人ID；不发送、不改配置、不导出XML。"""

import argparse
from datetime import datetime, timezone
import importlib.util
import json
from pathlib import Path
import sys

sys.dont_write_bytecode = True


def load_module(name):
    """按当前可信脚本目录的明确路径加载name模块，返回模块；加载失败直接抛异常，不搜索副本目录。"""
    spec = importlib.util.spec_from_file_location(name, Path(__file__).resolve().with_name(name + ".py"))
    module = importlib.util.module_from_spec(spec)
    spec.loader.exec_module(module)
    return module


native = load_module("contactid_native")
records = load_module("contactid_records")


def extract(snapshot: Path, library_dir: Path, identity: str, name: str, password: bytes) -> dict:
    """用password原始字节只读查询snapshot的明确identity与name，返回带源哈希和检查时间的匹配结果。

    library_dir必须是调用方信任的固定版构建目录；snapshot必须是正常退出后取得的完整私有副本。
    任何格式、身份、schema、XML或读库失败抛ReadError/ContactError，磁盘错误直接传播；不改配置或产生发送标识。
    """
    if not identity or "\x00" in identity:
        raise records.ContactError("必须明确确认当前身份原始后缀，不能用姓名或UI转换ID猜测。")
    return _read_snapshot(snapshot, library_dir, password,
                          lambda query: records.find_contact(query, identity, name))


def inspect_schema(snapshot: Path, library_dir: Path, password: bytes) -> dict:
    """仅返回snapshot内OrgStruct表名及列定义，供人工核对身份；不读联系人行，读取或格式错误直接抛异常。"""
    def inspect(query):
        """只查询表目录和列元数据，返回schema_only结果；query错误直接传播。"""
        tables = []
        for (table,) in query("SELECT name FROM sqlite_master WHERE type='table' AND name GLOB 'OrgStruct_*' ORDER BY name"):
            quoted = '"' + table.replace('"', '""') + '"'
            columns = [{"name": row[1], "type": row[2], "primary_key": row[5]} for row in query(f"PRAGMA table_xinfo({quoted})")]
            tables.append({"table": table, "columns": columns})
        return {"status": "schema_only", "tables": tables}
    return _read_snapshot(snapshot, library_dir, password, inspect)


def _read_snapshot(snapshot, library_dir, password, operation):
    """在同一只读事务中执行受控operation并核对前后哈希，返回带来源的结果；校验失败直接抛异常。"""
    if not snapshot.is_absolute() or not library_dir.is_absolute():
        raise native.ReadError("副本与可信codec构建目录必须是绝对路径。")
    snapshot = snapshot.resolve(strict=True)
    codec = native.Codec(library_dir)
    size, source_hash = native.snapshot_header(snapshot, codec, password)
    connection = codec.open(snapshot, password, size)
    try:
        result = operation(connection.query)
    finally:
        connection.close()
    native.check_sidecars(snapshot)
    if native.file_hash(snapshot) != source_hash:
        raise native.ReadError("查询期间副本发生变化，不能使用此次结果。")
    result.update({"source_sha256": source_hash, "page_size": size,
                   "checked_at": datetime.now(timezone.utc).isoformat(), "codec_commit": native.COMMIT})
    return result


def main(argv=None, stdin=None, stdout=None, stderr=None):
    """解析只读参数，从stdin读取原始口令并输出一次JSON；唯一匹配返回0，无匹配3，同名4，读取失败1，参数错误2。"""
    if stdin is None:
        stdin = sys.stdin.buffer
    if stdout is None:
        stdout = sys.stdout
    if stderr is None:
        stderr = sys.stderr
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--snapshot", type=Path, required=True, help="仓库外完整离线nr副本的绝对路径")
    parser.add_argument("--library-dir", type=Path, required=True, help="可信固定版codec构建输出目录的绝对路径")
    parser.add_argument("--schema-only", action="store_true", help="只查看OrgStruct表名与列结构，不读取联系人行")
    parser.add_argument("--identity", help="已核实的本机原始身份后缀，不是姓名或UI复制ID")
    parser.add_argument("--name", help="联系人原始Name精确值，不按本地备注搜索")
    parser.add_argument("--offline-confirmed", action="store_true", required=True,
                        help="确认客户端已正常退出且副本完整；程序不会关闭客户端或复制活动库")
    parser.add_argument("--password-stdin", action="store_true", required=True,
                        help="从标准输入接收精确口令字节直到EOF，不去除换行，不从命令参数或环境变量取密钥")
    args = parser.parse_args(argv)
    if args.schema_only:
        if args.identity is not None or args.name is not None:
            parser.error("--schema-only不能同时指定--identity或--name")
    elif args.identity is None or args.name is None:
        parser.error("联系人匹配必须同时指定--identity和--name；先查看表结构可用--schema-only")
    try:
        password = stdin.read()
        if args.schema_only:
            result = inspect_schema(args.snapshot, args.library_dir, password)
        else:
            result = extract(args.snapshot, args.library_dir, args.identity, args.name, password)
        # 只输出最终检查通过的结果，不逐行输出候选，避免后续坏记录使先前输出被误当作成功。
        stdout.write(json.dumps(result, ensure_ascii=True) + "\n")
        return {"schema_only": 0, "unique": 0, "not_found": 3, "ambiguous": 4}[result["status"]]
    except (native.ReadError, records.ContactError) as error:
        print(str(error), file=stderr)
        return 1
    except (OSError, ValueError, KeyError, AttributeError, UnicodeError):
        print("读取失败：请检查离线副本、固定codec构建清单、文件权限或文本格式；不更换参数重试。", file=stderr)
        return 1


if __name__ == "__main__":
    raise SystemExit(main())
