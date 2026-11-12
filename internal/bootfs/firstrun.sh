#!/bin/bash
# Meteor Imager first run: ask MeteorCloud for a device token.
#
# Meteor Imager writes this file and .meteorenv (METEOR_DOMAIN, METEOR_API_KEY)
# to the boot partition, and cloud-init runs it once on first boot:
#
#   1. Installs meteorcli if it is missing.
#   2. Saves the domain and enrollment key, and submits a token request
#      (meteorcli request-token --wait 0).
#   3. Once the request is sent, deletes this script and .meteorenv from the
#      boot partition, and starts meteorcli.service. The service waits for an
#      administrator to approve the device (also across reboots), then runs.
#
# If the request cannot be sent (no network, key refused), both files stay so
# the run can be repeated:  sudo bash /boot/firmware/meteor-firstrun.sh
# Log: /var/log/meteor-firstrun.log (and /var/log/cloud-init-output.log).
#
# Environment overrides (tests and debugging): METEOR_BOOT_DIR, METEORCLI_CONFIG_DIR,
# METEOR_INSTALL_URL, METEOR_FIRSTRUN_LOG, METEOR_RETRIES, METEOR_RETRY_SECONDS.
set -uo pipefail

# Everything runs from main(): bash has then read the whole file before this
# script deletes itself.
main() {
	# cloud-init may start us with a short PATH; meteorcli lands in /usr/local/bin.
	export PATH="${PATH:+${PATH}:}/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin"
	export HOME="${HOME:-/root}"
	local boot_dir="${METEOR_BOOT_DIR:-$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)}"
	SELF="${boot_dir}/meteor-firstrun.sh"
	ENV_FILE="${boot_dir}/.meteorenv"
	LOG_FILE="${METEOR_FIRSTRUN_LOG:-/var/log/meteor-firstrun.log}"
	export METEORCLI_CONFIG_DIR="${METEORCLI_CONFIG_DIR:-/etc/meteorcli}"
	INSTALL_URL="${METEOR_INSTALL_URL:-https://github.com/meteor-edge/meteor-cloud/releases/latest/download/install-meteorcli.sh}"
	RETRIES="${METEOR_RETRIES:-60}"
	RETRY_SECONDS="${METEOR_RETRY_SECONDS:-10}"
	FIRSTBOOT_UNIT="${METEOR_FIRSTBOOT_UNIT:-/etc/systemd/system/meteorcli-firstboot.service}"

	if [[ "${EUID}" -ne 0 && -z "${METEOR_FIRSTRUN_ALLOW_NONROOT:-}" ]]; then
		echo "meteor-firstrun: run as root: sudo bash $0" >&2
		return 1
	fi

	log "MeteorCloud first run (boot partition ${boot_dir})"

	if [[ -s "${METEORCLI_CONFIG_DIR}/device-token" ]]; then
		log "Already enrolled; nothing to request."
		remove_firstrun
		start_agent
		return 0
	fi

	if [[ -s "${METEORCLI_CONFIG_DIR}/claim-secret" ]]; then
		log "A token request was already sent; resuming."
		remove_firstrun
		start_agent
		return 0
	fi

	# A Meteor base image enrolls with its own meteorcli-firstboot.service, which
	# reads .meteorenv too. Two runs would race, so leave enrollment to it.
	if [[ -f "${FIRSTBOOT_UNIT}" ]]; then
		log "This image has MeteorCloud first boot (${FIRSTBOOT_UNIT}); it enrolls the device."
		rm -f "${SELF}"
		return 0
	fi

	read_meteorenv || return 1
	ensure_meteorcli || return 1
	request_token || return 1

	remove_firstrun
	start_agent
	log "Done. Approve the device in MeteorCloud (Devices, pending enrollment)."
}

log() {
	local line
	line="$(date -Is 2>/dev/null || date) meteor-firstrun: $*"
	echo "${line}"
	echo "${line}" >>"${LOG_FILE}" 2>/dev/null || true
}

# KEY=value lines as Meteor Imager writes them. The file is parsed, never sourced.
read_meteorenv() {
	METEOR_DOMAIN="" METEOR_API_KEY="" METEOR_NAME=""
	if [[ ! -r "${ENV_FILE}" ]]; then
		log "error: ${ENV_FILE} is missing; write the card again with an enrollment key."
		return 1
	fi
	local line key value
	while IFS= read -r line || [[ -n "${line}" ]]; do
		line="${line%$'\r'}"
		[[ "${line}" == *=* ]] || continue
		key="${line%%=*}"
		value="${line#*=}"
		case "${key}" in
			METEOR_DOMAIN) METEOR_DOMAIN="${value}" ;;
			METEOR_API_KEY) METEOR_API_KEY="${value}" ;;
			METEOR_NAME) METEOR_NAME="${value}" ;;
		esac
	done <"${ENV_FILE}"
	if [[ -z "${METEOR_DOMAIN}" || "${METEOR_API_KEY}" != key_* ]]; then
		log "error: ${ENV_FILE} needs METEOR_DOMAIN and METEOR_API_KEY=key_…"
		return 1
	fi
	METEOR_NAME="${METEOR_NAME:-$(hostname)}"
	log "Server ${METEOR_DOMAIN}, device name ${METEOR_NAME}, enrollment key set (not logged)."
}

ensure_meteorcli() {
	if command -v meteorcli >/dev/null 2>&1; then
		log "meteorcli is installed: $(command -v meteorcli) ($(meteorcli --version 2>/dev/null || echo unknown version))"
		return 0
	fi
	log "meteorcli is not installed; installing from ${INSTALL_URL}"
	local i installer
	installer="$(mktemp)" || return 1
	for ((i = 1; i <= RETRIES; i++)); do
		if curl -fsSL "${INSTALL_URL}" -o "${installer}" && bash "${installer}" --with-service; then
			if command -v meteorcli >/dev/null 2>&1; then
				rm -f "${installer}"
				log "meteorcli installed: $(meteorcli --version 2>/dev/null || echo unknown version)"
				return 0
			fi
		fi
		log "Install failed (${i}/${RETRIES}); the network may still be coming up. Retry in ${RETRY_SECONDS}s."
		sleep "${RETRY_SECONDS}"
	done
	rm -f "${installer}"
	log "error: could not install meteorcli."
	return 1
}

request_token() {
	if ! meteorcli config --domain "${METEOR_DOMAIN}" --api-key "${METEOR_API_KEY}" >/dev/null; then
		log "error: meteorcli config failed."
		return 1
	fi
	local i
	for ((i = 1; i <= RETRIES; i++)); do
		if meteorcli request-token --name "${METEOR_NAME}" --wait 0; then
			if [[ -s "${METEORCLI_CONFIG_DIR}/claim-secret" || -s "${METEORCLI_CONFIG_DIR}/device-token" ]]; then
				log "Token request sent."
				return 0
			fi
		fi
		log "Token request failed (${i}/${RETRIES}). Retry in ${RETRY_SECONDS}s."
		if ((i == 1)); then
			meteorcli test || true
		fi
		sleep "${RETRY_SECONDS}"
	done
	log "error: could not send the token request. Check the domain and key in ${ENV_FILE}, then run: sudo bash ${SELF}"
	return 1
}

# The request is sent: the script and the plain-text key on the boot partition
# (readable by anyone holding the card) are no longer needed. meteorcli keeps
# its own copy of the key, root-only, to resubmit a request that expires.
remove_firstrun() {
	rm -f "${SELF}" "${ENV_FILE}"
	sync
	log "Removed ${SELF} and ${ENV_FILE}."
}

start_agent() {
	if ! command -v systemctl >/dev/null 2>&1; then
		log "warning: no systemd; run 'meteorcli run' to wait for approval."
		return 0
	fi
	systemctl enable meteorcli.service >/dev/null 2>&1 || true
	# meteorcli releases from November 2026 wait for approval in `meteorcli run`.
	if COLUMNS=1000 meteorcli run --help 2>/dev/null | grep -q "waits for pending enrollment"; then
		systemctl restart meteorcli.service
		log "meteorcli.service is waiting for approval: journalctl -u meteorcli -f"
		return 0
	fi
	# Older meteorcli: wait in a transient unit (it does not survive a reboot).
	local dir="${METEORCLI_CONFIG_DIR}"
	systemd-run --unit=meteorcli-claim --collect \
		--setenv=METEORCLI_CONFIG_DIR="${dir}" --setenv=PATH="${PATH}" \
		/bin/sh -c "while [ ! -s '${dir}/device-token' ] && [ -s '${dir}/claim-secret' ]; do meteorcli claim --wait 300 || sleep 10; done; systemctl restart meteorcli.service" ||
		log "warning: could not start the approval wait; run 'sudo meteorcli claim' after approval."
	log "Waiting for approval in meteorcli-claim.service: journalctl -u meteorcli-claim -f"
}

main "$@"
exit $?
