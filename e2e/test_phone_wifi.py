"""Connect your phone on the private runtime: one click lets phones on this
Wi-Fi in, shows the QR for a one-time pairing link with the warning, and Stop
turns it off. Only the runtime `lectern up` starts can open a Wi-Fi listener
(POST/DELETE /api/phone/wifi, cmd/lectern/localruntime/phone.go, tested for
real in cmd/lectern TestPhoneOnTheSameWiFi); here the address list and that
switch are routed in their real shape, since the isolated suite has no Wi-Fi
address. The pairing code itself is minted by the real server."""
import json
import re

import pytest
from playwright.sync_api import expect

from conftest import DESKTOP, PHONE
from test_light_mode_sweep import Sweep, light, nav

LAN = "http://192.168.1.20:41429"


@pytest.mark.parametrize("theme", ["light", "dark"])
@pytest.mark.parametrize("page", [DESKTOP, PHONE], indirect=True, ids=["desk", "phone"])
def test_phone_on_this_wifi_in_one_click(page, server, theme):
    state = {"open": False}
    calls = []

    def addresses(route):
        lan = {"kind": "lan", "url": LAN, "available": True} if state["open"] else \
            {"kind": "lan", "url": "http://192.168.1.20:9110", "available": False, "reason": "loopback_only"}
        body = {"listening": "127.0.0.1:9110", "loopback_only": True, "can_enable_wifi": True,
                "wifi_open": state["open"], "options": [
                    {"kind": "tailnet", "available": False, "reason": "tailscale_off"}, lan,
                    {"kind": "relay", "available": False, "reason": "relay_not_set_up"}]}
        route.fulfill(status=200, content_type="application/json", body=json.dumps(body))

    def wifi(route):
        method = route.request.method
        calls.append(method)
        state["open"] = method == "POST"
        body = {"url": LAN} if state["open"] else {"open": False}
        route.fulfill(status=200, content_type="application/json", body=json.dumps(body))

    page.route("**/api/phone/addresses", addresses)
    page.route("**/api/phone/wifi", wifi)
    light(page, theme)
    page.goto(server + "/#sessions")
    nav(page, "targets")
    page.locator('[data-settings="basics"]').click()
    page.locator("#basics-phone").click()
    wizard = page.locator("#phone-wizard")
    expect(wizard).to_be_visible()
    lan = wizard.locator('.phone-choice[data-kind="lan"]')
    expect(lan).to_contain_text("only listens on this computer right now")
    expect(wizard).not_to_contain_text("lectern serve")
    label = "phone" if page.viewport_size["width"] < 600 else "desk"
    sweep = Sweep(page, label + "-phone-wifi", theme)
    sweep.check("closed", root="#phone-wizard")

    wizard.locator("#phone-lan-open").click()
    qr = wizard.get_by_test_id("phone-qr")
    expect(qr).to_have_attribute("data-url", re.compile("^" + re.escape(LAN) + "/pair#code=."))
    expect(wizard.locator("#phone-lan-warning")).to_contain_text("not encrypted")
    expect(lan).to_contain_text(LAN)
    assert calls.count("POST") == 1, calls
    sweep.check("open", root="#phone-wizard")

    wizard.locator("#phone-lan-stop").click()
    expect(wizard.locator("#phone-lan-open")).to_be_visible()
    expect(qr).to_have_count(0)
    assert "DELETE" in calls, calls
    sweep.done()
