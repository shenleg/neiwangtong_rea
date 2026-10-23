"""用人工口令验证样本指令转写与固定版参考实现；只处理本脚本生成的合成数据。"""
import argparse
import ctypes as ct
import hashlib
import json
import math
import pathlib
import re
import sqlite3
import struct
import subprocess
import tempfile
import xml.etree.ElementTree as ET

PADDING = bytes.fromhex(
    "28bf4e5e4e758a4164004e56fffa01082e2e00b6d0683e802f0ca9fe6453697a"
)
PASSWORD = b"nr-codec-synthetic-only-2026"
COMMIT = "f29b06b424e556cf0ad8b19646b30dfc3e3e36d8"
PAGE_SIZES = (512, 1024, 2048, 4096, 8192, 16384, 32768, 65536)


def evidence(name):
    """读取同目录保存的单条 REA 规范化结果；格式错误或缺失直接抛出异常。"""
    path = pathlib.Path(__file__).parent / name
    return json.loads(path.read_text())["response"]["result"]["structuredContent"]["normalized_result"]


MD5_INSTRUCTIONS = evidence("0019-read_function_instructions.json")["instructions"]
MD5_CONSTANTS = [int(match[1], 16) for line in MD5_INSTRUCTIONS
                 if (match := re.search(r"LEA .* \+ 0x([0-9a-f]{1,8})\]", line))]
MD5_SHIFTS = [int(match[2], 16) if match[1] == "ROL" else 32 - int(match[2], 16)
              for line in MD5_INSTRUCTIONS
              if (match := re.search(r"(ROL|ROR) \w+, 0x([0-9a-f]+)", line))]


def md5(data):
    """转写 0x6f8970 的 MD5；使用保存指令中的常量和移位，并校验标准库结果。"""
    assert MD5_CONSTANTS == [int(abs(math.sin(i + 1)) * (1 << 32)) for i in range(64)]
    assert MD5_SHIFTS == [7, 12, 17, 22] * 4 + [5, 9, 14, 20] * 4 + [4, 11, 16, 23] * 4 + [6, 10, 15, 21] * 4
    padded = data + b"\x80" + bytes((55 - len(data)) % 64) + struct.pack("<Q", len(data) * 8)
    state = [0x67452301, 0xefcdab89, 0x98badcfe, 0x10325476]
    for offset in range(0, len(padded), 64):
        words = struct.unpack("<16I", padded[offset:offset + 64])
        a, b, c, d = state
        for i in range(64):
            if i < 16:
                f, g = (b & c) | (~b & d), i
            elif i < 32:
                f, g = (b & d) | (c & ~d), (5 * i + 1) % 16
            elif i < 48:
                f, g = b ^ c ^ d, (3 * i + 5) % 16
            else:
                f, g = c ^ (b | ~d), (7 * i) % 16
            value = (a + f + MD5_CONSTANTS[i] + words[g]) & 0xffffffff
            shift = MD5_SHIFTS[i]
            value = ((value << shift) | (value >> (32 - shift))) & 0xffffffff
            a, b, c, d = d, (b + value) & 0xffffffff, b, c
        state = [(old + new) & 0xffffffff for old, new in zip(state, (a, b, c, d))]
    result = struct.pack("<4I", *state)
    assert result == hashlib.md5(data, usedforsecurity=False).digest()
    return result


def rc4(key, data):
    """以全新状态执行旧 codec 的 RC4；返回同长度字节，不保留跨调用状态。"""
    state = list(range(256))
    j = 0
    for i in range(256):
        j = (j + state[i] + key[i % len(key)]) & 255
        state[i], state[j] = state[j], state[i]
    a = b = 0
    output = bytearray()
    for value in data:
        a = (a + 1) & 255
        b = (b + state[a]) & 255
        state[a], state[b] = state[b], state[a]
        output.append(value ^ state[(state[a] + state[b]) & 255])
    return bytes(output)


def derive(password):
    """转写 0x6f9550/0x6f95a0 的字节口令派生，返回合成中间材料与基础密钥。"""
    user = password[:32] + PADDING[:max(0, 32 - len(password))]
    owner_digest = md5(PADDING)
    for _ in range(50):
        owner_digest = md5(owner_digest)
    owner = user
    for iteration in range(20):
        owner = rc4(bytes(value ^ iteration for value in owner_digest), owner)
    key = md5(user + owner)
    for _ in range(50):
        key = md5(key)
    return {"user_pad": user, "owner_digest": owner_digest, "owner_key": owner, "base_key": key}


def signed32(value):
    """返回 x86 低 32 位的有符号解释，用于重现 IMUL/SUB 的回绕。"""
    return (value + 0x80000000) % 0x100000000 - 0x80000000


def material(key, page):
    """逐条转写 0x6f92c0/0x6f9170，返回页密钥、IV 及其摘要输入。"""
    page_input = key + struct.pack("<I", page) + b"sAlT"
    z = signed32(page + 1)
    words = []
    for _ in range(4):
        # 样本把 /52774 优化成有符号高位乘法和移位；不能把该常量误作 LCG 乘数。
        high = (z * 0x4f7a09cd) >> 32
        quotient = high >> 14
        quotient += (quotient & 0xffffffff) >> 31
        z = signed32(signed32(z * 0x9ef4) - signed32(quotient * 0x7fffff07))
        if z < 0:
            z = signed32(z + 0x7fffff07)
        words.append(z)
    iv_input = struct.pack("<4I", *words)
    return {"page_key_input": page_input, "page_key": md5(page_input),
            "iv_input": iv_input, "iv": md5(iv_input)}


def multiply(a, b):
    """返回 AES 有限域乘积，供公开查表常量的独立校验使用。"""
    value = 0
    for _ in range(8):
        if b & 1:
            value ^= a
        a = ((a << 1) ^ (0x11b if a & 128 else 0)) & 255
        b >>= 1
    return value


class SampleAES:
    """转写样本的 AES 轮与 CBC，常量仅取自已保存的非敏感算法表。"""

    def __init__(self):
        """加载并逐项验证 S-box、轮表、逆混合表和轮常量；不匹配则断言失败。"""
        tables = bytes.fromhex(evidence("0031-read_bytes.json")["bytes_hex"])
        self.sbox = tables[:256]
        self.inverse = tables[8448:8704]
        self.encrypt_tables = [struct.unpack("<256I", tables[256 + i * 1024:1280 + i * 1024]) for i in range(4)]
        self.decrypt_tables = [struct.unpack("<256I", tables[4352 + i * 1024:5376 + i * 1024]) for i in range(4)]
        mix = bytes.fromhex(evidence("0042-read_bytes.json")["bytes_hex"])
        self.mix_tables = [struct.unpack("<256I", mix[i * 1024:(i + 1) * 1024]) for i in range(4)]
        self.rcon = struct.unpack("<10I", bytes.fromhex(evidence("0032-read_bytes.json")["bytes_hex"]))
        for value in range(256):
            inverse = 0 if value == 0 else next(candidate for candidate in range(1, 256) if multiply(value, candidate) == 1)
            expected = inverse
            for shift in range(1, 5):
                expected ^= ((inverse << shift) | (inverse >> (8 - shift))) & 255
            assert self.sbox[value] == expected ^ 0x63
            assert self.inverse[self.sbox[value]] == value
            for column in range(4):
                forward = [multiply(self.sbox[value], coefficient) for coefficient in (2, 1, 1, 3)]
                backward = [multiply(self.inverse[value], coefficient) for coefficient in (14, 9, 13, 11)]
                unmixed = [multiply(value, coefficient) for coefficient in (14, 9, 13, 11)]
                for source, table in ((forward, self.encrypt_tables), (backward, self.decrypt_tables), (unmixed, self.mix_tables)):
                    rotated = source[-column:] + source[:-column] if column else source
                    assert table[column][value] == int.from_bytes(bytes(rotated), "little")
        value = 1
        for expected in self.rcon:
            assert expected == value
            value = multiply(value, 2)

    def schedule(self, key):
        """转写 0x6f7d50 的 128 位扩展；返回 11 轮小端字密钥。"""
        words = list(struct.unpack("<4I", key))
        for constant in self.rcon:
            last = words[-1]
            temp = sum(self.sbox[(last >> (8 * ((i + 1) % 4))) & 255] << (8 * i) for i in range(4)) ^ constant
            for _ in range(4):
                words.append(words[-4] ^ temp)
                temp = words[-1]
        return words

    def block(self, words, data, decrypt=False):
        """转写 0x6f8010/0x6f83f0 的轮次与字节置换，返回一个块。"""
        direction = -1 if decrypt else 1
        rounds = list(range(10, -1, -1)) if decrypt else list(range(11))
        sbox = self.inverse if decrypt else self.sbox
        tables = self.decrypt_tables if decrypt else self.encrypt_tables
        keys = words[:]
        if decrypt:
            # 0x6f7f20 仅对第1..9轮执行逆 MixColumns，不变换首尾轮密钥。
            for i in range(4, 40):
                value = keys[i]
                keys[i] = 0
                for j in range(4):
                    keys[i] ^= self.mix_tables[j][(value >> (8 * j)) & 255]
        state = [value ^ keys[4 * rounds[0] + i] for i, value in enumerate(struct.unpack("<4I", data))]
        for round_index in rounds[1:-1]:
            new = []
            for i in range(4):
                value = keys[4 * round_index + i]
                for j in range(4):
                    value ^= tables[j][(state[(i + direction * j) % 4] >> (8 * j)) & 255]
                new.append(value)
            state = new
        result = []
        for i in range(4):
            value = sum(sbox[(state[(i + direction * j) % 4] >> (8 * j)) & 255] << (8 * j) for j in range(4))
            result.append(value ^ keys[4 * rounds[-1] + i])
        return struct.pack("<4I", *result)

    def page(self, key, page, data, decrypt=False):
        """按 0x6f7630/0x6f79b0 独立初始化 CBC，返回不增减长度的整页。"""
        values = material(key, page)
        words = self.schedule(values["page_key"])
        previous = values["iv"]
        output = bytearray()
        for offset in range(0, len(data), 16):
            block = data[offset:offset + 16]
            if decrypt:
                decoded = self.block(words, block, decrypt=True)
                output.extend(a ^ b for a, b in zip(decoded, previous))
                previous = block
            else:
                previous = self.block(words, bytes(a ^ b for a, b in zip(block, previous)))
                output.extend(previous)
        return bytes(output)


def crypt(key, page, data, decrypt=False):
    """用 OpenSSL 的标准 AES-CBC 处理合成整页；命令失败时抛出 CalledProcessError。"""
    values = material(key, page)
    command = ["openssl", "enc", "-aes-128-cbc", "-nopad", "-nosalt",
               "-K", values["page_key"].hex(), "-iv", values["iv"].hex()]
    if decrypt:
        command.append("-d")
    return subprocess.run(command, input=data, capture_output=True, check=True).stdout


class Reference:
    """连接固定版合成参考库；路径由调用者明确提供，ABI 错误立即抛出异常。"""

    def __init__(self, path):
        """加载已授权的参考库并声明所用 ABI；加载失败抛出 OSError。"""
        self.lib = ct.CDLL(str(path))
        signatures = {
            "nr_ref_key": (None, [ct.c_void_p, ct.c_int, ct.c_void_p]),
            "nr_ref_material": (None, [ct.c_void_p, ct.c_int, ct.c_void_p, ct.c_void_p]),
            "nr_ref_page": (ct.c_int, [ct.c_void_p, ct.c_int, ct.c_int, ct.c_void_p, ct.c_int]),
            "sqlite3_libversion": (ct.c_char_p, []),
            "sqlite3mc_version": (ct.c_char_p, []),
            "sqlite3_open_v2": (ct.c_int, [ct.c_char_p, ct.POINTER(ct.c_void_p), ct.c_int, ct.c_char_p]),
            "sqlite3_close": (ct.c_int, [ct.c_void_p]),
            "sqlite3_errmsg": (ct.c_char_p, [ct.c_void_p]),
            "sqlite3mc_cipher_index": (ct.c_int, [ct.c_char_p]),
            "sqlite3mc_config": (ct.c_int, [ct.c_void_p, ct.c_char_p, ct.c_int]),
            "sqlite3mc_config_cipher": (ct.c_int, [ct.c_void_p, ct.c_char_p, ct.c_char_p, ct.c_int]),
            "sqlite3_key": (ct.c_int, [ct.c_void_p, ct.c_void_p, ct.c_int]),
            "sqlite3_db_readonly": (ct.c_int, [ct.c_void_p, ct.c_char_p]),
            "sqlite3_prepare_v2": (ct.c_int, [ct.c_void_p, ct.c_char_p, ct.c_int, ct.POINTER(ct.c_void_p), ct.c_void_p]),
            "sqlite3_step": (ct.c_int, [ct.c_void_p]),
            "sqlite3_finalize": (ct.c_int, [ct.c_void_p]),
            "sqlite3_column_count": (ct.c_int, [ct.c_void_p]),
            "sqlite3_column_type": (ct.c_int, [ct.c_void_p, ct.c_int]),
            "sqlite3_column_text": (ct.c_char_p, [ct.c_void_p, ct.c_int]),
        }
        for name, (result, arguments) in signatures.items():
            function = getattr(self.lib, name)
            function.restype = result
            function.argtypes = arguments

    def key(self, password):
        """返回参考源码对合成口令的基础密钥；不打开文件。"""
        output = ct.create_string_buffer(16)
        self.lib.nr_ref_key(password, len(password), output)
        return output.raw

    def materials(self, key, page):
        """返回参考源码生成的页密钥和 IV；不打开文件。"""
        pagekey = ct.create_string_buffer(16)
        iv = ct.create_string_buffer(16)
        self.lib.nr_ref_material(key, page, pagekey, iv)
        return pagekey.raw, iv.raw

    def page(self, key, page, data, encrypt):
        """原位调用参考页算法并返回结果；非零 SQLite 状态抛出 AssertionError。"""
        output = ct.create_string_buffer(data, len(data))
        assert self.lib.nr_ref_page(key, page, encrypt, output, len(data)) == 0
        return output.raw

    def open_fixture(self, path, password, size):
        """只读打开本次合成 fixture；边文件存在时先拒绝，配置或密钥失败抛出异常。"""
        for suffix in ("-wal", "-shm", "-journal"):
            if pathlib.Path(str(path) + suffix).exists():
                raise ValueError("unsupported sidecar: " + suffix)
        connection = ct.c_void_p()
        status = self.lib.sqlite3_open_v2(str(path).encode(), ct.byref(connection), 1, None)
        if status:
            error = self.lib.sqlite3_errmsg(connection).decode()
            self.lib.sqlite3_close(connection)
            raise RuntimeError(error)
        try:
            cipher = self.lib.sqlite3mc_cipher_index(b"aes128cbc")
            assert cipher > 0
            assert self.lib.sqlite3mc_config(connection, b"cipher", cipher) == cipher
            for name, value in ((b"legacy", 1), (b"legacy_page_size", size)):
                assert self.lib.sqlite3mc_config_cipher(connection, b"aes128cbc", name, value) == value
            assert self.lib.sqlite3_key(connection, password, len(password)) == 0
            assert self.lib.sqlite3_db_readonly(connection, b"main") == 1
        except BaseException:
            self.lib.sqlite3_close(connection)
            raise
        return connection

    def query(self, connection, sql):
        """对合成连接运行固定 SQL 并返回文本行；SQLite 错误抛出带错误码的 RuntimeError。"""
        statement = ct.c_void_p()
        status = self.lib.sqlite3_prepare_v2(connection, sql.encode(), -1, ct.byref(statement), None)
        if status:
            raise RuntimeError(f"SQLite {status}: " + self.lib.sqlite3_errmsg(connection).decode())
        rows = []
        try:
            while True:
                status = self.lib.sqlite3_step(statement)
                if status == 101:
                    return rows
                if status != 100:
                    raise RuntimeError(f"SQLite {status}: " + self.lib.sqlite3_errmsg(connection).decode())
                rows.append([None if self.lib.sqlite3_column_type(statement, i) == 5
                             else self.lib.sqlite3_column_text(statement, i).decode()
                             for i in range(self.lib.sqlite3_column_count(statement))])
        finally:
            self.lib.sqlite3_finalize(statement)


def hex_values(values):
    """将合成字节材料转成便于重放的 JSON 字段；返回新字典。"""
    return {name: value.hex() for name, value in values.items()}


def check_vectors(reference, sample):
    """验证派生、页号字节序和所有已确认页长，返回含完整密文的合成向量。"""
    vectors = {"commit": COMMIT, "derivation": [], "pages": []}
    passwords = [b"", b"a", PASSWORD, bytes(range(32)), bytes(range(40)),
                 b"a\x00b", "人工口令".encode("utf-8")]
    for password in passwords:
        values = derive(password)
        assert values["base_key"] == reference.key(password)
        vectors["derivation"].append({"password_hex": password.hex(), **hex_values(values)})
    assert derive(bytes(range(32)))["base_key"] == derive(bytes(range(40)))["base_key"]
    key = derive(PASSWORD)["base_key"]
    for size in PAGE_SIZES:
        for page in (1, 2, 256, 65537, 0x01020304):
            # 固定字节斜坡既覆盖不同块，也避免重复全零输入掩盖偏移错误。
            plain = bytes((i * 17 + 3) & 255 for i in range(size))
            if page == 1:
                plain = b"SQLite format 3\x00" + plain[16:]
            values = material(key, page)
            assert reference.materials(key, page) == (values["page_key"], values["iv"])
            ciphertext = sample.page(key, page, plain)
            assert ciphertext == crypt(key, page, plain)
            assert ciphertext == reference.page(key, page, plain, 1)
            assert sample.page(key, page, ciphertext, decrypt=True) == plain
            assert reference.page(key, page, ciphertext, 0) == plain
            assert crypt(key, page, ciphertext, decrypt=True) == plain
            vectors["pages"].append({"password_hex": PASSWORD.hex(), "page": page,
                                    "page_size": size, "plaintext_rule": "(i*17+3)&255; page 1 replaces bytes 0..15 with SQLite header",
                                    **hex_values(values), "ciphertext_hex": ciphertext.hex(),
                                    "plaintext_sha256": hashlib.sha256(plain).hexdigest(),
                                    "ciphertext_sha256": hashlib.sha256(ciphertext).hexdigest()})
    return vectors


def check_databases(reference, sample, root):
    """生成合成 OrgStruct 库并跨实现读取；验证只读拒写、边文件拒绝及文件哈希不变。"""
    results = []
    key = derive(PASSWORD)["base_key"]
    for size in PAGE_SIZES:
        source = root / f"plain-{size}.db"
        with sqlite3.connect(source) as connection:
            connection.execute(f"PRAGMA page_size={size}")
            connection.execute("PRAGMA journal_mode=DELETE")
            connection.execute("""CREATE TABLE OrgStruct_SYNTHETIC (
                PriKey INTEGER PRIMARY KEY AUTOINCREMENT, UpdateTime INTEGER,
                OnlineTime INTEGER, Type INTEGER, Id TEXT, Data TEXT, UNIQUE(Id))""")
            rows = [(1, 2, 1, "synthetic-target-id", "<U><Name>合成联系人 &amp; 测试</Name></U>"),
                    (1, 2, 2, "synthetic-not-user", "<U><Name>合成联系人 &amp; 测试</Name></U>")]
            rows += [(1, 2, 1, f"synthetic-{i}", f"<U><Name>填充{i:04d}-{'x' * 120}</Name></U>") for i in range(80)]
            connection.executemany("INSERT INTO OrgStruct_SYNTHETIC(UpdateTime, OnlineTime, Type, Id, Data) VALUES (?,?,?,?,?)", rows)
        plain = source.read_bytes()
        assert plain[:16] == b"SQLite format 3\x00"
        assert plain[20] == 0
        encoded_size = int.from_bytes(plain[16:18], "big")
        assert (65536 if encoded_size == 1 else encoded_size) == size
        assert len(plain) % size == 0 and len(plain) > size
        encrypted = b"".join(sample.page(key, offset // size + 1, plain[offset:offset + size])
                             for offset in range(0, len(plain), size))
        path = root / f"encrypted-{size}.db"
        path.write_bytes(encrypted)
        path.chmod(0o444)
        before = hashlib.sha256(path.read_bytes()).hexdigest()
        # CBC 的前两块不依赖页长；先恢复32字节头，再设置准确的 legacy_page_size。
        header = sample.page(key, 1, encrypted[:32], decrypt=True)
        assert header[:16] == b"SQLite format 3\x00"
        header_size = int.from_bytes(header[16:18], "big")
        recovered_size = 65536 if header_size == 1 else header_size
        assert recovered_size == size
        assert header[18:24] == bytes((1, 1, 0, 64, 32, 32))
        connection = reference.open_fixture(path, PASSWORD, recovered_size)
        try:
            schema = reference.query(connection, "SELECT name, sql FROM sqlite_master WHERE type='table' AND name GLOB 'OrgStruct_*'")
            assert len(schema) == 1 and schema[0][0] == "OrgStruct_SYNTHETIC"
            columns = reference.query(connection, 'PRAGMA table_info("OrgStruct_SYNTHETIC")')
            assert [row[1] for row in columns] == ["PriKey", "UpdateTime", "OnlineTime", "Type", "Id", "Data"]
            data = reference.query(connection, 'SELECT Id, Data FROM "OrgStruct_SYNTHETIC" WHERE Type = 1')
            matches = [identifier for identifier, xml in data
                       if ET.fromstring(xml).findtext("Name") == "合成联系人 & 测试"]
            assert matches == ["synthetic-target-id"]
            try:
                reference.query(connection, 'INSERT INTO "OrgStruct_SYNTHETIC"(Id) VALUES (\'must-not-write\')')
            except RuntimeError as error:
                write_error = str(error)
                assert "SQLite 8:" in write_error
            else:
                raise AssertionError("read-only write was accepted")
        finally:
            assert reference.lib.sqlite3_close(connection) == 0
        after = hashlib.sha256(path.read_bytes()).hexdigest()
        assert before == after
        # 空边文件也拒绝，不能凭其为空便声称支持活动库或恢复流程。
        sidecars = []
        for suffix in ("-wal", "-shm", "-journal"):
            sibling = pathlib.Path(str(path) + suffix)
            sibling.touch(exist_ok=False)
            try:
                reference.open_fixture(path, PASSWORD, size)
            except ValueError as error:
                sidecars.append(str(error))
            else:
                raise AssertionError("sidecar was accepted")
            # 只移动本测试创建的空文件，保留测试产物，不删除任何已有文件。
            sibling.rename(root / f"rejected-{size}{suffix}")
        connection = reference.open_fixture(path, b"wrong-synthetic-password", size)
        try:
            try:
                reference.query(connection, "SELECT name FROM sqlite_master")
            except RuntimeError as error:
                wrong_password_error = str(error)
            else:
                raise AssertionError("wrong password unexpectedly read schema")
        finally:
            assert reference.lib.sqlite3_close(connection) == 0
        assert hashlib.sha256(path.read_bytes()).hexdigest() == before
        results.append({"page_size": size, "pages": len(plain) // size,
                        "database_sha256_before": before, "database_sha256_after": after,
                        "schema": schema, "matched_id": matches[0], "write_rejected": write_error,
                        "sidecars_rejected": sidecars, "wrong_password_rejected": wrong_password_error,
                        "header_bootstrap_page_size": recovered_size})
    return results


def main():
    """运行合成对照并独占写入输出文件；任意不一致或输出已存在时失败。"""
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--library", type=pathlib.Path, required=True)
    parser.add_argument("--vectors", type=pathlib.Path, required=True)
    parser.add_argument("--results", type=pathlib.Path, required=True)
    parser.add_argument("--expect-vectors", type=pathlib.Path)
    args = parser.parse_args()
    reference = Reference(args.library.resolve(strict=True))
    sample = SampleAES()
    vectors = check_vectors(reference, sample)
    if args.expect_vectors:
        assert vectors == json.loads(args.expect_vectors.read_text())
    root = pathlib.Path(tempfile.mkdtemp(prefix="nr-codec-synthetic-fixtures."))
    databases = check_databases(reference, sample, root)
    results = {"commit": COMMIT, "sqlite3mc_version": reference.lib.sqlite3mc_version().decode(),
               "reference_sqlite_version": reference.lib.sqlite3_libversion().decode(),
               "fixture_sqlite_version": sqlite3.sqlite_version,
               "openssl_version": subprocess.check_output(["openssl", "version"], text=True).strip(),
               "derivation_vectors": len(vectors["derivation"]), "page_vectors": len(vectors["pages"]),
               "synthetic_fixture_directory": str(root), "databases": databases, "passed": True}
    for path, value in ((args.vectors, vectors), (args.results, results)):
        with path.open("x") as output:
            json.dump(value, output, ensure_ascii=False, indent=2)
            output.write("\n")
    print(json.dumps({"passed": True, "derivation_vectors": len(vectors["derivation"]),
                      "page_vectors": len(vectors["pages"]), "databases": len(databases)}, ensure_ascii=False))


if __name__ == "__main__":
    main()
