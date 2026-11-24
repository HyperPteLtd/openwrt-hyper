import { t } from '../i18n.js';
// The rules page: custom rules and the rule sets of the active subscription.
import { attach, hush, diagnosticMessage, day, mainlandSets, isMainland, mainlandName, setLabel, setRefs, learnListSets } from '../dom.js';
import { ids, actions, muted, badge, field, input, choice, read, date, form, confirm, syncHyper, write } from './common.js';

const matchType = {
    domain: 'DOMAIN',
    domain_suffix: 'DOMAIN-SUFFIX',
    domain_keyword: 'DOMAIN-KEYWORD',
    ip_cidr: 'IP-CIDR',
    source_ip_cidr: 'SRC-IP',
    source_mac_address: 'SRC-MAC',
    network: 'NETWORK',
    port: 'PORT',
    port_range: 'PORT-RANGE',
    protocol: 'PROTOCOL',
    ip_version: 'IP-VERSION',
    ip_is_private: 'PRIVATE',
    rule_set: 'RULE-SET',
};
const editorTypes = [
    ['domain', t('域名')],
    ['domain_suffix', t('域名后缀')],
    ['domain_keyword', t('域名关键词')],
    ['ip_cidr', t('目标 IP 网段')],
    ['source_ip_cidr', t('来源 IP 网段')],
    ['network', 'TCP / UDP'],
    ['port', t('目标端口')],
    ['port_range', t('端口范围')],
    ['protocol', t('流量协议')],
    ['ip_version', t('IP 版本')],
];
const routeName = (u, target) =>
    ({ '@direct': t('直连'), '@reject': t('拦截'), '@global': t('全局'), '@default': t('默认代理') })[target] || u.name(target);
// A rule set a rule names reads as the pages show it: the plugin's mainland lists by their labels.
function setRef(key, value) {
    if (key === 'rule_set') return setLabel(value);
    if (key === 'clash') return setRefs(value);
    return value;
}
function describeRule(match) {
    return Object.entries(match || {}).map(([k, v]) => ({
        type: matchType[k] || k,
        value:
            k === 'ip_is_private'
                ? t('私有地址')
                : (Array.isArray(v) ? v : [v])
                      .map((value) => (typeof value === 'object' ? JSON.stringify(value) : setRef(k, String(value))))
                      .join(t('、')),
    }));
}
function targetLink(u, id) {
    if (!id || id === '@direct' || id === '@reject') return badge(u, routeName(u, id));
    return u.button(routeName(u, id) + ' ›', () => u.go('proxy', id));
}
async function allGroups(u, source) {
    const groups = [];
    for (let offset = 0; ; offset += 256) {
        if (u.signal?.aborted) throw new DOMException(t('页面已切换'), 'AbortError');
        const page = await read(u, 'groups', { source, limit: 256, offset });
        groups.push(...page.items);
        if (groups.length >= page.total || page.items.length < 256) return groups;
    }
}
function matchValue(key, typed) {
    const values = typed
        .split(/[,，\n]/)
        .map((v) => v.trim())
        .filter(Boolean);
    if (!values.length) throw Error(t('请输入匹配内容'));
    if (key === 'port' || key === 'ip_version') {
        const nums = values.map((v) => (/^\d+$/.test(v) ? Number(v) : NaN));
        if (nums.some((n) => !Number.isInteger(n) || (key === 'port' ? n < 1 || n > 65535 : n !== 4 && n !== 6)))
            throw Error(t('无效的端口或 IP 版本'));
        if (key === 'ip_version') {
            if (nums.length !== 1) throw Error(t('请选择一个 IP 版本'));
            return nums[0];
        }
        return nums;
    }
    if (key === 'network' && values.some((v) => !['tcp', 'udp'].includes(v.toLowerCase())))
        throw Error(t('网络类型必须为 tcp 或 udp'));
    return key === 'network' ? values.map((v) => v.toLowerCase()) : values;
}
async function editRule(u, custom, index, source, isLocal) {
    const old = custom[index],
        key = Object.keys(old?.match || { domain_suffix: '' })[0];
    const complex = old && (Object.keys(old.match).length !== 1 || !editorTypes.some(([k]) => k === key));
    const kind = choice(u, editorTypes, key, t('匹配类型'));
    const val = input(u, {
        required: true,
        placeholder: 'example.com',
        value: old ? [old.match[key]].flat().join(', ') : '',
    });
    const targets = [
        ['@direct', t('直连')],
        ['@reject', t('拦截')],
    ];
    if (isLocal) targets.push(['@default', t('默认代理')]);
    if (source && u.sources?.some((s) => s.source === source && s.ready))
        for (const g of u.ruleGroups || (await allGroups(u, source))) targets.push([g.id, g.name]);
    for (const g of u.status.intent?.policy?.groups || []) targets.push([g.id, g.name]);
    if (old && !targets.some(([id]) => id === old.target))
        targets.push([old.target, t('不可用 · {0}', routeName(u, old.target))]);
    const target = choice(u, targets, old?.target || '@direct', t('去向'));
    const note = input(u, { value: old?.note || '', maxlength: 256 }),
        enabled = input(u, { type: 'checkbox' });
    enabled.checked = old?.enabled !== false;
    const raw = u.h('textarea', { rows: 5, required: true, spellcheck: false, 'aria-label': t('匹配条件 JSON') });
    if (complex) raw.value = JSON.stringify(old.match, null, 2);
    const controls = complex
        ? [field(u, t('匹配条件 JSON'), raw)]
        : [field(u, t('类型'), kind), field(u, t('匹配内容（多个值以逗号分隔）'), val)];
    const save = async (list) => {
        await u.run('save_rules', { args: list, revision: u.status.intent.revision });
        await u.refresh();
    };
    const body = u.h('div', { class: 'ow-stack' }),
        dialog = u.dialog(old ? t('编辑规则') : t('添加自定义规则'), body);
    body.append(
        form(
            u,
            [...controls, field(u, t('去向'), target), field(u, t('说明'), note), field(u, t('启用规则'), enabled)],
            t('保存'),
            async () => {
                let match;
                if (complex) {
                    try {
                        match = JSON.parse(raw.value);
                    } catch {
                        throw Error(t('匹配条件必须是有效的 JSON 对象'));
                    }
                    if (
                        !match ||
                        Array.isArray(match) ||
                        typeof match !== 'object' ||
                        !Object.keys(match).length ||
                        Object.keys(match).some((k) => !editorTypes.some(([key]) => key === k))
                    )
                        throw Error(t('存在不支持的匹配条件'));
                } else {
                    match = { [kind.value]: matchValue(kind.value, val.value) };
                }
                const rule = {
                    ...old,
                    id: old?.id || 'rule-' + ids(),
                    match,
                    target: target.value,
                    note: note.value.trim(),
                    enabled: enabled.checked,
                };
                const next = custom.slice();
                if (old) next[index] = rule;
                else next.push(rule);
                await save(next);
                dialog.close();
            },
        ),
    );
    if (old)
        body.append(
            u.button(
                t('删除规则'),
                () =>
                    confirm(
                        u,
                        t('删除规则'),
                        muted(
                            u,
                            describeRule(old.match)
                                .map((x) => x.value)
                                .join(' · '),
                        ),
                        t('删除'),
                        async () => {
                            await save(custom.filter((_, i) => i !== index));
                            dialog.close();
                        },
                    ),
                'ow-danger',
            ),
        );
}
const limitRules = 50;
// The rules and lookups of the subscription name a mainland list by its tag.
async function setDetail(u, source, name, info) {
    const tag = isMainland(name) ? mainlandSets[name].tag : name;
    const list = u.h('div', { class: 'ow-rule-list' }, u.h('p', { class: 'ow-muted' }, t('正在读取…')));
    const query = u.h('input', { type: 'search', placeholder: t('域名或 IP，如 example.com'), 'aria-label': t('检查是否命中') }),
        answer = u.h('p', { class: 'ow-muted', role: 'status' });
    const check = u.button(t('检查'), async () => {
        const value = query.value.trim();
        if (!value) return;
        check.disabled = true;
        answer.textContent = t('正在检查…');
        try {
            const r = await read(u, 'set_match', { source, rule_set: tag, query: value });
            answer.textContent = value + (r.matched ? t(' 命中此规则集') : t(' 不在此规则集中'));
        } catch (e) {
            answer.textContent = diagnosticMessage(e.message);
        } finally {
            check.disabled = false;
        }
    });
    query.addEventListener('keydown', (e) => {
        if (e.key === 'Enter') {
            e.preventDefault();
            check.click();
        }
    });
    const lookup = u.h('div', { class: 'ow-stack' }, u.h('h3', {}, t('检查是否命中')), u.h('div', { class: 'ow-row' }, query, check), answer);
    // The router updates its mainland lists itself once a week.
    const china = u.status.china_lists;
    const facts = [
        [t('来源'), info.builtIn ? t('内置，每周自动更新') : t('订阅')],
        [t('条目'), info.builtIn ? '—' : t('{0} 条{1}', (info.count ?? '—'), (info.opaque ? t('（二进制格式，无法逐条列出）') : ''))],
        [t('更新时间'), info.builtIn ? (china ? day(china) : t('插件内置版本')) : date(info.primary?.updated_at)],
    ];
    u.dialog(
        setLabel(name),
        u.h(
            'div',
            { class: 'ow-stack' },
            u.h('dl', { class: 'ow-facts' }, facts.map(([k, v]) => [u.h('dt', {}, k), u.h('dd', {}, v)])),
            lookup,
            u.h('h3', {}, t('引用此规则集的规则')),
            list,
            u.button(t('查看命中的连接 ›'), () => u.go('connections', 'rule:' + name), 'ow-link'),
        ),
    );
    try {
        const rows = [];
        if (info.builtIn && info.local) rows.push({ match: { rule_set: name }, target: '@direct' });
        else if (source) {
            const data = await read(u, 'rules', { source, limit: limitRules, rule_set: tag });
            learnListSets(data.labels);
            data.items.forEach((r, i) => rows.push({ ...r, position: data.positions?.[i] }));
        }
        list.replaceChildren(
            ...(rows.length
                ? rows.map((r) => {
                      const detail = describeRule(r.match);
                      return u.h(
                          'div',
                          { class: 'ow-rule-row' },
                          r.position ? u.h('span', { class: 'ow-mono' }, '#' + r.position) : null,
                          u.h('span', {}, detail.map((x) => x.type + ' ' + x.value).join(' · ')),
                          targetLink(u, r.target),
                      );
                  })
                : [u.h('p', { class: 'ow-empty' }, t('没有规则引用此规则集'))]),
        );
        if (info.mode && info.mode !== 'smart')
            list.append(u.h('p', { class: 'ow-muted' }, t('当前不是智能分流模式，规则集暂未生效')));
    } catch (e) {
        list.replaceChildren(u.h('p', { class: 'ow-muted' }, diagnosticMessage(e.message)));
    }
}
// Where the loaded rules send each rule set's matches. The page reads only the first rules, so a
// set referenced further down has no known route here; its details list every rule that uses it.
function setRoutes(items) {
    const routes = new Map();
    for (const rule of items || [])
        for (const tag of new Set([rule.match?.rule_set, rule.rule_set].flat().filter(Boolean).map(mainlandName))) {
            if (!routes.has(tag)) routes.set(tag, []);
            if (rule.target && !routes.get(tag).includes(rule.target)) routes.get(tag).push(rule.target);
        }
    return routes;
}
// A route reads as text; a policy group links to its panel on the proxy page.
function setRoute(u, routes) {
    const shown = routes.slice(0, 2).map((id) =>
        !id || id === '@direct' || id === '@reject'
            ? u.h('span', {}, routeName(u, id))
            : u.button(routeName(u, id) + ' ›', () => u.go('proxy', id), 'ow-link'),
    );
    return [
        ...shown.flatMap((node, i) => (i ? [t('、'), node] : [node])),
        routes.length > 2 ? muted(u, t(' 等 {0} 个', routes.length)) : null,
    ];
}
let setCards = 0;
// One rule set: its name and kind, then entries, hits and route, then where to look further.
function setCard(u, name, info, hits, routes, open) {
    const heading = 'ow-set-' + ++setCards;
    const hit = hits?.[name]?.count;
    const stat = (label, value, cls) =>
        u.h('div', { class: cls }, u.h('dt', {}, label), u.h('dd', {}, value));
    const link = (text, handler) => {
        // Every card has these two; the set's name tells them apart.
        const b = u.button(text, handler, 'ow-link');
        b.setAttribute('aria-describedby', heading);
        return b;
    };
    return u.h(
        'li',
        { class: 'ow-set-card', 'data-origin': info.builtIn ? 'builtin' : 'subscription' },
        u.h(
            'div',
            { class: 'ow-set-title' },
            u.h('h3', { id: heading }, setLabel(name)),
            u.h(
                'span',
                { class: 'ow-set-tags' },
                badge(u, info.builtIn ? t('内置') : t('订阅')),
                info.opaque
                    ? u.h('span', { class: 'ow-badge ow-badge-plain', title: t('二进制格式，无法逐条列出') }, t('二进制'))
                    : null,
            ),
        ),
        u.h(
            'dl',
            { class: 'ow-set-stats' },
            stat(t('条目'), info.builtIn ? t('每周自动更新') : t('{0} 条', (info.count ?? '—'))),
            stat(t('命中连接'), hit ? t('{0} 次', hit) : '—', hit ? 'ow-set-hit' : null),
            routes?.length ? stat(t('去向'), setRoute(u, routes), 'ow-set-route') : null,
        ),
        actions(
            u,
            link(t('连接记录'), () => u.go('connections', 'rule:' + name)),
            link(t('详情'), open),
        ),
    );
}
// What the subscription or the plugin provides: its rule sets, one card each, and how often each was hit.
// A subscription's GEOSITE,CN and GEOIP,CN rules and a local profile's mainland routes use the
// plugin's mainland lists, which the core loads once each: one built-in card per list, under the
// name its connections and hits report.
function ruleSetCard(u, source, primary, initial, hits) {
    const mode = u.status.intent?.mode;
    const names = [
        ...new Set([...Object.keys(initial.sets || {}).map(mainlandName), ...(initial.local ? Object.keys(mainlandSets) : [])]),
    ];
    const routes = setRoutes(initial.items);
    const feedback = u.h('div', { class: 'ow-muted', role: 'status', hidden: true });
    const btn = u.button(t('同步服务端规则'), () => syncHyper(u, feedback));
    btn.disabled = !primary?.ready || initial.local || names.every(isMainland);
    const cards = names.map((name) => {
        const builtIn = isMainland(name),
            binary = !builtIn && !!initial.binary?.[name],
            // A file the router keeps; a text one (the account's lists) is not opaque.
            opaque = binary && initial.formats?.[name] !== 'text',
            count = binary ? initial.entries?.[name] : initial.sets[name];
        // A local profile sends the mainland lists direct, as their details say.
        const route = routes.get(name) || (builtIn && initial.local ? ['@direct'] : null);
        return setCard(u, name, { builtIn, binary, opaque, count }, hits, route, () =>
            setDetail(u, source, name, { builtIn, binary, opaque, count, primary, mode, local: initial.local }),
        );
    });
    return u.h(
        'section',
        { class: 'ow-card' },
        u.h(
            'div',
            { class: 'ow-row ow-toolbar' },
            u.h('div', { class: 'ow-set-heading' }, u.h('h2', {}, t('规则集')), names.length ? muted(u, t('{0} 个', names.length)) : null),
            actions(u, muted(u, !initial.local && primary?.updated_at ? t('更新于 {0}', date(primary.updated_at)) : ''), btn),
        ),
        muted(u, t('命中连接为本次启动以来的累计次数，清空连接记录时归零。')),
        feedback,
        names.length
            ? u.h('ul', { class: 'ow-set-grid', role: 'list' }, cards)
            : u.h('p', { class: 'ow-empty' }, t('当前来源没有规则集')),
    );
}
// What the user adds: custom rules, kept in their own order.
function customCard(u, custom, source, local) {
    const move = async (from, to) => {
        if (from === to || to < 0 || to >= custom.length) return;
        const list = custom.slice();
        list.splice(to, 0, list.splice(from, 1)[0]);
        await u.run('save_rules', { args: list, revision: u.status.intent.revision });
        await u.refresh();
    };
    const rows = u.h('div', { class: 'ow-rule-list' });
    let dragging = -1;
    for (const [i, r] of custom.entries()) {
        const up = u.button('↑', () => move(i, i - 1)),
            down = u.button('↓', () => move(i, i + 1));
        up.setAttribute('aria-label', t('上移第 {0} 条规则', (i + 1)));
        down.setAttribute('aria-label', t('下移第 {0} 条规则', (i + 1)));
        up.disabled = i === 0;
        down.disabled = i === custom.length - 1;
        const detail = describeRule(r.match);
        // Moving a rule that fails to save says so under the rule.
        const line = u.feedback();
        const row = u.h(
            'div',
            { class: 'ow-rule-row', draggable: 'true' },
            u.h('span', { class: 'ow-mono ow-rule-index' }, String(i + 1)),
            u.h(
                'div',
                { class: 'ow-rule-match' },
                u.h('strong', {}, detail.map((x) => x.value).join(' · ')),
                u.h('div', { class: 'ow-muted' }, detail.map((x) => x.type).join(' + ')),
                (r.note || r.enabled === false) &&
                    u.h(
                        'div',
                        { class: 'ow-muted ow-rule-note' },
                        r.note ? u.h('span', {}, r.note) : null,
                        r.enabled === false ? badge(u, t('已停用')) : null,
                    ),
            ),
            targetLink(u, r.target),
            actions(
                u,
                up,
                down,
                u.button(t('编辑'), () => editRule(u, custom, i, source, local)),
            ),
            line,
        );
        attach(row, line);
        row.addEventListener('dragstart', (e) => {
            dragging = i;
            e.dataTransfer.effectAllowed = 'move';
            e.dataTransfer.setData('text/plain', String(i));
        });
        row.addEventListener('dragend', () => {
            dragging = -1;
        });
        row.addEventListener('dragover', (e) => {
            if (dragging >= 0) e.preventDefault();
        });
        row.addEventListener('drop', async (e) => {
            e.preventDefault();
            const old = dragging;
            dragging = -1;
            if (old >= 0) {
                hush(line);
                try {
                    await move(old, i);
                } catch (err) {
                    if (err.name !== 'AbortError') u.report(row, err);
                }
            }
        });
        rows.append(row);
    }
    return u.h(
        'section',
        { class: 'ow-card' },
        u.h(
            'div',
            { class: 'ow-row ow-toolbar' },
            u.h('h2', {}, t('自定义规则')),
            u.button(t('＋ 添加规则'), () => editRule(u, custom, -1, source, local), 'ow-primary'),
        ),
        custom.length ? rows : u.h('p', { class: 'ow-empty' }, t('暂无自定义规则')),
    );
}
export async function rules(u) {
    const source = u.status.intent?.source,
        primary = (u.sources || []).find((s) => s.source === source),
        ready = primary?.ready;
    const emptyPage = { items: [], positions: [], sets: {}, binary: {}, total: 0, local: false };
    const [customResponse, groups, initial, hits, catalog] = await Promise.all([
        u.rpc('custom_rules'),
        ready ? allGroups(u, source) : [],
        // As many rules as one read returns, so more of the rule sets show where they lead.
        ready ? read(u, 'rules', { source, limit: 256 }) : emptyPage,
        u.rpc('rule_hits').catch(() => ({})),
        read(u, 'catalog').catch(() => ({ items: [] })),
    ]);
    learnListSets(initial.labels);
    const name = u.name,
        labels = new Map([...groups, ...(u.status.intent?.policy?.groups || [])].map((g) => [g.id, g.name]));
    u = { ...u, ruleGroups: groups, name: (id) => labels.get(id) || name(id) };
    // The overview's grid: the built-in rule sets take the wide column, as the status card does
    // there, and the user's own rules sit beside them, where the overview lists subscriptions.
    return u.h(
        'div',
        { class: 'ow-dashboard ow-rules' },
        u.h('div', { class: 'ow-side-stack ow-rules-builtin' }, catalogCard(u, catalog.items || []), ruleSetCard(u, source, primary, initial, hits)),
        u.h(
            'div',
            { class: 'ow-side-stack ow-rules-custom' },
            customCard(u, customResponse.items || [], source, initial.local),
        ),
    );
}

// The Hyper server's rule lists, in the server's order: each can be turned off on this router.
// A change takes effect by syncing the account again, which the router starts by itself.
const policyName = { DIRECT: t('直连'), PROXY: t('代理') };
function catalogCard(u, items) {
    const box = u.h('section', { class: 'ow-card' }, u.h('h2', {}, t('服务端规则')));
    const line = u.feedback(box, 'catalog');
    if (!items.length) {
        box.append(muted(u, t('同步节点后，这里会列出服务端下发的分流规则')), line);
        return box;
    }
    const list = u.h('ul', { class: 'ow-catalog' });
    for (const item of items) {
        const label = item.key + (item.server_enabled ? '' : t('（服务端已停用）'));
        const toggle = u.h('input', { type: 'checkbox', role: 'switch', 'aria-label': label });
        toggle.checked = !!item.enabled;
        toggle.disabled = !item.server_enabled;
        toggle.addEventListener('change', async () => {
            const wanted = toggle.checked;
            toggle.disabled = true;
            line.hidden = false;
            delete line.dataset.tone;
            line.textContent = (wanted ? t('正在启用 ') : t('正在停用 ')) + item.key + '…';
            try {
                await write(u, 'rule_toggle', { key: item.key, enabled: wanted });
                line.textContent = t('{0}，正在重新同步规则', ((wanted ? t('已启用 ') : t('已停用 ')) + item.key));
            } catch (e) {
                toggle.checked = !wanted;
                line.textContent = diagnosticMessage(e.message);
                line.dataset.tone = 'error';
            } finally {
                toggle.disabled = !item.server_enabled;
            }
        });
        list.append(u.h('li', { class: 'ow-row' },
            u.h('div', { class: 'ow-stack ow-grow' },
                u.h('strong', {}, item.key),
                muted(u, [policyName[item.policy] || item.policy, item.entries ? t('{0} 条', item.entries) : '', item.stale ? t('本次未能下载，沿用上次') : ''].filter(Boolean).join(' · '))),
            toggle));
    }
    box.append(list, line);
    return box;
}
