"""使用合成 SQLite 内存库验证联系人 schema、XML 安全限制及精确名称匹配。"""

import importlib.util
from pathlib import Path
import sqlite3
import sys
import unittest


# 显式路径加载支持 python -I，不依赖工作目录或 sys.path 中的项目目录。
sys.dont_write_bytecode = True
_SPEC = importlib.util.spec_from_file_location(
    "contactid_records", Path(__file__).resolve().with_name("contactid_records.py")
)
_RECORDS = importlib.util.module_from_spec(_SPEC)
_SPEC.loader.exec_module(_RECORDS)

_COLUMNS = (
    "PriKey INTEGER PRIMARY KEY AUTOINCREMENT, UpdateTime INTEGER, "
    "OnlineTime INTEGER, Type INTEGER, Id TEXT, Data TEXT, UNIQUE(Id)"
)
_TABLE = "OrgStruct_synthetic"


def _quote(identifier):
    """引用合成表名，返回 SQL 标识符；参数为测试常量，不抛业务异常。"""
    return '"' + identifier.replace('"', '""') + '"'


class ContactRecordsTests(unittest.TestCase):
    """验证合成联系人数据；由 unittest 构造并报告断言失败，不读取磁盘数据库。"""

    def setUp(self):
        """建立独立内存库及查询日志；无参数和返回值，SQLite 失败直接传播。"""
        self.db = self._database()
        self.calls = []

    def _database(self, columns=_COLUMNS, table=_TABLE):
        """按列定义和表名创建内存库并登记关闭；返回连接，SQLite 错误直接传播。"""
        db = sqlite3.connect(":memory:")
        self.addCleanup(db.close)
        db.execute(f"CREATE TABLE {_quote(table)} ({columns})")
        return db

    def _insert(self, user_id="synthetic-id", xml="<U><Name>Alice</Name></U>",
                kind=1, table=_TABLE, db=None):
        """向指定合成表写入 ID、XML 和类型；无返回值，违反 schema 时抛 SQLite 错误。"""
        if db is None:
            db = self.db
        db.execute(
            f"INSERT INTO {_quote(table)} (Type, Id, Data) VALUES (?, ?, ?)",
            (kind, user_id, xml),
        )

    def _query(self, sql, parameters=()):
        """记录 SQL 与参数并返回合成查询行；SQLite 错误直接传播给核心。"""
        self.calls.append((sql, parameters))
        return self.db.execute(sql, parameters).fetchall()

    def _find(self, name="Alice", identity="synthetic"):
        """开启连接级只读后查询指定合成身份和名称；返回核心结果或传播 ContactError。"""
        self.db.execute("PRAGMA query_only = ON")
        return _RECORDS.find_contact(self._query, identity, name)

    def _reject_row(self, user_id="bad-id", xml="<U><Name>Alice</Name></U>"):
        """添加坏行及有效同名行并断言失败；无返回值，漏报时抛断言异常。"""
        self._insert("valid-id")
        self._insert(user_id, xml)
        with self.assertRaises(_RECORDS.ContactError):
            self._find()

    def test_unique(self):
        """唯一匹配只返回固定字段和原始名称与 ID。"""
        self._insert()
        self.assertEqual(self._find(), {
            "status": "unique", "name": "Alice", "matching_rows": 1,
            "table": _TABLE,
            "candidates": [{"name": "Alice", "user_id": "synthetic-id"}],
        })

    def test_type_filter(self):
        """非用户行即使 XML 或 ID 无效也不读取和解释。"""
        self._insert()
        self._insert(None, "not XML", kind=2)
        self._insert("organization", "<U><Name>Alice</Name></U>", kind=0)
        self.assertEqual(self._find()["matching_rows"], 1)
        self.assertEqual(self.calls[-1], (
            f'SELECT Id, Data FROM "{_TABLE}" WHERE Type = ?', (1,),
        ))

    def test_missing(self):
        """未找到名称时返回零候选，不把其他联系人输出。"""
        self._insert()
        result = self._find("absent")
        self.assertEqual(result["status"], "not_found")
        self.assertEqual(result["name"], "absent")
        self.assertEqual(result["matching_rows"], 0)
        self.assertEqual(result["candidates"], [])

    def test_same_name(self):
        """同名返回全部原始 ID，禁止选取第一行。"""
        for user_id in ("first", "second", "third"):
            self._insert(user_id)
        result = self._find()
        self.assertEqual(result["status"], "ambiguous")
        self.assertEqual(result["matching_rows"], 3)
        self.assertCountEqual(result["candidates"], [
            {"name": "Alice", "user_id": value}
            for value in ("first", "second", "third")
        ])

    def test_case_sensitive(self):
        """名称不折叠大小写。"""
        self._insert()
        self.assertEqual(self._find("alice")["status"], "not_found")

    def test_no_trim(self):
        """查询名称的前导空格保持不变。"""
        self._insert()
        self.assertEqual(self._find(" Alice")["status"], "not_found")

    def test_trailing_space(self):
        """XML 和输入名称中的尾部空格均保留。"""
        self._insert(xml="<U><Name>Alice </Name></U>")
        self.assertEqual(self._find("Alice")["status"], "not_found")
        self.assertEqual(self._find("Alice ")["candidates"][0]["name"], "Alice ")

    def test_xml_entities(self):
        """实体还原后精确比较，CDATA 与拆分文本正确拼接。"""
        name = "A&B<中>\"'🙂"
        self._insert(xml="<U><Name>A&amp;B&lt;中&gt;&quot;&apos;<![CDATA[🙂]]></Name></U>")
        self.assertEqual(self._find(name)["candidates"][0]["name"], name)

    def test_quote(self):
        """名称中的引号和 SQL 片段只参与 Python 字符串比较。"""
        name = "O'Brien'; SELECT * FROM secrets; --"
        self._insert(xml=f"<U><Name>{name}</Name></U>")
        self.assertEqual(self._find(name)["status"], "unique")
        self.assertTrue(all(name not in sql for sql, _ in self.calls))

    def test_null_id(self):
        """NULL ID 不能被有效匹配行掩盖。"""
        self._reject_row(user_id=None)

    def test_empty_id(self):
        """空 ID 明确失败。"""
        self._reject_row(user_id="")

    def test_embedded_nul_id(self):
        """ID 含 NUL 时明确失败。"""
        self._reject_row(user_id="prefix\x00suffix")

    def test_malformed_xml(self):
        """坏 XML 不能跳过后声称已有候选唯一。"""
        self._reject_row(xml="<U><Name>Alice</U>")

    def test_duplicate_name_nodes(self):
        """两个直接 Name 即使内容相同也拒绝。"""
        self._reject_row(xml="<U><Name>Alice</Name><Name>Alice</Name></U>")

    def test_missing_name_node(self):
        """只有个人资料而无直接 Name 时拒绝。"""
        self._reject_row(xml="<U><Sign>Alice</Sign></U>")

    def test_id_not_prikey(self):
        """返回 Id 而不是自增 PriKey。"""
        self._insert("not-a-row-number")
        self.assertEqual(self._find()["candidates"][0]["user_id"], "not-a-row-number")

    def test_remark_is_not_canonical_name(self):
        """备注和其他资料不能冒充原名，也不出现在结果中。"""
        self._insert(xml=("<U><Name>Original</Name><Remark>Alice</Remark>"
                          "<Phone>PRIVATE_PHONE</Phone><Sign>PRIVATE_SIGN</Sign></U>"))
        self.assertEqual(self._find()["status"], "not_found")
        result = self._find("Original")
        self.assertNotIn("PRIVATE", repr(result))
        self.assertNotIn("Remark", repr(result))

    def test_readonly_rejects_write(self):
        """调用方只读连接允许核心读取并实际拒绝写入。"""
        self._insert()
        self.assertEqual(self._find()["status"], "unique")
        with self.assertRaises(sqlite3.OperationalError):
            self.db.execute(f"INSERT INTO {_TABLE}(Id) VALUES ('forbidden')")

    def test_identity_required(self):
        """没有明确身份或含 NUL 时不执行任何查询。"""
        for identity in ("", None, 17, "bad\x00identity"):
            with self.subTest(identity=identity):
                with self.assertRaises(_RECORDS.ContactError):
                    self._find(identity=identity)
        self.assertEqual(self.calls, [])

    def test_missing_identity_table(self):
        """其他身份表存在不代表所选身份存在。"""
        with self.assertRaises(_RECORDS.ContactError):
            self._find(identity="missing")
        self.assertEqual(len(self.calls), 1)

    def test_multiple_identity_isolation(self):
        """多身份存在时只读明确指定表，完全不读取群表或其他身份。"""
        self._insert()
        self.db.execute("CREATE TABLE OrgStruct_other (private TEXT)")
        self.db.execute("INSERT INTO OrgStruct_other VALUES ('PRIVATE_OTHER')")
        self.db.execute("CREATE TABLE GroupMembers (private TEXT)")
        self.db.execute("INSERT INTO GroupMembers VALUES ('PRIVATE_GROUP')")
        reads = []

        def authorize(action, arg1, arg2, database, trigger):
            """记录 SQLite 读表并拒绝其他表；参数来自 SQLite，返回授权码，不抛异常。"""
            if action == sqlite3.SQLITE_READ:
                reads.append(arg1)
                if arg1 not in ("sqlite_master", _TABLE):
                    return sqlite3.SQLITE_DENY
            return sqlite3.SQLITE_OK

        self.db.set_authorizer(authorize)
        self.assertEqual(self._find()["status"], "unique")
        self.assertEqual(set(reads), {"sqlite_master", _TABLE})
        for sql, _ in self.calls:
            self.assertNotIn("OrgStruct_other", sql)
            self.assertNotIn("GroupMembers", sql)
        self.assertEqual(len(self.calls), 5)

    def test_identifier_quotes_and_unicode(self):
        """来自实际表枚举的含引号、空格及 Unicode 身份可安全引用。"""
        identity = ' 身份";-- '
        table = "OrgStruct_" + identity
        self.db = self._database(table=table)
        self._insert(table=table)
        self.assertEqual(self._find(identity=identity)["table"], table)

    def test_identity_is_exact(self):
        """身份必须逐字符对应枚举名，不能利用 SQLite 大小写宽松匹配。"""
        with self.assertRaises(_RECORDS.ContactError):
            self._find(identity="SYNTHETIC")

    def test_view_is_not_table(self):
        """同名视图不能替代实际联系人表。"""
        self.db.execute(f'CREATE VIEW OrgStruct_view AS SELECT * FROM "{_TABLE}"')
        with self.assertRaises(_RECORDS.ContactError):
            self._find(identity="view")
        self.assertEqual(len(self.calls), 1)

    def test_schema_rejected(self):
        """列顺序、声明类型、主键和隐藏生成列必须与恢复结构一致。"""
        variants = (
            _COLUMNS.replace("UpdateTime INTEGER, OnlineTime INTEGER", "OnlineTime INTEGER, UpdateTime INTEGER"),
            _COLUMNS.replace("OnlineTime INTEGER", "OnlineTime INT"),
            _COLUMNS.replace("Id TEXT", "Id BLOB"),
            _COLUMNS.replace("Data TEXT", "Data VARCHAR"),
            _COLUMNS.replace(" PRIMARY KEY AUTOINCREMENT", ""),
            _COLUMNS.replace("PriKey INTEGER PRIMARY KEY AUTOINCREMENT", "PriKey INTEGER").replace("Type INTEGER", "Type INTEGER PRIMARY KEY"),
            _COLUMNS.replace(", UNIQUE(Id)", ", Extra TEXT, UNIQUE(Id)"),
            _COLUMNS.replace(", UNIQUE(Id)", ", Extra TEXT GENERATED ALWAYS AS (Id) VIRTUAL, UNIQUE(Id)"),
        )
        for columns in variants:
            with self.subTest(columns=columns):
                self.db = self._database(columns)
                with self.assertRaises(_RECORDS.ContactError):
                    self._find()
                self.assertFalse(any(sql.startswith("SELECT Id") for sql, _ in self.calls))

    def test_missing_unique_index(self):
        """没有真实唯一索引时不能读取用户行。"""
        self.db = self._database(_COLUMNS.replace(", UNIQUE(Id)", ""))
        self.db.execute(f'CREATE INDEX ordinary ON "{_TABLE}" (Id)')
        with self.assertRaises(_RECORDS.ContactError):
            self._find()

    def test_invalid_unique_indexes(self):
        """部分、复合、表达式或非二进制唯一索引不能替代 UNIQUE(Id)。"""
        for definition in (
            "Id, Type", "lower(Id)", "Data", "Id COLLATE NOCASE", "Id) WHERE Type=1 --",
        ):
            with self.subTest(definition=definition):
                self.db = self._database(_COLUMNS.replace(", UNIQUE(Id)", ""))
                self.db.execute(f'CREATE UNIQUE INDEX unsuitable ON "{_TABLE}" ({definition})')
                with self.assertRaises(_RECORDS.ContactError):
                    self._find()

    def test_explicit_unique_index(self):
        """接受真实的独立 UNIQUE(Id) 索引，不依赖自动索引名称。"""
        self.db = self._database(_COLUMNS.replace(", UNIQUE(Id)", ""))
        self.db.execute(f'CREATE INDEX ordinary ON "{_TABLE}" (Data)')
        self.db.execute(f'CREATE UNIQUE INDEX "unique\"\"id" ON "{_TABLE}" (Id)')
        self._insert()
        self.assertEqual(self._find()["status"], "unique")

    def test_nested_name(self):
        """Name 内嵌元素和仅嵌套存在的 Name 均拒绝。"""
        for xml in (
            "<U><Name>A<b>li</b>ce</Name></U>",
            "<U><Profile><Name>Alice</Name></Profile></U>",
        ):
            with self.subTest(xml=xml):
                self.db = self._database()
                self._reject_row(xml=xml)

    def test_other_nested_name_ignored(self):
        """无关资料中嵌套的 Name 不参与直接 Name 计数或输出。"""
        self._insert(xml="<U><Name>Alice</Name><Profile><Name>PRIVATE</Name></Profile></U>")
        self.assertEqual(self._find()["candidates"], [{"name": "Alice", "user_id": "synthetic-id"}])

    def test_dtd_and_external_entities(self):
        """无害 DTD、实体展开及本地或网络外部实体全部拒绝。"""
        prefixes = (
            "<!DOCTYPE U>",
            '<!DOCTYPE U [<!ENTITY secret "PRIVATE">]>',
            '<!DOCTYPE U SYSTEM "https://invalid.example/private">',
            '<!DOCTYPE U [<!ENTITY secret SYSTEM "file:///PRIVATE">]>',
            '<!DOCTYPE U [<!ENTITY % ext SYSTEM "https://invalid.example/private">%ext;]>',
        )
        for prefix in prefixes:
            with self.subTest(prefix=prefix):
                self.db = self._database()
                self._reject_row(xml=prefix + "<U><Name>Alice</Name></U>")

    def test_invalid_xml_roots(self):
        """非 U 根、多根、未定义实体和带命名空间的异名根均拒绝。"""
        for xml in (
            "<User><Name>Alice</Name></User>",
            "<U><Name>Alice</Name></U><U/>",
            "<U><Name>&undefined;</Name></U>",
            '<U xmlns="urn:other"><Name>Alice</Name></U>',
        ):
            with self.subTest(xml=xml):
                self.db = self._database()
                self._reject_row(xml=xml)

    def test_nontext_xml(self):
        """NULL、数字及 BLOB XML 明确失败。"""
        for data in (None, 17, b"<U><Name>Alice</Name></U>"):
            with self.subTest(data=data):
                self.db = self._database()
                self._reject_row(xml=data)

    def test_nontext_id(self):
        """SQLite BLOB Id 不隐式转换为文本。"""
        self._reject_row(user_id=b"binary-id")

    def test_bad_unmatched_record(self):
        """不匹配名称的坏 Id 也必须报错，而不是返回有效行唯一。"""
        self._reject_row(user_id=None, xml="<U><Name>Other</Name></U>")

    def test_raw_unicode_and_whitespace_ids(self):
        """ID 不套 ASCII 或长度正则，Unicode、空白及长值原样返回。"""
        ids = ("  用户🙂 id\t\n ", " ", "x" * 4097)
        for user_id in ids:
            self._insert(user_id)
        result = self._find()
        self.assertEqual(result["status"], "ambiguous")
        self.assertCountEqual([row["user_id"] for row in result["candidates"]], ids)

    def test_empty_and_unicode_names(self):
        """空 Name 有效且 Unicode 不做规范化。"""
        self._insert("empty", "<U><Name/></U>")
        self._insert("decomposed", "<U><Name>é</Name></U>")
        self.assertEqual(self._find("")["candidates"][0]["user_id"], "empty")
        self.assertEqual(self._find("é")["status"], "not_found")
        self.assertEqual(self._find("é")["status"], "unique")

    def test_invalid_query_name(self):
        """非文本查询名在访问数据库前失败。"""
        with self.assertRaises(_RECORDS.ContactError):
            self._find(name=None)
        self.assertEqual(self.calls, [])

    def test_error_does_not_leak_xml(self):
        """坏记录异常既不含 XML 和资料，也不附带原解析异常。"""
        self._insert(xml="<U><Name>Alice</Name><Mail>PRIVATE_MAIL</Mail><BROKEN")
        with self.assertRaises(_RECORDS.ContactError) as caught:
            self._find()
        self.assertNotIn("PRIVATE_MAIL", str(caught.exception))
        self.assertNotIn("<U>", str(caught.exception))
        self.assertIsNone(caught.exception.__cause__)
        self.assertTrue(caught.exception.__suppress_context__)

    def test_query_error_is_sanitized(self):
        """查询及惰性行迭代失败统一转换，原异常内容不泄漏。"""
        def broken(sql, parameters=()):
            """模拟外部查询失败；参数未使用，无返回值，始终抛含敏感标记的错误。"""
            raise RuntimeError("PRIVATE_XML <U><Mail>PRIVATE</Mail></U>")

        def lazy_broken(sql, parameters=()):
            """模拟迭代中查询失败；忽略参数，产生行前抛含敏感标记的错误。"""
            yield from broken(sql, parameters)

        for query in (broken, lazy_broken):
            with self.subTest(query=query.__name__):
                with self.assertRaises(_RECORDS.ContactError) as caught:
                    _RECORDS.find_contact(query, "synthetic", "Alice")
                self.assertNotIn("PRIVATE", str(caught.exception))
                self.assertIsNone(caught.exception.__cause__)
                self.assertTrue(caught.exception.__suppress_context__)


if __name__ == "__main__":
    unittest.main()
