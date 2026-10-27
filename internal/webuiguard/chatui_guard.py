"""Identity binding for the release-pinned Open WebUI application.

The outer proxy authenticates the browser and checks its exact KCP instance.
This wrapper binds *every* native application credential and socket event to
that authenticated identity. It is deliberately specific to the pinned image.
"""
import functools
from urllib.parse import parse_qs
from starlette.requests import cookie_parser

from open_webui.main import app as application
from open_webui.socket import main as sockets
from open_webui.utils.auth import get_verified_user_by_token

EMAIL = b"x-chatui-email"
ROLE = b"x-chatui-role"


def identity(headers):
    values = [v.decode("ascii", "strict") for k, v in headers if k.lower() == EMAIL]
    roles = [v for k, v in headers if k.lower() == ROLE]
    if len(values) != 1 or roles != [b"user"] or not values[0].endswith("@chatui.invalid"):
        return None
    return values[0]


async def matches(token, email):
    if not isinstance(token, str) or not token or token.startswith("sk-"):
        return False
    user = await get_verified_user_by_token(token, getattr(application.state, "redis", None))
    return bool(user and user.email == email and user.role == "user")


def guard_event(name, original):
    @functools.wraps(original)
    async def guarded(*args):
        # Socket.IO catch-all handlers receive the event name before sid.
        sid = args[1] if name == "*" else args[0]
        environ = args[1] if name == "connect" else sockets.sio.get_environ(sid)
        email = identity((environ or {}).get("asgi.scope", {}).get("headers", []))
        if not email:
            return False
        if name == "connect":
            auth = args[2] if len(args) > 2 else None
            if not isinstance(auth, dict) or not await matches(auth.get("token"), email):
                # The pinned frontend opens its socket before trusted signin.
                # Permit only an unjoined transport under the outer verified
                # identity; discard stale/peer credentials rather than passing
                # them to upstream. No application event can run until a valid
                # user-join supplies this identity's newly issued native token.
                return await original(sid, environ, None)
        else:
            # Preserve disconnect cleanup, including pre-authentication failures.
            if name not in ("disconnect", "user-join"):
                session = await sockets.sio.get_session(sid)
                if not session.get("token"):
                    return False
                if not await matches(session.get("token"), email):
                    await sockets.sio.disconnect(sid)
                    return False
            for payload in args[1:]:
                if isinstance(payload, dict) and "auth" in payload:
                    auth = payload["auth"]
                    if not isinstance(auth, dict) or not await matches(auth.get("token"), email):
                        return False
            if name == "user-join" and (len(args) < 2 or not isinstance(args[1], dict)
                                         or not await matches((args[1].get("auth") or {}).get("token"), email)):
                return False
        return await original(*args)
    return guarded


# Fail at startup if a dependency changes the registered handler contract.
sockets.sio.always_connect = False
handlers = sockets.sio.handlers.get("/", {})
if not {"connect", "disconnect", "user-join", "join-channels", "join-note"}.issubset(handlers):
    raise RuntimeError("Pinned WebUI socket handler contract changed")
for event, handler in list(handlers.items()):
    handlers[event] = guard_event(event, handler)


async def deny(scope, send):
    if scope["type"] == "websocket":
        await send({"type": "websocket.close", "code": 1008})
    else:
        await send({"type": "http.response.start", "status": 401,
                    "headers": [(b"cache-control", b"no-store"),
                                (b"content-type", b"application/json")]})
        await send({"type": "http.response.body", "body": b'{"detail":"Sign in required"}'})


async def stale_credential(scope, send):
    # A navigation after an organization account switch can carry the previous
    # native cookie. Clear it before loading frontend assets/config; that app's
    # signin page then replaces the stale localStorage token. API calls still
    # fail without exposing another identity's data.
    navigation = scope["type"] == "http" and scope.get("method") == "GET" and any(
        k.lower() == b"accept" and b"text/html" in v.lower()
        for k, v in scope.get("headers", []))
    if not navigation:
        return await deny(scope, send)
    await send({"type": "http.response.start", "status": 303, "headers": [
        (b"location", b"/auth"), (b"cache-control", b"no-store"),
        (b"set-cookie", b"token=; Max-Age=0; Path=/; HttpOnly; Secure; SameSite=Lax"),
    ]})
    await send({"type": "http.response.body", "body": b""})


async def app(scope, receive, send):
    if scope["type"] not in ("http", "websocket"):
        return await application(scope, receive, send)
    # The health port reaches only this fixed probe, never arbitrary paths.
    if scope.get("path") == "/health" and scope.get("method") == "GET":
        return await application(scope, receive, send)
    if scope["type"] == "websocket" and scope.get("path", "").rstrip("/") != "/ws/socket.io":
        # Other native WebSocket handlers authenticate a first-message JWT and
        # have no trusted-header binding. They are outside this chat profile.
        return await deny(scope, send)
    try:
        email = identity(scope.get("headers", []))
        if not email:
            return await deny(scope, send)
        query = parse_qs(scope.get("query_string", b"").decode("ascii"))
        if scope.get("path", "").startswith("/ws/") and "sid" in query:
            # Engine.IO polling and transport upgrade must retain the identity
            # from their first request, even before Socket.IO connect completes.
            sid = query["sid"]
            original = sockets.sio.environ.get(sid[0]) if len(sid) == 1 else None
            if identity((original or {}).get("asgi.scope", {}).get("headers", [])) != email:
                return await deny(scope, send)
        # Trusted signin replaces stale localStorage/cookies after an account
        # switch. Remove them there, and validate both forms on all other paths.
        signin = scope.get("path", "").rstrip("/") == "/api/v1/auths/signin"
        # The socket connect callback asks for public version metadata before
        # native signin, sometimes with a stale token or literal "undefined".
        # This exact pinned endpoint has no user dependency or user data.
        public_version = scope.get("path") == "/api/version" and scope.get("method") == "GET"
        headers = []
        for key, value in scope.get("headers", []):
            if key.lower() == b"authorization":
                if signin or public_version:
                    continue
                kind, _, token = value.decode("ascii").partition(" ")
                if kind.lower() != "bearer" or not await matches(token, email):
                    return await stale_credential(scope, send)
            if key.lower() == b"cookie":
                # Match the application's parser exactly. SimpleCookie silently
                # discards malformed pairs that Starlette still accepts, allowing
                # a later token cookie to escape identity validation.
                cookies = cookie_parser(value.decode("ascii"))
                if "token" in cookies:
                    if signin or public_version:
                        continue
                    elif not await matches(cookies["token"], email):
                        return await stale_credential(scope, send)
            headers.append((key, value))
        scope = {**scope, "headers": headers}
    except Exception:
        # Parsing/verification failures never reach optional-auth application paths.
        return await deny(scope, send)
    return await application(scope, receive, send)
