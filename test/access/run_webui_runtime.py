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
with tempfile.TemporaryDirectory(prefix='chatui-792-') as temporary:
    temp = Path(temporary)
    (temp / 'data').mkdir(mode=0o777)
    (temp / 'data').chmod(0o777)  # disposable fixture only; runtime uses fsGroup
    env = temp / 'environment'
    env.write_text('\n'.join([
        'WEBUI_SECRET_KEY=' + secrets.token_urlsafe(32), 'WEBUI_AUTH=true',
        'ENABLE_WEBSOCKET_SUPPORT=' + os.environ.get('ENABLE_WEBSOCKET_SUPPORT', 'true'),
        'ENABLE_LOGIN_FORM=false', 'ENABLE_INITIAL_ADMIN_SIGNUP=false', 'ENABLE_SIGNUP=false',
        'ENABLE_API_KEYS=false', 'DEFAULT_USER_ROLE=user', 'JWT_EXPIRES_IN=5m',
        'WEBUI_AUTH_TRUSTED_EMAIL_HEADER=X-Chatui-Email', 'WEBUI_AUTH_TRUSTED_ROLE_HEADER=X-Chatui-Role',
        'ENABLE_VERSION_UPDATE_CHECK=false', 'OFFLINE_MODE=true', 'HF_HUB_OFFLINE=1',
        'HOME=/tmp', 'HF_HOME=/tmp/huggingface', 'ENABLE_PERSISTENT_CONFIG=false',
        'ENABLE_OLLAMA_API=false', 'ENABLE_OPENAI_API=false', 'RAG_EMBEDDING_ENGINE=openai',
    ]))
    env.chmod(0o600)
    try:
        subprocess.run(['docker', 'run', '-d', '--name', name, '--network=none', '--user=1000:1000',
            '--cap-drop=ALL', '--security-opt=no-new-privileges', '--env-file', str(env),
            '--tmpfs=/tmp:uid=1000,gid=1000', '-v', str(temp / 'data') + ':/app/backend/data',
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
    except Exception:
        # These isolated logs contain no real user data or credentials.
        logs = subprocess.check_output(['docker', 'logs', '--tail=50', name], stderr=subprocess.STDOUT, text=True)
        print(logs.replace(env.read_text().splitlines()[0].split('=', 1)[1], '[redacted]'))
        raise
    finally:
        subprocess.run(['docker', 'rm', '-f', name], stdout=subprocess.DEVNULL, stderr=subprocess.DEVNULL)
