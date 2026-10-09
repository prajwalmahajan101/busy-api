# Cloud load-test runbook (rung-5: does the list cache lift the 15K ceiling?)

Two-machine AWS run, mirroring T29 so the numbers compare apples-to-apples. The
single question: **T29 hit a 15K-rps ceiling because the un-cached `/items` list
saturated the 16-conn pool (`repo_ms` p95 104ms→747ms). We now cache the list.
Does 15K pass, and how far does the ladder go?**

Local pre-check (done 2026-10-07): at 2000 rps mixed `load.js`, list served from
cache, `repo_ms` p95/p99 = 0 (vs 4ms pre-cache). The cloud run measures the win
at the scale only cloud can reach (local tops out ~5K — see `local-tuning.md`).

## Fixed variables (keep identical to T29)

| Var | Value | Why |
|---|---|---|
| Instances | 2× `c6i.2xlarge` (8 vCPU, 16 GB) | same as T29 |
| Region/AZ | `ap-south-1`, one AZ, one VPC | sub-ms RTT, no cross-AZ cost |
| `DB_MAX_CONNS` | **16** | the T29 value — the whole point is whether the cache removes pool pressure *at the same pool size* |
| Seed | **100k** rows | exact T29 comparison; not the local 600k |
| Box 1 | API + Postgres (Docker) + Valkey (Docker) | co-located, as T29 |
| Box 2 | k6 generator | isolates the load gen |

Change exactly one thing vs T29 (the list cache); hold everything else.

## 0. Pre-flight — billing alarm FIRST (R24)

```bash
export AWS_REGION=ap-south-1
# $10 alarm on estimated charges (us-east-1 is where billing metrics live)
aws cloudwatch put-metric-alarm --alarm-name busyapi-loadtest-spend \
  --namespace AWS/Billing --metric-name EstimatedCharges \
  --dimensions Name=Currency,Value=USD \
  --statistic Maximum --period 21600 --evaluation-periods 1 \
  --threshold 10 --comparison-operator GreaterThanThreshold \
  --region us-east-1
```

Do not proceed without the alarm. Tear everything down the same day.

## 1. Network + security group

```bash
# Use the default VPC/subnet in ap-south-1 (or your existing one)
VPC=$(aws ec2 describe-vpcs --filters Name=isDefault,Values=true \
  --query 'Vpcs[0].VpcId' --output text)
SUBNET=$(aws ec2 describe-subnets --filters Name=vpc-id,Values=$VPC \
  --query 'Subnets[0].SubnetId' --output text)

# SG: SSH from your IP; all traffic between the two boxes (intra-SG)
SG=$(aws ec2 create-security-group --group-name busyapi-lt \
  --description "busy-api loadtest" --vpc-id $VPC --query GroupId --output text)
MYIP=$(curl -s https://checkip.amazonaws.com)
aws ec2 authorize-security-group-ingress --group-id $SG \
  --protocol tcp --port 22 --cidr ${MYIP}/32
# intra-SG: let the two instances talk on any port (API 8000, PG 5432, Valkey 6379)
aws ec2 authorize-security-group-ingress --group-id $SG \
  --protocol -1 --source-group $SG
```

## 2. Launch both instances

```bash
# Amazon Linux 2023 x86_64 AMI (SSM public parameter — no hard-coded AMI id)
AMI=$(aws ssm get-parameters --names \
  /aws/service/ami-amazon-linux-latest/al2023-ami-kernel-default-x86_64 \
  --query 'Parameters[0].Value' --output text)
KEY=busyapi-lt   # aws ec2 create-key-pair --key-name busyapi-lt --query KeyMaterial --output text > busyapi-lt.pem; chmod 600 busyapi-lt.pem

run() { aws ec2 run-instances --image-id $AMI --instance-type c6i.2xlarge \
  --key-name $KEY --security-group-ids $SG --subnet-id $SUBNET \
  --tag-specifications "ResourceType=instance,Tags=[{Key=Name,Value=$1}]" \
  --query 'Instances[0].InstanceId' --output text; }

API_ID=$(run busyapi-api)
K6_ID=$(run busyapi-k6)
aws ec2 wait instance-running --instance-ids $API_ID $K6_ID

# Private IP of box 1 (k6 targets this), public IPs for SSH
API_PRIV=$(aws ec2 describe-instances --instance-ids $API_ID \
  --query 'Reservations[0].Instances[0].PrivateIpAddress' --output text)
API_PUB=$(aws ec2 describe-instances --instance-ids $API_ID \
  --query 'Reservations[0].Instances[0].PublicIpAddress' --output text)
K6_PUB=$(aws ec2 describe-instances --instance-ids $K6_ID \
  --query 'Reservations[0].Instances[0].PublicIpAddress' --output text)
echo "API priv=$API_PRIV pub=$API_PUB ; k6 pub=$K6_PUB"
```

## 3. Box 1 — API + Postgres + Valkey

SSH in (`ssh -i busyapi-lt.pem ec2-user@$API_PUB`), then:

```bash
sudo dnf install -y git docker
sudo systemctl enable --now docker
sudo usermod -aG docker ec2-user   # re-login for the group to take effect
# Go (match the project toolchain)
sudo dnf install -y golang   # or fetch go1.24 tarball if dnf lags

git clone <your busy-api remote> && cd busy-api
git checkout feat/rung5-list-cache

# Postgres + Valkey via the repo compose (binds 5432/6379 on the box)
docker compose up -d
# raise FD + ports (see docs/local-tuning.md) on BOTH boxes
ulimit -n 65535
sudo sysctl -w net.ipv4.ip_local_port_range="1024 65535" net.ipv4.tcp_tw_reuse=1

# 100k seed (apples-to-apples with T29)
export DATABASE_URL='postgres://root:root_password@localhost:5432/busyapi?sslmode=disable'
make load-seed N=100000

# Run the API bound to all interfaces so box 2 can reach it
DB_MAX_CONNS=16 PORT=8000 \
  CACHE_ITEM_TTL_S=300 CACHE_L1_ENABLED=true CACHE_TTL_JITTER_PCT=10 \
  nohup go run ./cmd/server > api.log 2>&1 &
curl -s localhost:8000/ping
```

## 4. Box 2 — k6

SSH in (`ssh -i busyapi-lt.pem ec2-user@$K6_PUB`), then:

```bash
sudo dnf install -y git
# k6 (official binary)
sudo gpg --no-default-keyring --keyring /etc/pki/rpm-gpg/k6 \
  --keyserver keyserver.ubuntu.com --recv-keys ... # or:
sudo dnf install -y https://dl.k6.io/rpm/repo.rpm && sudo dnf install -y k6
# fallback: curl -L https://github.com/grafana/k6/releases/latest/download/k6-*-linux-amd64.tar.gz | tar xz

git clone <your busy-api remote> && cd busy-api && git checkout feat/rung5-list-cache
ulimit -n 65535
sudo sysctl -w net.ipv4.ip_local_port_range="1024 65535" net.ipv4.tcp_tw_reuse=1

export BASE_URL="http://<API_PRIV>:8000"   # the private IP from step 2
make load-smoke BASE_URL=$BASE_URL          # sanity: 1 VU, all green
```

## 5. Run the ladder

From box 2. Record a `docs/benchmark-logbook.md` row per step (p50/p95/p99,
err%, `server_repo_ms` p95, achieved rps, dropped/s). The hypothesis is the 15K
step.

```bash
make load BASE_URL=$BASE_URL RPS=10000 VUS=2000 DURATION=30s   # confirm no regression vs T29
make load BASE_URL=$BASE_URL RPS=15000 VUS=4000 DURATION=30s   # THE TEST — T29 broke here
# only if 15K passes (p95 < budget, repo_ms flat):
make load BASE_URL=$BASE_URL RPS=30000 VUS=8000 DURATION=30s
```

Read `server_repo_ms` first: if the list cache worked, it stays low at 15K
(T29 showed it exploding to p95=747ms). If `repo_ms` is flat but p95 still
climbs, the next bottleneck has moved off the DB (CPU, Valkey, or the box) —
note which and stop; that defines the next rung.

Watch box 1 during the run: `docker stats`, `top`, and `api.log` for pool-wait
or breaker-open lines.

## 6. Teardown + cost (same day)

```bash
aws ec2 terminate-instances --instance-ids $API_ID $K6_ID
aws ec2 wait instance-terminated --instance-ids $API_ID $K6_ID
aws ec2 delete-security-group --group-id $SG
# record actual cost in the logbook row:
#   2× c6i.2xlarge ap-south-1 ≈ $0.34/hr each → ~$0.68/hr total
aws ce get-cost-and-usage --time-period Start=$(date +%F),End=$(date -d +1day +%F) \
  --granularity DAILY --metrics UnblendedCost --region us-east-1
```

Then delete the `$10` alarm (`aws cloudwatch delete-alarms --alarm-names
busyapi-loadtest-spend --region us-east-1`) and the key pair if done.

## Expected outcomes

- **15K passes, `repo_ms` flat** → list cache fixed the ceiling; climb the ladder
  (30K+) until the next resource binds. Record the new ceiling + what bound it.
- **15K still breaks on `repo_ms`** → the cache isn't catching cloud's list mix
  (unlikely — local pre-check showed repo=0); investigate the key/version before
  blaming the DB tier.
- **15K passes but p95 breaks elsewhere** → bottleneck moved (CPU/Valkey/LB). That
  is the rung-6 trigger, not a list-cache problem.
