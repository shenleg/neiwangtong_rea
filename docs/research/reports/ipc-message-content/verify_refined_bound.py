"""核算已恢复 IPC 路径的更紧正文上界；只做离线算术，不调用客户端或识别真实 URL。"""

import json


BUDGET = 261950
ROOT_OVERHEAD = 501
TEXT_OVERHEAD = 446
LINK_OVERHEAD = 328


def max_shape_overhead(units: int, dots: int, link_cost: int) -> tuple[int, int, int]:
    """按正文单元数、点号数和每链接固定成本求最大结构开销，返回开销、普通片段数和链接数。

    参数由调用方保证 units 为正、dots 非负；不解析 URL，不访问外部状态，不主动抛出异常。
    """
    max_links = min(dots, units // 4)
    # min(L+1, U-4L) 在交点两侧分别线性增减，整数最大值只需检查相邻两点。
    left = min(max_links, (units - 1) // 5)
    candidates = {left, min(left + 1, max_links)}
    return max(
        (TEXT_OVERHEAD * min(links + 1, units - 4 * links) + link_cost * links,
         min(links + 1, units - 4 * links), links)
        for links in candidates
    )


def body_metrics(body: str) -> tuple[int, int, int, int]:
    """计算正文的 UTF-8 字节数、UTF-16 单元数、值转义成本和点号数。

    空正文、NUL 或字面四字符换行串触发 ValueError；非法代理项触发 UnicodeEncodeError。
    本函数保留实际换行及所有空白，不提前对传给客户端的正文作转义。
    """
    encoded = body.encode("utf-8")
    if not body or "\x00" in body or r"\r\n" in body:
        raise ValueError("正文须非空、无NUL且不包含会被客户端改写的字面四字符换行串")
    units = 0
    content_cost = 0
    special = {'"': 7, "\\": 2, "'": 6, " ": 6, "&": 5, "<": 4, ">": 4}
    for char in body:
        width = 2 if ord(char) > 0xFFFF else 1
        units += width
        if char in special:
            content_cost += special[char]
        elif char in "\b\t\n\f\r":
            content_cost += 2
        elif ord(char) < 0x20:
            content_cost += 6
        else:
            content_cost += width
    return len(encoded), units, content_cost, body.count(".")


def body_bound(body: str) -> dict[str, int | bool]:
    """返回正文的度量、保守序列化上界及容量判断，不表示实际调用或送达。

    正文校验异常由 body_metrics 原样传出；不施加另外的产品字节限额。
    """
    byte_count, units, content_cost, dots = body_metrics(body)
    overhead, texts, links = max_shape_overhead(units, dots, LINK_OVERHEAD)
    bound = content_cost + overhead + ROOT_OVERHEAD
    return {
        "utf8_bytes": byte_count,
        "utf16_units": units,
        "literal_dots": dots,
        "content_cost": content_cost,
        "worst_case_text_fragments": texts,
        "worst_case_link_fragments": links,
        "escaped_json_upper_bound": bound,
        "fits_budget": bound <= BUDGET,
    }


def uniform_bound(units: int) -> int:
    """返回指定正 UTF-16 长度的统一预算上界；使用每链接至少四个低膨胀字符的证明。

    不接收实际正文，假设点号数量不构成额外限制；参数须为正，不主动抛出异常。
    """
    # 四个 host 单元各最多需要1而非7，因此每链接从粗内容估计中至少扣除24。
    overhead, _, _ = max_shape_overhead(units, units, LINK_OVERHEAD - 24)
    return 7 * units + overhead + ROOT_OVERHEAD


def main() -> None:
    """穷举验证常数时间求最大值的方法，复核示例与统一限额；不符则抛出 AssertionError。"""
    optimization_checks = 0
    for units in range(1, 257):
        for dots in range(units + 1):
            for link_cost in (LINK_OVERHEAD, LINK_OVERHEAD - 24):
                brute = max(
                    TEXT_OVERHEAD * min(links + 1, units - 4 * links) + link_cost * links
                    for links in range(min(dots, units // 4) + 1)
                )
                assert max_shape_overhead(units, dots, link_cost)[0] == brute
                optimization_checks += 1

    assert all(uniform_bound(units) <= BUDGET for units in range(1, 1666))
    assert uniform_bound(1665) == 261906
    assert uniform_bound(1666) == 262359

    examples = {
        "chinese_500": "中" * 500,
        "chinese_5000": "中" * 5000,
        "emoji_500": "😀" * 500,
        "quotes_16kib": '"' * 16384,
        "short_urls_333": "a.aa " * 333,
        "short_urls_334": "a.aa " * 334,
    }
    results = {name: body_bound(body) for name, body in examples.items()}
    assert results["chinese_500"]["escaped_json_upper_bound"] == 1447
    assert results["chinese_5000"]["escaped_json_upper_bound"] == 5947
    assert results["emoji_500"]["escaped_json_upper_bound"] == 1947
    assert results["quotes_16kib"]["escaped_json_upper_bound"] == 115635
    assert results["short_urls_333"]["escaped_json_upper_bound"] == 261573
    assert results["short_urls_334"]["escaped_json_upper_bound"] == 262357

    # 交叉核对值转义计数；标准库仅验证字符替换，不模拟客户端 JSON 排版或分片。
    weights = {"&": 5, '"': 6, "<": 4, ">": 4, "'": 6, " ": 6}
    scalars_checked = 0
    for codepoint in range(1, 0x110000):
        if 0xD800 <= codepoint <= 0xDFFF:
            continue
        char = chr(codepoint)
        encoded_value = json.dumps(char, ensure_ascii=False)[1:-1]
        expected = sum(weights.get(c, 2 if ord(c) > 0xFFFF else 1) for c in encoded_value)
        assert body_metrics(char)[2] == expected
        scalars_checked += 1

    print(json.dumps({
        "budget": BUDGET,
        "integer_optimization_checks": optimization_checks,
        "unicode_scalars_checked": scalars_checked,
        "uniform_limit_utf16_units": 1665,
        "uniform_bound_at_limit": uniform_bound(1665),
        "uniform_bound_at_next_unit": uniform_bound(1666),
        "examples": results,
    }, ensure_ascii=False, indent=2))


if __name__ == "__main__":
    main()
