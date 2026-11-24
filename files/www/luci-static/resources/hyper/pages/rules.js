import { t } from '../i18n.js';
// The rules page: the Hyper account's server lists and the administrator's own rules.
import { attach, hush, diagnosticMessage, setLabel, setRefs, learnListSets } from '../dom.js';
import { ids, muted, badge, field, input, choice, read, form, confirm, syncHyper, write } from './common.js';

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
// Where a rule sends its traffic, as one chip: a group's opens it on the proxy page.
function targetLink(u, id) {
    const text = '→ ' + routeName(u, id);
    if (!id || id.startsWith('@')) return u.h('span', { class: 'ow-target', 'data-target': id || '' }, text);
    const b = u.button(text, () => u.go('proxy', id), 'ow-target');
    b.title = t('在代理页查看');
    return b;
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
        const up = u.button('↑', () => move(i, i - 1), 'ow-icon-button'),
            down = u.button('↓', () => move(i, i + 1), 'ow-icon-button');
        up.setAttribute('aria-label', t('上移第 {0} 条规则', (i + 1)));
        down.setAttribute('aria-label', t('下移第 {0} 条规则', (i + 1)));
        up.disabled = i === 0;
        down.disabled = i === custom.length - 1;
        const detail = describeRule(r.match);
        // Moving a rule that fails to save says so under the rule.
        const line = u.feedback();
        const row = u.h(
            'div',
            { class: 'ow-rule-row' + (r.enabled === false ? ' is-off' : ''), draggable: 'true' },
            u.h('span', { class: 'ow-mono ow-rule-index' }, String(i + 1)),
            u.h(
                'div',
                { class: 'ow-rule-match' },
                u.h('div', { class: 'ow-rule-title' },
                    u.h('strong', {}, detail.map((x) => x.value).join(' · ')),
                    r.enabled === false ? badge(u, t('已停用')) : null),
                u.h('div', { class: 'ow-rule-meta' },
                    u.h('small', { class: 'ow-muted' }, detail.map((x) => x.type).join(' + ')),
                    targetLink(u, r.target),
                    r.note ? u.h('small', { class: 'ow-muted ow-rule-note' }, r.note) : null),
            ),
            u.h('div', { class: 'ow-rule-actions' },
                up,
                down,
                u.button(t('编辑'), () => editRule(u, custom, i, source, local), 'ow-link')),
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
    const [customResponse, groups, initial, catalog] = await Promise.all([
        u.rpc('custom_rules'),
        ready ? allGroups(u, source) : [],
        ready ? read(u, 'rules', { source, limit: 1 }) : { local: false },
        read(u, 'catalog').catch(() => ({ items: [] })),
    ]);
    learnListSets(initial.labels);
    const name = u.name,
        labels = new Map([...groups, ...(u.status.intent?.policy?.groups || [])].map((g) => [g.id, g.name]));
    u = { ...u, ruleGroups: groups, name: (id) => labels.get(id) || name(id) };
    // The account's lists beside the administrator's own rules.
    return u.h(
        'div',
        { class: 'ow-dashboard ow-rules' },
        u.h('div', { class: 'ow-side-stack ow-rules-builtin' }, catalogCard(u, catalog.items || [], primary)),
        u.h(
            'div',
            { class: 'ow-side-stack ow-rules-custom' },
            customCard(u, customResponse.items || [], source, initial.local),
        ),
    );
}
const policyName = { DIRECT: t('直连'), PROXY: t('代理') };
function catalogCard(u, items, primary) {
    const sync = u.button(t('同步服务端规则'), () => syncHyper(u, line), 'ow-link');
    sync.disabled = !primary?.ready;
    const box = u.h('section', { class: 'ow-card' }, u.h('div', { class: 'ow-row' }, u.h('h2', {}, t('服务端规则')), sync));
    const line = u.feedback(box, 'catalog');
    if (!items.length) {
        box.append(muted(u, t('同步节点后，这里会列出服务端下发的分流规则')), line);
        return box;
    }
    const list = u.h('ul', { class: 'ow-catalog' });
    for (const item of items) {
        const label = item.key + (item.server_enabled ? '' : t('（服务端已停用）'));
        let on = !!item.enabled;
        // The overview's switch, so a list reads like any other setting.
        const toggle = u.button('', async () => {
            const wanted = !on;
            line.hidden = false;
            delete line.dataset.tone;
            line.textContent = (wanted ? t('正在启用 ') : t('正在停用 ')) + item.key + '…';
            try {
                await write(u, 'rule_toggle', { key: item.key, enabled: wanted });
                on = wanted;
                toggle.setAttribute('aria-checked', String(on));
                line.textContent = t('{0}，正在重新同步规则', ((wanted ? t('已启用 ') : t('已停用 ')) + item.key));
            } catch (e) {
                line.textContent = diagnosticMessage(e.message);
                line.dataset.tone = 'error';
            }
        }, 'ow-switch');
        toggle.setAttribute('role', 'switch');
        toggle.setAttribute('aria-label', label);
        toggle.setAttribute('aria-checked', String(on));
        toggle.disabled = !item.server_enabled;
        list.append(u.h('li', { class: 'ow-row' },
            u.h('div', { class: 'ow-label' },
                u.h('strong', {}, item.key),
                muted(u, [policyName[item.policy] || item.policy, item.entries ? t('{0} 条', item.entries) : '', item.stale ? t('本次未能下载，沿用上次') : ''].filter(Boolean).join(' · '))),
            toggle));
    }
    box.append(list, line);
    return box;
}
