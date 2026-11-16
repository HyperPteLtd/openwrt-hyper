// SPDX-License-Identifier: GPL-2.0-only

export function updateDue(settings, checked, now, force) {
	if (!settings.auto_check && !settings.auto_update)
		return false;
	return !!force || !checked || now < checked || now - checked >= 86400;
};

/* Re-read preferences after the network request: turning off automatic updates
 * while checking must cancel installation, without cancelling a manual check. */
export function runAutoUpdate(readSettings, check, install) {
	const result = check();
	if (!result?.result || !result.update_available || !result.installable
		|| !readSettings().auto_update)
		return result;
	return install({ args: { force: false, automatic: true } });
};
