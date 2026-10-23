"""用固定版codec和合成加密库验证完整只读入口；不接收或读取真实客户端数据库。"""

import argparse
import ctypes as ct
import hashlib
import importlib.util
import io
import json
from pathlib import Path
import sys
import tempfile
import unittest

sys.dont_write_bytecode = True
SPEC = importlib.util.spec_from_file_location("contactid", Path(__file__).resolve().with_name("contactid.py"))
APP = importlib.util.module_from_spec(SPEC)
SPEC.loader.exec_module(APP)
NATIVE = APP.native
PASSWORD = b"nr-codec-synthetic-only-2026"
LIBRARY_DIR = None
SCHEMA = """CREATE TABLE OrgStruct_SYNTHETIC (
PriKey INTEGER PRIMARY KEY AUTOINCREMENT, UpdateTime INTEGER,
OnlineTime INTEGER, Type INTEGER, Id TEXT, Data TEXT, UNIQUE(Id))"""


class HeaderTests(unittest.TestCase):
    """检查生产输入的边文件、页头和哈希边界；不需要本地codec库。"""

    def setUp(self):
        """建立独立合成字节文件和合法头；无返回值，写入失败直接传播。"""
        root = tempfile.TemporaryDirectory()
        self.addCleanup(root.cleanup)
        self.path = Path(root.name) / "snapshot"
        self.path.write_bytes(bytes(512))
        self.header_data = bytearray(b"SQLite format 3\x00" + bytes((2, 0, 1, 1, 0, 64, 32, 32)) + bytes(8))
        self.codec = self

    def header(self, encrypted, password):
        """测试接口不实际解密，返回由用例指定的头；参数忽略，无额外异常。"""
        return bytes(self.header_bytes)

    def check(self, header=None):
        """把用例头交给生产校验器，返回结果；生产格式错误直接传播。"""
        self.header_bytes = bytes(self.header_data if header is None else header)
        return NATIVE.snapshot_header(self.path, self.codec, PASSWORD)

    def test_valid(self):
        """完整合法副本返回准确页长及源密文哈希。"""
        size, source_hash = self.check()
        self.assertEqual(size, 512)
        self.assertEqual(source_hash, hashlib.sha256(bytes(512)).hexdigest())

    def test_each_sidecar(self):
        """空WAL/SHM/journal也必须在解密之前拒绝。"""
        for suffix in ("-wal", "-shm", "-journal"):
            with self.subTest(suffix=suffix):
                sidecar = self.path.with_name(self.path.name + suffix)
                sidecar.touch()
                with self.assertRaises(NATIVE.ReadError):
                    self.check()
                sidecar.unlink()

    def test_invalid_headers(self):
        """魔数、页长、WAL头、reserved和负载比例均严格校验。"""
        for offset, value in ((0, 0), (16, 3), (18, 2), (19, 2), (20, 1), (21, 63)):
            with self.subTest(offset=offset):
                header = self.header_data[:]
                header[offset] = value
                with self.assertRaises(NATIVE.ReadError):
                    self.check(header)

    def test_partial_file(self):
        """尾部缺页或不足一页的副本明确拒绝。"""
        for length in (511, 513):
            self.path.write_bytes(bytes(length))
            with self.assertRaises(NATIVE.ReadError):
                self.check()


class EncryptedTests(unittest.TestCase):
    """用已构建固定codec生成合成加密库，再从生产只读入口验证。"""

    @classmethod
    def setUpClass(cls):
        """加载测试参数指定的可信构建目录；缺少参数时跳过，库错误直接失败。"""
        if LIBRARY_DIR is None:
            raise unittest.SkipTest("完整加密测试必须显式提供--library-dir；未验证codec")
        cls.codec = NATIVE.Codec(LIBRARY_DIR)

    def setUp(self):
        """为每项合成测试创建独立目录；无返回值，创建失败直接传播。"""
        root = tempfile.TemporaryDirectory()
        self.addCleanup(root.cleanup)
        self.path = Path(root.name) / "synthetic-nr"

    def fixture(self, size=4096, rows=None):
        """只在临时目录创建人工加密联系人库；返回路径，SQLite失败直接传播。"""
        if rows is None:
            rows = [(1, "原样 ID-🙂 ", "<U><Name>测试 &amp; A</Name><Phone>PRIVATE_PHONE</Phone></U>"),
                    (2, "organization", "not-user-XML")]
        lib = self.codec.lib
        connection = NATIVE.Connection(lib)
        status = lib.sqlite3_open_v2(str(self.path).encode("utf-8"), ct.byref(connection.handle), 6, None)
        self.assertEqual(status, 0)
        try:
            cipher = lib.sqlite3mc_cipher_index(b"aes128cbc")
            self.assertEqual(lib.sqlite3mc_config(connection.handle, b"cipher", cipher), cipher)
            for key, value in ((b"legacy", 1), (b"legacy_page_size", size)):
                self.assertEqual(lib.sqlite3mc_config_cipher(connection.handle, b"aes128cbc", key, value), value)
            self.assertEqual(lib.sqlite3_key(connection.handle, PASSWORD, len(PASSWORD)), 0)
            list(connection.query(f"PRAGMA page_size={size}"))
            list(connection.query("PRAGMA journal_mode=DELETE"))
            list(connection.query(SCHEMA))
            for kind, user_id, data in rows:
                list(connection.query("INSERT INTO OrgStruct_SYNTHETIC(Type,Id,Data) VALUES (?,?,?)", (kind, user_id, data)))
        finally:
            connection.close()
        return self.path

    def test_all_page_sizes_and_readonly(self):
        """全部八种页长的加密副本可查询、不输出其他资料、连接拒写且源哈希不变。"""
        for size in (512, 1024, 2048, 4096, 8192, 16384, 32768, 65536):
            with self.subTest(size=size):
                self.path = self.path.with_name(f"synthetic-{size}")
                self.fixture(size)
                before = NATIVE.file_hash(self.path)
                self.assertNotEqual(self.path.read_bytes()[:16], b"SQLite format 3\x00")
                result = APP.extract(self.path, LIBRARY_DIR, "SYNTHETIC", "测试 & A", PASSWORD)
                self.assertEqual(result["status"], "unique")
                self.assertEqual(result["candidates"], [{"name": "测试 & A", "user_id": "原样 ID-🙂 "}])
                self.assertEqual(result["page_size"], size)
                self.assertNotIn("PRIVATE_PHONE", json.dumps(result))
                self.assertEqual(before, result["source_sha256"])
                connection = self.codec.open(self.path, PASSWORD, size)
                try:
                    with self.assertRaises(NATIVE.ReadError):
                        list(connection.query("CREATE TABLE forbidden(x)"))
                finally:
                    connection.close()
                self.assertEqual(NATIVE.file_hash(self.path), before)
                self.assertEqual(list(self.path.parent.glob(self.path.name + "-*")), [])

    def test_saved_header_vectors(self):
        """已保存的独立样本转写向量验证头部解密，不只验证同库生成/读取互通。"""
        vectors = Path(__file__).resolve().parents[1] / "docs/research/reports/nr-codec/synthetic-vectors.json"
        cases = json.loads(vectors.read_text(encoding="utf-8"))["pages"]
        for case in cases:
            if case["page"] != 1:
                continue
            with self.subTest(size=case["page_size"]):
                ciphertext = bytes.fromhex(case["ciphertext_hex"])
                header = self.codec.header(ciphertext[:32], bytes.fromhex(case["password_hex"]))
                expected = b"SQLite format 3\x00" + bytes((i * 17 + 3) & 255 for i in range(16, 32))
                self.assertEqual(header, expected)

    def test_schema_only_does_not_read_contacts(self):
        """坏联系人行不影响schema-only模式，结果只含表结构而无姓名/ID/XML。"""
        self.fixture(rows=[(1, "private-id", "PRIVATE_BAD_XML")])
        result = APP.inspect_schema(self.path, LIBRARY_DIR, PASSWORD)
        self.assertEqual(result["status"], "schema_only")
        self.assertEqual([item["table"] for item in result["tables"]], ["OrgStruct_SYNTHETIC"])
        self.assertNotIn("PRIVATE", json.dumps(result))
        self.assertNotIn("private-id", json.dumps(result))
        output = io.StringIO()
        args = ["--snapshot", str(self.path), "--library-dir", str(LIBRARY_DIR), "--schema-only",
                "--offline-confirmed", "--password-stdin"]
        self.assertEqual(APP.main(args, io.BytesIO(PASSWORD), output, io.StringIO()), 0)
        self.assertEqual(json.loads(output.getvalue())["status"], "schema_only")

    def test_wrong_password_and_nul(self):
        """口令错误、空值或NUL不尝试其他格式，不产生结果或修改库。"""
        self.fixture()
        for password in (b"wrong", b"", b"a\x00b"):
            with self.subTest(password=password):
                with self.assertRaises(NATIVE.ReadError):
                    APP.extract(self.path, LIBRARY_DIR, "SYNTHETIC", "测试 & A", password)

    def test_nul_id_not_truncated(self):
        """原生TEXT字节长度必须保留NUL，不能经c_char_p截断后接受错误ID。"""
        self.fixture(rows=[(1, "prefix\x00suffix", "<U><Name>A</Name></U>")])
        with self.assertRaises(APP.records.ContactError):
            APP.extract(self.path, LIBRARY_DIR, "SYNTHETIC", "A", PASSWORD)

    def test_cli_status_and_no_partial_output(self):
        """命令以JSON返回唯一/无匹配/同名，错误时不输出部分结果或密钥。"""
        self.fixture(rows=[(1, "a", "<U><Name>A</Name></U>"), (1, "b", "<U><Name>A</Name></U>")])
        args = ["--snapshot", str(self.path), "--library-dir", str(LIBRARY_DIR),
                "--identity", "SYNTHETIC", "--offline-confirmed", "--password-stdin"]
        for name, code, status in (("A", 4, "ambiguous"), ("absent", 3, "not_found")):
            output, errors = io.StringIO(), io.StringIO()
            self.assertEqual(APP.main(args + ["--name", name], io.BytesIO(PASSWORD), output, errors), code)
            self.assertEqual(json.loads(output.getvalue())["status"], status)
            self.assertEqual(errors.getvalue(), "")
        output, errors = io.StringIO(), io.StringIO()
        self.assertEqual(APP.main(args + ["--name", "A"], io.BytesIO(b"wrong"), output, errors), 1)
        self.assertEqual(output.getvalue(), "")
        self.assertNotIn("wrong", errors.getvalue())


if __name__ == "__main__":
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--library-dir", type=Path)
    options, remaining = parser.parse_known_args()
    LIBRARY_DIR = options.library_dir
    unittest.main(argv=[sys.argv[0], *remaining])
