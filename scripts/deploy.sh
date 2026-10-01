#!/usr/bin/env bash
set -euo pipefail

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "$ROOT_DIR"

_STEP="startup"
_on_exit() { local c=$?; [[ $c -ne 0 ]] && printf '\n[deploy.sh] ABORTED (exit %d) at step: %s\n' "$c" "$_STEP" >&2; }
trap _on_exit EXIT

AWS_REGION="us-east-1"
SERVICE_NAME="go-dash-backend"
ECR_REPO="go-dash-backend"
CODEBUILD_PROJECT="go-dash-backend-build"
ENV_FILE="$ROOT_DIR/.env.aws"
DATABASE_URL=""
_local_running=0

lsof -ti:8080 >/dev/null 2>&1 && _local_running=1 || true

_shasum() { shasum -a 256 "$@" 2>/dev/null || sha256sum "$@" 2>/dev/null; }

printf '\n=== go-dashboard-backend ===\n\n'
printf '  [1] Local  — Go server on localhost + local Postgres (no cost)'
(( _local_running )) && printf ' [running]' || printf ' [not detected]'
printf '\n'
printf '  [2] AWS    — App Runner · Neon · scales to zero · ~$0/mo at idle\n'
printf '\nChoice [1/2, default 2]: '
read -r _MODE
case "${_MODE:-2}" in
  1) _TARGET="local" ;;
  *) _TARGET="remote" ;;
esac

if [[ "$_TARGET" == "local" ]]; then
  _STEP="local"
  command -v go >/dev/null 2>&1 || { printf 'Go not found — install from https://go.dev/dl/\n' >&2; exit 1; }
  if [[ ! -f "$ROOT_DIR/.env" ]]; then
    [[ -f "$ROOT_DIR/.env.example" ]] && cp "$ROOT_DIR/.env.example" "$ROOT_DIR/.env" \
      || { printf '.env not found and no .env.example — create %s/.env first\n' "$ROOT_DIR" >&2; exit 1; }
    printf '  Created .env from .env.example — edit DB credentials if needed.\n'
  fi
  lsof -ti:8080 >/dev/null 2>&1 && {
    printf '  Port 8080 in use — killing existing process...\n'
    kill $(lsof -ti:8080) 2>/dev/null || true
    sleep 1
  }
  printf '\nStarting Go server on :8080...\n'
  go run "$ROOT_DIR/cmd/server"
  exit 0
fi

_STEP="aws auth"
command -v aws >/dev/null 2>&1 || { printf 'aws CLI not found — install: https://docs.aws.amazon.com/cli/latest/userguide/install-cliv2.html\n' >&2; exit 1; }
aws sts get-caller-identity >/dev/null 2>&1 || { printf 'AWS credentials not configured — run: aws configure\n' >&2; exit 1; }
ACCOUNT_ID="$(aws sts get-caller-identity --query Account --output text)"
printf 'Auth: account %s  region %s\n' "$ACCOUNT_ID" "$AWS_REGION"

_STEP="db config"
_saved_url=""
[[ -f "$ENV_FILE" ]] && _saved_url=$(grep -E '^DATABASE_URL=' "$ENV_FILE" | cut -d= -f2- | tr -d '"' || true)
if [[ -z "$_saved_url" ]]; then
  _repo_root="$(cd "$ROOT_DIR/../.." && pwd)"
  _saved_url=$(find "$_repo_root" -name '.env*' -not -name '*.example' -type f 2>/dev/null \
    | xargs grep -h -E '^(NEON_)?DATABASE_URL=' 2>/dev/null \
    | grep 'neon\.tech' | head -1 | cut -d= -f2- | tr -d '"' || true)
fi

if [[ -n "$_saved_url" ]]; then
  printf '\n  Use saved db creds (Y/n): '
  read -r _USE_SAVED
  case "${_USE_SAVED:-Y}" in
    [Nn]*) printf '  Enter Neon DATABASE_URL:\n  > '; read -r DATABASE_URL ;;
    *) DATABASE_URL="$_saved_url" ;;
  esac
else
  printf '\n  Enter Neon DATABASE_URL\n'
  printf '  (postgresql://user:pass@ep-xxx.neon.tech/dbname?sslmode=require):\n  > '
  read -r DATABASE_URL
  [[ -n "$DATABASE_URL" ]] || { printf 'DATABASE_URL is required.\n' >&2; exit 1; }
fi

_STEP="db preflight"
_conn=$(psql "$DATABASE_URL" -t -c 'SELECT 1;' 2>/dev/null | tr -d ' \n' || printf '')
[[ "$_conn" == "1" ]] || { printf 'Cannot connect to Neon DB — check URL.\n' >&2; exit 1; }
printf 'DB connection: OK\n'

_STEP="ecr repo"
aws ecr describe-repositories --repository-names "$ECR_REPO" --region "$AWS_REGION" >/dev/null 2>&1 || {
  printf '  Creating ECR repo %s...\n' "$ECR_REPO"
  aws ecr create-repository --repository-name "$ECR_REPO" --region "$AWS_REGION" >/dev/null
}

TAG=$(find "$ROOT_DIR/cmd" "$ROOT_DIR/internal" "$ROOT_DIR/migrations" "$ROOT_DIR/Dockerfile" "$ROOT_DIR/go.mod" \
    "$ROOT_DIR/buildspec.yml" \
    -type f 2>/dev/null | sort | xargs cat 2>/dev/null \
  | _shasum | cut -c1-16 || true)
TAG="${TAG:-$(date +%Y%m%d%H%M%S)}"
IMAGE="${ACCOUNT_ID}.dkr.ecr.${AWS_REGION}.amazonaws.com/${ECR_REPO}:${TAG}"
IMAGE_CACHE="${ACCOUNT_ID}.dkr.ecr.${AWS_REGION}.amazonaws.com/${ECR_REPO}:cache"
ECR_URI="${ACCOUNT_ID}.dkr.ecr.${AWS_REGION}.amazonaws.com"

_img_exists() {
  aws ecr describe-images --repository-name "$ECR_REPO" --image-ids "imageTag=$1" \
    --region "$AWS_REGION" >/dev/null 2>&1
}

if _img_exists "$TAG"; then
  printf '  Image %s exists — skipping build.\n' "$TAG"
else
  _STEP="codebuild iam role"
  CB_ROLE="go-dash-codebuild-role"
  CB_ROLE_ARN=$(aws iam get-role --role-name "$CB_ROLE" --query 'Role.Arn' --output text 2>/dev/null || true)
  if [[ -z "$CB_ROLE_ARN" || "$CB_ROLE_ARN" == "None" ]]; then
    printf '  Creating CodeBuild IAM role...\n'
    CB_ROLE_ARN=$(aws iam create-role --role-name "$CB_ROLE" \
      --assume-role-policy-document '{"Version":"2012-10-17","Statement":[{"Effect":"Allow","Principal":{"Service":"codebuild.amazonaws.com"},"Action":"sts:AssumeRole"}]}' \
      --query 'Role.Arn' --output text)
    aws iam attach-role-policy --role-name "$CB_ROLE" \
      --policy-arn arn:aws:iam::aws:policy/AmazonEC2ContainerRegistryPowerUser
    aws iam attach-role-policy --role-name "$CB_ROLE" \
      --policy-arn arn:aws:iam::aws:policy/AmazonS3ReadOnlyAccess
    printf '  Waiting for IAM propagation...\n'
    sleep 10
  fi
  aws iam put-role-policy --role-name "$CB_ROLE" \
    --policy-name CodeBuildLogs \
    --policy-document "{\"Version\":\"2012-10-17\",\"Statement\":[{\"Effect\":\"Allow\",\"Action\":[\"logs:CreateLogGroup\",\"logs:CreateLogStream\",\"logs:PutLogEvents\"],\"Resource\":\"arn:aws:logs:${AWS_REGION}:${ACCOUNT_ID}:log-group:/aws/codebuild/*\"}]}"
  aws iam put-role-policy --role-name "$CB_ROLE" \
    --policy-name CodeBuildECRPublic \
    --policy-document '{"Version":"2012-10-17","Statement":[{"Effect":"Allow","Action":["ecr-public:GetAuthorizationToken","sts:GetServiceBearerToken"],"Resource":"*"}]}'

  _STEP="s3 source bucket"
  SRC_BUCKET="go-dash-codebuild-src-${ACCOUNT_ID}"
  aws s3api head-bucket --bucket "$SRC_BUCKET" 2>/dev/null || {
    printf '  Creating S3 source bucket %s...\n' "$SRC_BUCKET"
    if [[ "$AWS_REGION" == "us-east-1" ]]; then
      aws s3api create-bucket --bucket "$SRC_BUCKET" --region "$AWS_REGION" >/dev/null
    else
      aws s3api create-bucket --bucket "$SRC_BUCKET" --region "$AWS_REGION" \
        --create-bucket-configuration LocationConstraint="$AWS_REGION" >/dev/null
    fi
  }

  _STEP="codebuild project"
  _CB_EXISTS=$(aws codebuild batch-get-projects --names "$CODEBUILD_PROJECT" \
    --query 'projects[0].name' --output text 2>/dev/null || true)
  _cb_source="{\"type\":\"S3\",\"location\":\"${SRC_BUCKET}/go-dash-backend-source.zip\"}"
  _cb_env="{\"type\":\"LINUX_CONTAINER\",\"image\":\"aws/codebuild/standard:7.0\",\"computeType\":\"BUILD_GENERAL1_SMALL\",\"privilegedMode\":true,\"environmentVariables\":[]}"
  if [[ "$_CB_EXISTS" == "None" || -z "$_CB_EXISTS" ]]; then
    printf '  Creating CodeBuild project %s...\n' "$CODEBUILD_PROJECT"
    aws codebuild create-project \
      --name "$CODEBUILD_PROJECT" \
      --source "$_cb_source" \
      --artifacts '{"type":"NO_ARTIFACTS"}' \
      --environment "$_cb_env" \
      --service-role "$CB_ROLE_ARN" \
      --region "$AWS_REGION" >/dev/null
  else
    aws codebuild update-project --name "$CODEBUILD_PROJECT" --source "$_cb_source" --region "$AWS_REGION" >/dev/null
  fi

  _STEP="image build"
  _tmpzip="${TMPDIR:-/tmp}/go-dash-be-src-$$.zip"
  printf 'Packaging source...\n'
  (cd "$ROOT_DIR" && zip -qr "$_tmpzip" . -x '.git/*' -x '.env*' -x '*.zip')
  printf 'Uploading source to S3...\n'
  aws s3 cp "$_tmpzip" "s3://${SRC_BUCKET}/go-dash-backend-source.zip" >/dev/null
  rm -f "$_tmpzip"

  printf 'Starting CodeBuild build...\n'
  BUILD_ID=$(aws codebuild start-build \
    --project-name "$CODEBUILD_PROJECT" \
    --environment-variables-override \
      "[{\"name\":\"ECR_URI\",\"value\":\"${ECR_URI}\"},{\"name\":\"IMAGE\",\"value\":\"${IMAGE}\"},{\"name\":\"IMAGE_CACHE\",\"value\":\"${IMAGE_CACHE}\"}]" \
    --region "$AWS_REGION" \
    --query 'build.id' --output text)
  printf '  Build ID: %s\n' "$BUILD_ID"

  _cb_elapsed=0
  while true; do
    _STATUS=$(aws codebuild batch-get-builds --ids "$BUILD_ID" \
      --query 'builds[0].buildStatus' --output text --region "$AWS_REGION")
    case "$_STATUS" in
      SUCCEEDED) printf '  Build complete.\n'; break ;;
      FAILED|FAULT|STOPPED|TIMED_OUT) printf 'Build %s.\n' "$_STATUS" >&2; exit 1 ;;
    esac
    (( _cb_elapsed += 15 ))
    (( _cb_elapsed > 900 )) && { printf 'Build timed out after 15 min.\n' >&2; exit 1; }
    printf '  ...%ds (%s)\n' "$_cb_elapsed" "$_STATUS"
    sleep 15
  done
fi

_STEP="app runner ecr role"
AR_ECR_ROLE="go-dash-apprunner-ecr-role"
AR_ECR_ROLE_ARN=$(aws iam get-role --role-name "$AR_ECR_ROLE" --query 'Role.Arn' --output text 2>/dev/null || true)
if [[ -z "$AR_ECR_ROLE_ARN" || "$AR_ECR_ROLE_ARN" == "None" ]]; then
  printf '  Creating App Runner ECR access role...\n'
  AR_ECR_ROLE_ARN=$(aws iam create-role --role-name "$AR_ECR_ROLE" \
    --assume-role-policy-document '{"Version":"2012-10-17","Statement":[{"Effect":"Allow","Principal":{"Service":"build.apprunner.amazonaws.com"},"Action":"sts:AssumeRole"}]}' \
    --query 'Role.Arn' --output text)
  aws iam attach-role-policy --role-name "$AR_ECR_ROLE" \
    --policy-arn arn:aws:iam::aws:policy/service-role/AWSAppRunnerServicePolicyForECRAccess
  sleep 10
fi

_STEP="auto scaling config"
_asc_rows=$(aws apprunner list-auto-scaling-configurations \
  --auto-scaling-configuration-name "go-dash-scale-to-zero" \
  --region "$AWS_REGION" \
  --query 'AutoScalingConfigurationSummaryList[*].[AutoScalingConfigurationArn,Status,Latest]' \
  --output text 2>/dev/null || true)
_ASC_ARN=$(printf '%s\n' "$_asc_rows" | awk '$2=="ACTIVE" && $3=="True" {print $1; exit}')
[[ -z "$_ASC_ARN" ]] && _ASC_ARN=$(printf '%s\n' "$_asc_rows" | awk '$2=="ACTIVE" {print $1; exit}')
[[ -z "$_ASC_ARN" ]] && _ASC_ARN=$(printf '%s\n' "$_asc_rows" | awk 'NF {print $1; exit}')
if [[ -z "$_ASC_ARN" ]]; then
  printf '  Creating auto-scaling config (min=1, max=2)...\n'
  _ASC_ARN=$(aws apprunner create-auto-scaling-configuration \
    --auto-scaling-configuration-name "go-dash-scale-to-zero" \
    --min-size 1 --max-size 2 --max-concurrency 100 \
    --region "$AWS_REGION" \
    --query 'AutoScalingConfiguration.AutoScalingConfigurationArn' --output text)
fi

_STEP="app runner deploy"
_SVC_ARN=$(aws apprunner list-services --region "$AWS_REGION" \
  --query "ServiceSummaryList[?ServiceName=='${SERVICE_NAME}'].ServiceArn" \
  --output text 2>/dev/null | awk 'NF{print $1;exit}' || true)

_env_vars="{\"DATABASE_URL\":\"${DATABASE_URL}\",\"CORS_ORIGIN\":\"*\",\"MIGRATIONS_DIR\":\"/app/migrations\",\"BACKEND_RUNTIME\":\"go\",\"PORT\":\"8080\"}"
_source_config="{\"ImageRepository\":{\"ImageIdentifier\":\"${IMAGE}\",\"ImageConfiguration\":{\"Port\":\"8080\",\"RuntimeEnvironmentVariables\":${_env_vars}},\"ImageRepositoryType\":\"ECR\"},\"AuthenticationConfiguration\":{\"AccessRoleArn\":\"${AR_ECR_ROLE_ARN}\"},\"AutoDeploymentsEnabled\":false}"
_instance_config="{\"Cpu\":\"1024\",\"Memory\":\"2048\"}"

if [[ -z "$_SVC_ARN" ]]; then
  printf '\n=== creating App Runner service: %s ===\n' "$SERVICE_NAME"
  _SVC_ARN=$(aws apprunner create-service \
    --service-name "$SERVICE_NAME" \
    --source-configuration "$_source_config" \
    --instance-configuration "$_instance_config" \
    --auto-scaling-configuration-arn "$_ASC_ARN" \
    --region "$AWS_REGION" \
    --query 'Service.ServiceArn' --output text)
else
  printf '\n=== updating App Runner service: %s ===\n' "$SERVICE_NAME"
  aws apprunner update-service \
    --service-arn "$_SVC_ARN" \
    --source-configuration "$_source_config" \
    --instance-configuration "$_instance_config" \
    --auto-scaling-configuration-arn "$_ASC_ARN" \
    --region "$AWS_REGION" >/dev/null
fi

printf '  Waiting for service to reach RUNNING state...\n'
_ar_elapsed=0
while true; do
  _SVC_STATUS=$(aws apprunner describe-service --service-arn "$_SVC_ARN" \
    --region "$AWS_REGION" --query 'Service.Status' --output text)
  case "$_SVC_STATUS" in
    RUNNING) printf '  Service is RUNNING.\n'; break ;;
    CREATE_FAILED|UPDATE_FAILED|DELETE_FAILED) printf 'Service %s — check App Runner console.\n' "$_SVC_STATUS" >&2; exit 1 ;;
  esac
  (( _ar_elapsed += 15 ))
  (( _ar_elapsed > 600 )) && { printf 'Timed out waiting for App Runner (10 min).\n' >&2; exit 1; }
  printf '  ...%ds (%s)\n' "$_ar_elapsed" "$_SVC_STATUS"
  sleep 15
done

BACKEND_URL="https://$(aws apprunner describe-service --service-arn "$_SVC_ARN" \
  --region "$AWS_REGION" --query 'Service.ServiceUrl' --output text)"

printf '\nWriting %s...\n' "$ENV_FILE"
cat > "$ENV_FILE" <<ENVEOF
AWS_REGION=${AWS_REGION}
DATABASE_URL=${DATABASE_URL}
BACKEND_URL=${BACKEND_URL}
SERVICE_NAME=${SERVICE_NAME}
SERVICE_ARN=${_SVC_ARN}
ENVEOF

printf '\nDone. Backend URL:\n  %s\n' "$BACKEND_URL"
