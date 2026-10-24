# 本脚本生成 contacts/groups 包端到端测试所需的合成测试库与期望清单；只使用人工合成数据。
#
# nr 联系人库：明文库由 Python sqlite3 按已恢复的 OrgStruct 建表结构构造，再逐页用 nr codec
# 报告中已验证的样本转写（verify_codec.py 的 derive/material/crypt，OpenSSL 执行 AES-CBC）加密。
# qrp 群库：按 qgroup-id 报告恢复的 QGroupInfo 建表结构构造明文 SQLite（客户端打开路径不装 codec）。
# 重放：python -I -B tools/gen_test_fixtures.py   （依赖同目录树的证据文件与 openssl）
import argparse
import hashlib
import importlib.util
import json
import pathlib
import sqlite3
import sys
import tempfile

REPO_ROOT = pathlib.Path(__file__).resolve().parents[1]
VERIFY_CODEC = REPO_ROOT / "docs" / "research" / "reports" / "nr-codec" / "verify_codec.py"

# 人工合成口令与身份后缀，仅用于测试；不是客户端实际口令或真实身份。
FIXTURE_PASSWORD = b"nwt-go-contacts-fixture"
PAGE_SIZE = 1024

# qrp 群库夹具：覆盖唯一群名、同名歧义、溢出正文、空群名跳过与坏 ID 行整体失败。
QGROUP_DDL = (
    "CREATE TABLE {table} ("
    "PriKey INTEGER PRIMARY KEY AUTOINCREMENT, "
    "QGroupId TEXT, Name TEXT, Master TEXT, Intr TEXT, Ann TEXT, "
    "InfoVer INTEGER, UserVer INTEGER, CreateTime INTEGER, "
    "Reserved1 TEXT, Reserved2 TEXT, Reserved3 INTEGER, Reserved4 INTEGER)"
)
QGROUP_ROWS_A = [
    # (QGroupId, Name, Master, Intr)——Intr 放长文本以触发溢出页。
    ("g-alpha-001", "测试群 Alpha", "u-alice-001", "普通简介"),
    ("g-beta-002", "Beta 群", "u-bob-002", "x"),
    ("g-beta-003", "Beta 群", "u-carol-003", "y"),
    ("g-gamma-004", "Gamma 群", "u-dave-005", "z" * 6000),
    ("g-ghost-005", "", "u-bob-002", "空群名跳过"),
]
QGROUP_ROWS_B = [
    ("", "坏 ID 群", "u-bad-101", "QGroupId 为空应整体失败"),
]
EXPECTED_GROUPS = {
    "QGroupInfo_TESTID_A": [
        ["测试群 Alpha", "g-alpha-001"],
        ["Beta 群", "g-beta-002"],
        ["Beta 群", "g-beta-003"],
        ["Gamma 群", "g-gamma-004"],
    ],
}

# 三张身份表：A 覆盖正常/同名/溢出/空名/类型过滤/实体还原，B 是损坏行，C 是最小正常表。
DDL = (
    "CREATE TABLE {table} ("
    "PriKey INTEGER PRIMARY KEY AUTOINCREMENT, "
    "UpdateTime INTEGER, OnlineTime INTEGER, "
    "Type INTEGER, Id TEXT, Data TEXT, UNIQUE(Id))"
)
ROWS_A = [
    # (UpdateTime, OnlineTime, Type, Id, Data)——时间列混用 NULL、小整数与大整数以覆盖不同序列类型。
    (None, None, 1, "u-alice-001", "<U><Name>Alice 张三</Name><Sign>hi</Sign></U>"),
    (1700000000, None, 1, "u-bob-002", "<U><Name>Bob</Name></U>"),
    (1700000001, 1700000002, 1, "u-carol-003", "<U><Name>Carol</Name></U>"),
    (1700000003, 1700000004, 1, "u-carol-004", "<U><Name>Carol</Name></U>"),
    (1700000005, 1700000006, 1, "u-dave-005",
     "<U><Name>Dave</Name><Sign>" + "x" * 6000 + "</Sign></U>"),
    (1700000007, 1700000008, 1, "u-ghost-006", "<U><Name></Name></U>"),
    (1700000009, 1700000010, 1, "u-entity-007", "<U><Name>A&amp;B</Name></U>"),
    (1700000011, 1700000012, 2, "dept-008", "<D><Name>SomeDept</Name></D>"),
]
ROWS_B = [
    (1700000013, 1700000014, 1, "u-bad-101", "<V><Name>x</Name></V>"),
]
ROWS_C = [
    (1700000015, 1700000016, 1, "u-erin-201", "<U><Name>Erin</Name></U>"),
]

# LoadUsers 应输出的用户联系人（Type=1 且 Name 非空），按表组织；B 表因损坏行只能整体失败。
EXPECTED_USERS = {
    "OrgStruct_TESTID_A": [
        ["Alice 张三", "u-alice-001"],
        ["Bob", "u-bob-002"],
        ["Carol", "u-carol-003"],
        ["Carol", "u-carol-004"],
        ["Dave", "u-dave-005"],
        ["A&B", "u-entity-007"],
    ],
    "OrgStruct_TESTID_C": [
        ["Erin", "u-erin-201"],
    ],
}


def load_verify_codec():
    """以模块方式加载报告目录中的 verify_codec.py，复用其已验证的 codec 转写函数。"""
    spec = importlib.util.spec_from_file_location("verify_codec", VERIFY_CODEC)
    module = importlib.util.module_from_spec(spec)
    spec.loader.exec_module(module)
    return module


def build_plaintext(path, encoding=None):
    """按恢复的建表结构构造明文合成库；encoding 为 None 时用默认 UTF-8，
    传 "UTF-16le" 时生成与真实客户端库一致的编码。"""
    connection = sqlite3.connect(path)
    try:
        connection.execute(f"PRAGMA page_size={PAGE_SIZE}")
        if encoding:
            connection.execute(f"PRAGMA encoding='{encoding}'")
        rows = {
            "OrgStruct_TESTID_A": ROWS_A,
            "OrgStruct_TESTID_B": ROWS_B,
            "OrgStruct_TESTID_C": ROWS_C,
        }
        for table, table_rows in rows.items():
            connection.execute(DDL.format(table=table))
            connection.executemany(
                f"INSERT INTO {table} (UpdateTime, OnlineTime, Type, Id, Data)"
                " VALUES (?, ?, ?, ?, ?)",
                table_rows,
            )
        connection.commit()
    finally:
        connection.close()
    return sorted(rows), EXPECTED_USERS


def encrypt_database(verify, plaintext):
    """用样本转写逐页加密明文，返回密文与页数。"""
    base_key = verify.derive(FIXTURE_PASSWORD)["base_key"]
    ciphertext = bytearray()
    for index in range(len(plaintext) // PAGE_SIZE):
        # 页号从 1 开始；legacy=1 整页 CBC，第一页文件头同样在密文中。
        page = plaintext[index * PAGE_SIZE:(index + 1) * PAGE_SIZE]
        ciphertext += verify.crypt(base_key, index + 1, page)
    return bytes(ciphertext), len(plaintext) // PAGE_SIZE


def build_qrp_plaintext(path, encoding=None):
    """按 qgroup-id 报告恢复的建表结构构造明文 qrp 群库；返回表名列表。"""
    connection = sqlite3.connect(path)
    try:
        connection.execute(f"PRAGMA page_size={PAGE_SIZE}")
        if encoding:
            connection.execute(f"PRAGMA encoding='{encoding}'")
        rows = {
            "QGroupInfo_TESTID_A": QGROUP_ROWS_A,
            "QGroupInfo_TESTID_B": QGROUP_ROWS_B,
        }
        for table, table_rows in rows.items():
            connection.execute(QGROUP_DDL.format(table=table))
            connection.executemany(
                f"INSERT INTO {table} (QGroupId, Name, Master, Intr) VALUES (?, ?, ?, ?)",
                table_rows,
            )
        connection.commit()
    finally:
        connection.close()
    return sorted(rows)


def main():
    parser = argparse.ArgumentParser(
        description="生成 contacts/groups 包端到端测试用的合成测试库；不读取客户端或真实数据库。")
    args = parser.parse_args()

    verify = load_verify_codec()
    # 一、nr 联系人库（加密，UTF-8 与 UTF-16le 双编码）。
    nr_dir = REPO_ROOT / "internal" / "contacts" / "testdata"
    nr_dir.mkdir(parents=True, exist_ok=True)
    variants = [
        ("nr-synthetic.db", None),
        ("nr-synthetic-utf16.db", "UTF-16le"),
    ]
    manifest = {
        "password": FIXTURE_PASSWORD.decode("ascii"),
        "password_hex": FIXTURE_PASSWORD.hex(),
        "page_size": PAGE_SIZE,
        "codec_commit": verify.COMMIT,
    }
    expected = None
    tables = None
    for filename, encoding in variants:
        with tempfile.TemporaryDirectory() as temp:
            plain_path = pathlib.Path(temp) / "plain.db"
            tables, expected = build_plaintext(plain_path, encoding)
            plaintext = plain_path.read_bytes()
        if len(plaintext) % PAGE_SIZE != 0:
            raise SystemExit("明文库长度不是页长整数倍，生成中止")
        ciphertext, pages = encrypt_database(verify, plaintext)
        (nr_dir / filename).write_bytes(ciphertext)
        key = "utf16_ciphertext_sha256" if encoding else "ciphertext_sha256"
        manifest[key] = hashlib.sha256(ciphertext).hexdigest()
        print(f"已生成 {nr_dir / filename}（{len(ciphertext)} 字节，{pages} 页，编码 {encoding or 'UTF-8'}）")
    manifest["tables"] = tables
    manifest["expected_users"] = expected
    (nr_dir / "nr-synthetic-manifest.json").write_text(
        json.dumps(manifest, ensure_ascii=False, indent=2) + "\n", encoding="utf-8")
    print(f"nr UTF-8 密文 SHA-256: {manifest['ciphertext_sha256']}")
    print(f"nr UTF-16 密文 SHA-256: {manifest['utf16_ciphertext_sha256']}")

    # 二、qrp 群库（明文，客户端打开路径不装 codec）。
    qrp_dir = REPO_ROOT / "internal" / "groups" / "testdata"
    qrp_dir.mkdir(parents=True, exist_ok=True)
    qrp_manifest = {"page_size": PAGE_SIZE, "expected_groups": EXPECTED_GROUPS}
    for filename, encoding in [("qrp-synthetic.db", None), ("qrp-synthetic-utf16.db", "UTF-16le")]:
        with tempfile.TemporaryDirectory() as temp:
            qrp_path = pathlib.Path(temp) / "qrp.db"
            qrp_tables = build_qrp_plaintext(qrp_path, encoding)
            data = qrp_path.read_bytes()
        (qrp_dir / filename).write_bytes(data)
        key = "utf16_sha256" if encoding else "sha256"
        qrp_manifest[key] = hashlib.sha256(data).hexdigest()
        print(f"已生成 {qrp_dir / filename}（{len(data)} 字节，{len(data) // PAGE_SIZE} 页，编码 {encoding or 'UTF-8'}）")
    qrp_manifest["tables"] = qrp_tables
    (qrp_dir / "qrp-synthetic-manifest.json").write_text(
        json.dumps(qrp_manifest, ensure_ascii=False, indent=2) + "\n", encoding="utf-8")
    print(f"qrp UTF-8 SHA-256: {qrp_manifest['sha256']}")
    print(f"qrp UTF-16 SHA-256: {qrp_manifest['utf16_sha256']}")


if __name__ == "__main__":
    main()
