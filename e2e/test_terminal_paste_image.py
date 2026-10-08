"""Pasting an image into a session terminal uploads it, injects its path,
mirrors it to the headless clipboard, and reports typing activity."""
from playwright.sync_api import expect

from test_terminal_workspace import capture, open_terminal, real_terminal  # noqa: F401

PNG = bytes.fromhex(
    "89504e470d0a1a0a0000000d49484452000000010000000108060000001f15c489"
    "0000000d49444154789c6360f8cfc0f01f0005000201a5f645400000000049454e44ae426082"
)


def test_pasted_image_uploads_injects_path_mirrors_and_pings(page, real_terminal):  # noqa: F811
    t = real_terminal
    mirrors, pings, listens = [], [], []

    def clipboard_route(route):
        req = route.request
        if "/api/clipboard/listen" in req.url:
            listens.append(req.url)
            # Keep the stream open-ish without hanging: an empty SSE response.
            route.fulfill(status=200, headers={"Content-Type": "text/event-stream"}, body=": ok\n\n")
        elif "/api/clipboard/mirror" in req.url:
            mirrors.append((req.method, req.headers.get("content-type"), len(req.post_data_buffer or b""), req.url))
            route.fulfill(status=204)
        elif "/api/clipboard/active" in req.url:
            pings.append(req.url)
            route.fulfill(status=204)
        else:
            route.continue_()

    page.route("**/api/clipboard/**", clipboard_route)
    open_terminal(page, t)
    expect(page.locator("#connection")).to_have_text("Connected")

    page.keyboard.type("echo hi")
    page.wait_for_timeout(300)
    assert pings, "typing should send an activity ping"
    assert listens and "kind=web" in listens[0] and f"session={t['id']}" in listens[0]

    with page.expect_response(lambda r: r.url.endswith("/attachments") and r.request.method == "POST") as info:
        page.evaluate(
            """bytes => {
                const file = new File([new Uint8Array(bytes)], 'shot.png', {type: 'image/png'});
                const data = new DataTransfer();
                data.items.add(file);
                const target = document.querySelector('#agent-terminal textarea') || document.body;
                target.dispatchEvent(new ClipboardEvent('paste', {clipboardData: data, bubbles: true, cancelable: true}));
            }""",
            list(PNG),
        )
    path = info.value.json()["path"]
    assert path

    page.wait_for_timeout(500)
    assert mirrors, "image should be mirrored to the headless clipboard"
    method, ctype, size, url = mirrors[0]
    assert method == "PUT" and ctype == "image/png" and size == len(PNG)
    assert f"session={t['id']}" in url
    name = path.rsplit("/", 1)[-1]
    expect(page.locator("#agent-terminal .xterm-screen")).to_contain_text(name[:20], timeout=10000)
    # The path really reached the shell's input line.
    assert name[:20] in capture(t)
