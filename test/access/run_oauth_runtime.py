"""Extract and test the pinned OAuth executable; remove temporary artifacts."""
import os
from pathlib import Path
import subprocess
import tempfile

ROOT = Path(__file__).resolve().parents[2]
IMAGE = 'quay.io/oauth2-proxy/oauth2-proxy@sha256:8498b0d0ef0a7b29686414000a08aee467f02d0299c9ed1e006a8f33fc017916'
container = subprocess.check_output(['docker', 'create', IMAGE], text=True).strip()
try:
    with tempfile.TemporaryDirectory(prefix='chatui-oauth-') as temp:
        binary = Path(temp) / 'oauth2-proxy'
        subprocess.run(['docker', 'cp', container + ':/bin/oauth2-proxy', str(binary)], check=True)
        subprocess.run(['go', 'test', './internal/controller', '-run', 'TestPinnedOAuthRuntime', '-v', '-count=1'],
                       cwd=ROOT, env=dict(os.environ, CHATUI_TEST_OAUTH_PROXY=str(binary)), check=True)
finally:
    subprocess.run(['docker', 'rm', container], stdout=subprocess.DEVNULL, check=True)
