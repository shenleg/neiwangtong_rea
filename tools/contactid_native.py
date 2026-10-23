"""校验离线加密副本并通过固定版SQLite3MC只读查询；密钥仅由调用方以内存字节提供。"""

import ctypes as ct
import hashlib
import json
import os
from pathlib import Path

COMMIT = "f29b06b424e556cf0ad8b19646b30dfc3e3e36d8"
ARCHIVE_SHA256 = "dfbfb8651ff8b1d6fe14ee928ea002faf351d1ef37bb9a452ba1be87e23c1fe4"


class ReadError(Exception):
    """表示副本、固定库或只读查询失败；参数为不含口令和XML的说明，构造不抛额外异常。"""


def file_hash(path):
    """计算path文件SHA-256并关闭句柄，返回摘要；磁盘读取失败抛OSError。"""
    with path.open("rb") as source:
        return hashlib.file_digest(source, "sha256").hexdigest()


def check_sidecars(path):
    """检查副本同名WAL、SHM及journal；无返回值，任一存在（含空文件）即抛ReadError。"""
    for suffix in ("-wal", "-shm", "-journal"):
        if path.with_name(path.name + suffix).exists():
            raise ReadError("副本存在WAL、SHM或journal边文件，拒绝读取；不恢复或删除边文件。")


class Codec:
    """加载调用方明确指定的可信固定版构建目录；暴露头部解密和只读数据库连接，不生成明文文件。"""

    def __init__(self, directory: Path):
        """校验directory内构建清单、库哈希及版本；失败抛ReadError、OSError或JSON解析错误。"""
        directory = directory.resolve(strict=True)
        manifest = json.loads((directory / "build-manifest.json").read_text(encoding="utf-8"))
        if manifest["commit"] != COMMIT or manifest["archive_sha256"] != ARCHIVE_SHA256:
            raise ReadError("codec构建不是已经验证的固定参考版本。")
        wrapper = Path(__file__).resolve().with_name("contactid_codec.c")
        if manifest["wrapper_sha256"] != file_hash(wrapper):
            raise ReadError("codec包装源码与构建清单不符，须重新构建。")
        library_name = "contactid_codec.dll" if os.name == "nt" else "libcontactid_codec.so"
        if manifest["library_name"] != library_name or file_hash(directory / library_name) != manifest["library_sha256"]:
            raise ReadError("codec库文件名或SHA-256与构建清单不符。")
        self.lib = ct.CDLL(str(directory / library_name))
        signatures = {
            "nwt_contactid_header": (ct.c_int, [ct.c_void_p, ct.c_int, ct.c_void_p, ct.c_void_p]),
            "sqlite3_libversion": (ct.c_char_p, []),
            "sqlite3mc_version": (ct.c_char_p, []),
            "sqlite3_open_v2": (ct.c_int, [ct.c_char_p, ct.POINTER(ct.c_void_p), ct.c_int, ct.c_char_p]),
            "sqlite3_close": (ct.c_int, [ct.c_void_p]),
            "sqlite3mc_cipher_index": (ct.c_int, [ct.c_char_p]),
            "sqlite3mc_config": (ct.c_int, [ct.c_void_p, ct.c_char_p, ct.c_int]),
            "sqlite3mc_config_cipher": (ct.c_int, [ct.c_void_p, ct.c_char_p, ct.c_char_p, ct.c_int]),
            "sqlite3_key": (ct.c_int, [ct.c_void_p, ct.c_void_p, ct.c_int]),
            "sqlite3_db_readonly": (ct.c_int, [ct.c_void_p, ct.c_char_p]),
            "sqlite3_prepare_v2": (ct.c_int, [ct.c_void_p, ct.c_char_p, ct.c_int, ct.POINTER(ct.c_void_p), ct.c_void_p]),
            "sqlite3_bind_int64": (ct.c_int, [ct.c_void_p, ct.c_int, ct.c_int64]),
            "sqlite3_bind_text": (ct.c_int, [ct.c_void_p, ct.c_int, ct.c_char_p, ct.c_int, ct.c_void_p]),
            "sqlite3_bind_parameter_count": (ct.c_int, [ct.c_void_p]),
            "sqlite3_step": (ct.c_int, [ct.c_void_p]),
            "sqlite3_finalize": (ct.c_int, [ct.c_void_p]),
            "sqlite3_column_count": (ct.c_int, [ct.c_void_p]),
            "sqlite3_column_type": (ct.c_int, [ct.c_void_p, ct.c_int]),
            "sqlite3_column_int64": (ct.c_int64, [ct.c_void_p, ct.c_int]),
            "sqlite3_column_text": (ct.c_void_p, [ct.c_void_p, ct.c_int]),
            "sqlite3_column_bytes": (ct.c_int, [ct.c_void_p, ct.c_int]),
        }
        for name, (result, arguments) in signatures.items():
            function = getattr(self.lib, name)
            function.restype, function.argtypes = result, arguments
        if self.lib.sqlite3mc_version() != b"SQLite3 Multiple Ciphers 2.7.0" or self.lib.sqlite3_libversion() != b"3.54.1":
            raise ReadError("codec运行版本与已经验证的版本不符。")

    def header(self, encrypted: bytes, password: bytes) -> bytes:
        """用password原始字节解密页1前32字节，返回头部；长度/口令或codec错误抛ReadError。"""
        if len(encrypted) != 32 or not password or b"\x00" in password:
            raise ReadError("必须提供32字节加密头及非空、不含NUL的原始口令字节。")
        output = ct.create_string_buffer(32)
        if self.lib.nwt_contactid_header(password, len(password), encrypted, output) != 0:
            raise ReadError("codec头部解密失败。")
        return output.raw

    def open(self, path: Path, password: bytes, page_size: int):
        """以SQLITE_OPEN_READONLY打开path，密钥前配置legacy和页长；返回连接，失败抛ReadError。"""
        connection = Connection(self.lib)
        status = self.lib.sqlite3_open_v2(str(path).encode("utf-8"), ct.byref(connection.handle), 1, None)
        try:
            if status != 0:
                raise ReadError(f"SQLite只读打开失败，错误码={status}。")
            cipher = self.lib.sqlite3mc_cipher_index(b"aes128cbc")
            if cipher <= 0 or self.lib.sqlite3mc_config(connection.handle, b"cipher", cipher) != cipher:
                raise ReadError("codec不支持已验证的aes128cbc。")
            for key, value in ((b"legacy", 1), (b"legacy_page_size", page_size)):
                if self.lib.sqlite3mc_config_cipher(connection.handle, b"aes128cbc", key, value) != value:
                    raise ReadError("codec旧格式参数配置失败。")
            if self.lib.sqlite3_key(connection.handle, password, len(password)) != 0:
                raise ReadError("codec口令配置失败。")
            if self.lib.sqlite3_db_readonly(connection.handle, b"main") != 1:
                raise ReadError("SQLite连接没有处于操作系统只读模式。")
            list(connection.query("PRAGMA query_only=ON"))
            list(connection.query("PRAGMA trusted_schema=OFF"))
            list(connection.query("BEGIN"))
            return connection
        except BaseException:
            connection.close()
            raise


class Connection:
    """封装内部固定SQL的参数化只读查询，保存原始TEXT字节长度，不把NUL截断后误认为有效ID。"""

    def __init__(self, library):
        """保存已声明ABI的可信library和空连接句柄；无返回值或额外异常。"""
        self.lib = library
        self.handle = ct.c_void_p()

    def close(self):
        """关闭连接；无返回值，未释放语句或SQLite关闭失败抛ReadError。"""
        if self.handle.value:
            status = self.lib.sqlite3_close(self.handle)
            if status != 0:
                raise ReadError(f"SQLite关闭失败，错误码={status}。")
            self.handle = ct.c_void_p()

    def query(self, sql: str, parameters: tuple = ()):
        """执行内部sql及绑定parameters，逐行返回tuple；SQLite错误或不支持的值抛ReadError。"""
        statement = ct.c_void_p()
        encoded = sql.encode("utf-8")
        status = self.lib.sqlite3_prepare_v2(self.handle, encoded, len(encoded), ct.byref(statement), None)
        try:
            if status != 0:
                raise ReadError(f"SQLite准备查询失败，错误码={status}。")
            if self.lib.sqlite3_bind_parameter_count(statement) != len(parameters):
                raise ReadError("SQLite查询参数数量不符。")
            for index, value in enumerate(parameters, 1):
                if isinstance(value, int):
                    status = self.lib.sqlite3_bind_int64(statement, index, value)
                elif isinstance(value, str):
                    text = value.encode("utf-8")
                    status = self.lib.sqlite3_bind_text(statement, index, text, len(text), ct.c_void_p(-1))
                else:
                    raise ReadError("SQLite内部查询只接受整数或文本参数。")
                if status != 0:
                    raise ReadError(f"SQLite绑定参数失败，错误码={status}。")
            while True:
                status = self.lib.sqlite3_step(statement)
                if status == 101:
                    break
                if status != 100:
                    raise ReadError(f"SQLite查询失败，错误码={status}。")
                row = []
                for index in range(self.lib.sqlite3_column_count(statement)):
                    kind = self.lib.sqlite3_column_type(statement, index)
                    if kind == 5:
                        row.append(None)
                    elif kind == 1:
                        row.append(self.lib.sqlite3_column_int64(statement, index))
                    elif kind == 3:
                        pointer = self.lib.sqlite3_column_text(statement, index)
                        length = self.lib.sqlite3_column_bytes(statement, index)
                        row.append(ct.string_at(pointer, length).decode("utf-8", errors="strict"))
                    else:
                        raise ReadError("SQLite元数据含不支持的浮点或二进制字段。")
                yield tuple(row)
        finally:
            if statement.value:
                self.lib.sqlite3_finalize(statement)


def snapshot_header(path: Path, codec: Codec, password: bytes) -> tuple:
    """验证离线path并解密头，返回准确页长与加密源哈希；边文件、格式或磁盘错误明确失败。"""
    if not path.is_absolute() or not path.is_file():
        raise ReadError("副本必须是存在的绝对文件路径。")
    check_sidecars(path)
    before = file_hash(path)
    with path.open("rb") as source:
        header = codec.header(source.read(32), password)
    if header[:16] != b"SQLite format 3\x00":
        raise ReadError("口令或加密副本格式不符，解密头不是SQLite；不更换参数重试。")
    encoded = int.from_bytes(header[16:18], "big")
    size = 65536 if encoded == 1 else encoded
    if size not in (512, 1024, 2048, 4096, 8192, 16384, 32768, 65536):
        raise ReadError("副本页长无效。")
    if header[18:24] != bytes((1, 1, 0, 64, 32, 32)):
        raise ReadError("首版只接受rollback模式、reserved=0及标准SQLite负载比例的副本。")
    if path.stat().st_size % size != 0 or path.stat().st_size < size:
        raise ReadError("副本长度不是完整页长整数倍。")
    check_sidecars(path)
    if file_hash(path) != before:
        raise ReadError("读取头部期间副本发生变化。")
    return size, before
