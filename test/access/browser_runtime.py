"""Actual pinned frontend bootstrap and same-browser account-switch regression."""
import os
import sys
import time

from playwright.sync_api import sync_playwright

url = sys.argv[1]
with sync_playwright() as p:
    options = {'headless': True}
    if os.environ.get('CHATUI_CHROMIUM'):
        options['executable_path'] = os.environ['CHATUI_CHROMIUM']
    browser = p.chromium.launch(**options)
    context = browser.new_context(ignore_https_errors=True)
    page = context.new_page()
    errors = []
    connections = []
    page.on('pageerror', lambda error: errors.append(str(error)))
    def frame(data):
        if isinstance(data, str) and any(part.startswith('40{') for part in data.split('\x1e')):
            connections.append(True)

    page.on('websocket', lambda ws: ws.on('framereceived', frame))

    def poll_finished(request):
        if '/ws/socket.io/' in request.url and 'transport=polling' in request.url and request.method == 'GET':
            response = request.response()
            if response and response.status == 200:
                frame(response.text())

    page.on('requestfinished', poll_finished)
    page.goto(url, wait_until='domcontentloaded')

    def signed_in(expected):
        deadline = time.monotonic() + 60
        while not page.evaluate("""async expected => {
            if (!localStorage.token) return false;
            const r = await fetch('/api/v1/auths/', {
                headers: {Authorization: 'Bearer ' + localStorage.token}});
            return r.ok && (await r.json()).email === expected;
        }""", expected + '@chatui.invalid'):
            if time.monotonic() >= deadline:
                raise AssertionError(('frontend signin timed out', expected, page.url,
                                      page.locator('body').inner_text()[:1000], errors))
            page.wait_for_timeout(200)
        assert '/error' not in page.url, 'frontend bootstrap failed'

    signed_in('browser-alice')
    deadline = time.monotonic() + 5
    while not connections and time.monotonic() < deadline:
        page.wait_for_timeout(100)
    assert connections, 'first login left the frontend socket disconnected'
    print('PASS browser first login and socket connection through trusted-header frontend')
    alice = page.evaluate('localStorage.token')
    assert any(c['name'] == 'token' for c in context.cookies())
    # Simulate a different successful organization login. Preserve both the old
    # application cookie and localStorage, as a real shared browser does.
    context.request.get(url + '/_fixture/switch/browser-bob')
    connections.clear()
    page.goto(url, wait_until='domcontentloaded')
    signed_in('browser-bob')
    deadline = time.monotonic() + 5
    while not connections and time.monotonic() < deadline:
        page.wait_for_timeout(100)
    assert connections, 'account switch left the frontend socket disconnected'
    assert page.evaluate('localStorage.token') != alice
    models = page.evaluate("""async () => {
        const r = await fetch('/api/models', {headers: {Authorization: 'Bearer ' + localStorage.token}});
        const j = await r.json(); return {status: r.status, ids: j.data?.map(m => m.id)};
    }""")
    assert models == {'status': 200, 'ids': ['fixture-model']}, models
    page.get_by_text('fixture-model', exact=True).first.wait_for()
    page.locator('#chat-input').fill('Run the local fixture')
    page.locator('#chat-input').press('Enter')
    try:
        page.get_by_text('Fixture reply', exact=True).wait_for(timeout=30000)
    except Exception:
        print('chat UI after send', page.locator('body').inner_text()[:2000], flush=True)
        raise
    print('PASS browser chat displays a streamed response from the local fixture model')
    denied = page.evaluate("""async token => (await fetch('/api/config', {
        headers: {Authorization: 'Bearer ' + token}})).status""", alice)
    assert denied == 401, 'old user token reached an optional-auth endpoint'
    chat_id = page.url.split('/c/')[-1]
    assert chat_id != page.url, 'chat was not persisted'
    context.request.get(url + '/_fixture/switch/browser-alice')
    page.goto(url, wait_until='domcontentloaded')
    signed_in('browser-alice')
    denied = page.evaluate("""async id => (await fetch('/api/v1/chats/' + id, {
        headers: {Authorization: 'Bearer ' + localStorage.token}})).status""", chat_id)
    assert denied in (401, 403, 404), 'one user read another user\'s stored chat'
    assert not errors, errors
    print('PASS browser account switch, peer-token denial and private chat isolation')
    browser.close()
