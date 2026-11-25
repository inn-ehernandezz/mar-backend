"""Private HTTP adapter for the document extraction worker."""

import csv
import hashlib
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer
import json
import logging
import os
from pathlib import Path
import re
from tempfile import TemporaryDirectory
from threading import BoundedSemaphore
from zipfile import BadZipFile

from documents import FORMATS, Rejected, extract

MAX_BYTES = int(os.getenv("MAR_MEDIA_MAX_BYTES", str(100 << 20)))
SLOTS = BoundedSemaphore(int(os.getenv("MAR_MEDIA_MAX_CONCURRENT", "1")))
LOGGER = logging.getLogger("mar-media")


class Handler(BaseHTTPRequestHandler):
    def setup(self):
        super().setup()
        self.connection.settimeout(45)

    def do_GET(self):
        if self.path == "/health":
            self.reply(200, {"status": "ok"})
        else:
            self.reply(404, {"code": "not_found"})

    def do_POST(self):
        if self.path != "/extract":
            self.reply(404, {"code": "not_found"})
            return
        suffix = FORMATS.get(self.headers.get_content_type())
        digest = self.headers.get("X-Content-SHA256", "")
        length = self.headers.get("Content-Length", "")
        if not suffix or not re.fullmatch("[a-f0-9]{64}", digest) or not re.fullmatch(r"[0-9]{1,12}", length):
            self.reply(422, {"code": "invalid_document_request"})
            return
        size = int(length)
        if size > MAX_BYTES:
            self.reply(413, {"code": "document_too_large"})
            return
        if not SLOTS.acquire(blocking=False):
            self.reply(503, {"code": "extractor_busy"})
            return
        try:
            with TemporaryDirectory(prefix="mar-document-") as tmp:
                path = Path(tmp) / ("document" + suffix)
                self.receive(path, size, digest)
                result = extract(path)
            self.reply(200, {"fragments": result})
        except (BrokenPipeError, ConnectionResetError):
            LOGGER.info("El cliente cerró la conexión")
        except Rejected as exc:
            self.reply(422, {"code": str(exc)})
        except (BadZipFile, csv.Error, UnicodeError):
            self.reply(422, {"code": "invalid_document_format"})
        except Exception:
            LOGGER.exception("Fallo interno de extracción")
            self.reply(503, {"code": "extractor_unavailable"})
        finally:
            SLOTS.release()

    def receive(self, path, remaining, expected):
        digest = hashlib.sha256()
        with path.open("wb") as output:
            while remaining:
                data = self.rfile.read(min(remaining, 64 << 10))
                if not data:
                    raise Rejected("truncated_document")
                digest.update(data)
                output.write(data)
                remaining -= len(data)
        if digest.hexdigest() != expected:
            raise Rejected("document_integrity_error")

    def reply(self, status, data):
        encoded = json.dumps(data, ensure_ascii=False, separators=(",", ":")).encode()
        try:
            self.send_response(status)
            self.send_header("Content-Type", "application/json")
            self.send_header("Content-Length", str(len(encoded)))
            self.end_headers()
            self.wfile.write(encoded)
        except (BrokenPipeError, ConnectionResetError):
            LOGGER.info("El cliente cerró la conexión antes de recibir la respuesta")


if __name__ == "__main__":
    logging.basicConfig(level=logging.INFO)
    with ThreadingHTTPServer(("0.0.0.0", 8090), Handler) as server:
        server.serve_forever()
