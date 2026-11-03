"""临时把客户端登录口 7515 原样转发到 Go 网关 19214。

主后端重启并自带 7515 监听后，可结束本脚本；它不解析也不修改业务数据。
"""

import select
import socket
import socketserver


TARGET = ("127.0.0.1", 19214)


class Handler(socketserver.BaseRequestHandler):
    def handle(self) -> None:
        upstream = socket.create_connection(TARGET, timeout=5)
        sockets = (self.request, upstream)
        try:
            while True:
                readable, _, _ = select.select(sockets, (), (), 30)
                if not readable:
                    continue
                for source in readable:
                    data = source.recv(65536)
                    if not data:
                        return
                    target = upstream if source is self.request else self.request
                    target.sendall(data)
        finally:
            upstream.close()


class Server(socketserver.ThreadingTCPServer):
    allow_reuse_address = True
    daemon_threads = True


if __name__ == "__main__":
    with Server(("127.0.0.1", 7515), Handler) as server:
        server.serve_forever()
