"""Built React worker controls offline shell without caching live API state."""
from playwright.sync_api import expect


def test_react_worker_precaches_shell_and_leaves_api_and_posts_live(browser, server):
    context = browser.new_context(service_workers='allow')
    page = context.new_page()
    errors = []
    page.on('pageerror', lambda error: errors.append(str(error)))
    page.goto(server)
    page.wait_for_function('navigator.serviceWorker.controller !== null')
    keys = page.evaluate('caches.keys()')
    assert len(keys) == 1 and keys[0].startswith('lectern-react-'), keys
    urls = page.evaluate('''async () => {
      const cache = await caches.open((await caches.keys())[0]);
      return (await cache.keys()).map(request => new URL(request.url).pathname);
    }''')
    assert '/' in urls and '/fonts.css' in urls
    assert any(url.startswith('/react/assets/') and url.endswith('.js') for url in urls)
    assert any(url.startswith('/react/assets/') and url.endswith('.css') for url in urls)
    assert not any(url.startswith('/api/') for url in urls)
    # A POST hits the actual router (404), not the Cache API or cached shell.
    result = page.evaluate("async () => {const r = await fetch('/worker-test-unhandled', {method:'POST', body:'proof'});return {status:r.status, type:r.headers.get('content-type')};}")
    assert result['status'] == 404, result
    context.set_offline(True)
    page.reload()
    expect(page.locator('.tab[data-tab="sessions"]')).to_be_visible()
    assert page.evaluate("async () => {try {await fetch('/api/health');return false;}catch{return true;}}")
    assert not errors, errors
    context.close()
