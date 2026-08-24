#!/usr/bin/env python3
"""Serve dist/ the way a real static host would, so routing bugs surface locally.

Differences from `python -m http.server` that actually matter:
  * `/some/route` (no trailing slash) 301s to `/some/route/`, like S3/Netlify/Pages do.
  * A missing path serves 404.html with a 404 status instead of a directory listing.
  * blobs/ gets an immutable cache header; HTML gets no-cache.

    python serve.py            # http://127.0.0.1:4321
    python serve.py --port 8080 --root dist
"""
from __future__ import annotations

import argparse
import os
from functools import partial
from http.server import SimpleHTTPRequestHandler, ThreadingHTTPServer

IMMUTABLE = "public, max-age=31536000, immutable"


class StaticHostHandler(SimpleHTTPRequestHandler):
    def send_head(self):
        path = self.translate_path(self.path)
        # Directory without a trailing slash: redirect rather than silently rewriting,
        # otherwise every relative link on the page resolves one level too high.
        if os.path.isdir(path) and not self.path.split("?", 1)[0].endswith("/"):
            self.send_response(301)
            self.send_header("Location", self.path.split("?", 1)[0] + "/")
            self.end_headers()
            return None
        if not os.path.exists(path) and not os.path.isdir(path):
            return self.send_404()
        return super().send_head()

    def send_404(self):
        page = os.path.join(self.directory, "404.html")
        body = open(page, "rb").read() if os.path.exists(page) else b"404\n"
        self.send_response(404)
        self.send_header("Content-Type", "text/html; charset=utf-8")
        self.send_header("Content-Length", str(len(body)))
        self.end_headers()
        self.wfile.write(body)
        return None

    def end_headers(self):
        # Content-addressed blobs can never change under a given name.
        if "/blobs/" in self.path:
            self.send_header("Cache-Control", IMMUTABLE)
        elif self.path.endswith((".html", "/")):
            self.send_header("Cache-Control", "no-cache")
        super().end_headers()

    def log_message(self, fmt, *args):  # quieter than the default
        print(f"{self.command} {self.path} -> {args[1] if len(args) > 1 else '?'}")


def main() -> None:
    ap = argparse.ArgumentParser(description=__doc__)
    ap.add_argument("--root", default="dist")
    ap.add_argument("--port", type=int, default=4321)
    args = ap.parse_args()

    root = os.path.abspath(args.root)
    if not os.path.isdir(root):
        raise SystemExit(f"{root} does not exist — run `npm run build` first")

    handler = partial(StaticHostHandler, directory=root)
    with ThreadingHTTPServer(("127.0.0.1", args.port), handler) as httpd:
        print(f"serving {root} on http://127.0.0.1:{args.port} (ctrl-c to stop)")
        try:
            httpd.serve_forever()
        except KeyboardInterrupt:
            print()


if __name__ == "__main__":
    main()
