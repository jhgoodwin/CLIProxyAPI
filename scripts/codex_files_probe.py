#!/usr/bin/env python3
"""Provider-neutral Files screenshot probe. Synthetic PNG; stdlib only.

Files stores originals locally; reference-only downstream requests may become
inline image data upstream. This does not eliminate visual context/token cost.
CODEX_MODEL is the retained legacy environment name for any approved model.
"""
import argparse
import base64
import contextlib
import io
import json
import os
import secrets
import struct
import sys
import threading
import unittest
import urllib.error
import urllib.parse
import urllib.request
import zlib
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer

# Unambiguous, high-contrast 5x7 bitmap glyphs. No system fonts or metadata.
FONT = {
    'A': ['01110','10001','10001','11111','10001','10001','10001'],
    'C': ['01111','10000','10000','10000','10000','10000','01111'],
    'E': ['11111','10000','10000','11110','10000','10000','11111'],
    'F': ['11111','10000','10000','11110','10000','10000','10000'],
    'H': ['10001','10001','10001','11111','10001','10001','10001'],
    'K': ['10001','10010','10100','11000','10100','10010','10001'],
    'M': ['10001','11011','10101','10101','10001','10001','10001'],
    'N': ['10001','11001','11001','10101','10011','10011','10001'],
    'P': ['11110','10001','10001','11110','10000','10000','10000'],
    'R': ['11110','10001','10001','11110','10100','10010','10001'],
    'T': ['11111','00100','00100','00100','00100','00100','00100'],
    'W': ['10001','10001','10001','10101','10101','11011','10001'],
    'X': ['10001','10001','01010','00100','01010','10001','10001'],
    'Y': ['10001','10001','01010','00100','00100','00100','00100'],
    '2': ['01110','10001','00001','00010','00100','01000','11111'],
    '3': ['11110','00001','00001','01110','00001','00001','11110'],
    '4': ['00010','00110','01010','10010','11111','00010','00010'],
    '5': ['11111','10000','10000','11110','00001','00001','11110'],
    '6': ['01110','10000','10000','11110','10001','10001','01110'],
    '7': ['11111','00001','00010','00100','01000','01000','01000'],
    '8': ['01110','10001','10001','01110','10001','10001','01110'],
    '9': ['01110','10001','10001','01111','00001','00001','01110'],
}
PROMPT = 'Read the visual code in the image. Reply with only the code, without spaces or commentary.'
MAX_RESPONSE = 8 * 1024 * 1024


class ProbeError(Exception):
    """Messages are local strings or exact allowlisted namespace diagnostics."""


def safe_rejection(body):
    # Exact phrases only: never retain identifiers, codes, credentials or URLs.
    allowed = ('file not found', 'file does not exist', 'file not found in namespace',
               'file does not exist in namespace', 'file not found in the codex namespace',
               'file does not exist in the codex namespace')
    try:
        obj = json.loads(body)
        error = obj.get('error')
        message = error.get('message') if isinstance(error, dict) else None
        message = message or obj.get('message') or obj.get('detail')
        if isinstance(message, str) and message.lower() in allowed:
            return message
        prefix = 'files backend rejected request (HTTP '
        if isinstance(message, str) and message.startswith(prefix):
            status, separator, phrase = message[len(prefix):].partition('): ')
            if separator and len(status) == 3 and status.isascii() and status.isdigit() and phrase.lower() in allowed:
                return phrase
    except (ValueError, TypeError, AttributeError):
        pass
    return ''


def make_png():
    code = ''.join(secrets.choice(tuple(FONT)) for _ in range(6))
    scale, margin = 12, 24
    width, height = (6 * 6 - 1) * scale + 2 * margin, 7 * scale + 2 * margin
    rows = []
    for y in range(height):
        row = bytearray([0])  # PNG filter None
        for x in range(width):
            gx, gy = (x - margin) // scale, (y - margin) // scale
            ink = (0 <= gy < 7 and 0 <= gx < 35 and gx % 6 < 5
                   and FONT[code[gx // 6]][gy][gx % 6] == '1')
            row.extend((15, 23, 42) if ink else (255, 255, 255))
        rows.append(row)
    def chunk(kind, data):
        return struct.pack('>I', len(data)) + kind + data + struct.pack('>I', zlib.crc32(kind + data) & 0xffffffff)
    png = (b'\x89PNG\r\n\x1a\n' + chunk(b'IHDR', struct.pack('>IIBBBBB', width, height, 8, 2, 0, 0, 0))
           + chunk(b'IDAT', zlib.compress(b''.join(rows))) + chunk(b'IEND', b''))
    return code, png


class NoRedirect(urllib.request.HTTPRedirectHandler):
    def redirect_request(self, req, fp, code, msg, headers, newurl):
        return None


def base_url(value):
    try:
        url = urllib.parse.urlsplit(value)
        _ = url.port
        if (url.scheme not in ('http', 'https') or not url.hostname or url.username is not None
                or url.password is not None or url.query or url.fragment or url.path.rstrip('/') not in ('', '/v1')):
            raise ValueError()
    except ValueError:
        raise ProbeError('invalid PROXY_URL (use origin or /v1, without credentials/query/fragment)') from None
    return value.rstrip('/') + ('' if url.path.rstrip('/') == '/v1' else '/v1')


def diagnostic(headers):
    # Only validated known-safe scalar headers; never print arbitrary headers.
    out = {}
    if headers.get('X-CPA-Files-Stage') == 'ready':
        out['files_stage'] = 'ready'
    value = headers.get('X-CPA-Image-Bytes', '')
    if value.isascii() and value.isdigit() and len(value) <= 12:
        out['upload_original_image_bytes'] = int(value)
    for header, field in (('X-CPA-Final-JSON-Bytes', 'upstream_send_body_bytes'),
                          ('X-CPA-Final-Input-Count', 'upstream_input_count')):
        value = headers.get(header, '')
        if value.isascii() and value.isdigit() and len(value) <= 12:
            out[field] = int(value)
    value = headers.get('X-CPA-Final-Content-Types', '')
    counts = {}
    for entry in value.split(','):
        name, sep, number = entry.strip().partition(':')
        if (not sep or name not in ('input_image', 'input_text', 'other') or name in counts
                or not number.isascii() or not number.isdigit() or len(number) > 12):
            counts = {}
            break
        counts[name] = int(number)
    if counts:
        out['upstream_content_types'] = counts
    # Legacy owner diagnostics are not meaningful for locally stored attachments.
    # Do not infer provider support or routing identity from this obsolete header.
    return out


def text_from_response(obj):
    if not isinstance(obj, dict):
        raise ProbeError('invalid response object')
    if obj.get('error') or obj.get('status') in ('failed', 'incomplete', 'cancelled'):
        message = safe_rejection(json.dumps(obj).encode())
        raise ProbeError('Responses returned failure: ' + (message or 'remote details suppressed'))
    parts = []
    for item in obj.get('output', []):
        if not isinstance(item, dict):
            raise ProbeError('invalid response output')
        if item.get('type') == 'message':
            for part in item.get('content', []):
                if part.get('type') == 'output_text':
                    parts.append(part.get('text', ''))
    if not parts and isinstance(obj.get('output_text'), str):
        parts.append(obj['output_text'])
    if not parts or any(not isinstance(p, str) for p in parts):
        raise ProbeError('no textual answer in response')
    return ''.join(parts)


def decode_answer(body, content_type):
    try:
        if content_type.split(';', 1)[0].strip().lower() != 'text/event-stream':
            return text_from_response(json.loads(body))
        text = body.decode('utf-8').replace('\r\n', '\n')
        deltas, completed = [], None
        for block in text.split('\n\n'):
            event = ''
            data = []
            for line in block.split('\n'):
                if line.startswith('event:'):
                    event = line[6:].strip()
                elif line.startswith('data:'):
                    data.append(line[5:].lstrip(' '))
            raw = '\n'.join(data)
            if not raw or raw == '[DONE]':
                continue
            obj = json.loads(raw)
            kind = obj.get('type', event)
            if kind in ('error', 'response.failed', 'response.incomplete') or obj.get('error'):
                message = safe_rejection(json.dumps(obj).encode())
                if not message and isinstance(obj.get('response'), dict):
                    message = safe_rejection(json.dumps(obj['response']).encode())
                raise ProbeError('SSE returned failure: ' + (message or 'remote details suppressed'))
            if kind == 'response.output_text.delta':
                deltas.append(obj.get('delta', ''))
            if kind == 'response.completed':
                completed = obj.get('response')
        if completed is None:
            raise ProbeError('SSE ended without response.completed')
        try:
            return text_from_response(completed)
        except ProbeError:
            if completed.get('status') == 'completed' and deltas and all(isinstance(d, str) for d in deltas):
                return ''.join(deltas)
            raise
    except (ValueError, UnicodeError, TypeError, AttributeError):
        raise ProbeError('malformed JSON/SSE response (body suppressed)') from None


class Client:
    def __init__(self, url, key):
        self.url = base_url(url)
        self.key = key
        self.opener = urllib.request.build_opener(urllib.request.ProxyHandler({}), NoRedirect())

    def post(self, path, body, content_type, probe=False):
        headers = {'Authorization': 'Bearer ' + self.key, 'Content-Type': content_type}
        if probe:
            headers['X-CPA-Files-Probe'] = '1'
        try:
            req = urllib.request.Request(self.url + path, body, headers, method='POST')
            with self.opener.open(req) as response:
                data = response.read(MAX_RESPONSE + 1)
                if len(data) > MAX_RESPONSE:
                    raise ProbeError('response exceeds local size limit')
                headers = diagnostic(response.headers)
                headers['http_status'] = response.status
                return data, response.headers.get('Content-Type', ''), headers
        except urllib.error.HTTPError as exc:
            status = exc.code
            try:
                body = exc.read(65537)
                message = safe_rejection(body) if len(body) <= 65536 else ''
            except (OSError, ValueError):
                message = ''
            finally:
                exc.close()
            # Never expose arbitrary error types, reasons, bodies or headers.
            print(json.dumps({'stage': path.lstrip('/'), 'http_status': status,
                              'error_type': 'http_error', 'safe_namespace_message': message or 'suppressed'}))
            raise ProbeError('HTTP %d: %s' % (status, message or 'remote details suppressed')) from None
        except (urllib.error.URLError, OSError, ValueError, UnicodeError):
            raise ProbeError('HTTP transport/request failed (details suppressed)') from None


def payload(model, image, stream):
    return {'model': model, 'stream': stream, 'store': False, 'tools': [], 'tool_choice': 'none',
            'input': [{'role': 'user', 'content': [{'type': 'input_text', 'text': PROMPT}, image]}]}


def responses(client, model, image, stream, label):
    body = json.dumps(payload(model, image, stream), separators=(',', ':')).encode('utf-8')
    mode = 'file_id' if 'file_id' in image else 'inline_data_url'
    print(json.dumps({'stage': label, 'client_send_body_bytes': len(body), 'client_input_image_count': 1,
                      'client_image_type': mode}))
    data, kind, headers = client.post('/responses', body, 'application/json', probe=True)
    evidence = {field: headers.get(field, 'unknown') for field in
                ('upstream_send_body_bytes', 'upstream_input_count', 'upstream_content_types')}
    evidence['send_boundary_evidence'] = 'yes' if 'upstream_send_body_bytes' in headers else 'no'
    print(json.dumps({'stage': label, 'safe_response_headers': headers, **evidence}))
    return decode_answer(data, kind)


def run_probe(client, model, stream=False):
    expected, png = make_png()
    inline = {'type': 'input_image', 'image_url': 'data:image/png;base64,' + base64.b64encode(png).decode('ascii')}
    if responses(client, model, inline, stream, 'inline_control').strip() != expected:
        raise ProbeError('inline control mismatch; STOP before file upload')
    print('inline_control: match')
    boundary = 'cpa-' + secrets.token_hex(16)
    multipart = (('--' + boundary + '\r\nContent-Disposition: form-data; name="purpose"\r\n\r\nvision\r\n'
                  '--' + boundary + '\r\nContent-Disposition: form-data; name="file"; filename="probe.png"\r\n'
                  'Content-Type: image/png\r\n\r\n').encode() + png + ('\r\n--' + boundary + '--\r\n').encode())
    data, _, headers = client.post('/files', multipart, 'multipart/form-data; boundary=' + boundary)
    try:
        uploaded = json.loads(data)
        file_id = uploaded.get('id')
        if not isinstance(file_id, str) or not file_id.startswith('file-cpa-') or len(file_id) > 128:
            raise ValueError()
    except (ValueError, TypeError, AttributeError):
        raise ProbeError('invalid file upload response (body suppressed)') from None
    print(json.dumps({'stage': 'upload', 'client_png_bytes': len(png), 'safe_response_headers': headers}))
    if responses(client, model, {'type': 'input_image', 'file_id': file_id}, stream, 'reference').strip() != expected:
        raise ProbeError('file reference mismatch')
    print('reference: match; local file retained until expiry/delete; upstream input retention unknown; no cleanup attempted')


class OfflineTests(unittest.TestCase):
    def test_png(self):
        expected, png = make_png()
        self.assertEqual(len(expected), 6)
        self.assertTrue(set(expected) <= set(FONT))
        self.assertEqual(png[:8], b'\x89PNG\r\n\x1a\n')
        offset, chunks = 8, {}
        while offset < len(png):
            size = struct.unpack('>I', png[offset:offset + 4])[0]
            kind, data = png[offset + 4:offset + 8], png[offset + 8:offset + 8 + size]
            crc = struct.unpack('>I', png[offset + 8 + size:offset + 12 + size])[0]
            self.assertEqual(crc, zlib.crc32(kind + data) & 0xffffffff)
            chunks[kind] = data
            offset += size + 12
        self.assertEqual(set(chunks), {b'IHDR', b'IDAT', b'IEND'})
        w, h = struct.unpack('>II', chunks[b'IHDR'][:8])
        raw = zlib.decompress(chunks[b'IDAT'])
        self.assertEqual(len(raw), h * (1 + 3 * w))
        # Reconstruct every scaled glyph cell, not just a PNG signature check.
        for i, char in enumerate(expected):
            for y in range(7):
                for x in range(5):
                    at = (24 + y * 12 + 6) * (1 + 3 * w) + 1 + (24 + (i * 6 + x) * 12 + 6) * 3
                    self.assertEqual(raw[at:at + 3], bytes((15, 23, 42) if FONT[char][y][x] == '1' else (255, 255, 255)))

    def scenario(self, stream=False, mismatch=False, failure=None):
        from unittest.mock import patch
        expected, png = make_png()
        requests = []
        answer = {'status': 'completed', 'output': [{'type': 'message', 'content': [{'type': 'output_text', 'text': expected}]}]}
        class Fake(BaseHTTPRequestHandler):
            def log_message(self, *args):
                pass
            def do_POST(self):
                body = self.rfile.read(int(self.headers['Content-Length']))
                requests.append((self.path, body, dict(self.headers)))
                if failure == 'http':
                    self.send_response(403)
                    self.end_headers()
                    self.wfile.write(b'{"error":"SECRET https://signed.invalid/?token=SECRET"}')
                    return
                self.send_response(200)
                if self.path == '/v1/files':
                    self.send_header('Content-Type', 'application/json')
                    self.send_header('X-CPA-Files-Stage', 'ready')
                    self.send_header('X-CPA-Image-Bytes', str(len(png)))
                    self.end_headers()
                    self.wfile.write(b'{"id":"file-cpa-offline"}')
                elif stream:
                    self.send_header('Content-Type', 'text/event-stream')
                    self.end_headers()
                    event = ({'type': 'error', 'error': {'message': 'SECRET'}} if failure == 'sse'
                             else {'type': 'response.completed', 'response': answer})
                    self.wfile.write(('event: ' + event['type'] + '\r\ndata: ' + json.dumps(event) + '\r\n\r\n').encode())
                else:
                    self.send_header('Content-Type', 'application/json')
                    self.end_headers()
                    value = {'output_text': 'not the answer'} if mismatch else answer
                    self.wfile.write(json.dumps(value).encode())
        server = ThreadingHTTPServer(('127.0.0.1', 0), Fake)
        thread = threading.Thread(target=server.serve_forever, daemon=True)
        thread.start()
        output = io.StringIO()
        try:
            with patch(__name__ + '.make_png', return_value=(expected, png)), contextlib.redirect_stdout(output):
                client = Client('http://127.0.0.1:%d' % server.server_port, 'offline-key')
                if mismatch or failure:
                    with self.assertRaises(ProbeError) as caught:
                        run_probe(client, 'offline-model', stream)
                    self.assertNotIn('SECRET', str(caught.exception))
                else:
                    run_probe(client, 'offline-model', stream)
        finally:
            server.shutdown()
            server.server_close()
            thread.join()
        self.assertNotIn(expected, output.getvalue())
        self.assertNotIn('offline-key', output.getvalue())
        self.assertNotIn('SECRET', output.getvalue())
        if mismatch or failure:
            self.assertEqual(len(requests), 1)
            return
        self.assertEqual([r[0] for r in requests], ['/v1/responses', '/v1/files', '/v1/responses'])
        for index in (0, 2):
            obj = json.loads(requests[index][1])
            self.assertEqual(requests[index][2]['X-Cpa-Files-Probe'], '1')
            self.assertEqual(obj['tools'], [])
            self.assertEqual(obj['tool_choice'], 'none')
            self.assertFalse(obj['store'])
            self.assertNotIn('previous_response_id', obj)
            self.assertEqual(len(obj['input']), 1)
            self.assertNotIn(expected, obj['input'][0]['content'][0]['text'])
        ref = json.loads(requests[2][1])['input'][0]['content'][1]
        self.assertEqual(ref, {'type': 'input_image', 'file_id': 'file-cpa-offline'})
        self.assertNotIn(b'base64', requests[2][1])
        self.assertIn(b'filename="probe.png"', requests[1][1])
        self.assertIn(b'\r\n\r\nvision\r\n', requests[1][1])
        self.assertIn(png, requests[1][1])

    def test_json_primary_path(self):
        self.scenario()

    def test_sse_primary_path(self):
        self.scenario(stream=True)

    def test_control_mismatch_stops(self):
        self.scenario(mismatch=True)

    def test_http_error_private(self):
        self.scenario(failure='http')

    def test_sse_error_private(self):
        self.scenario(stream=True, failure='sse')

    def test_json_errors_and_truncated_sse(self):
        for body, kind in [(b'{"error":{"message":"SECRET"}}', 'application/json'),
                           (b'not json SECRET', 'application/json'),
                           (b'data: {"type":"response.output_text.delta","delta":"SECRET"}\n\n', 'text/event-stream')]:
            with self.assertRaises(ProbeError) as caught:
                decode_answer(body, kind)
            self.assertNotIn('SECRET', str(caught.exception))

    def test_diagnostic_allowlist(self):
        self.assertEqual(diagnostic({'X-CPA-Image-Bytes': 'SECRET', 'X-CPA-Files-Stage': 'SECRET', 'Location': 'SECRET'}), {})
        self.assertEqual(diagnostic({'X-CPA-Final-JSON-Bytes': '321', 'X-CPA-Final-Input-Count': '1',
                                     'X-CPA-Final-Content-Types': 'input_image:1,input_text:1,other:0',
                                     'X-CPA-Same-Owner': 'yes', 'X-CPA-Image-Bytes': '123'}),
                         {'upstream_send_body_bytes': 321, 'upstream_input_count': 1,
                          'upstream_content_types': {'input_image': 1, 'input_text': 1, 'other': 0},
                          'upload_original_image_bytes': 123})

    def test_local_storage_diagnostics(self):
        self.assertEqual(diagnostic({'X-CPA-Same-Owner': 'yes'}), {})
        self.assertEqual(diagnostic({'X-CPA-Files-Stage': 'ready', 'X-CPA-Image-Bytes': '123'}),
                         {'files_stage': 'ready', 'upload_original_image_bytes': 123})

    def test_sse_deltas_and_failed(self):
        body = (b'data: {"type":"response.output_text.delta","delta":"offline"}\n\n'
                b'data: {"type":"response.completed","response":{"status":"completed"}}\n\n'
                b'data: [DONE]\n\n')
        self.assertEqual(decode_answer(body, 'text/event-stream'), 'offline')
        for kind in ('response.failed', 'response.incomplete'):
            with self.assertRaises(ProbeError):
                decode_answer(('data: ' + json.dumps({'type': kind, 'response': {'error': {'message': 'SECRET'}}}) + '\n\n').encode(), 'text/event-stream')

    def test_safe_namespace_rejection(self):
        self.assertEqual(safe_rejection(b'{"error":{"message":"File not found in namespace"}}'), 'File not found in namespace')
        self.assertEqual(safe_rejection(b'{"error":{"message":"files backend rejected request (HTTP 404): File not found in namespace"}}'), 'File not found in namespace')
        for message in ('File not found in namespace SECRET', 'File file-secret123 not found in namespace',
                        'File not found in namespace https://blob?sig=SECRET', 'File not found in account namespace'):
            self.assertEqual(safe_rejection(json.dumps({'error': {'message': message}}).encode()), '')

    def test_url_validation(self):
        self.assertEqual(base_url('http://127.0.0.1:9000'), 'http://127.0.0.1:9000/v1')
        self.assertEqual(base_url('https://example.invalid/v1/'), 'https://example.invalid/v1')
        for value in ('https://user:SECRET@example.invalid', 'https://example.invalid/?token=SECRET',
                      'file:///SECRET', 'https://example.invalid/other', 'https://example.invalid/#SECRET'):
            with self.assertRaises(ProbeError) as caught:
                base_url(value)
            self.assertNotIn('SECRET', str(caught.exception))


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('--stream', action='store_true', help='request SSE for both inference calls')
    parser.add_argument('--self-test', action='store_true', help='offline fake HTTP + PNG tests; reads no environment')
    args = parser.parse_args()
    if args.self_test:
        suite = unittest.defaultTestLoader.loadTestsFromTestCase(OfflineTests)
        return 0 if unittest.TextTestRunner(verbosity=2).run(suite).wasSuccessful() else 1
    try:
        values = {name: os.environ.get(name, '') for name in ('PROXY_URL', 'PROXY_API_KEY', 'CODEX_MODEL')}
        if not all(values.values()):
            raise ProbeError('set PROXY_URL, PROXY_API_KEY and CODEX_MODEL after explicit approval')
        run_probe(Client(values['PROXY_URL'], values['PROXY_API_KEY']), values['CODEX_MODEL'], args.stream)
        return 0
    except ProbeError as exc:
        print('probe: ' + str(exc), file=sys.stderr)
        return 1
    except KeyboardInterrupt:
        print('probe: interrupted; remote state unknown', file=sys.stderr)
        return 1
    except Exception:
        print('probe: unexpected local failure (details suppressed)', file=sys.stderr)
        return 1


if __name__ == '__main__':
    sys.exit(main())
