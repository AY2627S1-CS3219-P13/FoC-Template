"""Exercise deployment ordering and failures without Docker or AWS access."""

import json
import os
from pathlib import Path
import shutil
import subprocess
import tempfile
import unittest


TAG = "a" * 40
SERVICES = ["user-service", "supplier-service", "credit-service", "order-service", "frontend", "gateway"]
STUB = r'''#!/usr/bin/env python3
import json
import os
from pathlib import Path
import sys

tool = Path(sys.argv[0]).name
args = sys.argv[1:]
with open(os.environ["CALL_LOG"], "a") as log:
    log.write(json.dumps([tool] + args) + "\n")
if tool == "curl":
    if os.environ.get("FAIL_READY") == "1" and ":8084/readyz" in args[-1]:
        sys.exit(22)
    print('{"status":"ready"}')
    sys.exit(0)
if args[0] == "inspect":
    service = args[-1].removeprefix("container-")
    tag = "old" if os.environ.get("WRONG_IMAGE") == service else os.environ["FOC_IMAGE_TAG"]
    print(os.environ["ECR_REGISTRY"] + "/foc/" + service + ":" + tag)
    sys.exit(0)
action = args[5]
if action == os.environ.get("FAIL_ACTION"):
    sys.exit(1)
if action == "port":
    ports = {"user-service": 8080, "supplier-service": 8083, "credit-service": 8082,
             "order-service": 8084, "gateway": 3000}
    print("127.0.0.1:" + str(ports[args[6]]))
elif action == "ps" and "-q" in args:
    print("container-" + args[-1])
'''


class DeployTests(unittest.TestCase):
    def setUp(self):
        self.temp = tempfile.TemporaryDirectory()
        self.addCleanup(self.temp.cleanup)
        self.root = Path(self.temp.name)
        (self.root / "scripts").mkdir()
        shutil.copy(Path(__file__).with_name("deploy-ec2.sh"), self.root / "scripts/deploy-ec2.sh")
        for name in [".env", "credit-service/.env", "order-service/.env"]:
            path = self.root / name
            path.parent.mkdir(parents=True, exist_ok=True)
            path.write_text("# disposable test configuration\n")
        tools = self.root / "bin"
        tools.mkdir()
        for name in ["docker", "curl"]:
            path = tools / name
            path.write_text(STUB)
            path.chmod(0o755)
        self.log = self.root / "calls.jsonl"
        self.env = {**os.environ, "PATH": str(tools) + os.pathsep + os.environ["PATH"],
                    "CALL_LOG": str(self.log), "ECR_REGISTRY": "test.invalid"}

    def run_deploy(self, tag=TAG, **extra):
        return subprocess.run(["sh", str(self.root / "scripts/deploy-ec2.sh"), tag],
                              env={**self.env, **extra}, text=True, capture_output=True, timeout=10)

    def calls(self):
        return [json.loads(line) for line in self.log.read_text().splitlines()] if self.log.exists() else []

    def action(self, call):
        return call[6] if call[0] == "docker" and call[1] == "compose" else None

    def test_success_downloads_before_stop_and_waits_for_credit_before_order(self):
        result = self.run_deploy()
        self.assertEqual(result.returncode, 0, result.stderr)
        self.assertIn("Deployment ready: " + TAG, result.stdout)
        calls = self.calls()
        pull = next(i for i, c in enumerate(calls) if self.action(c) == "pull")
        stop = next(i for i, c in enumerate(calls) if self.action(c) == "stop")
        gateway = next(i for i, c in enumerate(calls) if self.action(c) == "up" and c[-1] == "gateway")
        credit = next(i for i, c in enumerate(calls) if c[0] == "curl" and ":8082/readyz" in c[-1])
        self.assertLess(pull, stop)
        self.assertLess(stop, credit)
        self.assertLess(credit, gateway)
        self.assertEqual(calls[pull][-6:], SERVICES)
        self.assertFalse(any(self.action(c) == "down" or "--volumes" in c for c in calls))
        for call in calls:
            if self.action(call) == "up":
                self.assertIn("--no-build", call)
                self.assertIn("never", call)
        self.assertEqual(sum(c[0] == "docker" and c[1] == "inspect" for c in calls), 6)

    def test_failed_download_does_not_stop_running_services(self):
        result = self.run_deploy(FAIL_ACTION="pull")
        self.assertNotEqual(result.returncode, 0)
        self.assertFalse(any(self.action(c) in ["stop", "up"] for c in self.calls()))

    def test_invalid_configuration_does_not_download_or_stop(self):
        result = self.run_deploy(FAIL_ACTION="config")
        self.assertNotEqual(result.returncode, 0)
        self.assertFalse(any(self.action(c) in ["pull", "stop", "up"] for c in self.calls()))

    def test_invalid_tag_does_not_call_docker(self):
        result = self.run_deploy("latest")
        self.assertNotEqual(result.returncode, 0)
        self.assertEqual(self.calls(), [])

    def test_missing_environment_does_not_call_docker(self):
        (self.root / "credit-service/.env").unlink()
        result = self.run_deploy()
        self.assertNotEqual(result.returncode, 0)
        self.assertEqual(self.calls(), [])

    def test_failed_order_readiness_is_not_success(self):
        result = self.run_deploy(FAIL_READY="1")
        self.assertNotEqual(result.returncode, 0)
        self.assertNotIn("Deployment ready:", result.stdout)

    def test_wrong_running_image_is_not_success(self):
        result = self.run_deploy(WRONG_IMAGE="order-service")
        self.assertNotEqual(result.returncode, 0)
        self.assertIn("order-service is not running the requested image", result.stderr)


if __name__ == "__main__":
    unittest.main()
