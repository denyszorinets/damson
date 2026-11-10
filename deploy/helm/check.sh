#!/bin/sh
# Lints and renders the Damson chart with each values file in damson/ci/, and
# checks the failures the templates promise. Needs helm on PATH.
set -eu
chart="$(dirname "$0")/damson"
root="$(cd "$(dirname "$0")/../.." && pwd)"
rendered="$(mktemp -d)"
trap 'rm -rf "$rendered"' EXIT
fail() { echo "helm-check: $*" >&2; exit 1; }

helm lint "$chart" --strict -f "$chart/ci/minimal.yaml" >/dev/null
for f in "$chart"/ci/*.yaml; do
	echo "== $f"
	helm lint "$chart" --strict -f "$f" >/dev/null
	out="$(helm template check "$chart" -f "$f")"
	printf '%s\n' "$out" | grep -q 'kind: Deployment' || fail "$f renders no Deployment"
	# No credential ever lands in a ConfigMap: values come from Secret references.
	cfg="$(helm template check "$chart" -f "$f" -s templates/configmap.yaml)"
	printf '%s\n' "$cfg" | grep -iE '(password|secret|token|kek|role_id|dsn|session)[a-z_]* *= *"' | grep -qv '_file *=' && fail "$f puts a credential in the config"
	printf '%s\n' "$cfg" | sed -n '/config.toml: |/,$p' | sed '1d;s/^    //' > "$rendered/$(basename "$f" .yaml).toml"
	# Agents run job code: their ServiceAccount is not the controller's.
	ctl_sa="$(printf '%s\n' "$out" | awk -v RS='---' '/kind: Deployment/ && /component: controller/ {for(i=1;i<=NF;i++) if($i=="serviceAccountName:") {print $(i+1); exit}}')"
	agent_sa="$(printf '%s\n' "$out" | awk -v RS='---' '/kind: Deployment/ && /component: agent/ {for(i=1;i<=NF;i++) if($i=="serviceAccountName:") {print $(i+1); exit}}')"
	[ -n "$ctl_sa" ] && [ -n "$agent_sa" ] && [ "$ctl_sa" != "$agent_sa" ] || fail "$f: agents share the controller's ServiceAccount ($ctl_sa, $agent_sa)"
	# Every container drops all capabilities and has a read-only root.
	n_ctr="$(printf '%s\n' "$out" | grep -c '^        - name: \(controller\|agent\)$' || true)"
	n_ro="$(printf '%s\n' "$out" | grep -c 'readOnlyRootFilesystem: true' || true)"
	[ "$n_ctr" = "$n_ro" ] || fail "$f: $n_ctr containers but $n_ro read-only root file systems"
done

# The rendered configuration is one the controller accepts.
( cd "$root" && DAMSON_HELM_RENDERED="$rendered" go test -count=1 -run TestHelmRenderedConfig ./internal/config/ ) || fail "a rendered config.toml is refused by the controller"

# replicas 0 stays 0.
helm template check "$chart" -f "$chart/ci/openbao-token.yaml" | grep -A3 'name: check-damson-agent-empty$' | grep -q . || fail "pool with replicas 0 is missing"
helm template check "$chart" -f "$chart/ci/openbao-token.yaml" | awk '/name: check-damson-agent-empty$/{d=1} d&&/replicas:/{print $2; exit}' | grep -qx 0 || fail "replicas: 0 did not render as 0"

# Required values fail with a message that names them.
expect_fail() {
	msg="$1"; shift
	if out="$(helm template check "$chart" "$@" 2>&1)"; then fail "expected a failure containing: $msg"; fi
	printf '%s\n' "$out" | grep -q "$msg" || fail "failure lacks '$msg': $out"
}
expect_fail 'database.existingSecret is required'
expect_fail 'secrets.clusterSecret.existingSecret is required' --set database.existingSecret=x
expect_fail 'secrets.kek.existingSecret is required' --set database.existingSecret=x --set secrets.clusterSecret.existingSecret=y
expect_fail 'agents.controllerUrl is required' -f "$chart/ci/minimal.yaml" --set agents.controllerUrl=
expect_fail 'tokenSecret is required' -f "$chart/ci/minimal.yaml" --set 'agents.pools[0].tokenSecret='
expect_fail 'controller.replicas' -f "$chart/ci/minimal.yaml" --set controller.replicas=0
expect_fail 'secrets.backend' -f "$chart/ci/minimal.yaml" --set secrets.backend=vault
expect_fail 'oidc.clientId is required' -f "$chart/ci/minimal.yaml" --set oidc.issuer=https://idp
expect_fail 'used twice' -f "$chart/ci/minimal.yaml" --set 'agents.pools[1].name=default' --set 'agents.pools[1].tokenSecret=x'
expect_fail 'needs resources.requests.cpu' -f "$chart/ci/minimal.yaml" --set 'agents.pools[0].autoscaling.enabled=true'
expect_fail 'existingSecret is required for the approle method' -f "$chart/ci/minimal.yaml" --set secrets.backend=openbao --set secrets.openbao.address=https://b --set secrets.openbao.auth.method=approle
expect_fail 'Additional property replica' -f "$chart/ci/minimal.yaml" --set controller.replica=5
expect_fail 'agents.controllerUrl' -f "$chart/ci/minimal.yaml" --set agents.controllerUrl=http://x
echo "helm-check: ok"
