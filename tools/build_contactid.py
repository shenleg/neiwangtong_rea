"""下载并构建固定版联系人只读codec，下载、源码与构建目录分开；不读取客户端或真实数据库。"""

import argparse
import hashlib
import json
from pathlib import Path, PurePosixPath
import platform
import subprocess
import tarfile
import urllib.request

COMMIT = "f29b06b424e556cf0ad8b19646b30dfc3e3e36d8"
ARCHIVE_SHA256 = "dfbfb8651ff8b1d6fe14ee928ea002faf351d1ef37bb9a452ba1be87e23c1fe4"


def digest(path):
    """计算指定构建输入或产物的SHA-256；返回十六进制摘要，读取失败抛OSError。"""
    with path.open("rb") as source:
        return hashlib.file_digest(source, "sha256").hexdigest()


def build(work: Path, compiler: Path) -> Path:
    """在全新work中下载校验源码并用compiler编译，返回build目录；下载、校验或编译失败直接抛异常。"""
    if not work.is_absolute() or not compiler.is_absolute() or not compiler.is_file():
        raise ValueError("work和compiler必须是绝对路径，compiler必须存在")
    version_arg = "version" if compiler.stem.lower() == "zig" else "--version"
    compiler_version = subprocess.check_output([str(compiler), version_arg], text=True).splitlines()[0]
    work.mkdir()
    download, source, output = (work / part for part in ("download", "source", "build"))
    for directory in (download, source, output):
        directory.mkdir()
    archive = download / "reference.tar.gz"
    url = f"https://codeload.github.com/utelle/SQLite3MultipleCiphers/tar.gz/{COMMIT}"
    with urllib.request.urlopen(url, timeout=120) as response, archive.open("xb") as target:
        while block := response.read(1024 * 1024):
            target.write(block)
    if digest(archive) != ARCHIVE_SHA256:
        raise ValueError("固定版本参考源码归档SHA-256不符")
    with tarfile.open(archive) as package:
        selected = []
        for member in package.getmembers():
            path = PurePosixPath(member.name)
            if path.is_absolute() or ".." in path.parts:
                raise ValueError("参考源码归档包含非法路径")
            if len(path.parts) > 1 and path.parts[1] == "src":
                if not (member.isfile() or member.isdir()):
                    raise ValueError("参考源码目录包含非普通文件")
                selected.append(member)
        package.extractall(source, members=selected, filter="data")
    src = source / f"SQLite3MultipleCiphers-{COMMIT}" / "src"
    wrapper = Path(__file__).resolve().with_name("contactid_codec.c")
    windows = platform.system() == "Windows"
    library = output / ("contactid_codec.dll" if windows else "libcontactid_codec.so")
    command = [str(compiler)]
    if compiler.stem.lower() == "zig":
        command += ["cc"]
        if windows:
            command += ["-target", "x86_64-windows-gnu"]
    flags = ["-O2", "-shared", "-DCODEC_TYPE=CODEC_TYPE_AES128",
             "-DSQLITE3MC_OMIT_AES_HARDWARE_SUPPORT=1", "-DHAVE_CIPHER_AES_256_CBC=0",
             # 参考库随机数实现依赖ChaCha20；读取模式仍固定aes128cbc。
             "-DHAVE_CIPHER_CHACHA20=1", "-DHAVE_CIPHER_SQLCIPHER=0", "-DHAVE_CIPHER_RC4=0",
             "-DHAVE_CIPHER_ASCON128=0", "-DHAVE_CIPHER_AEGIS=0",
             "-DSQLITE_TEMP_STORE=3", "-DSQLITE_OMIT_LOAD_EXTENSION=1"]
    if windows:
        flags += ["-DSQLITE_API=__declspec(dllexport)", "-DSQLITE3MC_API=__declspec(dllexport)",
                  "-Wl,--export-all-symbols"]
        links = ["-ladvapi32"]
    else:
        flags += ["-fPIC", "-Wl,-z,defs"]
        links = ["-lpthread", "-ldl", "-lm"]
    command += [*flags, "-I", str(src), str(wrapper), "-o", str(library), *links]
    with (output / "compiler.stdout").open("xb") as stdout, (output / "compiler.stderr").open("xb") as stderr:
        subprocess.run(command, cwd=output, stdout=stdout, stderr=stderr, check=True)
    manifest = {"commit": COMMIT, "archive_sha256": digest(archive), "wrapper_sha256": digest(wrapper),
                "library_sha256": digest(library), "library_name": library.name,
                "command": command,
                "compiler_version": compiler_version}
    with (output / "build-manifest.json").open("x", encoding="utf-8") as target:
        json.dump(manifest, target, ensure_ascii=False, indent=2)
        target.write("\n")
    return output


def main():
    """解析显式构建目录和编译器并输出产物目录；参数错误退出，构建错误不吞掉。"""
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--work-dir", type=Path, required=True, help="尚不存在的绝对构建目录")
    parser.add_argument("--cc", type=Path, required=True, help="可信C编译器或zig可执行文件的绝对路径")
    args = parser.parse_args()
    print(build(args.work_dir, args.cc))


if __name__ == "__main__":
    main()
