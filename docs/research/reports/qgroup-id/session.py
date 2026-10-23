"""复用独立 REA 静态分析进程，保留原始请求和 Evidence，不执行目标程序。"""
import json
import os
import pathlib
import socket
import subprocess
import sys

ROOT = pathlib.Path(__file__).parent
ENDPOINT = ROOT / "session.sock"
REA = "/root/.npm/_npx/a1722018f28f9df2/node_modules/rea-agents/scripts/rea.mjs"


def serve():
    """串行处理本地 MCP 请求并保存证据；协议错误直接失败，关闭时释放分析器。"""
    env = dict(os.environ, REA_GHIDRA_STARTUP_TIMEOUT_MS="1200000")
    with (ROOT / "server.stderr").open("x") as log:
        proc = subprocess.Popen(["node", REA, "--mcp"], stdin=subprocess.PIPE,
                                stdout=subprocess.PIPE, stderr=log, text=True,
                                cwd="/tmp", env=env)
        serial = 0

        def rpc(method, params):
            """提交 MCP 请求并返回对应响应；服务退出时抛出错误。"""
            nonlocal serial
            serial += 1
            proc.stdin.write(json.dumps({"jsonrpc": "2.0", "id": serial,
                                         "method": method, "params": params}) + "\n")
            proc.stdin.flush()
            while True:
                line = proc.stdout.readline()
                if not line:
                    raise RuntimeError("REA exited; inspect server.stderr")
                response = json.loads(line)
                if response.get("id") == serial:
                    return response

        rpc("initialize", {"protocolVersion": "2025-03-26", "capabilities": {},
                           "clientInfo": {"name": "readonly-gateway-analysis", "version": "1"}})
        proc.stdin.write(json.dumps({"jsonrpc": "2.0", "method": "notifications/initialized"}) + "\n")
        proc.stdin.flush()
        with socket.socket(socket.AF_UNIX) as server:
            server.bind(str(ENDPOINT))
            server.listen(1)
            print("READY", ENDPOINT, flush=True)
            try:
                while True:
                    conn, _ = server.accept()
                    with conn:
                        request = json.loads(conn.makefile("r").readline())
                        stop = request.get("stop", False)
                        name = "close_binary" if stop else request["name"]
                        args = {} if stop else request.get("arguments", {})
                        response = rpc("tools/call", {"name": name, "arguments": args})
                        path = ROOT / f"{serial:04d}-{name}.json"
                        path.write_text(json.dumps({"request": request, "response": response}, ensure_ascii=False))
                        conn.sendall(json.dumps({"path": str(path), "response": response}, ensure_ascii=False).encode() + b"\n")
                        if stop:
                            break
            finally:
                proc.stdin.close()
                try:
                    proc.wait(timeout=15)
                except subprocess.TimeoutExpired:
                    proc.terminate()
                    proc.wait(timeout=15)


def query(request):
    """提交静态查询并显示规范化结果；原始响应保存于返回的证据路径。"""
    with socket.socket(socket.AF_UNIX) as client:
        client.connect(str(ENDPOINT))
        client.sendall(json.dumps(request).encode() + b"\n")
        envelope = json.loads(client.makefile("r").readline())
    result = envelope["response"].get("result", envelope["response"])
    evidence = result.get("structuredContent")
    if evidence is None:
        evidence = json.loads(result["content"][0]["text"]) if "content" in result else result
    print("REQUEST", json.dumps(request, ensure_ascii=False))
    print("SAVED", envelope["path"], "EVIDENCE", evidence.get("evidence_id"))
    value = evidence.get("normalized_result", evidence)
    print(value if isinstance(value, str) else json.dumps(value, ensure_ascii=False))
    if evidence.get("relationship_coverage"):
        print("COVERAGE", json.dumps(evidence["relationship_coverage"], ensure_ascii=False))
    if result.get("isError"):
        raise RuntimeError("REA returned an error; see saved response")


if __name__ == "__main__":
    if sys.argv[1] == "serve":
        serve()
    else:
        requests = json.loads(sys.argv[2])
        for request in requests if isinstance(requests, list) else [requests]:
            query(request)
