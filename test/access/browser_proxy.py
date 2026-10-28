"""Disposable browser fixture: simulate the trusted identity after the KCP gate.

Only the test runner starts this proxy, on a randomly published loopback port.
It has no real credentials, external identity provider or cluster connection.
"""
import asyncio
import json
import ssl

import aiohttp
from aiohttp import web

identity = 'browser-alice'
HOP = {'host', 'connection', 'upgrade', 'content-length', 'transfer-encoding',
       'sec-websocket-key', 'sec-websocket-version', 'sec-websocket-extensions'}


async def handle(request):
    global identity
    if request.path.startswith('/_fixture/switch/'):
        candidate = request.path.rsplit('/', 1)[-1]
        if candidate not in ('browser-alice', 'browser-bob'):
            raise web.HTTPBadRequest()
        identity = candidate
        return web.Response(text='Switched fixture identity')
    headers = {k: v for k, v in request.headers.items()
               if k.lower() not in HOP and not k.lower().startswith('x-')}
    headers.update({'X-Chatui-Email': identity + '@chatui.invalid',
                    'X-Chatui-Role': 'user', 'X-Forwarded-Proto': 'https'})
    url = 'http://127.0.0.1:8081' + str(request.rel_url)
    async with aiohttp.ClientSession(auto_decompress=False) as session:
        if request.headers.get('Upgrade', '').lower() == 'websocket':
            try:
                upstream = await session.ws_connect(url, headers=headers)
            except aiohttp.WSServerHandshakeError as error:
                return web.Response(status=error.status)
            async with upstream:
                browser = web.WebSocketResponse()
                await browser.prepare(request)

                async def relay(source, target):
                    async for message in source:
                        if message.type == aiohttp.WSMsgType.TEXT:
                            await target.send_str(message.data)
                        elif message.type == aiohttp.WSMsgType.BINARY:
                            await target.send_bytes(message.data)
                    await target.close()

                await asyncio.gather(relay(browser, upstream), relay(upstream, browser))
                return browser
        async with session.request(request.method, url, data=await request.read(),
                                   headers=headers, allow_redirects=False) as upstream:
            response = web.Response(status=upstream.status, body=await upstream.read())
            for key, value in upstream.headers.items():
                if key.lower() not in HOP:
                    response.headers.add(key, value)
            return response


async def model(request):
    if request.path == '/v1/models':
        return web.json_response({'object': 'list', 'data': [
            {'id': 'fixture-model', 'object': 'model', 'owned_by': 'fixture'}]})
    data = await request.json()
    reply = {'id': 'fixture-completion', 'object': 'chat.completion', 'model': 'fixture-model',
             'choices': [{'index': 0, 'message': {'role': 'assistant', 'content': 'Fixture reply'}, 'finish_reason': 'stop'}]}
    if not data.get('stream'):
        return web.json_response(reply)
    response = web.StreamResponse(headers={'Content-Type': 'text/event-stream'})
    await response.prepare(request)
    reply['choices'] = [{'index': 0, 'delta': {'role': 'assistant', 'content': 'Fixture reply'}, 'finish_reason': None}]
    await response.write(('data: ' + json.dumps(reply) + '\n\n').encode())
    await response.write(b'data: [DONE]\n\n')
    await response.write_eof()
    return response


async def main():
    app = web.Application()
    app.router.add_route('*', '/{path:.*}', handle)
    tls = ssl.SSLContext(ssl.PROTOCOL_TLS_SERVER)
    tls.load_cert_chain('/fixture/tls.crt', '/fixture/tls.key')
    runner = web.AppRunner(app, access_log=None)
    await runner.setup()
    await web.TCPSite(runner, '0.0.0.0', 8080, ssl_context=tls).start()
    backend = web.Application()
    backend.router.add_route('*', '/v1/{path:.*}', model)
    model_runner = web.AppRunner(backend, access_log=None)
    await model_runner.setup()
    await web.TCPSite(model_runner, '127.0.0.1', 8084).start()
    await asyncio.Event().wait()


asyncio.run(main())
