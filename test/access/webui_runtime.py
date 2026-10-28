"""Run inside the pinned image, against the managed guard on port 8081."""
import asyncio
import json
import os
import urllib.error
import urllib.request

import aiohttp
import socketio

BASE = 'http://127.0.0.1:8081'


def request(path, user='alice', token=None, method='GET', body=None, cookie=None):
    headers = {'X-Chatui-Email': user + '@chatui.invalid', 'X-Chatui-Role': 'user'} if user else {}
    if token:
        headers['Authorization'] = 'Bearer ' + token
    if cookie:
        headers['Cookie'] = cookie
    if body is not None:
        headers['Content-Type'] = 'application/json'
    req = urllib.request.Request(BASE + path, headers=headers, method=method,
                                 data=json.dumps(body).encode() if body is not None else None)
    try:
        with urllib.request.urlopen(req, timeout=10) as response:
            return response.status, response.read()
    except urllib.error.HTTPError as error:
        return error.code, error.read()


def signin(user, old_token=None):
    status, body = request('/api/v1/auths/signin', user, old_token, 'POST', {'email': '', 'password': ''})
    assert status == 200, ('signin', user, status)
    result = json.loads(body)
    assert result['role'] == 'user', 'first user became administrator'
    return result['token']


async def sockets(alice, bob):
    async with aiohttp.ClientSession() as session:
        for path in ('/api/v1/audio/realtime', '/api/v1/terminals/server/api/terminals/session', '/ws/other'):
            try:
                async with session.ws_connect(BASE + path, headers={
                    'X-Chatui-Email': 'alice@chatui.invalid', 'X-Chatui-Role': 'user'}):
                    raise AssertionError('unbound native WebSocket accepted: ' + path)
            except aiohttp.WSServerHandshakeError as error:
                assert error.status == 403, (path, error.status)
    print('PASS native terminal/realtime and unknown WebSocket routes denied before upgrade')
    # Use both frontend-supported transports and forbid peer credentials in
    # connect, later user-join and a stolen Engine.IO polling session.
    for transport in (('websocket',) if os.environ.get('ENABLE_WEBSOCKET_SUPPORT') != 'false' else ('polling',)):
        headers = {'X-Chatui-Email': 'alice@chatui.invalid', 'X-Chatui-Role': 'user'}
        http_session = aiohttp.ClientSession(headers=headers)
        client = socketio.AsyncClient(http_session=http_session)
        await client.connect(BASE, headers=headers, auth={'token': alice},
                             socketio_path='/ws/socket.io', transports=[transport])
        assert client.connected
        sid = client.get_sid()
        response = await client.call('user-join', {'auth': {'token': bob}}, timeout=5)
        assert response is False, 'peer token replaced socket identity'
        async with aiohttp.ClientSession() as session:
            url = BASE + '/ws/socket.io/?EIO=4&transport=polling&sid=' + client.eio.sid
            async with session.get(url, headers={'X-Chatui-Email': 'bob@chatui.invalid', 'X-Chatui-Role': 'user'}) as reply:
                assert reply.status == 401, 'peer hijacked Engine.IO session'
        assert client.get_sid() == sid
        await client.disconnect()
        await http_session.close()
        peer_http = aiohttp.ClientSession(headers=headers)
        peer = socketio.AsyncClient(http_session=peer_http)
        try:
            # Before native signin, the real frontend needs an unjoined socket.
            # A stale/peer connect credential must never grant an application
            # session, room membership or event access on that transport.
            await peer.connect(BASE, headers=headers, auth={'token': bob},
                               socketio_path='/ws/socket.io', transports=[transport])
            assert await peer.call('join-channels', timeout=5) is False
            assert await peer.call('user-join', {'auth': {'token': bob}}, timeout=5) is False
            joined = await peer.call('user-join', {'auth': {'token': alice}}, timeout=5)
            assert joined['id'] == json.loads(request('/api/v1/auths/', token=alice)[1])['id']
            assert await peer.call('user-join', {'auth': {'token': bob}}, timeout=5) is False
        finally:
            await peer.disconnect()
            await peer_http.close()
    print('PASS socket identity binding on ' + transport)


assert request('/api/config', user=None)[0] == 401
# Native signup is disabled in the actual app as well as in the outer gate.
assert request('/api/v1/auths/signup', method='POST', body={
    'name': 'Attacker', 'email': 'victim@chatui.invalid', 'password': 'Test-password-only-!234'})[0] == 403
alice = signin('alice')
bob = signin('bob')
assert request('/api/v1/auths/', token=alice)[0] == 200
assert request('/api/v1/auths/', user='bob', token=alice)[0] == 401
assert request('/api/config', user='bob', token=alice)[0] == 401  # optional/no-auth endpoint
assert request('/api/config', cookie='broken; token=' + bob)[0] == 401
status, body = request('/api/version', user='bob', token=alice)
assert status == 200 and set(json.loads(body)) == {'version', 'deployment_id'}
assert request('/api/config', token='sk-synthetic')[0] == 401
assert request('/api/v1/auths/api_key', token=alice, method='POST')[0] == 403
assert request('/api/v1/auths/admin/config', token=alice)[0] in (401, 403)
signin('bob', old_token=alice)  # shared-browser recovery replaces stale native JWT
print('PASS first user ordinary role, signup disabled, HTTP cross-user denial and account switch')
asyncio.run(sockets(alice, bob))
# Listen sockets must be loopback-only (proc is available with network=none).
with open('/proc/net/tcp') as handle:
    bindings = [line.split()[1] for line in handle.read().splitlines()[1:] if line.split()[3] == '0A']
assert '0100007F:1F91' in bindings and '00000000:1F91' not in bindings, bindings
print('PASS WebUI listener restricted to 127.0.0.1:8081')
