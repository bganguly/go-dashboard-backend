#!/usr/bin/env bash
set -euo pipefail

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "$ROOT_DIR"

_STEP="startup"
_on_exit() { local c=$?; [[ $c -ne 0 ]] && printf '\n[deploy.sh] ABORTED (exit %d) at step: %s\n' "$c" "$_STEP" >&2; }
trap _on_exit EXIT

_TARGET=""
DEPLOY_MODE=""
GCP_PROJECT=""
GCP_REGION="us-central1"
IMAGE=""
BACKEND_URL=""
ACTIVE_ACCOUNT=""
BACKEND_PID=""
USE_NEON="true"
NEON_DATABASE_URL=""
_local_running=0
SERVICE_NAME=""
ENV_FILE=""

lsof -ti:8080 >/dev/null 2>&1 && _local_running=1 || true

_shasum() { shasum -a 256 "$@" 2>/dev/null || sha256sum "$@" 2>/dev/null; }

printf '\n=== go-dashboard-backend ===\n\n'
printf '  [1] Local  — Go server on localhost + local Postgres (no GCP cost)'
(( _local_running )) && printf ' [running]' || printf ' [not detected]'
printf '\n'
printf '  [2] Lite   — GCP: Cloud Run · 4M rows · scales to zero · minimal cost\n'
printf '  [3] Full   — GCP: Cloud Run · 4M rows · always warm · considerable cost\n'
printf '\nChoice [1/2/3, default 2]: '
read -r _MODE
case "${_MODE:-2}" in
  3) _TARGET="remote"; DEPLOY_MODE="full"  ;;
  2) _TARGET="remote"; DEPLOY_MODE="lite"  ;;
  *) _TARGET="local";  DEPLOY_MODE=""      ;;
esac

if [[ "$_TARGET" == "remote" ]]; then
  SERVICE_NAME="go-dash-${DEPLOY_MODE}-backend"
  ENV_FILE="$ROOT_DIR/.env.gcp.${DEPLOY_MODE}"
  [[ -f "$ENV_FILE" ]] && source "$ENV_FILE"
fi

if [[ "$_TARGET" == "local" ]]; then
  _STEP="local"
  command -v go >/dev/null 2>&1 || { printf 'Go not found — install from https://go.dev/dl/\n' >&2; exit 1; }

  if [[ ! -f "$ROOT_DIR/.env" ]]; then
    [[ -f "$ROOT_DIR/.env.example" ]] && cp "$ROOT_DIR/.env.example" "$ROOT_DIR/.env" \
      || { printf '.env not found and no .env.example to copy — create %s/.env first\n' "$ROOT_DIR" >&2; exit 1; }
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

_STEP="gcloud auth"
if ! command -v gcloud >/dev/null 2>&1; then
  printf '\ngcloud CLI not found.\n'
  command -v brew >/dev/null 2>&1 && {
    brew install --cask google-cloud-sdk
    source "$(brew --prefix)/share/google-cloud-sdk/path.bash.inc" 2>/dev/null || true
  } || { printf 'Install: https://cloud.google.com/sdk/docs/install\n'; exit 1; }
fi

ACTIVE_ACCOUNT=$(gcloud auth list --filter=status:ACTIVE --format="value(account)" 2>/dev/null | head -1 || true)
if [[ -z "$ACTIVE_ACCOUNT" ]]; then
  printf '\nNot authenticated — logging in...\n'
  gcloud auth login
  ACTIVE_ACCOUNT=$(gcloud auth list --filter=status:ACTIVE --format="value(account)" 2>/dev/null | head -1 || true)
  [[ -n "$ACTIVE_ACCOUNT" ]] || { printf 'Login failed.\n' >&2; exit 1; }
fi

_CONFIG_PROJECT=$(gcloud config get-value project 2>/dev/null || true)
GCP_PROJECT="${_CONFIG_PROJECT:-${GCP_PROJECT:-}}"
[[ -n "$GCP_PROJECT" ]] || {
  printf '\nNo GCP project detected. Run: gcloud config set project <id>\n' >&2
  exit 1
}
_CONFIG_REGION=$(gcloud config get-value compute/region 2>/dev/null || true)
GCP_REGION="${_CONFIG_REGION:-${GCP_REGION:-us-central1}}"
printf 'Auth: %s  Project: %s  Region: %s\n' "$ACTIVE_ACCOUNT" "$GCP_PROJECT" "$GCP_REGION"

_STEP="db prompt"
_saved_neon_url=""
_saved_use_neon=""
if [[ -f "$ENV_FILE" ]]; then
  _saved_use_neon=$(grep -E '^USE_NEON=' "$ENV_FILE" | cut -d= -f2- | tr -d '"' || true)
  _saved_neon_url=$(grep -E '^NEON_DATABASE_URL=' "$ENV_FILE" | cut -d= -f2- | tr -d '"' || true)
fi

if [[ -n "$_saved_use_neon" ]]; then
  USE_NEON="$_saved_use_neon"
  NEON_DATABASE_URL="$_saved_neon_url"
  if [[ "$USE_NEON" == "true" ]]; then
    printf '\n  Database: Neon (%s...)  [saved]\n' "${NEON_DATABASE_URL:0:40}"
  else
    printf '\n  Database: local/other  [saved]\n'
  fi
  printf '  Continue with saved? [Y/n]: '
  read -r _REPLACE
  case "${_REPLACE:-Y}" in
    [Nn]*)
      printf '  Enter new Neon DATABASE_URL\n  > '
      read -r _NEW_URL
      [[ -n "$_NEW_URL" ]] && { NEON_DATABASE_URL="$_NEW_URL"; USE_NEON="true"; }
      ;;
  esac
else
  printf '\n  Database backend:\n'
  printf '  [Y] Neon serverless Postgres  — free tier, auto-suspends (~$0/mo)\n'
  printf '  [N] DATABASE_URL env var       — supply your own connection string\n'
  printf '\nUse Neon? [Y/n]: '
  read -r _NEON
  case "${_NEON:-Y}" in
    [Nn]*) USE_NEON="false" ;;
    *)     USE_NEON="true"  ;;
  esac
  if [[ "$USE_NEON" == "true" ]]; then
    printf '  Enter your Neon DATABASE_URL\n'
    printf '  (postgresql://user:pass@ep-xxx.neon.tech/dbname?sslmode=require):\n  > '
    read -r NEON_DATABASE_URL
    [[ -n "$NEON_DATABASE_URL" ]] || { printf 'Neon URL is required.\n'; exit 1; }
  else
    printf '  Enter DATABASE_URL:\n  > '
    read -r NEON_DATABASE_URL
    [[ -n "$NEON_DATABASE_URL" ]] || { printf 'DATABASE_URL is required.\n'; exit 1; }
  fi
fi

if [[ "$USE_NEON" == "true" ]]; then
  _STEP="db preflight"
  _conn=$(psql "$NEON_DATABASE_URL" -t -c 'SELECT 1;' 2>/dev/null | tr -d ' \n' || printf '')
  [[ "$_conn" == "1" ]] || { printf 'Cannot connect to Neon DB — check URL.\n' >&2; exit 1; }
  printf 'DB connection: OK\n'
fi

_STEP="image build"

ar_state=$(gcloud services list --project="$GCP_PROJECT" \
  --filter="name:artifactregistry.googleapis.com" --format="value(state)" 2>/dev/null || true)
[[ "$ar_state" != "ENABLED" ]] && gcloud services enable artifactregistry.googleapis.com --project="$GCP_PROJECT"

REGISTRY="go-dash-${DEPLOY_MODE}-repo"
if ! gcloud artifacts repositories describe "$REGISTRY" \
    --project="$GCP_PROJECT" --location="$GCP_REGION" >/dev/null 2>&1; then
  printf '  Creating Artifact Registry repo "%s"...\n' "$REGISTRY"
  gcloud artifacts repositories create "$REGISTRY" \
    --repository-format=docker --location="$GCP_REGION" --project="$GCP_PROJECT"
fi

TAG=$(find "$ROOT_DIR/cmd" "$ROOT_DIR/internal" "$ROOT_DIR/Dockerfile" "$ROOT_DIR/go.mod" \
    -type f 2>/dev/null | sort | xargs cat 2>/dev/null \
  | _shasum | cut -c1-16 || true)
TAG="${TAG:-$(date +%Y%m%d%H%M%S)}"
IMAGE="${GCP_REGION}-docker.pkg.dev/${GCP_PROJECT}/${REGISTRY}/backend:${TAG}"

_IMG_EXISTS=$(gcloud artifacts docker tags list \
  "${GCP_REGION}-docker.pkg.dev/${GCP_PROJECT}/${REGISTRY}/backend" \
  --filter="tag=${TAG}" --format="value(tag)" \
  --project "$GCP_PROJECT" 2>/dev/null | head -1 || true)

if [[ -n "$_IMG_EXISTS" ]]; then
  printf '  Image %s exists — skipping build.\n' "$TAG"
else
  printf 'Building via Cloud Build: %s\n' "$IMAGE"
  gcloud services enable cloudbuild.googleapis.com --project "$GCP_PROJECT"

  _CB_ROLE=$(gcloud projects get-iam-policy "$GCP_PROJECT" \
    --flatten="bindings[].members" \
    --filter="bindings.members:user:${ACTIVE_ACCOUNT} AND (bindings.role:roles/cloudbuild OR bindings.role:roles/owner OR bindings.role:roles/editor)" \
    --format="value(bindings.role)" 2>/dev/null | head -1 || true)
  if [[ -z "$_CB_ROLE" ]]; then
    gcloud projects add-iam-policy-binding "$GCP_PROJECT" \
      --member="user:${ACTIVE_ACCOUNT}" --role="roles/cloudbuild.builds.editor" --quiet
  fi

  _cache_tag="${IMAGE%:*}:cache"
  _tmpyaml=$(mktemp /private/tmp/cloudbuild.XXXXXX)
  cat > "$_tmpyaml" <<YAML
steps:
- name: 'gcr.io/cloud-builders/docker'
  entrypoint: bash
  args:
  - -c
  - |
    docker pull '${_cache_tag}' 2>/dev/null || true
    docker build --cache-from '${_cache_tag}' -t '${IMAGE}' -t '${_cache_tag}' .
- name: 'gcr.io/cloud-builders/docker'
  args: [push, '${IMAGE}']
- name: 'gcr.io/cloud-builders/docker'
  args: [push, '${_cache_tag}']
images:
- '${IMAGE}'
- '${_cache_tag}'
YAML

  _attempt=0 _rc=0
  while (( _attempt < 3 )); do
    _attempt=$(( _attempt + 1 ))
    set +e; gcloud builds submit --config "$_tmpyaml" --project "$GCP_PROJECT" "$ROOT_DIR"; _rc=$?; set -e
    [[ "$_rc" == "0" ]] && { rm -f "$_tmpyaml"; break; }
    [[ "$_rc" == "130" ]] && { printf '\nBuild cancelled.\n'; rm -f "$_tmpyaml"; exit 130; }
    (( _attempt < 3 )) && { printf '  Cloud Build failed (attempt %d/3) — waiting 20s...\n' "$_attempt"; sleep 20; }
  done
  rm -f "$_tmpyaml"
  [[ "$_rc" != "0" ]] && { printf 'Cloud Build failed after 3 attempts.\n' >&2; exit 1; }
fi

_STEP="cloud run deploy"
gcloud services enable run.googleapis.com --project "$GCP_PROJECT"

DATABASE_URL_ARG="$NEON_DATABASE_URL"

if [[ "$DEPLOY_MODE" == "lite" ]]; then
  _MIN_INST=0; _MAX_INST=1; _MEM="512Mi"; _CPU=1
else
  _MIN_INST=0; _MAX_INST=5; _MEM="1Gi"; _CPU=2
fi

printf '\n=== deploying Cloud Run service: %s ===\n' "$SERVICE_NAME"
gcloud run deploy "$SERVICE_NAME" \
  --image "$IMAGE" \
  --region "$GCP_REGION" \
  --project "$GCP_PROJECT" \
  --platform managed \
  --allow-unauthenticated \
  --min-instances "$_MIN_INST" \
  --max-instances "$_MAX_INST" \
  --memory "$_MEM" \
  --cpu "$_CPU" \
  --port 8080 \
  --set-env-vars "DATABASE_URL=${DATABASE_URL_ARG},CORS_ORIGIN=*,MIGRATIONS_DIR=/app/migrations,BACKEND_RUNTIME=go"

BACKEND_URL=$(gcloud run services describe "$SERVICE_NAME" \
  --region "$GCP_REGION" --project "$GCP_PROJECT" \
  --format="value(status.url)" 2>/dev/null || true)

printf '\nWriting %s...\n' "$ENV_FILE"
cat > "$ENV_FILE" <<ENVEOF
GCP_PROJECT=${GCP_PROJECT}
GCP_REGION=${GCP_REGION}
USE_NEON=${USE_NEON}
NEON_DATABASE_URL=${NEON_DATABASE_URL}
BACKEND_URL=${BACKEND_URL}
SERVICE_NAME=${SERVICE_NAME}
ENVEOF

printf '\nDone. Backend URL:\n  %s\n' "${BACKEND_URL:-<check Cloud Run console>}"
