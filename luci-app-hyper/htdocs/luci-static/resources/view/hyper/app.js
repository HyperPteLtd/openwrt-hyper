/*
 * SPDX-License-Identifier: GPL-2.0-only
 *
 * Mounts the Hyper web app (static build in /luci-static/hyper/) inside LuCI.
 * The app runs in a same-origin iframe and calls ubus `luci.hyper` with the
 * LuCI session id it reads from the parent window (window.parent.L.env.sessionid).
 *
 * Copyright (C) 2026 Hyper Pte. Ltd.
 */

'use strict';
'require view';

return view.extend({
	render: function() {
		const frame = E('iframe', {
			/* index.html is not content-hashed (its assets are): never let an old copy survive an update */
			src: L.resource('../hyper/index.html') + '?_=' + Date.now(),
			title: 'Hyper',
			style: 'display:block;width:100%;min-height:calc(100vh - 120px);border:0;border-radius:12px;background:#000'
		});

		/* Grow with the app so LuCI's own page scrolls, not the iframe */
		window.addEventListener('message', function(ev) {
			if (ev.origin === window.location.origin && ev.data && ev.data.type === 'hyper:height')
				frame.style.height = Math.max(600, +ev.data.height || 0) + 'px';
		});

		return frame;
	},

	handleSave: null,
	handleSaveApply: null,
	handleReset: null
});
