#!/usr/bin/env python3
"""Tiny uiautomator helper: ui.py dump | tap <regex> [--attr text|content-desc|resource-id] | wait <regex> [secs]"""
import os, re, subprocess, sys, time
import xml.etree.ElementTree as ET
ADB = [os.path.expanduser("~/android-sdk/platform-tools/adb"), "-s", os.environ.get("ANDROID_SERIAL", "emulator-5584")]

def dump():
    for _ in range(5):
        out = subprocess.run(ADB + ["exec-out", "uiautomator", "dump", "/dev/tty"], capture_output=True, text=True).stdout
        i = out.find("<?xml")
        j = out.rfind(">")
        if i >= 0:
            try:
                return ET.fromstring(out[i:j + 1])
            except ET.ParseError:
                pass
        time.sleep(0.5)
    raise SystemExit("dump failed")

def find(pattern, attrs=("text", "content-desc", "resource-id")):
    rx = re.compile(pattern)
    for node in dump().iter("node"):
        for a in attrs:
            if rx.search(node.get(a, "")):
                return node
    return None

def center(node):
    x1, y1, x2, y2 = map(int, re.findall(r"\d+", node.get("bounds")))
    return (x1 + x2) // 2, (y1 + y2) // 2

if __name__ == "__main__":
    cmd = sys.argv[1]
    if cmd == "dump":
        for n in dump().iter("node"):
            t, d, r = n.get("text", ""), n.get("content-desc", ""), n.get("resource-id", "")
            if t or d or r:
                print(repr(t)[:80], "|", d[:60], "|", r, "|", n.get("bounds"), "|", n.get("class"))
    elif cmd in ("tap", "wait"):
        pattern = sys.argv[2]
        secs = float(sys.argv[3]) if len(sys.argv) > 3 else (0 if cmd == "tap" else 20)
        end = time.time() + max(secs, 0.1)
        node = None
        while node is None and time.time() < end:
            node = find(pattern)
            if node is None:
                time.sleep(1)
        if node is None:
            raise SystemExit(f"not found: {pattern}")
        if cmd == "tap":
            x, y = center(node)
            subprocess.run(ADB + ["shell", "input", "tap", str(x), str(y)], check=True)
        print("ok", node.get("text") or node.get("content-desc"), node.get("bounds"))
