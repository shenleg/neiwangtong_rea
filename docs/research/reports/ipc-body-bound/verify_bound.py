"""复核固定 IPC JSON 形状的计数与正文上界；不运行客户端或复现 URL 识别器。"""

import json


BUDGET = 261950  # 调用方给定的共同 u(E(J)) 预算，不含外层 XML。
BODY_LIMIT = 512  # 项目输入限额，不是厂商最大值。
PER_BYTE = 7  # 双引号变成 JSON 反斜杠和 &quot;，为最大值。
PER_FRAGMENT = 450  # 普通/链接固定开销 446/328，统一向上取整。
METADATA = 1024  # 默认根对象含字体的开销 501，预留到 1024。

# 只计数已由指令证明的固定形状；末片段也计一个逗号，使上界不依赖位置。
TEXT_FRAME = '\n      {\n         "txt" : {\n            "t" : "normal",\n            "v" : ""\n         }\n      },'
LINK_FRAME = '\n      {\n         "lnk" : {\n            "v" : ""\n         }\n      },'
ROOT_FRAME = '''{
   "app" : "shiyeline",
   "dt" : [
   ],
   "ft" : {
      "s" : "10"
   },
   "id" : "ffffffffffffffffffffffffffffffff",
   "type" : "0",
   "ver" : "6.0"
}
'''


def escaped_units(text: str) -> int:
    """计算既有六项外层替换后的 UTF-16 长度；非法代理项触发编码异常。"""
    for source, replacement in (
        ("&", "&amp;"),
        ('"', "&quot;"),
        ("<", "&lt;"),
        (">", "&gt;"),
        ("'", "&apos;"),
        (" ", "&nbsp;"),
    ):
        text = text.replace(source, replacement)
    return len(text.encode("utf-16le")) // 2


def frame_counts(text: str) -> dict[str, int]:
    """返回固定形状的空格、换行及转义后长度；非法代理项触发编码异常。"""
    return {
        "ascii_spaces": text.count(" "),
        "lf": text.count("\n"),
        "escaped_units": escaped_units(text),
    }


def main() -> None:
    """复核固定开销、字符分类和限额算术，打印结果；断言不符即失败。"""
    text = frame_counts(TEXT_FRAME)
    link = frame_counts(LINK_FRAME)
    root = frame_counts(ROOT_FRAME)
    assert text == {"ascii_spaces": 60, "lf": 6, "escaped_units": 446}
    assert link == {"ascii_spaces": 46, "lf": 5, "escaped_units": 328}
    assert root == {"ascii_spaces": 44, "lf": 11, "escaped_units": 501}
    assert text["escaped_units"] <= PER_FRAGMENT
    assert link["escaped_units"] <= PER_FRAGMENT
    assert root["escaped_units"] <= METADATA

    # 标准库仅复核已有 JSON 字符转义规则，不用有限样例替代片段数量的归纳证明。
    cases = {
        "plain": "ASCII text",
        "quotes": '"' * BODY_LIMIT,
        "specials": "<>&'\\ ]]>" * 32,
        "controls": "".join(chr(i) for i in range(1, 32)),
        "chinese": "中文正文",
        "emoji": "😀🚀",
        "short_urls": "a.cn " * 100,
        "literal_crlf": r"\r\n" * 100,
    }
    for value in cases.values():
        encoded_value = json.dumps(value, ensure_ascii=False)[1:-1]
        assert escaped_units(encoded_value) <= PER_BYTE * len(value.encode("utf-8"))

    coefficient = PER_BYTE + PER_FRAGMENT
    uniform = coefficient * BODY_LIMIT + METADATA
    assert coefficient == 457
    assert uniform == 235008
    assert uniform <= BUDGET
    print(json.dumps({
        "text_frame": text,
        "link_frame": link,
        "root_frame": root,
        "per_input_byte": PER_BYTE,
        "per_fragment_bound": PER_FRAGMENT,
        "metadata_bound": METADATA,
        "uniform_coefficient": coefficient,
        "body_limit_utf8_bytes": BODY_LIMIT,
        "escaped_json_bound_at_limit": uniform,
        "budget": BUDGET,
        "remaining_units": BUDGET - uniform,
        "synthetic_character_checks": len(cases),
    }, ensure_ascii=False, indent=2))


if __name__ == "__main__":
    main()
