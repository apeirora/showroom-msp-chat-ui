"""Keep umbrella defaults and the user token path consistent with the child chart."""
from pathlib import Path
import unittest
import yaml

ROOT = Path(__file__).resolve().parents[2]


class ChartContracts(unittest.TestCase):
    def test_umbrella_keeps_the_child_namespace_contract(self):
        child = yaml.safe_load((ROOT / 'charts/chat-ui-sync-agent/values.yaml').read_text())
        umbrella = yaml.safe_load((ROOT / 'charts/chat-ui-msp-app/values.yaml').read_text())
        self.assertEqual(child['publishedResources']['naming'],
                         umbrella['chat-ui-sync-agent']['publishedResources']['naming'])

    def test_user_path_uses_the_token_secret_and_runtime_source_annotations(self):
        guide = (ROOT / 'docs/installation-local.md').read_text()
        self.assertNotIn('LLM_SVC=', guide)
        self.assertNotIn('CHAT_NS=$(kubectl get chatuiinstances', guide)
        self.assertIn("{.status.secretName}", guide)
        self.assertIn('name: "$TOKEN_SECRET"', guide)
        self.assertIn('syncagent.kcp.io/remote-object-namespace', guide)
        self.assertIn('syncagent.kcp.io/remote-object-name', guide)
        self.assertIn('kubectl --context msp port-forward', guide)
