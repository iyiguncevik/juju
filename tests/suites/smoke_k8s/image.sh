# Assertions for the QA operator-image registry path. When CI sets
# OPERATOR_IMAGE_ACCOUNT (the QA ECR Public repository namespace passed as
# caas-image-repo), the Kubernetes controller and the model operator must
# consume the final build-qualified amd64 operator image reference, never a
# staging tag or the default ghcr.io/juju fallback. Both assertions are
# no-ops when OPERATOR_IMAGE_ACCOUNT is empty, so local and
# production-registry runs are unaffected.

# qa_operator_image_ref echoes the final build-qualified operator image
# reference: <OPERATOR_IMAGE_ACCOUNT>/jujud-operator:<JUJU_VERSION>. The
# bootstrap passes the same build-qualified version via --agent-version, so
# the controller resolves exactly this tag for its own pod spec.
qa_operator_image_ref() {
	echo "${OPERATOR_IMAGE_ACCOUNT}/jujud-operator:$(juju_version)"
}

# assert_k8s_declared_image fails unless every container of the given
# workload, including its init containers, declares the expected operator
# image reference.
#
# ```
# assert_k8s_declared_image <namespace> <kind> <name>
# ```
assert_k8s_declared_image() {
	local namespace kind name expected declared
	namespace=${1}
	kind=${2}
	name=${3}
	expected="$(qa_operator_image_ref)"

	declared=$(kubectl get "${kind}" "${name}" -n "${namespace}" -o json |
		jq -r '[.spec.template.spec.containers[].image, (.spec.template.spec.initContainers[]? | .image)] | unique | .[]')
	if [[ -z ${declared} ]]; then
		echo "No containers declared in ${kind} ${namespace}/${name}" >&2
		return 1
	fi
	if [[ ${declared} != "${expected}" ]]; then
		echo "${kind} ${namespace}/${name} declares unexpected operator images:" >&2
		echo "${declared}" >&2
		echo "expected: ${expected}" >&2
		return 1
	fi
	echo "====> ${kind} ${namespace}/${name} declares ${expected}"
}

# assert_k8s_pod_digest_pinned_image fails unless the given pod resolved
# every container image, including completed init containers, to a
# digest-pinned image ID under the same repository as the expected
# reference.
#
# ```
# assert_k8s_pod_digest_pinned_image <namespace> <pod-name>
# ```
assert_k8s_pod_digest_pinned_image() {
	local namespace pod expected repo image_ids image_id
	namespace=${1}
	pod=${2}
	expected="$(qa_operator_image_ref)"
	repo="${expected%:*}"

	image_ids=$(kubectl get pod "${pod}" -n "${namespace}" -o json |
		jq -r '[.status.containerStatuses[].imageID, (.status.initContainerStatuses[]? | .imageID)] | unique | .[]')
	if [[ -z ${image_ids} ]]; then
		echo "No resolved image IDs on pod ${namespace}/${pod}" >&2
		return 1
	fi
	while read -r image_id; do
		[[ -z ${image_id} ]] && continue
		# Strip the runtime-specific scheme prefix ("docker-pullable://"
		# for the Docker runtime, absent for containerd) and require the
		# digest-pinned reference of the expected repository.
		image_id=${image_id#*://}
		if [[ ${image_id} != "${repo}"@sha256:* ]]; then
			echo "Pod ${namespace}/${pod} resolved an unexpected image ID: ${image_id}" >&2
			echo "expected a digest under ${repo}" >&2
			return 1
		fi
	done <<<"${image_ids}"
	echo "====> Pod ${namespace}/${pod} is digest-pinned under ${repo}"
}

# assert_k8s_controller_image asserts that the bootstrapped controller's
# StatefulSet and its running pod use the final build-qualified operator
# image in the controller model namespace.
#
# ```
# assert_k8s_controller_image
# ```
assert_k8s_controller_image() {
	if [[ -z ${OPERATOR_IMAGE_ACCOUNT:-} ]]; then
		return 0
	fi

	local namespace pod
	namespace="controller-${BOOTSTRAPPED_JUJU_CTRL_NAME}"

	assert_k8s_declared_image "${namespace}" statefulset "controller"

	pod=$(kubectl get pods -n "${namespace}" \
		-l "app.kubernetes.io/name=controller" \
		-o jsonpath='{.items[0].metadata.name}')
	if [[ -z ${pod} ]]; then
		echo "Failed to find controller pod in namespace ${namespace}" >&2
		return 1
	fi
	assert_k8s_pod_digest_pinned_image "${namespace}" "${pod}"
}

# assert_k8s_modeloperator_image asserts that the model operator deployment
# and its running pod use the final build-qualified operator image in the
# given model's namespace.
#
# ```
# assert_k8s_modeloperator_image <model-name>
# ```
assert_k8s_modeloperator_image() {
	if [[ -z ${OPERATOR_IMAGE_ACCOUNT:-} ]]; then
		return 0
	fi

	local namespace pod
	namespace=${1:?model name required}

	assert_k8s_declared_image "${namespace}" deployment "modeloperator"

	pod=$(kubectl get pods -n "${namespace}" \
		-l "app.kubernetes.io/name=modeloperator" \
		-o jsonpath='{.items[0].metadata.name}')
	if [[ -z ${pod} ]]; then
		echo "Failed to find model operator pod in namespace ${namespace}" >&2
		return 1
	fi
	assert_k8s_pod_digest_pinned_image "${namespace}" "${pod}"
}
