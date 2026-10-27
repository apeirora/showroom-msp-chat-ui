"""Identity binding for the release-pinned Open WebUI application.

The outer proxy authenticates the browser and checks its exact KCP instance.
This wrapper binds *every* native application credential and socket event to
that authenticated identity. It is deliberately specific to the pinned image.
"""
import functools
from http.cookies import SimpleCookie
from urllib.parse import parse_qs

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
                return False
        else:
            # Preserve disconnect cleanup, including pre-authentication failures.
            if name != "disconnect":
                session = await sockets.sio.get_session(sid)
                if not await matches(session.get("token"), email):
                    await sockets.sio.disconnect(sid)
                    return False
            for payload in args[1:]:
                if isinstance(payload, dict) and "auth" in payload:
                    auth = payload["auth"]
                    if not isinstance(auth, dict) or not await matches(auth.get("token"), email):
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
                    "headers": [(b"cache-control", b"no-store")]})
        await send({"type": "http.response.body", "body": b"Sign in required"})


async def app(scope, receive, send):
    if scope["type"] not in ("http", "websocket"):
        return await application(scope, receive, send)
    # The health port reaches only this fixed probe, never arbitrary paths.
    if scope.get("path") == "/health" and scope.get("method") == "GET":
        return await application(scope, receive, send)
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
        signin = scope.get("path", "").rstrip("/") in ("/api/v1/auths/signin", "", "/auth")
        headers = []
        for key, value in scope.get("headers", []):
            if key.lower() == b"authorization":
                if signin:
                    continue
                kind, _, token = value.decode("ascii").partition(" ")
                if kind.lower() != "bearer" or not await matches(token, email):
                    return await deny(scope, send)
            if key.lower() == b"cookie":
                cookies = SimpleCookie()
                cookies.load(value.decode("ascii"))
                if "token" in cookies:
                    if signin:
                        del cookies["token"]
                        value = "; ".join(m.OutputString() for m in cookies.values()).encode("ascii")
                    elif not await matches(cookies["token"].value, email):
                        return await deny(scope, send)
            headers.append((key, value))
        scope = {**scope, "headers": headers}
    except Exception:
        # Parsing/verification failures never reach optional-auth application paths.
        return await deny(scope, send)
    return await application(scope, receive, send)
