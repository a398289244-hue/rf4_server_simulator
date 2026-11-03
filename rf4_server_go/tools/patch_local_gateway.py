"""把 RF4 客户端的本地 HTTP 网关固定到 127.1:19214。

127.1 与 127.0.0.1 等价，但字符串更短，可以在不改变 GameAssembly.dll
长度和后续数据偏移的前提下带上端口。脚本只接受已做过本地 IP 补丁的文件，
并严格要求命中两处，避免误改未知版本。
"""

from pathlib import Path
import sys


OLD = b"http://127.0.0.1" + b"\0" * 4
INTERMEDIATE = b"http://127.1:19214" + b"\0" * 2
# 必须刚好占满原常量的 20 字节；托管字符串里的 NUL 也会算字符，不能拿它补齐。
NEW = b"http://127.0.1:19214"


def main() -> int:
    if len(sys.argv) != 2:
        print("用法: python patch_local_gateway.py <GameAssembly.dll>")
        return 2

    path = Path(sys.argv[1])
    data = path.read_bytes()
    old = OLD if data.count(OLD) else INTERMEDIATE
    hits = data.count(old)
    if hits != 2:
        raise SystemExit(f"拒绝修改：预期命中 2 处，实际 {hits} 处")
    patched = data.replace(old, NEW)
    if len(patched) != len(data):
        raise SystemExit("拒绝修改：补丁改变了文件长度")
    path.write_bytes(patched)
    print(f"已修改 {hits} 处，文件长度保持 {len(patched)} 字节")
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
