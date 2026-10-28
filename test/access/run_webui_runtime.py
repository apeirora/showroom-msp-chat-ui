"""Isolated local Docker test. No host ports, live identity or persistent volumes."""
import os
from pathlib import Path
import secrets
import subprocess
import tempfile
import time

ROOT = Path(__file__).resolve().parents[2]
IMAGE = 'ghcr.io/open-webui/open-webui@sha256:096c8f3d3132016210ca87b0b4dff01d2d66d9fe4fee088fae13fc30f9ac54f3'
name = 'chatui-792-test-' + secrets.token_hex(5)
browser_python = os.environ.get('CHATUI_BROWSER_PYTHON')
with tempfile.TemporaryDirectory(prefix='chatui-792-') as temporary:
    temp = Path(temporary)
    env = temp / 'environment'
    env.write_text('\n'.join([
        'WEBUI_SECRET_KEY=' + secrets.token_urlsafe(32), 'WEBUI_AUTH=true',
        'ENABLE_WEBSOCKET_SUPPORT=' + os.environ.get('ENABLE_WEBSOCKET_SUPPORT', 'true'),
        'ENABLE_LOGIN_FORM=false', 'ENABLE_INITIAL_ADMIN_SIGNUP=false', 'ENABLE_SIGNUP=false',
        'ENABLE_API_KEYS=false', 'DEFAULT_USER_ROLE=user', 'JWT_EXPIRES_IN=1h',
        'WEBUI_AUTH_TRUSTED_EMAIL_HEADER=X-Chatui-Email', 'WEBUI_AUTH_TRUSTED_ROLE_HEADER=X-Chatui-Role',
        'ENABLE_VERSION_UPDATE_CHECK=false', 'OFFLINE_MODE=true', 'HF_HUB_OFFLINE=1',
        'HOME=/tmp', 'HF_HOME=/tmp/huggingface', 'ENABLE_PERSISTENT_CONFIG=false',
        'ENABLE_OLLAMA_API=false', 'ENABLE_OPENAI_API=' + ('true' if browser_python else 'false'),
        'OPENAI_API_BASE_URLS=http://127.0.0.1:8084/v1', 'OPENAI_API_KEYS=synthetic-fixture-only',
        'DEFAULT_MODELS=fixture-model', 'RAG_EMBEDDING_ENGINE=openai',
        'BYPASS_MODEL_ACCESS_CONTROL=true', 'ENABLE_DIRECT_CONNECTIONS=false',
        'USER_PERMISSIONS_WORKSPACE_MODELS_ACCESS=false',
        'USER_PERMISSIONS_WORKSPACE_TOOLS_ACCESS=false',
    ]))
    env.chmod(0o600)
    network = ['--network=none']
    if browser_python:
        browser_fixture = temp / 'browser'
        browser_fixture.mkdir(mode=0o755)  # readable even when CI host UID differs from container UID
        subprocess.run(['openssl', 'req', '-x509', '-newkey', 'rsa:2048', '-nodes',
            '-keyout', str(browser_fixture / 'tls.key'), '-out', str(browser_fixture / 'tls.crt'), '-days', '1',
            '-subj', '/CN=localhost'], check=True, stdout=subprocess.DEVNULL, stderr=subprocess.DEVNULL)
        (browser_fixture / 'tls.key').chmod(0o644)  # disposable, self-signed fixture key only
        network = ['-p', '127.0.0.1::8080', '-v', str(browser_fixture) + ':/fixture:ro',
                   '-v', str(ROOT / 'test/access') + ':/fixture-tests:ro']
    try:
        subprocess.run(['docker', 'run', '-d', '--name', name, *network, '--user=1000:1000',
            '--cap-drop=ALL', '--security-opt=no-new-privileges', '--env-file', str(env),
            '--tmpfs=/tmp:uid=1000,gid=1000',
            # Match disposable pod storage without leaving container-owned files
            # on runners whose host UID differs from the application's UID.
            '--tmpfs=/app/backend/data:uid=1000,gid=1000,mode=0700',
            '-v', str(ROOT / 'internal/webuiguard') + ':/etc/chat-ui-guard:ro',
            '--entrypoint=python', IMAGE, '-m', 'uvicorn', 'chatui_guard:app',
            '--app-dir=/etc/chat-ui-guard', '--host=127.0.0.1', '--port=8081', '--workers=1'],
            check=True, stdout=subprocess.DEVNULL)
        for _ in range(90):
            probe = subprocess.run(['docker', 'exec', name, 'python', '-c',
                'import urllib.request; urllib.request.urlopen("http://127.0.0.1:8081/health", timeout=1)'],
                stdout=subprocess.DEVNULL, stderr=subprocess.DEVNULL)
            if probe.returncode == 0:
                break
            state = subprocess.check_output(['docker', 'inspect', '--format={{.State.Running}}', name], text=True).strip()
            if state != 'true':
                raise RuntimeError('WebUI container exited during startup')
            time.sleep(2)
        else:
            raise RuntimeError('WebUI startup timed out')
        subprocess.run(['docker', 'exec', '-i', name, 'python', '-'],
                       input=(ROOT / 'test/access/webui_runtime.py').read_text(), text=True, check=True, timeout=120)
        if browser_python:
            subprocess.run(['docker', 'exec', '-d', name, 'python', '/fixture-tests/browser_proxy.py'], check=True)
            port = subprocess.check_output(['docker', 'port', name, '8080'], text=True).strip().rsplit(':', 1)[-1]
            time.sleep(1)
            subprocess.run([browser_python, str(ROOT / 'test/access/browser_runtime.py'),
                            'https://127.0.0.1:' + port], check=True, timeout=180)
    except Exception:
        # These isolated logs contain no real user data or credentials.
        logs = subprocess.check_output(['docker', 'logs', '--tail=50', name], stderr=subprocess.STDOUT, text=True)
        print(logs.replace(env.read_text().splitlines()[0].split('=', 1)[1], '[redacted]'))
        raise
    finally:
        subprocess.run(['docker', 'rm', '-f', name], stdout=subprocess.DEVNULL, stderr=subprocess.DEVNULL)
