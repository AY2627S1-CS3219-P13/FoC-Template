"""Exercise the GitHub-to-SSM boundary without AWS or an application stack."""

import importlib.util
import json
import os
from pathlib import Path
import subprocess
import tempfile
import unittest
from unittest.mock import patch


spec = importlib.util.spec_from_file_location(
    "release", Path(__file__).with_name("release-ec2.py")
)
release = importlib.util.module_from_spec(spec)
spec.loader.exec_module(release)
SHA = "a" * 40
REGISTRY = "291723765178.dkr.ecr.ap-southeast-1.amazonaws.com"
INSTANCE = "i-0ceabdd31ee0f7594"


class ReleaseTests(unittest.TestCase):
    def test_rejects_command_injection_before_aws(self):
        for sha, registry in [(SHA + ";id", REGISTRY), (SHA, REGISTRY + ";id")]:
            with self.subTest(sha=sha, registry=registry), self.assertRaises(ValueError):
                release.deployment_command(sha, registry)

    @patch.object(release.time, "sleep")
    @patch.object(release, "aws")
    def test_waits_through_eventual_consistency_and_progress(self, aws, sleep):
        aws.side_effect = [
            RuntimeError("InvocationDoesNotExist"),
            {"Status": "Pending"}, {"Status": "InProgress"},
            {"Status": "Success", "ResponseCode": 0},
        ]
        release.wait_for_command("command", INSTANCE)
        self.assertEqual(aws.call_count, 4)
        self.assertEqual(sleep.call_count, 3)

    def test_remote_failure_and_nonzero_exit_fail_release(self):
        for status, code in [("Failed", 1), ("TimedOut", -1), ("Cancelled", -1), ("Success", 1)]:
            with self.subTest(status=status, code=code), patch.object(
                release, "aws", return_value={"Status": status, "ResponseCode": code}
            ), self.assertRaises(RuntimeError):
                release.wait_for_command("command", INSTANCE)

    @patch.object(release, "aws", side_effect=RuntimeError("AccessDenied"))
    def test_permission_error_is_not_retried(self, aws):
        with self.assertRaisesRegex(RuntimeError, "AccessDenied"):
            release.wait_for_command("command", INSTANCE)
        aws.assert_called_once()

    @patch.object(release.time, "monotonic", side_effect=[0, 1101])
    @patch.object(release, "aws")
    def test_wait_has_a_deadline(self, aws, monotonic):
        with self.assertRaisesRegex(RuntimeError, "may still be running"):
            release.wait_for_command("command", INSTANCE)
        aws.assert_not_called()

    @patch.dict(os.environ, {"RELEASE_SHA": SHA, "ECR_REGISTRY": REGISTRY,
                             "EC2_INSTANCE_ID": INSTANCE}, clear=True)
    @patch.object(release, "wait_for_command")
    @patch.object(release, "aws", return_value={"Command": {"CommandId": "command"}})
    def test_targets_existing_instance_with_bounded_ssm_command(self, aws, wait):
        release.main()
        args = aws.call_args.args
        self.assertEqual(args[:4], ("ssm", "send-command", "--instance-ids", INSTANCE))
        parameters = json.loads(args[args.index("--parameters") + 1])
        self.assertEqual(parameters["executionTimeout"], ["900"])
        self.assertIn(f"sh scripts/deploy-ec2.sh '{SHA}'", parameters["commands"][0])
        wait.assert_called_once_with("command", INSTANCE)

    def test_server_refuses_dirty_wrong_branch_stale_and_ahead_checkouts(self):
        # Run the real generated shell command with a fake Git executable and
        # deployment marker. No network, Docker, or production files are used.
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            (root / "scripts").mkdir()
            (root / "scripts/deploy-ec2.sh").write_text("touch deployed\n")
            (root / ".env").write_text("existing secret\n")
            git = root / "git"
            git.write_text("""#!/bin/sh
case "$*" in
  'branch --show-current') echo "${TEST_BRANCH:-main}" ;;
  'diff --quiet') test "${TEST_DIRTY:-0}" = 0 ;;
  'diff --cached --quiet') test "${TEST_STAGED:-0}" = 0 ;;
  'rev-parse FETCH_HEAD') echo "$TEST_REMOTE_SHA" ;;
  'rev-parse HEAD') echo "$TEST_LOCAL_SHA" ;;
  'fetch --no-tags origin main') exit 0 ;;
  'merge --ff-only '*) exit 0 ;;
  *) exit 99 ;;
esac
""")
            git.chmod(0o755)
            command = release.deployment_command(SHA, REGISTRY).replace(
                "cd /opt/foc", f"cd '{directory}'"
            )
            cases = [({}, True), ({"TEST_DIRTY": "1"}, False),
                     ({"TEST_STAGED": "1"}, False), ({"TEST_BRANCH": "feature"}, False),
                     ({"TEST_REMOTE_SHA": "b" * 40}, False),
                     ({"TEST_LOCAL_SHA": "b" * 40}, False)]
            for changes, expected in cases:
                with self.subTest(changes=changes):
                    marker = root / "deployed"
                    marker.unlink(missing_ok=True)
                    env = {**os.environ, "PATH": f"{root}:{os.environ['PATH']}",
                           "TEST_REMOTE_SHA": SHA, "TEST_LOCAL_SHA": SHA, **changes}
                    result = subprocess.run(["sh", "-c", command], env=env, capture_output=True)
                    self.assertEqual(result.returncode == 0, expected, result.stderr)
                    self.assertEqual(marker.exists(), expected)
                    self.assertEqual((root / ".env").read_text(), "existing secret\n")


if __name__ == "__main__":
    unittest.main()
