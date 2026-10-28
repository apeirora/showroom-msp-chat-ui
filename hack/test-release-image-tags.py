#!/usr/bin/env python3
"""Exercise release-channel outputs without Docker, credentials or publication."""
import os
import importlib.util
import json
from pathlib import Path
import subprocess
import tempfile
import unittest


SCRIPT = Path(__file__).with_name("release-image-tags.sh")
SET_IMAGE = Path(__file__).with_name("set-release-image.py")


class ReleaseImageTags(unittest.TestCase):
    def outputs(self, tag, owner="ApeiroRA"):
        with tempfile.TemporaryDirectory() as tmp:
            output = Path(tmp) / "outputs"
            proc = subprocess.run(
                ["bash", str(SCRIPT)],
                env={**os.environ, "GITHUB_REF_NAME": tag,
                     "GITHUB_REPOSITORY_OWNER": owner, "GITHUB_OUTPUT": str(output)},
                capture_output=True, text=True, check=False,
            )
            return proc, output.read_text() if output.exists() else ""

    def test_stable_versions_publish_latest(self):
        for tag in ["v2.17.1", "2.17.1", "v3.0.0", "0.1.0"]:
            with self.subTest(tag=tag):
                proc, output = self.outputs(tag)
                self.assertEqual(proc.returncode, 0, proc.stderr)
                self.assertIn("publish_latest=true\n", output)
                self.assertIn("ghcr.io/apeirora/chat-ui-controller:latest\n", output)
                self.assertIn("version=" + tag.removeprefix("v") + "\n", output)

    def test_prereleases_never_publish_latest(self):
        for tag in ["v0.14.4-rc.1", "0.14.4-rc.1", "v2.17.1-rc.1", "2.17.1-rc.794", "v3.0.0-beta.2", "v2.18.0-dev", "v2.18.0-0"]:
            with self.subTest(tag=tag):
                proc, output = self.outputs(tag)
                self.assertEqual(proc.returncode, 0, proc.stderr)
                self.assertIn("publish_latest=false\n", output)
                self.assertNotIn(":latest", output)
                self.assertIn("ghcr.io/apeirora/chat-ui-controller:" + tag.removeprefix("v") + "\n", output)

    def test_invalid_or_unrepresentable_tags_fail_before_output(self):
        for tag in ["latest", "v2.17", "v2.17.1+build.1", "v2.17.1-", "v02.17.1", "v2.17.1-rc.01", "v2.17.1\npublish_latest=true"]:
            with self.subTest(tag=tag):
                proc, output = self.outputs(tag)
                self.assertNotEqual(proc.returncode, 0)
                self.assertEqual(output, "")


class PackagedImageVersion(unittest.TestCase):
    repository = "ghcr.io/apeirora/chat-ui-controller"
    values = """PUBLIC_HOST: localhost
image:
  repository: ghcr.io/apeirora/chat-ui-controller
  tag: '2.17.0' # operator only
  pullPolicy: IfNotPresent
  imagePullSecrets: []
portalIntegration:
  image:
    repository: nginx
    tag: '1.25'
"""

    def rewrite(self, text, version, repository=None):
        if repository is None:
            repository = self.repository
        with tempfile.TemporaryDirectory() as tmp:
            values = Path(tmp) / "values.yaml"
            values.write_text(text)
            proc = subprocess.run(
                ["python3", str(SET_IMAGE), str(values), version, "--repository", repository],
                capture_output=True, text=True, check=False,
            )
            return proc, values.read_text()

    def test_exact_root_image_updated_preserving_other_content(self):
        for version in ["2.17.1", "2.17.1-rc.1", "3.0.0-0"]:
            with self.subTest(version=version):
                proc, after = self.rewrite(self.values, version)
                self.assertEqual(proc.returncode, 0, proc.stderr)
                self.assertEqual(
                    after, self.values.replace(
                        "  repository: " + self.repository,
                        '  repository: "' + self.repository + '"',
                    ).replace("  tag: '2.17.0'", f'  tag: "{version}"'),
                )

    def test_fork_image_matches_release_tags_and_preserves_nested_image(self):
        for tag in ["v2.17.1", "v2.17.1-rc.1"]:
            with self.subTest(tag=tag):
                proc, output = ReleaseImageTags().outputs(tag, "Fork-Team")
                self.assertEqual(proc.returncode, 0, proc.stderr)
                _, marker, block = output.partition("image_tags<<RELEASE_IMAGE_TAGS\n")
                self.assertTrue(marker, "missing image_tags output")
                images, terminator, trailing = block.partition("RELEASE_IMAGE_TAGS\n")
                self.assertTrue(terminator, "unterminated image_tags output")
                self.assertEqual(trailing, "")
                expected = ["ghcr.io/fork-team/chat-ui-controller:" + tag.removeprefix("v")]
                if "-" not in tag:
                    expected.append("ghcr.io/fork-team/chat-ui-controller:latest")
                self.assertEqual(images.splitlines(), expected)
                repository, version = images.splitlines()[0].rsplit(":", 1)
                self.assertEqual(repository, "ghcr.io/fork-team/chat-ui-controller")
                proc, after = self.rewrite(self.values, version, repository)
                self.assertEqual(proc.returncode, 0, proc.stderr)
                self.assertIn('  repository: "' + repository + '"\n', after)
                self.assertIn('  tag: "' + version + '" # operator only\n', after)
                self.assertNotIn(self.repository, after)
                self.assertIn("    repository: nginx\n    tag: '1.25'\n", after)

    def test_quoted_repository_comments_are_preserved(self):
        for quote in ["'", '"']:
            with self.subTest(quote=quote):
                text = self.values.replace(
                    self.repository, quote + self.repository + quote + " # operator repository",
                )
                fork = "ghcr.io/fork-team/chat-ui-controller"
                proc, after = self.rewrite(text, "2.17.1-rc.1", fork)
                self.assertEqual(proc.returncode, 0, proc.stderr)
                self.assertIn('  repository: "' + fork + '" # operator repository\n', after)

    def test_invalid_release_repository_does_not_modify_values(self):
        for repository in [
            "", "ghcr.io/Fork-Team/chat-ui-controller", "ghcr.io/-owner/chat-ui-controller",
            "ghcr.io/owner-/chat-ui-controller", "ghcr.io/" + "a" * 40 + "/chat-ui-controller",
            "docker.io/owner/chat-ui-controller", "https://ghcr.io/owner/chat-ui-controller",
            "ghcr.io/owner/other-image", "ghcr.io/owner/chat-ui-controller:2.17.1",
            "ghcr.io/owner/chat-ui-controller@sha256:" + "a" * 64,
            "ghcr.io/owner/chat-ui-controller\nother: injected",
        ]:
            with self.subTest(repository=repository):
                proc, after = self.rewrite(self.values, "2.17.1-rc.1", repository)
                self.assertNotEqual(proc.returncode, 0)
                self.assertEqual(after, self.values)

    def test_invalid_release_version_does_not_modify_values(self):
        for version in ["v2.17.1", "2.17.1+build.1", "2.17.1-rc.01", "latest", "2.17.1\nother: injected"]:
            with self.subTest(version=version):
                proc, after = self.rewrite(self.values, version)
                self.assertNotEqual(proc.returncode, 0)
                self.assertEqual(after, self.values)

    def test_schema_drift_fails_without_modifying_values(self):
        cases = [
            self.values.replace("image:\n", "containerImage:\n", 1),
            self.values + "image:\n  tag: 2.17.0\n",
            self.values.replace("  tag: '2.17.0'", "  tag: 2.17.0\n  tag: '2.17.0'"),
            self.values.replace("  tag: '2.17.0'", "    tag: '2.17.0'"),
            self.values.replace("image:\n", "image: {}\n", 1),
            self.values.replace("image:\n", "'image':\n", 1),
            self.values.replace("  tag: '2.17.0'", "  tag: latest"),
            self.values.replace("  tag: '2.17.0'", "\ttag: '2.17.0'"),
            self.values.replace("  repository:", "  'repository':"),
            self.values.replace("  repository:", "    repository:"),
            self.values.replace("  repository: " + self.repository, "  repository: {}"),
            self.values.replace("  repository: " + self.repository, "  repository: &image " + self.repository),
            self.values.replace("  repository: " + self.repository, "  repository: *image"),
            self.values.replace("  repository: " + self.repository, "  repository: [" + self.repository + "]"),
            self.values.replace(self.repository, self.repository + "#not-a-comment"),
            self.values.replace("'2.17.0' # operator only", "2.17.0#not-a-comment"),
            self.values.replace("  repository: " + self.repository, "  repository: " + self.repository + "\n  repository: " + self.repository),
            self.values.replace("  repository: " + self.repository, "  repository: " + self.repository + "\n  'repository': " + self.repository),
        ]
        for index, text in enumerate(cases):
            with self.subTest(case=index):
                proc, after = self.rewrite(text, "2.17.1-rc.1")
                self.assertNotEqual(proc.returncode, 0)
                self.assertEqual(after, text)


class OCMInstallerImage(unittest.TestCase):
    values = '''component:
  semver: "0.14.4-rc.1"
operator:
  accessPolicy:
    proxyImage: ''
  imageRepository: ghcr.io/apeirora/chat-ui-controller
  imageTag: ""
  imagePullSecretName: ghcr-credentials
'''

    def rewrite(self, text, repository="ghcr.io/apeirora/chat-ui-controller"):
        spec = importlib.util.spec_from_file_location("release_images", SET_IMAGE)
        module = importlib.util.module_from_spec(spec)
        spec.loader.exec_module(module)
        return module.ocm_release_values(text, "0.14.4-rc.1", repository)

    def test_installer_selects_published_repository_and_version(self):
        for owner in ("apeirora", "fork-team"):
            repository = f"ghcr.io/{owner}/chat-ui-controller"
            after = self.rewrite(self.values, repository)
            self.assertIn('  imageRepository: "' + repository + '"\n', after)
            self.assertIn('  imageTag: "0.14.4-rc.1"\n', after)
            self.assertIn("    proxyImage: ''\n", after)
            self.assertIn('  semver: "0.14.4-rc.1"\n', after)

    def test_installer_ambiguous_fields_rejected(self):
        for text in (
            self.values + "operator:\n  imageTag: ''\n",
            self.values.replace('  imageTag: ""', '  imageTag: ""\n  imageTag: ""'),
            self.values.replace('  imageTag: ""', '  imageTag: ""\n  \'imageTag\': ""'),
            self.values.replace('  imageTag: ""', '  imageTag: &tag ""'),
            self.values.replace('  imageTag: ""', '  imageTag: latest'),
            self.values.replace('operator:\n', "'operator':\n"),
            self.values.replace('  imageRepository:', '    imageRepository:'),
        ):
            with self.subTest(text=text), self.assertRaises(ValueError):
                self.rewrite(text)

    def test_invalid_installer_leaves_both_files_unchanged(self):
        with tempfile.TemporaryDirectory() as tmp:
            operator, installer = Path(tmp) / "values.yaml", Path(tmp) / "ocm.yaml"
            operator.write_text(PackagedImageVersion.values)
            installer.write_text(self.values.replace('  imageTag: ""', '  imageTag: latest'))
            before = installer.read_text()
            proc = subprocess.run(["python3", str(SET_IMAGE), str(operator), "0.14.4-rc.1",
                                   "--repository", "ghcr.io/apeirora/chat-ui-controller", "--ocm-values", str(installer)],
                                  capture_output=True, text=True, check=False)
            self.assertNotEqual(proc.returncode, 0)
            self.assertEqual(operator.read_text(), PackagedImageVersion.values)
            self.assertEqual(installer.read_text(), before)

    def test_repository_owned_chart_shapes_supported(self):
        root = SCRIPT.parent.parent
        values = (root / "charts/chat-ui-operator/values.yaml").read_text()
        proc, after = PackagedImageVersion().rewrite(values, "0.14.4-rc.1")
        self.assertEqual(proc.returncode, 0, proc.stderr)
        self.assertIn('  tag: "0.14.4-rc.1"', after)
        self.rewrite((root / "charts/chat-ui-operator-ocm/values.yaml").read_text())


class ReleaseWorkflowContract(unittest.TestCase):
    def test_prerelease_and_stable_guards_are_wired(self):
        root = SCRIPT.parent.parent
        workflow = (root / ".github/workflows/build-push.yml").read_text()
        self.assertIn("run: bash hack/release-image-tags.sh", workflow)
        self.assertIn("tags: ${{ steps.vars.outputs.image_tags }}", workflow)
        self.assertNotIn(":latest", workflow)
        self.assertIn('"latest": ${PUBLISH_LATEST}', workflow)
        self.assertIn("prerelease: ${{ needs.publish-image.outputs.publish_latest != 'true' }}", workflow)
        self.assertIn("make_latest: ${{ needs.publish-image.outputs.publish_latest }}", workflow)
        self.assertLess(workflow.index("python3 hack/set-release-image.py"), workflow.index('for sub in charts/*/'))
        config = json.loads((root / "release-please-config.json").read_text())["packages"]["."]
        if config.get("release-as"):
            self.assertEqual(config["prerelease"], "-" in config["release-as"])
        self.assertIn({"type": "yaml", "path": "charts/chat-ui-operator-ocm/values.yaml", "jsonpath": "$.component.semver"}, config["extra-files"])


if __name__ == "__main__":
    unittest.main()
