#!/usr/bin/env python3
"""Render security checks for opt-in enrollment; no Kubernetes access."""
import copy
import json
import os
from pathlib import Path
import subprocess
import tempfile
import unittest

import yaml

ROOT = Path(__file__).resolve().parents[1]
HELM = os.environ.get("HELM", "helm")
DIGEST_IMAGE = "registry.example.test/chat-ui-controller@sha256:" + "a" * 64


def enrollment_values():
    return {
        "enabled": True,
        "image": DIGEST_IMAGE,
        "serviceAccountName": "enrollment-reader",
        "kcpKubeconfigSecret": {"name": "kcp-reader", "key": "config"},
        "mspKubeconfigSecret": {"name": "msp-enrollment", "key": "config"},
        "configuration": {
            "clients": [{"name": "chat-ui-public"}, {"name": "chat-ui-example"}],
        },
    }


def render(chart, values=None, valid=True):
    with tempfile.TemporaryDirectory(prefix="enrollment-chart-") as directory:
        path = Path(directory) / "values.json"
        path.write_text(json.dumps(values or {}))
        result = subprocess.run(
            [HELM, "template", "enrollment-test", str(ROOT / "charts" / chart),
             "--namespace", "platform-auth", "--values", str(path)],
            capture_output=True, text=True, check=False,
        )
    if not valid:
        if result.returncode == 0:
            raise AssertionError("unsafe chart input rendered successfully")
        return result.stderr
    if result.returncode:
        raise AssertionError(result.stderr)
    return [obj for obj in yaml.safe_load_all(result.stdout) if obj]


def one(objects, kind):
    selected = [obj for obj in objects if obj["kind"] == kind]
    if len(selected) != 1:
        raise AssertionError(f"expected one {kind}, found {len(selected)}")
    return selected[0]


class EnrollmentChartTests(unittest.TestCase):
    def test_disabled_default_grants_nothing(self):
        self.assertEqual(render("chat-ui-enrollment"), [])
        self.assertEqual(render("chat-ui-enrollment", {"image": "unsafe:latest"}), [])

    def test_enabling_requires_digest_and_existing_credentials(self):
        for image in ["", "registry.example.test/controller:latest",
                      "registry.example.test/controller@sha256:" + "A" * 64,
                      "registry.example.test/controller@sha256:" + "a" * 63]:
            with self.subTest(image=image):
                values = enrollment_values()
                values["image"] = image
                self.assertIn("digest-pinned image", render("chat-ui-enrollment", values, False))
        for field in ["kcpKubeconfigSecret", "mspKubeconfigSecret"]:
            with self.subTest(field=field):
                values = enrollment_values()
                values[field]["name"] = ""
                self.assertIn(field + ".name is required", render("chat-ui-enrollment", values, False))
        values = enrollment_values()
        values["configuration"]["clients"] = []
        self.assertIn("configuration.clients is required", render("chat-ui-enrollment", values, False))
        values = enrollment_values()
        values["serviceAccountName"] = ""
        self.assertIn("serviceAccountName is required", render("chat-ui-enrollment", values, False))

    def test_platform_permissions_are_exact_and_have_no_secret_access(self):
        objects = render("chat-ui-enrollment", enrollment_values())
        self.assertCountEqual([obj["kind"] for obj in objects],
                              ["ServiceAccount", "ClusterRole", "ClusterRoleBinding", "ConfigMap", "Deployment"])
        role = one(objects, "ClusterRole")
        self.assertEqual(role["rules"], [
            {"apiGroups": ["openidclient.keycloak.crossplane.io"], "resources": ["clients"],
             "resourceNames": ["chat-ui-public", "chat-ui-example"], "verbs": ["get", "patch"]},
            {"apiGroups": [""], "resources": ["namespaces"],
             "resourceNames": ["kube-system"], "verbs": ["get"]},
        ])
        binding = one(objects, "ClusterRoleBinding")
        self.assertEqual(binding["roleRef"]["name"], role["metadata"]["name"])
        self.assertEqual(binding["subjects"], [{"kind": "ServiceAccount", "name": "enrollment-reader",
                                               "namespace": "platform-auth"}])

    def test_workload_uses_readonly_credentials_and_recreate(self):
        objects = render("chat-ui-enrollment", enrollment_values())
        deployment = one(objects, "Deployment")
        self.assertEqual(deployment["spec"]["replicas"], 1)
        self.assertEqual(deployment["spec"]["strategy"]["type"], "Recreate")
        pod = deployment["spec"]["template"]["spec"]
        self.assertEqual(pod["serviceAccountName"], "enrollment-reader")
        self.assertTrue(pod["securityContext"]["runAsNonRoot"])
        self.assertEqual(pod["securityContext"]["seccompProfile"]["type"], "RuntimeDefault")
        container = pod["containers"][0]
        self.assertEqual(container["image"], DIGEST_IMAGE)
        self.assertEqual(container["command"], ["/chat-ui-enrollment"])
        self.assertIn("--apply", container["args"])
        self.assertIn("--platform-in-cluster", container["args"])
        self.assertTrue(container["securityContext"]["readOnlyRootFilesystem"])
        self.assertFalse(container["securityContext"]["allowPrivilegeEscalation"])
        self.assertEqual(container["securityContext"]["capabilities"]["drop"], ["ALL"])
        self.assertTrue(all(mount["readOnly"] for mount in container["volumeMounts"]))
        secrets = [volume["secret"] for volume in pod["volumes"] if "secret" in volume]
        self.assertEqual({secret["secretName"] for secret in secrets}, {"kcp-reader", "msp-enrollment"})
        self.assertTrue(all(secret["defaultMode"] == 0o440 for secret in secrets))
        self.assertEqual(container["readinessProbe"]["httpGet"]["path"], "/readyz")

    def test_configuration_changes_roll_the_single_writer(self):
        values = enrollment_values()
        before = one(render("chat-ui-enrollment", values), "Deployment")["spec"]["template"]["metadata"]
        values["configuration"]["publicHost"] = "chat.example.test"
        after = one(render("chat-ui-enrollment", values), "Deployment")["spec"]["template"]["metadata"]
        self.assertNotEqual(before["annotations"]["checksum/configuration"],
                            after["annotations"]["checksum/configuration"])

    def test_operator_reader_uses_only_the_named_configmap(self):
        values = {"accessPolicy": {"credentialsNamespace": "credential-store",
                                   "enrollment": {"configMapName": "trusted-enrollment"}}}
        objects = render("chat-ui-operator", values)
        role = one(objects, "Role")
        self.assertEqual(role["metadata"]["namespace"], "credential-store")
        self.assertEqual(role["rules"], [{"apiGroups": [""], "resources": ["configmaps"],
                                         "resourceNames": ["trusted-enrollment"], "verbs": ["get"]}])
        binding = one(objects, "RoleBinding")
        self.assertEqual(binding["metadata"]["namespace"], "credential-store")
        self.assertEqual(binding["subjects"][0]["namespace"], "platform-auth")
        self.assertEqual(binding["roleRef"]["name"], role["metadata"]["name"])
        for key in ["credentialsNamespace", "configMapName"]:
            missing = copy.deepcopy(values)
            if key == "credentialsNamespace":
                missing["accessPolicy"][key] = ""
            else:
                missing["accessPolicy"]["enrollment"][key] = ""
            self.assertIn("is required", render("chat-ui-operator", missing, False))
        values["rbac"] = {"create": False}
        self.assertFalse(any(obj["kind"] in {"Role", "RoleBinding"}
                             for obj in render("chat-ui-operator", values)))

    def test_operator_defaults_do_not_add_enrollment_permissions(self):
        self.assertFalse(any(obj["kind"] in {"Role", "RoleBinding"}
                             for obj in render("chat-ui-operator")))


if __name__ == "__main__":
    unittest.main()
