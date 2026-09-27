#!/usr/bin/env python3
"""Needs aiohttp. Evaluate JS in the app's WebView over raw CDP: cdp.py '<expression>' [url-substring]
The expression may be an async arrow function; it is called and awaited."""
import asyncio, json, os, subprocess, sys
import aiohttp
ADB = [os.path.expanduser("~/android-sdk/platform-tools/adb"), "-s", os.environ.get("ANDROID_SERIAL", "emulator-5584")]
pkg = os.environ.get("PKG", "io.github.jeremiahm37.lectern.debug")

async def main():
    pid = subprocess.check_output(ADB + ["shell", "pidof", pkg], text=True).split()[0]
    subprocess.run(ADB + ["forward", "tcp:19222", f"localabstract:webview_devtools_remote_{pid}"], check=True, capture_output=True)
    want = sys.argv[2] if len(sys.argv) > 2 else ""
    async with aiohttp.ClientSession() as s:
        targets = await (await s.get("http://127.0.0.1:19222/json")).json()
        pages = [t for t in targets if t.get("type") == "page" and want in t.get("url", "")]
        if not pages:
            raise SystemExit("no page; have: " + str([t.get("url") for t in targets]))
        async with s.ws_connect(pages[0]["webSocketDebuggerUrl"], max_msg_size=0) as ws:
            expr = sys.argv[1]
            if expr.lstrip().startswith(("async", "(")):
                expr = f"({expr})()"
            await ws.send_json({"id": 1, "method": "Runtime.evaluate", "params": {"expression": expr, "awaitPromise": True, "returnByValue": True}})
            async for msg in ws:
                data = json.loads(msg.data)
                if data.get("id") == 1:
                    r = data["result"]
                    if "exceptionDetails" in r:
                        raise SystemExit("JS error: " + json.dumps(r["exceptionDetails"])[:1500])
                    print(json.dumps(r["result"].get("value"), indent=1)[:6000])
                    return
asyncio.run(main())
