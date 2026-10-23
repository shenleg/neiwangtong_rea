"""下载固定公开提交并在独立目录构建合成测试参考库；不读取客户端或数据库。"""
import argparse
import hashlib
import json
import pathlib
import subprocess
import tarfile
import urllib.request

COMMIT = "f29b06b424e556cf0ad8b19646b30dfc3e3e36d8"
ARCHIVE_SHA256 = "dfbfb8651ff8b1d6fe14ee928ea002faf351d1ef37bb9a452ba1be87e23c1fe4"
FLAGS = ["-O2", "-shared", "-fPIC", "-Wl,-z,defs", "-DCODEC_TYPE=CODEC_TYPE_AES128",
         "-DSQLITE3MC_OMIT_AES_HARDWARE_SUPPORT=1", "-DHAVE_CIPHER_AES_256_CBC=0",
         # 参考版本的随机数生成也使用 ChaCha20，因此不能去掉其实现。
         "-DHAVE_CIPHER_CHACHA20=1", "-DHAVE_CIPHER_SQLCIPHER=0", "-DHAVE_CIPHER_RC4=0",
         "-DHAVE_CIPHER_ASCON128=0", "-DHAVE_CIPHER_AEGIS=0"]


def digest(path):
    """返回指定构建输入的 SHA-256；文件读取失败时抛出 OSError。"""
    return hashlib.sha256(path.read_bytes()).hexdigest()


def main():
    """创建全新工作目录并构建参考库；下载、校验或编译失败时直接报错。"""
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--work-dir", type=pathlib.Path, required=True,
                        help="尚不存在的绝对目录；下载、源码和构建产物分开保存")
    args = parser.parse_args()
    work = args.work_dir
    if not work.is_absolute():
        parser.error("--work-dir must be absolute")
    work.mkdir()
    download, source, build = [work / name for name in ("download", "source", "build")]
    for directory in (download, source, build):
        directory.mkdir()
    url = f"https://codeload.github.com/utelle/SQLite3MultipleCiphers/tar.gz/{COMMIT}"
    archive = download / "source.tar.gz"
    with urllib.request.urlopen(url, timeout=120) as response, archive.open("xb") as output:
        output.write(response.read())
    if digest(archive) != ARCHIVE_SHA256:
        raise ValueError("fixed reference archive SHA-256 mismatch")
    with tarfile.open(archive) as package:
        selected = []
        for member in package.getmembers():
            path = pathlib.PurePosixPath(member.name)
            if path.is_absolute() or ".." in path.parts:
                raise ValueError("unsafe reference archive path")
            if len(path.parts) > 1 and path.parts[1] == "src":
                if not (member.isfile() or member.isdir()):
                    raise ValueError("non-regular reference source member")
                selected.append(member)
        package.extractall(source, members=selected, filter="data")
    src = source / f"SQLite3MultipleCiphers-{COMMIT}" / "src"
    probe = pathlib.Path(__file__).resolve().parent / "reference_probe.c"
    library = build / "libnr_reference.so"
    command = ["gcc", *FLAGS, "-I", str(src), str(probe), "-o", str(library), "-lpthread", "-ldl", "-lm"]
    with (build / "compiler.stdout").open("xb") as stdout, (build / "compiler.stderr").open("xb") as stderr:
        subprocess.run(command, cwd=build, stdout=stdout, stderr=stderr, check=True)
    manifest = {"commit": COMMIT, "url": url, "archive_sha256": digest(archive),
                "gcc_version": subprocess.check_output(["gcc", "--version"], text=True).splitlines()[0],
                "command": command, "working_directory": str(build),
                "source_sha256": {str(path.relative_to(src)): digest(path) for path in sorted(src.rglob("*")) if path.is_file()},
                "probe_sha256": digest(probe), "library_sha256": digest(library)}
    with (build / "build-manifest.json").open("x") as output:
        json.dump(manifest, output, indent=2)
        output.write("\n")
    print(library)


if __name__ == "__main__":
    main()
