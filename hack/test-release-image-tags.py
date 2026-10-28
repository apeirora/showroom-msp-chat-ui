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
  repositoryUrl: ghcr.io/apeirora/ocm
  semver: "0.14.4-rc.1"
operator:
  accessPolicy:
    proxyImage: ''
  imageRepository: ghcr.io/apeirora/chat-ui-controller
  imageTag: ""
  imagePullSecretName: ghcr-credentials
'''

    def rewrite(self, text, repository="ghcr.io/apeirora/chat-ui-controller", ocm_repository=None):
        spec = importlib.util.spec_from_file_location("release_images", SET_IMAGE)
        module = importlib.util.module_from_spec(spec)
        spec.loader.exec_module(module)
        if ocm_repository is None:
            ocm_repository = "oci://" + repository.removesuffix("/chat-ui-controller") + "/ocm"
        return module.ocm_release_values(text, "0.14.4-rc.1", repository, ocm_repository)

    def rewrite_cli(self, text, ocm_repository="oci://ghcr.io/fork-team/ocm"):
        with tempfile.TemporaryDirectory() as tmp:
            operator, installer = Path(tmp) / "values.yaml", Path(tmp) / "ocm.yaml"
            operator.write_text(PackagedImageVersion.values, encoding="utf-8")
            installer.write_text(text, encoding="utf-8")
            before = operator.read_bytes(), installer.read_bytes()
            proc = subprocess.run(
                ["python3", str(SET_IMAGE), str(operator), "0.14.4-rc.1",
                 "--repository", "ghcr.io/fork-team/chat-ui-controller", "--ocm-values", str(installer),
                 "--ocm-repository", ocm_repository], capture_output=True, text=True, check=False,
            )
            return proc, before, (operator.read_bytes(), installer.read_bytes())

    def test_installer_selects_published_repository_and_version(self):
        for owner in ("apeirora", "fork-team"):
            repository = f"ghcr.io/{owner}/chat-ui-controller"
            after = self.rewrite(self.values, repository)
            self.assertIn('  imageRepository: "' + repository + '"\n', after)
            self.assertIn('  imageTag: "0.14.4-rc.1"\n', after)
            self.assertIn(f'  repositoryUrl: "ghcr.io/{owner}/ocm"\n', after)
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

    def test_cli_fork_component_source_matches_publication(self):
        proc, _, (operator, installer) = self.rewrite_cli(self.values)
        self.assertEqual(proc.returncode, 0, proc.stderr)
        self.assertIn(b'repository: "ghcr.io/fork-team/chat-ui-controller"', operator)
        self.assertIn(b'repositoryUrl: "ghcr.io/fork-team/ocm"', installer)
        self.assertNotIn(b"oci://", installer)
        self.assertNotIn(b"ghcr.io/apeirora/", installer)

    def test_ocm_source_is_explicit_not_inferred_from_image(self):
        after = self.rewrite(self.values, "ghcr.io/fork-team/chat-ui-controller", "oci://ghcr.io/publisher/ocm")
        self.assertIn('repositoryUrl: "ghcr.io/publisher/ocm"', after)
        self.assertIn('imageRepository: "ghcr.io/fork-team/chat-ui-controller"', after)

    def test_invalid_component_repositories_leave_both_files_unchanged(self):
        for repository in (
            "", "oci://", "oci://oci://ghcr.io/owner/ocm", "https://ghcr.io/owner/ocm",
            "oci://ghcr.io/Fork-Team/ocm", "ghcr.io/-owner/ocm", "ghcr.io/owner-/ocm",
            "ghcr.io/" + "a" * 40 + "/ocm", "ghcr.io/owner/charts", "ghcr.io/owner/ocm/",
            "ghcr.io/owner/ocm:v1", "ghcr.io/owner/ocm@sha256:" + "a" * 64,
            "docker.io/owner/ocm", "ghcr.io/owner/ocm?next=other", "ghcr.io/owner/ocm\nother: injected",
        ):
            with self.subTest(repository=repository):
                proc, before, after = self.rewrite_cli(self.values, repository)
                self.assertNotEqual(proc.returncode, 0)
                self.assertEqual(after, before)

    def test_component_schema_drift_leaves_both_files_unchanged(self):
        field = "  repositoryUrl: ghcr.io/apeirora/ocm"
        for text in (
            self.values.replace("component:\n", "source:\n", 1),
            self.values.replace("component:\n", "component: {}\n", 1),
            self.values.replace("component:\n", "'component':\n", 1),
            self.values + "component:\n" + field + "\n",
            self.values + "'component':\n" + field + "\n",
            self.values.replace(field, field + "\n" + field),
            self.values.replace(field, field + "\n  'repositoryUrl': ghcr.io/apeirora/ocm"),
            self.values.replace(field, '  "repositoryUrl": ghcr.io/apeirora/ocm'),
            self.values.replace(field, "    repositoryUrl: ghcr.io/apeirora/ocm"),
            self.values.replace(field, "  repositoryUrl: &repo ghcr.io/apeirora/ocm"),
            self.values.replace(field, "  repositoryUrl: *repo"),
            self.values.replace(field, "  repositoryUrl: {}"),
            self.values.replace(field, "  repositoryUrl: oci://ghcr.io/apeirora/ocm"),
            self.values.replace(field, field + "#not-a-comment"),
        ):
            with self.subTest(text=text):
                proc, before, after = self.rewrite_cli(text)
                self.assertNotEqual(proc.returncode, 0)
                self.assertEqual(after, before)

    def test_component_repository_comments_and_nested_content_preserved(self):
        for quoting in ("'", '"'):
            text = self.values.replace("  repositoryUrl: ghcr.io/apeirora/ocm",
                                       "  repositoryUrl: " + quoting + "ghcr.io/apeirora/ocm" + quoting + " # Zürich")
            after = self.rewrite(text, "ghcr.io/fork-team/chat-ui-controller")
            self.assertIn('  repositoryUrl: "ghcr.io/fork-team/ocm" # Zürich\n', after)
            self.assertIn("    proxyImage: ''\n", after)

    def test_invalid_installer_leaves_both_files_unchanged(self):
        with tempfile.TemporaryDirectory() as tmp:
            operator, installer = Path(tmp) / "values.yaml", Path(tmp) / "ocm.yaml"
            operator.write_text(PackagedImageVersion.values)
            installer.write_text(self.values.replace('  imageTag: ""', '  imageTag: latest'))
            before = installer.read_text()
            proc = subprocess.run(["python3", str(SET_IMAGE), str(operator), "0.14.4-rc.1",
                                   "--repository", "ghcr.io/apeirora/chat-ui-controller", "--ocm-values", str(installer),
                                   "--ocm-repository", "oci://ghcr.io/apeirora/ocm"],
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

    def test_cli_preserves_utf8_values_under_ascii_locale(self):
        with tempfile.TemporaryDirectory() as tmp:
            operator, installer = Path(tmp) / "values-é.yaml", Path(tmp) / "ocm-ñ.yaml"
            note = "# Zürich — café 東京\n"
            operator.write_text(note + PackagedImageVersion.values, encoding="utf-8")
            installer.write_text(note + self.values, encoding="utf-8")
            proc = subprocess.run(
                ["python3", str(SET_IMAGE), str(operator), "0.14.4-rc.1",
                 "--repository", "ghcr.io/apeirora/chat-ui-controller", "--ocm-values", str(installer),
                 "--ocm-repository", "oci://ghcr.io/apeirora/ocm"],
                env={**os.environ, "LC_ALL": "C", "PYTHONUTF8": "0", "PYTHONCOERCECLOCALE": "0"},
                capture_output=True, text=True, check=False,
            )
            self.assertEqual(proc.returncode, 0, proc.stderr)
            self.assertTrue(operator.read_bytes().startswith(note.encode("utf-8")))
            self.assertTrue(installer.read_bytes().startswith(note.encode("utf-8")))


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
        package_job = workflow.split("  package-chart:\n", 1)[1].split("  publish-release:\n", 1)[0]
        self.assertIn("OCM_REPOSITORY: ${{ needs.publish-image.outputs.ocm_repository }}", package_job)
        self.assertIn('--ocm-repository "$OCM_REPOSITORY"', package_job)
        self.assertLess(package_job.index("--ocm-repository"), package_job.index("helm dependency update"))
        self.assertLess(workflow.index("python3 hack/set-release-image.py"), workflow.index('for sub in charts/*/'))
        config = json.loads((root / "release-please-config.json").read_text())["packages"]["."]
        if config.get("release-as"):
            self.assertEqual(config["prerelease"], "-" in config["release-as"])
        self.assertIn({"type": "yaml", "path": "charts/chat-ui-operator-ocm/values.yaml", "jsonpath": "$.component.semver"}, config["extra-files"])


if __name__ == "__main__":
    unittest.main()
