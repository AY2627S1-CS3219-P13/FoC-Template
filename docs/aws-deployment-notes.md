# AWS deployment notes

The initial AWS deployment was confirmed working by Keith on 24 October 2026.
The application runs on one Ubuntu 24.04 `t3a.medium` EC2 instance in Singapore,
using Docker Compose and an encrypted 40 GiB gp3 root volume. Browser access is
through SSM tunnels to the gateway on port 3000 and Mailpit on port 8025.
All four service readiness endpoints passed after the recovery below.

The server's `compose.override.yaml` now sets `restart: unless-stopped` on all
11 runtime containers. A controlled EC2 reboot passed: Docker and all containers
started automatically, all four backend readiness endpoints returned ready, and
the gateway returned HTTP 200. User, Supplier and Order each reported one
automatic container restart after boot. Startup robustness remains a follow-up;
recovery after a reboot does not validate the fresh-volume initialization path.
The earlier override exists on EC2. The versioned `compose.ec2.yaml` now carries
these restart settings for image deployments; the deployment command explicitly
selects it instead of that untracked server override.

## Pending first start issue

On the first deployment, Supplier and Order exited with `database migration
failed`. Their PostgreSQL health checks passed during initialization, before
the temporary database servers shut down and the final TCP listeners started.
The application startup timestamps overlap that initialization window, indicating
a database readiness race. The homepage still returned HTTP 200 while those two
services were stopped.

- **Reproduction condition:** Start the stack against fresh, empty database
  volumes. A dependent service starts after the health check passes for the
  temporary PostgreSQL server but before the final server accepts TCP connections.
  Timing affects reproduction; an ordinary restart with initialized volumes may
  not reproduce it.
- **Intended behavior:** Services start only once their application databases
  accept connections over the same TCP path the services use. Deployment success
  requires every backend to be ready, in addition to the homepage loading.
- **Recovery performed:** After database initialization completed, ran
  `docker compose start supplier-service order-service`. Supplier applied all
  3 migrations and Order applied all 9; both then returned ready.
- **Follow-up:** Correct database readiness checks, review bounded startup
  retries, and make deployment verification check all four backends. Confirm
  the fix with repeated fresh starts using disposable test volumes; preserve
  existing deployment data. The EC2 override now probes TCP at `127.0.0.1`,
  excluding the temporary socket-only initialization server. The original
  development Compose health checks remain unchanged. CI exercises fresh
  databases with the EC2 override in its disposable stack.

## Next infrastructure work

- Complete gateway configuration and target registration for the CloudFront
  distribution below, then verify browser access through its HTTPS hostname.
- Configure real email delivery for independent registration, and database backups.
- Measure runtime memory before attempting to downsize to `t3a.small`.

## Public HTTPS through CloudFront and an internal ALB

On 28 October 2026, the team configured the following infrastructure in Singapore.
The existing release workflow had successfully deployed commit
`2af78aeb6c882d36e478fe8c66f94d1244d1325d` before this ingress setup.

```text
Browser --HTTPS--> CloudFront --private HTTP:80--> internal ALB
  --HTTP:3000--> EC2 gateway --> frontend and four services
```

| Resource | Current value |
| --- | --- |
| Public hostname | `https://d5ovr1dbnwrxo.cloudfront.net` |
| CloudFront distribution | `E29RB28L8JY07Q` |
| VPC origin | `vo_J7xwQ1gJlUwGPZdQTK46AM` (`foc-demo-vpc-origin`) |
| Internal ALB | `foc-demo-alb`, HTTP listener 80 |
| Target group | `foc-demo-gateway-tg`, instance targets on HTTP 3000, health `/healthz` |
| Private subnet 1a | `subnet-088c19995eb49969a`, `10.20.16.0/24` |
| Private subnet 1b | `subnet-07a413d65a178bd7a`, `10.20.17.0/24` |
| Private route table | `rtb-0fe307ee2e09dfb94`, local VPC route only |
| ALB security group | `sg-0e5d9a0585b72a4ad` |
| CloudFront managed source group | `sg-0a4ad2d1614445701` |

The ALB security group permits inbound HTTP 80 only from the CloudFront managed
source group and outbound TCP 3000 only to `foc-demo-ec2-sg`. EC2 permits inbound
TCP 3000 only from the ALB group. Its existing public-subnet placement supports
outbound SSM/ECR access; the ALB connects using its private IP. Neither database
ports nor Mailpit are publicly exposed.

The distribution uses the CloudFront certificate for its assigned hostname,
redirects HTTP viewers to HTTPS, and allows all seven HTTP methods. Its single
default behavior uses `CachingDisabled` and
`AllViewerAndCloudFrontHeaders-2022-06`. Use pay-as-you-go billing: the Free and Pro
flat-rate plans do not support private VPC origins. No WAF is currently attached.

### Activate application ingress

First configure and test a real STARTTLS SMTP provider using `USER_SMTP_ADDRESS`,
`USER_SMTP_FROM`, `USER_SMTP_TLS=true` and the provider's SMTP credentials. The
default Mailpit setup uses plain SMTP and cannot be retained unchanged in public
HTTPS mode: User Service intentionally refuses secure cookies with SMTP TLS
disabled. Do not turn on the TLS flag against plain Mailpit as a workaround.

Start EC2 manually, wait for SSM access, and edit the existing `/opt/foc/.env`
as root. Set each of these keys once, replacing an existing entry if present:

```dotenv
USER_APP_ORIGIN=https://d5ovr1dbnwrxo.cloudfront.net
USER_COOKIE_SECURE=true
GATEWAY_BIND_ADDRESS=0.0.0.0
GATEWAY_INGRESS_CONFIG=./gateway/cloudfront-ingress.conf
```

Preserve all other settings and credentials. The included services derive their
shared origin and secure session-cookie name from this root configuration; do
not hard-code a different cookie name in the child service environments. Keep
the browser API URL same-origin (the default empty `NEXT_PUBLIC_USER_API_URL`).

Merge the ingress PR after CI passes and run Release to AWS on `main` if the
automatic attempt occurred while EC2 was stopped. The release fast-forwards the
server checkout before loading Compose, so the ingress file exists when mounted.
The EC2 override replaces the gateway's loopback-only port with the configured
binding and mounts the policy read-only. The helper checks a wildcard listener
through `127.0.0.1` and continues checking all backend readiness endpoints.

After deployment succeeds, register instance `i-0ceabdd31ee0f7594` in the target
group on port 3000, wait for healthy status, and test the public hostname without
an SSM browser tunnel. Check login/logout, secure session cookies, supplier/wallet
and order flows. The ALB `/healthz` checks only the gateway, so it does not replace
the deployment helper's backend checks. Verify registration email delivery with
the configured provider; Mailpit's web UI remains private.

The gateway trusts forwarding peers only in the two private ingress subnets.
Recursive real-IP parsing rejects forged header prefixes and preserves independent
viewer limits. It accepts the CloudFront viewer scheme only from these trusted
peers. Keep these trust ranges aligned with the ALB/CloudFront ENI subnet layout
and retain the security-group restrictions. Local development and an EC2 `.env`
without these ingress keys retain loopback binding and no proxy trust.

CI exercises trusted/untrusted proxy behavior with a disposable echo upstream,
checks both Compose bindings and policy mounts, and retains the normal application
smoke and fresh-database image deployment tests. AWS routing and public browser
behavior require the activation checks above; isolated CI does not prove the
actual CloudFront/ALB path.

### SMTP preflight failure and recovery

The first ingress rollout on 28 October failed because secure cookies were
enabled while `USER_SMTP_TLS=false` still selected plain Mailpit. User Service
rejected startup with `deployed email must use SMTP STARTTLS`; Order had already
been stopped by the deployment helper. The initial activation instructions
incorrectly treated real SMTP as a later independent step.

Recovery saved a root-only copy of `.env` outside the Git checkout, restored
localhost origin, insecure local cookies, loopback gateway binding and the local
ingress policy, then successfully deployed the same immutable release
`d96cb4ac75ee62f0d63ebff200d89c8d05a917d2`. All backend readiness checks and the
gateway passed. Data and credentials were preserved; no ALB target was registered.

Deployment now runs `user-service check-config` after downloading images and
before stopping Order. A configuration failure therefore aborts before changing
running services. The check validates configuration without database/network
side effects; it does not establish SMTP connectivity or prove mail delivery.

SES account reads in Singapore succeeded. The account is in the sandbox with
200 messages/day, one message/second and no verified identities. Sandbox sending
requires verified recipient identities. `u.nus.edu` publishes DMARC `p=reject`;
do not use an NUS sender with SES without domain-owner authorization and aligned
authentication. Choose a domain the team controls for SES, or use the owning
mail provider's supported authenticated SMTP service.

## GitHub AWS connection check

The `AWS connection check` workflow is a manual infrastructure check. It assumes
the GitHub deployment role through OIDC, logs in to ECR, reads image metadata
from the six application repositories, and sends a read-only readiness command
to EC2 through SSM. Empty image lists are expected before the first image upload.
It does not upload images, change application data or restart containers.

The role trust policy accepts this repository's immutable OIDC subject on `main`
only. The workflow therefore must be merged into `main` before it can be run.
It does not use a GitHub environment; adding one later requires adjusting the
OIDC subject condition to match the environment identity.

Configure these repository variables under Settings, Secrets and variables,
Actions, Variables:

| Variable | Value to copy from AWS |
| --- | --- |
| `AWS_REGION` | Deployment region, currently `ap-southeast-1` |
| `AWS_ROLE_ARN` | ARN of the `foc-github-deploy` IAM role |
| `ECR_REGISTRY` | Private registry hostname, without `https://` or a repository path |
| `EC2_INSTANCE_ID` | ID of the running application instance |

The workflow also restricts authentication to the current school AWS account.
When moving to another account, update that account restriction along with the
variables and IAM policies. Application secrets remain on EC2.

After merging, open GitHub Actions, select AWS connection check, choose Run
workflow with branch `main`, and inspect each step. Success verifies AWS
authentication, ECR login and repository reads, SSM command execution, and current
backend readiness. Image upload and EC2 image download permissions still need
to be exercised by the subsequent image build and deployment workflow.

The connection check passed on 24 October 2026, including GitHub OIDC login,
ECR authentication and repository reads, and all backend readiness requests
through SSM.

## Automatic CI/CD releases

`Release to AWS` (the earlier `Publish AWS images` workflow) runs when CI finishes
successfully for a push or manual run on this repository's `main` branch. PR and
fork runs cannot release. It takes the commit from the completed CI run, checks
that the latest push/manual CI run for that exact commit succeeded, then publishes
and deploys it. A manual Run workflow on `main` remains available for retries.
If CI did not run because Actions was disabled, run CI manually on `main` first.

```text
Merge/push main → CI checks → six ECR images → SSM command → EC2 readiness checks
```

The release uses one full commit SHA for checkout, all six image tags and the EC2
checkout. It rejects releases superseded by a newer `main` commit, including a
second check on EC2 after image publishing. If a newer change arrives during a
release, that older release may fail as outdated; wait for the newer commit's CI
and release instead. Only one release workflow runs at a time.

The publisher builds the four Go backends, frontend and gateway using their
existing Dockerfiles on GitHub runners. It builds Linux amd64 images for the
current EC2 architecture and pushes them to the corresponding `foc/` ECR
repositories. Every image uses the full Git commit SHA as its immutable tag.
Runtime secrets are not supplied to the build; application `.env` files stay on
EC2. PostgreSQL and Mailpit continue using upstream images.

To release:

1. Start EC2 manually and ensure it is reachable through SSM.
2. Merge changes into `main` and wait for CI to pass.
3. Open Actions, select Release to AWS, and inspect the six `publish` jobs followed
   by `deploy`. The final summary identifies the deployed commit and instance.
4. Check the application through the existing SSM browser tunnel.

The publisher reuses tags that already exist, allowing a failed release to be
retried without overwriting immutable images. Permission errors and missing
repositories fail the workflow. A partial failure may leave only some images
uploaded; do not deploy a commit until all six jobs succeed. Concurrent publisher
runs are serialized, and at most two images build in parallel within a run.

The `deploy` job runs only after all six publishers succeed. GitHub assumes the
existing deployment role through OIDC and uses SSM to execute the command as root
on the configured instance. `scripts/release-ec2.py` runs on GitHub, where the AWS
CLI is available; EC2 only needs Git, Docker, curl, flock and its ECR credential
helper. No SSH access, inbound security group rule, new IAM role, or application
secret in GitHub is needed.

The remote command requires a clean tracked checkout on branch `main` at
`/opt/foc`, fetches `main`, and fast-forwards to the tested commit. It refuses
divergent/local commits or a different remote head, without resetting files.
Untracked `.env` files are retained. It then runs `scripts/deploy-ec2.sh` using
that commit's deployment configuration and image tag.

SSM gets 60 seconds for delivery and 15 minutes for execution; GitHub polls for
the result and requires both SSM success and exit code zero. A stopped or
unreachable instance fails deployment; the workflow never starts EC2. Start it
manually when needed, then retry Release to AWS on the current `main` commit.
If a release fails after reaching EC2, inspect its SSM output before retrying.
Cancelling GitHub's workflow does not cancel an already submitted SSM command.
The existing IAM policy needs only SendCommand to this instance and document,
plus GetCommandInvocation; no additional AWS permissions are required.

## Deploy published images to EC2

EC2 uses the ECR Docker credential helper and its existing instance role to
download private images. Install `amazon-ecr-credential-helper` on Ubuntu and
configure `/root/.docker/config.json` with a `credHelpers` entry mapping the
private registry hostname to `ecr-login`. The root configuration applies because
Docker commands run with `sudo`. The AWS CLI and personal AWS credentials are
not needed on EC2. The gateway image pull was confirmed working before this
deployment checkpoint.

The first manual image deployment was confirmed successful on 25 October 2026:
all six application containers used ECR tag
`ac4c2d0349e79d51069e92033bcfab69106cf16e`, all four databases were healthy,
and the deployment helper's backend and gateway checks passed.

The image deployment command combines `compose.yaml` with `compose.ec2.yaml`.
The latter removes runtime build definitions, references the six ECR images by
one full commit tag, retains restart policies, and checks database readiness
over TCP. The root project name and existing database volume definitions stay
the same. The earlier server `compose.override.yaml` is not selected by this
command. Local development still uses `scripts/start.sh` and local builds.

On the existing EC2 instance, after merging the deployment changes:

```sh
cd /opt/foc
sudo git pull --ff-only origin main
sudo env ECR_REGISTRY=291723765178.dkr.ecr.ap-southeast-1.amazonaws.com \
  sh scripts/deploy-ec2.sh ac4c2d0349e79d51069e92033bcfab69106cf16e
```

The example tag is the first successfully published release. For future changes,
use a successfully published commit and a compatible checkout of the deployment
configuration. All six repositories must contain the selected commit tag.
The command preserves the existing `.env` files; it does not generate replacement
credentials or delete database volumes.

The command performs these steps:

1. Validate the arguments, existing environment files and combined Compose model.
2. Acquire a deployment lock and download any of the six images not already cached.
   Download failure stops the deployment before running services are stopped.
3. Stop the old Order process, as required for Order upgrades.
4. Update User, Supplier and Credit and wait for all three readiness endpoints.
   This ensures the required Credit endpoint is available before starting Order.
5. Update Order, frontend and gateway, check Order readiness and the homepage,
   and confirm all six containers reference the requested images.

Single-instance deployment briefly interrupts service. A rollout failure returns
a nonzero status and may leave a partially updated stack for investigation; the
script does not automatically reverse database migrations. Retrying the command
uses the same immutable images and existing data. Reboot recovery remains handled
by Docker's restart policies; restarting EC2 does not fetch a newer application
version. A successful pull alone also does not replace an existing container.

Deployment checks include command-order/failure tests and a fresh disposable CI
stack that uses locally tagged images with the EC2 override. It runs the same
deployment command, the existing smoke check and the funded four-service lifecycle
without private ECR access. This CI check removes only its own disposable volumes;
do not run `scripts/check-ec2-images.sh` on the application server.

`scripts/check-ec2-release.py` separately tests SSM propagation, failures and
timeouts, command targeting, and refusal of dirty/stale/different-branch server
checkouts without calling AWS or touching the application stack.

## Bounded container logs and CloudWatch observability

Added on 30 October 2026. Goal: stop unbounded `json-file` growth on the root
volume, centralize container logs with seven-day retention, and collect memory
and root-disk metrics for three basic alarms. The CloudWatch agent was chosen
over the `awslogs` Docker driver because it also provides the host metrics, it
keeps `docker logs` working on the host, and it does not leak AWS dependencies
into the CI disposable stack.

### Log bounds in the deployment Compose file

`compose.ec2.yaml` now sets a shared logging block on all eleven runtime
containers: `json-file` with `max-size: 10m`, `max-file: 3` (30 MB per
container, roughly 330 MB worst case) and `labels: com.docker.compose.service`.
Docker applies log options only at container creation, so the bounds take
effect when the next deployment recreates the containers. Local development
keeps the unbounded defaults. Every line is a Docker JSON envelope
(`{"log","stream","time","attrs",...}`) whose `attrs` field carries the compose
service name, keeping lines self-describing in CloudWatch. Gateway access logs
stay off; its stderr warnings reach CloudWatch through the same files.

### CloudWatch agent configuration

`scripts/amazon-cloudwatch-agent.json` ships in the repository and reaches
`/opt/foc/scripts/` through the normal release fast-forward. It publishes:

| Data | Detail |
| --- | --- |
| Container logs | `/var/lib/docker/containers/*/*-json.log` into log group `/foc/app`, one stream per container log file, seven-day retention |
| Memory | `mem_used_percent` in namespace `CWAgent`, dimension `InstanceId` |
| Root disk | `disk_used_percent` for mount `/` only, `drop_device` keeps dimensions stable across Nitro reboots |

`publish_multi_logs: true` is required for the wildcard path: without it the
agent ships only the most recently modified matching file, silently dropping
the other ten containers. The log envelope is stored verbatim in CloudWatch;
Logs Insights can unwrap it with
`parse @message '{"log": *, "stream": *, "time": *, "attrs": *}' as line, stream, time, attrs`.

### One-time agent installation on EC2

Run after a release that includes the agent configuration (so the file exists
under `/opt/foc/scripts/`). Send through SSM as root:

```sh
aws ssm send-command \
  --profile nus-foc --region ap-southeast-1 \
  --instance-ids i-0ceabdd31ee0f7594 \
  --document-name AWS-RunShellScript \
  --parameters commands='
set -eu
cd /opt/foc
curl -fsSLo /tmp/amazon-cloudwatch-agent.deb \
  https://amazoncloudwatch-agent.s3.amazonaws.com/ubuntu/amd64/latest/amazon-cloudwatch-agent.deb
dpkg -i /tmp/amazon-cloudwatch-agent.deb
/opt/aws/amazon-cloudwatch-agent/bin/amazon-cloudwatch-agent-ctl -a fetch-config -m ec2 \
  -c file:/opt/foc/scripts/amazon-cloudwatch-agent.json -s
' --query 'Command.CommandId' --output text
```

Check progress with `aws ssm get-command-invocation` and verify with
`.../amazon-cloudwatch-agent-ctl -a status`. The instance role needs
`CloudWatchAgentServerPolicy`; attach it first (next section).

### IAM, notifications and alarms

Run these from a laptop with the AWS CLI. The IAM and SNS steps can happen at
any time; the alarms need the agent installed and publishing first.

The agent authenticates through the instance role, exactly like ECR pulls do.
`CloudWatchAgentServerPolicy` is the AWS-managed policy covering
`cloudwatch:PutMetricData`, log group/stream creation, `PutLogEvents`,
`PutRetentionPolicy` and SSM parameter reads. Find the role behind the instance
profile and attach it:

```sh
aws ec2 describe-instances --profile nus-foc --region ap-southeast-1 \
  --instance-ids i-0ceabdd31ee0f7594 \
  --query 'Reservations[0].Instances[0].IamInstanceProfile.Arn' --output text
# The role name is the last path segment of that instance-profile ARN.
aws iam attach-role-policy --profile nus-foc \
  --role-name <role-name> \
  --policy-arn arn:aws:iam::aws:policy/CloudWatchAgentServerPolicy
```

Alarms need somewhere to notify. The `foc-demo-alarms` SNS topic is that
destination; the email subscription fans out to an inbox. AWS sends a
confirmation email that must be accepted before notifications are delivered:

```sh
aws sns create-topic --profile nus-foc --region ap-southeast-1 --name foc-demo-alarms
aws sns subscribe --profile nus-foc --region ap-southeast-1 \
  --topic-arn <topic-arn> --protocol email --notification-endpoint <email>
aws sns list-subscriptions-by-topic --profile nus-foc --region ap-southeast-1 \
  --topic-arn <topic-arn>   # Status flips to Confirmed after accepting
```

After the agent publishes its first data points, create the alarms. A CloudWatch
alarm targets one metric plus an exact dimension set, and dimension sets must
match what the agent actually publishes — list them instead of guessing:

```sh
aws cloudwatch list-metrics --profile nus-foc --region ap-southeast-1 \
  --namespace CWAgent
```

| Alarm | Condition | Detection window |
| --- | --- | --- |
| `foc-demo-memory-high` | `mem_used_percent` > 85 | 2 × 5 min |
| `foc-demo-disk-high` | root `disk_used_percent` > 80 | 2 × 5 min |
| `foc-demo-instance-status-check` | `StatusCheckFailed` > 0 | 3 × 1 min |

```sh
aws cloudwatch put-metric-alarm --profile nus-foc --region ap-southeast-1 \
  --alarm-name foc-demo-memory-high \
  --alarm-description "FoC EC2 memory above 85% for 10 minutes" \
  --namespace CWAgent --metric-name mem_used_percent --statistic Average \
  --dimensions Name=InstanceId,Value=i-0ceabdd31ee0f7594 \
  --period 300 --evaluation-periods 2 --threshold 85 \
  --comparison-operator GreaterThanThreshold --alarm-actions <topic-arn>

# Copy the full dimension set of the disk metric (InstanceId, path, fstype,
# partition) from list-metrics output:
aws cloudwatch put-metric-alarm --profile nus-foc --region ap-southeast-1 \
  --alarm-name foc-demo-disk-high \
  --alarm-description "FoC EC2 root disk above 80% for 10 minutes" \
  --namespace CWAgent --metric-name disk_used_percent --statistic Average \
  --dimensions <disk dimensions from list-metrics> \
  --period 300 --evaluation-periods 2 --threshold 80 \
  --comparison-operator GreaterThanThreshold --alarm-actions <topic-arn>

aws cloudwatch put-metric-alarm --profile nus-foc --region ap-southeast-1 \
  --alarm-name foc-demo-instance-status-check \
  --alarm-description "FoC EC2 status checks failing" \
  --namespace AWS/EC2 --metric-name StatusCheckFailed --statistic Maximum \
  --dimensions Name=InstanceId,Value=i-0ceabdd31ee0f7594 \
  --period 60 --evaluation-periods 3 --threshold 0 \
  --comparison-operator GreaterThanThreshold --alarm-actions <topic-arn>
```

New alarms start in `INSUFFICIENT_DATA` and settle to `OK` once enough
datapoints exist. Missing data is treated as missing, so a stopped agent leaves
alarms in `INSUFFICIENT_DATA` rather than firing them.

### Cost and teardown

The agent itself is free. Charges are log ingestion and seven-day storage,
two custom metrics and three standard alarms — typically a few dollars per
month at this traffic. For teardown, delete the alarms and the SNS topic; the
`/foc/app` log group expires by its own seven-day retention.

### Verification

1. After the next deployment, `docker inspect` any container and confirm the
   `LogConfig` shows `max-size=10m`.
2. `aws logs tail /foc/app --follow` shows one stream per container with
   service names in `attrs`.
3. `aws cloudwatch list-metrics --namespace CWAgent` shows both metrics.
4. Alarms leave `INSUFFICIENT_DATA` after roughly ten minutes of data.
