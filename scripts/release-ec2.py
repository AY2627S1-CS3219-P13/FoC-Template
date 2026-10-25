"""GitHub runner entry point: deploy one tested release through SSM."""

import json
import os
import re
import subprocess
import time


def deployment_command(sha, registry):
    if not re.fullmatch(r"[0-9a-f]{40}", sha):
        raise ValueError("RELEASE_SHA must be a full lowercase Git commit SHA")
    if not re.fullmatch(r"[0-9]{12}\.dkr\.ecr\.[a-z0-9-]+\.amazonaws\.com", registry):
        raise ValueError("ECR_REGISTRY must be a private ECR registry hostname")
    # Values above contain no shell metacharacters. Keep main attached so manual
    # git pull still works; preserve untracked .env files and never reset/clean.
    return f"""set -eu
cd /opt/foc
test "$(git branch --show-current)" = main
git diff --quiet
git diff --cached --quiet
git fetch --no-tags origin main
if [ "$(git rev-parse FETCH_HEAD)" != '{sha}' ]; then
  echo 'Refusing an outdated release: main has changed.' >&2
  exit 1
fi
git merge --ff-only '{sha}'
test "$(git rev-parse HEAD)" = '{sha}'
env ECR_REGISTRY='{registry}' sh scripts/deploy-ec2.sh '{sha}'
"""


def aws(*args):
    result = subprocess.run(
        ["aws", "--cli-connect-timeout", "10", "--cli-read-timeout", "30", *args],
        check=False, capture_output=True, text=True,
    )
    if result.returncode:
        raise RuntimeError(result.stderr.strip())
    return json.loads(result.stdout)


def wait_for_command(command_id, instance_id):
    # Delivery can take 60s, execution up to 900s. Allow propagation/poll overhead.
    deadline = time.monotonic() + 1100
    previous_status = None
    while time.monotonic() < deadline:
        try:
            invocation = aws(
                "ssm", "get-command-invocation", "--command-id", command_id,
                "--instance-id", instance_id, "--output", "json",
            )
        except RuntimeError as error:
            # SSM is eventually consistent immediately after SendCommand.
            if "InvocationDoesNotExist" not in str(error):
                raise
            time.sleep(10)
            continue
        status = invocation["Status"]
        if status != previous_status:
            print(f"SSM status: {status}", flush=True)
            previous_status = status
        if status not in {"Pending", "InProgress", "Delayed", "Cancelling"}:
            print(invocation.get("StandardOutputContent", ""), flush=True)
            print(invocation.get("StandardErrorContent", ""), flush=True)
            if status != "Success" or invocation.get("ResponseCode") != 0:
                raise RuntimeError(f"Deployment failed: {status}")
            return
        time.sleep(10)
    raise RuntimeError(
        f"Stopped waiting for SSM command {command_id}. Check it in AWS before "
        "retrying; the remote command may still be running."
    )


def main():
    sha = os.environ["RELEASE_SHA"]
    command = deployment_command(sha, os.environ["ECR_REGISTRY"])
    instance_id = os.environ["EC2_INSTANCE_ID"]
    if not re.fullmatch(r"i-([0-9a-f]{8}|[0-9a-f]{17})", instance_id):
        raise ValueError("EC2_INSTANCE_ID must be an EC2 instance ID")
    result = aws(
        "ssm", "send-command", "--instance-ids", instance_id,
        "--document-name", "AWS-RunShellScript", "--timeout-seconds", "60",
        "--parameters", json.dumps({"commands": [command], "executionTimeout": ["900"]}),
        "--comment", f"FoC release {sha}", "--output", "json",
    )
    command_id = result["Command"]["CommandId"]
    print(f"SSM command: {command_id}", flush=True)
    wait_for_command(command_id, instance_id)
    summary = os.environ.get("GITHUB_STEP_SUMMARY")
    if summary:
        with open(summary, "a", encoding="utf-8") as output:
            output.write(f"Deployed `{sha}` to `{instance_id}`; readiness checks passed.\n")


if __name__ == "__main__":
    main()
