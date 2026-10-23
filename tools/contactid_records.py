"""校验指定身份的联系人表，并仅按原始 XML 名称返回精确匹配的名称与 ID。"""

from xml.parsers import expat as _expat


class ContactError(Exception):
    """表示联系人读取或契约校验失败。

    参数为不含联系人 XML 或其他个人资料的错误说明；返回异常实例。
    构造本异常不另行抛出业务异常。
    """


_SCHEMA = (
    (0, "PriKey", "INTEGER", 1, 0),
    (1, "UpdateTime", "INTEGER", 0, 0),
    (2, "OnlineTime", "INTEGER", 0, 0),
    (3, "Type", "INTEGER", 0, 0),
    (4, "Id", "TEXT", 0, 0),
    (5, "Data", "TEXT", 0, 0),
)


def _quote(identifier):
    """引用已从数据库元数据核实的标识符，返回 SQL 标识符文本，不抛业务异常。"""
    return '"' + identifier.replace('"', '""') + '"'


def _rows(query, sql, parameters=()):
    """执行只读查询并迭代结果；参数传给调用方，查询或迭代失败抛出脱敏 ContactError。"""
    try:
        yield from query(sql, parameters)
    except Exception:
        # 查询实现的异常可能包含数据、路径或 SQL；不把原异常带给调用方。
        raise ContactError("联系人数据库只读查询失败。") from None


def _verified_table(query, identity):
    """核实指定身份的真实表、列和唯一索引，返回表名；不符时抛 ContactError。"""
    expected = "OrgStruct_" + identity
    tables = _rows(
        query,
        "SELECT name FROM sqlite_master "
        "WHERE type = 'table' AND name GLOB 'OrgStruct_*' ORDER BY name",
    )
    table = next((row[0] for row in tables if row[0] == expected), None)
    if table is None:
        raise ContactError("指定身份的联系人表不存在。")

    quoted = _quote(table)
    # table_xinfo 还列出生成列，避免 table_info 隐藏额外列而误判 schema 一致。
    columns = tuple(
        (row[0], row[1], row[2].upper(), row[5], row[6])
        for row in _rows(query, f"PRAGMA table_xinfo({quoted})")
    )
    if columns != _SCHEMA:
        raise ContactError("联系人表的列顺序、声明类型或主键不符。")

    for index in _rows(query, f"PRAGMA index_list({quoted})"):
        if index[2] != 1 or index[4] != 0:
            continue
        keys = [
            (row[1], row[2], row[4])
            for row in _rows(query, f"PRAGMA index_xinfo({_quote(index[1])})")
            if row[5] == 1
        ]
        # 部分、复合、表达式及非 BINARY 索引均不能证明原始 Id 的全表唯一性。
        if keys == [(4, "Id", "BINARY")]:
            return table
    raise ContactError("联系人表缺少有效的 UNIQUE(Id) 索引。")


def _xml_name(data):
    """从 XML 文本提取 U 根的唯一直接 Name；格式或安全限制不符抛 ContactError。"""
    if not isinstance(data, str):
        raise ContactError("联系人 XML 必须是文本。")
    parser = _expat.ParserCreate(namespace_separator="}")
    depth = 0
    name_count = 0
    in_name = False
    parts = []

    def start(tag, attributes):
        """校验元素层次并开始名称捕获；参数来自 Expat，无返回值，违规抛 ContactError。"""
        nonlocal depth, name_count, in_name
        if depth == 0 and tag != "U":
            raise ContactError("联系人 XML 根元素必须为 U。")
        if in_name:
            raise ContactError("联系人 Name 不允许嵌入元素。")
        if depth == 1 and tag == "Name":
            name_count += 1
            if name_count > 1:
                raise ContactError("联系人 XML 存在重复 Name。")
            in_name = True
        depth += 1

    def end(tag):
        """结束元素并停止直接 Name 捕获；参数来自 Expat，无返回值或额外异常。"""
        nonlocal depth, in_name
        depth -= 1
        if depth == 1 and tag == "Name":
            in_name = False

    def text(value):
        """仅保留 Name 的已还原文本；参数来自 Expat，无返回值或额外异常。"""
        if in_name:
            parts.append(value)

    def forbidden(*arguments):
        """拒绝 DTD 和外部实体回调；忽略解析器参数，始终抛 ContactError。"""
        raise ContactError("联系人 XML 禁止 DTD 和外部实体。")

    parser.StartElementHandler = start
    parser.EndElementHandler = end
    parser.CharacterDataHandler = text
    parser.StartDoctypeDeclHandler = forbidden
    parser.ExternalEntityRefHandler = forbidden
    parser.SetParamEntityParsing(_expat.XML_PARAM_ENTITY_PARSING_NEVER)
    try:
        parser.Parse(data, True)
    except (_expat.ExpatError, UnicodeError):
        raise ContactError("联系人 XML 格式无效。") from None
    if name_count != 1:
        raise ContactError("联系人 XML 缺少直接 Name。")
    return "".join(parts)


def find_contact(query, identity: str, name: str) -> dict:
    """在明确确认的身份表中精确匹配原始名称，不发送消息。

    query 是调用方提供的可信只读查询函数，接收 SQL 与参数元组并返回行迭代器；
    identity 是已确认的非空原始身份后缀，name 是不作 trim 或大小写转换的查询名。
    返回 status、输入 name、matching_rows、已核实 table 和全部同名 candidates，
    每个候选仅含原始 name 与 user_id；status 为 unique、not_found 或 ambiguous。
    参数、表结构、唯一索引、任一用户记录或查询失败时抛 ContactError，错误不含 XML。
    """
    if not isinstance(identity, str) or not identity or "\x00" in identity:
        raise ContactError("必须显式提供非空且不含 NUL 的身份。")
    if not isinstance(name, str):
        raise ContactError("查询名称必须是文本。")
    table = _verified_table(query, identity)
    candidates = []
    for user_id, data in _rows(
        query, f"SELECT Id, Data FROM {_quote(table)} WHERE Type = ?", (1,)
    ):
        # 所有 Type=1 行都必须有效，不能跳过坏记录后宣称名称唯一。
        if not isinstance(user_id, str) or not user_id or "\x00" in user_id:
            raise ContactError("联系人 Id 必须是非空且不含 NUL 的文本。")
        original_name = _xml_name(data)
        if original_name == name:
            candidates.append({"name": original_name, "user_id": user_id})
    count = len(candidates)
    status = "not_found" if count == 0 else "unique" if count == 1 else "ambiguous"
    return {
        "status": status,
        "name": name,
        "matching_rows": count,
        "table": table,
        "candidates": candidates,
    }
