test_smoke_k8s() {
	if [ "$(skip 'test_smoke_k8s')" ]; then
		echo "==> TEST SKIPPED: k8s smoke tests"
		return
	fi

	set_verbosity

	echo "==> Checking for dependencies"
	check_dependencies juju

	file="${TEST_DIR}/test-smoke-k8s.log"
	echo "====> Logging to ${file}"
	bootstrap "test-smoke-k8s" "${file}"

	# Assert the controller consumes the final build-qualified QA operator
	# image before any model work runs (no-op without a QA registry path).
	assert_k8s_controller_image

	test_deploy "${file}"

	test_restart_resilience "${file}"

	destroy_controller "test-smoke-k8s"

	# This test destroys the controller and must run last.
	test_destroy_controller "${file}"
}
