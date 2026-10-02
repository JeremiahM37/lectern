"""Connect your phone on the private runtime: one click lets phones on this
Wi-Fi in, shows the QR for a one-time pairing link with the warning, and Stop
turns it off. /api/local/phone exists only on the runtime `lectern up` starts
(cmd/lectern/localruntime/phone.go, tested for real in cmd/lectern
TestPhoneOnTheSameWiFi); here it is routed in its real shape, since the
isolated suite has no Wi-Fi address."""
import json

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

    def local_phone(route):
        method = route.request.method
        calls.append(method)
        if method == "POST":
            state["open"] = True
            body = {"open": True, "url": LAN, "lan_address": "192.168.1.20",
                    "pair_url": LAN + "/pair#code=ABC123", "expires_at": 4102444800}
        elif method == "DELETE":
            state["open"] = False
            body = {"open": False, "lan_address": "192.168.1.20"}
        else:
            body = {"open": state["open"], "lan_address": "192.168.1.20", **({"url": LAN} if state["open"] else {})}
        route.fulfill(status=200, content_type="application/json", body=json.dumps(body))

    page.route("**/api/local/phone", local_phone)
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
    expect(qr).to_have_attribute("data-url", LAN + "/pair#code=ABC123")
    expect(wizard.locator("#phone-lan-warning")).to_contain_text("not encrypted")
    expect(lan).to_contain_text(LAN)
    assert calls.count("POST") == 1, calls
    sweep.check("open", root="#phone-wizard")

    wizard.locator("#phone-lan-stop").click()
    expect(wizard.locator("#phone-lan-open")).to_be_visible()
    expect(qr).to_have_count(0)
    assert "DELETE" in calls, calls
    sweep.done()
