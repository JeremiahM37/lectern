"""Minimal OpenAI-compatible server for offline CLI probing.

Answers /v1/models, /v1/chat/completions (stream and not) and /v1/responses
(stream and not) with a fixed reply and no tool calls, so a coding CLI can
complete one turn and save its session without any real provider.
"""
import json, sys, time
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer

REPLY = "PROBE-REPLY ok"
LOG = open(sys.argv[2] if len(sys.argv) > 2 else '/dev/null', 'a')

class H(BaseHTTPRequestHandler):
    protocol_version = 'HTTP/1.1'
    def log_message(self, *a): pass
    def _json(self, obj, code=200):
        b = json.dumps(obj).encode()
        self.send_response(code); self.send_header('Content-Type', 'application/json')
        self.send_header('Content-Length', str(len(b))); self.end_headers(); self.wfile.write(b)
    def _sse(self, events):
        self.send_response(200); self.send_header('Content-Type', 'text/event-stream')
        self.send_header('Cache-Control', 'no-cache'); self.send_header('Connection', 'close'); self.end_headers()
        for ev in events:
            if isinstance(ev, tuple):
                self.wfile.write(f"event: {ev[0]}\ndata: {json.dumps(ev[1])}\n\n".encode())
            else:
                self.wfile.write(f"data: {ev if isinstance(ev,str) else json.dumps(ev)}\n\n".encode())
            self.wfile.flush()
        self.close_connection = True
    def do_GET(self):
        LOG.write(f"GET {self.path}\n"); LOG.flush()
        if self.path.rstrip('/').endswith('/models'):
            return self._json({"object": "list", "data": [{"id": "m1", "object": "model", "owned_by": "probe", "created": 0}]})
        self._json({"ok": True})
    def do_POST(self):
        n = int(self.headers.get('Content-Length') or 0)
        body = json.loads(self.rfile.read(n) or b'{}')
        msgs = body.get('messages') if isinstance(body.get('messages'), list) else body.get('input') if isinstance(body.get('input'), list) else []
        texts = json.dumps(msgs)
        LOG.write(f"POST {self.path} stream={body.get('stream')} n={len(msgs)} saw_probe={'PROBE-REPLY' in texts} marker={[m for m in ('MARK-' + x for x in 'ABCDEFGHIJKLMNOPQRSTUVWXYZ') if m in texts]}\n"); LOG.flush()
        now = int(time.time())
        if self.path.rstrip('/').endswith('/chat/completions'):
            if body.get('stream'):
                base = {"id": "c1", "object": "chat.completion.chunk", "created": now, "model": body.get('model', 'm1')}
                return self._sse([
                    {**base, "choices": [{"index": 0, "delta": {"role": "assistant", "content": ""}, "finish_reason": None}]},
                    {**base, "choices": [{"index": 0, "delta": {"content": REPLY}, "finish_reason": None}]},
                    {**base, "choices": [{"index": 0, "delta": {}, "finish_reason": "stop"}], "usage": {"prompt_tokens": 5, "completion_tokens": 2, "total_tokens": 7}},
                    "[DONE]"])
            return self._json({"id": "c1", "object": "chat.completion", "created": now, "model": body.get('model', 'm1'),
                "choices": [{"index": 0, "message": {"role": "assistant", "content": REPLY}, "finish_reason": "stop"}],
                "usage": {"prompt_tokens": 5, "completion_tokens": 2, "total_tokens": 7}})
        if self.path.rstrip('/').endswith('/responses'):
            item = {"id": "msg1", "type": "message", "role": "assistant", "status": "completed",
                    "content": [{"type": "output_text", "text": REPLY, "annotations": []}]}
            resp = {"id": "resp1", "object": "response", "created_at": now, "status": "completed", "model": body.get('model','m1'),
                    "output": [item], "usage": {"input_tokens": 5, "output_tokens": 2, "total_tokens": 7}}
            if body.get('stream'):
                return self._sse([("response.created", {"type": "response.created", "response": {**resp, "status": "in_progress", "output": []}}),
                    ("response.output_item.added", {"type": "response.output_item.added", "output_index": 0, "item": {**item, "status": "in_progress", "content": []}}),
                    ("response.content_part.added", {"type": "response.content_part.added", "item_id": "msg1", "output_index": 0, "content_index": 0, "part": {"type": "output_text", "text": "", "annotations": []}}),
                    ("response.output_text.delta", {"type": "response.output_text.delta", "item_id": "msg1", "output_index": 0, "content_index": 0, "delta": REPLY}),
                    ("response.output_text.done", {"type": "response.output_text.done", "item_id": "msg1", "output_index": 0, "content_index": 0, "text": REPLY}),
                    ("response.output_item.done", {"type": "response.output_item.done", "output_index": 0, "item": item}),
                    ("response.completed", {"type": "response.completed", "response": resp})])
            return self._json(resp)
        self._json({"error": {"message": "unknown path " + self.path}}, 404)

ThreadingHTTPServer(('127.0.0.1', int(sys.argv[1])), H).serve_forever()
